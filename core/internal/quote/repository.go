// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

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
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is the repository's answer for a quote that does not exist or
// is outside the caller's branch wall. The service maps it to 404.
var ErrNotFound = errors.New("quote not found")

// ListFilter is a list route's filters and keyset position. After is the
// last row of the previous page; the list resumes strictly past it in
// (created_at, id) descending order.
type ListFilter struct {
	Statuses   []QuoteState
	CustomerID *uuid.UUID
	AfterTime  *time.Time
	AfterID    uuid.UUID
	Limit      int
}

// ProductRef is the slice of a product a quote line defaults from.
type ProductRef struct {
	ID          uuid.UUID
	SKU         string
	Description string
	UOMPrimary  string
}

// Repository is the quote store. Every method reads and writes through the
// context's executor, so inside a transaction (Service wraps the multi step
// writes in one) it never reaches for a second pool connection.
type Repository interface {
	NextNumber(ctx context.Context) (string, error)
	LookupProducts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]ProductRef, error)
	InsertQuote(ctx context.Context, q *Quote) error
	GetQuote(ctx context.Context, id uuid.UUID) (*Quote, error)
	// LockQuote takes the quote's row lock for the rest of the transaction,
	// so a revision check and the write after it are one act.
	LockQuote(ctx context.Context, id uuid.UUID) error
	ReplaceDraft(ctx context.Context, q *Quote) error
	SetStatus(ctx context.Context, q *Quote) error
	ListQuotes(ctx context.Context, f ListFilter) ([]QuoteSummary, error)
	CountQuotes(ctx context.Context, f ListFilter) (int64, error)
	ListQuotesByCustomer(ctx context.Context, customerID uuid.UUID) ([]QuoteSummary, error)
	GetQuoteAnalytics(ctx context.Context) (*QuoteAnalytics, error)
	GetOriginalFile(ctx context.Context, id uuid.UUID) ([]byte, string, string, error)
}

type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// NextNumber mints the next document number through the caller's executor,
// so a create that rolls back abandons it (ADR 0001 section 8).
func (r *PostgresRepository) NextNumber(ctx context.Context) (string, error) {
	return httpx.NextDocumentNumber(ctx, r.db.GetExecutor(ctx), "quote_number_seq", "Q", httpx.DefaultDocNumberWidth)
}

func (r *PostgresRepository) LookupProducts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]ProductRef, error) {
	out := make(map[uuid.UUID]ProductRef, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT id, sku, description, uom_primary::text FROM products WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to look up products: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p ProductRef
		if err := rows.Scan(&p.ID, &p.SKU, &p.Description, &p.UOMPrimary); err != nil {
			return nil, fmt.Errorf("failed to scan product: %w", err)
		}
		out[p.ID] = p
	}
	return out, rows.Err()
}

// nullIfEmpty renders an empty string as a SQL NULL.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// lineProductID renders a line's product reference for a parameterised
// INSERT. A special order line (a thing the dealer does not stock) has no
// product, and its NULL must stay NULL: the all zeros UUID trips
// quote_lines_product_id_fkey.
func lineProductID(l *QuoteLine) any {
	if l.ProductID == nil || *l.ProductID == uuid.Nil {
		return nil
	}
	return *l.ProductID
}

// referenceFields maps the foreign keys a quote write can violate to the
// request field that named the missing record, so a bad reference is a 400
// naming it and not a 500 from the database.
var referenceFields = map[string]string{
	"quotes_customer_id_fkey":     "customer_id",
	"quotes_project_id_fkey":      "job_id",
	"quotes_vehicle_id_fkey":      "vehicle_id",
	"quotes_branch_id_fkey":       "branch_id",
	"quote_lines_product_id_fkey": "lines.product_id",
}

func validationFailed(msg string, details ...httpx.FieldError) *httpx.Error {
	return &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed, Message: msg, Details: details}
}

