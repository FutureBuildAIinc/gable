// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// ApplyLine is one invoice a payment or credit memo settles: the cash (or credit)
// applied and, for a payment, the early pay discount taken beside it.
type ApplyLine struct {
	InvoiceID     uuid.UUID
	AmountCents   int64
	DiscountCents int64
}

// CardFacts are the gateway facts of a card payment; the token is stored, never
// shown.
type CardFacts struct {
	GatewayTxID, GatewayStatus, TokenID, Last4, Brand, AuthCode string
}

// RecordPaymentIn is a payment received. Applications, when sent, settle
// invoices in the same act.
type RecordPaymentIn struct {
	PaymentID          uuid.UUID // optional; new when nil
	CustomerID         uuid.UUID
	BranchID           uuid.UUID
	Currency, Method   string
	AmountCents        int64
	Reference, Notes   string
	ReceivedOn         time.Time
	OrderID, ProjectID *uuid.UUID
	Card               *CardFacts
	Actor              string
	Applications       []ApplyLine
}

// RecordPayment writes the payment (POSTED, the whole amount unapplied), posts
// its receipt (DR 1010 / CR 2200 for the full amount) and applies it when
// applications are sent: each application its own entry and subledger row.
func (s *Service) RecordPayment(ctx context.Context, in RecordPaymentIn) (uuid.UUID, *Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return uuid.Nil, nil, err
	}
	if in.AmountCents <= 0 {
		return uuid.Nil, nil, invalidField("amount_cents", "must be greater than zero")
	}
	id := in.PaymentID
	if id == uuid.Nil {
		id = uuid.New()
	}
	// Section 11: the invoices an application names (step 4) are locked before
	// the customer row (step 7), and the customer row before the payment insert,
	// whose foreign key takes a key share on it that two receipts of one
	// customer would otherwise hold against each other's FOR UPDATE.
	if len(in.Applications) > 0 {
		ids := make([]uuid.UUID, len(in.Applications))
		for i, l := range in.Applications {
			ids[i] = l.InvoiceID
		}
		sort.Slice(ids, func(a, b int) bool { return ids[a].String() < ids[b].String() })
		if _, err := s.lockInvoices(ctx, ids, func(j int) string {
			for i, l := range in.Applications {
				if l.InvoiceID == ids[j] {
					return lineField("applications", i, "invoice_id")
				}
			}
			return "applications"
		}); err != nil {
			return uuid.Nil, nil, err
		}
	}
	if _, err := s.lockCustomer(ctx, in.CustomerID); err != nil {
		return uuid.Nil, nil, err
	}
	card := in.Card
	if card == nil {
		card = &CardFacts{}
	}
	var number string
	err := s.ex(ctx).QueryRow(ctx, `
		INSERT INTO payments (id, customer_id, branch_id, currency, method, amount, amount_unapplied, reference, notes, received_on,
			order_id, project_id, gateway_tx_id, gateway_status, token_id, card_last4, card_brand, auth_code)
		VALUES ($1, $2, $3, $4, $5, $6::bigint::numeric / 100, $6::bigint::numeric / 100, NULLIF($7, ''), NULLIF($8, ''), $9::date,
			$10, $11, NULLIF($12, ''), NULLIF($13, ''), NULLIF($14, ''), NULLIF($15, ''), NULLIF($16, ''), NULLIF($17, ''))
		RETURNING number`,
		id, in.CustomerID, in.BranchID, in.Currency, in.Method, in.AmountCents, in.Reference, in.Notes, date(in.ReceivedOn),
		in.OrderID, in.ProjectID, card.GatewayTxID, card.GatewayStatus, card.TokenID, card.Last4, card.Brand, card.AuthCode).Scan(&number)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("failed to record the payment: %w", err)
	}
	fx := &Effects{}
	payID := id
	entry, err := s.post(ctx, gl.PostingInput{EntryDate: in.ReceivedOn, Memo: "Payment " + number, Source: gl.SourcePayment,
		SourceRefID: &payID, Currency: in.Currency, PostedBy: in.Actor, Legs: []gl.Leg{
			{AccountCode: gl.AccountCodeCash, Description: "Cash", Debit: in.AmountCents},
			{AccountCode: gl.AccountCodeCustomerDeposit, Description: "Customer Deposits", Credit: in.AmountCents},
		}})
	if err != nil {
		return uuid.Nil, nil, err
	}
	if entry != nil {
		fx.entry(entry)
		if _, err := s.ex(ctx).Exec(ctx, `UPDATE payments SET gl_entry_id = $2 WHERE id = $1`, id, *entry); err != nil {
			return uuid.Nil, nil, fmt.Errorf("failed to record the receipt entry: %w", err)
		}
	}
	if len(in.Applications) > 0 {
		pay, err := s.lockPayment(ctx, id)
		if err != nil {
			return uuid.Nil, nil, err
		}
		if err := s.applyLocked(ctx, fx, pay, in.Applications, in.ReceivedOn, in.Actor, "applications"); err != nil {
			return uuid.Nil, nil, err
		}
	}
	return id, fx, nil
}

