// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventRecorder writes a domain event into the transactional outbox, as the
// LAST statement of the mutation's transaction (ADR 0003 section 2).
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// BranchGuard applies the payload branch rule (ADR 0007 section 2.3).
type BranchGuard interface {
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
}

// PriceEngine prices a stocked product line for a customer and quantity
// (ADR 0005 section 1: today's engine wrapped at the order boundary, its
// result converted to a scale 4 price once). When none is wired the product's
// base price is the list price.
type PriceEngine interface {
	PriceFor(ctx context.Context, customerID, productID uuid.UUID, basePrice httpx.Price, quantity httpx.Quantity, jobID *uuid.UUID) (httpx.Price, error)
}

// TaxProvider is the configured tax provider path behind the rate resolver
// (ADR 0005 section 3). When none is wired the resolver runs alone.
type TaxProvider interface {
	// Configured reports whether a provider answers (the Avalara path).
	Configured() bool
	PreviewTax(ctx context.Context, req *tax.TaxPreviewRequest) (*tax.TaxResult, error)
}

// ExposureGate is the narrow pre-ship gate the order module depends on,
// implemented by pricing.PostgresExposureChecker.
type ExposureGate interface {
	RequireClearForOrder(ctx context.Context, orderID uuid.UUID) error
}

// ExposureOverrider records an explicit owner override of the pre-ship gate.
type ExposureOverrider interface {
	OverrideForOrder(ctx context.Context, orderID uuid.UUID, notes, actor, role string) error
}

// Event types the module writes to the outbox (ADR 0005 section 12).
const (
	EventCreated      = "order.created"
	EventUpdated      = "order.updated"
	EventConfirmed    = "order.confirmed"
	EventHold         = "order.hold"
	EventHoldReleased = "order.hold_released"
	EventReopened     = "order.reopened"
	EventCancelled    = "order.cancelled"
	EventClosedShort  = "order.closed_short"
)

// Audit actions of the line price rules (ADR 0005 section 2.3).
const (
	auditLinePriceOverridden = "order.line_price_overridden"
	auditLineDiscounted      = "order.line_discounted"
)

type Service struct {
	repo      Repository
	events    EventRecorder
	tx        TxRunner
	branches  BranchGuard
	pricer    PriceEngine
	tax       TaxProvider
	exposure  ExposureGate
	inventory Inventory
	invoices  InvoiceWriter
	deposits  DepositApplier
	overrider ExposureOverrider
	audit     *audit.Logger
	logger    *slog.Logger
	now       func() time.Time
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo, logger: slog.Default(), now: time.Now}
}

func (s *Service) WithOutbox(events EventRecorder) *Service { s.events = events; return s }
func (s *Service) WithTxRunner(tx TxRunner) *Service        { s.tx = tx; return s }
func (s *Service) WithBranchGuard(g BranchGuard) *Service   { s.branches = g; return s }
func (s *Service) WithPriceEngine(p PriceEngine) *Service   { s.pricer = p; return s }
func (s *Service) WithTaxProvider(t TaxProvider) *Service   { s.tax = t; return s }
func (s *Service) WithAuditLog(l *audit.Logger) *Service    { s.audit = l; return s }
func (s *Service) WithExposureGate(gate ExposureGate, overrider ExposureOverrider) *Service {
	s.exposure, s.overrider = gate, overrider
	return s
}

// checkOrderBranch is the record branch rule (ADR 0007 section 2.3) for an
// order a path id addresses on the new write routes: the order's branch must
// be one the caller may target, else 403 forbidden naming id. It fails closed:
// a caller with no branch context is refused unless it marked itself a system
// caller with branchctx.WithSystem (the workers, the delivery adapter and the
// integration seam do).
func (s *Service) checkOrderBranch(ctx context.Context, o *Order) error {
	if s.branches == nil {
		return nil
	}
	err := s.branches.CheckPayloadBranch(ctx, o.BranchID)
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		return &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
			Message: "order is outside the branches this caller may target",
			Details: []httpx.FieldError{{Field: "id", Code: httpx.CodeForbidden, Message: "not a branch this caller may target"}}}
	}
	return err
}

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

// Precondition is the client's revision for an update or a transition
// (ADR 0001 section 11).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

func (p Precondition) missing() bool { return p.IfMatch == "" && p.Revision == nil }

func (p Precondition) check(current int64) error {
	return httpx.CheckRevision(current, p.IfMatch, p.Revision)
}

func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound(ErrNotFound.Error())
	}
	return err
}

// built is a priced, extended, taxed document body ready to store.
type built struct {
	order     Order
	overrides []audit.Entry
	discounts []audit.Entry
}

// build turns a Draft into a priced Order body: product defaults filled, the
// stocking unit rule enforced, prices resolved through the engine (an
// override kept beside the engine's answer), charge lines filled from their
// codes, kits exploded, every line extended once and the totals and tax
// resolved (ADR 0005 sections 2 and 3). The provider, when configured, is
// called here, outside any transaction.
func (s *Service) build(ctx context.Context, d *Draft, branchID uuid.UUID, actor string) (*built, error) {
	facts, err := s.repo.CustomerFacts(ctx, d.CustomerID)
	if err != nil {
		return nil, err
	}
	if !facts.Exists {
		return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
			Message: "a referenced record does not exist",
			Details: []httpx.FieldError{{Field: "customer_id", Message: "no such customer"}}}
	}

	o := Order{}
	o.ID = uuid.New()
	o.BranchID = branchID
	o.CustomerID, o.JobID = d.CustomerID, d.JobID
	o.DeliveryType = d.DeliveryType
	o.CustomerPO, o.OrderedByContactID = d.CustomerPO, d.OrderedByContactID
	o.SalespersonID = d.SalespersonID
	if o.SalespersonID == nil {
		o.SalespersonID = facts.SalespersonID
	}
	o.ScheduledDeliveryDate = d.ScheduledDeliveryDate
	o.Currency = facts.Currency
	o.Status = StatusDraft
	o.CreatedAt = httpx.TimestampOf(s.now().UTC())
	o.UpdatedAt = o.CreatedAt
	o.Revision = 1

	if d.DeliveryType == DeliveryDelivery {
		if d.ShipToID != nil {
			o.ShipToID = d.ShipToID
		} else if id, err := s.repo.DefaultShipToID(ctx, d.CustomerID); err != nil {
			return nil, err
		} else if id != nil {
			o.ShipToID = id
		}
	} else if d.ShipToID != nil {
		o.ShipToID = d.ShipToID
	}
	if o.ShipToID != nil {
		snap, _, err := s.repo.ShipTo(ctx, *o.ShipToID)
		if err != nil {
			return nil, err
		}
		o.ShipTo = snap
	}

	lines, audits, err := s.buildLines(ctx, d, o.ID, o.CustomerID, o.JobID, actor)
	if err != nil {
		return nil, err
	}
	o.Lines = lines
	b := &built{order: o, overrides: audits.overrides, discounts: audits.discounts}

	if err := s.applyTotalsAndTax(ctx, &b.order); err != nil {
		return nil, err
	}
	return b, nil
}

type lineAudits struct {
	overrides []audit.Entry
	discounts []audit.Entry
}

