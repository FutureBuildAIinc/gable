// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/units"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// ReturnIn is a parsed counter return request.
type ReturnIn struct {
	RegisterID     string
	OriginalSaleID *uuid.UUID
	CustomerID     *uuid.UUID
	RefundMethod   RefundMethod
	Reason         string
	GatewayTxID    string
	Lines          []ReturnLineIn
	// Exchange marks the return half of an exchange: its credit is applied to
	// the replacement sale in the same act, so the walk-in customer's return
	// is no credit left on an account.
	Exchange bool
}

// ReturnLineIn is one parsed returned line.
type ReturnLineIn struct {
	ProductID   *uuid.UUID
	SaleLineID  *uuid.UUID
	Description string
	Quantity    httpx.Quantity
	UOM         string
	UnitPrice   *httpx.Price
	Restock     bool
	// ReasonCode is the line's reason from the closed list; absent is other.
	ReasonCode string
}

// returnLinePriced is a returned line with its money resolved.
type returnLinePriced struct {
	in        ReturnLineIn
	lineTotal int64 // positive; the memo line stores it negative
	taxable   bool
	// saleLine is the sale line a linked return names: the refund follows it
	// (its per unit extension, its unit, its tax share).
	saleLine *salesdoc.Line
	// perUnit is the ten thousandths a linked line refunds at: the sale
	// line's own extension divided by the quantity it sold.
	perUnit httpx.Price
	// chargeCodeID and lineType carry a linked charge line's own shape.
	chargeCodeID *uuid.UUID
	lineType     string
	// feeCents is the restocking fee withheld from this line's refund, set
	// with the totals (resolveReturnMoney).
	feeCents int64
	uom      string
	stockUOM *string
	stockQty *httpx.Quantity
}

// returnState is a priced, guarded return ready for its money transaction:
// every figure the transaction restates under the sale row lock lives here,
// so the operator resolution replays a stranded claim's booking through the
// same state and the same rules.
type returnState struct {
	in           *ReturnIn
	cashierID    uuid.UUID
	customerID   *uuid.UUID
	branchID     *uuid.UUID
	currency     string
	actor        string
	saleLines    []salesdoc.Line
	byID         map[uuid.UUID]salesdoc.Line
	legacyBearer map[uuid.UUID]uuid.UUID
	refs         map[string]salesdoc.ProductRef
	priced       []returnLinePriced
	saleTaxCents int64
	saleTaxable  int64
	// the restated figures: read before the transaction, restated under the
	// sale row lock inside it, and written from
	taxAlreadyBack   int64
	backByLine       map[uuid.UUID]int64
	completesTaxable bool
	subtotal         int64
	taxCents         int64
	total            int64
	taxRate          *string
	// feeBps is the restocking fee of each reason, read once with the pricing;
	// feeCents is the whole fee withheld, a positive figure that lowers the
	// credit (subtotal and total carry it).
	feeBps   map[string]int
	feeCents int64
	// restock is the RESTOCK charge code the fee line of the memo is booked
	// to, resolved when a fee is withheld.
	restock *salesdoc.ChargeCode
}

// refundClaim is the committed intent a card refund stands on: what it
// claimed, what the tender kept, and the gateway's own refund id once the
// terminal answered. resolveAudit, when set, is written inside the money
// transaction: the operator resolution's own record, rolling back with the
// booking it names. replay marks the operator resolution's replay of a
// stranded claim: its money moved long before the act stranded, so a refused
// replay writes no orphan row (the claim and the act's own rows already
// record it; tenth review P3-2).
type refundClaim struct {
	intent       RefundIntent
	tenderCap    int64
	refundTxID   string
	resolveAudit *audit.Entry
	replay       bool
}

