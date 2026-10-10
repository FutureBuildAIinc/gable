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
	if customerID == nil && in.OriginalSaleID != nil {
		if original, err := s.repo.GetSale(ctx, *in.OriginalSaleID); err == nil && original.CustomerID != nil {
			customerID = original.CustomerID
		}
	}
	if customerID == nil {
		walkInID, _, err := s.repo.WalkInCustomer(ctx)
		if err != nil {
			return nil, err
		}
		customerID = &walkInID
	}
	facts, err := s.repo.CustomerFacts(ctx, *customerID)
	if err != nil {
		return nil, err
	}
	// Price the lines: from the original sale's lines when named, else the
	// request's own price (which is then required).
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
	// lookup covers the resolved products too; a line that named no sale line
	// stands alone on the product the request named.
	resolved := make([]ReturnLineIn, len(in.Lines))
	for i := range in.Lines {
		rl := in.Lines[i]
		if rl.SaleLineID != nil {
			src, ok := byID[*rl.SaleLineID]
			if !ok {
				return nil, invalid(fmt.Sprintf("lines[%d].line_id", i), "names no line of the original sale")
			}
			if rl.UnitPrice == nil && src.UnitPrice != nil {
				p := *src.UnitPrice
				rl.UnitPrice = &p
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
	// Tax: the sale path's resolver, the branch rate (a return at the
	// counter is a pickup, 5.5), exemptions first.
	exempt, err := s.repo.CustomerExempt(ctx, *customerID)
	if err != nil {
		return nil, err
	}
	var taxableSum int64
	for i := range priced {
		if priced[i].taxable {
			taxableSum += priced[i].lineTotal
		}
	}
	subtotal := taxableSum
	for i := range priced {
		if !priced[i].taxable {
			subtotal += priced[i].lineTotal
		}
	}
	var taxCents int64
	var taxRate *string
	if exempt {
		zero := "0"
		taxRate = &zero
	} else {
		rate, ok, err := s.repo.BranchTaxRate(ctx, branchID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, conflict("tax_rate_not_configured", "set the branch's default tax rate before returning goods")
		}
		scaled, _, err := salesdoc.ParseTaxRate(rate)
		if err != nil {
			return nil, err
		}
		taxCents = int64(salesdoc.TaxAt(httpx.Cents(taxableSum), scaled))
		taxRate = &rate
	}
	total := -(subtotal + taxCents)
	// A card refund goes through the gateway before the transaction, so a
	// decline aborts with nothing persisted.
	if in.RefundMethod == RefundCard && in.GatewayTxID != "" {
		if s.gateway == nil {
			return nil, conflict("card_terminal", "this register has no card terminal gateway to refund the card")
		}
		res, err := s.gateway.Refund(ctx, in.GatewayTxID, -total)
		if err != nil {
			return nil, fmt.Errorf("card refund failed: %w", err)
		}
		if res.Status != "REFUNDED" && res.Status != "APPROVED" {
			return nil, conflict("card_refund", fmt.Sprintf("the card refund was not accepted (%s)", res.Status))
		}
	}
	var tillSessionID *uuid.UUID
	if session, err := s.repo.GetOpenTillSession(ctx, in.RegisterID); err == nil && session != nil {
		tillSessionID = &session.ID
	}
	var out *Return
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
			// The return touches the sale: its revision moves, in process, so
			// a void built on the earlier revision is refused stale.
			if err := s.repo.BumpSaleRevision(ctx, sale.ID); err != nil {
				return err
			}
		}
		date, err := s.repo.BranchLocalDate(ctx, *branchID, s.now())
		if err != nil {
			return err
		}
		// The credit memo: a DRAFT row with its negative lines, then the AR
		// core's PostCreditMemo mints the gapless number, posts the entry
		// (DR each revenue account its group's line totals, DR the tax, DR
		// 1030 / CR 5010 the restocked cost; CR 1020 the core adds) and the
		// subledger row, all in this transaction.
		memoID := uuid.New()
		var lines []ReturnLine
		var restockCost int64
		for i := range priced {
			p := &priced[i]
			qty := p.in.Quantity
			uom := "EA"
			lines = append(lines, ReturnLine{
				ID: uuid.New(), Position: i, ProductID: p.in.ProductID, Description: p.in.Description,
				Quantity: &qty, UOM: &uom, UnitPrice: p.in.UnitPrice, LineTotal: ptrC(-p.lineTotal), Restock: p.in.Restock,
			})
			if p.in.Restock && p.in.ProductID != nil {
				if ref, ok := refs[p.in.ProductID.String()]; ok && ref.AverageCost > 0 {
					restockCost += int64(salesdoc.CostOf(p.in.Quantity, ref.AverageCost))
				}
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
			if _, err := s.db.GetExecutor(ctx).Exec(ctx, `
				INSERT INTO credit_memo_lines (credit_memo_id, position, line_type, product_id, description, quantity,
					uom, price_uom, uom_qty, price_uom_qty, unit_price, price_source, line_total, taxable, restock, created_at)
				VALUES ($1, $2, 'PRODUCT', $3, $4, -($5::numeric / 10000), $6, $6, 1, 1, $7::numeric / 10000, 'MANUAL',
					$8::numeric / 100, $9, $10, NOW())`,
				memoID, l.Position, l.ProductID, l.Description, qtyArg(l.Quantity), l.UOM, priceArg(l.UnitPrice),
				centsArg(l.LineTotal), priced[i].taxable, l.Restock); err != nil {
				return fmt.Errorf("failed to create the credit memo line: %w", mapWriteError(err))
			}
		}
		// The gapless CM number, minted late: after every other row lock,
		// before the posting (ADR 0005 section 4.1).
		memoNumber, err := httpx.NextGaplessNumber(ctx, s.db.GetExecutor(ctx), "credit_memo", "CM", httpx.DefaultDocNumberWidth)
		if err != nil {
			return err
		}
		legs := []gl.Leg{{AccountCode: gl.AccountCodeRevenue, Description: "Sales Revenue", Debit: subtotal}}
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
		// The stock returns: restock lines back on hand.
		for i := range lines {
			l := &lines[i]
			if !l.Restock || l.ProductID == nil {
				continue
			}
			if err := s.inventory.RestockQty(ctx, *l.ProductID, *branchID, *l.Quantity); err != nil {
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
		return nil, err
	}
	return out, nil
}

func ptrC(v int64) *httpx.Cents {
	c := httpx.Cents(v)
	return &c
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
func (s *Service) GetReturn(ctx context.Context, id uuid.UUID) (*Return, error) { return s.repo.GetReturn(ctx, id) }

// ListReturns lists returns for a register on a date.
func (s *Service) ListReturns(ctx context.Context, f ReturnFilter) ([]Return, error) {
	return s.repo.ListReturns(ctx, f, 200)
}
