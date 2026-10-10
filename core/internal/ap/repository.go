// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ap

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

var errInvoiceNotFound = httpx.NotFound("vendor invoice not found")

// Repository reads and writes the AP tables. Every statement goes through the
// executor the context resolves, so inside a transaction it is the
// transaction; the branch wall rides every read and every lock.
type Repository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *Repository { return &Repository{db: db} }

// wall is the branch predicate on an aliased table's branch_id (nil context
// values mean no wall: an in-process caller or an administrator).
func wall(alias string, branch, grants int) string {
	return fmt.Sprintf(`(
	    ($%[2]d::uuid IS NOT NULL AND %[1]s.branch_id = $%[2]d)
	    OR ($%[2]d::uuid IS NULL AND $%[3]d::text IS NOT NULL AND %[1]s.branch_id IN
	        (SELECT branch_id FROM user_locations WHERE user_sub = $%[3]d))
	    OR ($%[2]d::uuid IS NULL AND $%[3]d::text IS NULL)
	  )`, alias, branch, grants)
}

const summaryColumns = `
	vi.id, vi.number, vi.vendor_id, COALESCE(v.name, ''), vi.branch_id, vi.invoice_number, vi.currency,
	to_char(vi.invoice_date, 'YYYY-MM-DD'), to_char(vi.due_date, 'YYYY-MM-DD'), vi.po_id,
	ROUND(vi.subtotal * 100)::bigint, ROUND(vi.tax_amount * 100)::bigint, ROUND(vi.total * 100)::bigint,
	ROUND(vi.amount_paid * 100)::bigint, ROUND(vi.amount_open * 100)::bigint, vi.status,
	vi.approved_by, vi.approved_at, COALESCE(vi.notes, ''), vi.revision, vi.gl_entry_id, vi.created_at`

const summaryFrom = `
	FROM vendor_invoices vi
	LEFT JOIN vendors v ON v.id = vi.vendor_id`

type rowScan func(dest ...any) error

func scanSummary(scan rowScan, s *Summary) error {
	var (
		subtotal, tax, total, paid, open int64
		status                           string
		approvedAt                       *time.Time
		created                          time.Time
	)
	if err := scan(&s.ID, &s.Number, &s.VendorID, &s.VendorName, &s.BranchID, &s.VendorInvoiceNumber, &s.Currency,
		&s.InvoiceDate, &s.DueDate, &s.POID, &subtotal, &tax, &total, &paid, &open, &status,
		&s.ApprovedBy, &approvedAt, &s.Notes, &s.Revision, &s.GLEntryID, &created); err != nil {
		return err
	}
	s.SubtotalCents, s.TaxCents, s.TotalCents, s.AmountPaidCents, s.AmountOpenCents =
		httpx.Cents(subtotal), httpx.Cents(tax), httpx.Cents(total), httpx.Cents(paid), httpx.Cents(open)
	s.Status, s.ApprovedAt, s.CreatedAt = wireStatus(status), httpx.PtrTimestamp(approvedAt), httpx.TimestampOf(created)
	return nil
}

const lineColumns = `
	l.id, l.position, l.description, l.quantity::text, ROUND(l.unit_price * 10000)::bigint, ROUND(l.line_total * 100)::bigint,
	l.gl_account_id, l.purchase_order_line_id, l.product_id, l.po_freight_charge_id, l.created_at`

func scanLine(scan rowScan, l *InvoiceLine) error {
	var (
		qty     string
		price   int64
		total   int64
		created time.Time
	)
	if err := scan(&l.ID, &l.Position, &l.Description, &qty, &price, &total,
		&l.GLAccountID, &l.PurchaseOrderLineID, &l.ProductID, &l.POFreightChargeID, &created); err != nil {
		return err
	}
	q, err := httpx.ParseQuantity(qty)
	if err != nil {
		return fmt.Errorf("vendor invoice line quantity %q: %w", qty, err)
	}
	l.Quantity, l.UnitPriceTenThousandths, l.LineTotalCents = q, httpx.Price(price), httpx.Cents(total)
	l.CreatedAt = httpx.TimestampOf(created)
	return nil
}