// ApplyIn applies unapplied cash of an existing payment.
type ApplyIn struct {
	PaymentID uuid.UUID
	Lines     []ApplyLine
	On        time.Time
	Actor     string
}

// Apply applies a payment's unapplied cash to invoices (section 9.2).
func (s *Service) Apply(ctx context.Context, in ApplyIn) (*Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return nil, err
	}
	pay, err := s.lockPayment(ctx, in.PaymentID)
	if err != nil {
		return nil, err
	}
	if pay.Status != "POSTED" {
		return nil, conflict("payment_voided", "the payment is voided: it applies nothing")
	}
	fx := &Effects{}
	if err := s.applyLocked(ctx, fx, pay, in.Lines, in.On, in.Actor, "applications"); err != nil {
		return nil, err
	}
	return fx, nil
}

// maxDiscount is the early pay discount an invoice allows in total:
// round_half_away(total x percent / 100), percent kept in ten thousandths.
func maxDiscount(total int64, percentTT int64) int64 {
	return (total*percentTT*2 + 1_000_000) / 2_000_000
}

func lineField(prefix string, i int, name string) string {
	return fmt.Sprintf("%s[%d].%s", prefix, i, name)
}

func (s *Service) applyLocked(ctx context.Context, fx *Effects, pay *paymentRow, lines []ApplyLine, on time.Time, actor, prefix string) error {
	if len(lines) == 0 {
		return invalidField(prefix, "name at least one invoice")
	}
	ids := make([]uuid.UUID, len(lines))
	seen := map[uuid.UUID]bool{}
	var cash int64
	for i, l := range lines {
		if seen[l.InvoiceID] {
			return invalidField(lineField(prefix, i, "invoice_id"), "an invoice appears once in a request")
		}
		seen[l.InvoiceID] = true
		ids[i] = l.InvoiceID
		switch {
		case l.AmountCents < 0:
			return invalidField(lineField(prefix, i, "amount_cents"), "must not be negative")
		case l.DiscountCents < 0:
			return invalidField(lineField(prefix, i, "discount_cents"), "must not be negative")
		case l.AmountCents == 0 && l.DiscountCents == 0:
			return invalidField(lineField(prefix, i, "amount_cents"), "must be greater than zero")
		case l.AmountCents == 0:
			return invalidField(lineField(prefix, i, "discount_cents"), "a discount is taken with a payment in the same request")
		}
		cash += l.AmountCents
	}
	if cash > pay.Unapplied {
		return conflict("exceeds_unapplied", fmt.Sprintf("the payment has %d cents unapplied: %d cents were asked", pay.Unapplied, cash))
	}
	sorted := append([]uuid.UUID(nil), ids...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].String() < sorted[b].String() })
	byID, err := s.lockInvoices(ctx, sorted, func(j int) string {
		for i := range ids {
			if ids[i] == sorted[j] {
				return lineField(prefix, i, "invoice_id")
			}
		}
		return prefix
	})
	if err != nil {
		return err
	}
	type plan struct {
		inv      *invoiceRow
		line     ApplyLine
		discount int64
	}
	plans := make([]plan, 0, len(lines))
	for _, l := range lines {
		inv := byID[l.InvoiceID]
		switch {
		case inv.Status == invVoid:
			return conflict("invoice_void", "the invoice is void: it takes no application")
		case inv.CustomerID != pay.CustomerID:
			return conflict("customer_mismatch", "the payment and the invoice belong to different customers")
		case inv.Currency != pay.Currency:
			return conflict("currency_mismatch", "the payment and the invoice are in different currencies")
		case l.AmountCents+l.DiscountCents > inv.Open:
			return conflict("exceeds_open_amount", fmt.Sprintf("invoice %s has %d cents open: %d cents were asked", inv.Number, inv.Open, l.AmountCents+l.DiscountCents))
		}
		if l.DiscountCents > 0 {
			if inv.DiscountDue == nil || inv.DiscountPercent == nil || on.After(*inv.DiscountDue) {
				return conflict("discount_not_available", "the early pay discount of invoice "+inv.Number+" is not available on this date")
			}
			var taken int64
			if err := s.ex(ctx).QueryRow(ctx, `SELECT COALESCE(SUM(ROUND(amount * 100)::bigint), 0) FROM ar_applications
				WHERE invoice_id = $1 AND kind = 'DISCOUNT' AND reversed_at IS NULL`, inv.ID).Scan(&taken); err != nil {
				return fmt.Errorf("failed to read the discount taken: %w", err)
			}
			if l.DiscountCents > maxDiscount(inv.Total, *inv.DiscountPercent)-taken {
				return conflict("discount_not_available", "the discount exceeds what invoice "+inv.Number+" allows")
			}
		}
		plans = append(plans, plan{inv: inv, line: l})
	}

	if _, err := s.lockCustomer(ctx, pay.CustomerID); err != nil {
		return err
	}
	act := uuid.New()
	var applied []uuid.UUID
	for _, p := range plans {
		inv, l := p.inv, p.line
		appID := uuid.New()
		entry, err := s.post(ctx, gl.PostingInput{EntryDate: on, Memo: fmt.Sprintf("Payment %s applied to %s", pay.Number, inv.Number),
			Source: gl.SourcePayment, SourceRefID: &appID, Currency: pay.Currency, PostedBy: actor, Legs: []gl.Leg{
				{AccountCode: gl.AccountCodeCustomerDeposit, Description: "Customer Deposits", Debit: l.AmountCents},
				{AccountCode: gl.AccountCodeAR, Description: "Accounts Receivable", Credit: l.AmountCents},
			}})
		if err != nil {
			return err
		}
		fx.entry(entry)
		payID := pay.ID
		if err := s.insertApplication(ctx, &appRow{ID: appID, CustomerID: pay.CustomerID, InvoiceID: inv.ID, ActID: act, Currency: pay.Currency,
			Kind: KindPayment, PaymentID: &payID, Amount: l.AmountCents}, on, actor, nil, entry); err != nil {
			return err
		}
		fx.ApplicationIDs = append(fx.ApplicationIDs, appID)
		if err := s.subledger(ctx, fx, pay.CustomerID, pay.Currency, TransactionTypePayment, -l.AmountCents, appID, sourceApplication,
			fmt.Sprintf("Payment %s applied to %s", pay.Number, inv.Number)); err != nil {
			return err
		}
		if l.DiscountCents > 0 {
			dID := uuid.New()
			dEntry, err := s.post(ctx, gl.PostingInput{EntryDate: on, Memo: fmt.Sprintf("Early pay discount on %s", inv.Number),
				Source: gl.SourcePayment, SourceRefID: &dID, Currency: pay.Currency, PostedBy: actor, Legs: []gl.Leg{
					{AccountCode: gl.AccountCodeSalesDiscounts, Description: "Sales Discounts", Debit: l.DiscountCents},
					{AccountCode: gl.AccountCodeAR, Description: "Accounts Receivable", Credit: l.DiscountCents},
				}})
			if err != nil {
				return err
			}
			fx.entry(dEntry)
			if err := s.insertApplication(ctx, &appRow{ID: dID, CustomerID: pay.CustomerID, InvoiceID: inv.ID, ActID: act, Currency: pay.Currency,
				Kind: KindDiscount, PaymentID: &payID, Amount: l.DiscountCents}, on, actor, nil, dEntry); err != nil {
				return err
			}
			fx.ApplicationIDs = append(fx.ApplicationIDs, dID)
			if err := s.subledger(ctx, fx, pay.CustomerID, pay.Currency, TransactionTypeDiscount, -l.DiscountCents, dID, sourceApplication,
				"Early pay discount on "+inv.Number); err != nil {
				return err
			}
		}
		if err := s.setInvoiceOpen(ctx, fx, inv, inv.Open-l.AmountCents-l.DiscountCents, false); err != nil {
			return err
		}
		applied = append(applied, inv.ID)
	}
	if err := s.setPaymentUnapplied(ctx, pay, pay.Unapplied-cash); err != nil {
		return err
	}
	fx.payment(pay, false, cash, applied)
	return nil
}

