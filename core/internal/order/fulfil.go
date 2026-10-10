// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// Event types of a fulfilment (ADR 0005 section 12).
const (
	EventPartiallyFulfilled = "order.partially_fulfilled"
	EventFulfilled          = "order.fulfilled"
	EventInvoiceCreated     = "invoice.created"

	auditChecksSkipped = "order.fulfillment_checks_skipped"
)

// InvoiceWriter writes the invoice of a fulfilment with its entry and its
// subledger debit inside the caller's transaction (invoice.Service).
type InvoiceWriter interface {
	CreateFulfilmentInvoice(ctx context.Context, in *invoice.FulfilmentInvoice) error
}

func (s *Service) WithInvoices(w InvoiceWriter) *Service { s.invoices = w; return s }

// DepositApplier applies the order's unapplied payments to the invoice a
// fulfilment created, oldest first, up to the invoice's total (ADR 0005 5.6
// step 7). account.Service satisfies it.
type DepositApplier interface {
	ApplyDeposits(ctx context.Context, invoiceID uuid.UUID, paymentIDs []uuid.UUID, on time.Time, actor string) (*account.Effects, error)
}

// WithDeposits wires the deposit application of a fulfilment.
func (s *Service) WithDeposits(d DepositApplier) *Service { s.deposits = d; return s }

// FulfilLineRequest names one order line and the quantity to bill.
type FulfilLineRequest struct {
	OrderLineID uuid.UUID
	Quantity    httpx.Quantity
}

// FulfilRequest is POST /orders/{id}/fulfillments (ADR 0005 5.6). Lines is
// optional: absent means every allocated quantity, every non stock product
// line's unbilled quantity and every charge line not yet billed.
type FulfilRequest struct {
	Lines      []FulfilLineRequest
	PickedUpBy string
	DeliveryID *uuid.UUID
	Actor      string
	// InProcess marks the delivery completion's fulfilment (ADR 0005 5.5): the
	// goods have left the yard, so the credit and price exposure re-checks of
	// step 1 are skipped, with one audit row naming them.
	InProcess bool
}

// Fulfilment is the outcome: the updated order and the invoice created.
type Fulfilment struct {
	Order     *Order
	InvoiceID uuid.UUID
}

// nothingToFulfil is the refusal when no line has anything left to bill.
func nothingToFulfil() *httpx.Error {
	return conflictBlocker("nothing_to_fulfil", "the order has nothing left to fulfil")
}

// IsNothingToFulfil reports whether err is the nothing_to_fulfil refusal: the
// delivery adapter treats it as success.
func IsNothingToFulfil(err error) bool {
	var he *httpx.Error
	if errors.As(err, &he) && he.Status == http.StatusConflict {
		for _, d := range he.Details {
			if d.Code == "nothing_to_fulfil" {
				return true
			}
		}
	}
	return false
}

func exceeds(code, message string) *httpx.Error { return conflictBlocker(code, message) }

type fulfilItem struct {
	idx int            // index into the order's lines
	qty httpx.Quantity // the quantity billed now
}

type fulfilPlan struct {
	items []fulfilItem // non text lines, in line order
}

// kitPerComponent is a kit component's billed quantity for kitQty of its kit:
// floor(kitQty x componentQty / kitOrderedQty).
func kitComponentQty(kitQty, compQty, kitOrdered httpx.Quantity) httpx.Quantity {
	if kitOrdered <= 0 {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(int64(kitQty)), big.NewInt(int64(compQty)))
	return httpx.Quantity(new(big.Int).Div(n, big.NewInt(int64(kitOrdered))).Int64())
}

// wholeKitsAllocated is the most whole kits the components' allocations cover.
func wholeKitsAllocated(lines []OrderLine, kitAt int, comps []int) httpx.Quantity {
	kit := &lines[kitAt]
	k4 := big.NewInt(int64(*kit.Quantity))
	var best *big.Int
	for _, ci := range comps {
		c := &lines[ci]
		if c.Quantity == nil || *c.Quantity <= 0 {
			return 0
		}
		// kits covered = allocated x K / component quantity, in scale 4 kits
		n := new(big.Int).Mul(big.NewInt(int64(c.QuantityAllocated)), k4)
		n.Div(n, big.NewInt(int64(*c.Quantity)))
		if best == nil || n.Cmp(best) < 0 {
			best = n
		}
	}
	if best == nil {
		return 0
	}
	whole := new(big.Int).Div(best, big.NewInt(int64(salesdoc.One)))
	whole.Mul(whole, big.NewInt(int64(salesdoc.One)))
	return httpx.Quantity(whole.Int64())
}