func mapWriteError(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23503":
			if field, ok := referenceFields[pgErr.ConstraintName]; ok {
				return validationFailed("a referenced record does not exist",
					httpx.FieldError{Field: field, Message: "no such record"})
			}
		case pgErr.Code == "23505" && pgErr.ConstraintName == "quote_lines_pkey":
			return validationFailed("a line id belongs to another document",
				httpx.FieldError{Field: "lines.id", Message: "is already used by a line of another quote"})
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// InsertQuote writes the header and its lines. The caller has priced the
// document and minted its number; the revision starts at 1.
func (r *PostgresRepository) InsertQuote(ctx context.Context, q *Quote) error {
	exec := r.db.GetExecutor(ctx)

	// branch_id falls back to the request's branch, then the deployment default.
	var branchArg any
	if q.BranchID != uuid.Nil {
		branchArg = q.BranchID
	} else if bid := middleware.BranchIDForQuery(ctx); bid != nil {
		branchArg = *bid
	}
	_, err := exec.Exec(ctx, `
		INSERT INTO quotes (
			id, number, customer_id, project_id, state, total_amount, expires_at, created_at, updated_at,
			margin_total, source, original_file, original_filename, original_content_type, parse_map,
			delivery_type, freight_amount, vehicle_id, branch_id, revision
		) VALUES ($1, $2, $3, $4, $5, $6::numeric / 100, $7, $8, $8,
			$9::numeric / 100, $10, $11, $12, $13, $14,
			$15, $16::numeric / 100, $17,
			COALESCE($18::uuid, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')), 1)`,
		q.ID, q.Number, q.CustomerID, q.JobID, string(q.Status), int64(q.TotalCents), tsArg(q.ExpiresAt), q.CreatedAt.Time,
		int64(q.MarginTotalCents), q.Source, q.OriginalFile, nullIfEmpty(derefStr(q.OriginalFilename)),
		nullIfEmpty(derefStr(q.OriginalContentType)), []byte(q.ParseMap),
		string(q.DeliveryType), int64(q.FreightCents), q.VehicleID, branchArg,
	)
	if err != nil {
		return mapWriteError(err, "failed to insert quote header")
	}
	return r.insertLines(ctx, q, nil)
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func tsArg(t *httpx.Timestamp) any {
	if t == nil {
		return nil
	}
	return t.Time
}

// insertLines writes q.Lines in order. priorNotes carries the customer notes
// of lines whose id survives an edit.
func (r *PostgresRepository) insertLines(ctx context.Context, q *Quote, priorNotes map[uuid.UUID]string) error {
	exec := r.db.GetExecutor(ctx)
	for i := range q.Lines {
		line := &q.Lines[i]
		note := ""
		if line.CustomerNote != nil {
			note = *line.CustomerNote
		}
		if note == "" {
			note = priorNotes[line.ID]
		}
		_, err := exec.Exec(ctx, `
			INSERT INTO quote_lines (
				id, quote_id, product_id, sku, description, customer_note,
				quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, line_total, position, created_at
			) VALUES ($1, $2, $3, $4, $5, $6,
				$7::numeric / 10000, $8, $9, $10::numeric / 10000, $11::numeric / 10000, $12::numeric / 10000, $13::numeric / 100, $14, $15)`,
			line.ID, line.QuoteID, lineProductID(line), line.SKU, line.Description, nullIfEmpty(note),
			int64(line.Quantity), string(line.UOM), line.PriceUOM, int64(line.UOMQty), int64(line.PriceUOMQty),
			int64(line.UnitPrice), int64(line.LineTotal), i, line.CreatedAt.Time,
		)
		if err != nil {
			return mapWriteError(err, "failed to insert quote line")
		}
	}
	return nil
}

// summaryColumns and summaryFrom are the one header projection: a list item,
// a partner list row, and the head of the full document all read it.
const summaryColumns = `
	q.id, q.number, q.branch_id, q.customer_id, COALESCE(c.name, ''), q.project_id, q.state::text, q.revision,
	ROUND(q.total_amount * 100)::bigint, ROUND(COALESCE(q.freight_amount, 0) * 100)::bigint,
	ROUND(COALESCE(q.margin_total, 0) * 100)::bigint,
	COALESCE(q.delivery_type, 'PICKUP'), q.vehicle_id, v.name, COALESCE(q.source, 'manual'),
	q.expires_at, q.sent_at, q.accepted_at, q.rejected_at, q.created_at, COALESCE(q.updated_at, q.created_at)`

const summaryFrom = `
	FROM quotes q
	LEFT JOIN customers c ON c.id = q.customer_id
	LEFT JOIN vehicles v ON v.id = q.vehicle_id`

// scanSummary scans summaryColumns, then any extra destinations, from one row.
func scanSummary(row pgx.Row, s *QuoteSummary, extra ...any) error {
	var (
		status, delivery                  string
		total, freight, margin            int64
		expires, sent, accepted, rejected *time.Time
		created, updated                  time.Time
	)
	dest := append([]any{
		&s.ID, &s.Number, &s.BranchID, &s.CustomerID, &s.CustomerName, &s.JobID, &status, &s.Revision,
		&total, &freight, &margin,
		&delivery, &s.VehicleID, &s.VehicleName, &s.Source,
		&expires, &sent, &accepted, &rejected, &created, &updated,
	}, extra...)
	if err := row.Scan(dest...); err != nil {
		return err
	}
	s.Status = QuoteState(status)
	s.DeliveryType = DeliveryType(delivery)
	s.TotalCents, s.FreightCents, s.MarginTotalCents = httpx.Cents(total), httpx.Cents(freight), httpx.Cents(margin)
	s.ExpiresAt, s.SentAt, s.AcceptedAt, s.RejectedAt = httpx.PtrTimestamp(expires), httpx.PtrTimestamp(sent), httpx.PtrTimestamp(accepted), httpx.PtrTimestamp(rejected)
	s.CreatedAt, s.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

func (r *PostgresRepository) GetQuote(ctx context.Context, id uuid.UUID) (*Quote, error) {
	q := &Quote{}
	var (
		exposureState string
		exposureCents int64
		exposureAt    *time.Time
		parseMap      []byte
	)
	row := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT `+summaryColumns+`,
			q.original_filename, q.original_content_type, q.parse_map,
			COALESCE(q.exposure_state, 'OK'), ROUND(COALESCE(q.exposure_dollars, 0) * 100)::bigint, q.exposure_last_checked_at`+
		summaryFrom+`
		WHERE q.id = $1
		  AND ($2::uuid IS NULL OR q.branch_id = $2)`, id, middleware.BranchIDForQuery(ctx))
	if err := scanSummary(row, &q.QuoteSummary, &q.OriginalFilename, &q.OriginalContentType, &parseMap,
		&exposureState, &exposureCents, &exposureAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get quote header: %w", err)
	}
	q.ParseMap = parseMap
	q.ExposureState = ExposureState(exposureState)
	q.ExposureCents = httpx.Cents(exposureCents)
	q.ExposureLastCheckedAt = httpx.PtrTimestamp(exposureAt)

	// Lines, with the product's average cost for the margin basis.
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT ql.id, ql.quote_id, ql.product_id, ql.sku, ql.description, NULLIF(ql.customer_note, ''),
		       ROUND(ql.quantity * 10000)::bigint, ql.uom::text, COALESCE(ql.price_uom, ql.uom::text),
		       ROUND(ql.uom_qty * 10000)::bigint, ROUND(ql.price_uom_qty * 10000)::bigint,
		       ROUND(ql.unit_price * 10000)::bigint, ROUND(COALESCE(p.average_unit_cost, 0) * 10000)::bigint,
		       ROUND(ql.line_total * 100)::bigint, ql.created_at
		FROM quote_lines ql
		LEFT JOIN products p ON p.id = ql.product_id
		WHERE ql.quote_id = $1
		ORDER BY ql.position, ql.created_at, ql.id`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get quote lines: %w", err)
	}
	defer rows.Close()

	q.Lines = []QuoteLine{}
	for rows.Next() {
		var (
			l                                            QuoteLine
			uom                                          string
			qty, uomQty, priceUomQty, price, cost, total int64
			created                                      time.Time
		)
		if err := rows.Scan(&l.ID, &l.QuoteID, &l.ProductID, &l.SKU, &l.Description, &l.CustomerNote,
			&qty, &uom, &l.PriceUOM, &uomQty, &priceUomQty, &price, &cost, &total, &created); err != nil {
			return nil, fmt.Errorf("failed to scan quote line: %w", err)
		}
		l.UOM = productUOM(uom)
		l.Quantity, l.UOMQty, l.PriceUOMQty = httpx.Quantity(qty), httpx.Quantity(uomQty), httpx.Quantity(priceUomQty)
		l.UnitPrice, l.UnitCost, l.LineTotal = httpx.Price(price), httpx.Price(cost), httpx.Cents(total)
		l.CreatedAt = httpx.TimestampOf(created)
		q.Lines = append(q.Lines, l)
	}
	return q, rows.Err()
}

func (r *PostgresRepository) LockQuote(ctx context.Context, id uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM quotes WHERE id = $1 AND ($2::uuid IS NULL OR branch_id = $2) FOR UPDATE`,
		id, middleware.BranchIDForQuery(ctx)).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock quote: %w", err)
	}
	return nil
}

// SetStatus writes a status change and its lifecycle timestamps and moves the
// revision. The caller holds the row lock (LockQuote).
func (r *PostgresRepository) SetStatus(ctx context.Context, q *Quote) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE quotes
		SET state = $2::quote_state, sent_at = $3, accepted_at = $4, rejected_at = $5,
			revision = revision + 1, updated_at = NOW()
		WHERE id = $1`,
		q.ID, string(q.Status), tsArg(q.SentAt), tsArg(q.AcceptedAt), tsArg(q.RejectedAt))
	if err != nil {
		return fmt.Errorf("failed to update quote status: %w", err)
	}
	return nil
}

// ReplaceDraft replaces the header and all lines and moves the revision. A
// line whose id survives keeps the customer note the edit did not resend.
func (r *PostgresRepository) ReplaceDraft(ctx context.Context, q *Quote) error {
	exec := r.db.GetExecutor(ctx)

	_, err := exec.Exec(ctx, `
		UPDATE quotes
		SET customer_id = $2, project_id = $3, total_amount = $4::numeric / 100, expires_at = $5,
			delivery_type = $6, freight_amount = $7::numeric / 100, vehicle_id = $8,
			revision = revision + 1, updated_at = NOW()
		WHERE id = $1`,
		q.ID, q.CustomerID, q.JobID, int64(q.TotalCents), tsArg(q.ExpiresAt),
		string(q.DeliveryType), int64(q.FreightCents), q.VehicleID)
	if err != nil {
		return mapWriteError(err, "failed to update quote header")
	}

	// Snapshot the customer notes BEFORE the delete. A portal originated line
	// carries the contractor's own words (migration 084); an ERP client that
	// does not resend them would otherwise wipe them by pricing the quote.
	priorNotes := make(map[uuid.UUID]string)
	noteRows, err := exec.Query(ctx,
		`SELECT id, COALESCE(customer_note, '') FROM quote_lines WHERE quote_id = $1`, q.ID)
	if err != nil {
		return fmt.Errorf("failed to read existing quote line notes: %w", err)
	}
	for noteRows.Next() {
		var id uuid.UUID
		var note string
		if err := noteRows.Scan(&id, &note); err != nil {
			noteRows.Close()
			return fmt.Errorf("failed to scan quote line note: %w", err)
		}
		priorNotes[id] = note
	}
	noteRows.Close()
	if err := noteRows.Err(); err != nil {
		return fmt.Errorf("quote line note rows error: %w", err)
	}

	if _, err := exec.Exec(ctx, "DELETE FROM quote_lines WHERE quote_id = $1", q.ID); err != nil {
		return fmt.Errorf("failed to delete old lines: %w", err)
	}
	return r.insertLines(ctx, q, priorNotes)
}

// listFilters is the shared predicate of the list and its count. The count
// ignores the keyset position: it is the size of the filtered set. The
// grants sub trails each query's own parameters, so its placeholder is
// filled per query (ADR 0007 section 2.3, the list form of the record rule):
// a context branch lists its own rows; with no context branch a bound
// non-admin user lists the branches granted to the user, none granted
// listing none; an administrator without a header, an unbound key, the
// single-branch switch and dev mode list every branch's.
const listFilters = `
	WHERE (
	    ($1::uuid IS NOT NULL AND q.branch_id = $1)
	    OR ($1::uuid IS NULL AND $%d::text IS NOT NULL AND q.branch_id IN
	        (SELECT branch_id FROM user_locations WHERE user_sub = $%d))
	    OR ($1::uuid IS NULL AND $%d::text IS NULL)
	  )
	  AND (cardinality($2::text[]) = 0 OR q.state::text = ANY($2))
	  AND ($3::uuid IS NULL OR q.customer_id = $3)`

func statusStrings(states []QuoteState) []string {
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = string(s)
	}
	return out
}

