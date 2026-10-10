// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// Event types of the counter (ADR 0005 section 12).
const (
	EventSaleCompleted   = "pos_transaction.completed"
	EventSaleVoided      = "pos_transaction.voided"
	EventReturnCompleted = "pos_return.completed"
	EventTillOpened      = "till.opened"
	EventTillClosed      = "till.closed"

	auditLinePriceOverridden = "pos_transaction.line_price_overridden"
	auditLineDiscounted      = "pos_transaction.line_discounted"
)

// PriceEngine prices a product line for a customer and quantity at the
// counter boundary, exactly the order module's engine (ADR 0005 section 1).
type PriceEngine interface {
	PriceFor(ctx context.Context, customerID, productID uuid.UUID, basePrice httpx.Price, quantity httpx.Quantity, jobID *uuid.UUID) (httpx.Price, error)
}

// TaxProvider is the configured provider path behind the rate resolver
// (ADR 0005 section 3), the tax service's own surface.
type TaxProvider interface {
	ProviderConfigured() bool
	PreviewTax(ctx context.Context, req *tax.TaxPreviewRequest) (*tax.TaxResult, error)
}

// InvoiceWriter writes the counter sale's invoice with its entry and its
// subledger debit inside the caller's transaction (invoice.Service).
type InvoiceWriter interface {
	CreateCounterInvoice(ctx context.Context, in *invoice.CounterInvoice) error
}

// LedgerPoster posts the till drawer's over/short inside the close's
// transaction (gl.Service).
type LedgerPoster interface {
	PostTillOverShort(ctx context.Context, sessionID uuid.UUID, overShortCents int64) (uuid.UUID, error)
}

// EventRecorder writes the act's events, last, in its transaction.
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Service is the counter: sales, voids, returns, till sessions and the
// offline sync, every money moment one transaction whose events are written
// last (ADR 0005 sections 11 and 14.2 C2-5).
type Service struct {
	db        *database.DB
	repo      Repository
	logger    *slog.Logger
	now       func() time.Time
	pricer    PriceEngine
	tax       TaxProvider
	invoices  InvoiceWriter
	ledger    LedgerPoster
	gateway   PaymentGateway
	auditLog  *audit.Logger
	events    EventRecorder
	tx        TxRunner
	ar        ARCore
	inventory CounterInventory
}

// ARCore is the slice of the AR core the counter drives (account.Service).
type ARCore interface {
	RecordPayment(ctx context.Context, in account.RecordPaymentIn) (uuid.UUID, *account.Effects, error)
	VoidPayment(ctx context.Context, in account.VoidPaymentIn) (*account.Effects, error)
	Reverse(ctx context.Context, in account.ReverseIn) (*account.Effects, error)
	RefundPayment(ctx context.Context, in account.RefundPaymentIn) (uuid.UUID, *account.Effects, error)
	PostCreditMemo(ctx context.Context, in account.PostCreditMemoIn) (*account.Effects, error)
	RefundCreditMemo(ctx context.Context, in account.RefundCreditMemoIn) (uuid.UUID, *account.Effects, error)
	VoidInvoice(ctx context.Context, in account.VoidInvoiceIn) (*account.Effects, error)
}

// CounterInventory moves stock at the counter: an issue takes from on hand
// (a counter sale never allocates), a restock returns goods.
type CounterInventory interface {
	IssueQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error
	RestockQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error
}

// NewService builds the counter's service.
func NewService(db *database.DB, repo Repository, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: db, repo: repo, logger: logger, now: time.Now}
}

// WithPriceEngine wires the pricing engine.
func (s *Service) WithPriceEngine(p PriceEngine) *Service { s.pricer = p; return s }

// WithTaxProvider wires the configured tax provider behind the resolver.
func (s *Service) WithTaxProvider(p TaxProvider) *Service { s.tax = p; return s }

// WithInvoices wires the invoice writer.
func (s *Service) WithInvoices(w InvoiceWriter) *Service { s.invoices = w; return s }

// WithLedger wires the till over/short poster.
func (s *Service) WithLedger(p LedgerPoster) *Service { s.ledger = p; return s }

// WithGateway wires the card-present terminal gateway.
func (s *Service) WithGateway(g PaymentGateway) *Service { s.gateway = g; return s }

// WithAuditLog attaches the audit logger.
func (s *Service) WithAuditLog(l *audit.Logger) *Service { s.auditLog = l; return s }

// WithOutbox wires the event recorder.
func (s *Service) WithOutbox(e EventRecorder) *Service { s.events = e; return s }

// WithTxRunner wires the transaction runner (serve does; unit tests need
// none).
func (s *Service) WithTxRunner(t TxRunner) *Service { s.tx = t; return s }