func (o *Order) componentsOf() map[uuid.UUID][]int {
	out := map[uuid.UUID][]int{}
	for i := range o.Lines {
		if o.Lines[i].LineType == salesdoc.LineComponent && o.Lines[i].ParentLineID != nil {
			out[*o.Lines[i].ParentLineID] = append(out[*o.Lines[i].ParentLineID], i)
		}
	}
	return out
}

// planFulfilment decides what a fulfilment bills (ADR 0005 5.6). It reads the
// order as locked and changes nothing.
func planFulfilment(cur *Order, req FulfilRequest) (*fulfilPlan, error) {
	comps := cur.componentsOf()
	byID := map[uuid.UUID]int{}
	for i := range cur.Lines {
		byID[cur.Lines[i].ID] = i
	}
	billed := map[int]httpx.Quantity{}

	addKit := func(kitAt int, kitQty httpx.Quantity) {
		billed[kitAt] = kitQty
		for _, ci := range comps[cur.Lines[kitAt].ID] {
			if q := kitComponentQty(kitQty, *cur.Lines[ci].Quantity, *cur.Lines[kitAt].Quantity); q > 0 {
				billed[ci] = q
			}
		}
	}

	if len(req.Lines) == 0 {
		for i := range cur.Lines {
			l := &cur.Lines[i]
			if l.Quantity == nil {
				continue
			}
			switch {
			case l.LineType == salesdoc.LineKit:
				whole := wholeKitsAllocated(cur.Lines, i, comps[l.ID])
				if open := *l.Quantity - l.QuantityFulfilled; whole > open {
					whole = open
				}
				if whole > 0 {
					addKit(i, whole)
				}
			case l.LineType == salesdoc.LineComponent:
				// follows its kit
			case l.IsStocked():
				if l.QuantityAllocated > 0 {
					billed[i] = l.QuantityAllocated
				}
			case l.LineType == salesdoc.LineProduct, l.LineType == salesdoc.LineCharge:
				// a non stock product line bills its unbilled quantity; a charge
				// line bills in full on the first invoice that bills it
				if open := *l.Quantity - l.QuantityFulfilled; open > 0 {
					billed[i] = open
				}
			}
		}
	} else {
		seen := map[uuid.UUID]bool{}
		for n, e := range req.Lines {
			field := fmt.Sprintf("lines[%d]", n)
			at, ok := byID[e.OrderLineID]
			if !ok {
				return nil, validationErr(field+".order_line_id", "no such line on this order")
			}
			if seen[e.OrderLineID] {
				return nil, validationErr(field+".order_line_id", "is named twice")
			}
			seen[e.OrderLineID] = true
			l := &cur.Lines[at]
			if e.Quantity <= 0 {
				return nil, validationErr(field+".quantity", "must be greater than zero")
			}
			switch {
			case l.LineType == salesdoc.LineComponent:
				return nil, validationErr(field+".order_line_id", "is a kit component: fulfil the kit line, its components follow")
			case l.LineType == salesdoc.LineText:
				return nil, validationErr(field+".order_line_id", "is a text line: it carries no quantity to fulfil")
			case l.LineType == salesdoc.LineKit:
				if e.Quantity%salesdoc.One != 0 {
					return nil, validationErr(field+".quantity", "a kit is fulfilled in whole kits")
				}
				if e.Quantity > *l.Quantity-l.QuantityFulfilled {
					return nil, exceeds("exceeds_unbilled", fmt.Sprintf("%s asks for more kits than are left to fulfil", field))
				}
				if e.Quantity > wholeKitsAllocated(cur.Lines, at, comps[l.ID]) {
					return nil, exceeds("exceeds_allocation", fmt.Sprintf("%s asks for more kits than are allocated", field))
				}
				addKit(at, e.Quantity)
			case l.IsStocked():
				if e.Quantity > l.QuantityAllocated {
					return nil, exceeds("exceeds_allocation", fmt.Sprintf("%s asks for more than the %s allocated", field, l.QuantityAllocated.DecimalString()))
				}
				billed[at] = e.Quantity
			default: // a non stock product line, a charge line
				if e.Quantity > *l.Quantity-l.QuantityFulfilled {
					return nil, exceeds("exceeds_unbilled", fmt.Sprintf("%s asks for more than the %s left to bill", field, (*l.Quantity-l.QuantityFulfilled).DecimalString()))
				}
				billed[at] = e.Quantity
			}
		}
	}

	plan := &fulfilPlan{}
	for i := range cur.Lines {
		if q, ok := billed[i]; ok && q > 0 {
			plan.items = append(plan.items, fulfilItem{idx: i, qty: q})
		}
	}
	if len(plan.items) == 0 {
		return nil, nothingToFulfil()
	}
	return plan, nil
}