// ApplyCreditMemoIn applies an open credit memo to invoices.
type ApplyCreditMemoIn struct {
	MemoID uuid.UUID
	Lines  []ApplyLine // DiscountCents must be zero
	On     time.Time
	Actor  string
}

// ApplyCreditMemo uses a memo's open credit on invoices: no ledger entry (both
// sides are in 1020) and no subledger row.
func (s *Service) ApplyCreditMemo(ctx context.Context, in ApplyCreditMemoIn) (*Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return nil, err
	}
	m, err := s.lockMemo(ctx, in.MemoID)
	if err != nil {
		return nil, err
	}
	if m.Status != "OPEN" && m.Status != "PARTIAL" {
		return nil, httpx.InvalidStateTransition("only an open credit memo can be applied: it is " + lower(m.Status))
	}
	if len(in.Lines) == 0 {
		return nil, invalidField("applications", "name at least one invoice")
	}
	ids := make([]uuid.UUID, len(in.Lines))
	seen := map[uuid.UUID]bool{}
	var total int64
	for i, l := range in.Lines {
		if seen[l.InvoiceID] {
			return nil, invalidField(lineField("applications", i, "invoice_id"), "an invoice appears once in a request")
		}
		seen[l.InvoiceID] = true
		ids[i] = l.InvoiceID
		if l.AmountCents <= 0 {
			return nil, invalidField(lineField("applications", i, "amount_cents"), "must be greater than zero")
		}
		if l.DiscountCents != 0 {
			return nil, invalidField(lineField("applications", i, "discount_cents"), "a credit memo takes no early pay discount")
		}
		total += l.AmountCents
	}
	if total > -m.Open {
		return nil, conflict("exceeds_open_credit", fmt.Sprintf("the credit memo has %d cents of open credit: %d cents were asked", -m.Open, total))
	}
	sorted := append([]uuid.UUID(nil), ids...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].String() < sorted[b].String() })
	byID, err := s.lockInvoices(ctx, sorted, func(j int) string {
		for i := range ids {
			if ids[i] == sorted[j] {
				return lineField("applications", i, "invoice_id")
			}
		}
		return "applications"
	})
	if err != nil {
		return nil, err
	}
	for _, l := range in.Lines {
		inv := byID[l.InvoiceID]
		switch {
		case inv.Status == invVoid:
			return nil, conflict("invoice_void", "the invoice is void: it takes no application")
		case inv.CustomerID != m.CustomerID:
			return nil, conflict("customer_mismatch", "the credit memo and the invoice belong to different customers")
		case inv.Currency != m.Currency:
			return nil, conflict("currency_mismatch", "the credit memo and the invoice are in different currencies")
		case l.AmountCents > inv.Open:
			return nil, conflict("exceeds_open_amount", fmt.Sprintf("invoice %s has %d cents open: %d cents were asked", inv.Number, inv.Open, l.AmountCents))
		}
	}
	fx := &Effects{}
	act := uuid.New()
	memoID := m.ID
	for _, l := range in.Lines {
		inv := byID[l.InvoiceID]
		appID := uuid.New()
		if err := s.insertApplication(ctx, &appRow{ID: appID, CustomerID: m.CustomerID, InvoiceID: inv.ID, ActID: act, Currency: m.Currency,
			Kind: KindCreditMemo, CreditMemoID: &memoID, Amount: l.AmountCents}, in.On, in.Actor, nil, nil); err != nil {
			return nil, err
		}
		fx.ApplicationIDs = append(fx.ApplicationIDs, appID)
		if err := s.setInvoiceOpen(ctx, fx, inv, inv.Open-l.AmountCents, false); err != nil {
			return nil, err
		}
	}
	if err := s.setMemoOpen(ctx, fx, m, m.Open+total, false); err != nil {
		return nil, err
	}
	return fx, nil
}

