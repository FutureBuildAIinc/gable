// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package account is the AR core (ADR 0005 section 9.3): the single writer of
// the subledger (customer_transactions), customers.balance_due,
// ar_applications, invoices.amount_open and invoices.status after create,
// credit_memos.amount_open and credit_memos.status after draft,
// payments.amount_unapplied and payments.status, and the journal entries of
// the AR movements of section 8.2. Its acts run only inside the caller's
// transaction and take plain values, so the package imports gl and the
// platform and nothing of invoice, payment or order. A test in this package
// (gate_test.go) fails when any other Go file writes those tables or columns.
//
// Locks are taken in section 11's order: a payment or a credit memo, then the
// invoices in id order, then the applications, then the customer row (the
// balance_due lock), then the journal inserts. The caller writes the events
// last, from Effects.
package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Service is the AR core and the reads of the account module.
type Service struct {
	db       *database.DB
	gl       *gl.Service
	logger   *slog.Logger
	now      func() time.Time
	auditLog *audit.Logger
	events   EventRecorder
}

// NewService builds the AR core. glSvc may be nil in a unit test that posts no
// entry; a posting then writes the subledger and the documents only.
func NewService(db *database.DB, glSvc *gl.Service, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: db, gl: glSvc, logger: logger, now: time.Now}
}

// conflict is a 409 with a blocker, the shape the refusals of section 9 take.
func conflict(code, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict, Message: message,
		Details: []httpx.FieldError{httpx.Blocker(code, message)}}
}

func invalidField(field, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed, Message: "the request is not valid",
		Details: []httpx.FieldError{{Field: field, Message: message}}}
}

// ErrNoTransaction is the refusal to run an act with no transaction open.
var ErrNoTransaction = gl.ErrNoTransaction

func (s *Service) requireTx(ctx context.Context) error {
	if !database.InTx(ctx) {
		return ErrNoTransaction
	}
	return nil
}

func (s *Service) ex(ctx context.Context) database.Executor { return s.db.GetExecutor(ctx) }

// post writes one entry through the ledger when one is wired, mapping a closed
// period to its 409 blocker. A nil entry means every leg was zero.
func (s *Service) post(ctx context.Context, in gl.PostingInput) (*uuid.UUID, error) {
	if s.gl == nil {
		return nil, nil
	}
	e, err := s.gl.PostEntry(ctx, in)
	if err != nil {
		return nil, mapPosting(err)
	}
	if e == nil {
		return nil, nil
	}
	id := e.ID
	return &id, nil
}

func (s *Service) reverseEntry(ctx context.Context, in gl.ReversalInput) (*uuid.UUID, error) {
	if s.gl == nil {
		return nil, nil
	}
	e, err := s.gl.PostReversal(ctx, in)
	if err != nil {
		return nil, mapPosting(err)
	}
	if e == nil {
		return nil, nil
	}
	id := e.ID
	return &id, nil
}

func mapPosting(err error) error {
	if errors.Is(err, gl.ErrPeriodClosed) {
		return conflict("period_closed", "the entry would be dated into a closed fiscal period")
	}
	return err
}

// lockCustomer takes the customer's row FOR UPDATE (section 11, step 7) and
// answers the balance in cents.
func (s *Service) lockCustomer(ctx context.Context, id uuid.UUID) (int64, error) {
	var bal int64
	err := s.ex(ctx).QueryRow(ctx, `SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1 FOR UPDATE`, id).Scan(&bal)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, invalidField("customer_id", "no such customer")
	}
	if err != nil {
		return 0, fmt.Errorf("failed to lock the customer: %w", err)
	}
	return bal, nil
}

// subledger writes one row of customer_transactions and moves balance_due by
// the same amount (debit positive). It is the only place either is written.
func (s *Service) subledger(ctx context.Context, fx *Effects, customerID uuid.UUID, currency string, typ TransactionType,
	amount int64, ref uuid.UUID, sourceKind, description string) error {
	if amount == 0 {
		return nil
	}
	bal, err := s.lockCustomer(ctx, customerID)
	if err != nil {
		return err
	}
	next := bal + amount
	if _, err := s.ex(ctx).Exec(ctx, `
		INSERT INTO customer_transactions (id, customer_id, type, amount, balance_after, reference_id, description, currency, source_kind, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, clock_timestamp())`,
		uuid.New(), customerID, string(typ), amount, next, ref, description, currency, sourceKind); err != nil {
		return fmt.Errorf("failed to write the subledger row: %w", err)
	}
	if _, err := s.ex(ctx).Exec(ctx, `UPDATE customers SET balance_due = $1::bigint::numeric / 100, updated_at = NOW() WHERE id = $2`, next, customerID); err != nil {
		return fmt.Errorf("failed to update the customer balance: %w", err)
	}
	fx.balance(customerID, currency, next)
	return nil
}

// -----------------------------------------------------------------------------
// The rows the acts lock.
// -----------------------------------------------------------------------------

