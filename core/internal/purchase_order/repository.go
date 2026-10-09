// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Repository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *Repository {
	return &Repository{db: db}
}

// ErrNotFound is the repository's missing row; the service maps it to the
// boundary's 404.
var ErrNotFound = errors.New("purchase order not found")

// ErrStaleRevision is a conditional write that matched no row on its
// revision; the service maps it to ADR 0001 section 11's 409.
var ErrStaleRevision = errors.New("purchase order revision is stale")

// mapWriteError turns a database write refusal into the boundary error, so
// a bad reference is a 400 naming the request field, never a 500.
func mapWriteError(err error, field string) error {
	if err == nil {
		return nil
	}
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		switch pgErr.SQLState() {
		case "23503": // foreign_key_violation
			return httpx.BadRequest(field+" names a record that does not exist",
				httpx.FieldError{Field: field, Message: "names a record that does not exist"})
		case "23505": // unique_violation
			return httpx.Duplicate("a record with this value already exists",
				httpx.FieldError{Field: field, Message: "already exists"})
		}
	}
	return err
}

// storageStatus maps the wire's lowercase status back to the CHECK
// vocabulary. An unknown value keeps its own spelling and the column
// refuses it.
func storageStatus(wire string) string {
	switch wire {
	case StatusDraft.Wire():
		return string(StatusDraft)
	case StatusSent.Wire():
		return string(StatusSent)
	case StatusPartialReceive.Wire():
		return string(StatusPartialReceive)
	case StatusReceived.Wire():
		return string(StatusReceived)
	case StatusCancelled.Wire():
		return string(StatusCancelled)
	}
	return wire
}

// storageSource maps the wire's lowercase source to migration 055's
// vocabulary.
func storageSource(wire string) string {
	switch wire {
	case SourceManual.Wire():
		return string(SourceManual)
	case SourceReorder.Wire():
		return string(SourceReorder)
	case SourceSpecialOrder.Wire():
		return string(SourceSpecialOrder)
	case SourceA2A.Wire():
		return string(SourceA2A)
	}
	return wire
}

// CreatePO inserts a purchase order, minting its PO- number through the
// caller's transaction (ADR 0001 section 8).
func (r *Repository) CreatePO(ctx context.Context, po *PurchaseOrder) error {
	if po.Source == "" {
		po.Source = SourceManual.Wire()
	}
	sourceValue := storageSource(po.Source)
	// branch_id falls back to the caller's context branch, then the default.
	var branchArg any
	if po.BranchID != uuid.Nil {
		branchArg = po.BranchID
	} else if bid := middleware.BranchIDForQuery(ctx); bid != nil {
		branchArg = *bid
		po.BranchID = *bid
	}
	if po.Currency == "" {
		po.Currency = "USD"
	}
	number, err := httpx.NextDocumentNumber(ctx, r.db.GetExecutor(ctx), "purchase_order_number_seq", "PO", httpx.DefaultDocNumberWidth)
	if err != nil {
		return err
	}
	po.Number = number
	query := `
		INSERT INTO purchase_orders (id, number, vendor_id, status, source, branch_id, currency)
		VALUES ($1, $2, $3, $4, $5,
			COALESCE($6::uuid, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')),
			$7)
		RETURNING created_at, updated_at, branch_id, revision
	`
	var created, updated time.Time
	err = r.db.GetExecutor(ctx).QueryRow(ctx, query,
		po.ID, po.Number, po.VendorID, storageStatus(po.Status), sourceValue, branchArg, po.Currency,
	).Scan(&created, &updated, &po.BranchID, &po.Revision)
	if err == nil {
		po.CreatedAt, po.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	}
	return mapWriteError(err, "vendor_id")
}

// lineColumns is the line read shared by the document and the reorder
// dedup. The unit columns are NULL on a line no product names (a raw SQL
// writer's row); they read as the empty string.
// The scaled columns read as text and parse through the package's fixed
// scale helpers, never through float64.
const lineColumns = `
	id, po_id, product_id, description, quantity::text, COALESCE(qty_received, 0)::text,
	unit_cost::text, COALESCE(uom, ''), COALESCE(price_uom, ''), uom_qty::text, price_uom_qty::text,
	stock_uom, stock_quantity::text, line_total::text, position, linked_so_line_id`

