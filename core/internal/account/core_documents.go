// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// PostInvoiceIn is an invoice already inserted (status UNPAID or PAID, amount
// open at the total) whose posting the AR core makes.
type PostInvoiceIn struct {
	InvoiceID, CustomerID uuid.UUID
	Number, Currency      string
	TotalCents            int64
	On                    time.Time // the invoice date, the branch's business date
	Actor                 string
	// Legs are every leg of the entry except the receivable's debit, which the
	// core adds from TotalCents so the subledger and the ledger cannot differ.
	Legs []gl.Leg
}

// PostInvoice posts the invoice: the balanced entry (DR 1020 for the total, the
// caller's legs for revenue, tax and cost), the subledger debit, and
// invoices.gl_entry_id. A zero total writes neither the debit nor the row.
func (s *Service) PostInvoice(ctx context.Context, in PostInvoiceIn) (*Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return nil, err
	}
	fx := &Effects{}
	if in.Number == "" || in.Currency == "" {
		// The counter's account charge (until C2-5) leaves the number and the
		// currency to the invoice's column defaults: read them.
		if err := s.ex(ctx).QueryRow(ctx, `SELECT number, currency FROM invoices WHERE id = $1`, in.InvoiceID).Scan(&in.Number, &in.Currency); err != nil {
			return nil, httpx.NotFound("invoice not found")
		}
	}
	legs := append([]gl.Leg{{AccountCode: gl.AccountCodeAR, Description: "Accounts Receivable", Debit: in.TotalCents}}, in.Legs...)
	if _, err := s.lockCustomer(ctx, in.CustomerID); err != nil {
		return nil, err
	}
	id := in.InvoiceID
	entry, err := s.post(ctx, gl.PostingInput{EntryDate: in.On, Memo: "Invoice " + in.Number, Source: gl.SourceInvoice,
		SourceRefID: &id, Currency: in.Currency, PostedBy: in.Actor, Legs: legs})
	if err != nil {
		return nil, err
	}
	if entry != nil {
		fx.entry(entry)
		if _, err := s.ex(ctx).Exec(ctx, `UPDATE invoices SET gl_entry_id = $2 WHERE id = $1`, in.InvoiceID, *entry); err != nil {
			return nil, fmt.Errorf("failed to record the invoice entry: %w", err)
		}
	}
	if err := s.subledger(ctx, fx, in.CustomerID, in.Currency, TransactionTypeInvoice, in.TotalCents, in.InvoiceID, sourceInvoice, "Invoice "+in.Number); err != nil {
		return nil, err
	}
	return fx, nil
}

// VoidInvoiceIn names the invoice to void and the entry to reverse.
type VoidInvoiceIn struct {
	InvoiceID uuid.UUID
	// EntryID is the invoice's posted entry, nil for an invoice that has none
	// (a zero total).
	EntryID *uuid.UUID
	Actor   string
	Reason  string
	On      time.Time // the void date, the branch's business date
}

// ErrHasApplications is the refusal to void a document that has been used.
var ErrHasApplications = conflict("has_applications", "the invoice has payments or applied credit memos: reverse them before voiding it")