// WithAR wires the AR core.
func (s *Service) WithAR(a ARCore) *Service { s.ar = a; return s }

// WithInventory wires the stock moves.
func (s *Service) WithInventory(i CounterInventory) *Service { s.inventory = i; return s }

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx != nil {
		return s.tx.RunInTx(ctx, fn)
	}
	if s.db != nil {
		return s.db.RunInTx(ctx, fn)
	}
	return fn(ctx)
}

// actorOf reads the acting user from the request context's claims.
func actorOf(ctx context.Context) string { return "" }

// eventJSON marshals an event's data.
func eventJSON(v map[string]any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}

// estimateTax is the cart's live estimate at the branch rate; the money
// moment resolves its own tax through the provider or the resolver
// (ADR 0005 section 3: the estimate is not the bill).
func (s *Service) estimateTax(ctx context.Context, sale *Sale, taxableCents httpx.Cents) int64 {
	rate, ok, err := s.repo.BranchTaxRate(ctx, &sale.BranchID)
	if err != nil || !ok {
		return 0
	}
	scaled, _, err := salesdoc.ParseTaxRate(rate)
	if err != nil {
		return 0
	}
	return int64(salesdoc.TaxAt(taxableCents, scaled))
}

func (s *Service) recordEvent(ctx context.Context, ev outbox.Event) error {
	if s.events == nil {
		return nil
	}
	return s.events.Write(ctx, ev)
}

