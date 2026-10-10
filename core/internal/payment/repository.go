// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var errPaymentNotFound = httpx.NotFound("payment not found")

// Repository reads payments. Every statement goes through the executor the
// context resolves, so inside a transaction it is the transaction. The writes
// are the AR core's (internal/account): payments.amount_unapplied and status
// have one writer.
type Repository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *Repository { return &Repository{db: db} }

// wall is the branch predicate on an aliased table's branch_id.
func wall(alias string, branch, grants int) string {
	return fmt.Sprintf(`(
	    ($%[2]d::uuid IS NOT NULL AND %[1]s.branch_id = $%[2]d)
	    OR ($%[2]d::uuid IS NULL AND $%[3]d::text IS NOT NULL AND %[1]s.branch_id IN
	        (SELECT branch_id FROM user_locations WHERE user_sub = $%[3]d))
	    OR ($%[2]d::uuid IS NULL AND $%[3]d::text IS NULL)
	  )`, alias, branch, grants)
}

const summaryColumns = `
	p.id, p.number, p.customer_id, COALESCE(c.name, ''), p.branch_id, p.status, p.revision, p.currency, p.method,
	ROUND(p.amount * 100)::bigint, ROUND(p.amount_unapplied * 100)::bigint, to_char(p.received_on, 'YYYY-MM-DD'),
	p.reference, p.notes, p.order_id, p.project_id, p.gl_entry_id, p.card_last4, p.card_brand, p.gateway_tx_id, p.auth_code,
	p.voided_at, p.voided_by, p.void_reason, p.created_at, p.updated_at`

const summaryFrom = `
	FROM payments p
	LEFT JOIN customers c ON c.id = p.customer_id`

func scanSummary(row pgx.Row, s *Summary) error {
	var (
		status, method    string
		amount, unapplied int64
		voided            *time.Time
		created, updated  time.Time
	)
	if err := row.Scan(&s.ID, &s.Number, &s.CustomerID, &s.CustomerName, &s.BranchID, &status, &s.Revision, &s.Currency, &method,
		&amount, &unapplied, &s.ReceivedOn, &s.Reference, &s.Notes, &s.OrderID, &s.JobID, &s.GLEntryID, &s.CardLast4, &s.CardBrand,
		&s.GatewayTxID, &s.AuthCode, &voided, &s.VoidedBy, &s.VoidReason, &created, &updated); err != nil {
		return err
	}
	s.Status, s.Method = Status(status), PaymentMethod(method)
	s.AmountCents, s.UnappliedCents = httpx.Cents(amount), httpx.Cents(unapplied)
	s.VoidedAt = httpx.PtrTimestamp(voided)
	s.CreatedAt, s.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

// ListFilter is the list's filters and keyset position.
type ListFilter struct {
	CustomerID *uuid.UUID
	Statuses   []Status
	Unapplied  *bool
	OrderID    *uuid.UUID
	JobID      *uuid.UUID
	Methods    []PaymentMethod
	AfterTime  *time.Time
	AfterID    uuid.UUID
	Limit      int
}

var listWhere = "\n\tWHERE " + wall("p", 1, 2) + `
	  AND ($3::uuid IS NULL OR p.customer_id = $3)
	  AND (cardinality($4::text[]) = 0 OR p.status = ANY($4))
	  AND ($5::boolean IS NULL OR (p.amount_unapplied > 0 AND p.status = 'POSTED') = $5)
	  AND ($6::uuid IS NULL OR p.order_id = $6)
	  AND ($7::uuid IS NULL OR p.project_id = $7)
	  AND (cardinality($8::text[]) = 0 OR p.method = ANY($8))`

func (r *Repository) listArgs(ctx context.Context, f ListFilter) []any {
	statuses := make([]string, len(f.Statuses))
	for i, s := range f.Statuses {
		statuses[i] = string(s)
	}
	methods := make([]string, len(f.Methods))
	for i, m := range f.Methods {
		methods[i] = string(m)
	}
	return []any{middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), f.CustomerID, statuses, f.Unapplied, f.OrderID, f.JobID, methods}
}

