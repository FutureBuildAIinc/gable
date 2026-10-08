// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Repository is the slice of the store the older callers use: the payment
// module reads and updates an invoice's status around a payment, the counter
// creates an account charge, and the order credit check sums the open
// balance. The invoice acts of C2-3 (void, the credit memo lifecycle, the
// fulfilment invoice) use Store, which the Postgres repository also
// implements; unit tests that fake only this slice never reach them.
type Repository interface {
	CreateInvoice(ctx context.Context, inv *LegacyInvoice) error
	GetInvoice(ctx context.Context, id uuid.UUID) (*Invoice, error)
	UpdateInvoice(ctx context.Context, inv *Invoice) error
	ExistsInvoiceForOrder(ctx context.Context, orderID uuid.UUID) (bool, error)
	SumOpenBalanceCents(ctx context.Context, customerID uuid.UUID) (int64, error)
	GetBranchTaxRate(ctx context.Context, branchID *uuid.UUID) (float64, bool)
}

type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// referenceFields maps the foreign keys a write can violate to the request
// field that named the missing record, so a bad reference is a 400 naming it
// and not a 500 from the database.
var referenceFields = map[string]string{
	"credit_memos_customer_id_fkey":          "customer_id",
	"credit_memos_invoice_id_fkey":           "invoice_id",
	"credit_memos_project_id_fkey":           "job_id",
	"credit_memos_ship_to_id_fkey":           "ship_to_id",
	"credit_memos_branch_id_fkey":            "branch_id",
	"credit_memo_lines_product_id_fkey":      "lines.product_id",
	"credit_memo_lines_charge_code_id_fkey":  "lines.charge_code",
	"credit_memo_lines_invoice_line_id_fkey": "lines.invoice_line_id",
}

func validationFailed(msg string, details ...httpx.FieldError) *httpx.Error {
	return &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed, Message: msg, Details: details}
}