// WriteOffIn writes an amount of an open invoice off as bad debt.
type WriteOffIn struct {
	InvoiceID   uuid.UUID
	AmountCents int64
	Reason      string
	Actor       string
	On          time.Time
}

// WriteOff posts DR 5040 / CR 1020 and the WRITE_OFF subledger row, closes the
// invoice as written off when it takes the open amount.
func (s *Service) WriteOff(ctx context.Context, in WriteOffIn) (*Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return nil, err
	}
	if in.AmountCents <= 0 {
		return nil, invalidField("amount_cents", "must be greater than zero")
	}
	byID, err := s.lockInvoices(ctx, []uuid.UUID{in.InvoiceID}, nil)
	if err != nil {
		return nil, err
	}
	inv := byID[in.InvoiceID]
	if inv.Status != invUnpaid && inv.Status != invPartial {
		return nil, httpx.InvalidStateTransition("only an unpaid or partly paid invoice can be written off: it is " + lower(inv.Status))
	}
	if in.AmountCents > inv.Open {
		return nil, conflict("exceeds_open_amount", fmt.Sprintf("invoice %s has %d cents open: %d cents were asked", inv.Number, inv.Open, in.AmountCents))
	}
	fx := &Effects{}
	if _, err := s.lockCustomer(ctx, inv.CustomerID); err != nil {
		return nil, err
	}
	appID := uuid.New()
	entry, err := s.post(ctx, gl.PostingInput{EntryDate: in.On, Memo: "Write off " + inv.Number, Source: gl.SourceWriteOff,
		SourceRefID: &appID, Currency: inv.Currency, PostedBy: in.Actor, Legs: []gl.Leg{
			{AccountCode: gl.AccountCodeBadDebt, Description: "Bad Debt Expense", Debit: in.AmountCents},
			{AccountCode: gl.AccountCodeAR, Description: "Accounts Receivable", Credit: in.AmountCents},
		}})
	if err != nil {
		return nil, err
	}
	fx.entry(entry)
	reason := in.Reason
	if err := s.insertApplication(ctx, &appRow{ID: appID, CustomerID: inv.CustomerID, InvoiceID: inv.ID, ActID: uuid.New(), Currency: inv.Currency,
		Kind: KindWriteOff, Amount: in.AmountCents}, in.On, in.Actor, &reason, entry); err != nil {
		return nil, err
	}
	fx.ApplicationIDs = append(fx.ApplicationIDs, appID)
	if err := s.subledger(ctx, fx, inv.CustomerID, inv.Currency, TransactionTypeWriteOff, -in.AmountCents, appID, sourceApplication, "Write off "+inv.Number); err != nil {
		return nil, err
	}
	if err := s.setInvoiceOpen(ctx, fx, inv, inv.Open-in.AmountCents, false); err != nil {
		return nil, err
	}
	return fx, nil
}