// priceReturn resolves the return's customer, prices and guards its lines and
// reads the sale's tax facts: everything the money transaction restates under
// the sale row lock, gathered once. The guards that need no lock run here so
// a refusal never moves money at the terminal.
//
// holdCustomer puts a named customer, the request's or the original sale's,
// through the counter's one customer rule (Service.namedCustomer) before
// anything is written: a hidden customer answers the module's 404. The
// operator resolution's replay of a stranded claim passes false: its money
// moved long before, and the replay restates what the claim recorded.
func (s *Service) priceReturn(ctx context.Context, in *ReturnIn, holdCustomer bool) (*returnState, error) {
	branchID, err := s.repo.GetRegisterBranch(ctx, in.RegisterID)
	if err != nil {
		return nil, err
	}
	// The customer: named, the original sale's, or the walk-in.
	customerID := in.CustomerID
	var original *Sale
	if in.OriginalSaleID != nil {
		if original, err = s.repo.GetSale(ctx, *in.OriginalSaleID); err != nil {
			return nil, err
		}
		// The status refusal runs before the gateway is asked for money (a
		// refusal must never move money at the terminal); the transaction
		// rechecks it under the sale row lock.
		if original.Status != StatusCompleted {
			return nil, httpx.InvalidStateTransition(
				fmt.Sprintf("cannot return against a %s sale: a voided sale's goods came back with the void", original.Status.Status()))
		}
		// Every refund method but the card's own consults the intent table
		// before the return prices (ninth review P1-2): a card claim PENDING
		// at the terminal may already have paid these goods back, so neither
		// the drawer nor the account pays them a second time. The money
		// transaction rechecks it under the sale row lock. A tender the sale's
		// void already refunded (a partly resolved void) blocks every method
		// the same way.
		if in.RefundMethod != RefundCard {
			if pending, err := s.repo.SaleHasPendingRefundIntent(ctx, original.ID); err != nil {
				return nil, err
			} else if pending {
				return nil, conflict("refund_in_flight",
					"a card refund of this sale is still in flight: settle it before refunding by any other method")
			}
			if doneVoid, err := s.repo.SaleHasVoidIntentDone(ctx, original.ID); err != nil {
				return nil, err
			} else if doneVoid {
				return nil, conflict("void_refund_recorded",
					"this sale's void already refunded a card tender at the terminal: reconcile the void before returning goods against this sale")
			}
		}
		// A return that names the sale names its lines (third review P1-1):
		// a product line at a client price would stand outside every cap, so
		// every line of a named sale carries the sale line it returns. Only
		// a return that names no sale stands free.
		for i := range in.Lines {
			if in.Lines[i].SaleLineID == nil {
				return nil, invalid(fmt.Sprintf("lines[%d].line_id", i),
					"a return that names the original sale names the line of it: every line carries line_id")
			}
		}
		// A return against a sale credits that sale's customer: a named
		// customer other than the sale's is refused, and a walk-in sale's
		// goods go back by the original tenders' methods, never onto an
		// account.
		if in.CustomerID != nil && (original.CustomerID == nil || *original.CustomerID != *in.CustomerID) {
			walkInID, _, err := s.repo.WalkInCustomer(ctx)
			if err != nil {
				return nil, err
			}
			if original.CustomerID != nil || walkInID != *in.CustomerID {
				return nil, invalid("customer_id",
					"a return against a sale credits the sale's own customer: leave the customer off, or name the sale's")
			}
		}
		customerID = original.CustomerID
		if customerID == nil && in.RefundMethod == RefundAccount && !in.Exchange {
			return nil, invalid("refund_method",
				"the sale named no customer: its goods are refunded to the drawer or the card, not to an account")
		}
	}
	isWalkIn := false
	if customerID == nil {
		walkInID, _, err := s.repo.WalkInCustomer(ctx)
		if err != nil {
			return nil, err
		}
		customerID = &walkInID
		isWalkIn = true
	} else if walkInID, _, err := s.repo.WalkInCustomer(ctx); err != nil {
		return nil, err
	} else if walkInID == *customerID {
		// naming the walk-in customer's id explicitly is still the walk-in
		// (third review P3-D): the credit a return leaves open cannot sit
		// on it whatever way it was named
		isWalkIn = true
	}
	// The walk-in customer cannot hold account credit: as the sale side
	// refuses an ACCOUNT tender for it, a return left on its account is
	// refused the same way.
	if isWalkIn && in.RefundMethod == RefundAccount && !in.Exchange {
		return nil, conflict("walk_in_account",
			"account credit needs a named customer: the walk-in customer is refunded from the drawer or to the card")
	}
	var facts CustomerFacts
	if holdCustomer && !isWalkIn {
		if _, facts, err = s.namedCustomer(ctx, *customerID); err != nil {
			return nil, err
		}
	} else if facts, err = s.repo.CustomerFacts(ctx, *customerID); err != nil {
		return nil, err
	}
	// Price the lines: a line naming a sale line refunds at that line's own
	// extension per unit (never the list price, never a client price); a line
	// that named no sale line stands alone on the product the request named
	// and the request's own price.
	var saleLines []salesdoc.Line
	if in.OriginalSaleID != nil {
		if saleLines, err = s.repo.GetLines(ctx, *in.OriginalSaleID); err != nil {
			return nil, err
		}
	}
	byID := map[uuid.UUID]salesdoc.Line{}
	for i := range saleLines {
		byID[saleLines[i].ID] = saleLines[i]
	}
	// The bearer of the legacy (unlinked) returns of each product: its first
	// line by position, stable across requests (sixth review P1-1 and P3-2).
	// A legacy return names no sale line, so its quantity and cents are
	// attributed here once, to one line of the product, instead of counting
	// against every line of it; whichever line bears them, the sale's goods
	// bound its refunds.
	legacyBearer := map[uuid.UUID]uuid.UUID{}
	for i := range saleLines {
		if saleLines[i].ProductID != nil {
			if _, ok := legacyBearer[*saleLines[i].ProductID]; !ok {
				legacyBearer[*saleLines[i].ProductID] = saleLines[i].ID
			}
		}
	}
	// First pass: fill each line from the sale line it names, so the product
	// lookup covers the resolved products too. One request names one sale
	// line at most once (sixth review P2-2, fifth review P2-2): each
	// duplicate would price its own rounded share against the same earlier
	// total, refunding a cent more per duplicate than the sale took; the
	// quantity belongs in one line.
	seenLine := map[uuid.UUID]bool{}
	resolved := make([]ReturnLineIn, len(in.Lines))
	for i := range in.Lines {
		rl := in.Lines[i]
		if rl.SaleLineID != nil {
			if seenLine[*rl.SaleLineID] {
				return nil, invalid(fmt.Sprintf("lines[%d].line_id", i),
					"names the sale line twice: return it once, with the whole quantity in one line")
			}
			seenLine[*rl.SaleLineID] = true
			src, ok := byID[*rl.SaleLineID]
			if !ok {
				return nil, invalid(fmt.Sprintf("lines[%d].line_id", i), "names no line of the original sale")
			}
			if rl.UnitPrice != nil {
				return nil, invalid(fmt.Sprintf("lines[%d].unit_price_ten_thousandths", i),
					"a line that names a sale line refunds at the sale's own price: drop the unit price")
			}
			if rl.Description == "" {
				rl.Description = src.Description
			}
			if rl.ProductID == nil && src.ProductID != nil {
				p := *src.ProductID
				rl.ProductID = &p
			}
		}
		resolved[i] = rl
	}
	productIDs := make([]uuid.UUID, 0, len(resolved)+len(saleLines))
	for i := range resolved {
		if resolved[i].ProductID != nil {
			productIDs = append(productIDs, *resolved[i].ProductID)
		}
	}
	for i := range saleLines {
		if saleLines[i].ProductID != nil {
			productIDs = append(productIDs, *saleLines[i].ProductID)
		}
	}
	refs, err := s.repo.LookupProducts(ctx, productIDs)
	if err != nil {
		return nil, err
	}
	unitSets, err := s.repo.LookupProductUnits(ctx, productIDs)
	if err != nil {
		return nil, err
	}
	priced := make([]returnLinePriced, 0, len(resolved))
	for i := range resolved {
		rl := resolved[i]
		if rl.SaleLineID == nil && rl.ProductID != nil {
			if _, ok := refs[rl.ProductID.String()]; !ok {
				return nil, invalid(fmt.Sprintf("lines[%d].product_id", i), "no such product")
			}
		}
		var src salesdoc.Line
		if rl.SaleLineID != nil {
			src = byID[*rl.SaleLineID]
			// A kit's components never restock on their own line: the
			// component carries no price, the kit line carries the return.
			if src.LineType == salesdoc.LineComponent && rl.Restock {
				return nil, invalid(fmt.Sprintf("lines[%d].restock", i),
					"a kit component returns with its kit line: name the kit line, its components follow")
			}
			if rl.Restock {
				if src.LineType == salesdoc.LineKit {
					for j := range saleLines {
						comp := &saleLines[j]
						if comp.ParentLineID == nil || *comp.ParentLineID != src.ID || comp.ProductID == nil || comp.StockUOM == nil || *comp.StockUOM == "" {
							continue
						}
						if ref, ok := refs[comp.ProductID.String()]; ok && ref.UOMPrimary != "" && *comp.StockUOM != ref.UOMPrimary {
							return nil, salesdoc.StockUnitChangedError(i, *comp.StockUOM, ref.UOMPrimary)
						}
					}
				} else if src.ProductID != nil && src.StockUOM != nil && *src.StockUOM != "" {
					if ref, ok := refs[src.ProductID.String()]; ok && ref.UOMPrimary != "" && *src.StockUOM != ref.UOMPrimary {
						return nil, salesdoc.StockUnitChangedError(i, *src.StockUOM, ref.UOMPrimary)
					}
				}
			}
			// The line's own extension per unit: the extension the sale
			// charged (discount included) divided by the quantity it sold
			// (cents at scale 2 over quantity at scale 4, to a price at
			// scale 4: the divisor carries 10^6).
			if src.LineTotal == nil || src.Quantity == nil || *src.Quantity <= 0 || *src.LineTotal < 0 {
				return nil, invalid(fmt.Sprintf("lines[%d].line_id", i), "the sale line carries no priced extension to return against")
			}
			perUnit := httpx.Price(divRound(int64(*src.LineTotal)*1_000_000, int64(*src.Quantity)))
			p := returnLinePriced{in: rl, lineTotal: int64(salesdoc.CostOf(rl.Quantity, perUnit)),
				taxable: src.Taxable, saleLine: &src, perUnit: perUnit,
				chargeCodeID: src.ChargeCodeID, lineType: string(src.LineType)}
			up := perUnit
			p.in.UnitPrice = &up
			priced = append(priced, p)
			continue
		}
		if rl.UnitPrice == nil {
			return nil, invalid("lines.unit_price_ten_thousandths",
				"is required when the line names no line of the original sale")
		}
		taxable := true
		uom := rl.UOM
		var stockUOM *string
		var stockQty *httpx.Quantity
		if rl.ProductID != nil {
			if ref, ok := refs[rl.ProductID.String()]; ok {
				taxable = ref.Taxable
				uSet := unitSets[*rl.ProductID]
				if uom == "" {
					if uSet.SaleUOM != "" {
						uom = uSet.SaleUOM
					} else if ref.UOMPrimary != "" {
						uom = ref.UOMPrimary
					} else {
						uom = "EA"
					}
				}
				su := ref.UOMPrimary
				if su == "" {
					su = uom
				}
				sq := rl.Quantity
				if uom != su {
					uRow, ok := uSet.Row(uom)
					if !ok || !uRow.Sell {
						return nil, invalid(fmt.Sprintf("lines[%d].uom", i), "is not a sale unit of this product")
					}
					converted, err := units.ConvertStock(rl.Quantity, uom, su, uRow.Pair())
					if err != nil {
						return nil, invalid(fmt.Sprintf("lines[%d].quantity", i), err.Error())
					}
					sq = converted
				} else if rl.UOM != "" && len(uSet.Rows) > 0 {
					if uRow, ok := uSet.Row(uom); !ok || !uRow.Sell {
						return nil, invalid(fmt.Sprintf("lines[%d].uom", i), "is not a sale unit of this product")
					}
				}
				stockUOM = &su
				stockQty = &sq
			}
		}
		if uom == "" {
			uom = "EA"
		}
		priced = append(priced, returnLinePriced{in: rl, lineTotal: int64(salesdoc.CostOf(rl.Quantity, *rl.UnitPrice)),
			taxable: taxable, uom: uom, stockUOM: stockUOM, stockQty: stockQty})
	}
	// The cap (ADR 0005 14.2 C2-5, second review P1-1): no sale line gives
	// back more than it sold less every earlier return of it. Checked before
	// the transaction, and again under the sale row lock inside it.
	if err := s.checkReturnCaps(ctx, in.OriginalSaleID, priced, byID); err != nil {
		return nil, err
	}
	// Tax: a linked line's share of the tax the sale actually collected (the
	// sale's own rate, provider priced or exempt, whatever it was); a free
	// line at today's branch rate, exemptions first.
	var saleTaxCents, saleTaxable int64
	if original != nil {
		saleTaxCents = int64(original.TaxCents)
		for i := range saleLines {
			if saleLines[i].Taxable && saleLines[i].LineTotal != nil {
				saleTaxable += int64(*saleLines[i].LineTotal)
			}
		}
	}
	feeBps, err := s.repo.RestockingFeeBps(ctx)
	if err != nil {
		return nil, err
	}
	st := &returnState{in: in, branchID: branchID, customerID: customerID, currency: facts.Currency,
		saleLines: saleLines, byID: byID, legacyBearer: legacyBearer, refs: refs, priced: priced,
		saleTaxCents: saleTaxCents, saleTaxable: saleTaxable, feeBps: feeBps}
	if err := s.applyReturnRemainders(ctx, st); err != nil {
		return nil, err
	}
	if err := s.resolveReturnMoney(ctx, st); err != nil {
		return nil, err
	}
	return st, nil
}