// List answers one page, newest first by (created_at, id).
func (r *Repository) List(ctx context.Context, f ListFilter) ([]Summary, error) {
	args := append(r.listArgs(ctx, f), f.AfterTime, f.AfterID, f.Limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+summaryColumns+summaryFrom+listWhere+`
		  AND ($9::timestamptz IS NULL OR (p.created_at, p.id) < ($9, $10::uuid))
		ORDER BY p.created_at DESC, p.id DESC
		LIMIT $11`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list payments: %w", err)
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var s Summary
		if err := scanSummary(rows, &s); err != nil {
			return nil, fmt.Errorf("failed to scan payment: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Count is the size of the filtered set, for ?include=total.
func (r *Repository) Count(ctx context.Context, f ListFilter) (int64, error) {
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT count(*) FROM payments p`+listWhere, r.listArgs(ctx, f)...).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count payments: %w", err)
	}
	return n, nil
}

// Get reads one payment with its applications and refunds, held to the branch
// wall.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*Payment, error) {
	var p Payment
	row := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+summaryColumns+summaryFrom+`
		WHERE p.id = $1 AND `+wall("p", 2, 3), id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err := scanSummary(row, &p.Summary); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errPaymentNotFound
		}
		return nil, fmt.Errorf("failed to get payment: %w", err)
	}
	refunds, err := r.Refunds(ctx, id)
	if err != nil {
		return nil, err
	}
	p.Refunds = refunds
	return &p, nil
}

// Refunds lists a payment's refunds, oldest first.
func (r *Repository) Refunds(ctx context.Context, paymentID uuid.UUID) ([]Refund, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT f.id, f.payment_id, f.credit_memo_id, ROUND(f.amount * 100)::bigint, f.reason, f.method, f.gateway_refund_id, f.status,
		       f.gl_entry_id, to_char(f.refunded_on, 'YYYY-MM-DD'), f.created_at
		FROM payment_refunds f WHERE f.payment_id = $1 ORDER BY f.created_at, f.id`, paymentID)
	if err != nil {
		return nil, fmt.Errorf("failed to list refunds: %w", err)
	}
	defer rows.Close()
	out := []Refund{}
	for rows.Next() {
		var f Refund
		var amount int64
		var method string
		var created time.Time
		if err := rows.Scan(&f.ID, &f.PaymentID, &f.CreditMemoID, &amount, &f.Reason, &method, &f.GatewayRefundID, &f.Status, &f.GLEntryID,
			&f.RefundedOn, &created); err != nil {
			return nil, fmt.Errorf("failed to scan refund: %w", err)
		}
		f.AmountCents, f.Method, f.CreatedAt = httpx.Cents(amount), PaymentMethod(method), httpx.TimestampOf(created)
		out = append(out, f)
	}
	return out, rows.Err()
}

// CustomerFacts are what a payment takes from its customer: the currency
// (the customer's effective one, never sent) and the primary branch.
type CustomerFacts struct {
	Currency string
	Branch   uuid.UUID
}

// CustomerFacts reads them; a customer that does not exist is a 400 naming
// customer_id.
func (r *Repository) CustomerFacts(ctx context.Context, id uuid.UUID) (*CustomerFacts, error) {
	var f CustomerFacts
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD'), c.primary_branch_id
		FROM customers c WHERE c.id = $1`, id).Scan(&f.Currency, &f.Branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed, Message: "a referenced record does not exist",
			Details: []httpx.FieldError{{Field: "customer_id", Message: "no such customer"}}}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the customer: %w", err)
	}
	return &f, nil
}

// Visible answers which of the invoices the caller's branch wall lets it see:
// an application naming one it cannot see is refused as a missing record.
func (r *Repository) InvoiceVisible(ctx context.Context, id uuid.UUID) (bool, error) {
	var n int
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT count(*) FROM invoices i WHERE i.id = $3 AND `+wall("i", 1, 2),
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), id).Scan(&n)
	return n > 0, err
}

// OrderBelongsTo reports whether the order exists for the customer.
func (r *Repository) OrderBelongsTo(ctx context.Context, orderID, customerID uuid.UUID) (exists, matches bool, err error) {
	var owner uuid.UUID
	e := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT customer_id FROM orders WHERE id = $1`, orderID).Scan(&owner)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, false, nil
	}
	if e != nil {
		return false, false, fmt.Errorf("failed to read the order: %w", e)
	}
	return true, owner == customerID, nil
}

// JobBelongsTo reports whether the job exists for the customer.
func (r *Repository) JobBelongsTo(ctx context.Context, jobID, customerID uuid.UUID) (exists, matches bool, err error) {
	var owner uuid.UUID
	e := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT customer_id FROM projects WHERE id = $1`, jobID).Scan(&owner)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, false, nil
	}
	if e != nil {
		return false, false, fmt.Errorf("failed to read the job: %w", e)
	}
	return true, owner == customerID, nil
}

