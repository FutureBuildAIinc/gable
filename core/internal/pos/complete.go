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
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return nil, err
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
	// a sale named before a grant change is held again: the walk-in
	// customer backs a sale that names none and is not the caller's to name
	if pre.CustomerID != nil {
		if _, _, err := s.namedCustomer(ctx, realCustomerID); err != nil {
			return nil, err
		}
	}
	if len(preLines) == 0 {
		return nil, invalid("lines", "the sale has no lines to complete")
	}
	if err := s.checkLineStockUnits(ctx, preLines); err != nil {
		return nil, err
	}
	// The tax, priced before the transaction with the same resolver the
	// transaction uses (exemption first, then the provider or the branch
	// rate), so the tender pre-check refuses the obvious cases before a card
	// is charged; the transaction's own check against the live total under
	// the lock is the authority.
	priced, err := s.prepareSaleTax(ctx, pre, preLines, realCustomerID)
	if err != nil {
		return nil, err
	}
	estimate := priced
	if estimate == nil {
		exempt, err := s.repo.CustomerExempt(ctx, realCustomerID)
		if err != nil {
			return nil, err
		}
		if exempt {
			estimate = &providerTax{}
		} else {
			estimate = &providerTax{taxCents: httpx.Cents(s.estimateTax(ctx, pre, salesdoc.SumTotals(preLines).TaxableCents))}
		}
	}
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
	total := int64(totals.SubtotalCents) + int64(estimate.taxCents)
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
	// The credit check an ACCOUNT tender owes, read before the card is
	// charged (a refusal must never move money at the terminal); the
	// transaction repeats it under the customer's credit lock.
	if accountPortion > 0 {
		facts, err := s.repo.CustomerFacts(ctx, realCustomerID)
		if err != nil {
			return nil, err
		}
		// An account that asks for a purchase order is not sold to on
		// account without one; the check runs before any card is charged.
		if facts.PORequired && (pre.CustomerPO == nil || strings.TrimSpace(*pre.CustomerPO) == "") {
			return nil, conflict("po_required",
				"the customer requires a purchase order number: name it on the sale before charging it to the account")
		}
		if err := s.checkAccountCredit(ctx, realCustomerID, accountPortion); err != nil {
			return nil, err
		}
	}
	// Card charges: outside the transaction, with the sale's own currency
	// (the hard coded USD of the base commit is the bug this fixes). Every
	// charge the transaction then refuses is reversed (gateway.go), and a
	// tender whose charge is declined or fails reverses every charge the
	// tenders before it already took (fourth review P1-3): no card is left
	// charged behind a sale that never was.
	type chargeMade struct {
		result      *chargeResult
		amountCents int64
	}
	var chargesMade []chargeMade
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
		if err != nil || res.Status != chargeApproved {
			// the charges already made go back before the refusal answers,
			// every one of them (a reversal that fails must not leave the
			// later charges standing): the first failure is answered, the
			// rest are still attempted
			var firstErr error
			for _, c := range chargesMade {
				if rerr := s.reverseCharge(ctx, c.result.GatewayTxID, c.amountCents, saleID, actor, err); rerr != nil && firstErr == nil {
					firstErr = rerr
				}
			}
			if firstErr != nil {
				return nil, firstErr
			}
			if err != nil {
				return nil, fmt.Errorf("card charge failed: %w", err)
			}
			return nil, conflict("card_declined", fmt.Sprintf("the card was declined (%s)", res.Status))
		}
		charges[i] = &chargeResult{GatewayTxID: res.TransactionID, AuthCode: res.AuthCode, Last4: res.CardLast4, Brand: res.CardBrand}
		chargesMade = append(chargesMade, chargeMade{result: charges[i], amountCents: int64(t.AmountCents)})
	}
	var out *Sale
	out, err = s.completeSaleTx(ctx, saleID, ifMatch, bodyRevision, tenders, pickedUpBy, actor, pre, realCustomerID, charges, priced)
	if err != nil {
		// The transaction refused after the gateway approved a charge: the
		// charge is put back before the refusal returns, so no card is left
		// charged behind a sale that never was; every charge is attempted
		// (a reversal that fails must not leave the later charges standing)
		// and a reversal that fails too answers 502 charge_not_reversed
		// instead of the refusal.
		var firstErr error
		for _, c := range chargesMade {
			if rerr := s.reverseCharge(ctx, c.result.GatewayTxID, c.amountCents, saleID, actor, err); rerr != nil && firstErr == nil {
				firstErr = rerr
			}
		}
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, err
	}
	return out, nil
}