// applyReturnRemainders restates each linked line's refund against what
// earlier returns already took (fourth review P2-1): the return that
// completes a line takes the line's whole total less every cent already
// refunded, so the rounded per unit shares of repeated partial returns never
// sum past what the sale was paid; the tax is capped the same way. Read
// before the transaction (the guards) and again under the sale row lock (the
// authority the memo is written from).
func (s *Service) applyReturnRemainders(ctx context.Context, st *returnState) error {
	if st.in.OriginalSaleID == nil {
		return nil
	}
	returned, err := s.repo.ReturnedQtyByLine(ctx, *st.in.OriginalSaleID)
	if err != nil {
		return err
	}
	refunded, err := s.repo.RefundedCentsByLine(ctx, *st.in.OriginalSaleID)
	if err != nil {
		return err
	}
	if st.taxAlreadyBack, err = s.repo.RefundedTaxCents(ctx, *st.in.OriginalSaleID); err != nil {
		return err
	}
	legacyCents, err := s.repo.LegacyRefundedCentsByProduct(ctx, *st.in.OriginalSaleID)
	if err != nil {
		return err
	}
	legacyQty, err := s.repo.LegacyReturnedQtyByProduct(ctx, *st.in.OriginalSaleID)
	if err != nil {
		return err
	}
	// the legacy quantity of a product bears on its bearer line, so a line
	// the legacy return partly brought back completes with the linked
	// returns that bring the rest (eighth review P3-1)
	legacyBack := func(l *salesdoc.Line) int64 {
		if l.ProductID != nil && st.legacyBearer[*l.ProductID] == l.ID {
			return int64(legacyQty[*l.ProductID])
		}
		return 0
	}
	st.backByLine = make(map[uuid.UUID]int64, len(returned))
	for id, qty := range returned {
		st.backByLine[id] = int64(qty)
	}
	// does this return finish every taxable line of the sale, counting
	// what the earlier returns already brought back?
	st.completesTaxable = false
	for i := range st.saleLines {
		l := &st.saleLines[i]
		if !l.Taxable || l.Quantity == nil {
			continue
		}
		st.completesTaxable = true
		back := int64(returned[l.ID]) + legacyBack(l)
		for j := range st.priced {
			if st.priced[j].saleLine != nil && st.priced[j].saleLine.ID == l.ID {
				back += int64(st.priced[j].in.Quantity)
			}
		}
		if back < int64(*l.Quantity) {
			st.completesTaxable = false
			break
		}
	}
	for i := range st.priced {
		p := &st.priced[i]
		if p.saleLine == nil || p.saleLine.Quantity == nil || p.saleLine.LineTotal == nil {
			continue
		}
		sold, back := int64(*p.saleLine.Quantity), int64(returned[p.saleLine.ID])+legacyBack(p.saleLine)
		remaining := int64(*p.saleLine.LineTotal) - refunded[p.saleLine.ID]
		// the legacy refund of the product lowers the bearer line's
		// remaining money, so the rounded shares of later linked
		// returns cannot pass what the sale took
		if p.saleLine.ProductID != nil && st.legacyBearer[*p.saleLine.ProductID] == p.saleLine.ID {
			remaining -= legacyCents[*p.saleLine.ProductID]
		}
		if back+int64(p.in.Quantity) >= sold {
			p.lineTotal = remaining
		} else if p.lineTotal > remaining {
			p.lineTotal = remaining
		}
		if p.lineTotal < 0 {
			p.lineTotal = 0
		}
	}
	return nil
}

// resolveReturnMoney restates the memo's totals from the lines' money; the
// transaction runs it again under the sale row lock after the caps recheck,
// and the memo, the legs, the refund and the return row are all written from
// its latest answer.
func (s *Service) resolveReturnMoney(ctx context.Context, st *returnState) error {
	var linkedTaxable int64
	for i := range st.priced {
		if st.priced[i].saleLine != nil && st.priced[i].taxable {
			linkedTaxable += st.priced[i].lineTotal
		}
	}
	st.subtotal = linkedTaxable
	var freeTaxable int64
	var freeNontaxable int64
	for i := range st.priced {
		if st.priced[i].saleLine == nil {
			if st.priced[i].taxable {
				freeTaxable += st.priced[i].lineTotal
			} else {
				freeNontaxable += st.priced[i].lineTotal
			}
		} else if !st.priced[i].taxable {
			st.subtotal += st.priced[i].lineTotal
		}
	}
	st.subtotal += freeTaxable + freeNontaxable
	st.taxCents = 0
	st.taxRate = nil
	if linkedTaxable > 0 && st.saleTaxable > 0 && st.saleTaxCents > 0 {
		st.taxCents += divRound(linkedTaxable*st.saleTaxCents, st.saleTaxable)
		// never more tax back than the sale collected less what earlier
		// returns took (the same remainder rule)
		if remaining := st.saleTaxCents - st.taxAlreadyBack; st.taxCents > remaining {
			st.taxCents = remaining
		}
		// the return that finishes every taxable line takes the rest of
		// the tax exactly (sixth review P3-1): the rounded shares of
		// repeated partial returns cannot leave cents owed to the
		// customer
		if st.completesTaxable {
			if remaining := st.saleTaxCents - st.taxAlreadyBack; remaining > st.taxCents {
				st.taxCents = remaining
			}
		}
	}
	if freeTaxable > 0 {
		exempt, err := s.exemptOn(ctx, *st.branchID, *st.customerID)
		if err != nil {
			return err
		}
		if exempt {
			zero := "0"
			st.taxRate = &zero
		} else {
			rate, ok, err := s.repo.BranchTaxRate(ctx, st.branchID)
			if err != nil {
				return err
			}
			if !ok {
				return conflict("tax_rate_not_configured", "set the branch's default tax rate before returning goods")
			}
			scaled, _, err := salesdoc.ParseTaxRate(rate)
			if err != nil {
				return err
			}
			st.taxCents += int64(salesdoc.TaxAt(httpx.Cents(freeTaxable), scaled))
			st.taxRate = &rate
		}
	}
	// The restocking fee each line's reason carries, taken from the line's own
	// refund (the figure the remainder rule left it), half even to the cent. It
	// lowers the credit: the subtotal the memo states and the total both carry
	// it, and the tax refunded stays whole.
	st.feeCents = 0
	for i := range st.priced {
		p := &st.priced[i]
		code := p.in.ReasonCode
		if code == "" {
			code = ReasonOther
		}
		p.feeCents = restockingFee(p.lineTotal, st.feeBps[code])
		st.feeCents += p.feeCents
	}
	st.total = -(st.subtotal + st.taxCents - st.feeCents)
	return nil
}