// buildLines prices a draft's lines. quoteSource marks lines that came from a
// quote (price_source QUOTE, the price kept exactly, no engine call).
func (s *Service) buildLines(ctx context.Context, d *Draft, orderID, customerID uuid.UUID, jobID *uuid.UUID, actor string) ([]OrderLine, lineAudits, error) {
	var out lineAudits
	refs, codes, kits, err := s.lookups(ctx, d.Lines)
	if err != nil {
		return nil, out, err
	}

	lines := make([]OrderLine, 0, len(d.Lines))
	for i, pl := range d.Lines {
		line := salesdoc.Line{
			Description: pl.Description,
			CreatedAt:   httpx.TimestampOf(s.now().UTC()),
		}
		if pl.ID != nil {
			line.ID = *pl.ID
		} else {
			line.ID = uuid.New()
		}
		switch pl.LineType {
		case salesdoc.LineText:
			line.LineType = salesdoc.LineText
			line.PriceSource = salesdoc.PriceSourceNone
			lines = append(lines, OrderLine{Line: line})
			continue

		case salesdoc.LineCharge:
			line.LineType = salesdoc.LineCharge
			code, ok := codes[pl.ChargeCode]
			if !ok {
				return nil, out, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
					Message: "a referenced record does not exist",
					Details: []httpx.FieldError{{Field: fmt.Sprintf("lines[%d].charge_code", i), Message: "no such charge code"}}}
			}
			if !code.IsActive {
				return nil, out, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
					Message: "a charge code is not active",
					Details: []httpx.FieldError{{Field: fmt.Sprintf("lines[%d].charge_code", i), Message: "is not active"}}}
			}
			line.ChargeCodeID = &code.ID
			codeText := code.Code
			line.ChargeCode = &codeText
			account := code.RevenueAccountCode
			line.RevenueAccountCode = &account
			if line.Description == "" {
				line.Description = code.Name
			}
			qty := pl.Quantity
			line.Quantity = &qty
			uom := pl.UOM
			if uom == "" {
				uom = "EA"
			}
			line.UOM, line.PriceUOM = &uom, &uom
			line.UOMQty, line.PriceUOMQty = salesdocPtr(salesdoc.One), salesdocPtr(salesdoc.One)
			price := httpx.Price(0)
			if pl.UnitPrice != nil {
				price = *pl.UnitPrice
			} else if code.DefaultUnitPrice != nil {
				price = *code.DefaultUnitPrice
			} else {
				return nil, out, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
					Message: "a charge line needs a price",
					Details: []httpx.FieldError{{Field: fmt.Sprintf("lines[%d].unit_price_ten_thousandths", i),
						Message: "is required when the charge code has no default price"}}}
			}
			line.UnitPrice = &price
			line.PriceSource = salesdoc.PriceSourceManual
			if pl.Taxable != nil {
				line.Taxable = *pl.Taxable
			} else {
				line.Taxable = code.Taxable
			}

		default: // product
			line.LineType = salesdoc.LineProduct
			var ref *salesdoc.ProductRef
			if pl.ProductID != nil {
				if r, ok := refs[pl.ProductID.String()]; ok {
					ref = &r
				} else {
					return nil, out, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
						Message: "a referenced record does not exist",
						Details: []httpx.FieldError{{Field: fmt.Sprintf("lines[%d].product_id", i), Message: "no such product"}}}
				}
			}
			if ref != nil {
				line.ProductID = &ref.ID
				if line.SKU == nil || *line.SKU == "" {
					sku := ref.SKU
					line.SKU = &sku
				}
				if line.Description == "" {
					line.Description = ref.Description
				}
				// A stocked line is sold in its product's stocking unit
				// (ADR 0005 section 1): any other unit is refused until
				// cycle 3's conversions land.
				stock := ref.UOMPrimary
				if pl.UOM != "" && pl.UOM != stock {
					return nil, out, &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
						Message: "a stocked line is sold in its product's stocking unit",
						Details: []httpx.FieldError{httpx.Blocker("unit_not_stock_unit",
							fmt.Sprintf("lines[%d] is sold in %s but the product stocks in %s", i, pl.UOM, stock))}}
				}
				uom := stock
				line.UOM = &uom
				line.Taxable = ref.Taxable
			} else {
				// a non stock line: the dealer does not carry it
				uom := pl.UOM
				line.UOM = &uom
				if pl.Taxable != nil {
					line.Taxable = *pl.Taxable
				} else {
					line.Taxable = true
				}
			}
			priceUOM := pl.PriceUOM
			if priceUOM == "" {
				priceUOM = *line.UOM
			}
			line.PriceUOM = &priceUOM
			// The pair rules of ADR 0001 7a run on the RESOLVED units: the
			// parse sees only what the request named, and a stocked line's
			// unit is filled from the product just above.
			if err := checkResolvedPair(i, *line.UOM, priceUOM, pl.UOMQty, pl.PriceUOMQty); err != nil {
				return nil, out, err
			}
			if pl.UOMQty == 0 && pl.PriceUOMQty == 0 {
				line.UOMQty, line.PriceUOMQty = salesdocPtr(salesdoc.One), salesdocPtr(salesdoc.One)
			} else {
				line.UOMQty, line.PriceUOMQty = salesdocPtr(pl.UOMQty), salesdocPtr(pl.PriceUOMQty)
			}
			qty := pl.Quantity
			line.Quantity = &qty

			// The price. Absent: the engine's answer, stored as both the
			// priced and the charged price. Present: an override beside the
			// engine's answer, stored as PRICE_LIST when the two agree
			// (ADR 0005 section 2.3).
			var enginePrice httpx.Price
			if ref != nil {
				enginePrice, err = s.enginePrice(ctx, customerID, ref, pl.Quantity, jobID)
				if err != nil {
					return nil, out, err
				}
			}
			if pl.UnitPrice == nil {
				line.UnitPrice = &enginePrice
				line.PricedUnitPrice = &enginePrice
				line.PriceSource = salesdoc.PriceSourceList
			} else {
				p := *pl.UnitPrice
				line.UnitPrice = &p
				if ref != nil {
					line.PricedUnitPrice = &enginePrice
					if p == enginePrice {
						line.PriceSource = salesdoc.PriceSourceList
					} else {
						line.PriceSource = salesdoc.PriceSourceOverride
						reason := pl.OverrideReason
						line.OverrideReason = &reason
						line.PriceAdjustedBy = actorString(actor)
						out.overrides = append(out.overrides, audit.Entry{
							Action: auditLinePriceOverridden, EntityType: "order", EntityID: orderID, UserID: actor,
							Changes: map[string]any{"line_id": line.ID, "product_id": line.ProductID,
								"engine_price_ten_thousandths":   int64(enginePrice),
								"override_price_ten_thousandths": int64(p), "reason": pl.OverrideReason}})
					}
				} else {
					line.PriceSource = salesdoc.PriceSourceManual
				}
			}

			line.IsSpecialOrder = pl.IsSpecialOrder
			line.VendorID = pl.VendorID
			if pl.SpecialOrderCost != nil {
				cost := *pl.SpecialOrderCost
				line.SpecialOrderCost = &cost
			}
		}

		// The discount, on any priced line a discount may ride (never a text
		// line; the parse refused that).
		if pl.DiscountPercent != nil {
			pct := *pl.DiscountPercent
			line.DiscountPercent = &pct
			line.DiscountReason = &pl.DiscountReason
			line.PriceAdjustedBy = actorString(actor)
		} else if pl.DiscountAmount != nil {
			amt := *pl.DiscountAmount
			line.DiscountAmount = &amt
			line.DiscountReason = &pl.DiscountReason
			line.PriceAdjustedBy = actorString(actor)
		}
		if line.DiscountPercent != nil || line.DiscountAmount != nil {
			out.discounts = append(out.discounts, audit.Entry{
				Action: auditLineDiscounted, EntityType: "order", EntityID: orderID, UserID: actor,
				Changes: map[string]any{"line_id": line.ID, "product_id": line.ProductID,
					"discount_percent": pl.DiscountPercent, "discount_cents": pl.DiscountAmount,
					"reason": pl.DiscountReason}})
		}
		lines = append(lines, OrderLine{Line: line})
	}

	// Kits explode (ADR 0005 section 2.6): the kit line priced as a whole,
	// one component line per component at zero.
	exploded, err := salesdoc.Explode(plainLines(lines), kits, refs)
	if err != nil {
		return nil, out, err
	}
	outLines := make([]OrderLine, 0, len(exploded))
	for _, l := range exploded {
		outLines = append(outLines, OrderLine{Line: l})
	}
	// Extend every line once; an amount discount past the extension names its
	// line.
	// The error names the line the REQUEST sent: a kit before it adds
	// component lines in front, so the exploded position is not the index.
	requestIndex := make(map[uuid.UUID]int, len(lines))
	for i := range lines {
		requestIndex[lines[i].ID] = i
	}
	for i := range outLines {
		if err := salesdoc.ExtendLine(&outLines[i].Line); err != nil {
			var he *httpx.Error
			if errors.As(err, &he) {
				at, ok := requestIndex[outLines[i].ID]
				if !ok {
					at = i
				}
				for j := range he.Details {
					if he.Details[j].Field == "lines.discount_cents" {
						he.Details[j].Field = fmt.Sprintf("lines[%d].discount_cents", at)
					}
				}
			}
			return nil, out, err
		}
	}
	return outLines, out, nil
}

// checkResolvedPair holds a line's conversion pair to the rules of ADR 0001
// section 7a once both units are known: equal units need a pair of 1 and 1
// (or none), and a different price unit needs a pair. A zero pair side means
// the request sent none.
func checkResolvedPair(i int, uom, priceUOM string, uomQty, priceUOMQty httpx.Quantity) error {
	field := fmt.Sprintf("lines[%d].uom_qty", i)
	sent := uomQty != 0 || priceUOMQty != 0
	var msg string
	switch {
	case uom == priceUOM && sent && (uomQty != salesdoc.One || priceUOMQty != salesdoc.One):
		msg = "must be 1 when price_uom equals uom: the units agree, so the pair is 1 and 1"
	case uom != priceUOM && !sent:
		msg = "is required when price_uom differs from uom: send uom_qty and price_uom_qty"
	default:
		return nil
	}
	return &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
		Message: "one or more fields failed validation",
		Details: []httpx.FieldError{{Field: field, Message: msg}}}
}