func scanLine(scan func(dest ...any) error) (*PurchaseOrderLine, error) {
	var l PurchaseOrderLine
	var quantity, qtyReceived, uomQty, priceUOMQty, unitCost, lineTotal string
	var stockQty *string
	err := scan(&l.ID, &l.POID, &l.ProductID, &l.Description,
		&quantity, &qtyReceived, &unitCost, &l.UOM, &l.PriceUOM,
		&uomQty, &priceUOMQty, &l.StockUOM, &stockQty,
		&lineTotal, &l.Position, &l.LinkedSOLineID)
	if err != nil {
		return nil, err
	}
	if l.Quantity, err = httpx.ParseQuantity(quantity); err != nil {
		return nil, err
	}
	if l.QtyReceived, err = httpx.ParseQuantity(qtyReceived); err != nil {
		return nil, err
	}
	if l.UOMQty, err = httpx.ParseQuantity(uomQty); err != nil {
		return nil, err
	}
	if l.PriceUOMQty, err = httpx.ParseQuantity(priceUOMQty); err != nil {
		return nil, err
	}
	price, err := httpx.ParsePrice(unitCost)
	if err != nil {
		return nil, err
	}
	l.UnitCostTenThousandths = price
	cents, err := httpx.ParseCents(lineTotal)
	if err != nil {
		return nil, err
	}
	l.LineTotalCents = cents
	if stockQty != nil {
		q, err := httpx.ParseQuantity(*stockQty)
		if err != nil {
			return nil, err
		}
		l.StockQuantity = &q
	}
	return &l, nil
}