// -----------------------------------------------------------------------------
// Reversal and void.
// -----------------------------------------------------------------------------

// ReverseIn reverses one application of any kind.
type ReverseIn struct {
	ApplicationID uuid.UUID
	Reason        string
	Actor         string
	On            time.Time
}

// Reverse reverses one live application. Reversing a PAYMENT application also
// reverses the DISCOUNT applications of the same act on the same invoice in the
// same transaction. Locks: the owning payment or credit memo, then the invoice,
// then the application rows, then the customer.
func (s *Service) Reverse(ctx context.Context, in ReverseIn) (*Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return nil, err
	}
	var probe appRow
	if err := scanApp(s.ex(ctx).QueryRow(ctx, `SELECT `+appCols+` FROM ar_applications a WHERE a.id = $1`, in.ApplicationID), &probe); err != nil {
		return nil, httpx.NotFound("application not found")
	}
	if probe.Kind == KindDiscount {
		return nil, conflict("discount_stands_with_payment", "a discount stands beside the payment that earned it: reverse that payment's application")
	}
	r := &reversal{invoices: map[uuid.UUID]*invoiceRow{}, invoiceAdd: map[uuid.UUID]int64{}}
	switch {
	case probe.PaymentID != nil:
		p, err := s.lockPayment(ctx, *probe.PaymentID)
		if err != nil {
			return nil, err
		}
		r.payment = p
	case probe.CreditMemoID != nil:
		m, err := s.lockMemo(ctx, *probe.CreditMemoID)
		if err != nil {
			return nil, err
		}
		r.memo = m
	}
	byID, err := s.lockInvoices(ctx, []uuid.UUID{probe.InvoiceID}, nil)
	if err != nil {
		return nil, err
	}
	r.invoices = byID
	rows, err := s.ex(ctx).Query(ctx, `SELECT `+appCols+` FROM ar_applications a
		WHERE a.reversed_at IS NULL AND (a.id = $1 OR ($2 = 'PAYMENT' AND a.act_id = $3 AND a.invoice_id = $4 AND a.kind = 'DISCOUNT' AND a.payment_id = $5))
		ORDER BY a.id FOR UPDATE`, in.ApplicationID, string(probe.Kind), probe.ActID, probe.InvoiceID, probe.PaymentID)
	if err != nil {
		return nil, fmt.Errorf("failed to lock the applications: %w", err)
	}
	var apps []*appRow
	for rows.Next() {
		a := &appRow{}
		if err := scanApp(rows, a); err != nil {
			rows.Close()
			return nil, err
		}
		apps = append(apps, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	live := false
	for _, a := range apps {
		if a.ID == in.ApplicationID {
			live = true
		}
	}
	if !live {
		return nil, httpx.InvalidStateTransition("the application is already reversed")
	}
	fx := &Effects{}
	if err := s.reverseApps(ctx, fx, r, apps, in.Reason, in.Actor, in.On); err != nil {
		return nil, err
	}
	if err := s.flushReversal(ctx, fx, r, false); err != nil {
		return nil, err
	}
	return fx, nil
}

// reversal accumulates what a set of reversals does to the documents so each
// document is written once per act.
type reversal struct {
	invoices   map[uuid.UUID]*invoiceRow
	invoiceAdd map[uuid.UUID]int64
	payment    *paymentRow
	paymentAdd int64
	memo       *memoRow
	memoAdd    int64
	unapplied  []paymentFact
}

// reverseApps reverses the applications in id order: each its own reversal
// entry (or, for an application that posted none, a new entry that undoes its
// effect), the opposite subledger row, and the application marked reversed.
func (s *Service) reverseApps(ctx context.Context, fx *Effects, r *reversal, apps []*appRow, reason, actor string, on time.Time) error {
	if len(apps) == 0 {
		return nil
	}
	if _, err := s.lockCustomer(ctx, apps[0].CustomerID); err != nil {
		return err
	}
	// The applications are locked in id order (section 11, step 5) but undone in
	// the order they were made, so the subledger reads the same history backwards
	// whichever ids they drew.
	apps = append([]*appRow(nil), apps...)
	sort.SliceStable(apps, func(x, y int) bool {
		if !apps[x].CreatedAt.Equal(apps[y].CreatedAt) {
			return apps[x].CreatedAt.Before(apps[y].CreatedAt)
		}
		return apps[x].ID.String() < apps[y].ID.String()
	})
	for _, a := range apps {
		inv := r.invoices[a.InvoiceID]
		var rev *uuid.UUID
		var err error
		switch a.Kind {
		case KindPayment, KindDiscount, KindWriteOff:
			if a.GLEntryID != nil {
				rev, err = s.reverseEntry(ctx, gl.ReversalInput{EntryID: *a.GLEntryID, EntryDate: on, Currency: a.Currency, Reason: reason, PostedBy: actor})
			} else {
				contra, source := gl.AccountCodeCustomerDeposit, gl.SourcePayment
				switch a.Kind {
				case KindDiscount:
					contra = gl.AccountCodeSalesDiscounts
				case KindWriteOff:
					contra, source = gl.AccountCodeBadDebt, gl.SourceWriteOff
				}
				id := a.ID
				rev, err = s.post(ctx, gl.PostingInput{EntryDate: on, Memo: "Reversal of an application to " + inv.Number, Source: source,
					SourceRefID: &id, Currency: a.Currency, PostedBy: actor, Legs: []gl.Leg{
						{AccountCode: gl.AccountCodeAR, Description: "Accounts Receivable", Debit: a.Amount},
						{AccountCode: contra, Description: "Reversal", Credit: a.Amount},
					}})
			}
			if err != nil {
				return err
			}
			fx.entry(rev)
			if err := s.subledger(ctx, fx, a.CustomerID, a.Currency, TransactionTypeReversal, a.Amount, a.ID, sourceApplication,
				"Reversal of an application to "+inv.Number); err != nil {
				return err
			}
		}
		if _, err := s.ex(ctx).Exec(ctx, `
			UPDATE ar_applications SET reversed_at = NOW(), reversed_by = NULLIF($2, ''), reversal_reason = $3, reversal_gl_entry_id = $4,
				reversed_on = $5::date WHERE id = $1`, a.ID, actor, reason, rev, date(on)); err != nil {
			return fmt.Errorf("failed to reverse the application: %w", err)
		}
		fx.ReversedIDs = append(fx.ReversedIDs, a.ID)
		r.invoiceAdd[a.InvoiceID] += a.Amount
		switch a.Kind {
		case KindPayment:
			r.paymentAdd += a.Amount
			p := *r.payment
			p.Unapplied += a.Amount
			r.unapplied = append(r.unapplied, paymentFact{p: p, unapplied: true, amount: a.Amount, invoices: []uuid.UUID{a.InvoiceID}})
		case KindCreditMemo:
			r.memoAdd -= a.Amount
		}
	}
	return nil
}

// flushReversal writes each document once. A payment being voided is written by
// its own act, so skipPayment leaves it.
func (s *Service) flushReversal(ctx context.Context, fx *Effects, r *reversal, skipPayment bool) error {
	if r.payment != nil && r.paymentAdd != 0 && !skipPayment {
		if err := s.setPaymentUnapplied(ctx, r.payment, r.payment.Unapplied+r.paymentAdd); err != nil {
			return err
		}
	}
	for _, f := range r.unapplied {
		f.p.Revision = 0 // the payment's revision is the post-act one, set below
		if r.payment != nil {
			f.p.Revision = r.payment.Revision
			f.p.Unapplied = r.payment.Unapplied
		}
		fx.payments = append(fx.payments, f)
	}
	if r.memo != nil && r.memoAdd != 0 {
		if err := s.setMemoOpen(ctx, fx, r.memo, r.memo.Open+r.memoAdd, true); err != nil {
			return err
		}
	}
	ids := make([]uuid.UUID, 0, len(r.invoiceAdd))
	for id := range r.invoiceAdd {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a].String() < ids[b].String() })
	for _, id := range ids {
		inv := r.invoices[id]
		if err := s.setInvoiceOpen(ctx, fx, inv, inv.Open+r.invoiceAdd[id], true); err != nil {
			return err
		}
	}
	return nil
}