func actorString(actor string) *string {
	if actor == "" {
		return nil
	}
	return &actor
}

func salesdocPtr(q httpx.Quantity) *httpx.Quantity { return &q }

// enginePrice asks the wrapped pricing engine, converting its answer to a
// scale 4 price once (ADR 0005 section 1); with no engine wired the product's
// base price is the list price.
func (s *Service) enginePrice(ctx context.Context, customerID uuid.UUID, ref *salesdoc.ProductRef, qty httpx.Quantity, jobID *uuid.UUID) (httpx.Price, error) {
	if s.pricer == nil {
		return ref.BasePrice, nil
	}
	return s.pricer.PriceFor(ctx, customerID, ref.ID, ref.BasePrice, qty, jobID)
}

func plainLines(lines []OrderLine) []salesdoc.Line {
	out := make([]salesdoc.Line, 0, len(lines))
	for i := range lines {
		out = append(out, lines[i].Line)
	}
	return out
}

// lookups reads every product, charge code and kit definition the draft's
// lines name, in three queries.
func (s *Service) lookups(ctx context.Context, parsed []salesdoc.ParsedLine) (map[string]salesdoc.ProductRef, map[string]salesdoc.ChargeCode, map[string][]salesdoc.KitComponent, error) {
	var productIDs []uuid.UUID
	var kitIDs []uuid.UUID
	var codes []string
	for _, pl := range parsed {
		if pl.ProductID != nil {
			productIDs = append(productIDs, *pl.ProductID)
			kitIDs = append(kitIDs, *pl.ProductID)
		}
		if pl.ChargeCode != "" {
			codes = append(codes, pl.ChargeCode)
		}
	}
	refs, err := s.repo.LookupProducts(ctx, productIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	kits, err := s.repo.LookupKitComponents(ctx, kitIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	// A kit's components price, cost and stock like any product: read them
	// into the same map the lines default from.
	var componentIDs []uuid.UUID
	for kitID, comps := range kits {
		if _, ok := refs[kitID]; !ok {
			continue
		}
		for _, c := range comps {
			if _, seen := refs[c.ComponentProductID.String()]; !seen {
				componentIDs = append(componentIDs, c.ComponentProductID)
			}
		}
	}
	if len(componentIDs) > 0 {
		componentRefs, err := s.repo.LookupProducts(ctx, componentIDs)
		if err != nil {
			return nil, nil, nil, err
		}
		for k, v := range componentRefs {
			refs[k] = v
		}
	}
	codeMap, err := s.repo.LookupChargeCodes(ctx, codes)
	if err != nil {
		return nil, nil, nil, err
	}
	return refs, codeMap, kits, nil
}

// applyTotalsAndTax sums the lines and resolves the tax (ADR 0005 section 3):
// exemption first, then the provider when one is configured (an HTTP call,
// so this runs outside any transaction), then the ship-to or branch rate.
// A provider failure answers 503 before any row is written, never a silent
// fallback to a rate.
func (s *Service) applyTotalsAndTax(ctx context.Context, o *Order) error {
	totals := salesdoc.SumTotals(plainLines(o.Lines))
	exempt, err := s.repo.CustomerExempt(ctx, o.CustomerID)
	if err != nil {
		return err
	}
	o.SubtotalCents = totals.SubtotalCents

	if !exempt && s.tax != nil && s.tax.Configured() {
		result, err := s.tax.PreviewTax(ctx, taxRequest(o, totals.TaxableCents))
		if err != nil {
			return &httpx.Error{Status: http.StatusServiceUnavailable, Code: httpx.CodeUnavailable,
				Message: "the tax provider did not answer: the order is not written"}
		}
		o.TaxCents = httpx.Cents(result.TotalTax)
		o.TaxRatePercent = nil
		o.TaxSource = salesdoc.TaxSourceProvider
	} else if err := s.applyRateTax(ctx, o); err != nil {
		return err
	}
	o.TotalCents = o.SubtotalCents + o.TaxCents
	return nil
}

// applyRateTax resolves the tax by rate through the resolver (ADR 0005
// section 3): exemption, the delivery ship-to's rate, the branch's rate,
// then the refusal. Every read goes through the context's executor, so this
// runs as well inside a transaction as out.
func (s *Service) applyRateTax(ctx context.Context, o *Order) error {
	totals := salesdoc.SumTotals(plainLines(o.Lines))
	exempt, err := s.repo.CustomerExempt(ctx, o.CustomerID)
	if err != nil {
		return err
	}
	inputs, err := s.taxInputs(ctx, o, exempt)
	if err != nil {
		return err
	}
	resolved, err := salesdoc.ResolveTax(inputs)
	if err != nil {
		if errors.Is(err, salesdoc.ErrTaxRateNotConfigured) {
			return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
				Message: "no tax rate is configured for this order's branch",
				Details: []httpx.FieldError{httpx.Blocker("tax_rate_not_configured",
					"set the branch's default tax rate (or the ship-to's rate) before confirming or saving the order")}}
		}
		return err
	}
	o.SubtotalCents = totals.SubtotalCents
	pct, err := RateToPercent(*resolved.Rate)
	if err != nil {
		return err
	}
	o.TaxRatePercent = &pct
	o.TaxSource = resolved.Source
	o.TaxExempt = resolved.Source == salesdoc.TaxSourceExempt
	scaled, _, err := salesdoc.ParseTaxRate(*resolved.Rate)
	if err != nil {
		return err
	}
	o.TaxCents = salesdoc.TaxAt(totals.TaxableCents, scaled)
	o.TotalCents = o.SubtotalCents + o.TaxCents
	return nil
}

func (s *Service) taxInputs(ctx context.Context, o *Order, exempt bool) (salesdoc.TaxInputs, error) {
	in := salesdoc.TaxInputs{Exempt: exempt, Delivery: o.DeliveryType == DeliveryDelivery}
	if in.Delivery && o.ShipToID != nil {
		_, rate, err := s.repo.ShipTo(ctx, *o.ShipToID)
		if err != nil {
			return in, err
		}
		in.ShipToRate = rate
	}
	branchRate, err := s.repo.BranchTaxRate(ctx, o.BranchID)
	if err != nil {
		return in, err
	}
	in.BranchRate = branchRate
	return in, nil
}

// taxRequest builds the provider's preview: the document's taxable lines
// (ADR 0005 section 3).
func taxRequest(o *Order, taxableCents httpx.Cents) *tax.TaxPreviewRequest {
	req := &tax.TaxPreviewRequest{
		DocumentType: "SalesInvoice",
		RateHint:     0,
	}
	customerID := o.CustomerID
	req.CustomerID = &customerID
	n := 0
	for i := range o.Lines {
		l := &o.Lines[i]
		if l.LineType == salesdoc.LineText || !l.Taxable || l.LineTotal == nil {
			continue
		}
		n++
		req.Lines = append(req.Lines, tax.TaxLineInput{
			LineNumber:  n,
			ItemCode:    derefStr(l.SKU),
			Description: l.Description,
			Quantity:    float64(derefQ(l.Quantity)) / 10000,
			Amount:      int64(*l.LineTotal),
		})
	}
	return req
}

func derefQ(q *httpx.Quantity) httpx.Quantity {
	if q == nil {
		return 0
	}
	return *q
}

// Create prices, taxes and stores a draft order, writing order.created as
// the transaction's last statement.
func (s *Service) Create(ctx context.Context, d *Draft, actor string) (*Order, error) {
	if s.branches != nil && d.BranchID != nil {
		if err := s.branches.CheckPayloadBranch(ctx, *d.BranchID); err != nil {
			if errors.Is(err, middleware.ErrPayloadBranchRefused) {
				return nil, &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
					Message: "branch_id is outside the branches this caller may target",
					Details: []httpx.FieldError{{Field: "branch_id", Code: httpx.CodeForbidden, Message: "not a branch this caller may target"}}}
			}
			return nil, err
		}
	}
	var out *Order
	err := s.inTx(ctx, func(ctx context.Context) error {
		o, ev, err := s.createCore(ctx, d, actor)
		if err != nil {
			return err
		}
		out = o
		return s.writeEvent(ctx, ev)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// createCore is Create's in-transaction core (ADR 0007 section 4.3): it
// prices, taxes and stores the draft order and returns the order.created
// event instead of writing it, so the caller writes it as its transaction's
// last statement. The entity route writes it itself; a draft promotion
// writes it beside draft.promoted.
func (s *Service) createCore(ctx context.Context, d *Draft, actor string) (*Order, *outbox.Event, error) {
	// The order's branch: the body's, the caller's context, then the
	// deployment default (the same fallback the insert always gave raw
	// writers). The tax resolver needs it before the write.
	branchID, err := s.branchFor(ctx, d.BranchID)
	if err != nil {
		return nil, nil, err
	}
	b, err := s.build(ctx, d, branchID, actor)
	if err != nil {
		return nil, nil, err
	}
	number, err := s.repo.NextNumber(ctx)
	if err != nil {
		return nil, nil, err
	}
	b.order.Number = number
	if err := s.writeLineAudits(ctx, b); err != nil {
		return nil, nil, err
	}
	if err := s.repo.InsertOrder(ctx, &b.order); err != nil {
		return nil, nil, err
	}
	out, err := s.repo.GetOrder(ctx, b.order.ID)
	if err != nil {
		return nil, nil, err
	}
	ev, err := s.outboxEventFor(out, EventCreated, "")
	if err != nil {
		return nil, nil, err
	}
	return out, ev, nil
}

// branchFor resolves the order's branch: the body's, the caller's context,
// then the deployment default.
func (s *Service) branchFor(ctx context.Context, branch *uuid.UUID) (uuid.UUID, error) {
	if branch != nil && *branch != uuid.Nil {
		return *branch, nil
	}
	if bid := middleware.BranchIDForQuery(ctx); bid != nil {
		return *bid, nil
	}
	return s.repo.DefaultBranchID(ctx)
}

func (s *Service) writeLineAudits(ctx context.Context, b *built) error {
	if s.audit == nil {
		return nil
	}
	for _, e := range append(append([]audit.Entry{}, b.overrides...), b.discounts...) {
		if err := s.audit.Log(ctx, e); err != nil {
			return fmt.Errorf("failed to write the line price audit row: %w", err)
		}
	}
	return nil
}

// Update replaces a draft order's header and lines on the client's revision
// (the recipe's edit rule: any other status is a 409 with order_not_draft).
func (s *Service) Update(ctx context.Context, id uuid.UUID, d *Draft, pre Precondition, actor string) (*Order, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Order
	err := s.inTx(ctx, func(ctx context.Context) error {
		o, ev, err := s.updateCore(ctx, id, d, pre, actor)
		if err != nil {
			return err
		}
		out = o
		return s.writeEvent(ctx, ev)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// updateCore is Update's in-transaction core (ADR 0007 section 4.3): it
// locks the order row, checks the precondition and the draft status,
// replaces the draft and returns the order.updated event instead of writing
// it, so the caller writes it as its transaction's last statement. The
// entity route writes it itself; an edit draft's promotion writes it beside
// draft.promoted (unlike the quote, the order's update has an event).
func (s *Service) updateCore(ctx context.Context, id uuid.UUID, d *Draft, pre Precondition, actor string) (*Order, *outbox.Event, error) {
	// An edit keeps the order's branch: read it first, so the rebuilt tax
	// estimate resolves against the same branch.
	cur, err := s.repo.GetOrder(ctx, id)
	if err != nil {
		return nil, nil, notFound(err)
	}
	b, err := s.build(ctx, d, cur.BranchID, actor)
	if err != nil {
		return nil, nil, err
	}
	if err := s.repo.LockOrder(ctx, id); err != nil {
		return nil, nil, notFound(err)
	}
	if cur, err = s.repo.GetOrder(ctx, id); err != nil {
		return nil, nil, notFound(err)
	}
	if err := pre.check(cur.Revision); err != nil {
		return nil, nil, err
	}
	if cur.Status != StatusDraft {
		return nil, nil, &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
			Message: "only draft orders can be edited",
			Details: []httpx.FieldError{httpx.Blocker("order_not_draft", "the order is "+cur.Status.Status())}}
	}
	b.order.ID = cur.ID
	b.order.Number = cur.Number
	b.order.BranchID = cur.BranchID
	b.order.QuoteID = cur.QuoteID
	b.order.CreatedAt = cur.CreatedAt
	b.order.Revision = cur.Revision
	if err := s.writeLineAudits(ctx, b); err != nil {
		return nil, nil, err
	}
	if err := s.repo.ReplaceDraft(ctx, &b.order); err != nil {
		return nil, nil, err
	}
	out, err := s.repo.GetOrder(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	ev, err := s.outboxEventFor(out, EventUpdated, "")
	if err != nil {
		return nil, nil, err
	}
	return out, ev, nil
}

func (s *Service) GetOrder(ctx context.Context, id uuid.UUID) (*Order, error) {
	o, err := s.repo.GetOrder(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return o, nil
}

// NumberPattern is the order's document number pattern, from the entity's
// prefix and pad (ADR 0007 section 7): reads accept it in the {id} slot.
var NumberPattern = regexp.MustCompile(`^SO-[0-9]{6,}$`)

// ResolveRecordID parses a record URL's {id} slot: a UUID first, then the
// entity's number pattern. A well formed number of another entity, or
// anything else, is a 400 naming id; a value that names no visible row is
// the caller's 404.
func ResolveRecordID(raw string) (uuid.UUID, string, error) {
	if id, err := uuid.Parse(raw); err == nil {
		return id, "", nil
	}
	if NumberPattern.MatchString(raw) {
		return uuid.Nil, raw, nil
	}
	return uuid.Nil, "", httpx.BadRequest("invalid order id",
		httpx.FieldError{Field: "id", Message: "must be a UUID or an order number such as SO-000123"})
}

// GetOrderByIDOrNumber reads an order by its UUID or its document number
// (section 7): both spellings answer exactly the same body, no redirect.
func (s *Service) GetOrderByIDOrNumber(ctx context.Context, raw string) (*Order, error) {
	id, number, err := ResolveRecordID(raw)
	if err != nil {
		return nil, err
	}
	if number != "" {
		o, err := s.repo.GetOrderByNumber(ctx, number)
		if err != nil {
			return nil, notFound(err)
		}
		return o, nil
	}
	return s.GetOrder(ctx, id)
}

// ListOrders returns one page: up to f.Limit rows and whether more follow.
func (s *Service) ListOrders(ctx context.Context, f ListFilter, wantTotal bool) (items []OrderSummary, hasMore bool, total *int64, err error) {
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.ListOrders(ctx, f)
	if err != nil {
		return nil, false, nil, err
	}
	if len(rows) > limit {
		rows, hasMore = rows[:limit], true
	}
	if wantTotal {
		n, err := s.repo.CountOrders(ctx, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return rows, hasMore, total, nil
}

// TransitionBody carries a transition's own fields (ADR 0005 section 5.2).
type TransitionBody struct {
	Reason   string
	HoldNote string
	Actor    string
	// Role is the caller's role; empty when the auth chain set none (an in
	// process caller, a machine key, dev mode), which the release admits.
	Role string
}

// releaseRoles are the roles that may release a hold (ADR 0005 section 5.2).
var releaseRoles = map[string]bool{"admin": true, "owner": true, "finance": true}

// Transition moves an order along its lifecycle on the client's revision
// (ADR 0005 section 5.2's table). Anything outside the table is 409
// invalid_state_transition.
func (s *Service) Transition(ctx context.Context, id uuid.UUID, to OrderStatus, pre Precondition, body TransitionBody) (*Order, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	return s.transition(ctx, id, to, &pre, body)
}

// ConfirmInProcess is the confirm transition for in process callers (the
// integration seam): the same rules, the same events, no client revision.
func (s *Service) ConfirmInProcess(ctx context.Context, id uuid.UUID) (*Order, error) {
	return s.transition(ctx, id, StatusConfirmed, nil, TransitionBody{})
}

// CancelInProcess is the cancel transition for in process callers (the
// portal): the same rules, the same events, no client revision.
func (s *Service) CancelInProcess(ctx context.Context, id uuid.UUID, reason string) (*Order, error) {
	return s.transition(ctx, id, StatusCancelled, nil, TransitionBody{Reason: reason})
}

// providerTax is a provider answer minted before the transaction opened,
// with the fingerprint of the lines it priced: inside the transaction the
// confirm verifies the lines it actually confirms equal what was priced, and
// a difference answers 409 tax_quote_stale (ADR 0005 section 3).
type providerTax struct {
	taxCents    httpx.Cents
	fingerprint string
}

func (s *Service) transition(ctx context.Context, id uuid.UUID, to OrderStatus, pre *Precondition, body TransitionBody) (*Order, error) {
	// A confirm with a configured provider prices the tax BEFORE the
	// transaction opens: the provider is an HTTP call and never runs inside a
	// transaction that holds row locks.
	var priced *providerTax
	if to == StatusConfirmed && s.tax != nil && s.tax.Configured() {
		if cur, err := s.repo.GetOrder(ctx, id); err == nil {
			exempt, err := s.repo.CustomerExempt(ctx, cur.CustomerID)
			if err != nil {
				return nil, err
			}
			if !exempt {
				totals := salesdoc.SumTotals(plainLines(cur.Lines))
				result, err := s.tax.PreviewTax(ctx, taxRequest(cur, totals.TaxableCents))
				if err != nil {
					return nil, &httpx.Error{Status: http.StatusServiceUnavailable, Code: httpx.CodeUnavailable,
						Message: "the tax provider did not answer: the order is not confirmed"}
				}
				priced = &providerTax{
					taxCents:    httpx.Cents(result.TotalTax),
					fingerprint: linesFingerprint(cur),
				}
			}
		}
	}

	var out *Order
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockOrder(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetOrder(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if pre != nil {
			if err := pre.check(cur.Revision); err != nil {
				return err
			}
		}
		if err := allowedTransition(cur.Status, to); err != nil {
			return err
		}
		if _, err := s.applyTransition(ctx, cur, to, body, priced); err != nil {
			return err
		}
		if out, err = s.repo.GetOrder(ctx, id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// linesFingerprint captures everything the provider priced: the lines in
// order, their types, products, skus, descriptions, charge codes, units,
// quantities, prices, discounts, taxable flags and extensions, and the ship-to the rates came from. A line's own id is not
// part of it: a quote conversion builds its lines twice (before the
// transaction and inside it), each build minting fresh ids.
func linesFingerprint(o *Order) string {
	var b []byte
	b = append(b, []byte(derefUUID(o.ShipToID))...)
	for i := range o.Lines {
		l := &o.Lines[i]
		// The provider request carries each taxable line's sku and description,
		// and a non stock or charge line has no product to stand for them: so
		// the sku, the description, the charge code and the unit are part of
		// what was priced.
		b = append(b, []byte(fmt.Sprintf("|%d;%s;%s;%s;%s;%s;%s;%s;%t;%q;%q;%q;%q",
			i, derefUUID(l.ProductID), derefQtyStr(l.Quantity), derefPriceStr(l.UnitPrice),
			derefQtyStr(l.DiscountPercent), derefCentsStr(l.DiscountAmount), derefCentsStr(l.LineTotal), string(l.LineType),
			l.Taxable, derefStr(l.SKU), l.Description, derefStr(l.ChargeCode), derefStr(l.UOM)))...)
	}
	return string(b)
}

func derefUUID(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func derefQtyStr(q *httpx.Quantity) string {
	if q == nil {
		return ""
	}
	return q.DecimalString()
}

func derefPriceStr(p *httpx.Price) string {
	if p == nil {
		return ""
	}
	return p.DecimalString()
}

func derefCentsStr(c *httpx.Cents) string {
	if c == nil {
		return ""
	}
	return c.DecimalString()
}

// allowedTransition is the from/to table of ADR 0005 section 5.2.
func allowedTransition(from, to OrderStatus) error {
	allowed := map[OrderStatus][]OrderStatus{
		StatusDraft:       {StatusConfirmed, StatusCancelled},
		StatusOnHold:      {StatusConfirmed, StatusDraft, StatusCancelled},
		StatusConfirmed:   {StatusOnHold, StatusDraft, StatusCancelled, StatusFulfilled},
		StatusBackordered: {StatusOnHold, StatusDraft, StatusCancelled, StatusFulfilled},
		StatusFulfilled:   {},
		StatusCancelled:   {},
	}
	for _, t := range allowed[from] {
		if t == to {
			return nil
		}
	}
	return httpx.InvalidStateTransition(fmt.Sprintf("cannot transition from %s to %s", from.Status(), to.Status()))
}

// applyTransition runs one allowed transition's guards, effects and events,
// inside the caller's transaction with the order row locked. The guards are
// the blocker codes of ADR 0005 section 5.2's table.
func (s *Service) applyTransition(ctx context.Context, cur *Order, to OrderStatus, body TransitionBody, priced *providerTax) (*Order, error) {
	hasInvoices, err := s.repo.HasInvoices(ctx, cur.ID)
	if err != nil {
		return nil, err
	}
	// Anything shipped counts as billed for the has_fulfilments guards.
	hasInvoices = hasInvoices || anyFulfilled(cur)

	switch to {
	case StatusConfirmed:
		if cur.Status == StatusDraft {
			return s.confirm(ctx, cur, body, priced)
		}
		// on_hold to confirmed: the release (ADR 0005 section 5.2), held to
		// the finance roles under the order's lock. It skips the credit
		// check; the exposure gate and the tax rate still hold.
		if body.Role != "" && !releaseRoles[body.Role] {
			return nil, &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
				Message: "releasing a hold needs the admin, owner or finance role"}
		}
		if s.exposure != nil {
			if err := s.exposure.RequireClearForOrder(ctx, cur.ID); err != nil {
				return nil, err
			}
		}
		if err := s.refreshTax(ctx, cur, priced); err != nil {
			return nil, err
		}
		if err := s.repo.LockCustomerCredit(ctx, cur.CustomerID); err != nil {
			return nil, err
		}
		// Allocation: a release allocates when the order never was (a credit
		// hold lands before allocation); a manual hold kept its allocations.
		if neverAllocated(cur) || hasBackorders(cur) {
			if _, err := s.allocateLines(ctx, cur); err != nil {
				return nil, err
			}
		}
		cur.Status = deriveStatus(cur)
		if cur.Status == StatusFulfilled {
			cur.Status = StatusConfirmed
		}
		var heldReason, heldNote string
		if cur.HoldReason != nil {
			heldReason = string(*cur.HoldReason)
		}
		heldNote = derefStr(cur.HoldNote)
		cur.HoldReason, cur.HoldNote = nil, nil
		neverConfirmed := cur.ConfirmedAt == nil
		if neverConfirmed {
			// a release of a credit hold is the first confirm: its
			// timestamp is the release's, written with it
			now := httpx.TimestampOf(s.now().UTC())
			cur.ConfirmedAt = &now
		}
		if err := s.repo.SaveTransition(ctx, cur); err != nil {
			return nil, err
		}
		if err := s.auditTransition(ctx, cur, "order.hold_released", body.Actor, StatusOnHold, map[string]any{
			"hold_reason": heldReason, "hold_note": heldNote}); err != nil {
			return nil, err
		}
		if err := s.record(ctx, cur, EventHoldReleased, StatusOnHold.Status()); err != nil {
			return nil, err
		}
		// 5.2: order.confirmed only if never confirmed before, so a
		// consumer counting confirms counts a held order once.
		if neverConfirmed {
			if err := s.record(ctx, cur, EventConfirmed, StatusOnHold.Status()); err != nil {
				return nil, err
			}
		}
		if cur.Status == StatusBackordered {
			if err := s.record(ctx, cur, EventBackordered, StatusOnHold.Status()); err != nil {
				return nil, err
			}
		}
		return cur, nil

	case StatusOnHold:
		// A manual hold: a note is required, allocations are kept.
		if body.HoldNote == "" {
			return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
				Message: "a manual hold carries its note",
				Details: []httpx.FieldError{{Field: "hold_note", Message: "is required to hold an order"}}}
		}
		reason := HoldManual
		from := cur.Status
		cur.Status, cur.HoldReason, cur.HoldNote = StatusOnHold, &reason, &body.HoldNote
		if err := s.repo.SaveTransition(ctx, cur); err != nil {
			return nil, err
		}
		if err := s.auditTransition(ctx, cur, "order.hold", body.Actor, from, map[string]any{"hold_reason": "manual", "hold_note": body.HoldNote}); err != nil {
			return nil, err
		}
		return cur, s.record(ctx, cur, EventHold, from.Status())

	case StatusDraft:
		// The reopen: refused once anything was billed against the order.
		if hasInvoices {
			return nil, conflictBlocker("has_fulfilments", "the order has been billed: reverse its invoices, do not reopen it")
		}
		from := cur.Status
		if err := s.releaseAllocations(ctx, cur); err != nil {
			return nil, err
		}
		cur.Status = StatusDraft
		cur.HoldReason, cur.HoldNote = nil, nil
		if err := s.repo.SaveTransition(ctx, cur); err != nil {
			return nil, err
		}
		if err := s.auditTransition(ctx, cur, "order.reopened", body.Actor, from, nil); err != nil {
			return nil, err
		}
		return cur, s.record(ctx, cur, EventReopened, from.Status())

	case StatusCancelled:
		if body.Reason == "" {
			return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
				Message: "a cancellation carries its reason",
				Details: []httpx.FieldError{{Field: "reason", Message: "is required to cancel an order"}}}
		}
		if hasInvoices {
			return nil, conflictBlocker("has_fulfilments", "the order has been billed: reverse its invoices, do not cancel it")
		}
		from := cur.Status
		if err := s.releaseAllocations(ctx, cur); err != nil {
			return nil, err
		}
		cur.Status = StatusCancelled
		cur.HoldReason, cur.HoldNote = nil, nil
		if err := s.repo.SaveTransition(ctx, cur); err != nil {
			return nil, err
		}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				Action: "order.cancelled", EntityType: "order", EntityID: cur.ID, UserID: body.Actor,
				Changes: map[string]any{"previous_status": from.Status(), "reason": body.Reason,
					"total_cents": int64(cur.TotalCents)}}); err != nil {
				return nil, err
			}
		}
		return cur, s.record(ctx, cur, EventCancelled, from.Status())

	case StatusFulfilled:
		// The close short: the allocations and back orders of the unfulfilled
		// remainder are released, and the remainder is recorded closed (the
		// line's quantity exceeds allocated, backordered and fulfilled by it).
		// A remainder presupposes a partial bill.
		if body.Reason == "" {
			return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
				Message: "a close short carries its reason",
				Details: []httpx.FieldError{{Field: "reason", Message: "is required to close an order short"}}}
		}
		if !hasInvoices && !anyFulfilled(cur) {
			return nil, conflictBlocker("no_fulfilments", "nothing has been billed against this order: cancel it instead of closing it short")
		}
		from := cur.Status
		if err := s.releaseAllocations(ctx, cur); err != nil {
			return nil, err
		}
		cur.Status = StatusFulfilled
		cur.HoldReason, cur.HoldNote = nil, nil
		if err := s.repo.SaveTransition(ctx, cur); err != nil {
			return nil, err
		}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				Action: "order.closed_short", EntityType: "order", EntityID: cur.ID, UserID: body.Actor,
				Changes: map[string]any{"previous_status": from.Status(), "reason": body.Reason}}); err != nil {
				return nil, err
			}
		}
		return cur, s.record(ctx, cur, EventClosedShort, from.Status())
	}
	return nil, httpx.InvalidStateTransition(fmt.Sprintf("cannot transition to %s", to.Status()))
}

// confirm is the draft to confirmed transition (ADR 0005 sections 5.2 and
// 5.3): the exposure gate, the tax rate, the PO and contact authority guards,
// then the credit check, whose over the limit answer is a committed hold
// with its own event, never an error. Allocation and the back order
// derivation arrive with section 5.4 (C2-2b).
func (s *Service) confirm(ctx context.Context, cur *Order, body TransitionBody, priced *providerTax) (*Order, error) {
	// empty_order: a document with nothing to bill.
	billable := 0
	for i := range cur.Lines {
		if cur.Lines[i].Billable() {
			billable++
		}
	}
	if billable == 0 {
		return nil, conflictBlocker("empty_order", "the order has no billable line")
	}
	if s.exposure != nil {
		if err := s.exposure.RequireClearForOrder(ctx, cur.ID); err != nil {
			return nil, err
		}
	}
	// One customer's credit reading acts serialize (ADR 0005 section 11): two
	// confirms of one customer's orders cannot both read the exposure before
	// either has been counted in it.
	if err := s.repo.LockCustomerCredit(ctx, cur.CustomerID); err != nil {
		return nil, err
	}
	facts, err := s.repo.CustomerFacts(ctx, cur.CustomerID)
	if err != nil {
		return nil, err
	}
	if facts.PORequired && derefStr(cur.CustomerPO) == "" {
		return nil, conflictBlocker("po_required", "the customer requires a purchase order number: set customer_po before confirming")
	}
	var contactLimit *httpx.Cents
	if cur.OrderedByContactID != nil {
		authority, err := s.repo.ContactAuthority(ctx, *cur.OrderedByContactID)
		if err != nil {
			return nil, err
		}
		if !authority.Exists {
			return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
				Message: "a referenced record does not exist",
				Details: []httpx.FieldError{{Field: "ordered_by_contact_id", Message: "no such contact"}}}
		}
		if !authority.CanPlaceOrders {
			return nil, conflictBlocker("contact_authority", "the contact named on the order may not place orders")
		}
		contactLimit = authority.OrderLimitCents
	}
	if err := s.refreshTax(ctx, cur, priced); err != nil {
		return nil, err
	}
	// The contact's order limit is checked against the REFRESHED total, the one
	// this confirm will write: the draft's saved total predates a rate change
	// or the provider's answer (review round 2 P3-1).
	if contactLimit != nil && cur.TotalCents > *contactLimit {
		return nil, conflictBlocker("contact_authority", "the order is over the contact named on the order's limit")
	}

	// The credit check (ADR 0005 section 5.3): exposure is read from the
	// documents, never from customers.balance_due. Over the limit lands the
	// order on hold in this same transaction and answers 200.
	if facts.CreditLimitCents != nil {
		open, err := s.repo.OpenReceivableCents(ctx, cur.CustomerID, &cur.ID)
		if err != nil {
			return nil, err
		}
		if open+int64(cur.TotalCents) > int64(*facts.CreditLimitCents) {
			reason := HoldCreditLimit
			cur.Status, cur.HoldReason = StatusOnHold, &reason
			if err := s.repo.SaveTransition(ctx, cur); err != nil {
				return nil, err
			}
			if err := s.auditTransition(ctx, cur, "order.hold", body.Actor, StatusDraft, map[string]any{"hold_reason": "credit_limit"}); err != nil {
				return nil, err
			}
			return cur, s.record(ctx, cur, EventHold, StatusDraft.Status())
		}
	}

	now := httpx.TimestampOf(s.now().UTC())
	cur.ConfirmedAt = &now
	// Allocation (ADR 0005 5.4): each stocked line takes min(available,
	// quantity) and the rest lands on back order; the status is derived.
	if _, err := s.allocateLines(ctx, cur); err != nil {
		return nil, err
	}
	cur.Status = deriveStatus(cur)
	if cur.Status == StatusFulfilled {
		cur.Status = StatusConfirmed
	}
	// The ship-to snapshot at confirm (ADR 0005 section 5.1).
	if cur.ShipToID != nil && cur.ShipTo == nil {
		snap, _, err := s.repo.ShipTo(ctx, *cur.ShipToID)
		if err != nil {
			return nil, err
		}
		cur.ShipTo = snap
	}
	if err := s.repo.SaveTransition(ctx, cur); err != nil {
		return nil, err
	}
	if s.audit != nil {
		if err := s.audit.Log(ctx, audit.Entry{
			Action: "order.confirmed", EntityType: "order", EntityID: cur.ID, UserID: body.Actor,
			Changes: map[string]any{"customer_id": cur.CustomerID, "total_cents": int64(cur.TotalCents),
				"line_count": len(cur.Lines)}}); err != nil {
			return nil, err
		}
	}
	if err := s.record(ctx, cur, EventConfirmed, StatusDraft.Status()); err != nil {
		return nil, err
	}
	if cur.Status == StatusBackordered {
		if err := s.record(ctx, cur, EventBackordered, StatusDraft.Status()); err != nil {
			return nil, err
		}
	}
	return cur, nil
}

// anyFulfilled reports whether any line of the order has shipped.
func anyFulfilled(cur *Order) bool {
	for i := range cur.Lines {
		if cur.Lines[i].QuantityFulfilled > 0 {
			return true
		}
	}
	return false
}

// refreshTax re-resolves the tax at confirm (ADR 0005 section 3): by rate
// through the resolver, or from the provider answer minted before the
// transaction opened, verified against the lines under the lock. A line that
// changed between the provider's price and this transaction answers 409
// tax_quote_stale; the client retries, which prices afresh.
func (s *Service) refreshTax(ctx context.Context, o *Order, priced *providerTax) error {
	if priced != nil {
		if linesFingerprint(o) != priced.fingerprint {
			return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
				Message: "the order's lines moved between the tax price and the confirm",
				Details: []httpx.FieldError{httpx.Blocker("tax_quote_stale",
					"the order changed while its tax was priced: retry the confirm to price afresh")}}
		}
		totals := salesdoc.SumTotals(plainLines(o.Lines))
		o.SubtotalCents = totals.SubtotalCents
		o.TaxCents = priced.taxCents
		o.TaxRatePercent = nil
		o.TaxSource = salesdoc.TaxSourceProvider
		o.TotalCents = o.SubtotalCents + o.TaxCents
		return nil
	}
	totals := salesdoc.SumTotals(plainLines(o.Lines))
	exempt, err := s.repo.CustomerExempt(ctx, o.CustomerID)
	if err != nil {
		return err
	}
	inputs, err := s.taxInputs(ctx, o, exempt)
	if err != nil {
		return err
	}
	resolved, err := salesdoc.ResolveTax(inputs)
	if err != nil {
		if errors.Is(err, salesdoc.ErrTaxRateNotConfigured) {
			return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
				Message: "no tax rate is configured for this order's branch",
				Details: []httpx.FieldError{httpx.Blocker("tax_rate_not_configured",
					"set the branch's default tax rate (or the ship-to's rate) before confirming the order")}}
		}
		return err
	}
	o.SubtotalCents = totals.SubtotalCents
	pct, err := RateToPercent(*resolved.Rate)
	if err != nil {
		return err
	}
	o.TaxRatePercent = &pct
	o.TaxSource = resolved.Source
	o.TaxExempt = resolved.Source == salesdoc.TaxSourceExempt
	scaled, _, err := salesdoc.ParseTaxRate(*resolved.Rate)
	if err != nil {
		return err
	}
	o.TaxCents = salesdoc.TaxAt(totals.TaxableCents, scaled)
	o.TotalCents = o.SubtotalCents + o.TaxCents
	return nil
}

func conflictBlocker(code, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
		Message: message, Details: []httpx.FieldError{httpx.Blocker(code, message)}}
}