// AddPOLine inserts a purchase line in the wire's line shape. The unit
// fields and the pair are the caller's (the service's hold settles them);
// line_total is the line's own extension.
func (r *Repository) AddPOLine(ctx context.Context, line *PurchaseOrderLine) error {
	total, err := httpx.Extend(line.Quantity, line.UOMQty, line.PriceUOMQty, line.UnitCostTenThousandths)
	if err != nil {
		return err
	}
	line.LineTotalCents = total
	query := `
		INSERT INTO purchase_order_lines (id, po_id, product_id, description, quantity, unit_cost,
			uom, price_uom, uom_qty, price_uom_qty, stock_uom, stock_quantity, position, line_total, linked_so_line_id)
		VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric,
			$7, $8, $9::numeric, $10::numeric, $11, $12::numeric, $13, $14::numeric, $15)
	`
	var stockUOM any
	if line.StockUOM != nil && *line.StockUOM != "" {
		stockUOM = *line.StockUOM
	}
	var stockQty any
	if line.StockQuantity != nil {
		stockQty = line.StockQuantity.DecimalString()
	}
	_, err = r.db.GetExecutor(ctx).Exec(ctx, query,
		line.ID, line.POID, line.ProductID, line.Description, line.Quantity.DecimalString(),
		line.UnitCostTenThousandths.DecimalString(),
		nullString(line.UOM), nullString(line.PriceUOM), line.UOMQty.DecimalString(),
		line.PriceUOMQty.DecimalString(), stockUOM, stockQty, line.Position,
		centsNumeric(line.LineTotalCents), line.LinkedSOLineID)
	return mapWriteError(err, "lines")
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func centsNumeric(c httpx.Cents) string {
	v := int64(c)
	return fmt.Sprintf("%d.%02d", v/100, ((v%100)+100)%100)
}

// GetPOLineByProduct returns the existing line for a product on a PO, or
// (nil, nil) if there is none. Used to dedup auto-reorder lines.
func (r *Repository) GetPOLineByProduct(ctx context.Context, poID, productID uuid.UUID) (*PurchaseOrderLine, error) {
	query := `SELECT ` + lineColumns + `
		FROM purchase_order_lines
		WHERE po_id = $1 AND product_id = $2
		LIMIT 1
	`
	line, err := scanLine(r.db.GetExecutor(ctx).QueryRow(ctx, query, poID, productID).Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get PO line by product: %w", err)
	}
	return line, nil
}

// UpdatePOLineQuantity sets the quantity for an existing reorder line,
// recomputing its extension.
func (r *Repository) UpdatePOLineQuantity(ctx context.Context, lineID uuid.UUID, quantity httpx.Quantity, cost httpx.Price, uomQty, priceUOMQty httpx.Quantity) error {
	total, err := httpx.Extend(quantity, uomQty, priceUOMQty, cost)
	if err != nil {
		return err
	}
	_, err = r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE purchase_order_lines SET quantity = $1::numeric, line_total = $2::numeric WHERE id = $3`,
		quantity.DecimalString(), centsNumeric(total), lineID)
	if err != nil {
		return fmt.Errorf("failed to update PO line quantity: %w", err)
	}
	return nil
}

// GetDraftPOByVendor returns the vendor's open draft for the caller's
// branch, or (nil, nil) when there is none.
func (r *Repository) GetDraftPOByVendor(ctx context.Context, vendorID *uuid.UUID) (*PurchaseOrder, error) {
	if vendorID == nil {
		return nil, fmt.Errorf("vendor_id required lookup")
	}

	branchID := middleware.BranchIDForQuery(ctx)
	query := `
		SELECT id, number, vendor_id, status, source, currency, revision, branch_id, created_at, updated_at, sent_at
		FROM purchase_orders
		WHERE vendor_id = $1 AND status = 'DRAFT'
		  AND ($2::uuid IS NULL OR branch_id = $2)
		ORDER BY created_at, id
		LIMIT 1
	`
	po, err := scanPOHeader(r.db.GetExecutor(ctx).QueryRow(ctx, query, vendorID, branchID).Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return po, nil
}

const summaryColumns = `
	po.id, po.number, po.vendor_id, po.status, po.source, po.currency, po.revision,
	po.branch_id, po.created_at, po.updated_at, po.sent_at,
	COUNT(pol.id) AS line_count,
	COALESCE(SUM(pol.line_total), 0)::text AS total_cents`

func scanSummary(scan func(dest ...any) error) (PurchaseOrderSummary, error) {
	var s PurchaseOrderSummary
	var status, source, total string
	var created, updated time.Time
	var sentAt *time.Time
	err := scan(&s.ID, &s.Number, &s.VendorID, &status, &source, &s.Currency, &s.Revision,
		&s.BranchID, &created, &updated, &sentAt, &s.LineCount, &total)
	if err != nil {
		return s, err
	}
	if s.TotalCents, err = httpx.ParseCents(total); err != nil {
		return s, err
	}
	s.Status = Status(status).Wire()
	s.Source = Source(source).Wire()
	s.CreatedAt, s.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	s.SentAt = httpx.PtrTimestamp(sentAt)
	return s, nil
}

func scanPOHeader(scan func(dest ...any) error) (*PurchaseOrder, error) {
	var po PurchaseOrder
	var status, source string
	var created, updated time.Time
	var sentAt *time.Time
	err := scan(&po.ID, &po.Number, &po.VendorID, &status, &source, &po.Currency, &po.Revision,
		&po.BranchID, &created, &updated, &sentAt)
	if err != nil {
		return nil, err
	}
	po.Status = Status(status).Wire()
	po.Source = Source(source).Wire()
	po.CreatedAt, po.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	po.SentAt = httpx.PtrTimestamp(sentAt)
	return &po, nil
}

// poListFilters is the shared predicate of the list and its count: the
// three arm branch predicate (ADR 0008 section 11, the list form of the
// record rule, the quotes list's shape): a context branch lists its own
// purchase orders; with no context branch a bound non-admin user lists the
// branches granted to the user, none granted listing none; an administrator
// without a header, an unbound key and the single-branch switch list every
// branch's.
const poListFilters = `
	WHERE (
	    ($1::uuid IS NOT NULL AND po.branch_id = $1)
	    OR ($1::uuid IS NULL AND $%d::text IS NOT NULL AND po.branch_id IN
	        (SELECT branch_id FROM user_locations WHERE user_sub = $%d))
	    OR ($1::uuid IS NULL AND $%d::text IS NULL)
	  )
	  AND (cardinality($2::text[]) = 0 OR po.status::text = ANY($2))
	  AND ($3::uuid IS NULL OR po.vendor_id = $3)`

func statusStrings(statuses []Status) []string {
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = string(s)
	}
	return out
}

// ListFilter is the purchase order list's filters beside the keyset
// position.
type ListFilter struct {
	Statuses  []Status
	VendorID  *uuid.UUID
	AfterTime *time.Time
	AfterID   *uuid.UUID
	Limit     int
}

// ListPOsPage reads one page of the list: the filters, the three arm branch
// wall, and the keyset position, LIMIT limit+1 so the caller knows whether
// another page exists.
func (r *Repository) ListPOsPage(ctx context.Context, f ListFilter) ([]PurchaseOrderSummary, bool, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+summaryColumns+`
		FROM purchase_orders po
		LEFT JOIN purchase_order_lines pol ON pol.po_id = po.id`+fmt.Sprintf(poListFilters, 8, 8, 8)+`
		  AND ($4::timestamptz IS NULL OR (po.created_at, po.id) < ($4, $5::uuid))
		GROUP BY po.id
		ORDER BY po.created_at DESC, po.id DESC
		LIMIT $6`,
		middleware.BranchIDForQuery(ctx), statusStrings(f.Statuses), f.VendorID, f.AfterTime, f.AfterID, f.Limit+1,
		middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, false, fmt.Errorf("failed to list POs: %w", err)
	}
	defer rows.Close()
	out := []PurchaseOrderSummary{}
	for rows.Next() {
		s, err := scanSummary(rows.Scan)
		if err != nil {
			return nil, false, fmt.Errorf("failed to scan PO: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > f.Limit
	if more {
		out = out[:f.Limit]
	}
	return out, more, nil
}

// CountPOs counts the filtered set (include=total).
func (r *Repository) CountPOs(ctx context.Context, f ListFilter) (int64, error) {
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT count(*) FROM purchase_orders po`+fmt.Sprintf(poListFilters, 4, 4, 4),
		middleware.BranchIDForQuery(ctx), statusStrings(f.Statuses), f.VendorID,
		middleware.GrantsSubForQuery(ctx)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count POs: %w", err)
	}
	return n, nil
}