// VoidPaymentIn voids a posted payment.
type VoidPaymentIn struct {
	PaymentID uuid.UUID
	Reason    string
	Actor     string
	On        time.Time
}

// VoidPayment reverses every live application (with its discounts), then posts
// the void entry (DR 2200 / CR 1010) for the amount still unapplied, which is
// the amount less refunds, and ends the payment as VOIDED. A card payment is
// refused (409 card_payment): it is refunded through the gateway.
func (s *Service) VoidPayment(ctx context.Context, in VoidPaymentIn) (*Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return nil, err
	}
	pay, err := s.lockPayment(ctx, in.PaymentID)
	if err != nil {
		return nil, err
	}
	if pay.Status != "POSTED" {
		return nil, httpx.InvalidStateTransition("the payment is already voided")
	}
	if pay.Method == "CARD" {
		return nil, conflict("card_payment", "a card payment is not voided here: refund it through the gateway")
	}
	rows, err := s.ex(ctx).Query(ctx, `SELECT `+appCols+` FROM ar_applications a WHERE a.payment_id = $1 AND a.reversed_at IS NULL ORDER BY a.invoice_id, a.id`, pay.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the payment's applications: %w", err)
	}
	var probes []*appRow
	var invIDs []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for rows.Next() {
		a := &appRow{}
		if err := scanApp(rows, a); err != nil {
			rows.Close()
			return nil, err
		}
		probes = append(probes, a)
		if !seen[a.InvoiceID] {
			seen[a.InvoiceID] = true
			invIDs = append(invIDs, a.InvoiceID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(invIDs, func(a, b int) bool { return invIDs[a].String() < invIDs[b].String() })
	r := &reversal{payment: pay, invoiceAdd: map[uuid.UUID]int64{}}
	if r.invoices, err = s.lockInvoices(ctx, invIDs, nil); err != nil {
		return nil, err
	}
	// The application rows, in id order, under their lock.
	locked, err := s.ex(ctx).Query(ctx, `SELECT `+appCols+` FROM ar_applications a WHERE a.payment_id = $1 AND a.reversed_at IS NULL ORDER BY a.id FOR UPDATE`, pay.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to lock the applications: %w", err)
	}
	var apps []*appRow
	for locked.Next() {
		a := &appRow{}
		if err := scanApp(locked, a); err != nil {
			locked.Close()
			return nil, err
		}
		apps = append(apps, a)
	}
	locked.Close()
	if err := locked.Err(); err != nil {
		return nil, err
	}
	fx := &Effects{}
	if _, err := s.lockCustomer(ctx, pay.CustomerID); err != nil {
		return nil, err
	}
	if err := s.reverseApps(ctx, fx, r, apps, in.Reason, in.Actor, in.On); err != nil {
		return nil, err
	}
	remaining := pay.Unapplied + r.paymentAdd
	payID := pay.ID
	entry, err := s.post(ctx, gl.PostingInput{EntryDate: in.On, Memo: "Void of payment " + pay.Number, Source: gl.SourcePayment,
		SourceRefID: &payID, Currency: pay.Currency, PostedBy: in.Actor, Legs: []gl.Leg{
			{AccountCode: gl.AccountCodeCustomerDeposit, Description: "Customer Deposits", Debit: remaining},
			{AccountCode: gl.AccountCodeCash, Description: "Cash", Credit: remaining},
		}})
	if err != nil {
		return nil, err
	}
	fx.entry(entry)
	if _, err := s.ex(ctx).Exec(ctx, `
		UPDATE payments SET status = 'VOIDED', amount_unapplied = 0, voided_at = NOW(), voided_by = NULLIF($2, ''), void_reason = $3,
			voided_on = $4::date, updated_at = NOW(), revision = revision + 1 WHERE id = $1`, pay.ID, in.Actor, in.Reason, date(in.On)); err != nil {
		return nil, fmt.Errorf("failed to void the payment: %w", err)
	}
	pay.Revision++
	pay.Status, pay.Unapplied = "VOIDED", 0
	if err := s.flushReversal(ctx, fx, r, true); err != nil {
		return nil, err
	}
	return fx, nil
}

// RefundPaymentIn refunds unapplied cash.
type RefundPaymentIn struct {
	PaymentID       uuid.UUID
	AmountCents     int64
	Reason          string
	GatewayRefundID string
	Actor           string
	On              time.Time
}

// RefundPayment pays unapplied cash back out: DR 2200 / CR 1010 and a refund
// row. A card refund went through the gateway before the transaction.
func (s *Service) RefundPayment(ctx context.Context, in RefundPaymentIn) (uuid.UUID, *Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return uuid.Nil, nil, err
	}
	if in.AmountCents <= 0 {
		return uuid.Nil, nil, invalidField("amount_cents", "must be greater than zero")
	}
	pay, err := s.lockPayment(ctx, in.PaymentID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	if pay.Status != "POSTED" {
		return uuid.Nil, nil, conflict("payment_voided", "the payment is voided: it refunds nothing")
	}
	if in.AmountCents > pay.Unapplied {
		return uuid.Nil, nil, conflict("exceeds_unapplied", fmt.Sprintf("the payment has %d cents unapplied: %d cents were asked", pay.Unapplied, in.AmountCents))
	}
	fx := &Effects{}
	refundID := uuid.New()
	entry, err := s.post(ctx, gl.PostingInput{EntryDate: in.On, Memo: "Refund of payment " + pay.Number, Source: gl.SourcePayment,
		SourceRefID: &refundID, Currency: pay.Currency, PostedBy: in.Actor, Legs: []gl.Leg{
			{AccountCode: gl.AccountCodeCustomerDeposit, Description: "Customer Deposits", Debit: in.AmountCents},
			{AccountCode: gl.AccountCodeCash, Description: "Cash", Credit: in.AmountCents},
		}})
	if err != nil {
		return uuid.Nil, nil, err
	}
	fx.entry(entry)
	if _, err := s.ex(ctx).Exec(ctx, `
		INSERT INTO payment_refunds (id, payment_id, amount, reason, gateway_refund_id, status, method, gl_entry_id, refunded_on, refunded_by)
		VALUES ($1, $2, $3::bigint::numeric / 100, $4, NULLIF($5, ''), 'COMPLETE', $6, $7, $8::date, NULLIF($9, ''))`,
		refundID, pay.ID, in.AmountCents, in.Reason, in.GatewayRefundID, pay.Method, entry, date(in.On), in.Actor); err != nil {
		return uuid.Nil, nil, fmt.Errorf("failed to record the refund: %w", err)
	}
	if err := s.setPaymentUnapplied(ctx, pay, pay.Unapplied-in.AmountCents); err != nil {
		return uuid.Nil, nil, err
	}
	return refundID, fx, nil
}

// RefundCreditMemoIn pays a credit memo's open credit out.
type RefundCreditMemoIn struct {
	MemoID          uuid.UUID
	AmountCents     int64
	Reason          string
	Method          string
	GatewayRefundID string
	Actor           string
	On              time.Time
}

// RefundCreditMemo pays credit out: DR 1020 / CR 1010, a REFUND subledger row
// (the credit leaves the receivable) and a refund row; the memo's open credit
// shrinks and its status follows.
func (s *Service) RefundCreditMemo(ctx context.Context, in RefundCreditMemoIn) (uuid.UUID, *Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return uuid.Nil, nil, err
	}
	if in.AmountCents <= 0 {
		return uuid.Nil, nil, invalidField("amount_cents", "must be greater than zero")
	}
	m, err := s.lockMemo(ctx, in.MemoID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	if m.Status != "OPEN" && m.Status != "PARTIAL" {
		return uuid.Nil, nil, httpx.InvalidStateTransition("only an open credit memo can be refunded: it is " + lower(m.Status))
	}
	if in.AmountCents > -m.Open {
		return uuid.Nil, nil, conflict("exceeds_open_credit", fmt.Sprintf("the credit memo has %d cents of open credit: %d cents were asked", -m.Open, in.AmountCents))
	}
	fx := &Effects{}
	if _, err := s.lockCustomer(ctx, m.CustomerID); err != nil {
		return uuid.Nil, nil, err
	}
	refundID := uuid.New()
	entry, err := s.post(ctx, gl.PostingInput{EntryDate: in.On, Memo: "Refund of credit memo " + m.Number, Source: gl.SourceCreditMemo,
		SourceRefID: &refundID, Currency: m.Currency, PostedBy: in.Actor, Legs: []gl.Leg{
			{AccountCode: gl.AccountCodeAR, Description: "Accounts Receivable", Debit: in.AmountCents},
			{AccountCode: gl.AccountCodeCash, Description: "Cash", Credit: in.AmountCents},
		}})
	if err != nil {
		return uuid.Nil, nil, err
	}
	fx.entry(entry)
	if _, err := s.ex(ctx).Exec(ctx, `
		INSERT INTO payment_refunds (id, credit_memo_id, amount, reason, gateway_refund_id, status, method, gl_entry_id, refunded_on, refunded_by)
		VALUES ($1, $2, $3::bigint::numeric / 100, $4, NULLIF($5, ''), 'COMPLETE', $6, $7, $8::date, NULLIF($9, ''))`,
		refundID, m.ID, in.AmountCents, in.Reason, in.GatewayRefundID, in.Method, entry, date(in.On), in.Actor); err != nil {
		return uuid.Nil, nil, fmt.Errorf("failed to record the refund: %w", err)
	}
	if err := s.subledger(ctx, fx, m.CustomerID, m.Currency, TransactionTypeRefund, in.AmountCents, refundID, sourceRefund, "Refund of credit memo "+m.Number); err != nil {
		return uuid.Nil, nil, err
	}
	if err := s.setMemoOpen(ctx, fx, m, m.Open+in.AmountCents, false); err != nil {
		return uuid.Nil, nil, err
	}
	return refundID, fx, nil
}

// ApplyDeposits applies the named payments (an order's deposits, already
// locked by the caller in id order, given here oldest first) to one invoice, up
// to its open amount (ADR 0005 5.6 step 7): each payment gives min(its
// unapplied cash, what the invoice still owes), as its own application and
// entry. A payment with nothing unapplied, or an invoice with nothing open, is
// skipped. It runs inside the fulfilment's transaction.
func (s *Service) ApplyDeposits(ctx context.Context, invoiceID uuid.UUID, paymentIDs []uuid.UUID, on time.Time, actor string) (*Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return nil, err
	}
	fx := &Effects{}
	for _, id := range paymentIDs {
		rows, err := s.lockInvoices(ctx, []uuid.UUID{invoiceID}, nil)
		if err != nil {
			return nil, err
		}
		inv := rows[invoiceID]
		if inv.Open <= 0 {
			break
		}
		pay, err := s.lockPayment(ctx, id)
		if err != nil {
			return nil, err
		}
		if pay.Status != "POSTED" || pay.Unapplied <= 0 || pay.CustomerID != inv.CustomerID || pay.Currency != inv.Currency {
			continue
		}
		take := pay.Unapplied
		if inv.Open < take {
			take = inv.Open
		}
		one, err := s.Apply(ctx, ApplyIn{PaymentID: id, Lines: []ApplyLine{{InvoiceID: invoiceID, AmountCents: take}}, On: on, Actor: actor})
		if err != nil {
			return nil, err
		}
		fx.Merge(one)
	}
	return fx, nil
}