// ReturnSale records a counter return (ADR 0005 section 14.2 C2-5): a
// credit memo created and posted in ONE act with its restock lines,
// refunded in cash or card or left as account credit. The card refund goes
// through the gateway before the transaction (section 11, as every gateway
// call does), claimed first by a committed refund intent unique per sale
// and tender so a concurrent card return or a void of the same tender never
// reaches the terminal twice; the memo's entry, subledger row, the stock
// return and the refund commit together.
func (s *Service) ReturnSale(ctx context.Context, cashierID uuid.UUID, in *ReturnIn, actor string) (*Return, error) {
	if s.ar == nil || s.inventory == nil {
		return nil, errNotConfigured
	}
	st, err := s.priceReturn(ctx, in, true)
	if err != nil {
		return nil, err
	}
	st.cashierID, st.actor = cashierID, actor
	var claim *refundClaim
	if in.RefundMethod == RefundCard {
		if in.GatewayTxID != "" {
			return nil, invalid("gateway_tx_id",
				"the card refund goes to the card the sale was paid with: drop gateway_tx_id")
		}
		if in.OriginalSaleID == nil {
			return nil, conflict("card_refund_needs_sale",
				"a card return needs the sale it refunds: no sale, no card of the business to refund")
		}
		cardTenders, err := s.repo.GetTenders(ctx, *in.OriginalSaleID)
		if err != nil {
			return nil, err
		}
		var target *Tender
		for i := range cardTenders {
			t := &cardTenders[i]
			if t.Method != TenderCard || t.GatewayTxID == nil || *t.GatewayTxID == "" {
				continue
			}
			if target != nil {
				return nil, conflict("card_tender",
					"the sale was paid with several card tenders: a card return cannot pick one; refund it from the drawer")
			}
			target = t
		}
		if target == nil {
			return nil, conflict("card_tender",
				"the sale was not paid by card: refund it from the drawer or leave it on the account")
		}
		if s.gateway == nil {
			return nil, conflict("card_terminal", "this register has no card terminal gateway to refund the card")
		}
		// The claim, in one short transaction of its own (ADR 0005 section
		// 11 and 14.2 C2-5: "The card gateway is called before the
		// transaction and its result written inside it"): the sale row is
		// locked, the status and the caps are rechecked under it, and the
		// committed refund intent, unique per sale and tender while pending,
		// refuses a concurrent card return of the same tender, or a void of
		// the sale, with 409 before it reaches the terminal. No lock of the
		// money transaction is held across the network call. The claim carries
		// its own request shape, so an operator can resolve a claim that
		// outlives its act by replaying this booking.
		intent := RefundIntent{ID: uuid.New(), SaleID: *in.OriginalSaleID, TenderID: target.ID,
			GatewayTxID: *target.GatewayTxID, AmountCents: -st.total, Act: "return",
			Request: storeReturnRequest(in)}
		claim = &refundClaim{intent: intent, tenderCap: int64(target.AmountCents)}
		if err := s.inTx(ctx, func(ctx context.Context) error {
			if err := s.repo.LockSale(ctx, *in.OriginalSaleID); err != nil {
				return err
			}
			held, err := s.repo.GetSale(ctx, *in.OriginalSaleID)
			if err != nil {
				return err
			}
			// the status recheck under the lock (seventh review P1-1): a void
			// that committed while this return priced its lines refunded the
			// tender already, and the claim must refuse before the terminal
			if held.Status != StatusCompleted {
				return httpx.InvalidStateTransition(
					fmt.Sprintf("cannot return against a %s sale: a voided sale's goods came back with the void", held.Status.Status()))
			}
			if err := s.checkReturnCaps(ctx, in.OriginalSaleID, st.priced, st.byID); err != nil {
				return err
			}
			// The remainders and the totals, restated under the claim's lock
			// (seventh review P2-1): the terminal is asked for the figure the
			// lock sees, and the money transaction compares its own restated
			// total with that claim.
			if err := s.applyReturnRemainders(ctx, st); err != nil {
				return err
			}
			if err := s.resolveReturnMoney(ctx, st); err != nil {
				return err
			}
			if err := s.checkCardClaim(ctx, held.ID, target.ID, -st.total, claim.tenderCap); err != nil {
				return err
			}
			claim.intent.AmountCents = -st.total
			return s.repo.InsertRefundIntent(ctx, claim.intent)
		}); err != nil {
			return nil, err
		}
		// The call is bounded (fourteenth review P1-1): every path it can take ends inside
		// the bound, so the claim it holds never sits in flight past its own
		// settle path.
		termLeg := s.terminalRefundLeg(ctx, claim.intent.SaleID, claim.intent.ID, payment.TerminalKindRefund, claim.intent.AmountCents, "")
		callCtx, cancel := context.WithTimeout(ctx, refundCallTimeout)
		res, err := payment.RefundUnderKey(callCtx, s.gateway, *target.GatewayTxID, claim.intent.AmountCents, terminalRefundKey(termLeg))
		cancel()
		if err != nil {
			// no verdict came back (a timeout, a cancel): the terminal may
			// have moved the money, so the claim stands and the unknown
			// outcome is recorded for reconciliation (seventh review P2-2)
			s.unknownRefundOutcome(ctx, claim.intent, err)
			return nil, fmt.Errorf("card refund failed: %w", err)
		}
		switch refundVerdictOf(res) {
		case refundAccepted:
			s.terminalRefundVerdict(ctx, termLeg, true, res.TransactionID)
		case refundDeclined:
			s.terminalRefundVerdict(ctx, termLeg, false, res.TransactionID)
			// a definite refusal: the terminal's own no proves nothing moved
			s.failRefundIntent(ctx, claim.intent)
			return nil, conflict("card_refund", fmt.Sprintf("the card refund was not accepted (%s)", res.Status))
		default:
			// a status that is neither a success nor the terminal's own no
			// proves nothing about the money (tenth review P2-3): the claim
			// stands as an error leaves it, with the unknown outcome row
			s.unknownRefundOutcome(ctx, claim.intent, noVerdictOf(res))
			return nil, conflict("card_refund", fmt.Sprintf("the card refund was not accepted (%s)", res.Status))
		}
		// the gateway's own refund id, on the standing claim the moment the
		// terminal answers and in its own committed write (seventh review
		// P2-3), whatever state the claim is in (fourteenth review P1-1): a claim carrying
		// a refund id is a refund that moved, one without it is a refund that
		// may not have
		if err := s.repo.MarkRefundIntentRefunded(ctx, claim.intent.ID, res.TransactionID); err != nil {
			s.logger.Error("CRITICAL: the gateway's refund id was not written to the claim",
				"intent_id", claim.intent.ID, "sale_id", claim.intent.SaleID, "error", err)
		}
		claim.refundTxID = res.TransactionID
		// the refund row names the tender's own gateway transaction, so the
		// money is reconcilable against the charge it came from
		in.GatewayTxID = *target.GatewayTxID
	}
	return s.postReturn(ctx, st, claim)
}