func (r *PostgresRepository) ListQuotes(ctx context.Context, f ListFilter) ([]QuoteSummary, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+summaryColumns+summaryFrom+fmt.Sprintf(listFilters, 7, 7, 7)+`
		  AND ($4::timestamptz IS NULL OR (q.created_at, q.id) < ($4, $5::uuid))
		ORDER BY q.created_at DESC, q.id DESC
		LIMIT $6`,
		middleware.BranchIDForQuery(ctx), statusStrings(f.Statuses), f.CustomerID, f.AfterTime, f.AfterID, f.Limit,
		middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to list quotes: %w", err)
	}
	defer rows.Close()
	return scanSummaries(rows)
}

func scanSummaries(rows pgx.Rows) ([]QuoteSummary, error) {
	out := []QuoteSummary{}
	for rows.Next() {
		var s QuoteSummary
		if err := scanSummary(rows, &s); err != nil {
			return nil, fmt.Errorf("failed to scan quote: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CountQuotes(ctx context.Context, f ListFilter) (int64, error) {
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT count(*) FROM quotes q`+fmt.Sprintf(listFilters, 4, 4, 4),
		middleware.BranchIDForQuery(ctx), statusStrings(f.Statuses), f.CustomerID,
		middleware.GrantsSubForQuery(ctx)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count quotes: %w", err)
	}
	return n, nil
}

func (r *PostgresRepository) ListQuotesByCustomer(ctx context.Context, customerID uuid.UUID) ([]QuoteSummary, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+summaryColumns+summaryFrom+`
		WHERE q.customer_id = $1
		  AND (
		    ($2::uuid IS NOT NULL AND q.branch_id = $2)
		    OR ($2::uuid IS NULL AND $3::text IS NOT NULL AND q.branch_id IN
		        (SELECT branch_id FROM user_locations WHERE user_sub = $3))
		    OR ($2::uuid IS NULL AND $3::text IS NULL)
		  )
		ORDER BY q.created_at DESC, q.id DESC`,
		customerID, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to list quotes: %w", err)
	}
	defer rows.Close()
	return scanSummaries(rows)
}

// GetQuoteWithLinesAndCustomer builds the narrow projection the pricing
// exposure snapshot/scanner needs: customer escalation policy (frozen at
// snapshot time) plus each line's commodity flag and resolved index override.
// Implements QuoteLineReader.
//
// Deliberately unscoped by branch: the exposure scanner and the nightly
// safety-net run as system processes with no branch context, and an
// index move affects a quote regardless of which branch issued it.
func (r *PostgresRepository) GetQuoteWithLinesAndCustomer(ctx context.Context, quoteID uuid.UUID) (*QuoteForSnapshot, error) {
	q := &QuoteForSnapshot{}
	headerQuery := `
		SELECT q.id, q.customer_id, COALESCE(c.name, ''), c.salesperson_id,
			COALESCE(c.price_escalation_policy, 'FLAG_FOR_REQUOTE'),
			COALESCE(c.escalation_threshold_pct, 5.0),
			c.escalation_agreement_signed_at,
			COALESCE(c.escalation_agreement_ref, '')
		FROM quotes q
		LEFT JOIN customers c ON c.id = q.customer_id
		WHERE q.id = $1
	`
	err := r.db.GetExecutor(ctx).QueryRow(ctx, headerQuery, quoteID).Scan(
		&q.ID, &q.CustomerID, &q.CustomerName, &q.SalespersonID,
		&q.CustomerPolicy.Policy, &q.CustomerPolicy.ThresholdPct,
		&q.CustomerPolicy.AgreementSignedAt, &q.CustomerPolicy.AgreementRef,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to load quote for snapshot: %w", err)
	}
	q.ShortID = q.ID.String()[:8]

	linesQuery := `
		SELECT ql.id, ql.product_id, ql.sku, ql.quantity, ql.uom_qty, ql.price_uom_qty, ql.unit_price,
			COALESCE(p.is_commodity, FALSE), p.market_index_id
		FROM quote_lines ql
		LEFT JOIN products p ON p.id = ql.product_id
		WHERE ql.quote_id = $1
		ORDER BY ql.created_at ASC
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, linesQuery, quoteID)
	if err != nil {
		return nil, fmt.Errorf("failed to load quote lines for snapshot: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l QuoteLineForSnapshot
		if err := rows.Scan(
			&l.ID, &l.ProductID, &l.SKU, &l.Quantity, &l.UOMQty, &l.PriceUOMQty, &l.UnitPrice,
			&l.IsCommodity, &l.MarketIndexID,
		); err != nil {
			return nil, fmt.Errorf("failed to scan quote line for snapshot: %w", err)
		}
		q.Lines = append(q.Lines, l)
	}
	return q, nil
}

// UpdateQuoteExposure writes the denormalized exposure rollup onto the quote
// header. Implements QuoteLineReader.
func (r *PostgresRepository) UpdateQuoteExposure(ctx context.Context, quoteID uuid.UUID, state string, dollars float64, lastCheckedAt time.Time) error {
	query := `
		UPDATE quotes
		SET exposure_state = $2, exposure_dollars = $3, exposure_last_checked_at = $4, updated_at = NOW()
		WHERE id = $1
	`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, quoteID, state, dollars, lastCheckedAt)
	if err != nil {
		return fmt.Errorf("failed to update quote exposure: %w", err)
	}
	return nil
}

// UpdateLineUnitPrice mutates a single line's unit price and recomputes its
// line total. Used by AUTO_ESCALATE. Implements QuoteLineReader.
func (r *PostgresRepository) UpdateLineUnitPrice(ctx context.Context, lineID uuid.UUID, newUnitPrice float64) error {
	query := `
		UPDATE quote_lines
		SET unit_price = $2, line_total = ROUND(quantity * $2 * price_uom_qty / uom_qty, 2)
		WHERE id = $1
	`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, lineID, newUnitPrice)
	if err != nil {
		return fmt.Errorf("failed to update line unit price: %w", err)
	}
	return nil
}

// RecomputeQuoteTotal re-sums the quote's line totals plus freight onto the
// header and moves the revision: an escalation rewrites prices under an
// editor, and the editor's next write must see a stale revision rather than
// overwrite them (ADR 0001 section 11). Implements QuoteLineReader.
func (r *PostgresRepository) RecomputeQuoteTotal(ctx context.Context, quoteID uuid.UUID) error {
	query := `
		UPDATE quotes q
		SET total_amount = COALESCE((
			SELECT SUM(ql.line_total) FROM quote_lines ql WHERE ql.quote_id = q.id
		), 0) + COALESCE(q.freight_amount, 0),
		revision = q.revision + 1,
		updated_at = NOW()
		WHERE q.id = $1
	`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, quoteID)
	if err != nil {
		return fmt.Errorf("failed to recompute quote total: %w", err)
	}
	return nil
}