type invoiceRow struct {
	ID, CustomerID, BranchID uuid.UUID
	Number, Currency, Status string
	Total, Open              int64
	Revision                 int64
	DiscountDue              *time.Time
	DiscountPercent          *int64 // ten thousandths of a percent
	InvoiceDate              time.Time
}

const invoiceCols = `i.id, i.customer_id, i.branch_id, i.number, i.currency, i.status, ROUND(i.total_amount * 100)::bigint,
	ROUND(i.amount_open * 100)::bigint, i.revision, i.discount_due_date, ROUND(i.discount_percent * 10000)::bigint, i.invoice_date`

func scanInvoice(row pgx.Row, r *invoiceRow) error {
	return row.Scan(&r.ID, &r.CustomerID, &r.BranchID, &r.Number, &r.Currency, &r.Status, &r.Total, &r.Open, &r.Revision,
		&r.DiscountDue, &r.DiscountPercent, &r.InvoiceDate)
}

// lockInvoices takes the named invoices FOR UPDATE in id order (section 11,
// step 4) and answers them by id. field names the request field for a missing
// one ("applications[2].invoice_id").
func (s *Service) lockInvoices(ctx context.Context, ids []uuid.UUID, field func(int) string) (map[uuid.UUID]*invoiceRow, error) {
	rows, err := s.ex(ctx).Query(ctx, `SELECT `+invoiceCols+` FROM invoices i WHERE i.id = ANY($1) ORDER BY i.id FOR UPDATE`, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to lock the invoices: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]*invoiceRow{}
	for rows.Next() {
		r := &invoiceRow{}
		if err := scanInvoice(rows, r); err != nil {
			return nil, fmt.Errorf("failed to read an invoice: %w", err)
		}
		out[r.ID] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, id := range ids {
		if _, ok := out[id]; !ok {
			name := "invoice_id"
			if field != nil {
				name = field(i)
			}
			return nil, invalidField(name, "no such invoice")
		}
	}
	return out, nil
}

type paymentRow struct {
	ID, CustomerID, BranchID uuid.UUID
	Number, Currency         string
	Status, Method           string
	Amount, Unapplied        int64
	Revision                 int64
	OrderID, ProjectID       *uuid.UUID
}

const paymentCols = `p.id, p.customer_id, p.branch_id, p.number, p.currency, p.status, p.method, ROUND(p.amount * 100)::bigint,
	ROUND(p.amount_unapplied * 100)::bigint, p.revision, p.order_id, p.project_id`

func scanPayment(row pgx.Row, r *paymentRow) error {
	return row.Scan(&r.ID, &r.CustomerID, &r.BranchID, &r.Number, &r.Currency, &r.Status, &r.Method, &r.Amount, &r.Unapplied,
		&r.Revision, &r.OrderID, &r.ProjectID)
}

// lockPayment takes the payment FOR UPDATE (section 11, step 2).
func (s *Service) lockPayment(ctx context.Context, id uuid.UUID) (*paymentRow, error) {
	r := &paymentRow{}
	err := scanPayment(s.ex(ctx).QueryRow(ctx, `SELECT `+paymentCols+` FROM payments p WHERE p.id = $1 FOR UPDATE`, id), r)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("payment not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock the payment: %w", err)
	}
	return r, nil
}

type memoRow struct {
	ID, CustomerID, BranchID uuid.UUID
	Number                   string
	Currency, Status         string
	Total, Open              int64 // both <= 0
	Revision                 int64
	GLEntryID                *uuid.UUID
}

const memoCols = `m.id, m.customer_id, m.branch_id, COALESCE(m.number, ''), m.currency, m.status, ROUND(m.total_amount * 100)::bigint,
	ROUND(m.amount_open * 100)::bigint, m.revision, m.gl_entry_id`

func scanMemo(row pgx.Row, r *memoRow) error {
	return row.Scan(&r.ID, &r.CustomerID, &r.BranchID, &r.Number, &r.Currency, &r.Status, &r.Total, &r.Open, &r.Revision, &r.GLEntryID)
}

// lockMemo takes the credit memo FOR UPDATE (section 11, step 3).
func (s *Service) lockMemo(ctx context.Context, id uuid.UUID) (*memoRow, error) {
	r := &memoRow{}
	err := scanMemo(s.ex(ctx).QueryRow(ctx, `SELECT `+memoCols+` FROM credit_memos m WHERE m.id = $1 FOR UPDATE`, id), r)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("credit memo not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock the credit memo: %w", err)
	}
	return r, nil
}

type appRow struct {
	ID, CustomerID, InvoiceID, ActID uuid.UUID
	Currency                         string
	Kind                             ApplicationKind
	PaymentID, CreditMemoID          *uuid.UUID
	Amount                           int64
	GLEntryID                        *uuid.UUID
	Reversed                         bool
	AppliedOn                        time.Time
	CreatedAt                        time.Time
}

const appCols = `a.id, a.customer_id, a.invoice_id, a.act_id, a.currency, a.kind, a.payment_id, a.credit_memo_id,
	ROUND(a.amount * 100)::bigint, a.gl_entry_id, a.reversed_at IS NOT NULL, a.applied_on, a.created_at`