// GatewayFacts are what a refund of a card payment needs.
type GatewayFacts struct {
	Method      PaymentMethod
	GatewayTxID string
	AmountCents int64
	Status      Status
	Unapplied   int64
	BranchID    uuid.UUID
	Currency    string
}

// GatewayFactsFor reads them, held to the branch wall.
func (r *Repository) GatewayFactsFor(ctx context.Context, id uuid.UUID) (*GatewayFacts, error) {
	var f GatewayFacts
	var method, status string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT p.method, COALESCE(p.gateway_tx_id, ''), ROUND(p.amount * 100)::bigint, p.status, ROUND(p.amount_unapplied * 100)::bigint,
		       p.branch_id, p.currency
		FROM payments p WHERE p.id = $1 AND `+wall("p", 2, 3), id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).
		Scan(&method, &f.GatewayTxID, &f.AmountCents, &status, &f.Unapplied, &f.BranchID, &f.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errPaymentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the payment: %w", err)
	}
	f.Method, f.Status = PaymentMethod(method), Status(status)
	return &f, nil
}

// InvoiceFacts are the facts a card charge checks before it touches the card.
type InvoiceFacts struct {
	Number   string
	Status   string
	Open     int64
	Customer uuid.UUID
	Currency string
}

// InvoiceFactsFor reads an invoice unlocked, for a pre-charge check.
func (r *Repository) InvoiceFactsFor(ctx context.Context, id uuid.UUID) (*InvoiceFacts, error) {
	var f InvoiceFacts
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT i.number, i.status, ROUND(i.amount_open * 100)::bigint, i.customer_id, i.currency
		FROM invoices i WHERE i.id = $1 AND `+wall("i", 2, 3), id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).
		Scan(&f.Number, &f.Status, &f.Open, &f.Customer, &f.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the invoice: %w", err)
	}
	return &f, nil
}

// MemoFacts are the facts a refund of credit reads about a credit memo.
type MemoFacts struct {
	ID       uuid.UUID
	Number   string
	Customer uuid.UUID
	Branch   uuid.UUID
	Currency string
	Status   string
	Open     int64 // negative
	Total    int64 // negative
	Revision int64
}

// MemoFactsFor reads a credit memo held to the branch wall.
func (r *Repository) MemoFactsFor(ctx context.Context, id uuid.UUID) (*MemoFacts, error) {
	var f MemoFacts
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT m.id, COALESCE(m.number, ''), m.customer_id, m.branch_id, m.currency, m.status, ROUND(m.amount_open * 100)::bigint,
		       ROUND(m.total_amount * 100)::bigint, m.revision
		FROM credit_memos m WHERE m.id = $1 AND `+wall("m", 2, 3), id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).
		Scan(&f.ID, &f.Number, &f.Customer, &f.Branch, &f.Currency, &f.Status, &f.Open, &f.Total, &f.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("credit memo not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the credit memo: %w", err)
	}
	return &f, nil
}

// LockMemo takes the credit memo row FOR UPDATE, held to the branch wall, and
// answers its revision.
func (r *Repository) LockMemo(ctx context.Context, id uuid.UUID) (int64, error) {
	var rev int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT m.revision FROM credit_memos m WHERE m.id = $1 AND `+wall("m", 2, 3)+` FOR UPDATE`,
		id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).Scan(&rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, httpx.NotFound("credit memo not found")
	}
	if err != nil {
		return 0, fmt.Errorf("failed to lock the credit memo: %w", err)
	}
	return rev, nil
}