// GetOriginalFile retrieves the original uploaded file for a quote.
func (r *PostgresRepository) GetOriginalFile(ctx context.Context, id uuid.UUID) ([]byte, string, string, error) {
	var data []byte
	var filename, contentType string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT original_file, COALESCE(original_filename, ''), COALESCE(original_content_type, '')
		FROM quotes WHERE id = $1 AND ($2::uuid IS NULL OR branch_id = $2)`,
		id, middleware.BranchIDForQuery(ctx)).Scan(&data, &filename, &contentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", "", ErrNotFound
	}
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to get original file: %w", err)
	}
	return data, filename, contentType, nil
}

// GetQuoteAnalytics returns aggregated quote analytics.
func (r *PostgresRepository) GetQuoteAnalytics(ctx context.Context) (*QuoteAnalytics, error) {
	a := &QuoteAnalytics{}

	// State counts and totals
	countQuery := `
		SELECT
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE state = 'DRAFT') as draft,
			COUNT(*) FILTER (WHERE state = 'SENT') as sent,
			COUNT(*) FILTER (WHERE state = 'ACCEPTED') as accepted,
			COUNT(*) FILTER (WHERE state = 'REJECTED') as rejected,
			COUNT(*) FILTER (WHERE state = 'EXPIRED') as expired,
			ROUND(COALESCE(SUM(total_amount), 0) * 100)::bigint as total_value,
			ROUND(COALESCE(SUM(total_amount) FILTER (WHERE state = 'ACCEPTED'), 0) * 100)::bigint as accepted_value,
			ROUND(COALESCE(AVG(COALESCE(margin_total, 0)) FILTER (WHERE state = 'ACCEPTED'), 0) * 100)::bigint as avg_margin_accepted,
			ROUND(COALESCE(AVG(COALESCE(margin_total, 0)) FILTER (WHERE state = 'REJECTED'), 0) * 100)::bigint as avg_margin_rejected,
			COUNT(*) FILTER (WHERE COALESCE(source, 'manual') = 'ai') as ai_count,
			COUNT(*) FILTER (WHERE COALESCE(source, 'manual') = 'ai' AND state = 'ACCEPTED') as ai_accepted,
			COUNT(*) FILTER (WHERE COALESCE(source, 'manual') != 'ai' AND state = 'ACCEPTED') as manual_accepted,
			COUNT(*) FILTER (WHERE COALESCE(source, 'manual') != 'ai') as manual_count
		FROM quotes
		WHERE created_at >= NOW() - INTERVAL '90 days'
	`
	var aiAccepted, manualAccepted, manualCount int
	err := r.db.GetExecutor(ctx).QueryRow(ctx, countQuery).Scan(
		&a.TotalQuotes, &a.DraftCount, &a.SentCount, &a.AcceptedCount, &a.RejectedCount, &a.ExpiredCount,
		&a.TotalQuoteValueCents, &a.TotalAcceptedCents,
		&a.AvgMarginAcceptedCents, &a.AvgMarginRejectedCents,
		&a.AISourcedCount, &aiAccepted, &manualAccepted, &manualCount,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get analytics counts: %w", err)
	}

	// Conversion rates
	closedCount := a.AcceptedCount + a.RejectedCount + a.ExpiredCount
	if closedCount > 0 {
		a.ConversionRate = float64(a.AcceptedCount) / float64(closedCount) * 100
	}
	if a.AISourcedCount > 0 {
		a.AIConversionRate = float64(aiAccepted) / float64(a.AISourcedCount) * 100
	}
	if manualCount > 0 {
		a.ManualConversionRate = float64(manualAccepted) / float64(manualCount) * 100
	}

	// Average days to close
	daysQuery := `
		SELECT COALESCE(AVG(EXTRACT(EPOCH FROM (accepted_at - created_at)) / 86400), 0)
		FROM quotes
		WHERE state = 'ACCEPTED' AND accepted_at IS NOT NULL
		AND created_at >= NOW() - INTERVAL '90 days'
	`
	err = r.db.GetExecutor(ctx).QueryRow(ctx, daysQuery).Scan(&a.AvgDaysToClose)
	if err != nil {
		a.AvgDaysToClose = 0
	}

	// Trend data (last 30 days)
	trendQuery := `
		SELECT
			d::date as date,
			COUNT(q.id) FILTER (WHERE q.id IS NOT NULL) as created,
			COUNT(q.id) FILTER (WHERE q.state = 'ACCEPTED') as accepted,
			COUNT(q.id) FILTER (WHERE q.state = 'REJECTED') as rejected,
			ROUND(COALESCE(SUM(q.total_amount), 0) * 100)::bigint as total_value,
			ROUND(COALESCE(SUM(q.total_amount) FILTER (WHERE q.state = 'ACCEPTED'), 0) * 100)::bigint as accepted_value
		FROM generate_series(
			(NOW() - INTERVAL '29 days')::date,
			NOW()::date,
			'1 day'::interval
		) d
		LEFT JOIN quotes q ON q.created_at::date = d::date
		GROUP BY d::date
		ORDER BY d::date
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, trendQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to get trend data: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var t QuoteAnalyticsTrend
		var date time.Time
		if err := rows.Scan(&date, &t.Created, &t.Accepted, &t.Rejected, &t.TotalValueCents, &t.AcceptedValueCents); err != nil {
			return nil, fmt.Errorf("failed to scan trend: %w", err)
		}
		t.Date = date.Format("2006-01-02")
		a.TrendData = append(a.TrendData, t)
	}
	if a.TrendData == nil {
		a.TrendData = []QuoteAnalyticsTrend{}
	}

	return a, nil
}