// VoidInvoice reverses the invoice's whole entry (dated the void date), writes
// the opposite subledger row and ends the invoice as VOID with nothing open. An
// invoice with a live application is refused with 409 has_applications.
func (s *Service) VoidInvoice(ctx context.Context, in VoidInvoiceIn) (*Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return nil, err
	}
	rows, err := s.lockInvoices(ctx, []uuid.UUID{in.InvoiceID}, nil)
	if err != nil {
		return nil, err
	}
	inv := rows[in.InvoiceID]
	if inv.Status == invVoid {
		return nil, httpx.InvalidStateTransition("the invoice is already void")
	}
	var live int
	if err := s.ex(ctx).QueryRow(ctx, `SELECT count(*) FROM ar_applications WHERE invoice_id = $1 AND reversed_at IS NULL`, inv.ID).Scan(&live); err != nil {
		return nil, fmt.Errorf("failed to read the invoice's applications: %w", err)
	}
	if live > 0 || inv.Open != inv.Total {
		return nil, ErrHasApplications
	}
	fx := &Effects{}
	if _, err := s.lockCustomer(ctx, inv.CustomerID); err != nil {
		return nil, err
	}
	if in.EntryID != nil {
		rev, err := s.reverseEntry(ctx, gl.ReversalInput{EntryID: *in.EntryID, EntryDate: in.On, Currency: inv.Currency,
			Reason: "invoice " + inv.Number + " voided", PostedBy: in.Actor})
		if err != nil {
			return nil, err
		}
		fx.entry(rev)
	}
	if err := s.subledger(ctx, fx, inv.CustomerID, inv.Currency, TransactionTypeReversal, -inv.Total, inv.ID, sourceInvoice, "Void of invoice "+inv.Number); err != nil {
		return nil, err
	}
	if _, err := s.ex(ctx).Exec(ctx, `
		UPDATE invoices SET status = 'VOID', amount_open = 0, voided_at = NOW(), voided_by = NULLIF($2, ''), void_reason = $3,
			voided_on = $4::date, updated_at = NOW(), revision = revision + 1
		WHERE id = $1`, inv.ID, in.Actor, in.Reason, date(in.On)); err != nil {
		return nil, fmt.Errorf("failed to void the invoice: %w", err)
	}
	return fx, nil
}

// PostCreditMemoIn posts a draft credit memo. Totals are signed as the
// document keeps them: negative.
type PostCreditMemoIn struct {
	MemoID, CustomerID uuid.UUID
	Number, Currency   string
	MemoDate           time.Time
	SubtotalCents      int64
	TaxCents           int64
	TotalCents         int64
	TaxRate            *string
	Actor              string
	// Legs are every leg of the entry except the receivable's credit, which
	// the core adds from the total.
	Legs []gl.Leg
}

// PostCreditMemo posts the memo: the entry (CR 1020 for the credit, the
// caller's legs for revenue, tax and restocked cost), the negative CREDIT_MEMO
// subledger row, and the document goes from DRAFT to OPEN with the whole credit
// open.
func (s *Service) PostCreditMemo(ctx context.Context, in PostCreditMemoIn) (*Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return nil, err
	}
	if in.TotalCents >= 0 {
		return nil, conflict("empty_credit_memo", "the credit memo credits nothing: add a priced line")
	}
	m, err := s.lockMemo(ctx, in.MemoID)
	if err != nil {
		return nil, err
	}
	if m.Status != "DRAFT" {
		return nil, httpx.InvalidStateTransition("only a draft credit memo can be posted: it is " + lower(m.Status))
	}
	fx := &Effects{}
	if _, err := s.lockCustomer(ctx, in.CustomerID); err != nil {
		return nil, err
	}
	legs := append([]gl.Leg{{AccountCode: gl.AccountCodeAR, Description: "Accounts Receivable", Credit: -in.TotalCents}}, in.Legs...)
	id := in.MemoID
	entry, err := s.post(ctx, gl.PostingInput{EntryDate: in.MemoDate, Memo: "Credit memo " + in.Number, Source: gl.SourceCreditMemo,
		SourceRefID: &id, Currency: in.Currency, PostedBy: in.Actor, Legs: legs})
	if err != nil {
		return nil, err
	}
	fx.entry(entry)
	if err := s.subledger(ctx, fx, in.CustomerID, in.Currency, TransactionTypeCreditMemo, in.TotalCents, in.MemoID, sourceCreditMemo, "Credit memo "+in.Number); err != nil {
		return nil, err
	}
	ct, err := s.ex(ctx).Exec(ctx, `
		UPDATE credit_memos SET number = $2, status = 'OPEN', gl_entry_id = $3, memo_date = $4::date,
			amount = -($5::bigint::numeric / 100), subtotal = $6::bigint::numeric / 100, tax_amount = $7::bigint::numeric / 100,
			total_amount = $5::bigint::numeric / 100, amount_open = $5::bigint::numeric / 100, tax_rate = $8::numeric,
			updated_at = NOW(), revision = revision + 1
		WHERE id = $1`,
		in.MemoID, in.Number, entry, date(in.MemoDate), in.TotalCents, in.SubtotalCents, in.TaxCents, in.TaxRate)
	if err != nil {
		return nil, fmt.Errorf("failed to post the credit memo: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return nil, httpx.NotFound("credit memo not found")
	}
	return fx, nil
}