// record writes the order's event into the outbox through the transaction's
// executor. fromStatus is set on a status change.
// record writes the module's outbox event for a finished order. The cores
// return the event instead (eventFor), so a caller inside another
// transaction can order it among its own last statements.
func (s *Service) record(ctx context.Context, o *Order, eventType, fromStatus string) error {
	ev, err := s.outboxEventFor(o, eventType, fromStatus)
	if err != nil {
		return err
	}
	return s.writeEvent(ctx, ev)
}

// outboxEventFor builds the module's outbox event for a finished order
// without writing it.
func (s *Service) outboxEventFor(o *Order, eventType, fromStatus string) (*outbox.Event, error) {
	data := map[string]any{
		"number":      o.Number,
		"customer_id": o.CustomerID,
		"status":      o.Status.Status(),
		"revision":    o.Revision,
		"currency":    o.Currency,
		"total_cents": int64(o.TotalCents),
	}
	if fromStatus != "" {
		data["from_status"] = fromStatus
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	branch := o.BranchID
	return &outbox.Event{
		Type: eventType, EntityType: "order", EntityID: o.ID, BranchID: &branch, Data: raw,
	}, nil
}

// writeEvent writes one event when the recorder is wired.
func (s *Service) writeEvent(ctx context.Context, ev *outbox.Event) error {
	if s.events == nil || ev == nil {
		return nil
	}
	return s.events.Write(ctx, *ev)
}

// CheckExposureGate reports whether the order is currently blocked by the
// pre-ship gate.
func (s *Service) CheckExposureGate(ctx context.Context, id uuid.UUID) error {
	if s.exposure == nil {
		return nil
	}
	return s.exposure.RequireClearForOrder(ctx, id)
}

// OverrideExposure records an explicit owner override of the pre-ship gate.
func (s *Service) OverrideExposure(ctx context.Context, id uuid.UUID, notes, actor, role string) error {
	if s.overrider == nil {
		return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed,
			Message: "exposure override not available"}
	}
	if len(notes) < 10 {
		return &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
			Message: "override notes must be at least 10 characters",
			Details: []httpx.FieldError{{Field: "notes", Message: "must be at least 10 characters"}}}
	}
	if err := s.overrider.OverrideForOrder(ctx, id, notes, actor, role); err != nil {
		return err
	}
	if s.audit != nil {
		s.audit.Log(ctx, audit.Entry{
			Action: "order.exposure.overridden", EntityType: "order", EntityID: id, UserID: actor,
			Changes: map[string]any{"notes": notes, "role": role}})
	}
	return nil
}