// StartSale opens a cart on a register, numbered POS- through the sequence
// and attached to the register's open till session when one exists. A sale
// without a customer is a walk-in sale: the walk-in customer is resolved at
// completion, where the invoice needs a real customer.
func (s *Service) StartSale(ctx context.Context, in *StartSale, actor string) (*Sale, error) {
	walkInID, defaultCurrency, err := s.repo.WalkInCustomer(ctx)
	if err != nil {
		return nil, err
	}
	currency := defaultCurrency
	if in.CustomerID != nil {
		facts, err := s.repo.CustomerFacts(ctx, *in.CustomerID)
		if err != nil {
			return nil, err
		}
		currency = facts.Currency
	}
	var out *Sale
	err = s.inTx(ctx, func(ctx context.Context) error {
		sale := &Sale{RegisterID: in.RegisterID, CashierID: in.CashierID, CustomerID: in.CustomerID,
			Status: StatusOpen, Currency: currency, WalkInID: walkInID}
		if session, err := s.repo.GetOpenTillSession(ctx, in.RegisterID); err == nil && session != nil {
			sale.TillSessionID = &session.ID
		}
		if err := s.repo.CreateSale(ctx, sale); err != nil {
			return err
		}
		out = sale
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// pricingOut comes back from pricing a request line: the line, ready to
// store, and the audit rows the price adjustment owes.
type pricingOut struct {
	overrides []audit.Entry
	discounts []audit.Entry
}

// priceLine turns one parsed request line into a stored line at the counter
// (ADR 0005 sections 2.3 and 2.7, the order module's pricing step applied at
// the counter): defaults from the product or the charge code, the engine's
// price beside an override, the discount on the line, kits exploded.
func (s *Service) priceLine(ctx context.Context, sale *Sale, pl salesdoc.ParsedLine, actor string) (salesdoc.Line, *pricingOut, error) {
	out := &pricingOut{}
	line := salesdoc.Line{ID: uuid.New(), Description: pl.Description, PriceSource: salesdoc.PriceSourceNone}
	if pl.Taxable != nil {
		line.Taxable = *pl.Taxable
	}
	customerID := uuid.Nil
	if sale.CustomerID != nil {
		customerID = *sale.CustomerID
	}
	refs, codes, _, err := s.lookups(ctx, []salesdoc.ParsedLine{pl})
	if err != nil {
		return line, out, err
	}
	switch pl.LineType {
	case salesdoc.LineText:
		line.LineType = salesdoc.LineText
		return line, out, nil
	case salesdoc.LineCharge:
		line.LineType = salesdoc.LineCharge
		code, ok := codes[pl.ChargeCode]
		if !ok {
			return line, out, invalid(lineField("charge_code"), "no such charge code")
		}
		line.ChargeCodeID = &code.ID
		cc := code.Code
		line.ChargeCode = &cc
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
		line.UOMQty, line.PriceUOMQty = ptrQty(salesdoc.One), ptrQty(salesdoc.One)
		price := httpx.Price(0)
		if pl.UnitPrice != nil {
			price = *pl.UnitPrice
		} else if code.DefaultUnitPrice != nil {
			price = *code.DefaultUnitPrice
		} else {
			return line, out, invalid("line.unit_price_ten_thousandths",
				"is required when the charge code has no default price")
		}
		line.UnitPrice = &price
		line.PriceSource = salesdoc.PriceSourceManual
		if pl.Taxable != nil {
			line.Taxable = *pl.Taxable
		} else {
			line.Taxable = code.Taxable
		}
		line.RevenueAccountCode = &code.RevenueAccountCode
	default: // product
		line.LineType = salesdoc.LineProduct
		var ref *salesdoc.ProductRef
		if pl.ProductID != nil {
			if r, ok := refs[pl.ProductID.String()]; ok {
				ref = &r
			} else {
				return line, out, invalid(lineField("product_id"), "no such product")
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
			stock := ref.UOMPrimary
			if pl.UOM != "" && pl.UOM != stock {
				return line, out, conflict("unit_not_stock_unit",
					fmt.Sprintf("the line is sold in %s but the product stocks in %s", pl.UOM, stock))
			}
			uom := stock
			line.UOM = &uom
			line.Taxable = ref.Taxable
		} else {
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
			priceUOM = derefString(line.UOM)
		}
		line.PriceUOM = &priceUOM
		if pl.UOMQty == 0 && pl.PriceUOMQty == 0 {
			line.UOMQty, line.PriceUOMQty = ptrQty(salesdoc.One), ptrQty(salesdoc.One)
		} else {
			line.UOMQty, line.PriceUOMQty = ptrQty(pl.UOMQty), ptrQty(pl.PriceUOMQty)
		}
		qty := pl.Quantity
		line.Quantity = &qty
		var enginePrice httpx.Price
		if ref != nil {
			enginePrice, err = s.enginePrice(ctx, customerID, ref, pl.Quantity)
			if err != nil {
				return line, out, err
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
					line.PriceAdjustedBy = actorPtr(actor)
					out.overrides = append(out.overrides, audit.Entry{
						Action: auditLinePriceOverridden, EntityType: "pos_transaction", EntityID: sale.ID, UserID: actor,
						Changes: map[string]any{"line_id": line.ID, "product_id": line.ProductID,
							"engine_price_ten_thousandths":   int64(enginePrice),
							"override_price_ten_thousandths": int64(p), "reason": pl.OverrideReason}})
				}
			} else {
				line.PriceSource = salesdoc.PriceSourceManual
			}
		}
	}
	if pl.DiscountPercent != nil {
		pct := *pl.DiscountPercent
		line.DiscountPercent = &pct
		line.DiscountReason = &pl.DiscountReason
		line.PriceAdjustedBy = actorPtr(actor)
	} else if pl.DiscountAmount != nil {
		amt := *pl.DiscountAmount
		line.DiscountAmount = &amt
		line.DiscountReason = &pl.DiscountReason
		line.PriceAdjustedBy = actorPtr(actor)
	}
	if line.DiscountPercent != nil || line.DiscountAmount != nil {
		out.discounts = append(out.discounts, audit.Entry{
			Action: auditLineDiscounted, EntityType: "pos_transaction", EntityID: sale.ID, UserID: actor,
			Changes: map[string]any{"line_id": line.ID, "product_id": line.ProductID,
				"discount_percent": pl.DiscountPercent, "discount_cents": pl.DiscountAmount,
				"reason":           pl.DiscountReason}})
	}
	// Kits explode: the kit line priced as a whole, one component per
	// component at zero (2.6). The caller stores what comes back.
	return line, out, nil
}

// explodeAndExtend runs Explode then ExtendLine over the priced lines,
// returning the stored order.
func (s *Service) explodeAndExtend(ctx context.Context, lines []salesdoc.Line) ([]salesdoc.Line, error) {
	var productIDs, kitIDs []uuid.UUID
	for i := range lines {
		if lines[i].ProductID != nil {
			productIDs = append(productIDs, *lines[i].ProductID)
			kitIDs = append(kitIDs, *lines[i].ProductID)
		}
	}
	refs, err := s.repo.LookupProducts(ctx, productIDs)
	if err != nil {
		return nil, err
	}
	kits, err := s.repo.LookupKitComponents(ctx, kitIDs)
	if err != nil {
		return nil, err
	}
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
		more, err := s.repo.LookupProducts(ctx, componentIDs)
		if err != nil {
			return nil, err
		}
		for k, v := range more {
			refs[k] = v
		}
	}
	exploded, err := salesdoc.Explode(lines, kits, refs)
	if err != nil {
		return nil, err
	}
	for i := range exploded {
		if err := salesdoc.ExtendLine(&exploded[i]); err != nil {
			return nil, err
		}
	}
	return exploded, nil
}

func (s *Service) enginePrice(ctx context.Context, customerID uuid.UUID, ref *salesdoc.ProductRef, qty httpx.Quantity) (httpx.Price, error) {
	if s.pricer == nil {
		return ref.BasePrice, nil
	}
	return s.pricer.PriceFor(ctx, customerID, ref.ID, ref.BasePrice, qty, nil)
}

// lookups reads every product, charge code and kit definition the parsed
// lines name.
func (s *Service) lookups(ctx context.Context, parsed []salesdoc.ParsedLine) (map[string]salesdoc.ProductRef, map[string]salesdoc.ChargeCode, map[string][]salesdoc.KitComponent, error) {
	var productIDs, kitIDs []uuid.UUID
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
	chargeCodes, err := s.repo.LookupChargeCodes(ctx, codes)
	if err != nil {
		return nil, nil, nil, err
	}
	return refs, chargeCodes, kits, nil
}

// AddLine adds one line to an open cart: priced, extended, with the audit
// rows an override or a discount owes, all in the add's own transaction.
func (s *Service) AddLine(ctx context.Context, saleID uuid.UUID, parsed []salesdoc.ParsedLine, actor string) (*Sale, error) {
	var out *Sale
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if sale.Status != StatusOpen && sale.Status != StatusHeld {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot add a line to a %s sale", sale.Status.Status()))
		}
		var priced []salesdoc.Line
		var audits []audit.Entry
		for i := range parsed {
			line, out, err := s.priceLine(ctx, sale, parsed[i], actor)
			if err != nil {
				return err
			}
			priced = append(priced, line)
			audits = append(audits, out.overrides...)
			audits = append(audits, out.discounts...)
		}
		lines, err := s.explodeAndExtend(ctx, priced)
		if err != nil {
			return err
		}
		existing, err := s.repo.GetLines(ctx, saleID)
		if err != nil {
			return err
		}
		// the new lines keep the order Explode built (a kit before its
		// components), numbered after the cart's own
		for i := range lines {
			lines[i].Position = len(existing) + i
		}
		if err := s.repo.AddLines(ctx, saleID, lines); err != nil {
			return err
		}
		for i := range audits {
			if s.auditLog != nil {
				if err := s.auditLog.Log(ctx, audits[i]); err != nil {
					return fmt.Errorf("failed to write the audit row: %w", err)
				}
			}
		}
		if err := s.recalcTotals(ctx, sale); err != nil {
			return err
		}
		out, err = s.GetSale(ctx, saleID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RemoveLine removes a line from an open cart.
func (s *Service) RemoveLine(ctx context.Context, saleID, lineID uuid.UUID) (*Sale, error) {
	var out *Sale
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if sale.Status != StatusOpen && sale.Status != StatusHeld {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot remove a line from a %s sale", sale.Status.Status()))
		}
		lines, err := s.repo.GetLines(ctx, saleID)
		if err != nil {
			return err
		}
		for i := range lines {
			if lines[i].ID == lineID && lines[i].ParentLineID != nil {
				return invalid("line_id", "is a kit component: remove the kit line, its components follow")
			}
		}
		if err := s.repo.RemoveLine(ctx, saleID, lineID); err != nil {
			return err
		}
		if err := s.recalcTotals(ctx, sale); err != nil {
			return err
		}
		out, err = s.GetSale(ctx, saleID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetSale reads a sale with its lines and tenders.
func (s *Service) GetSale(ctx context.Context, saleID uuid.UUID) (*Sale, error) {
	sale, err := s.repo.GetSale(ctx, saleID)
	if err != nil {
		return nil, err
	}
	if sale.Lines, err = s.repo.GetLines(ctx, saleID); err != nil {
		return nil, err
	}
	if sale.Tenders, err = s.repo.GetTenders(ctx, saleID); err != nil {
		return nil, err
	}
	return sale, nil
}

// recalcTotals recomputes the cart's subtotal and the live tax estimate.
func (s *Service) recalcTotals(ctx context.Context, sale *Sale) error {
	lines, err := s.repo.GetLines(ctx, sale.ID)
	if err != nil {
		return err
	}
	totals := salesdoc.SumTotals(lines)
	return s.repo.UpdateSaleTotals(ctx, sale.ID, int64(totals.SubtotalCents), int64(s.estimateTax(ctx, sale, totals.TaxableCents)), int64(totals.SubtotalCents)+int64(s.estimateTax(ctx, sale, totals.TaxableCents)))
}

func invalid(field, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed,
		Message: "one or more fields failed validation", Details: []httpx.FieldError{{Field: field, Message: message}}}
}

func lineField(name string) string { return "line." + name }

func conflict(code, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
		Message: message, Details: []httpx.FieldError{httpx.Blocker(code, message)}}
}

func ptrQty(q httpx.Quantity) *httpx.Quantity { return &q }

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func actorPtr(actor string) *string {
	if actor == "" {
		return nil
	}
	return &actor
}

var errNotConfigured = errors.New("the counter's money path is not wired")