// postReturn runs the return's money transaction: the sale row lock, the
// guards restated under it, the credit memo posted with its entry and its
// subledger row, the stock returns, the refund and the events, all one
// transaction. The claim, when the refund is a card's, completes with the
// posting; a refusing transaction rolls it back to PENDING and the refund
// the terminal already made is recorded in a committed row of its own.
func (s *Service) postReturn(ctx context.Context, st *returnState, claim *refundClaim) (*Return, error) {
	in := st.in
	customerID := st.customerID
	branchID := st.branchID
	priced := st.priced
	saleLines := st.saleLines
	refs := st.refs
	var tillSessionID *uuid.UUID
	var out *Return
	var invoiceLines []InvoiceLineCost
	var invoiceLineIDs map[uuid.UUID]uuid.UUID
	err := s.inTx(ctx, func(ctx context.Context) error {
		// The sale row first (section 11, step 1): a void racing this return
		// serializes here, and the loser sees the sale it priced change under
		// it. Only a completed sale's goods come back; a voided sale's came
		// back with the void.
		if in.OriginalSaleID != nil {
			if err := s.repo.LockSale(ctx, *in.OriginalSaleID); err != nil {
				return err
			}
			sale, err := s.repo.GetSale(ctx, *in.OriginalSaleID)
			if err != nil {
				return err
			}
			if sale.Status != StatusCompleted {
				return httpx.InvalidStateTransition(
					fmt.Sprintf("cannot return against a %s sale: a voided sale's goods came back with the void", sale.Status.Status()))
			}
			// A return refunded by any method but the card's own, of a sale
			// whose card refund claim still stands, is refused (eighth review
			// P2-1, ninth review P1-2): the claim may already have taken the
			// terminal's money, and neither the drawer nor the account may
			// pay the same goods a second time. Under the sale row lock,
			// before any money of this return moves.
			if in.RefundMethod != RefundCard {
				if pending, err := s.repo.SaleHasPendingRefundIntent(ctx, sale.ID); err != nil {
					return err
				} else if pending {
					return conflict("refund_in_flight",
						"a card refund of this sale is still in flight: settle it before refunding by any other method")
				}
				if doneVoid, err := s.repo.SaleHasVoidIntentDone(ctx, sale.ID); err != nil {
					return err
				} else if doneVoid {
					return conflict("void_refund_recorded",
						"this sale's void already refunded a card tender at the terminal: reconcile the void before returning goods against this sale")
				}
			}
			// The claim's own state, under the sale row lock and before the
			// caps: a replay whose claim was settled by a concurrent
			// resolution answers the race (tenth review P3-3), never the
			// caps the winner's own booking trips.
			if claim != nil {
				cf, err := s.repo.RefundIntentFacts(ctx, claim.intent.SaleID, claim.intent.TenderID)
				if err != nil {
					return err
				}
				if cf.DoneVoid {
					return conflict("tender_refunded",
						"the card tender was already refunded whole by this sale's void: no second refund of it can be made")
				}
				if cf.PendingCents == 0 {
					return conflict("intent_not_pending",
						"the refund claim was settled by another resolution first")
				}
			}
			// The cap, rechecked under the lock: a return that raced this one
			// for the same line is counted before this one is allowed.
			if err := s.checkReturnCaps(ctx, in.OriginalSaleID, st.priced, st.byID); err != nil {
				return err
			}
			// The remainder rule and the totals, restated under the lock: the
			// money is written from these answers, never from the pre
			// transaction read (a card refund already taken keeps the amount
			// it took; the race window is the cents a concurrent return's
			// rounding moves).
			if err := s.applyReturnRemainders(ctx, st); err != nil {
				return err
			}
			if err := s.resolveReturnMoney(ctx, st); err != nil {
				return err
			}
			// The drawer pays a named sale only what the sale itself kept in
			// cash or check, less every cash refund already made against it
			// (a card, store credit or account sale's goods go back to the
			// card, the memo or the account): the bound holds unless a
			// refund_method manager override is consumed here, once.
			if !in.Exchange && in.RefundMethod == RefundCash {
				refundableCash, bound, err := s.repo.CashRefundableCents(ctx, sale.ID)
				if err != nil {
					return err
				}
				if bound && -st.total > refundableCash {
					spent, err := s.consumeOverride(ctx, sale.ID, OverrideRefundMethod, -st.total, st.actor)
					if err != nil {
						return err
					}
					if !spent {
						return conflict("exceeds_cash_tender", fmt.Sprintf(
							"the sale kept %d cents in cash or check less earlier cash refunds: refund to the account or card, or approve a refund_method override",
							refundableCash))
					}
				}
			}
			// The return touches the sale: its revision moves, in process, so
			// a void built on the earlier revision is refused stale.
			if err := s.repo.BumpSaleRevision(ctx, sale.ID); err != nil {
				return err
			}
			// The sale's own invoice lines, by the stored link on each sale
			// line: a linked return's restocking line takes its source invoice
			// line's unit_cost, the cost the sale relieved (8.4), never today's
			// average and never a position match (a removed cart line leaves
			// the sale's positions gapped).
			if sale.InvoiceID != nil {
				if invoiceLines, err = s.repo.InvoiceLineCosts(ctx, *sale.InvoiceID); err != nil {
					return err
				}
			}
			if invoiceLineIDs, err = s.repo.InvoiceLineIDsBySaleLine(ctx, sale.ID); err != nil {
				return err
			}
		}
		// The drawer the refund pays out of (fourth review P2-2), whatever
		// the return is linked to: a cash return locks the register's open
		// session FOR SHARE (against the close's FOR UPDATE) inside the
		// transaction and is refused when none is open, so it never lands in
		// a drawer already counted (the close waits on the same lock and
		// aggregates the payout) or in no drawer at all. A card or account
		// return may live sessionless.
		session, err := s.repo.GetOpenTillSession(ctx, in.RegisterID)
		if err != nil {
			return err
		}
		if session != nil {
			if err := s.repo.LockTillSession(ctx, session.ID, false); err != nil {
				return err
			}
			held, err := s.repo.GetTillSession(ctx, session.ID)
			if err != nil {
				return err
			}
			if held.Status == TillOpen {
				tillSessionID = &session.ID
			} else if in.RefundMethod == RefundCash {
				return conflict("session_closed",
					"the till session closed while the return was made: open the drawer and take it again")
			}
		} else if in.RefundMethod == RefundCash {
			return conflict("no_open_session",
				"a cash return pays out of the drawer: open the till session before refunding cash")
		}
		// The claim's rules, rechecked under the sale row lock as the refund
		// posts (seventh review P2-1): the intent table is the authority. The
		// books take exactly what the terminal took: a restated total that
		// differs from the claimed amount refuses, the intent stays PENDING
		// beside the orphan row that records the gateway's figure, and the
		// books never record a cent less than the terminal moved.
		if claim != nil {
			// The claim row locked and read whole under the sale row lock
			// (fourteenth review P1-1): a replay may only settle a claim whose own terminal
			// call has answered (or whose in flight lease expired with no
			// answer at all, marked UNKNOWN then), never a live one, and the
			// id it books must be the terminal's own.
			held, err := s.repo.LockRefundIntent(ctx, claim.intent.ID)
			if err != nil {
				return err
			}
			if held.State != "PENDING" {
				return conflict("intent_not_pending",
					fmt.Sprintf("the refund claim is %s: only a PENDING claim is resolved", held.State))
			}
			if err := s.settleUnansweredClaim(ctx, held); err != nil {
				return err
			}
			if held.RefundTxID != "" && held.RefundTxID != claim.refundTxID {
				return conflict("refund_tx_id_conflict",
					fmt.Sprintf("the claim carries the terminal's refund id %s, not %s: record the terminal's own id",
						held.RefundTxID, claim.refundTxID))
			}
			cf, err := s.repo.RefundIntentFacts(ctx, claim.intent.SaleID, claim.intent.TenderID)
			if err != nil {
				return err
			}
			if cf.DoneVoid {
				return conflict("tender_refunded",
					"the card tender was already refunded whole by this sale's void: no second refund of it can be made")
			}
			if cf.UnbookedRefundedCents > 0 {
				return conflict("tender_refunded",
					"the terminal already refunded this tender under a recorded refund id that never booked: reconcile that claim before refunding the tender again")
			}
			if cf.PendingCents == 0 {
				// this return's own claim left PENDING underneath it (a
				// concurrent resolution won the claim): the race is lost, not
				// in flight (ninth review P2-2, tenth review P3-3)
				return conflict("intent_not_pending",
					"the refund claim was settled by another resolution first")
			}
			if heldOthers := cf.PendingCents - claim.intent.AmountCents; heldOthers != 0 {
				return conflict("refund_in_flight",
					"a card refund of this sale's tender is already in flight: only one refund of a tender runs at a time")
			}
			if -st.total != claim.intent.AmountCents {
				return conflict("refund_amount_changed",
					fmt.Sprintf("the terminal refunded %d cents but the return restates to %d under the lock: the intent stands beside the orphan row until it is reconciled",
						claim.intent.AmountCents, -st.total))
			}
			already, err := s.repo.CardRefundedCents(ctx, claim.intent.SaleID)
			if err != nil {
				return err
			}
			if -st.total > claim.tenderCap-already {
				return conflict("exceeds_card_tender",
					fmt.Sprintf("the card tender kept %d cents less the %d already refunded to the card: refund the rest from the drawer",
						claim.tenderCap, already))
			}
			if err := s.repo.CompleteRefundIntent(ctx, claim.intent.SaleID, claim.intent.TenderID, claim.refundTxID); err != nil {
				return err
			}
			if claim.resolveAudit != nil && s.auditLog != nil {
				if err := s.auditLog.Log(ctx, *claim.resolveAudit); err != nil {
					return fmt.Errorf("failed to write the resolution audit row: %w", err)
				}
			}
		}
		date, err := s.repo.BranchLocalDate(ctx, *branchID, s.now())
		if err != nil {
			return err
		}
		invoiceLineAt := func(saleLineID uuid.UUID) *InvoiceLineCost {
			id, ok := invoiceLineIDs[saleLineID]
			if !ok {
				return nil
			}
			for i := range invoiceLines {
				if invoiceLines[i].ID == id {
					return &invoiceLines[i]
				}
			}
			return nil
		}
		// The credit memo: a DRAFT row with its negative lines, then the AR
		// core's PostCreditMemo mints the gapless number, posts the entry
		// (DR each revenue account its group's line totals, DR the tax, DR
		// 1030 / CR 5010 the restocked cost; CR 1020 the core adds) and the
		// subledger row, all in this transaction.
		memoID := uuid.New()
		var lines []ReturnLine
		var restockCost int64
		// restockAct is one physical restock the return makes: a product
		// line's own units, or a returned kit's components.
		type restockAct struct {
			productID uuid.UUID
			qty       httpx.Quantity
		}
		var restocks []restockAct
		for i := range priced {
			p := &priced[i]
			qty := p.in.Quantity
			uom, priceUOM := "EA", "EA"
			if p.uom != "" {
				uom, priceUOM = p.uom, p.uom
			}
			var uomQty, priceUOMQty httpx.Quantity = salesdoc.One, salesdoc.One
			stockUOM := p.stockUOM
			stockQty := p.stockQty
			var invoiceLineID *uuid.UUID
			restockUnitCost := httpx.Price(0)
			if p.saleLine != nil {
				// the memo line mirrors the sale line's own unit and pair
				if p.saleLine.UOM != nil {
					uom = *p.saleLine.UOM
				}
				if p.saleLine.PriceUOM != nil {
					priceUOM = *p.saleLine.PriceUOM
				}
				if p.saleLine.UOMQty != nil {
					uomQty = *p.saleLine.UOMQty
				}
				if p.saleLine.PriceUOMQty != nil {
					priceUOMQty = *p.saleLine.PriceUOMQty
				}
				if src := invoiceLineAt(p.saleLine.ID); src != nil {
					id := src.ID
					invoiceLineID = &id
					restockUnitCost = httpx.Price(src.UnitCost)
				}
				if p.in.ProductID != nil && p.saleLine.LineType != salesdoc.LineKit && p.saleLine.Quantity != nil && *p.saleLine.Quantity > 0 {
					back := st.backByLine[p.saleLine.ID]
					stockSold := int64(salesdoc.StockQty(p.saleLine))
					sold := int64(*p.saleLine.Quantity)
					before := httpx.Quantity(divRound(stockSold*back, sold))
					after := httpx.Quantity(divRound(stockSold*(back+int64(p.in.Quantity)), sold))
					sQty := after - before
					stockQty = &sQty
					if p.saleLine.StockUOM != nil {
						stockUOM = p.saleLine.StockUOM
					} else if ref, ok := refs[p.in.ProductID.String()]; ok && ref.UOMPrimary != "" {
						u := ref.UOMPrimary
						stockUOM = &u
					}
				}
			}
			unitPrice := p.in.UnitPrice
			if unitPrice == nil {
				unitPrice = ptrPrice(0)
			}
			lineType := p.lineType
			if lineType == "" {
				lineType = "PRODUCT"
			}
			lines = append(lines, ReturnLine{
				ID: uuid.New(), Position: i, ProductID: p.in.ProductID, Description: p.in.Description,
				Quantity: &qty, UOM: &uom, PriceUOM: &priceUOM, UOMQty: &uomQty, PriceUOMQty: &priceUOMQty,
				StockUOM: stockUOM, StockQuantity: stockQty,
				UnitPrice: unitPrice, LineTotal: ptrC(-p.lineTotal), Restock: p.in.Restock,
				ReasonCode: reasonOf(p.in.ReasonCode), RestockingFeeCents: httpx.Cents(p.feeCents),
				SaleLineID: p.in.SaleLineID, InvoiceLineID: invoiceLineID,
				LineType: lineType, ChargeCodeID: p.chargeCodeID,
			})
			if !p.in.Restock {
				continue
			}
			// A returned kit line restocks its components and never itself:
			// the kit product has no inventory row, and the goods that left
			// were the components. Each component comes back at the returned
			// fraction of its quantity, at its own invoice line's cost (the
			// cost the sale relieved, 8.4).
			if p.saleLine != nil && p.saleLine.LineType == salesdoc.LineKit && p.saleLine.Quantity != nil {
				// the component quantity and cost telescope on the kit
				// line's cumulative returned quantity (sixth review P3-1):
				// each return restocks the difference between the
				// cumulative position after it and before it, so repeated
				// partial returns put exactly the cost the sale relieved
				// back, never the rounded shares' drift
				back := st.backByLine[p.saleLine.ID]
				for j := range saleLines {
					comp := &saleLines[j]
					if comp.ParentLineID == nil || *comp.ParentLineID != p.saleLine.ID ||
						comp.Quantity == nil || comp.ProductID == nil {
						continue
					}
					before := divRound(int64(*comp.Quantity)*back, int64(*p.saleLine.Quantity))
					after := divRound(int64(*comp.Quantity)*(back+int64(p.in.Quantity)), int64(*p.saleLine.Quantity))
					qtyBack := httpx.Quantity(after - before)
					if qtyBack <= 0 {
						continue
					}
					cost := httpx.Price(0)
					if src := invoiceLineAt(comp.ID); src != nil {
						cost = httpx.Price(src.UnitCost)
					}
					if cost <= 0 {
						if ref, ok := refs[comp.ProductID.String()]; ok {
							cost = ref.AverageCost
						}
					}
					if cost > 0 {
						restockCost += int64(salesdoc.CostOf(httpx.Quantity(after), cost)) -
							int64(salesdoc.CostOf(httpx.Quantity(before), cost))
					}
					restocks = append(restocks, restockAct{productID: *comp.ProductID, qty: qtyBack})
				}
				continue
			}
			if p.in.ProductID != nil {
				// a free line (no sale) restocks at today's average: it has
				// no source invoice line to read, and no earlier return to
				// telescope on; a linked line's cost and stock quantity
				// telescope on its cumulative returned quantity
				cost := restockUnitCost
				if cost <= 0 {
					if ref, ok := refs[p.in.ProductID.String()]; ok {
						cost = ref.AverageCost
					}
				}
				if p.saleLine == nil || p.saleLine.Quantity == nil || *p.saleLine.Quantity <= 0 {
					sQty := p.in.Quantity
					if stockQty != nil {
						sQty = *stockQty
					}
					if cost > 0 {
						restockCost += int64(salesdoc.CostOf(sQty, cost))
					}
					restocks = append(restocks, restockAct{productID: *p.in.ProductID, qty: sQty})
				} else {
					back := st.backByLine[p.saleLine.ID]
					stockSold := int64(salesdoc.StockQty(p.saleLine))
					sold := int64(*p.saleLine.Quantity)
					before := httpx.Quantity(divRound(stockSold*back, sold))
					after := httpx.Quantity(divRound(stockSold*(back+int64(p.in.Quantity)), sold))
					if cost > 0 {
						restockCost += int64(salesdoc.CostOf(after, cost)) -
							int64(salesdoc.CostOf(before, cost))
					}
					if after > before {
						restocks = append(restocks, restockAct{productID: *p.in.ProductID, qty: after - before})
					}
				}
			}
		}
		// The stock returns (section 11, step 6, before the customer row of
		// step 7 and the memo's number of step 8): each restock act back on
		// hand, a product line's own units or a kit's components (the kit
		// product itself never stocks), in product order so two acts that
		// move the same products agree on the order.
		sort.SliceStable(restocks, func(a, b int) bool { return restocks[a].productID.String() < restocks[b].productID.String() })
		for i := range restocks {
			if err := s.inventory.RestockQty(ctx, restocks[i].productID, *branchID, restocks[i].qty); err != nil {
				return err
			}
		}
		// The restocking fee, as its own line of the memo (a charge that lowers
		// the credit) booked to the RESTOCK charge code's income account.
		var feeAccount string
		var feeCodeID uuid.UUID
		if st.feeCents > 0 {
			codes, err := s.repo.LookupChargeCodes(ctx, []string{"RESTOCK"})
			if err != nil {
				return err
			}
			code, ok := codes["RESTOCK"]
			if !ok {
				return conflict("restock_charge_missing",
					"a restocking fee is configured but the RESTOCK charge code is missing: add it before returning goods that carry a fee")
			}
			feeAccount = code.RevenueAccountCode
			feeCodeID = code.ID
		}
		// Section 11 steps 7 and 8: lock the customer row FOR UPDATE before the
		// gapless CM counter row and before the credit_memos foreign-key insert
		// ("No code takes the customer row after a gapless number.").
		if err := s.repo.LockCustomer(ctx, *customerID); err != nil {
			return err
		}
		memoNumber, err := httpx.NextGaplessNumber(ctx, s.db.GetExecutor(ctx), "credit_memo", "CM", httpx.DefaultDocNumberWidth)
		if err != nil {
			return err
		}
		if _, err := s.db.GetExecutor(ctx).Exec(ctx, `
			INSERT INTO credit_memos (id, customer_id, branch_id, currency, reason_code, reason, status, amount,
				subtotal, tax_amount, total_amount, amount_open, memo_date, created_at, updated_at, revision)
			VALUES ($1, $2, $3, $4, 'RETURN', $5, 'DRAFT', 0, 0, 0, 0, 0, $6::date, NOW(), NOW(), 1)`,
			memoID, customerID, branchID, st.currency, in.Reason, date.Format("2006-01-02")); err != nil {
			return fmt.Errorf("failed to create the credit memo: %w", mapWriteError(err))
		}
		for i := range lines {
			l := &lines[i]
			lineType := l.LineType
			if lineType == "" || lineType == "KIT" || lineType == "COMPONENT" {
				// the memo keeps the document's own product shape; a kit
				// returns as its whole line (its components never priced)
				lineType = "PRODUCT"
			}
			if _, err := s.db.GetExecutor(ctx).Exec(ctx, `
				INSERT INTO credit_memo_lines (credit_memo_id, position, line_type, product_id, charge_code_id, description, quantity,
					uom, price_uom, uom_qty, price_uom_qty, stock_uom, stock_quantity, unit_price, price_source, line_total, taxable, restock,
					invoice_line_id, created_at)
				VALUES ($1, $2, $15, $3, $16, $4, -($5::numeric / 10000), $6, $7, $8::numeric / 10000, $9::numeric / 10000,
					$17, -($18::numeric / 10000), $10::numeric / 10000, 'MANUAL', $11::numeric / 100, $12, $13, $14, NOW())`,
				memoID, l.Position, l.ProductID, l.Description, qtyArg(l.Quantity), l.UOM, l.PriceUOM,
				qtyArg(l.UOMQty), qtyArg(l.PriceUOMQty), priceArg(l.UnitPrice),
				centsArg(l.LineTotal), priced[i].taxable, l.Restock, l.InvoiceLineID, lineType, l.ChargeCodeID,
				l.StockUOM, qtyArg(l.StockQuantity)); err != nil {
				return fmt.Errorf("failed to create the credit memo line: %w", mapWriteError(err))
			}
		}
		if st.feeCents > 0 {
			if _, err := s.db.GetExecutor(ctx).Exec(ctx, `
				INSERT INTO credit_memo_lines (credit_memo_id, position, line_type, charge_code_id, description, quantity,
					uom, price_uom, uom_qty, price_uom_qty, unit_price, price_source, line_total, taxable, revenue_account_code,
					restock, created_at)
				VALUES ($1, $2, 'CHARGE', $3, 'Restocking fee', 1, 'EA', 'EA', 1, 1, $4::numeric / 100, 'MANUAL',
					$4::numeric / 100, FALSE, $5, FALSE, NOW())`,
				memoID, len(lines), feeCodeID, st.feeCents, feeAccount); err != nil {
				return fmt.Errorf("failed to create the restocking fee line: %w", mapWriteError(err))
			}
		}
		// Revenue, per the line's own account (8.3): product lines to 4010, a
		// returned charge to its code's account.
		var revenueLines []salesdoc.Line
		for i := range priced {
			revType := salesdoc.LineProduct
			if priced[i].lineType == string(salesdoc.LineCharge) {
				revType = salesdoc.LineCharge
			}
			lt := httpx.Cents(priced[i].lineTotal)
			revenueLines = append(revenueLines, salesdoc.Line{LineType: revType, LineTotal: &lt,
				RevenueAccountCode: revenueAccountOf(priced[i])})
		}
		var legs []gl.Leg
		for _, g := range salesdoc.RevenueGroups(revenueLines) {
			legs = append(legs, gl.Leg{AccountCode: g.AccountCode, Description: "Sales Revenue", Debit: int64(g.Cents)})
		}
		if st.taxCents > 0 {
			legs = append(legs, gl.Leg{AccountCode: gl.AccountCodeSalesTax, Description: "Sales Tax Payable", Debit: st.taxCents})
		}
		if st.feeCents > 0 {
			legs = append(legs, gl.Leg{AccountCode: feeAccount, Description: "Restocking fee", Credit: st.feeCents})
		}
		if restockCost > 0 {
			legs = append(legs,
				gl.Leg{AccountCode: gl.AccountCodeInventory, Description: "Inventory", Debit: restockCost},
				gl.Leg{AccountCode: gl.AccountCodeCOGS, Description: "Cost of Goods Sold", Credit: restockCost})
		}
		fxPost, err := s.ar.PostCreditMemo(ctx, account.PostCreditMemoIn{
			MemoID: memoID, CustomerID: *customerID, Number: memoNumber, Currency: st.currency, MemoDate: date,
			SubtotalCents: -(st.subtotal - st.feeCents), TaxCents: -st.taxCents, TotalCents: st.total, TaxRate: st.taxRate, Actor: st.actor, Legs: legs})
		if err != nil {
			return err
		}
		var fxAll *account.Effects
		mergeEffects(&fxAll, fxPost)
		// The refund: cash or card pays the credit out (DR 1020 / CR 1010),
		// account leaves the credit open.
		if in.RefundMethod == RefundCash || in.RefundMethod == RefundCard {
			_, rfx, err := s.ar.RefundCreditMemo(ctx, account.RefundCreditMemoIn{
				MemoID: memoID, AmountCents: -st.total, Reason: in.Reason, Method: string(in.RefundMethod),
				GatewayRefundID: in.GatewayTxID, Actor: st.actor, On: date})
			if err != nil {
				return err
			}
			mergeEffects(&fxAll, rfx)
		}
		// The return row, linked to its memo.
		ret := &Return{
			RegisterID: in.RegisterID, TillSessionID: tillSessionID, OriginalSaleID: in.OriginalSaleID,
			CustomerID: customerID, BranchID: branchID, CashierID: st.cashierID, Currency: st.currency,
			SubtotalCents: httpx.Cents(-(st.subtotal - st.feeCents)), TaxCents: httpx.Cents(-st.taxCents), TotalCents: httpx.Cents(st.total),
			RestockingFeeCents: httpx.Cents(st.feeCents),
			RefundMethod:       in.RefundMethod, Reason: in.Reason, CreditMemoID: &memoID, Lines: lines,
		}
		if err := s.repo.CreateReturn(ctx, ret, lines); err != nil {
			return err
		}
		// The audit row, inside the transaction (R1-14): a rolled back return
		// leaves no row.
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.return.completed", EntityType: "pos_return", EntityID: ret.ID, UserID: st.actor,
				Changes: map[string]any{
					"number": ret.Number, "total_cents": st.total, "refund_method": in.RefundMethod.Status(),
					"register_id": in.RegisterID, "customer_id": customerID, "credit_memo_id": memoID,
					"original_sale_id": in.OriginalSaleID,
				}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		// The events, last: the memo's lifecycle, the AR core's, the
		// return's own.
		branch := *branchID
		number := memoNumber
		if err := s.recordEvent(ctx, outbox.Event{Type: "credit_memo.created", EntityType: "credit_memo", EntityID: memoID,
			BranchID: &branch, Data: eventJSON(map[string]any{
				"number": nil, "customer_id": customerID, "status": "draft", "currency": st.currency,
				"pos_return_id": ret.ID,
			})}); err != nil {
			return err
		}
		if err := s.recordEvent(ctx, outbox.Event{Type: "credit_memo.posted", EntityType: "credit_memo", EntityID: memoID,
			BranchID: &branch, Data: eventJSON(map[string]any{
				"number": number, "customer_id": customerID, "status": "open", "from_status": "draft",
				"currency": st.currency, "total_cents": st.total,
			})}); err != nil {
			return err
		}
		if in.RefundMethod == RefundCash || in.RefundMethod == RefundCard {
			if err := s.recordEvent(ctx, outbox.Event{Type: "credit_memo.refunded", EntityType: "credit_memo", EntityID: memoID,
				BranchID: &branch, Data: eventJSON(map[string]any{
					"customer_id": customerID, "status": "applied", "currency": st.currency,
					"amount_cents": -st.total, "method": in.RefundMethod.Status(),
				})}); err != nil {
				return err
			}
		}
		if fxAll != nil {
			for _, ev := range fxAll.Events() {
				if err := s.recordEvent(ctx, ev); err != nil {
					return err
				}
			}
		}
		if err := s.recordEvent(ctx, outbox.Event{Type: EventReturnCompleted, EntityType: "pos_return", EntityID: ret.ID,
			BranchID: &branch, Data: eventJSON(map[string]any{
				"number": ret.Number, "customer_id": customerID, "status": "completed", "currency": st.currency,
				"total_cents": st.total, "refund_method": in.RefundMethod.Status(), "credit_memo_id": memoID,
			})}); err != nil {
			return err
		}
		out = ret
		return nil
	})
	if err != nil {
		// The gateway already refunded and the transaction refused: the
		// money is accounted for in a committed row of its own carrying the
		// gateway ids.
		if claim != nil && !claim.replay && claim.refundTxID != "" {
			s.recordOrphanRefund(ctx, gatewayRefund{gatewayTxID: claim.intent.GatewayTxID, refundTxID: claim.refundTxID,
				amountCents: claim.intent.AmountCents, tenderID: claim.intent.TenderID, act: claim.intent.Act,
				entity: claim.intent.SaleID, actor: st.actor}, err)
		}
		return nil, err
	}
	return out, nil
}