// QuoteSourceLine is one quote line as an order line source (ADR 0005
// section 5.8's table): the pair and the price carried without loss.
type QuoteSourceLine struct {
	QuoteLineID uuid.UUID
	ProductID   *uuid.UUID
	SKU         string
	Description string
	Quantity    httpx.Quantity
	UOM         string
	PriceUOM    string
	UOMQty      httpx.Quantity
	PriceUOMQty httpx.Quantity
	UnitPrice   httpx.Price
}

// QuoteSource is a quote being converted: what the order copies (ADR 0005
// section 5.8).
type QuoteSource struct {
	QuoteID      uuid.UUID
	BranchID     uuid.UUID // the quote's branch: the order's branch, and so its tax rate
	CustomerID   uuid.UUID
	JobID        *uuid.UUID
	DeliveryType DeliveryType
	FreightCents httpx.Cents
	Lines        []QuoteSourceLine
}

// ProviderTax is a provider answer minted before the transaction opened,
// with the fingerprint of the lines it priced (ADR 0005 section 3): the
// create inside the transaction verifies the lines it writes equal what was
// priced, and a difference answers 409 tax_quote_stale.
type ProviderTax struct {
	TaxCents    httpx.Cents
	Fingerprint string
}

// PrepareQuoteTax prices a conversion's tax before its transaction opens:
// the provider, when one is configured and the customer is not exempt, with
// the built order in hand. Nil means "resolve by rate inside the
// transaction".
func (s *Service) PrepareQuoteTax(ctx context.Context, src *QuoteSource) (*ProviderTax, error) {
	if s.tax == nil || !s.tax.Configured() {
		return nil, nil
	}
	facts, err := s.repo.CustomerFacts(ctx, src.CustomerID)
	if err != nil {
		return nil, err
	}
	if !facts.Exists {
		return nil, nil
	}
	exempt, err := s.repo.CustomerExempt(ctx, src.CustomerID)
	if err != nil {
		return nil, err
	}
	if exempt {
		return nil, nil
	}
	b, err := s.buildFromQuote(ctx, src, src.BranchID)
	if err != nil {
		return nil, err
	}
	totals := salesdoc.SumTotals(plainLines(b.order.Lines))
	result, err := s.tax.PreviewTax(ctx, taxRequest(&b.order, totals.TaxableCents))
	if err != nil {
		return nil, &httpx.Error{Status: http.StatusServiceUnavailable, Code: httpx.CodeUnavailable,
			Message: "the tax provider did not answer: the quote is not converted"}
	}
	return &ProviderTax{TaxCents: httpx.Cents(result.TotalTax), Fingerprint: linesFingerprint(&b.order)}, nil
}

