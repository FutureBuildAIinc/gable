// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
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
}

// ReturnLineIn is one parsed returned line.
type ReturnLineIn struct {
	ProductID   *uuid.UUID
	SaleLineID  *uuid.UUID
	Description string
	Quantity    httpx.Quantity
	UnitPrice   *httpx.Price
	Restock     bool
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
}

// ReturnSale records a counter return (ADR 0005 section 14.2 C2-5): a
// credit memo created and posted in ONE act with its restock lines,
// refunded in cash or card or left as account credit. The card refund goes
// through the gateway before the transaction; the memo's entry, subledger
// row, the stock return and the refund commit together.
func (s *Service) ReturnSale(ctx context.Context, cashierID uuid.UUID, in *ReturnIn, actor string) (*Return, error) {
	if s.ar == nil || s.inventory == nil {
		return nil, errNotConfigured
	}
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
		if customerID == nil {
			customerID = original.CustomerID
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
	}
	// The walk-in customer cannot hold account credit: as the sale side
	// refuses an ACCOUNT tender for it, a return left on its account is
	// refused the same way.
	if isWalkIn && in.RefundMethod == RefundAccount {
		return nil, conflict("walk_in_account",
			"account credit needs a named customer: the walk-in customer is refunded from the drawer or to the card")
	}
	facts, err := s.repo.CustomerFacts(ctx, *customerID)
	if err != nil {
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
	// First pass: fill each line from the sale line it names, so the product
	// lookup covers the resolved products too.
	resolved := make([]ReturnLineIn, len(in.Lines))
	for i := range in.Lines {
		rl := in.Lines[i]
		if rl.SaleLineID != nil {
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
	productIDs := make([]uuid.UUID, 0, len(resolved))
	for i := range resolved {
		if resolved[i].ProductID != nil {
			productIDs = append(productIDs, *resolved[i].ProductID)
		}
	}
	refs, err := s.repo.LookupProducts(ctx, productIDs)
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
		if rl.ProductID != nil {
			if ref, ok := refs[rl.ProductID.String()]; ok {
				taxable = ref.Taxable
			}
		}
		priced = append(priced, returnLinePriced{in: rl, lineTotal: int64(salesdoc.CostOf(rl.Quantity, *rl.UnitPrice)), taxable: taxable})
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
	// applyRemainders restates each linked line's refund against what earlier
	// returns already took (fourth review P2-1): the return that completes a
	// line takes the line's whole total less every cent already refunded, so
	// the rounded per unit shares of repeated partial returns never sum past
	// what the sale was paid; the tax is capped the same way. Read before the
	// transaction (the guards) and again under the sale row lock (the
	// authority the memo is written from).
	var taxAlreadyBack int64
	applyRemainders := func(ctx context.Context) error {
		if in.OriginalSaleID == nil {
			return nil
		}
		returned, err := s.repo.ReturnedQtyByLine(ctx, *in.OriginalSaleID)
		if err != nil {
			return err
		}
		refunded, err := s.repo.RefundedCentsByLine(ctx, *in.OriginalSaleID)
		if err != nil {
			return err
		}
		if taxAlreadyBack, err = s.repo.RefundedTaxCents(ctx, *in.OriginalSaleID); err != nil {
			return err
		}
		for i := range priced {
			p := &priced[i]
			if p.saleLine == nil || p.saleLine.Quantity == nil || p.saleLine.LineTotal == nil {
				continue
			}
			sold, back := int64(*p.saleLine.Quantity), int64(returned[p.saleLine.ID])
			remaining := int64(*p.saleLine.LineTotal) - refunded[p.saleLine.ID]
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
	if err := applyRemainders(ctx); err != nil {
		return nil, err
	}
	// resolveMoney restates the memo's totals from the lines' money; the
	// transaction runs it again under the sale row lock after the caps
	// recheck, and the memo, the legs, the refund and the return row are all
	// written from its latest answer.
	var subtotal, taxCents, total int64
	var taxRate *string
	resolveMoney := func(ctx context.Context) error {
		var linkedTaxable int64
		for i := range priced {
			if priced[i].saleLine != nil && priced[i].taxable {
				linkedTaxable += priced[i].lineTotal
			}
		}
		subtotal = linkedTaxable
		var freeTaxable int64
		var freeNontaxable int64
		for i := range priced {
			if priced[i].saleLine == nil {
				if priced[i].taxable {
					freeTaxable += priced[i].lineTotal
				} else {
					freeNontaxable += priced[i].lineTotal
				}
			} else if !priced[i].taxable {
				subtotal += priced[i].lineTotal
			}
		}
		subtotal += freeTaxable + freeNontaxable
		taxCents = 0
		taxRate = nil
		if linkedTaxable > 0 && saleTaxable > 0 && saleTaxCents > 0 {
			taxCents += divRound(linkedTaxable*saleTaxCents, saleTaxable)
			// never more tax back than the sale collected less what earlier
			// returns took (the same remainder rule)
			if remaining := saleTaxCents - taxAlreadyBack; taxCents > remaining {
				taxCents = remaining
			}
		}
		if freeTaxable > 0 {
			exempt, err := s.repo.CustomerExempt(ctx, *customerID)
			if err != nil {
				return err
			}
			if exempt {
				zero := "0"
				taxRate = &zero
			} else {
				rate, ok, err := s.repo.BranchTaxRate(ctx, branchID)
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
				taxCents += int64(salesdoc.TaxAt(httpx.Cents(freeTaxable), scaled))
				taxRate = &rate
			}
		}
		total = -(subtotal + taxCents)
		return nil
	}
	if err := resolveMoney(ctx); err != nil {
		return nil, err
	}
	// A card refund goes through the gateway before the transaction, so a
	// decline aborts with nothing persisted. Every guard that needs no lock
	// (the sale's status, the caps) has run by now; a refund the transaction
	// then refuses is recorded in a committed row of its own (gateway.go).
	// The refund targets the sale's own card tender's gateway transaction,
	// never a transaction the client names, and never more than that tender
	// kept net of change and of earlier card refunds (third review P2-1,
	// fourth review P2-4): a return with no sale behind it has no card of
	// the business to refund.
	var refundTaken *gatewayRefund
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
		already, err := s.repo.CardRefundedCents(ctx, *in.OriginalSaleID)
		if err != nil {
			return nil, err
		}
		if -total > int64(target.AmountCents)-already {
			return nil, conflict("exceeds_card_tender",
				fmt.Sprintf("the card tender kept %d cents less the %d already refunded to the card: refund the rest from the drawer",
					int64(target.AmountCents), already))
		}
		res, err := s.gateway.Refund(ctx, *target.GatewayTxID, -total)
		if err != nil {
			return nil, fmt.Errorf("card refund failed: %w", err)
		}
		if res.Status != "REFUNDED" && res.Status != "APPROVED" {
			return nil, conflict("card_refund", fmt.Sprintf("the card refund was not accepted (%s)", res.Status))
		}
		gatewayTxID := *target.GatewayTxID
		refundTaken = &gatewayRefund{gatewayTxID: gatewayTxID, refundTxID: res.TransactionID,
			amountCents: -total, act: "return", entity: *in.OriginalSaleID, actor: actor}
		// the refund row names the tender's own gateway transaction, so the
		// money is reconcilable against the charge it came from
		in.GatewayTxID = gatewayTxID
	}
	var tillSessionID *uuid.UUID
	var out *Return
	var invoiceLines []InvoiceLineCost
	var invoiceLineIDs map[uuid.UUID]uuid.UUID
	err = s.inTx(ctx, func(ctx context.Context) error {
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
			// The cap, rechecked under the lock: a return that raced this one
			// for the same line is counted before this one is allowed.
			if err := s.checkReturnCaps(ctx, in.OriginalSaleID, priced, byID); err != nil {
				return err
			}
			// The remainder rule and the totals, restated under the lock: the
			// money is written from these answers, never from the pre
			// transaction read (a card refund already taken keeps the amount
			// it took; the race window is the cents a concurrent return's
			// rounding moves).
			if err := applyRemainders(ctx); err != nil {
				return err
			}
			if err := resolveMoney(ctx); err != nil {
				return err
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
			var uomQty, priceUOMQty httpx.Quantity = salesdoc.One, salesdoc.One
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
				UnitPrice: unitPrice, LineTotal: ptrC(-p.lineTotal), Restock: p.in.Restock,
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
				for j := range saleLines {
					comp := &saleLines[j]
					if comp.ParentLineID == nil || *comp.ParentLineID != p.saleLine.ID ||
						comp.Quantity == nil || comp.ProductID == nil {
						continue
					}
					qtyBack := httpx.Quantity(divRound(int64(*comp.Quantity)*int64(p.in.Quantity), int64(*p.saleLine.Quantity)))
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
						restockCost += int64(salesdoc.CostOf(qtyBack, cost))
					}
					restocks = append(restocks, restockAct{productID: *comp.ProductID, qty: qtyBack})
				}
				continue
			}
			if p.in.ProductID != nil {
				// a free line (no sale) restocks at today's average: it has
				// no source invoice line to read
				cost := restockUnitCost
				if cost <= 0 {
					if ref, ok := refs[p.in.ProductID.String()]; ok {
						cost = ref.AverageCost
					}
				}
				if cost > 0 {
					restockCost += int64(salesdoc.CostOf(p.in.Quantity, cost))
				}
				restocks = append(restocks, restockAct{productID: *p.in.ProductID, qty: p.in.Quantity})
			}
		}
		if _, err := s.db.GetExecutor(ctx).Exec(ctx, `
			INSERT INTO credit_memos (id, customer_id, branch_id, currency, reason_code, reason, status, amount,
				subtotal, tax_amount, total_amount, amount_open, memo_date, created_at, updated_at, revision)
			VALUES ($1, $2, $3, $4, 'RETURN', $5, 'DRAFT', 0, 0, 0, 0, 0, $6::date, NOW(), NOW(), 1)`,
			memoID, customerID, branchID, facts.Currency, in.Reason, date.Format("2006-01-02")); err != nil {
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
					uom, price_uom, uom_qty, price_uom_qty, unit_price, price_source, line_total, taxable, restock,
					invoice_line_id, created_at)
				VALUES ($1, $2, $15, $3, $16, $4, -($5::numeric / 10000), $6, $7, $8::numeric / 10000, $9::numeric / 10000,
					$10::numeric / 10000, 'MANUAL', $11::numeric / 100, $12, $13, $14, NOW())`,
				memoID, l.Position, l.ProductID, l.Description, qtyArg(l.Quantity), l.UOM, l.PriceUOM,
				qtyArg(l.UOMQty), qtyArg(l.PriceUOMQty), priceArg(l.UnitPrice),
				centsArg(l.LineTotal), priced[i].taxable, l.Restock, l.InvoiceLineID, lineType, l.ChargeCodeID); err != nil {
				return fmt.Errorf("failed to create the credit memo line: %w", mapWriteError(err))
			}
		}
		// The gapless CM number, minted late: after every other row lock,
		// before the posting (ADR 0005 section 4.1).
		memoNumber, err := httpx.NextGaplessNumber(ctx, s.db.GetExecutor(ctx), "credit_memo", "CM", httpx.DefaultDocNumberWidth)
		if err != nil {
			return err
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
		if taxCents > 0 {
			legs = append(legs, gl.Leg{AccountCode: gl.AccountCodeSalesTax, Description: "Sales Tax Payable", Debit: taxCents})
		}
		if restockCost > 0 {
			legs = append(legs,
				gl.Leg{AccountCode: gl.AccountCodeInventory, Description: "Inventory", Debit: restockCost},
				gl.Leg{AccountCode: gl.AccountCodeCOGS, Description: "Cost of Goods Sold", Credit: restockCost})
		}
		fxPost, err := s.ar.PostCreditMemo(ctx, account.PostCreditMemoIn{
			MemoID: memoID, CustomerID: *customerID, Number: memoNumber, Currency: facts.Currency, MemoDate: date,
			SubtotalCents: -subtotal, TaxCents: -taxCents, TotalCents: total, TaxRate: taxRate, Actor: actor, Legs: legs})
		if err != nil {
			return err
		}
		var fxAll *account.Effects
		mergeEffects(&fxAll, fxPost)
		// The stock returns: each restock act back on hand (a product line's
		// own units, a kit's components; the kit product itself never stocks).
		for i := range restocks {
			if err := s.inventory.RestockQty(ctx, restocks[i].productID, *branchID, restocks[i].qty); err != nil {
				return err
			}
		}
		// The refund: cash or card pays the credit out (DR 1020 / CR 1010),
		// account leaves the credit open.
		if in.RefundMethod == RefundCash || in.RefundMethod == RefundCard {
			_, rfx, err := s.ar.RefundCreditMemo(ctx, account.RefundCreditMemoIn{
				MemoID: memoID, AmountCents: -total, Reason: in.Reason, Method: string(in.RefundMethod),
				GatewayRefundID: in.GatewayTxID, Actor: actor, On: date})
			if err != nil {
				return err
			}
			mergeEffects(&fxAll, rfx)
		}
		// The return row, linked to its memo.
		ret := &Return{
			RegisterID: in.RegisterID, TillSessionID: tillSessionID, OriginalSaleID: in.OriginalSaleID,
			CustomerID: customerID, BranchID: branchID, CashierID: cashierID, Currency: facts.Currency,
			SubtotalCents: httpx.Cents(-subtotal), TaxCents: httpx.Cents(-taxCents), TotalCents: httpx.Cents(total),
			RefundMethod: in.RefundMethod, Reason: in.Reason, CreditMemoID: &memoID, Lines: lines,
		}
		if err := s.repo.CreateReturn(ctx, ret, lines); err != nil {
			return err
		}
		// The audit row, inside the transaction (R1-14): a rolled back return
		// leaves no row.
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.return.completed", EntityType: "pos_return", EntityID: ret.ID, UserID: actor,
				Changes: map[string]any{
					"number": ret.Number, "total_cents": total, "refund_method": in.RefundMethod.Status(),
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
				"number": nil, "customer_id": customerID, "status": "draft", "currency": facts.Currency,
				"pos_return_id": ret.ID,
			})}); err != nil {
			return err
		}
		if err := s.recordEvent(ctx, outbox.Event{Type: "credit_memo.posted", EntityType: "credit_memo", EntityID: memoID,
			BranchID: &branch, Data: eventJSON(map[string]any{
				"number": number, "customer_id": customerID, "status": "open", "from_status": "draft",
				"currency": facts.Currency, "total_cents": total,
			})}); err != nil {
			return err
		}
		if in.RefundMethod == RefundCash || in.RefundMethod == RefundCard {
			if err := s.recordEvent(ctx, outbox.Event{Type: "credit_memo.refunded", EntityType: "credit_memo", EntityID: memoID,
				BranchID: &branch, Data: eventJSON(map[string]any{
					"customer_id": customerID, "status": "applied", "currency": facts.Currency,
					"amount_cents": -total, "method": in.RefundMethod.Status(),
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
				"number": ret.Number, "customer_id": customerID, "status": "completed", "currency": facts.Currency,
				"total_cents": total, "refund_method": in.RefundMethod.Status(), "credit_memo_id": memoID,
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
		if refundTaken != nil {
			s.recordOrphanRefund(ctx, *refundTaken, err)
		}
		return nil, err
	}
	return out, nil
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
	for lineID, want := range requested {
		src, ok := byID[lineID]
		if !ok || src.Quantity == nil {
			continue
		}
		already := int64(returned[lineID])
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