// refuseHeldAccount refuses every account charge to a customer whose credit
// limit is zero ("no credit", the account on hold), whatever unapplied cash
// the account holds: the standing the counter shows is the rule the server
// keeps.
func refuseHeldAccount(facts CustomerFacts) error {
	if facts.CreditLimitCents != nil && *facts.CreditLimitCents <= 0 {
		return conflict("credit_hold",
			"this account is on hold: it has no credit, so nothing can be charged to it")
	}
	return nil
}

// checkAccountCredit refuses an ACCOUNT portion that would take the customer
// over its credit limit (ADR 0005 14.2 C2-5). Caller-side lock-free read; the
// authoritative check runs inside the transaction under the customer's
// credit lock.
func (s *Service) checkAccountCredit(ctx context.Context, customerID uuid.UUID, accountPortion int64) error {
	facts, err := s.repo.CustomerFacts(ctx, customerID)
	if err != nil {
		return err
	}
	if err := refuseHeldAccount(facts); err != nil {
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
	return nil
}

// completeSaleTx runs the completion's one transaction (ADR 0005 14.2 C2-5):
// the sale row, the session it lives in, the live tender check, the credit
// check under the customer's credit lock, the stock out, the invoice with its
// tax and COGS, the payments, the audit row and the events, last. The tenders
// and charges the caller resolved (the gateway charges made before it opened)
// come with it.
func (s *Service) completeSaleTx(ctx context.Context, saleID uuid.UUID, ifMatch string, bodyRevision *int64,
	tenders []TenderIn, pickedUpBy, actor string, pre *Sale, realCustomerID uuid.UUID,
	charges []*chargeResult, priced *providerTax) (*Sale, error) {
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
	var out *Sale
	err := s.inTx(ctx, func(ctx context.Context) error {
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
		if sale.CustomerID != nil {
			if _, _, err := s.namedCustomer(ctx, *sale.CustomerID); err != nil {
				return err
			}
		}
		if err := httpx.CheckRevision(sale.Revision, ifMatch, bodyRevision); err != nil {
			return err
		}
		if sale.Status != StatusOpen && sale.Status != StatusHeld {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot complete a %s sale", sale.Status.Status()))
		}
		// The session the sale lives in, FOR SHARE against the close's FOR
		// UPDATE: a sale never completes into a drawer being counted; after
		// the close the money has been counted and the way back is a return.
		if sale.TillSessionID != nil {
			if err := s.repo.LockTillSession(ctx, *sale.TillSessionID, false); err != nil {
				return err
			}
			session, err := s.repo.GetTillSession(ctx, *sale.TillSessionID)
			if err != nil {
				return err
			}
			if session.Status != TillOpen {
				return conflict("session_closed",
					"the till session this sale was made in is closed: the money has been counted and the sale cannot complete into it")
			}
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
		// The tender check against the LIVE total (the tax is known now, the
		// lines locked): the change is the tenders less the total, an under
		// tender is refused and so is a change above the cash taken; a
		// negative change is never written.
		change := totalTendered - total
		if change < 0 {
			return conflict("insufficient_tender",
				fmt.Sprintf("the sale needs %d cents and the tenders carry %d", total, totalTendered))
		}
		if change > cashTendered {
			return conflict("change_from_cash",
				"only a cash tender can give change: the over tender exceeds the cash taken")
		}

		// The credit check an ACCOUNT tender owes (ADR 0005 14.2 C2-5), under
		// the customer's credit advisory lock (section 11, step 1a, taken
		// right after the sale row): without it two concurrent ACCOUNT sales
		// each read an exposure that does not yet count the other and both
		// pass.
		if accountPortion > 0 {
			if err := s.repo.LockCustomerCredit(ctx, customerID); err != nil {
				return err
			}
			facts, err := s.repo.CustomerFacts(ctx, customerID)
			if err != nil {
				return err
			}
			if err := refuseHeldAccount(facts); err != nil {
				return err
			}
			if facts.PORequired && (sale.CustomerPO == nil || strings.TrimSpace(*sale.CustomerPO) == "") {
				return conflict("po_required",
					"the customer requires a purchase order number: name it on the sale before charging it to the account")
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

		// Stock out: each stocked line's stock quantity from on hand, in
		// (product id, line id) order (section 11, step 6).
		if err := s.checkLineStockUnits(ctx, lines); err != nil {
			return err
		}
		stocked := stockedLines(lines)
		for _, i := range stocked {
			l := &lines[i]
			sq := salesdoc.StockQty(l)
			if err := s.inventory.IssueQty(ctx, *l.ProductID, sale.BranchID, sq); err != nil {
				if errors.Is(err, inventoryErrInsufficient) {
					return conflict("insufficient_stock",
						fmt.Sprintf("line %s asks for more %s than the branch holds on hand", l.ID, sq.DecimalString()))
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
			CustomerPO: sale.CustomerPO, JobID: sale.JobID,
		}
		refs, err := s.costRefs(ctx, lines)
		if err != nil {
			return err
		}
		// The sale line id to invoice line id map: a component's parent, a
		// pos sale line id on the sale, becomes the kit's own INVOICE line id
		// here (the parent is built before its components, as position
		// orders them).
		invoiceLineOf := map[uuid.UUID]uuid.UUID{}
		for n := range lines {
			b := &lines[n]
			fl := invoice.FulfilmentLine{
				ID: uuid.New(), Position: n, LineType: string(b.LineType), ProductID: b.ProductID,
				ChargeCodeID: b.ChargeCodeID, SKU: b.SKU, Description: b.Description, UOM: b.UOM, PriceUOM: b.PriceUOM,
				StockUOM: b.StockUOM, PriceBasis: b.PriceBasis, CostSource: b.CostSource,
				PriceSource: string(b.PriceSource), Taxable: b.Taxable, RevenueAccountCode: b.RevenueAccountCode,
				OverrideReason: b.OverrideReason, PriceAdjustedBy: b.PriceAdjustedBy,
			}
			if b.Tally != nil && len(b.Tally.Rows) > 0 {
				if th := b.TallyThickness(); th != nil {
					t := int64(*th)
					fl.BoardThicknessIn = &t
				}
				if wi := b.TallyWidth(); wi != nil {
					w := int64(*wi)
					fl.BoardWidthIn = &w
				}
				fl.TallyRows = make([]invoice.TallyRowInput, 0, len(b.Tally.Rows))
				for _, r := range b.Tally.Rows {
					fl.TallyRows = append(fl.TallyRows, invoice.TallyRowInput{Pieces: r.Pieces, LengthFT: int64(r.LengthFT)})
				}
			}
			invoiceLineOf[b.ID] = fl.ID
			if b.PricedUnitPrice != nil {
				p := int64(*b.PricedUnitPrice)
				fl.PricedUnitPrice = &p
			}
			if b.ParentLineID != nil {
				parent := invoiceLineOf[*b.ParentLineID]
				fl.ParentLineID = &parent
			}
			if b.LineType != salesdoc.LineText {
				q := int64(*b.Quantity)
				fl.Quantity = &q
				if b.StockQuantity != nil {
					sq := int64(*b.StockQuantity)
					fl.StockQuantity = &sq
				} else if b.ProductID != nil {
					sq := int64(salesdoc.StockQty(b))
					fl.StockQuantity = &sq
				}
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
						cost := int64(salesdoc.CostOf(salesdoc.StockQty(b), ref.AverageCost))
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
		// The stored link each sale line now carries to the invoice line built
		// from it: a linked return reads its cost and its memo link from this
		// id, never from a position (a removed cart line leaves the sale's
		// positions gapped while the invoice numbers its own lines from zero).
		if err := s.repo.LinkInvoiceLines(ctx, saleID, invoiceLineOf); err != nil {
			return err
		}

		// The tenders become payments applied to the invoice, stored net:
		// change comes out of the cash tendered, so the payment is the
		// money kept (ADR 0005 14.2 C2-5). The change is the checked value
		// from the live total above.
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

// checkLineStockUnits verifies that every stocked line in lines still matches
// its product's current stocking unit (ADR 0006 section 3.4).
func (s *Service) checkLineStockUnits(ctx context.Context, lines []salesdoc.Line) error {
	var ids []uuid.UUID
	for i := range lines {
		if lines[i].IsStocked() && lines[i].ProductID != nil && lines[i].StockUOM != nil && *lines[i].StockUOM != "" {
			ids = append(ids, *lines[i].ProductID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	refs, err := s.repo.LookupProducts(ctx, ids)
	if err != nil {
		return err
	}
	for i := range lines {
		l := &lines[i]
		if !l.IsStocked() || l.ProductID == nil || l.StockUOM == nil || *l.StockUOM == "" {
			continue
		}
		if ref, ok := refs[l.ProductID.String()]; ok && ref.UOMPrimary != "" && *l.StockUOM != ref.UOMPrimary {
			return salesdoc.StockUnitChangedError(i, *l.StockUOM, ref.UOMPrimary)
		}
	}
	return nil
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
