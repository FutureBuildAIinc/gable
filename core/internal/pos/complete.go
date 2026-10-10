// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// TenderIn is one parsed tender of a completion request.
type TenderIn struct {
	Method      TenderMethod
	AmountCents httpx.Cents
	Reference   string
	TokenID     string
}

// ErrTaxProviderUnavailable is the 503 the counter answers when the tax
// provider does not answer. The offline sync treats it as pending, never a
// rejection (ADR 0005 section 3).
var ErrTaxProviderUnavailable = &httpx.Error{Status: http.StatusServiceUnavailable, Code: httpx.CodeUnavailable,
	Message: "the tax provider did not answer: the sale is not completed"}

// chargeResult is a gateway charge made before the transaction opened.
type chargeResult struct {
	GatewayTxID, AuthCode, Last4, Brand string
}

// providerTax is the provider's answer, priced before the transaction with
// the fingerprint of what it priced.
type providerTax struct {
	taxCents    httpx.Cents
	fingerprint string
}

// linesFingerprint is a stable digest of the lines a tax price was taken
// against: any change to a billed line between the price and the
// transaction is a stale quote (ADR 0005 section 3).
func linesFingerprint(lines []salesdoc.Line) string {
	var b strings.Builder
	for i := range lines {
		l := &lines[i]
		fmt.Fprintf(&b, "%s|%d|%s|%s|", l.ID, derefQty(l.Quantity), derefString(l.UOM), derefString(l.PriceUOM))
		if l.LineTotal != nil {
			fmt.Fprintf(&b, "%d|%t", int64(*l.LineTotal), l.Taxable)
		}
		b.WriteByte(';')
	}
	return b.String()
}

func derefQty(q *httpx.Quantity) int64 {
	if q == nil {
		return 0
	}
	return int64(*q)
}