// GetSourceSummary returns a count of POs grouped by source, on the wire's
// lowercase vocabulary. Drives the "% of replenishments automated" KPI on
// the purchasing dashboard.
func (r *Repository) GetSourceSummary(ctx context.Context) (map[string]int, error) {
	query := `SELECT source, COUNT(*) FROM purchase_orders GROUP BY source`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query source summary: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			return nil, fmt.Errorf("failed to scan source summary row: %w", err)
		}
		out[Source(source).Wire()] = count
	}
	return out, rows.Err()
}

// GetPO reads the full document, its lines in (position, created_at, id)
// order. The branch wall applies (the record rule: a bound caller's own
// branches only).
func (r *Repository) GetPO(ctx context.Context, id uuid.UUID) (*PurchaseOrder, error) {
	branchID := middleware.BranchIDForQuery(ctx)
	query := `SELECT id, number, vendor_id, status, source, currency, revision, branch_id, created_at, updated_at, sent_at
		FROM purchase_orders WHERE id = $1 AND ($2::uuid IS NULL OR branch_id = $2)`
	po, err := scanPOHeader(r.db.GetExecutor(ctx).QueryRow(ctx, query, id, branchID).Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get PO header: %w", err)
	}

	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+lineColumns+` FROM purchase_order_lines WHERE po_id = $1 ORDER BY position, created_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get PO lines: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		line, err := scanLine(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("failed to scan PO line: %w", err)
		}
		po.Lines = append(po.Lines, *line)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	po.LineCount = len(po.Lines)
	for _, l := range po.Lines {
		po.TotalCents += l.LineTotalCents
	}
	return po, nil
}

// LockPO takes the purchase order row FOR UPDATE and reads it: the receive
// and the submit check status and revision under the lock, never on a read
// before it (ADR 0008 section 4 step 1). Must run inside a transaction.
func (r *Repository) LockPO(ctx context.Context, id uuid.UUID) (*PurchaseOrder, error) {
	query := `SELECT id, number, vendor_id, status, source, currency, revision, branch_id, created_at, updated_at, sent_at
		FROM purchase_orders WHERE id = $1 FOR UPDATE`
	po, err := scanPOHeader(r.db.GetExecutor(ctx).QueryRow(ctx, query, id).Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock PO: %w", err)
	}
	return po, nil
}

// LoadLines reads the purchase order's lines without its header, for the
// acts that already hold the lock.
func (r *Repository) LoadLines(ctx context.Context, poID uuid.UUID) ([]PurchaseOrderLine, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+lineColumns+` FROM purchase_order_lines WHERE po_id = $1 ORDER BY position, created_at, id`, poID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PurchaseOrderLine
	for rows.Next() {
		line, err := scanLine(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *line)
	}
	return out, rows.Err()
}

// GetPOBranch returns the branch the purchase order belongs to, or nil when
// no such purchase order exists. Unlike GetPO it never filters by the
// caller's branch context: the branch wall needs the record's own branch to
// hold it against.
func (r *Repository) GetPOBranch(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
	var branch *uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT branch_id FROM purchase_orders WHERE id = $1`, id).Scan(&branch)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get PO branch: %w", err)
	}
	return branch, nil
}

