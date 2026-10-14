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
	"strings"
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
	db                *database.DB
	gl                *gl.Service
	logger            *slog.Logger
	now               func() time.Time
	auditLog          *audit.Logger
	events            EventRecorder
	tx                TxRunner
	applyGuard        InvoiceApplyGuard
	tenderCreditGuard TenderCreditGuard
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

// refuseDayToCome refuses a money day after the business day of the branch
// the act belongs to, in the branch's time zone (the aging's own rule for today):
// cash is never received, applied or refunded on a day that has not come.
func (s *Service) refuseDayToCome(ctx context.Context, branchID uuid.UUID, on time.Time, field string) error {
	if on.IsZero() {
		return nil
	}
	today, err := s.LocalDate(ctx, branchID, s.now())
	if err != nil {
		return err
	}
	if date(on) > date(today) {
		return invalidField(field, fmt.Sprintf("%s has not come yet: money is dated no later than today, %s", date(on), date(today)))
	}
	return nil
}

// refuseDayToComeOf is refuseDayToCome for an act that names a document, not
// a branch: the day is the document's own branch's. A document that is not
// there is left to the act's own refusal.
func (s *Service) refuseDayToComeOf(ctx context.Context, table string, id uuid.UUID, on time.Time, field string) error {
	var branch uuid.UUID
	err := s.ex(ctx).QueryRow(ctx, `SELECT branch_id FROM `+table+` WHERE id = $1`, id).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read the document's branch: %w", err)
	}
	return s.refuseDayToCome(ctx, branch, on, field)
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
		UPDATE credit_memos SET amount_open = $2::bigint::numeric / 100, status = $3::text, applied_at = CASE WHEN $3::text = 'APPLIED' THEN NOW() ELSE NULL END,
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

// refuseHeldInvoice refuses an application to an invoice another module
// holds mid act: the counter wires the guard (C2-5, the fourteenth review's
// P2-1) so that while a sale's void has standing refund claims, the sale's
// invoice takes no application: its open cents are the void's interim, and a
// live application would strand the void's completion. The refusal names the
// standing claims and comes before the amount checks, so the operator is
// told the invoice is held whatever they asked for. Without a guard nothing
// changes.
func (s *Service) refuseHeldInvoice(ctx context.Context, invoiceID uuid.UUID) error {
	if s.applyGuard == nil {
		return nil
	}
	claims, err := s.applyGuard.PendingVoidClaimsOfInvoice(ctx, invoiceID)
	if err != nil {
		return err
	}
	if len(claims) == 0 {
		return nil
	}
	ids := make([]string, 0, len(claims))
	for _, id := range claims {
		ids = append(ids, id.String())
	}
	return conflict("void_claim_pending",
		fmt.Sprintf("this invoice's sale has a void whose refund claims still stand (claims %s): settle each at POST /api/v1/pos/refund-intents/{id}/resolve before applying anything to the invoice",
			strings.Join(ids, ", ")))
}