// CreateFromQuote creates the converted order inside the caller's
// transaction (the quote is locked and accepted around it), writing
// order.created as its last statement: the convert writes quote.accepted
// first, then this writes order.created (ADR 0005 section 5.8).
func (s *Service) CreateFromQuote(ctx context.Context, src *QuoteSource, priced *ProviderTax) (*Order, error) {
	// The converted order takes the QUOTE's branch (ADR 0005 section 5.8's
	// table), never the caller's context or the deployment default: the
	// branch decides the tax rate and, in C2-2b, the stock the order reads.
	var out *Order
	err := s.inTx(ctx, func(ctx context.Context) error {
		b, err := s.buildFromQuote(ctx, src, src.BranchID)
		if err != nil {
			return err
		}
		if priced != nil {
			if linesFingerprint(&b.order) != priced.Fingerprint {
				return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
					Message: "the quote's lines moved between the tax price and the conversion",
					Details: []httpx.FieldError{httpx.Blocker("tax_quote_stale",
						"the quote changed while its tax was priced: retry the convert to price afresh")}}
			}
			totals := salesdoc.SumTotals(plainLines(b.order.Lines))
			b.order.SubtotalCents = totals.SubtotalCents
			b.order.TaxCents = priced.TaxCents
			b.order.TaxRatePercent = nil
			b.order.TaxSource = salesdoc.TaxSourceProvider
			b.order.TotalCents = b.order.SubtotalCents + b.order.TaxCents
		} else if err := s.applyRateTax(ctx, &b.order); err != nil {
			return err
		}
		number, err := s.repo.NextNumber(ctx)
		if err != nil {
			return err
		}
		b.order.Number = number
		if err := s.repo.InsertOrder(ctx, &b.order); err != nil {
			return err
		}
		if out, err = s.repo.GetOrder(ctx, b.order.ID); err != nil {
			return err
		}
		return s.record(ctx, out, EventCreated, "")
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// QuoteHasOrder answers whether the quote already has an order not
// cancelled: the convert's already_converted guard (ADR 0005 section 5.8).
func (s *Service) QuoteHasOrder(ctx context.Context, quoteID uuid.UUID) (bool, error) {
	return s.repo.OrderExistsForQuote(ctx, quoteID)
}

// quoteParsedLines shapes a quote source as parsed lines for the shared
// lookups (products, kit components, charge codes).
func quoteParsedLines(src *QuoteSource) []salesdoc.ParsedLine {
	out := make([]salesdoc.ParsedLine, 0, len(src.Lines))
	for i := range src.Lines {
		l := src.Lines[i]
		out = append(out, salesdoc.ParsedLine{
			LineType: salesdoc.LineProduct, ProductID: l.ProductID,
			Quantity: l.Quantity, UOM: l.UOM, PriceUOM: l.PriceUOM,
			UOMQty: l.UOMQty, PriceUOMQty: l.PriceUOMQty,
		})
	}
	return out
}

// buildFromQuote builds the order a conversion creates (ADR 0005 section
// 5.8's table): every quote line carried with its pair and price exactly
// (price_source QUOTE), a stocked product sold in another unit than its
// stocking unit refused until cycle 3, a kit product exploded, the quote's
// freight a FREIGHT charge line.
func (s *Service) buildFromQuote(ctx context.Context, src *QuoteSource, branchID uuid.UUID) (*built, error) {
	facts, err := s.repo.CustomerFacts(ctx, src.CustomerID)
	if err != nil {
		return nil, err
	}
	if !facts.Exists {
		return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
			Message: "a referenced record does not exist",
			Details: []httpx.FieldError{{Field: "customer_id", Message: "no such customer"}}}
	}

	o := Order{}
	o.ID = uuid.New()
	o.BranchID = branchID
	o.QuoteID = &src.QuoteID
	o.CustomerID, o.JobID = src.CustomerID, src.JobID
	o.DeliveryType = src.DeliveryType
	if o.DeliveryType == "" {
		o.DeliveryType = DeliveryPickup
	}
	o.SalespersonID = facts.SalespersonID
	o.Currency = facts.Currency
	o.Status = StatusDraft
	o.CreatedAt = httpx.TimestampOf(s.now().UTC())
	o.UpdatedAt = o.CreatedAt
	o.Revision = 1

	if o.DeliveryType == DeliveryDelivery {
		if id, err := s.repo.DefaultShipToID(ctx, src.CustomerID); err != nil {
			return nil, err
		} else if id != nil {
			o.ShipToID = id
			snap, _, err := s.repo.ShipTo(ctx, *id)
			if err != nil {
				return nil, err
			}
			o.ShipTo = snap
		}
	}

	var productIDs []uuid.UUID
	for _, l := range src.Lines {
		if l.ProductID != nil {
			productIDs = append(productIDs, *l.ProductID)
		}
	}
	refs, _, kits, err := s.lookups(ctx, quoteParsedLines(src))
	if err != nil {
		return nil, err
	}

	lines := make([]OrderLine, 0, len(src.Lines)+1)
	for i, ql := range src.Lines {
		line := salesdoc.Line{
			ID:          uuid.New(),
			LineType:    salesdoc.LineProduct,
			Description: ql.Description,
			Quantity:    salesdocPtr(ql.Quantity),
			CreatedAt:   o.CreatedAt,
		}
		quoteLineID := ql.QuoteLineID
		if ql.SKU != "" {
			sku := ql.SKU
			line.SKU = &sku
		}
		uom, priceUOM := ql.UOM, ql.PriceUOM
		if priceUOM == "" {
			priceUOM = uom
		}
		if ql.ProductID != nil {
			ref, ok := refs[ql.ProductID.String()]
			if !ok {
				return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
					Message: "a referenced record does not exist",
					Details: []httpx.FieldError{{Field: fmt.Sprintf("lines[%d].product_id", i), Message: "no such product"}}}
			}
			line.ProductID = &ref.ID
			stock := ref.UOMPrimary
			if uom != stock {
				return nil, &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
					Message: "a stocked line is sold in its product's stocking unit",
					Details: []httpx.FieldError{httpx.Blocker("unit_not_stock_unit",
						fmt.Sprintf("lines[%d] is sold in %s but the product stocks in %s", i, uom, stock))}}
			}
			line.Taxable = ref.Taxable
			if line.SKU == nil {
				sku := ref.SKU
				line.SKU = &sku
			}
			if line.Description == "" {
				line.Description = ref.Description
			}
		} else {
			// a non stock line: the dealer does not carry it
			line.Taxable = true
		}
		line.UOM, line.PriceUOM = &uom, &priceUOM
		uomQty, priceUomQty := ql.UOMQty, ql.PriceUOMQty
		if uomQty == 0 && priceUomQty == 0 {
			uomQty, priceUomQty = salesdoc.One, salesdoc.One
		}
		line.UOMQty, line.PriceUOMQty = &uomQty, &priceUomQty
		price := ql.UnitPrice
		line.UnitPrice = &price
		line.PricedUnitPrice = &price
		line.PriceSource = salesdoc.PriceSourceQuote
		lines = append(lines, OrderLine{Line: line, QuoteLineID: &quoteLineID})
	}

	// The quote's freight becomes one FREIGHT charge line (ADR 0005 5.8).
	if src.FreightCents > 0 {
		codes, err := s.repo.LookupChargeCodes(ctx, []string{"FREIGHT"})
		if err != nil {
			return nil, err
		}
		code, ok := codes["FREIGHT"]
		if !ok {
			return nil, fmt.Errorf("the FREIGHT charge code is not seeded")
		}
		one := salesdoc.One
		qty := salesdoc.One
		ea := "EA"
		price := httpx.Price(int64(src.FreightCents) * 100)
		zero := httpx.Cents(0)
		text := "FREIGHT"
		account := code.RevenueAccountCode
		charge := salesdoc.Line{
			ID: uuid.New(), LineType: salesdoc.LineCharge, ChargeCodeID: &code.ID, ChargeCode: &text,
			Description: code.Name, Quantity: &qty, UOM: &ea, PriceUOM: &ea,
			UOMQty: &one, PriceUOMQty: &one, UnitPrice: &price, PricedUnitPrice: nil,
			PriceSource: salesdoc.PriceSourceQuote, LineTotal: &zero, Taxable: code.Taxable,
			RevenueAccountCode: &account, CreatedAt: o.CreatedAt,
		}
		lines = append(lines, OrderLine{Line: charge})
	}

	exploded, err := salesdoc.Explode(plainLines(lines), kits, refs)
	if err != nil {
		return nil, err
	}
	outLines := make([]OrderLine, 0, len(exploded))
	// Explode rebuilds positions; the quote line links ride along by line id.
	quoteLinks := map[uuid.UUID]uuid.UUID{}
	for i := range lines {
		if lines[i].QuoteLineID != nil {
			quoteLinks[lines[i].Line.ID] = *lines[i].QuoteLineID
		}
	}
	for _, l := range exploded {
		ol := OrderLine{Line: l}
		if q, ok := quoteLinks[l.ID]; ok {
			qlID := q
			ol.QuoteLineID = &qlID
		}
		outLines = append(outLines, ol)
	}
	for i := range outLines {
		if err := salesdoc.ExtendLine(&outLines[i].Line); err != nil {
			return nil, err
		}
	}
	o.Lines = outLines
	return &built{order: o}, nil
}

// auditTransition writes the audit row of a hold, a release or a reopen with
// the actor (review round 2 P3-3): a finance release of a credit hold is the
// override of the credit check, so who did it is recorded. It rides the act's
// transaction.
func (s *Service) auditTransition(ctx context.Context, cur *Order, action, actor string, from OrderStatus, extra map[string]any) error {
	if s.audit == nil {
		return nil
	}
	changes := map[string]any{"previous_status": from.Status(), "total_cents": int64(cur.TotalCents)}
	for k, v := range extra {
		changes[k] = v
	}
	return s.audit.Log(ctx, audit.Entry{Action: action, EntityType: "order", EntityID: cur.ID, UserID: actor, Changes: changes})
}