// UpdatePOStatus is the submit's conditional write: the revision check and
// the write are one database act (ADR 0001 section 11). The caller holds
// the row lock and has already resolved the precondition, so zero rows is
// the stale case.
func (r *Repository) UpdatePOStatus(ctx context.Context, id uuid.UUID, status Status, sentAt *time.Time, fromRevision int64) error {
	var revision int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`UPDATE purchase_orders SET status = $2, sent_at = COALESCE($3, sent_at),
			revision = revision + 1, updated_at = NOW()
		 WHERE id = $1 AND revision = $4 RETURNING revision`,
		id, status, sentAt, fromRevision).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStaleRevision
	}
	return err
}

// SetPOStatus writes the derived status of a receive (partial or received)
// and moves the revision with it.
func (r *Repository) SetPOStatus(ctx context.Context, id uuid.UUID, status Status) error {
	var revision int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`UPDATE purchase_orders SET status = $2, revision = revision + 1, updated_at = NOW()
		 WHERE id = $1 RETURNING revision`, id, status).Scan(&revision)
	return err
}

// UpdateLineReceived sets the received quantity of a line, in the wire's
// scale 4.
func (r *Repository) UpdateLineReceived(ctx context.Context, lineID uuid.UUID, qtyReceived httpx.Quantity) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE purchase_order_lines SET qty_received = $1::numeric WHERE id = $2`,
		qtyReceived.DecimalString(), lineID)
	return err
}

// OverReceiptPercent reads the purchasing.over_receipt_percent setting; the
// default is 0: a line's total received is at most its ordered quantity
// (ADR 0008 section 4).
func (r *Repository) OverReceiptPercent(ctx context.Context) (string, error) {
	var pct *string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT value FROM system_settings WHERE key = 'purchasing.over_receipt_percent'`).Scan(&pct)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if pct == nil || *pct == "" {
		return "0", nil
	}
	return *pct, nil
}