// refuseHeldTenderCredit refuses an application of a payment's unapplied
// cash another module holds to one of its own tenders (C2-5,
// FROM-PRINCIPAL 60): a completed void leaves a declined card tender's
// charge as credit on the tender's payment, and that credit is payable
// back only to its card, never spent on another invoice. The refusal names
// the credit's origin, the void and the claim it released, and comes
// before anything of the application is written. Without a guard nothing
// changes.
func (s *Service) refuseHeldTenderCredit(ctx context.Context, paymentID uuid.UUID) error {
	if s.tenderCreditGuard == nil {
		return nil
	}
	held, ok, err := s.tenderCreditGuard.HeldTenderCredit(ctx, paymentID)
	if err != nil || !ok {
		return err
	}
	return conflict("tender_credit_held",
		fmt.Sprintf("the payment's unapplied cash is the credit of the card tender the void of sale %s holds (claim %s): a declined void tender's credit is held to its own tender and applies to no invoice, payable back only to its card",
			held.SaleNumber, held.IntentID))
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

// RestampHistory puts a trading day on the rows an invoice's posting and its
// payment's act stamped with the present. Only the demo seed calls it: its
// history posts each invoice and the payment that settles it through the acts
// above, so the ledger, the subledger and the application are this core's own,
// then moves their stamps to the history's day (the revenue trend, the
// statements and the ledger's created dates would otherwise collapse onto the
// day of the seed) and links the payment to the invoice it paid, as the order
// and invoice walks follow it. It writes stamps and that link in the caller's
// transaction, never an amount, a status or a balance. It is not an act: no
// route or worker calls it. It refuses a call with no transaction, a zero day,
// an invoice that is not paid, a payment with no live application to that
// invoice, and a payment day before the invoice's or after the present. It
// moves only the named payment's live applications, never another payment's.
func (s *Service) RestampHistory(ctx context.Context, invoiceID, paymentID uuid.UUID, invoicedAt, paidAt time.Time) error {
	if err := s.requireTx(ctx); err != nil {
		return err
	}
	if invoicedAt.IsZero() || paidAt.IsZero() {
		return invalidField("paid_at", "a history day is given for the invoice and the payment")
	}
	if paidAt.Before(invoicedAt) {
		return invalidField("paid_at", "a history payment is not made before its invoice")
	}
	if paidAt.After(s.now()) {
		return invalidField("paid_at", "a history day is not in the future")
	}
	var settled bool
	if err := s.ex(ctx).QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM invoices WHERE id = $1 AND status = 'PAID')
			AND EXISTS (SELECT 1 FROM ar_applications
				WHERE invoice_id = $1 AND payment_id = $2 AND reversed_at IS NULL)`,
		invoiceID, paymentID).Scan(&settled); err != nil {
		return fmt.Errorf("the history's invoice was not read: %w", err)
	}
	if !settled {
		return conflict("not_settled", "only a paid invoice and a payment applied to it take a history day")
	}
	return s.restampRows(ctx, invoiceID, paymentID, invoicedAt, paidAt)
}

// RestampDay is RestampHistory for the base seed, whose invoices are unpaid or
// part paid as often as paid: it puts the documents' days on the rows an
// invoice's posting and, when a payment is named, that payment's act stamped
// with the present. The same writes and the same limits (a transaction, a
// payment day not before the invoice's or after the present, a payment with a
// live application to the invoice), without the settled invoice guard.
// uuid.Nil names no payment.
func (s *Service) RestampDay(ctx context.Context, invoiceID, paymentID uuid.UUID, invoicedAt, paidAt time.Time) error {
	if err := s.requireTx(ctx); err != nil {
		return err
	}
	if invoicedAt.IsZero() {
		return invalidField("invoiced_at", "a day is given for the invoice")
	}
	if paymentID == uuid.Nil {
		paidAt = invoicedAt
	}
	if paidAt.Before(invoicedAt) {
		return invalidField("paid_at", "a payment is not made before its invoice")
	}
	if paidAt.After(s.now()) || invoicedAt.After(s.now()) {
		return invalidField("paid_at", "a day is not in the future")
	}
	if paymentID != uuid.Nil {
		var applied bool
		if err := s.ex(ctx).QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM ar_applications WHERE invoice_id = $1 AND payment_id = $2 AND reversed_at IS NULL)`,
			invoiceID, paymentID).Scan(&applied); err != nil {
			return fmt.Errorf("the invoice's payment was not read: %w", err)
		}
		if !applied {
			return conflict("not_settled", "only a payment applied to the invoice takes its day")
		}
	}
	return s.restampRows(ctx, invoiceID, paymentID, invoicedAt, paidAt)
}

// restampRows writes the stamps and the payment's link in the caller's
// transaction.
func (s *Service) restampRows(ctx context.Context, invoiceID, paymentID uuid.UUID, invoicedAt, paidAt time.Time) error {
	// One statement: each update is a CTE over the rows the acts just wrote, so
	// a history invoice costs one more round trip, not seven. Each is scoped to
	// what the payment settled, so the guard above and the writes agree.
	_, err := s.ex(ctx).Exec(ctx, `
		WITH apps AS (
			UPDATE ar_applications SET created_at = $2
			WHERE invoice_id = $1 AND payment_id = $4 AND reversed_at IS NULL RETURNING id
		), ct_inv AS (
			UPDATE customer_transactions SET created_at = $3 WHERE source_kind = 'invoice' AND reference_id = $1
		), ct_pay AS (
			UPDATE customer_transactions SET created_at = $2 WHERE source_kind = 'application'
				AND reference_id IN (SELECT id FROM apps)
		), pay AS (
			UPDATE payments SET invoice_id = $1, created_at = $2, updated_at = $2 WHERE id = $4
		), inv AS (
			UPDATE invoices SET paid_at = $2, updated_at = $2 WHERE id = $1 AND status = 'PAID'
		), gl_inv AS (
			UPDATE gl_journal_entries SET created_at = $3, updated_at = $3 WHERE source_ref_id = $1
		)
		UPDATE gl_journal_entries SET created_at = $2, updated_at = $2
		WHERE source_ref_id = $4 OR source_ref_id IN (SELECT id FROM apps)`,
		invoiceID, paidAt, invoicedAt, paymentID)
	if err != nil {
		return fmt.Errorf("the history's day was not stamped: %w", err)
	}
	return nil
}