// billLines is the invoice body of a plan, in the order's line order with
// text lines copied and kit components after their kit: each billed line's
// extension is the difference of two cumulative totals (salesdoc.BilledTotal),
// so the invoices of a line sum to the order line's total to the cent. src[i]
// is the order line index bill[i] came from.
func billLines(cur *Order, plan *fulfilPlan, live map[uuid.UUID]LiveBilled) (bill []salesdoc.Line, src []int, discounts []httpx.Cents, err error) {
	qty := map[int]httpx.Quantity{}
	for _, it := range plan.items {
		qty[it.idx] = it.qty
	}
	for i := range cur.Lines {
		l := cur.Lines[i].Line
		if l.LineType == salesdoc.LineText {
			bill = append(bill, l)
			src = append(src, i)
			discounts = append(discounts, 0)
			continue
		}
		q, ok := qty[i]
		if !ok {
			continue
		}
		inv := l
		inv.Quantity = &q
		var discount httpx.Cents
		switch l.LineType {
		case salesdoc.LineComponent:
			zero := httpx.Cents(0)
			inv.LineTotal = &zero
		default:
			// against what the line's live invoices already carry, so a voided
			// piece never leaves the live pieces off the line's total by a cent
			lv := live[cur.Lines[i].ID]
			total, d, err := salesdoc.PieceAgainstLive(&l, cur.Lines[i].QuantityFulfilled+q, lv.TotalCents, lv.DiscountCents)
			if err != nil {
				return nil, nil, nil, err
			}
			inv.LineTotal, discount = &total, d
		}
		bill = append(bill, inv)
		src = append(src, i)
		discounts = append(discounts, discount)
	}
	return bill, src, discounts, nil
}

func billFingerprint(cur *Order, bill []salesdoc.Line) string {
	probe := Order{}
	probe.ShipToID = cur.ShipToID
	for i := range bill {
		probe.Lines = append(probe.Lines, OrderLine{Line: bill[i]})
	}
	return linesFingerprint(&probe)
}

// prepareFulfilTax prices the fulfilment's tax with the provider BEFORE the
// transaction opens (ADR 0005 section 3): the provider is an HTTP call and
// never runs inside a transaction that holds locks. Nil means "resolve by rate
// inside the transaction". A refusal the transaction will repeat (nothing to
// fulfil, a bad line) is left to it.
func (s *Service) prepareFulfilTax(ctx context.Context, id uuid.UUID, req FulfilRequest) (*providerTax, error) {
	if s.tax == nil || !s.tax.Configured() {
		return nil, nil
	}
	cur, err := s.repo.GetOrder(ctx, id)
	if err != nil {
		return nil, nil
	}
	exempt, err := s.repo.CustomerExempt(ctx, cur.CustomerID)
	if err != nil {
		return nil, err
	}
	if exempt {
		return nil, nil
	}
	plan, err := planFulfilment(cur, req)
	if err != nil {
		return nil, nil
	}
	live, err := s.repo.LiveBilledByLine(ctx, cur.ID)
	if err != nil {
		return nil, nil
	}
	bill, _, _, err := billLines(cur, plan, live)
	if err != nil {
		return nil, nil
	}
	totals := salesdoc.SumTotals(bill)
	probe := Order{}
	probe.CustomerID = cur.CustomerID
	for i := range bill {
		probe.Lines = append(probe.Lines, OrderLine{Line: bill[i]})
	}
	result, err := s.tax.PreviewTax(ctx, taxRequest(&probe, totals.TaxableCents))
	if err != nil {
		return nil, &httpx.Error{Status: http.StatusServiceUnavailable, Code: httpx.CodeUnavailable,
			Message: "the tax provider did not answer: the order is not fulfilled"}
	}
	return &providerTax{taxCents: httpx.Cents(result.TotalTax), fingerprint: billFingerprint(cur, bill)}, nil
}