// prepareSaleTax prices the completion's tax with the provider BEFORE the
// transaction opens (ADR 0005 section 3). Nil means "resolve by rate inside
// the transaction". A provider failure is the 503 the caller may treat as
// pending (the offline sync).
func (s *Service) prepareSaleTax(ctx context.Context, sale *Sale, lines []salesdoc.Line, customerID uuid.UUID) (*providerTax, error) {
	if s.tax == nil || !s.tax.ProviderConfigured() {
		return nil, nil
	}
	exempt, err := s.repo.CustomerExempt(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if exempt {
		return nil, nil
	}
	totals := salesdoc.SumTotals(lines)
	req := taxPreviewRequest(sale, lines, totals.TaxableCents)
	answer, err := s.tax.PreviewTax(ctx, req)
	if err != nil {
		return nil, ErrTaxProviderUnavailable
	}
	return &providerTax{taxCents: httpx.Cents(answer.TotalTax), fingerprint: linesFingerprint(lines)}, nil
}

// CompleteSale is the counter's money moment (ADR 0005 section 14.2 C2-5):
// ONE transaction that takes the stock out, writes the invoice (origin POS,
// pickup) posted with tax and cost of goods sold, turns each cash, check or
// card tender into a payment applied to it (stored net: the tendered amount
// less change, so the payment is the money kept), leaves an ACCOUNT tender
// open on the invoice, and writes the events last. The card charges and the
// tax provider call happen before the transaction opens; their results are
// written inside it.
func (s *Service) CompleteSale(ctx context.Context, saleID uuid.UUID, ifMatch string, bodyRevision *int64,
	tenders []TenderIn, pickedUpBy, actor string) (*Sale, error) {
	if s.ar == nil || s.invoices == nil || s.inventory == nil {
		return nil, errNotConfigured
	}
	pre, err := s.repo.GetSale(ctx, saleID)
	if err != nil {
		return nil, err
	}
	if pre.Status != StatusOpen && pre.Status != StatusHeld {
		return nil, httpx.InvalidStateTransition(fmt.Sprintf("cannot complete a %s sale", pre.Status.Status()))
	}
	preLines, err := s.repo.GetLines(ctx, saleID)
	if err != nil {
		return nil, err
	}
	// The walk-in customer backs a sale that names none: the invoice and
	// its payments need a real customer row.
	customerID := pre.CustomerID
	if customerID == nil {
		id, _, err := s.repo.WalkInCustomer(ctx)
		if err != nil {
			return nil, err
		}
		pre.WalkInID = id
		customerID = &id
	} else {
		customerID = pre.CustomerID
	}
	realCustomerID := *customerID
	// The tender plan: what the drawer keeps per tender and what walks out
	// as change (only cash makes change; an ACCOUNT tender needs a named
	// customer and passes the credit check).
	var totalTendered, cashTendered, accountPortion int64
	for i := range tenders {
		t := &tenders[i]
		totalTendered += int64(t.AmountCents)
		if t.Method == TenderCash {
			cashTendered += int64(t.AmountCents)
		}
		if t.Method == TenderAccount {
			accountPortion += int64(t.AmountCents)
		}
	}
	totals := salesdoc.SumTotals(preLines)
	total := int64(totals.SubtotalCents) + int64(s.estimateTax(ctx, pre, totals.TaxableCents))
	change := totalTendered - total
	if change < 0 {
		return nil, conflict("insufficient_tender",
			fmt.Sprintf("the sale needs %d cents and the tenders carry %d", total, totalTendered))
	}
	if change > 0 && cashTendered < change {
		return nil, conflict("change_from_cash",
			"only a cash tender can give change: the over tender exceeds the cash taken")
	}
	if accountPortion > 0 {
		if pre.CustomerID == nil {
			return nil, conflict("walk_in_account",
				"an ACCOUNT tender needs a named customer: the walk-in customer buys on the barrelhead")
		}
	}
	if len(preLines) == 0 {
		return nil, invalid("lines", "the sale has no lines to complete")
	}
	// Card charges: outside the transaction, with the sale's own currency
	// (the hard coded USD of the base commit is the bug this fixes).
	charges := make([]*chargeResult, len(tenders))
	for i := range tenders {
		t := &tenders[i]
		if t.Method != TenderCard || t.TokenID == "" {
			continue
		}
		if s.gateway == nil {
			return nil, conflict("card_terminal",
				"this register has no card terminal gateway: record the card tender without a token (the terminal settles it)")
		}
		res, err := s.gateway.Charge(ctx, chargeRequest(saleID, t, pre.Currency))
		if err != nil {
			return nil, fmt.Errorf("card charge failed: %w", err)
		}
		if res.Status != chargeApproved {
			return nil, conflict("card_declined", fmt.Sprintf("the card was declined (%s)", res.Status))
		}
		charges[i] = &chargeResult{GatewayTxID: res.TransactionID, AuthCode: res.AuthCode, Last4: res.CardLast4, Brand: res.CardBrand}
	}
	priced, err := s.prepareSaleTax(ctx, pre, preLines, realCustomerID)
	if err != nil {
		return nil, err
	}
	var out *Sale
	err = s.inTx(ctx, func(ctx context.Context) error {
		// Section 11, step 1: the counter sale row.
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		sale.WalkInID = pre.WalkInID
		customerID := realCustomerID
		if err := httpx.CheckRevision(sale.Revision, ifMatch, bodyRevision); err != nil {
			return err
		}
		if sale.Status != StatusOpen && sale.Status != StatusHeld {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot complete a %s sale", sale.Status.Status()))
		}
		lines, err := s.repo.GetLines(ctx, saleID)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			return invalid("lines", "the sale has no lines to complete")
		}
		totals := salesdoc.SumTotals(lines)

		// Tax: the provider's answer (verified against what is billed now)
		// or the rate resolver. A pickup sale takes the branch rate
		// (ADR 0005 section 5.5).
		var taxCents int64
		var taxRate *string
		taxExempt := false
		taxSource := ""
		if priced != nil {
			if linesFingerprint(lines) != priced.fingerprint {
				return conflict("tax_quote_stale",
					"the sale's lines moved between the tax price and the completion: retry to price afresh")
			}
			taxCents = int64(priced.taxCents)
			taxSource = string(salesdoc.TaxSourceProvider)
		} else {
			exempt, err := s.repo.CustomerExempt(ctx, customerID)
			if err != nil {
				return err
			}
			rate, ok, err := s.repo.BranchTaxRate(ctx, &sale.BranchID)
			if err != nil {
				return err
			}
			switch {
			case exempt:
				taxSource = string(salesdoc.TaxSourceExempt)
				zero := "0"
				taxRate = &zero
			case ok:
				scaled, _, err := salesdoc.ParseTaxRate(rate)
				if err != nil {
					return err
				}
				taxCents = int64(salesdoc.TaxAt(totals.TaxableCents, scaled))
				taxRate = &rate
				taxSource = string(salesdoc.TaxSourceBranchRate)
			default:
				return conflict("tax_rate_not_configured",
					"set the branch's default tax rate before completing a sale")
			}
		}
		total := int64(totals.SubtotalCents) + taxCents
		if totalTendered-recomputedChange(tenders, total) < 0 {
			return conflict("insufficient_tender",
				fmt.Sprintf("the sale needs %d cents and the tenders carry %d", total, totalTendered))
		}

		// The credit check an ACCOUNT tender owes (ADR 0005 14.2 C2-5).
		if accountPortion > 0 {
			facts, err := s.repo.CustomerFacts(ctx, customerID)
			if err != nil {
				return err
			}
			if facts.CreditLimitCents != nil {
				open, err := s.repo.OpenReceivableCents(ctx, customerID)
				if err != nil {
					return err
				}
				if open+accountPortion > int64(*facts.CreditLimitCents) {
					return conflict("credit_limit",
						fmt.Sprintf("the customer's open receivable plus the %d cents on account is over the credit limit", accountPortion))
				}
			}
		}

		// Stock out: each stocked line's quantity from on hand, in
		// (product id, line id) order (section 11, step 6).
		stocked := stockedLines(lines)
		for _, i := range stocked {
			l := &lines[i]
			if err := s.inventory.IssueQty(ctx, *l.ProductID, sale.BranchID, *l.Quantity); err != nil {
				if errors.Is(err, inventoryErrInsufficient) {
					return conflict("insufficient_stock",
						fmt.Sprintf("line %s asks for more %s than the branch holds on hand", l.ID, (*l.Quantity).DecimalString()))
				}
				return err
			}
		}

		// The invoice: origin POS, pickup, its lines the sale's, posted
		// with the tax and the cost of goods sold (8.2 and 8.4).
		date, err := s.repo.BranchLocalDate(ctx, sale.BranchID, s.now())
		if err != nil {
			return err
		}
		var pickedBy *string
		if pickedUpBy != "" {
			p := pickedUpBy
			pickedBy = &p
		}
		inv := &invoice.CounterInvoice{
			BranchID: sale.BranchID, CustomerID: customerID, Currency: sale.Currency,
			PickedUpBy: pickedBy, InvoiceDate: date, Actor: actor,
			SubtotalCents: int64(totals.SubtotalCents), TaxCents: taxCents, TotalCents: total,
			TaxRate: taxRate, TaxExempt: taxExempt, TaxSource: taxSource,
		}
		refs, err := s.costRefs(ctx, lines)
		if err != nil {
			return err
		}
		for n := range lines {
			b := &lines[n]
			fl := invoice.FulfilmentLine{
				ID: uuid.New(), Position: n, LineType: string(b.LineType), ProductID: b.ProductID,
				ChargeCodeID: b.ChargeCodeID, SKU: b.SKU, Description: b.Description, UOM: b.UOM, PriceUOM: b.PriceUOM,
				PriceSource: string(b.PriceSource), Taxable: b.Taxable, RevenueAccountCode: b.RevenueAccountCode,
				OverrideReason: b.OverrideReason, PriceAdjustedBy: b.PriceAdjustedBy,
			}
			if b.PricedUnitPrice != nil {
				p := int64(*b.PricedUnitPrice)
				fl.PricedUnitPrice = &p
			}
			if b.ParentLineID != nil {
				fl.ParentLineID = b.ParentLineID
			}
			if b.LineType != salesdoc.LineText {
				q := int64(*b.Quantity)
				fl.Quantity = &q
				uq, pq := int64(*b.UOMQty), int64(*b.PriceUOMQty)
				fl.UOMQty, fl.PriceUOMQty = &uq, &pq
				up := int64(*b.UnitPrice)
				fl.UnitPrice = &up
				lt := int64(*b.LineTotal)
				fl.LineTotalCents = &lt
				if b.DiscountPercent != nil {
					dp := int64(*b.DiscountPercent)
					fl.DiscountPercent = &dp
				}
				if b.DiscountAmount != nil {
					dc := int64(*b.DiscountAmount)
					fl.DiscountCents = &dc
				}
				fl.DiscountReason = b.DiscountReason
				if b.ProductID != nil && (b.LineType == salesdoc.LineProduct || b.LineType == salesdoc.LineComponent) {
					if ref, ok := refs[b.ProductID.String()]; ok && ref.AverageCost > 0 {
						uc := int64(ref.AverageCost)
						fl.UnitCost = &uc
						cost := int64(salesdoc.CostOf(*b.Quantity, ref.AverageCost))
						fl.CostCents = cost
						inv.CostCents += cost
					}
				}
			}
			inv.Lines = append(inv.Lines, fl)
		}
		for _, g := range salesdoc.RevenueGroups(lines) {
			inv.Revenue = append(inv.Revenue, invoice.RevenueLeg{AccountCode: g.AccountCode, Cents: int64(g.Cents)})
		}
		if err := s.invoices.CreateCounterInvoice(ctx, inv); err != nil {
			return err
		}

		// The tenders become payments applied to the invoice, stored net:
		// change comes out of the cash tendered, so the payment is the
		// money kept (ADR 0005 14.2 C2-5).
		change := totalTendered - total
		cashChangeLeft := change
		var fxAll *account.Effects
		for i := range tenders {
			t := &tenders[i]
			if t.Method == TenderAccount {
				tender := &Tender{SaleID: saleID, Method: t.Method, AmountCents: t.AmountCents}
				if t.Reference != "" {
					r := t.Reference
					tender.Reference = &r
				}
				if err := s.repo.AddTender(ctx, tender); err != nil {
					return err
				}
				continue
			}
			amount := int64(t.AmountCents)
			if t.Method == TenderCash && cashChangeLeft > 0 {
				give := min64(amount, cashChangeLeft)
				amount -= give
				cashChangeLeft -= give
			}
			if amount <= 0 {
				// the whole tender walked out as change: no payment, but
				// the tender row keeps what was tendered
				tender := &Tender{SaleID: saleID, Method: t.Method, AmountCents: t.AmountCents}
				if t.Reference != "" {
					r := t.Reference
					tender.Reference = &r
				}
				if err := s.repo.AddTender(ctx, tender); err != nil {
					return err
				}
				continue
			}
			card := (*account.CardFacts)(nil)
			if t.Method == TenderCard && charges[i] != nil {
				card = &account.CardFacts{GatewayTxID: charges[i].GatewayTxID, GatewayStatus: "SETTLED",
					Last4: charges[i].Last4, Brand: charges[i].Brand, AuthCode: charges[i].AuthCode}
			}
			payID, fx, err := s.ar.RecordPayment(ctx, account.RecordPaymentIn{
				CustomerID: customerID, BranchID: sale.BranchID, Currency: sale.Currency,
				Method: string(t.Method), AmountCents: amount, Reference: t.Reference, ReceivedOn: date,
				Actor: actor, Card: card,
				Applications: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: amount}},
			})
			if err != nil {
				return err
			}
			mergeEffects(&fxAll, fx)
			tender := &Tender{SaleID: saleID, Method: t.Method, AmountCents: httpx.Cents(amount),
				PaymentID: &payID}
			if t.Reference != "" {
				r := t.Reference
				tender.Reference = &r
			}
			if charges[i] != nil {
				g, a, l, b := charges[i].GatewayTxID, charges[i].AuthCode, charges[i].Last4, charges[i].Brand
				tender.GatewayTxID, tender.AuthCode = &g, &a
				tender.CardLast4, tender.CardBrand = &l, &b
			}
			if err := s.repo.AddTender(ctx, tender); err != nil {
				return err
			}
			number, _ := s.paymentNumber(ctx, payID)
			if err := s.recordEvent(ctx, outbox.Event{Type: "payment.recorded", EntityType: "payment", EntityID: payID,
				BranchID: &sale.BranchID, Data: eventJSON(map[string]any{
					"number": number, "customer_id": customerID, "status": "posted", "currency": sale.Currency,
					"amount_cents": amount, "unapplied_cents": 0, "register_id": sale.RegisterID,
				})}); err != nil {
				return err
			}
		}
		if err := s.repo.CompleteSale(ctx, saleID, inv.ID, int64(totals.SubtotalCents), taxCents, total, change); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.transaction.completed", EntityType: "pos_transaction", EntityID: saleID, UserID: actor,
				Changes: map[string]any{
					"number": sale.Number, "invoice_id": inv.ID, "total_cents": total, "tax_cents": taxCents,
					"register_id": sale.RegisterID, "customer_id": sale.CustomerID, "tenders": len(tenders),
				}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}

		// The events, last (ADR 0005 section 11 step 10): the invoice's,
		// the AR core's, then the sale's own.
		branch := sale.BranchID
		if err := s.recordEvent(ctx, outbox.Event{Type: "invoice.created", EntityType: "invoice", EntityID: inv.ID,
			BranchID: &branch, Data: eventJSON(map[string]any{
				"number": inv.Number, "customer_id": customerID, "order_id": nil, "status": "unpaid",
				"currency": sale.Currency, "total_cents": total, "subtotal_cents": int64(totals.SubtotalCents),
				"tax_cents": taxCents, "origin": "pos",
			})}); err != nil {
			return err
		}
		if fxAll != nil {
			for _, ev := range fxAll.Events() {
				if err := s.recordEvent(ctx, ev); err != nil {
					return err
				}
			}
		}
		if err := s.recordEvent(ctx, outbox.Event{Type: EventSaleCompleted, EntityType: "pos_transaction", EntityID: saleID,
			BranchID: &branch, Data: eventJSON(map[string]any{
				"number": sale.Number, "customer_id": sale.CustomerID, "status": StatusCompleted.Status(),
				"from_status": StatusOpen.Status(), "revision": sale.Revision + 1, "currency": sale.Currency,
				"total_cents": total, "invoice_id": inv.ID, "change_cents": change,
			})}); err != nil {
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

// recomputedChange is the change the recomputed total implies.
func recomputedChange(tenders []TenderIn, total int64) int64 {
	var tendered int64
	for i := range tenders {
		tendered += int64(tenders[i].AmountCents)
	}
	if tendered <= total {
		return 0
	}
	return tendered - total
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func mergeEffects(dst **account.Effects, fx *account.Effects) {
	if fx == nil {
		return
	}
	if *dst == nil {
		*dst = fx
		return
	}
	(*dst).Merge(fx)
}

// stockedLines indexes the lines that move stock, in (product id, line id)
// order (section 11, step 6).
func stockedLines(lines []salesdoc.Line) []int {
	var out []int
	for i := range lines {
		if lines[i].IsStocked() {
			out = append(out, i)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := &lines[out[j]], &lines[out[j-1]]
			if *a.ProductID == *b.ProductID && a.ID.String() < b.ID.String() {
				out[j], out[j-1] = out[j-1], out[j]
			} else if productLess(a, b) {
				out[j], out[j-1] = out[j-1], out[j]
			} else {
				break
			}
		}
	}
	return out
}

func productLess(a, b *salesdoc.Line) bool {
	if a.ProductID == nil || b.ProductID == nil {
		return false
	}
	return a.ProductID.String() < b.ProductID.String()
}

// costRefs reads the products the billed lines name, for the cost of goods
// sold (8.4: the average unit cost read inside the posting transaction).
func (s *Service) costRefs(ctx context.Context, lines []salesdoc.Line) (map[string]salesdocProductRef, error) {
	var ids []uuid.UUID
	for i := range lines {
		if lines[i].ProductID != nil {
			ids = append(ids, *lines[i].ProductID)
		}
	}
	return s.repo.LookupProducts(ctx, ids)
}

// paymentNumber reads a payment's number inside the transaction, for its
// payment.recorded event.
func (s *Service) paymentNumber(ctx context.Context, id uuid.UUID) (string, error) {
	return paymentNumberOf(ctx, s, id)
}