func scanApp(row pgx.Row, a *appRow) error {
	var kind string
	if err := row.Scan(&a.ID, &a.CustomerID, &a.InvoiceID, &a.ActID, &a.Currency, &kind, &a.PaymentID, &a.CreditMemoID,
		&a.Amount, &a.GLEntryID, &a.Reversed, &a.AppliedOn, &a.CreatedAt); err != nil {
		return err
	}
	a.Kind = ApplicationKind(kind)
	return nil
}

// -----------------------------------------------------------------------------
// Statuses.
// -----------------------------------------------------------------------------

const (
	invUnpaid     = "UNPAID"
	invPartial    = "PARTIAL"
	invPaid       = "PAID"
	invWrittenOff = "WRITTEN_OFF"
	invVoid       = "VOID"
)

// invoiceStatus derives the status from the open amount (section 6.2): the
// total open is unpaid, nothing open is paid, or written off when a write off
// stands among the live applications, and anything between is partial.
func (s *Service) invoiceStatus(ctx context.Context, id uuid.UUID, total, open int64) (string, error) {
	switch {
	case open >= total && total > 0:
		return invUnpaid, nil
	case open <= 0:
		var n int
		if err := s.ex(ctx).QueryRow(ctx, `SELECT count(*) FROM ar_applications WHERE invoice_id = $1 AND kind = 'WRITE_OFF' AND reversed_at IS NULL`, id).Scan(&n); err != nil {
			return "", fmt.Errorf("failed to read the invoice's write offs: %w", err)
		}
		if n > 0 {
			return invWrittenOff, nil
		}
		return invPaid, nil
	default:
		return invPartial, nil
	}
}

func memoStatus(total, open int64) string {
	switch {
	case open == 0:
		return "APPLIED"
	case open == total:
		return "OPEN"
	default:
		return "PARTIAL"
	}
}

// setInvoiceOpen writes a new open amount and the status it implies and records
// the change. kind picks the event the change raises.
func (s *Service) setInvoiceOpen(ctx context.Context, fx *Effects, inv *invoiceRow, newOpen int64, reopen bool) error {
	status, err := s.invoiceStatus(ctx, inv.ID, inv.Total, newOpen)
	if err != nil {
		return err
	}
	if _, err := s.ex(ctx).Exec(ctx, `
		UPDATE invoices SET amount_open = $2::bigint::numeric / 100, status = $3,
			paid_at = CASE WHEN $3 = 'PAID' THEN COALESCE(paid_at, NOW()) ELSE NULL END,
			updated_at = NOW(), revision = revision + 1
		WHERE id = $1`, inv.ID, newOpen, status); err != nil {
		return fmt.Errorf("failed to update the invoice: %w", err)
	}
	fx.invoice(inv, status, newOpen, reopen)
	inv.Open, inv.Status = newOpen, status
	inv.Revision++
	return nil
}

func (s *Service) setMemoOpen(ctx context.Context, fx *Effects, m *memoRow, newOpen int64, reopen bool) error {
	status := memoStatus(m.Total, newOpen)
	if _, err := s.ex(ctx).Exec(ctx, `
		UPDATE credit_memos SET amount_open = $2::bigint::numeric / 100, status = $3, applied_at = CASE WHEN $3 = 'APPLIED' THEN NOW() ELSE NULL END,
			updated_at = NOW(), revision = revision + 1
		WHERE id = $1`, m.ID, newOpen, status); err != nil {
		return fmt.Errorf("failed to update the credit memo: %w", err)
	}
	fx.memo(m, status, newOpen, reopen)
	m.Open, m.Status = newOpen, status
	m.Revision++
	return nil
}

func (s *Service) setPaymentUnapplied(ctx context.Context, p *paymentRow, newUnapplied int64) error {
	if _, err := s.ex(ctx).Exec(ctx, `UPDATE payments SET amount_unapplied = $2::bigint::numeric / 100, updated_at = NOW(), revision = revision + 1 WHERE id = $1`,
		p.ID, newUnapplied); err != nil {
		return fmt.Errorf("failed to update the payment: %w", err)
	}
	p.Unapplied = newUnapplied
	p.Revision++
	return nil
}

// insertApplication writes one application row.
func (s *Service) insertApplication(ctx context.Context, a *appRow, on time.Time, actor string, reason *string, glEntry *uuid.UUID) error {
	_, err := s.ex(ctx).Exec(ctx, `
		INSERT INTO ar_applications (id, customer_id, currency, kind, payment_id, credit_memo_id, invoice_id, amount, reason,
			applied_on, applied_by, act_id, gl_entry_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::bigint::numeric / 100, $9, $10::date, NULLIF($11, ''), $12, $13, clock_timestamp())`,
		a.ID, a.CustomerID, a.Currency, string(a.Kind), a.PaymentID, a.CreditMemoID, a.InvoiceID, a.Amount, reason,
		date(on), actor, a.ActID, glEntry)
	if err != nil {
		return fmt.Errorf("failed to write the application: %w", err)
	}
	return nil
}