// storedReturnLine is one line of a return claim's stored request shape.
type storedReturnLine struct {
	SaleLineID  *uuid.UUID `json:"sale_line_id,omitempty"`
	ProductID   *uuid.UUID `json:"product_id,omitempty"`
	Description string     `json:"description"`
	Quantity    int64      `json:"quantity"`
	UOM         string     `json:"uom,omitempty"`
	Restock     bool       `json:"restock"`
	ReasonCode  string     `json:"reason_code,omitempty"`
}

// storedReturnRequest is a return claim's own request, carried on the intent
// so the operator resolution can replay the money transaction the lost act
// never ran. Quantities ride at their wire scale of four.
type storedReturnRequest struct {
	RegisterID     string             `json:"register_id"`
	CustomerID     *uuid.UUID         `json:"customer_id,omitempty"`
	OriginalSaleID *uuid.UUID         `json:"original_sale_id,omitempty"`
	Reason         string             `json:"reason"`
	Lines          []storedReturnLine `json:"lines"`
}

func storeReturnRequest(in *ReturnIn) []byte {
	out := storedReturnRequest{RegisterID: in.RegisterID, CustomerID: in.CustomerID,
		OriginalSaleID: in.OriginalSaleID, Reason: in.Reason}
	for i := range in.Lines {
		l := in.Lines[i]
		out.Lines = append(out.Lines, storedReturnLine{SaleLineID: l.SaleLineID, ProductID: l.ProductID,
			Description: l.Description, Quantity: int64(l.Quantity), UOM: l.UOM, Restock: l.Restock, ReasonCode: l.ReasonCode})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return raw
}

// toReturnIn rebuilds the return request the claim stored. The refund method
// is card by construction: a claim exists only for a card refund.
func (r *storedReturnRequest) toReturnIn() *ReturnIn {
	in := &ReturnIn{RegisterID: r.RegisterID, CustomerID: r.CustomerID, OriginalSaleID: r.OriginalSaleID,
		RefundMethod: RefundCard, Reason: r.Reason}
	for i := range r.Lines {
		l := r.Lines[i]
		in.Lines = append(in.Lines, ReturnLineIn{ProductID: l.ProductID, SaleLineID: l.SaleLineID,
			Description: l.Description, Quantity: httpx.Quantity(l.Quantity), UOM: l.UOM, Restock: l.Restock, ReasonCode: l.ReasonCode})
	}
	return in
}

func ptrC(v int64) *httpx.Cents {
	c := httpx.Cents(v)
	return &c
}

// divRound divides two integers rounding half away from zero, the money
// convention of the extensions (a positive denominator only).
func divRound(num, den int64) int64 {
	if den <= 0 {
		return 0
	}
	if num >= 0 {
		return (num + den/2) / den
	}
	return -((-num + den/2) / den)
}

// checkReturnCaps refuses a return whose lines, taken with every earlier
// return of the same sale lines, bring back more than the lines sold: the
// sale's own goods bound its refunds (blocker exceeds_sold).
func (s *Service) checkReturnCaps(ctx context.Context, saleID *uuid.UUID, priced []returnLinePriced, byID map[uuid.UUID]salesdoc.Line) error {
	if saleID == nil {
		return nil
	}
	requested := map[uuid.UUID]int64{}
	for i := range priced {
		if priced[i].in.SaleLineID != nil {
			requested[*priced[i].in.SaleLineID] += int64(priced[i].in.Quantity)
		}
	}
	if len(requested) == 0 {
		return nil
	}
	returned, err := s.repo.ReturnedQtyByLine(ctx, *saleID)
	if err != nil {
		return err
	}
	legacy, err := s.repo.LegacyReturnedQtyByProduct(ctx, *saleID)
	if err != nil {
		return err
	}
	// The bearer of each product's legacy returns: its first line by
	// position among the sale's lines (the same rule the caller priced by),
	// so a sale of several lines of one product loses headroom on one line
	// only, and always the same one.
	ordered := make([]salesdoc.Line, 0, len(byID))
	for _, l := range byID {
		ordered = append(ordered, l)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Position != ordered[j].Position {
			return ordered[i].Position < ordered[j].Position
		}
		return ordered[i].ID.String() < ordered[j].ID.String()
	})
	bearer := map[uuid.UUID]uuid.UUID{}
	for _, l := range ordered {
		if l.ProductID != nil {
			if _, ok := bearer[*l.ProductID]; !ok {
				bearer[*l.ProductID] = l.ID
			}
		}
	}
	for lineID, want := range requested {
		src, ok := byID[lineID]
		if !ok || src.Quantity == nil {
			continue
		}
		already := int64(returned[lineID])
		if src.ProductID != nil && bearer[*src.ProductID] == lineID {
			already += int64(legacy[*src.ProductID])
		}
		if want+already > int64(*src.Quantity) {
			return conflict("exceeds_sold",
				fmt.Sprintf("the line sold %s and %s is already returned: %s more is refused",
					(*src.Quantity).DecimalString(), httpx.Quantity(already).DecimalString(), httpx.Quantity(want).DecimalString()))
		}
	}
	return nil
}