// ReorderRun records one execution of an auto-reorder scheduler job.
// Lifecycle: insert with status=RUNNING on entry; update with finished_at,
// status, counts, and error_message on exit. See migration 056.
type ReorderRun struct {
	ID              uuid.UUID  `json:"id"`
	Job             string     `json:"job"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	DryRun          bool       `json:"dry_run"`
	Status          string     `json:"status"`
	POsCreated      int        `json:"pos_created"`
	ProductsUpdated int        `json:"products_updated"`
	ProductsSkipped int        `json:"products_skipped"`
	ErrorMessage    string     `json:"error_message,omitempty"`
}

// StartReorderRun inserts a row with status='RUNNING' and returns the row id.
// The scheduler later calls FinishReorderRun to stamp the outcome.
func (r *Repository) StartReorderRun(ctx context.Context, job string, dryRun bool) (uuid.UUID, error) {
	const q = `
		INSERT INTO reorder_runs (job, dry_run, status)
		VALUES ($1, $2, 'RUNNING')
		RETURNING id
	`
	var id uuid.UUID
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, q, job, dryRun).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("insert reorder_runs: %w", err)
	}
	return id, nil
}

// FinishReorderRun stamps the outcome of a reorder-run row.
func (r *Repository) FinishReorderRun(ctx context.Context, id uuid.UUID, status string, posCreated, productsUpdated, productsSkipped int, errMsg string) error {
	const q = `
		UPDATE reorder_runs
		SET finished_at = now(),
		    status = $2,
		    pos_created = $3,
		    products_updated = $4,
		    products_skipped = $5,
		    error_message = NULLIF($6, '')
		WHERE id = $1
	`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, q, id, status, posCreated, productsUpdated, productsSkipped, errMsg)
	return err
}

// ListReorderRuns returns the most recent N rows for the operator dashboard.
func (r *Repository) ListReorderRuns(ctx context.Context, limit int) ([]ReorderRun, error) {
	if limit <= 0 {
		limit = 50
	}
	const q = `
		SELECT id, job, started_at, finished_at, dry_run, status,
		       pos_created, products_updated, products_skipped,
		       COALESCE(error_message, '')
		FROM reorder_runs
		ORDER BY started_at DESC
		LIMIT $1
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("query reorder_runs: %w", err)
	}
	defer rows.Close()

	var out []ReorderRun
	for rows.Next() {
		var rr ReorderRun
		if err := rows.Scan(&rr.ID, &rr.Job, &rr.StartedAt, &rr.FinishedAt,
			&rr.DryRun, &rr.Status, &rr.POsCreated, &rr.ProductsUpdated,
			&rr.ProductsSkipped, &rr.ErrorMessage); err != nil {
			return nil, fmt.Errorf("scan reorder_run: %w", err)
		}
		out = append(out, rr)
	}
	return out, rows.Err()
}

// LocationBranch is the branch a location belongs to (locations.branch_id):
// the branch a receipt into it put the stock in. False when the location has
// no branch of its own.
func (r *Repository) LocationBranch(ctx context.Context, id uuid.UUID) (uuid.UUID, bool, error) {
	var branch *uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT branch_id FROM locations WHERE id = $1`, id).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && branch == nil) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to read the location's branch: %w", err)
	}
	return *branch, true, nil
}

// InsertA2ALog claims an A2A idempotency key: the row is the transaction's
// first write, ON CONFLICT DO NOTHING, so concurrent webhooks converge on
// one winner (ADR 0008 section 11). It reports whether this call claimed
// the key.
func (r *Repository) InsertA2ALog(ctx context.Context, key, eventType string, payload json.RawMessage, traceID string) (bool, error) {
	var id uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO a2a_inbound_po_log (idempotency_key, event_type, payload, trace_id, received_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id`, key, eventType, payload, traceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SetA2ALogPO stamps the purchase order the claimed key created.
func (r *Repository) SetA2ALogPO(ctx context.Context, key string, poID uuid.UUID) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE a2a_inbound_po_log SET created_po_id = $2 WHERE idempotency_key = $1`, key, poID)
	return err
}

// ProductStockingUnit reads a product's stocking unit, for the create's
// unit hold and the reorder payload.
func (r *Repository) ProductStockingUnit(ctx context.Context, id uuid.UUID) (uom string, err error) {
	err = r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT uom_primary::text FROM products WHERE id = $1`, id).Scan(&uom)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return uom, err
}