// Fulfil is the money moment (ADR 0005 5.6): one transaction that moves the
// stock, builds and stores the invoice, posts its entry with the cost of goods
// sold and the subledger debit, updates the lines and the status, and writes
// the events last. pre is the client's revision; the in process caller sends
// none.
func (s *Service) Fulfil(ctx context.Context, id uuid.UUID, pre *Precondition, req FulfilRequest) (*Fulfilment, error) {
	if pre != nil && pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	priced, err := s.prepareFulfilTax(ctx, id, req)
	if err != nil {
		return nil, err
	}
	return s.fulfil(ctx, id, pre, req, priced, nil)
}

// errRequestGone is the claim's answer when another worker holds the request
// or it is already gone: the serve skips it without a failure.
var errRequestGone = errors.New("order: the fulfilment request is gone")

// fulfil is the fulfilment's transaction. claim, when set, runs first inside
// it: the queue worker takes its request row there (lock order step 0, before
// the order row).
func (s *Service) fulfil(ctx context.Context, id uuid.UUID, pre *Precondition, req FulfilRequest, priced *providerTax,
	claim func(ctx context.Context) (bool, error)) (*Fulfilment, error) {
	var out *Fulfilment
	err := s.inTx(ctx, func(ctx context.Context) error {
		if claim != nil {
			ok, err := claim(ctx)
			if err != nil {
				return err
			}
			if !ok {
				return errRequestGone
			}
		}
		if err := s.repo.LockOrder(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetOrder(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if !req.InProcess {
			if err := s.checkOrderBranch(ctx, cur); err != nil {
				return err
			}
		}
		if s.invoices == nil {
			return errors.New("the invoice writer is not wired")
		}
		if pre != nil {
			if err := pre.check(cur.Revision); err != nil {
				return err
			}
		}
		if cur.Status == StatusFulfilled && req.InProcess {
			// the desk (or the migration) already billed everything
			return nothingToFulfil()
		}
		if cur.Status != StatusConfirmed && cur.Status != StatusBackordered {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot fulfil an order in %s", cur.Status.Status()))
		}
		if err := s.checkFulfilFields(ctx, cur, &req); err != nil {
			return err
		}
		// One customer's credit-reading acts serialize (ADR 0005 section 11).
		if err := s.repo.LockCustomerCredit(ctx, cur.CustomerID); err != nil {
			return err
		}
		if err := s.fulfilChecks(ctx, cur, req); err != nil {
			return err
		}
		// Step 2: the order's payments with an unapplied amount, in id order,
		// before stock (section 11, step 2).
		deposits, err := s.repo.LockOrderPayments(ctx, cur.ID)
		if err != nil {
			return err
		}

		plan, err := planFulfilment(cur, req)
		if err != nil {
			return err
		}
		live, err := s.repo.LiveBilledByLine(ctx, cur.ID)
		if err != nil {
			return err
		}
		bill, src, discounts, err := billLines(cur, plan, live)
		if err != nil {
			return err
		}

		// Tax: the provider answer minted before the transaction (verified
		// against what is billed now) or the rate resolver.
		totals := salesdoc.SumTotals(bill)
		inv := &invoice.FulfilmentInvoice{
			ID: uuid.New(), BranchID: cur.BranchID, OrderID: cur.ID, CustomerID: cur.CustomerID, Currency: cur.Currency,
			DeliveryType: string(cur.DeliveryType), ShipToID: cur.ShipToID, ProjectID: cur.JobID, DeliveryID: req.DeliveryID,
			Actor: req.Actor,
		}
		if req.PickedUpBy != "" {
			p := req.PickedUpBy
			inv.PickedUpBy = &p
		}
		if cur.ShipTo != nil {
			raw, err := jsonMarshal(cur.ShipTo)
			if err != nil {
				return err
			}
			inv.ShipToSnapshot = raw
		}
		date, err := s.repo.BranchLocalDate(ctx, cur.BranchID, s.now())
		if err != nil {
			return err
		}
		inv.InvoiceDate = date
		if priced != nil {
			if billFingerprint(cur, bill) != priced.fingerprint {
				return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
					Message: "the order's lines moved between the tax price and the fulfilment",
					Details: []httpx.FieldError{httpx.Blocker("tax_quote_stale",
						"the order changed while its tax was priced: retry the fulfilment to price afresh")}}
			}
			inv.TaxCents = int64(priced.taxCents)
			inv.TaxSource = string(salesdoc.TaxSourceProvider)
		} else {
			exempt, err := s.repo.CustomerExempt(ctx, cur.CustomerID)
			if err != nil {
				return err
			}
			inputs, err := s.taxInputs(ctx, cur, exempt)
			if err != nil {
				return err
			}
			resolved, err := salesdoc.ResolveTax(inputs)
			if err != nil {
				if errors.Is(err, salesdoc.ErrTaxRateNotConfigured) {
					return conflictBlocker("tax_rate_not_configured",
						"set the branch's default tax rate (or the ship-to's rate) before fulfilling the order")
				}
				return err
			}
			scaled, _, err := salesdoc.ParseTaxRate(*resolved.Rate)
			if err != nil {
				return err
			}
			inv.TaxCents = int64(salesdoc.TaxAt(totals.TaxableCents, scaled))
			inv.TaxRate = resolved.Rate
			inv.TaxExempt = resolved.Source == salesdoc.TaxSourceExempt
			inv.TaxSource = string(resolved.Source)
		}
		inv.SubtotalCents = int64(totals.SubtotalCents)
		inv.TotalCents = inv.SubtotalCents + inv.TaxCents
		for _, g := range salesdoc.RevenueGroups(bill) {
			inv.Revenue = append(inv.Revenue, invoice.RevenueLeg{AccountCode: g.AccountCode, Cents: int64(g.Cents)})
		}

		// Cost and stock: the stocked lines in (product id, line id) order
		// (section 11 step 6), each billed quantity out of its allocation at
		// the cost read now (8.4).
		costs, err := s.lineCosts(ctx, cur, plan)
		if err != nil {
			return err
		}
		if s.inventory != nil {
			for _, i := range stockedIndexes(cur.Lines) {
				var q httpx.Quantity
				for _, it := range plan.items {
					if it.idx == i {
						q = it.qty
					}
				}
				if q <= 0 {
					continue
				}
				l := &cur.Lines[i]
				if err := s.inventory.FulfillQty(ctx, *l.ProductID, cur.BranchID, q); err != nil {
					if errors.Is(err, inventory.ErrInsufficientAllocated) {
						return exceeds("exceeds_allocation", fmt.Sprintf("line %s asks for more than inventory holds allocated", l.ID))
					}
					return err
				}
			}
		}

		// The invoice lines.
		newID := map[uuid.UUID]uuid.UUID{} // order line id -> invoice line id
		for n := range bill {
			b := &bill[n]
			ol := &cur.Lines[src[n]]
			fl := invoice.FulfilmentLine{
				ID: uuid.New(), Position: n, LineType: string(b.LineType), ProductID: b.ProductID, ChargeCodeID: b.ChargeCodeID,
				SKU: b.SKU, Description: b.Description, UOM: b.UOM, PriceUOM: b.PriceUOM,
				PriceSource: string(b.PriceSource), Taxable: b.Taxable, RevenueAccountCode: b.RevenueAccountCode,
				OverrideReason: b.OverrideReason, PriceAdjustedBy: b.PriceAdjustedBy,
			}
			if b.PricedUnitPrice != nil {
				fl.PricedUnitPrice = i64(int64(*b.PricedUnitPrice))
			}
			olID := ol.ID
			fl.OrderLineID = &olID
			newID[ol.ID] = fl.ID
			if b.ParentLineID != nil {
				if pid, ok := newID[*b.ParentLineID]; ok {
					fl.ParentLineID = &pid
				}
			}
			if b.LineType != salesdoc.LineText {
				fl.Quantity = i64(int64(*b.Quantity))
				fl.UOMQty, fl.PriceUOMQty = i64(int64(derefQty(b.UOMQty))), i64(int64(derefQty(b.PriceUOMQty)))
				fl.UnitPrice = i64(int64(derefPrice(b.UnitPrice)))
				fl.LineTotalCents = i64(int64(*b.LineTotal))
				if b.DiscountPercent != nil {
					fl.DiscountPercent = i64(int64(*b.DiscountPercent))
				}
				if b.DiscountAmount != nil {
					// the invoice line's share of the amount discount: its gross piece
					// less its net, so the shares sum to the order line's discount
					fl.DiscountCents = i64(int64(discounts[n]))
				}
				fl.DiscountReason = b.DiscountReason
				if c, ok := costs[src[n]]; ok {
					fl.UnitCost = i64(int64(c.unit))
					fl.CostCents = int64(c.cents)
					inv.CostCents += int64(c.cents)
				}
			} else {
				fl.OrderLineID = nil
			}
			inv.Lines = append(inv.Lines, fl)
		}
		if err := s.invoices.CreateFulfilmentInvoice(ctx, inv); err != nil {
			return err
		}
		// Step 7: apply the order's deposits to the invoice just billed, oldest
		// first, up to its total (the AR core caps each at what is open).
		fx := inv.Effects
		if len(deposits) > 0 && s.deposits != nil && inv.TotalCents > 0 {
			on := inv.InvoiceDate
			dfx, err := s.deposits.ApplyDeposits(ctx, inv.ID, deposits, on, req.Actor)
			if err != nil {
				return err
			}
			if fx == nil {
				fx = dfx
			} else {
				fx.Merge(dfx)
			}
		}

		// Update the lines and derive the status.
		for _, it := range plan.items {
			l := &cur.Lines[it.idx]
			if l.IsStocked() {
				l.QuantityAllocated -= it.qty
			}
			l.QuantityFulfilled += it.qty
		}
		if err := s.repo.SaveLineQuantities(ctx, cur.Lines); err != nil {
			return err
		}
		from := cur.Status
		cur.Status = deriveStatus(cur)
		if err := s.repo.SaveTransition(ctx, cur); err != nil {
			return err
		}
		updated, err := s.repo.GetOrder(ctx, cur.ID)
		if err != nil {
			return err
		}

		// Events last: invoice.created, then the order's.
		if err := s.recordInvoice(ctx, updated, inv); err != nil {
			return err
		}
		if fx != nil {
			for _, ev := range fx.Events() {
				if err := s.recordEvent(ctx, ev); err != nil {
					return err
				}
			}
		}
		event := EventPartiallyFulfilled
		if updated.Status == StatusFulfilled {
			event = EventFulfilled
		}
		if err := s.record(ctx, updated, event, from.Status()); err != nil {
			return err
		}
		out = &Fulfilment{Order: updated, InvoiceID: inv.ID}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func i64(v int64) *int64 { return &v }

func derefQty(q *httpx.Quantity) httpx.Quantity {
	if q == nil {
		return 0
	}
	return *q
}

func derefPrice(p *httpx.Price) httpx.Price {
	if p == nil {
		return 0
	}
	return *p
}

// checkFulfilFields holds the request fields to the order: a will-call
// (pickup) fulfilment names who collected (1 to 200 characters), a delivery
// fulfilment must not; a delivery id must be one of this order's deliveries.
func (s *Service) checkFulfilFields(ctx context.Context, cur *Order, req *FulfilRequest) error {
	req.PickedUpBy = strings.TrimSpace(req.PickedUpBy)
	switch cur.DeliveryType {
	case DeliveryPickup:
		if req.PickedUpBy == "" {
			return validationErr("picked_up_by", "is required to fulfil a will-call (pickup) order: the name of the person at the counter")
		}
		if len(req.PickedUpBy) > 200 {
			return validationErr("picked_up_by", "must be at most 200 characters")
		}
		if req.DeliveryID != nil {
			return validationErr("delivery_id", "belongs to a delivery order: a pickup order is never routed")
		}
	default:
		if req.PickedUpBy != "" {
			return validationErr("picked_up_by", "belongs to a pickup order")
		}
	}
	if req.DeliveryID != nil {
		orderID, ok, err := s.repo.DeliveryOrderID(ctx, *req.DeliveryID)
		if err != nil {
			return err
		}
		if !ok || orderID != cur.ID {
			return validationErr("delivery_id", "is not a delivery of this order")
		}
	}
	return nil
}

// fulfilChecks is step 1's re-check (ADR 0005 5.6): the price exposure gate
// and the credit, this order counting by its unbilled remainder (5.3). An over
// limit customer is 409 credit_limit with no hold. The in process caller (the
// delivery completion, 5.5) skips both and writes one audit row naming them
// and whether each would have refused.
func (s *Service) fulfilChecks(ctx context.Context, cur *Order, req FulfilRequest) error {
	var exposureErr error
	if s.exposure != nil {
		exposureErr = s.exposure.RequireClearForOrder(ctx, cur.ID)
	}
	creditOver := false
	facts, err := s.repo.CustomerFacts(ctx, cur.CustomerID)
	if err != nil {
		return err
	}
	if facts.CreditLimitCents != nil {
		open, err := s.repo.OpenReceivableCents(ctx, cur.CustomerID, &cur.ID)
		if err != nil {
			return err
		}
		remainder, err := s.repo.UnbilledRemainderCents(ctx, cur.ID)
		if err != nil {
			return err
		}
		creditOver = open+remainder > int64(*facts.CreditLimitCents)
	}
	if req.InProcess {
		if s.audit != nil {
			return s.audit.Log(ctx, audit.Entry{
				Action: auditChecksSkipped, EntityType: "order", EntityID: cur.ID, UserID: req.Actor,
				Changes: map[string]any{
					"checks_skipped":        []string{"price_exposure_gate", "credit_limit"},
					"price_exposure_refuse": exposureErr != nil,
					"credit_limit_refuse":   creditOver,
					"delivery_id":           req.DeliveryID,
				}})
		}
		return nil
	}
	if exposureErr != nil {
		return exposureErr
	}
	if creditOver {
		return conflictBlocker("credit_limit", "the customer is over the credit limit: fulfilling would add to the exposure")
	}
	return nil
}

type lineCost struct {
	unit  httpx.Price
	cents httpx.Cents
}

// lineCosts reads the cost of every billed line that carries one (ADR 0005
// 8.4, as PR 35 amends it and C2-2b implements ahead of that merge): a
// stocked line (a product line with a product, a component, a special order
// line included) the product's average unit cost read now, because its
// receipt entered stock and moved the average, so the purchase cost would
// leave a residue in 1030 against the stock that is left; a non stock line
// (no product, a direct ship whose goods never enter stock) the cost of the
// received purchase order line linked to it. Kit, charge and text lines carry
// none. A zero or missing cost posts no cost and is not an error.
func (s *Service) lineCosts(ctx context.Context, cur *Order, plan *fulfilPlan) (map[int]lineCost, error) {
	var ids []uuid.UUID
	for _, it := range plan.items {
		if p := cur.Lines[it.idx].ProductID; p != nil {
			ids = append(ids, *p)
		}
	}
	refs := map[string]salesdoc.ProductRef{}
	if len(ids) > 0 {
		var err error
		if refs, err = s.repo.LookupProducts(ctx, ids); err != nil {
			return nil, err
		}
	}
	out := map[int]lineCost{}
	for _, it := range plan.items {
		l := &cur.Lines[it.idx]
		if l.LineType == salesdoc.LineKit || l.LineType == salesdoc.LineCharge {
			continue
		}
		if l.ProductID == nil {
			// a non stock or direct ship line: relieved from the linked
			// receipts' posted values, never from one purchase line's cost
			rc, ok, err := s.repo.NonStockReceiptsFor(ctx, l.ID)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			cents := nonStockRelief(rc, l.QuantityFulfilled, it.qty)
			if cents <= 0 {
				continue
			}
			// the display figure: relieved cents per billed unit
			unit := httpx.Price((int64(cents)*1_000_000 + int64(it.qty)/2) / int64(it.qty))
			out[it.idx] = lineCost{unit: unit, cents: cents}
			continue
		}
		var unit httpx.Price
		if ref, ok := refs[l.ProductID.String()]; ok {
			unit = ref.AverageCost
		}
		if unit <= 0 {
			continue
		}
		out[it.idx] = lineCost{unit: unit, cents: salesdoc.CostOf(it.qty, unit)}
	}
	return out, nil
}

// nonStockRelief is the cost one bill of a non stock or direct ship line
// relieves from 1030 (ADR 0005 8.4 as PR 35 amends it): the unrelieved posted
// value of the linked receipts x the billed quantity / (the received quantity
// less the quantity billed before), rounded half away from zero, and the bill
// that brings billed up to received takes the unrelieved remainder, so the
// line's bills relieve exactly what its receipts posted. A bill ahead of the
// receipts relieves nothing.
func nonStockRelief(rc NonStockReceipts, billedBefore, qty httpx.Quantity) httpx.Cents {
	unrelieved := int64(rc.PostedCents - rc.RelievedCents)
	remaining := int64(rc.Received - billedBefore)
	if unrelieved <= 0 || remaining <= 0 || qty <= 0 {
		return 0
	}
	if int64(qty) >= remaining {
		return httpx.Cents(unrelieved)
	}
	n := new(big.Int).Mul(big.NewInt(unrelieved), big.NewInt(int64(qty)))
	d := big.NewInt(remaining)
	n.Add(n, new(big.Int).Rsh(d, 1))
	n.Div(n, d)
	return httpx.Cents(n.Int64())
}

// recordInvoice writes invoice.created through the transaction's executor, and
// invoice.paid after it for a zero total (ADR 0005 6.2: an invoice with
// nothing owed is created paid).
// recordEvent writes one event the AR core's act produced, last.
func (s *Service) recordEvent(ctx context.Context, ev outbox.Event) error {
	if s.events == nil {
		return nil
	}
	return s.events.Write(ctx, ev)
}

func (s *Service) recordInvoice(ctx context.Context, o *Order, inv *invoice.FulfilmentInvoice) error {
	if s.events == nil {
		return nil
	}
	status := "unpaid"
	if inv.TotalCents == 0 {
		status = "paid"
	}
	raw, err := jsonMarshal(map[string]any{
		"number": inv.Number, "customer_id": inv.CustomerID, "order_id": inv.OrderID, "status": status,
		"currency": inv.Currency, "total_cents": inv.TotalCents, "subtotal_cents": inv.SubtotalCents,
		"tax_cents": inv.TaxCents, "delivery_id": inv.DeliveryID,
	})
	if err != nil {
		return err
	}
	branch := o.BranchID
	if err := s.events.Write(ctx, outbox.Event{
		Type: EventInvoiceCreated, EntityType: "invoice", EntityID: inv.ID, BranchID: &branch, Data: raw,
	}); err != nil {
		return err
	}
	if inv.TotalCents != 0 {
		return nil
	}
	paid, err := jsonMarshal(map[string]any{
		"number": inv.Number, "customer_id": inv.CustomerID, "order_id": inv.OrderID, "status": "paid",
		"from_status": "unpaid", "currency": inv.Currency, "total_cents": inv.TotalCents,
	})
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{
		Type: "invoice.paid", EntityType: "invoice", EntityID: inv.ID, BranchID: &branch, Data: paid,
	})
}

// validationErr is a 400 naming one field.
func validationErr(field, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed,
		Message: "one or more fields failed validation",
		Details: []httpx.FieldError{{Field: field, Message: message}}}
}