// VoidCreditMemoIn names the memo to void. EntryID is not needed: the memo's
// own entry is read under its lock.
type VoidCreditMemoIn struct {
	MemoID uuid.UUID
	Actor  string
	Reason string
	On     time.Time
}

// VoidCreditMemo voids a draft (no money moves, no number was drawn) or an open
// memo no application or refund has used: it reverses the memo's entry, writes
// the opposite subledger row and ends the memo as VOID with nothing open.
func (s *Service) VoidCreditMemo(ctx context.Context, in VoidCreditMemoIn) (*Effects, error) {
	if err := s.requireTx(ctx); err != nil {
		return nil, err
	}
	m, err := s.lockMemo(ctx, in.MemoID)
	if err != nil {
		return nil, err
	}
	fx := &Effects{}
	switch m.Status {
	case "VOID":
		return nil, httpx.InvalidStateTransition("the credit memo is already void")
	case "PARTIAL", "APPLIED":
		return nil, conflict("has_applications", "the credit memo has been used: reverse its applications and refunds before voiding it")
	case "OPEN":
		if _, err := s.lockCustomer(ctx, m.CustomerID); err != nil {
			return nil, err
		}
		if m.GLEntryID != nil {
			rev, err := s.reverseEntry(ctx, gl.ReversalInput{EntryID: *m.GLEntryID, EntryDate: in.On, Currency: m.Currency,
				Reason: "credit memo " + m.Number + " voided", PostedBy: in.Actor})
			if err != nil {
				return nil, err
			}
			fx.entry(rev)
		}
		if err := s.subledger(ctx, fx, m.CustomerID, m.Currency, TransactionTypeReversal, -m.Total, m.ID, sourceCreditMemo, "Void of credit memo "+m.Number); err != nil {
			return nil, err
		}
	}
	if _, err := s.ex(ctx).Exec(ctx, `
		UPDATE credit_memos SET status = 'VOID', amount_open = 0, voided_at = NOW(), voided_by = NULLIF($2, ''), void_reason = $3,
			voided_on = $4::date, updated_at = NOW(), revision = revision + 1
		WHERE id = $1`, m.ID, in.Actor, in.Reason, date(in.On)); err != nil {
		return nil, fmt.Errorf("failed to void the credit memo: %w", err)
	}
	return fx, nil
}

// PostLegacyReturnCredit writes the subledger row of a counter return refunded
// as store credit: a negative REFUND row against the return. The counter's
// return becomes a credit memo in C2-5, which retires this; until then the row
// is written here so no code outside the core writes the subledger.
func (s *Service) PostLegacyReturnCredit(ctx context.Context, customerID, returnID uuid.UUID, amountCents int64) error {
	if err := s.requireTx(ctx); err != nil {
		return err
	}
	var currency string
	if err := s.ex(ctx).QueryRow(ctx, `SELECT COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD')
		FROM customers c WHERE c.id = $1`, customerID).Scan(&currency); err != nil {
		return invalidField("customer_id", "no such customer")
	}
	return s.subledger(ctx, &Effects{}, customerID, currency, TransactionTypeRefund, -amountCents, returnID, "pos_return", "POS return credit #"+returnID.String())
}