func mapWriteError(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		if field, ok := referenceFields[pgErr.ConstraintName]; ok {
			return validationFailed("a referenced record does not exist",
				httpx.FieldError{Field: field, Message: "no such record"})
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// ---------------------------------------------------------------------------
// The branch wall (ADR 0007 section 2.3): a context branch reads its own
// rows; with no context branch a bound non-admin user reads the branches
// granted to the user, none granted none; an administrator without a header,
// an unbound key, the single-branch switch and dev mode read every branch's.
// The wall applies to EVERY read and every lock, not only the list.
// ---------------------------------------------------------------------------

// wall is the predicate on an aliased table's branch_id; branch and grants
// are the placeholders it uses.
func wall(alias string, branch, grants int) string {
	return fmt.Sprintf(`(
	    ($%[2]d::uuid IS NOT NULL AND %[1]s.branch_id = $%[2]d)
	    OR ($%[2]d::uuid IS NULL AND $%[3]d::text IS NOT NULL AND %[1]s.branch_id IN
	        (SELECT branch_id FROM user_locations WHERE user_sub = $%[3]d))
	    OR ($%[2]d::uuid IS NULL AND $%[3]d::text IS NULL)
	  )`, alias, branch, grants)
}

// overdueExpr is the computed is_overdue flag: open and due before the
// branch's today.
const overdueExpr = `(i.status IN ('UNPAID', 'PARTIAL') AND i.due_date IS NOT NULL
	AND i.due_date < (NOW() AT TIME ZONE COALESCE((SELECT lc.timezone FROM locations lc WHERE lc.id = i.branch_id), 'UTC'))::date)`

// openExpr is the open amount in cents: the total less the payments recorded
// against the invoice while it is unpaid or partial.
const openExpr = `CASE WHEN i.status IN ('UNPAID', 'PARTIAL') THEN GREATEST(
		ROUND(i.total_amount * 100)::bigint
		- COALESCE((SELECT SUM(ROUND(p.amount * 100)::bigint) FROM payments p WHERE p.invoice_id = i.id), 0), 0)
	ELSE 0 END`

// summaryColumns is the one invoice header projection: a list item and the
// head of the full document both read it.
const summaryColumns = `
	i.id, i.number, i.branch_id, i.customer_id, COALESCE(c.name, ''), i.order_id, i.project_id, i.ship_to_id,
	i.status, i.revision, i.currency, i.origin, i.delivery_type, i.picked_up_by, i.delivery_id,
	to_char(i.invoice_date, 'YYYY-MM-DD'), to_char(i.due_date, 'YYYY-MM-DD'), i.payment_terms_id,
	to_char(i.discount_due_date, 'YYYY-MM-DD'), ROUND(i.discount_percent * 10000)::bigint,
	ROUND(COALESCE(i.subtotal, 0) * 100)::bigint, ROUND(COALESCE(i.tax_amount, 0) * 100)::bigint,
	i.tax_rate::text, i.tax_exempt, i.tax_source, ROUND(i.total_amount * 100)::bigint,
	` + openExpr + `, ` + overdueExpr + `,
	i.paid_at, i.gl_entry_id, i.voided_at, i.voided_by, i.void_reason, i.created_at, i.updated_at`

const summaryFrom = `
	FROM invoices i
	LEFT JOIN customers c ON c.id = i.customer_id`

func scanSummary(row pgx.Row, s *InvoiceSummary, extra ...any) error {
	var (
		status, origin, delivery, taxSource string
		discount                            *int64
		subtotal, tax, total, open          int64
		taxRate                             *string
		paid, voided                        *time.Time
		created, updated                    time.Time
	)
	dest := append([]any{
		&s.ID, &s.Number, &s.BranchID, &s.CustomerID, &s.CustomerName, &s.OrderID, &s.JobID, &s.ShipToID,
		&status, &s.Revision, &s.Currency, &origin, &delivery, &s.PickedUpBy, &s.DeliveryID,
		&s.InvoiceDate, &s.DueDate, &s.PaymentTermsID,
		&s.DiscountDueDate, &discount,
		&subtotal, &tax, &taxRate, &s.TaxExempt, &taxSource, &total,
		&open, &s.IsOverdue,
		&paid, &s.GLEntryID, &voided, &s.VoidedBy, &s.VoidReason, &created, &updated,
	}, extra...)
	if err := row.Scan(dest...); err != nil {
		return err
	}
	s.Status = InvoiceStatus(status)
	s.Origin, s.DeliveryType = Origin(origin), DeliveryType(delivery)
	s.TaxSource = salesdoc.TaxSource(taxSource)
	s.SubtotalCents, s.TaxCents, s.TotalCents, s.OpenCents = httpx.Cents(subtotal), httpx.Cents(tax), httpx.Cents(total), httpx.Cents(open)
	s.DiscountPercent = salesdoc.PtrQuantity(discount)
	if taxRate != nil {
		if pct, err := salesdoc.RateToPercent(*taxRate); err == nil {
			s.TaxRatePercent = &pct
		}
	}
	s.PaidAt, s.VoidedAt = httpx.PtrTimestamp(paid), httpx.PtrTimestamp(voided)
	s.CreatedAt, s.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

// ListFilter is the list's filters and keyset position.
type ListFilter struct {
	Statuses   []InvoiceStatus
	CustomerID *uuid.UUID
	JobID      *uuid.UUID
	ShipToID   *uuid.UUID
	OrderID    *uuid.UUID
	Overdue    *bool
	AfterTime  *time.Time
	AfterID    uuid.UUID
	Limit      int
}

func statusStrings(states []InvoiceStatus) []string {
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = string(s)
	}
	return out
}

// listWhere is the shared predicate of the list and its count: the branch
// wall, then the filters that filter. The count ignores the keyset position.
var listWhere = "\n\tWHERE " + wall("i", 1, 2) + `
	  AND (cardinality($3::text[]) = 0 OR i.status = ANY($3))
	  AND ($4::uuid IS NULL OR i.customer_id = $4)
	  AND ($5::uuid IS NULL OR i.project_id = $5)
	  AND ($6::uuid IS NULL OR i.ship_to_id = $6)
	  AND ($7::uuid IS NULL OR i.order_id = $7)
	  AND ($8::boolean IS NULL OR ` + overdueExpr + ` = $8)`

func (r *PostgresRepository) listArgs(ctx context.Context, f ListFilter) []any {
	return []any{middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), statusStrings(f.Statuses),
		f.CustomerID, f.JobID, f.ShipToID, f.OrderID, f.Overdue}
}

// ListInvoices answers one page, newest first by (created_at, id).
func (r *PostgresRepository) ListInvoices(ctx context.Context, f ListFilter) ([]InvoiceSummary, error) {
	args := append(r.listArgs(ctx, f), f.AfterTime, f.AfterID, f.Limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+summaryColumns+summaryFrom+listWhere+`
		  AND ($9::timestamptz IS NULL OR (i.created_at, i.id) < ($9, $10::uuid))
		ORDER BY i.created_at DESC, i.id DESC
		LIMIT $11`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list invoices: %w", err)
	}
	defer rows.Close()
	out := []InvoiceSummary{}
	for rows.Next() {
		var s InvoiceSummary
		if err := scanSummary(rows, &s); err != nil {
			return nil, fmt.Errorf("failed to scan invoice: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CountInvoices is the size of the filtered set, for ?include=total.
func (r *PostgresRepository) CountInvoices(ctx context.Context, f ListFilter) (int64, error) {
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT count(*) FROM invoices i`+listWhere, r.listArgs(ctx, f)...).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count invoices: %w", err)
	}
	return n, nil
}

var errInvoiceNotFound = httpx.NotFound("invoice not found")

// GetInvoice reads one invoice with its lines, held to the branch wall. A
// missing invoice, or one outside the caller's branches, is a 404.
func (r *PostgresRepository) GetInvoice(ctx context.Context, id uuid.UUID) (*Invoice, error) {
	inv, err := r.invoiceHead(ctx, id, false)
	if err != nil {
		return nil, err
	}
	lines, err := r.invoiceLines(ctx, id)
	if err != nil {
		return nil, err
	}
	inv.Lines = lines
	return inv, nil
}

// LockInvoice takes the invoice row FOR UPDATE (section 11, step 4), held to
// the branch wall, and reads it. Lock first, read second: a joined select
// cannot lock the joined rows.
func (r *PostgresRepository) LockInvoice(ctx context.Context, id uuid.UUID) (*Invoice, error) {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT i.id FROM invoices i WHERE i.id = $1 AND `+wall("i", 2, 3)+` FOR UPDATE`,
		id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errInvoiceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock invoice: %w", err)
	}
	return r.GetInvoice(ctx, id)
}

// GetInvoiceRecord reads an invoice for a route that holds the loaded record's
// own branch to the payload branch rule (CheckPayloadBranch: a 403 naming the
// record), as the document print and email routes do (PR 39). It applies the
// request's context branch only: the record check, not this read, is what
// scopes a bound caller with no context branch to its grants.
func (r *PostgresRepository) GetInvoiceRecord(ctx context.Context, id uuid.UUID) (*Invoice, error) {
	inv, err := r.invoiceHead(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if inv.Lines, err = r.invoiceLines(ctx, id); err != nil {
		return nil, err
	}
	return inv, nil
}

// invoiceHead reads the invoice header; the wall applies. contextOnly drops
// the grants half of the wall (GetInvoiceRecord).
func (r *PostgresRepository) invoiceHead(ctx context.Context, id uuid.UUID, contextOnly bool) (*Invoice, error) {
	var inv Invoice
	var snapshot []byte
	grants := middleware.GrantsSubForQuery(ctx)
	if contextOnly {
		grants = nil
	}
	row := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT `+summaryColumns+`, i.ship_to_snapshot`+summaryFrom+`
		WHERE i.id = $1 AND `+wall("i", 2, 3),
		id, middleware.BranchIDForQuery(ctx), grants)
	if err := scanSummary(row, &inv.InvoiceSummary, &snapshot); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errInvoiceNotFound
		}
		return nil, fmt.Errorf("failed to get invoice: %w", err)
	}
	if len(snapshot) > 0 {
		var s salesdoc.ShipToSnapshot
		if err := json.Unmarshal(snapshot, &s); err != nil {
			return nil, fmt.Errorf("failed to read the ship-to snapshot: %w", err)
		}
		inv.ShipTo = &s
	}
	return &inv, nil
}

func (r *PostgresRepository) invoiceLines(ctx context.Context, id uuid.UUID) ([]InvoiceLine, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+salesdoc.LineColumns("COALESCE(ol.is_special_order, false), ol.vendor_id, ROUND(ol.special_order_cost * 10000)::bigint")+`,
		       l.order_line_id, ROUND(l.unit_cost * 10000)::bigint, ROUND(l.cost * 100)::bigint
		FROM invoice_lines l
		LEFT JOIN charge_codes cc ON cc.id = l.charge_code_id
		LEFT JOIN order_lines ol ON ol.id = l.order_line_id
		WHERE l.invoice_id = $1
		ORDER BY l.position, l.created_at, l.id`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get invoice lines: %w", err)
	}
	defer rows.Close()
	out := []InvoiceLine{}
	for rows.Next() {
		var (
			l        InvoiceLine
			ls       salesdoc.LineScan
			unitCost *int64
			cost     int64
		)
		if err := rows.Scan(append(ls.Dests(&l.Line), &l.OrderLineID, &unitCost, &cost)...); err != nil {
			return nil, fmt.Errorf("failed to scan invoice line: %w", err)
		}
		ls.Finish(&l.Line)
		l.UnitCost = salesdoc.PtrPrice(unitCost)
		l.CostCents = httpx.Cents(cost)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// The older callers' slice.
// ---------------------------------------------------------------------------

// UpdateInvoice writes the status and paid_at the payment module derives and
// moves the revision (an in process write: no client precondition).
func (r *PostgresRepository) UpdateInvoice(ctx context.Context, inv *Invoice) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE invoices SET status = $1, paid_at = $2, updated_at = NOW(), revision = revision + 1
		WHERE id = $3`, string(inv.Status), inv.PaidAt, inv.ID)
	if err != nil {
		return fmt.Errorf("failed to update invoice: %w", err)
	}
	return nil
}

// ExistsInvoiceForOrder reports whether an invoice (not void) exists for the order.
func (r *PostgresRepository) ExistsInvoiceForOrder(ctx context.Context, orderID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM invoices WHERE order_id = $1 AND status <> 'VOID')`, orderID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check existing invoice for order: %w", err)
	}
	return exists, nil
}

// SumOpenBalanceCents returns the customer's outstanding AR balance, computed
// live from open invoices (total less the payments recorded against each).
func (r *PostgresRepository) SumOpenBalanceCents(ctx context.Context, customerID uuid.UUID) (int64, error) {
	var cents int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(`+openExpr+`), 0)::bigint FROM invoices i
		WHERE i.customer_id = $1 AND i.status IN (`+OpenInvoiceStatuses+`)`, customerID).Scan(&cents)
	if err != nil {
		return 0, fmt.Errorf("failed to sum open balance: %w", err)
	}
	return cents, nil
}

// GetBranchTaxRate returns the default sales tax rate configured on a branch
// (locations.default_tax_rate). branchID may be nil, in which case the active
// branch context, or the system default branch, is used. It answers true only
// for a positive rate; the counter's legacy path falls back to DefaultTaxRate.
func (r *PostgresRepository) GetBranchTaxRate(ctx context.Context, branchID *uuid.UUID) (float64, bool) {
	bid := branchID
	if bid == nil || *bid == uuid.Nil {
		bid = middleware.BranchIDForQuery(ctx)
	}
	var rate *float64
	var err error
	if bid != nil {
		err = r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT default_tax_rate FROM locations WHERE id = $1`, *bid).Scan(&rate)
	} else {
		err = r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT default_tax_rate FROM locations
			 WHERE id = (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`).Scan(&rate)
	}
	if err != nil || rate == nil || *rate <= 0 {
		return 0, false
	}
	return *rate, true
}

// CreateInvoice is the counter's account charge (the legacy path C2-5
// retires): the customer row is locked first (section 11: nothing takes the
// customer after the gapless counter), the invoice numbers itself through the
// column DEFAULT, the terms are the customer's by id, and the line lands in
// the shared shape with the unit and description from the product, the pair
// 1 and 1 and the extension rounded once.
func (r *PostgresRepository) CreateInvoice(ctx context.Context, inv *LegacyInvoice) error {
	exec := r.db.GetExecutor(ctx)
	if inv.ID == uuid.Nil {
		inv.ID = uuid.New()
	}
	now := time.Now()
	inv.CreatedAt, inv.UpdatedAt = now, now
	if inv.Status == "" {
		inv.Status = InvoiceStatusUnpaid
	}
	if err := r.LockCustomer(ctx, inv.CustomerID); err != nil {
		return err
	}
	var branchArg any
	if inv.BranchID != uuid.Nil {
		branchArg = inv.BranchID
	} else if bid := middleware.BranchIDForQuery(ctx); bid != nil {
		branchArg = *bid
		inv.BranchID = *bid
	}
	// The terms, by id, and the dates they give, from the invoice's date.
	terms, err := r.TermsFor(ctx, inv.CustomerID)
	if err != nil {
		return err
	}
	var due, discountDue any
	if inv.DueDate != nil {
		due = inv.DueDate.Format("2006-01-02")
	} else {
		due = terms.Terms.DueDate(now).Format("2006-01-02")
	}
	var discountPct any
	if dd := terms.Terms.DiscountDueDate(now); dd != nil && terms.Terms.DiscountPercent != nil {
		discountDue, discountPct = dd.Format("2006-01-02"), int64(*terms.Terms.DiscountPercent)
	}
	_, err = exec.Exec(ctx, `
		INSERT INTO invoices (id, order_id, customer_id, status, total_amount, subtotal, tax_rate, tax_amount,
			payment_terms_id, due_date, discount_due_date, discount_percent, paid_at, created_at, updated_at, branch_id, origin)
		VALUES ($1, NULLIF($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid), $3, $4, $5::numeric / 100, $6::numeric / 100,
			$7, $8::numeric / 100, $9, $10::date, $11::date, $12::numeric / 10000, $13, $14, $15,
			COALESCE($16::uuid, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')),
			CASE WHEN NULLIF($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid) IS NULL THEN 'POS' ELSE 'ORDER' END)`,
		inv.ID, inv.OrderID, inv.CustomerID, string(inv.Status), inv.TotalAmount, inv.Subtotal, inv.TaxRate, inv.TaxAmount,
		terms.ID, due, discountDue, discountPct, inv.PaidAt, inv.CreatedAt, inv.UpdatedAt, branchArg)
	if err != nil {
		return fmt.Errorf("failed to insert invoice: %w", err)
	}
	queryLine := `
		INSERT INTO invoice_lines (id, invoice_id, product_id, quantity, price_each, created_at,
			sku, description, uom, price_uom, uom_qty, price_uom_qty, unit_price, line_total)
		SELECT $1, $2, $3, $4::numeric, $5::numeric, $6,
			p.sku, COALESCE(p.description, p.sku, ''), p.uom_primary::text, p.uom_primary::text, 1, 1, $5::numeric,
			ROUND($4::numeric * $5::numeric, 2)
		FROM products p WHERE p.id = $3`
	for i := range inv.Lines {
		line := &inv.Lines[i]
		if line.ID == uuid.Nil {
			line.ID = uuid.New()
		}
		line.InvoiceID = inv.ID
		priceEach := float64(line.PriceEach) / 100.0
		tag, err := exec.Exec(ctx, queryLine, line.ID, line.InvoiceID, line.ProductID, line.Quantity, priceEach, now)
		if err != nil {
			return fmt.Errorf("failed to insert invoice line: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("failed to insert invoice line: product %s does not exist", line.ProductID)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The locks and the numbering of section 11.
// ---------------------------------------------------------------------------

// LockCustomer takes the customer's row FOR UPDATE (section 11, step 7): the
// AR core's balance_due lock, taken before the gapless counter so no code
// takes the customer after a number.
func (r *PostgresRepository) LockCustomer(ctx context.Context, customerID uuid.UUID) error {
	var id uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT id FROM customers WHERE id = $1 FOR UPDATE`, customerID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return validationFailed("a referenced record does not exist", httpx.FieldError{Field: "customer_id", Message: "no such customer"})
	}
	if err != nil {
		return fmt.Errorf("failed to lock the customer: %w", err)
	}
	return nil
}

// LockCustomerCredit serializes the acts that add to one customer's credit
// exposure with the order module's transaction scoped advisory lock (section
// 11, step 1a: the same key as the order's confirm, release and fulfilment).
// It is not a row lock, so it adds no edge to the lock order.
func (r *PostgresRepository) LockCustomerCredit(ctx context.Context, customerID uuid.UUID) error {
	if _, err := r.db.GetExecutor(ctx).Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('order-credit:' || $1::text, 0))`, customerID.String()); err != nil {
		return fmt.Errorf("failed to serialize the customer's credit acts: %w", err)
	}
	return nil
}

// NextInvoiceNumber mints the next gapless IN- number through the caller's
// transaction (section 11, step 8).
func (r *PostgresRepository) NextInvoiceNumber(ctx context.Context) (string, error) {
	return httpx.NextGaplessNumber(ctx, r.db.GetExecutor(ctx), "invoice", "IN", httpx.DefaultDocNumberWidth)
}

// NextCreditMemoNumber mints the next gapless CM- number.
func (r *PostgresRepository) NextCreditMemoNumber(ctx context.Context) (string, error) {
	return httpx.NextGaplessNumber(ctx, r.db.GetExecutor(ctx), "credit_memo", "CM", httpx.DefaultDocNumberWidth)
}

// CustomerTerms is a customer's payment terms row with its id.
type CustomerTerms struct {
	ID    uuid.UUID
	Terms customer.PaymentTerms
}

// TermsFor reads the customer's payment terms (ADR 0005 7.2).
func (r *PostgresRepository) TermsFor(ctx context.Context, customerID uuid.UUID) (*CustomerTerms, error) {
	var (
		t            CustomerTerms
		kind         string
		discount     *int64
		netDays, dom *int
		discDays     *int
	)
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT t.id, t.code, t.name, t.kind, t.net_days, t.day_of_month, ROUND(t.discount_percent * 10000)::bigint, t.discount_days
		FROM customers c JOIN payment_terms t ON t.id = c.payment_terms_id
		WHERE c.id = $1`, customerID).Scan(&t.ID, &t.Terms.Code, &t.Terms.Name, &kind, &netDays, &dom, &discount, &discDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, validationFailed("a referenced record does not exist", httpx.FieldError{Field: "customer_id", Message: "no such customer"})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the customer's terms: %w", err)
	}
	t.Terms.ID = t.ID
	t.Terms.Kind = customer.TermsKind(kind)
	t.Terms.NetDays, t.Terms.DayOfMonth, t.Terms.DiscountDays = netDays, dom, discDays
	if discount != nil {
		q := httpx.Quantity(*discount)
		t.Terms.DiscountPercent = &q
	}
	return &t, nil
}

// BranchLocalDate is the branch's local calendar date at the instant (the
// business date of ADR 0005 section 8.1).
func (r *PostgresRepository) BranchLocalDate(ctx context.Context, branchID uuid.UUID, at time.Time) (time.Time, error) {
	var d time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT ($1::timestamptz AT TIME ZONE COALESCE((SELECT timezone FROM locations WHERE id = $2), 'UTC'))::date`,
		at, branchID).Scan(&d)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to read the branch's date: %w", err)
	}
	return d, nil
}

// InsertFulfilmentInvoice writes the header and the lines of an order
// fulfilment's invoice in the shape of ADR 0005 6.1 and 2.2, through the
// caller's executor, the number minted just before (the caller holds the
// customer row). Money arrives in cents and scale 4 integers and is divided
// in SQL; price_each, kept for the readers still on it, holds the effective
// price per sale unit (the line total over the quantity).
func (r *PostgresRepository) InsertFulfilmentInvoice(ctx context.Context, in *FulfilmentInvoice) error {
	exec := r.db.GetExecutor(ctx)
	var discountDue any
	if in.DiscountDueDate != nil {
		discountDue = in.DiscountDueDate.Format("2006-01-02")
	}
	_, err := exec.Exec(ctx, `
		INSERT INTO invoices (id, number, order_id, customer_id, status, subtotal, tax_rate, tax_amount, total_amount,
			payment_terms_id, due_date, discount_due_date, discount_percent,
			branch_id, currency, delivery_type, picked_up_by, delivery_id,
			ship_to_id, ship_to_snapshot, project_id, tax_exempt, tax_source, invoice_date, origin, paid_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, CASE WHEN $8::bigint = 0 THEN 'PAID' ELSE 'UNPAID' END,
			$5::numeric / 100, $6::numeric, $7::numeric / 100, $8::numeric / 100,
			$9, $10::date, $11::date, $12::numeric / 10000,
			$13, $14, $15, $16, $17,
			$18, $19, $20, $21, $22, $23::date, 'ORDER', CASE WHEN $8::bigint = 0 THEN NOW() END, NOW(), NOW())`,
		in.ID, in.Number, in.OrderID, in.CustomerID, in.SubtotalCents, in.TaxRate, in.TaxCents, in.TotalCents,
		in.PaymentTermsID, in.DueDate.Format("2006-01-02"), discountDue, in.DiscountPercent,
		in.BranchID, in.Currency, in.DeliveryType, in.PickedUpBy, in.DeliveryID,
		in.ShipToID, in.ShipToSnapshot, in.ProjectID, in.TaxExempt, in.TaxSource, in.InvoiceDate)
	if err != nil {
		return fmt.Errorf("failed to insert invoice: %w", err)
	}
	// Parents first: a component's parent_line_id references its kit line.
	for i := range in.Lines {
		l := &in.Lines[i]
		_, err := exec.Exec(ctx, `
			INSERT INTO invoice_lines (id, invoice_id, position, line_type, parent_line_id, product_id, charge_code_id,
				sku, description, quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, price_each, price_source,
				priced_unit_price, override_reason, price_adjusted_by,
				discount_percent, discount_amount, discount_reason, line_total, taxable, revenue_account_code,
				order_line_id, unit_cost, cost, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10::numeric / 10000, $11, $12, $13::numeric / 10000, $14::numeric / 10000, $15::numeric / 10000,
				CASE WHEN $10::numeric > 0 AND $24::numeric IS NOT NULL THEN ROUND(($24::numeric / 100) / ($10::numeric / 10000), 4) ELSE $15::numeric / 10000 END,
				$16, $26::numeric / 10000, $27, $28,
				$17::numeric / 10000, $18::numeric / 100, $19, $24::numeric / 100, $20, $21,
				$22, $23::numeric / 10000, $25::numeric / 100, NOW())`,
			l.ID, in.ID, l.Position, l.LineType, l.ParentLineID, l.ProductID, l.ChargeCodeID,
			l.SKU, l.Description, l.Quantity, l.UOM, l.PriceUOM, l.UOMQty, l.PriceUOMQty, l.UnitPrice, l.PriceSource,
			l.DiscountPercent, l.DiscountCents, l.DiscountReason, l.Taxable, l.RevenueAccountCode,
			l.OrderLineID, l.UnitCost, l.LineTotalCents, l.CostCents,
			l.PricedUnitPrice, l.OverrideReason, l.PriceAdjustedBy)
		if err != nil {
			return fmt.Errorf("failed to insert invoice line: %w", err)
		}
	}
	return nil
}

// SetInvoiceGLEntry records the invoice's journal entry on the invoice.
func (r *PostgresRepository) SetInvoiceGLEntry(ctx context.Context, invoiceID, entryID uuid.UUID) error {
	if _, err := r.db.GetExecutor(ctx).Exec(ctx, `UPDATE invoices SET gl_entry_id = $2 WHERE id = $1`, invoiceID, entryID); err != nil {
		return fmt.Errorf("failed to record the invoice entry: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// The void.
// ---------------------------------------------------------------------------

// VoidFacts are the documents that stand in the way of a void (ADR 0005
// 6.2): a payment recorded against the invoice, an applied credit memo
// naming it, any credit memo naming it that is not void.
type VoidFacts struct {
	Payments     int
	AppliedMemos int
	LiveMemos    int
}

// VoidFactsFor reads them. The credit memo rows are read under the invoice
// lock the caller holds: a credit memo post locks the invoice first, so the
// check cannot race it.
func (r *PostgresRepository) VoidFactsFor(ctx context.Context, invoiceID uuid.UUID) (VoidFacts, error) {
	var f VoidFacts
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT (SELECT count(*) FROM payments WHERE invoice_id = $1),
		       (SELECT count(*) FROM credit_memos WHERE invoice_id = $1 AND status IN ('APPLIED', 'PARTIAL')),
		       (SELECT count(*) FROM credit_memos WHERE invoice_id = $1 AND status <> 'VOID')`, invoiceID).
		Scan(&f.Payments, &f.AppliedMemos, &f.LiveMemos)
	if err != nil {
		return f, fmt.Errorf("failed to read what stands in the way of a void: %w", err)
	}
	return f, nil
}

// MarkVoid ends the invoice: status VOID, the void columns, the revision.
func (r *PostgresRepository) MarkVoid(ctx context.Context, id uuid.UUID, actor, reason string, voidedOn time.Time) error {
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE invoices SET status = 'VOID', voided_at = NOW(), voided_by = NULLIF($2, ''), void_reason = $3,
			voided_on = $4::date, updated_at = NOW(), revision = revision + 1
		WHERE id = $1`, id, actor, reason, voidedOn.Format("2006-01-02"))
	if err != nil {
		return fmt.Errorf("failed to void the invoice: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return errInvoiceNotFound
	}
	return nil
}

// BilledLine is a line an invoice billed against an order line: what a void
// returns to the order (stock back on hand, quantity_fulfilled reduced).
type BilledLine struct {
	OrderLineID uuid.UUID
	LineType    string
	ProductID   *uuid.UUID
	Quantity    httpx.Quantity
}

// BilledLines lists the invoice lines that name an order line.
func (r *PostgresRepository) BilledLines(ctx context.Context, invoiceID uuid.UUID) ([]BilledLine, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT order_line_id, line_type, product_id, ROUND(quantity * 10000)::bigint
		FROM invoice_lines WHERE invoice_id = $1 AND order_line_id IS NOT NULL AND quantity IS NOT NULL
		ORDER BY position, id`, invoiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the billed lines: %w", err)
	}
	defer rows.Close()
	var out []BilledLine
	for rows.Next() {
		var b BilledLine
		var q int64
		if err := rows.Scan(&b.OrderLineID, &b.LineType, &b.ProductID, &q); err != nil {
			return nil, fmt.Errorf("failed to scan a billed line: %w", err)
		}
		b.Quantity = httpx.Quantity(q)
		out = append(out, b)
	}
	return out, rows.Err()
}