// fxMemoNumber reads the memo's minted number for its event.
func fxMemoNumber(ctx context.Context, s *Service, memoID uuid.UUID) string {
	var number string
	if err := s.db.GetExecutor(ctx).QueryRow(ctx, `SELECT number FROM credit_memos WHERE id = $1`, memoID).Scan(&number); err != nil {
		return ""
	}
	return number
}

// GetReturn reads a return with its lines.
func (s *Service) GetReturn(ctx context.Context, id uuid.UUID) (*Return, error) {
	return s.repo.GetReturn(ctx, id)
}

// ListReturns lists returns for a register on a date.
func (s *Service) ListReturns(ctx context.Context, f ReturnFilter) ([]Return, error) {
	return s.repo.ListReturns(ctx, f, 200)
}

func ptrPrice(v int64) *httpx.Price {
	p := httpx.Price(v)
	return &p
}

// revenueAccountOf reads the revenue account a returned line credits back:
// the sale line's own snapshot when linked, nothing for a free line (the
// group defaults it to product revenue).
func revenueAccountOf(p returnLinePriced) *string {
	if p.saleLine != nil {
		return p.saleLine.RevenueAccountCode
	}
	return nil
}

// reasonOf is a line's reason code, other when none was named.
func reasonOf(code string) string {
	if code == "" {
		return ReasonOther
	}
	return code
}