// Get reads one vendor invoice, header and lines, held to the branch wall.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*Invoice, error) {
	var inv Invoice
	row := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+summaryColumns+summaryFrom+`
		WHERE vi.id = $1 AND `+wall("vi", 2, 3), id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err := scanSummary(row.Scan, &inv.Summary); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errInvoiceNotFound
		}
		return nil, fmt.Errorf("failed to get vendor invoice: %w", err)
	}
	lines, err := r.Lines(ctx, id)
	if err != nil {
		return nil, err
	}
	inv.Lines = lines
	return &inv, nil
}

// Lines reads a bill's lines in position order.
func (r *Repository) Lines(ctx context.Context, invoiceID uuid.UUID) ([]InvoiceLine, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `SELECT `+lineColumns+`
		FROM vendor_invoice_lines l WHERE l.invoice_id = $1 ORDER BY l.position, l.created_at, l.id`, invoiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to read vendor invoice lines: %w", err)
	}
	defer rows.Close()
	out := []InvoiceLine{}
	for rows.Next() {
		var l InvoiceLine
		if err := scanLine(rows.Scan, &l); err != nil {
			return nil, fmt.Errorf("failed to scan vendor invoice line: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LineWithCode is a line beside its account's stable code, what the approve
// entry's debit legs are built from.
type LineWithCode struct {
	InvoiceLine
	AccountCode string
}

// LinesWithCodes reads a bill's lines joined to their account codes, in
// position order. A line with no account reads an empty code (the service
// refuses the approve naming it).
func (r *Repository) LinesWithCodes(ctx context.Context, invoiceID uuid.UUID) ([]LineWithCode, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `SELECT `+lineColumns+`, COALESCE(a.code, '')
		FROM vendor_invoice_lines l LEFT JOIN gl_accounts a ON a.id = l.gl_account_id
		WHERE l.invoice_id = $1 ORDER BY l.position, l.created_at, l.id`, invoiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to read vendor invoice lines: %w", err)
	}
	defer rows.Close()
	out := []LineWithCode{}
	for rows.Next() {
		var (
			l       LineWithCode
			qty     string
			price   int64
			total   int64
			created time.Time
		)
		if err := rows.Scan(&l.ID, &l.Position, &l.Description, &qty, &price, &total,
			&l.GLAccountID, &l.PurchaseOrderLineID, &l.ProductID, &l.POFreightChargeID, &created, &l.AccountCode); err != nil {
			return nil, fmt.Errorf("failed to scan vendor invoice line: %w", err)
		}
		q, err := httpx.ParseQuantity(qty)
		if err != nil {
			return nil, fmt.Errorf("vendor invoice line quantity %q: %w", qty, err)
		}
		l.Quantity, l.UnitPriceTenThousandths, l.LineTotalCents = q, httpx.Price(price), httpx.Cents(total)
		l.CreatedAt = httpx.TimestampOf(created)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListFilter is the list's filters and keyset position.
type ListFilter struct {
	VendorID  *uuid.UUID
	Statuses  []Status
	POID      *uuid.UUID
	AfterTime *time.Time
	AfterID   uuid.UUID
	Limit     int
}

var listWhere = "\n\tWHERE " + wall("vi", 1, 2) + `
		  AND ($3::uuid IS NULL OR vi.vendor_id = $3)
		  AND (cardinality($4::text[]) = 0 OR vi.status = ANY($4))
		  AND ($5::uuid IS NULL OR vi.po_id = $5)`

func (r *Repository) listArgs(ctx context.Context, f ListFilter) []any {
	statuses := make([]string, len(f.Statuses))
	for i, s := range f.Statuses {
		statuses[i] = storageStatus(s)
	}
	return []any{middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), f.VendorID, statuses, f.POID}
}

// List answers one page, newest first by (created_at, id).
func (r *Repository) List(ctx context.Context, f ListFilter) ([]Summary, error) {
	args := append(r.listArgs(ctx, f), f.AfterTime, f.AfterID, f.Limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `SELECT `+summaryColumns+summaryFrom+listWhere+`
		  AND ($6::timestamptz IS NULL OR (vi.created_at, vi.id) < ($6, $7::uuid))
		ORDER BY vi.created_at DESC, vi.id DESC
		LIMIT $8`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list vendor invoices: %w", err)
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var s Summary
		if err := scanSummary(rows.Scan, &s); err != nil {
			return nil, fmt.Errorf("failed to scan vendor invoice: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Count is the size of the filtered set, for ?include=total.
func (r *Repository) Count(ctx context.Context, f ListFilter) (int64, error) {
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT count(*) FROM vendor_invoices vi`+listWhere,
		r.listArgs(ctx, f)...).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count vendor invoices: %w", err)
	}
	return n, nil
}

// LockedInvoice is a bill read under its row lock: what a transition or a
// payment checks before it writes.
type LockedInvoice struct {
	Summary
}

// Lock takes the vendor invoice row FOR UPDATE, held to the branch wall.
func (r *Repository) Lock(ctx context.Context, id uuid.UUID) (*LockedInvoice, error) {
	var locked LockedInvoice
	row := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+summaryColumns+summaryFrom+`
		WHERE vi.id = $1 AND `+wall("vi", 2, 3)+` FOR UPDATE OF vi`, id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err := scanSummary(row.Scan, &locked.Summary); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errInvoiceNotFound
		}
		return nil, fmt.Errorf("failed to lock vendor invoice: %w", err)
	}
	return &locked, nil
}

// LockForPayment takes one invoice of the named vendor FOR UPDATE, in the
// caller's id order (ADR 0008 section 9 step 5a). No row means no such
// invoice for this vendor.
func (r *Repository) LockForPayment(ctx context.Context, id, vendorID uuid.UUID) (*Summary, error) {
	var s Summary
	row := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+summaryColumns+summaryFrom+`
		WHERE vi.id = $1 AND vi.vendor_id = $2 AND `+wall("vi", 3, 4)+` FOR UPDATE OF vi`,
		id, vendorID, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err := scanSummary(row.Scan, &s); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to lock vendor invoice for payment: %w", err)
	}
	return &s, nil
}

// Create inserts a bill and its lines. The number comes from the column's
// DEFAULT, which mints through this transaction (a rollback abandons it).
func (r *Repository) Create(ctx context.Context, in *Input, branch uuid.UUID, currency string) (*Invoice, error) {
	var inv Invoice
	var created time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO vendor_invoices (vendor_id, invoice_number, invoice_date, due_date, po_id, branch_id, currency,
		                             subtotal, tax_amount, total, amount_open, status, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::bigint::numeric / 100, $9::bigint::numeric / 100,
		        $10::bigint::numeric / 100, $10::bigint::numeric / 100, 'PENDING', NULLIF($11, ''))
		RETURNING id, number, created_at`,
		in.VendorID, in.VendorInvoiceNumber, in.InvoiceDate, in.DueDate, in.POID, branch, currency,
		int64(subtotalCents(in)), int64(in.TaxCents), int64(totalCents(in)), in.Notes).Scan(&inv.ID, &inv.Number, &created)
	if err != nil {
		return nil, mapWriteError(err, "vendor_invoice_number", "vendor_id")
	}
	inv.CreatedAt = httpx.TimestampOf(created)
	for i, l := range in.Lines {
		if _, err := r.db.GetExecutor(ctx).Exec(ctx, `
			INSERT INTO vendor_invoice_lines (invoice_id, position, description, quantity, unit_price, line_total,
			                                  gl_account_id, purchase_order_line_id, product_id)
			VALUES ($1, $2, $3, $4::numeric, $5::bigint::numeric / 10000, $6::bigint::numeric / 100, $7, $8, $9)`,
			inv.ID, i, l.Description, l.Quantity.DecimalString(), int64(l.UnitPrice), int64(l.LineTotalCents),
			l.GLAccountID, l.PurchaseOrderLineID, l.ProductID); err != nil {
			return nil, mapWriteError(err, "", fmt.Sprintf("lines[%d]", i))
		}
	}
	full, err := r.Get(ctx, inv.ID)
	if err != nil {
		return nil, err
	}
	return full, nil
}

// MarkApproved sets the bill approved under its lock, recording who approved
// it and the entry the approval posted.
func (r *Repository) MarkApproved(ctx context.Context, id uuid.UUID, approver *uuid.UUID, entryID *uuid.UUID) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE vendor_invoices SET status = 'APPROVED', approved_by = $2, approved_at = NOW(),
		                           gl_entry_id = $3, revision = revision + 1
		WHERE id = $1 AND status = 'PENDING'`, id, approver, entryID)
	if err != nil {
		return fmt.Errorf("failed to approve vendor invoice: %w", err)
	}
	return nil
}

// MarkVoided sets the bill voided under its lock, owing nothing more.
func (r *Repository) MarkVoided(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE vendor_invoices SET status = 'VOIDED', amount_open = 0, revision = revision + 1
		WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to void vendor invoice: %w", err)
	}
	return nil
}

// ApplyToInvoice moves a payment's cents onto a locked bill: the paid and open
// amounts, the status the open amount settles, and the revision.
func (r *Repository) ApplyToInvoice(ctx context.Context, id uuid.UUID, apply int64) (paid bool, err error) {
	err = r.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE vendor_invoices
		SET amount_paid = amount_paid + $2::bigint::numeric / 100,
		    amount_open = amount_open - $2::bigint::numeric / 100,
		    status = CASE WHEN amount_open - $2::bigint::numeric / 100 = 0 THEN 'PAID' ELSE 'PARTIAL' END,
		    revision = revision + 1
		WHERE id = $1 RETURNING (status = 'PAID')`, id, apply).Scan(&paid)
	if err != nil {
		return false, fmt.Errorf("failed to apply payment to vendor invoice: %w", err)
	}
	return paid, nil
}

// CreatePayment writes an AP payment row.
func (r *Repository) CreatePayment(ctx context.Context, pmt *APPayment) error {
	if pmt.ID == uuid.Nil {
		pmt.ID = uuid.New()
	}
	pmt.CreatedAt = time.Now()
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO ap_payments (id, vendor_id, amount, method, check_number, reference, payment_date, status, created_at)
		VALUES ($1, $2, $3::bigint::numeric / 100, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8, $9)`,
		pmt.ID, pmt.VendorID, pmt.Amount, pmt.Method, pmt.CheckNumber, pmt.Reference, pmt.PaymentDate, pmt.Status, pmt.CreatedAt)
	if err != nil {
		return mapWriteError(err, "", "vendor_id")
	}
	return nil
}

// CreatePaymentApplication links an applied amount to a bill.
func (r *Repository) CreatePaymentApplication(ctx context.Context, app *APPaymentApplication) error {
	if app.ID == uuid.Nil {
		app.ID = uuid.New()
	}
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO ap_payment_applications (id, payment_id, invoice_id, amount, created_at)
		VALUES ($1, $2, $3, $4::bigint::numeric / 100, $5)`,
		app.ID, app.PaymentID, app.InvoiceID, app.Amount, time.Now())
	if err != nil {
		return mapWriteError(err, "", "invoice_id")
	}
	return nil
}

// ListPayments lists a vendor's payments, newest first.
func (r *Repository) ListPayments(ctx context.Context, vendorID *uuid.UUID) ([]APPayment, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT p.id, p.vendor_id, COALESCE(v.name, '') as vendor_name,
		       ROUND(p.amount * 100)::bigint, p.method, COALESCE(p.check_number, ''),
		       COALESCE(p.reference, ''), to_char(p.payment_date, 'YYYY-MM-DD'), p.status, p.created_at
		FROM ap_payments p
		LEFT JOIN vendors v ON v.id = p.vendor_id
		WHERE ($1::uuid IS NULL OR p.vendor_id = $1)
		ORDER BY p.created_at DESC, p.id DESC`, vendorID)
	if err != nil {
		return nil, fmt.Errorf("failed to list AP payments: %w", err)
	}
	defer rows.Close()
	var payments []APPayment
	for rows.Next() {
		var pmt APPayment
		var amount int64
		if err := rows.Scan(&pmt.ID, &pmt.VendorID, &pmt.VendorName, &amount, &pmt.Method, &pmt.CheckNumber,
			&pmt.Reference, &pmt.PaymentDate, &pmt.Status, &pmt.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan AP payment: %w", err)
		}
		pmt.Amount = amount
		payments = append(payments, pmt)
	}
	return payments, rows.Err()
}

// GetAgingSummary returns the AP aging report by vendor, in cents.
func (r *Repository) GetAgingSummary(ctx context.Context) ([]APAgingSummary, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT vi.vendor_id, COALESCE(v.name, 'Unknown') as vendor_name,
			ROUND(COALESCE(SUM(CASE WHEN vi.due_date >= CURRENT_DATE THEN vi.amount_open ELSE 0 END), 0) * 100)::bigint,
			ROUND(COALESCE(SUM(CASE WHEN vi.due_date < CURRENT_DATE AND vi.due_date >= CURRENT_DATE - 30 THEN vi.amount_open ELSE 0 END), 0) * 100)::bigint,
			ROUND(COALESCE(SUM(CASE WHEN vi.due_date < CURRENT_DATE - 30 AND vi.due_date >= CURRENT_DATE - 60 THEN vi.amount_open ELSE 0 END), 0) * 100)::bigint,
			ROUND(COALESCE(SUM(CASE WHEN vi.due_date < CURRENT_DATE - 60 THEN vi.amount_open ELSE 0 END), 0) * 100)::bigint,
			ROUND(COALESCE(SUM(vi.amount_open), 0) * 100)::bigint
		FROM vendor_invoices vi
		LEFT JOIN vendors v ON v.id = vi.vendor_id
		WHERE vi.status NOT IN ('PAID', 'VOIDED')
		GROUP BY vi.vendor_id, v.name
		ORDER BY 6 DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to get AP aging: %w", err)
	}
	defer rows.Close()
	var summaries []APAgingSummary
	for rows.Next() {
		var s APAgingSummary
		if err := rows.Scan(&s.VendorID, &s.VendorName, &s.Current, &s.Past30, &s.Past60, &s.Past90, &s.Total); err != nil {
			return nil, fmt.Errorf("failed to scan aging: %w", err)
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

// VendorFacts are what a bill takes from its vendor: that it exists.
func (r *Repository) VendorExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var n int
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT count(*) FROM vendors WHERE id = $1`, id).Scan(&n)
	return n > 0, err
}

// POExists reports whether a purchase order exists, for the po_id reference.
func (r *Repository) POExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var n int
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT count(*) FROM purchase_orders WHERE id = $1`, id).Scan(&n)
	return n > 0, err
}

// POLineExists reports whether a purchase order line exists, for a line's
// purchase_order_line_id reference.
func (r *Repository) POLineExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var n int
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT count(*) FROM purchase_order_lines WHERE id = $1`, id).Scan(&n)
	return n > 0, err
}

// ProductExists reports whether a product exists, for a line's product_id
// reference.
func (r *Repository) ProductExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var n int
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT count(*) FROM products WHERE id = $1`, id).Scan(&n)
	return n > 0, err
}

// mapWriteError turns a database write refusal into the wire's error: a
// unique violation is a 409 duplicate naming the field, a foreign key
// violation is a 400 naming the field, and everything else stands as it is.
// dupField is the field a uniqueness refusal names; fks are the fields a
// foreign key refusal names, in the order the insert writes them.
func mapWriteError(err error, dupField string, fks ...string) error {
	var pg interface{ SQLState() string }
	if !errors.As(err, &pg) {
		return err
	}
	field := "vendor_id"
	if len(fks) > 0 {
		field = fks[0]
	}
	switch pg.SQLState() {
	case "23505":
		dup := httpx.FieldError{Field: dupField, Message: "already used"}
		if dupField == "" {
			return httpx.Duplicate("a record this request would create already exists")
		}
		return httpx.Duplicate("a record this request would create already exists", dup)
	case "23503":
		return httpx.BadRequest("a referenced record does not exist",
			httpx.FieldError{Field: field, Message: "no such record"})
	}
	return err
}
