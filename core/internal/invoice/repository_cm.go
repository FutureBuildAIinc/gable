// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var errCreditNotFound = httpx.NotFound("credit memo not found")

// creditColumns is the one credit memo header projection.
const creditColumns = `
	cm.id, cm.number, cm.branch_id, cm.customer_id, COALESCE(c.name, ''), cm.invoice_id, cm.pos_return_id, cm.project_id,
	cm.ship_to_id, cm.status, cm.revision, cm.currency, cm.reason_code, cm.reason,
	ROUND(cm.subtotal * 100)::bigint, ROUND(cm.tax_amount * 100)::bigint, cm.tax_rate::text, ROUND(cm.total_amount * 100)::bigint,
	ROUND(cm.amount_open * 100)::bigint,
	cm.gl_entry_id, to_char(cm.memo_date, 'YYYY-MM-DD'), cm.voided_at, cm.voided_by, cm.void_reason, cm.created_at, cm.updated_at`

const creditFrom = `
	FROM credit_memos cm
	LEFT JOIN customers c ON c.id = cm.customer_id`

func scanCredit(row pgx.Row, s *CreditMemoSummary) error {
	var (
		status, reasonCode string
		subtotal, tax      int64
		total, open        int64
		taxRate            *string
		voided             *time.Time
		created, updated   time.Time
	)
	if err := row.Scan(&s.ID, &s.Number, &s.BranchID, &s.CustomerID, &s.CustomerName, &s.InvoiceID, &s.PosReturnID, &s.JobID,
		&s.ShipToID, &status, &s.Revision, &s.Currency, &reasonCode, &s.Reason,
		&subtotal, &tax, &taxRate, &total, &open,
		&s.GLEntryID, &s.MemoDate, &voided, &s.VoidedBy, &s.VoidReason, &created, &updated); err != nil {
		return err
	}
	s.Status, s.ReasonCode = CreditStatus(status), ReasonCode(reasonCode)
	s.SubtotalCents, s.TaxCents, s.TotalCents, s.OpenCents = httpx.Cents(subtotal), httpx.Cents(tax), httpx.Cents(total), httpx.Cents(open)
	if taxRate != nil {
		if pct, err := salesdoc.RateToPercent(*taxRate); err == nil {
			s.TaxRatePercent = &pct
		}
	}
	s.VoidedAt = httpx.PtrTimestamp(voided)
	s.CreatedAt, s.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

// CreditFilter is the credit memo list's filters and keyset position.
type CreditFilter struct {
	Statuses   []CreditStatus
	CustomerID *uuid.UUID
	InvoiceID  *uuid.UUID
	JobID      *uuid.UUID
	AfterTime  *time.Time
	AfterID    uuid.UUID
	Limit      int
}

var creditWhere = "\n\tWHERE " + wall("cm", 1, 2) + `
	  AND (cardinality($3::text[]) = 0 OR cm.status = ANY($3))
	  AND ($4::uuid IS NULL OR cm.customer_id = $4)
	  AND ($5::uuid IS NULL OR cm.invoice_id = $5)
	  AND ($6::uuid IS NULL OR cm.project_id = $6)`

func (r *PostgresRepository) creditArgs(ctx context.Context, f CreditFilter) []any {
	statuses := make([]string, len(f.Statuses))
	for i, s := range f.Statuses {
		statuses[i] = string(s)
	}
	return []any{middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), statuses, f.CustomerID, f.InvoiceID, f.JobID}
}

// ListCreditMemos answers one page, newest first by (created_at, id).
func (r *PostgresRepository) ListCreditMemos(ctx context.Context, f CreditFilter) ([]CreditMemoSummary, error) {
	args := append(r.creditArgs(ctx, f), f.AfterTime, f.AfterID, f.Limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+creditColumns+creditFrom+creditWhere+`
		  AND ($7::timestamptz IS NULL OR (cm.created_at, cm.id) < ($7, $8::uuid))
		ORDER BY cm.created_at DESC, cm.id DESC
		LIMIT $9`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list credit memos: %w", err)
	}
	defer rows.Close()
	out := []CreditMemoSummary{}
	for rows.Next() {
		var s CreditMemoSummary
		if err := scanCredit(rows, &s); err != nil {
			return nil, fmt.Errorf("failed to scan credit memo: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CountCreditMemos is the size of the filtered set, for ?include=total.
func (r *PostgresRepository) CountCreditMemos(ctx context.Context, f CreditFilter) (int64, error) {
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT count(*) FROM credit_memos cm`+creditWhere, r.creditArgs(ctx, f)...).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count credit memos: %w", err)
	}
	return n, nil
}

// GetCreditMemo reads one credit memo with its lines, held to the branch
// wall.
func (r *PostgresRepository) GetCreditMemo(ctx context.Context, id uuid.UUID) (*CreditMemo, error) {
	var cm CreditMemo
	row := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+creditColumns+creditFrom+`
		WHERE cm.id = $1 AND `+wall("cm", 2, 3),
		id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err := scanCredit(row, &cm.CreditMemoSummary); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errCreditNotFound
		}
		return nil, fmt.Errorf("failed to get credit memo: %w", err)
	}
	lines, err := r.creditLines(ctx, id)
	if err != nil {
		return nil, err
	}
	cm.Lines = lines
	return &cm, nil
}

// LockCreditMemo takes the credit memo row FOR UPDATE (section 11, step 3),
// held to the branch wall, and reads it.
func (r *PostgresRepository) LockCreditMemo(ctx context.Context, id uuid.UUID) (*CreditMemo, error) {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT cm.id FROM credit_memos cm WHERE cm.id = $1 AND `+wall("cm", 2, 3)+` FOR UPDATE`,
		id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errCreditNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock credit memo: %w", err)
	}
	return r.GetCreditMemo(ctx, id)
}

// CreditMemoInvoiceID answers the invoice a credit memo names (read without a
// lock, to learn what to lock), held to the branch wall.
func (r *PostgresRepository) CreditMemoInvoiceID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
	var inv *uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT cm.invoice_id FROM credit_memos cm WHERE cm.id = $1 AND `+wall("cm", 2, 3),
		id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).Scan(&inv)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errCreditNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the credit memo's invoice: %w", err)
	}
	return inv, nil
}

func (r *PostgresRepository) creditLines(ctx context.Context, id uuid.UUID) ([]CreditLine, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+salesdoc.LineColumns("false, NULL::uuid, NULL::bigint")+`,
		       l.invoice_line_id, l.restock, ROUND(l.unit_cost * 10000)::bigint, ROUND(l.cost * 100)::bigint
		FROM credit_memo_lines l
		LEFT JOIN charge_codes cc ON cc.id = l.charge_code_id
		WHERE l.credit_memo_id = $1
		ORDER BY l.position, l.created_at, l.id`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get credit memo lines: %w", err)
	}
	defer rows.Close()
	out := []CreditLine{}
	for rows.Next() {
		var (
			l        CreditLine
			ls       salesdoc.LineScan
			unitCost *int64
			cost     int64
		)
		if err := rows.Scan(append(ls.Dests(&l.Line), &l.InvoiceLineID, &l.Restock, &unitCost, &cost)...); err != nil {
			return nil, fmt.Errorf("failed to scan credit memo line: %w", err)
		}
		ls.Finish(&l.Line)
		l.UnitCost = salesdoc.PtrPrice(unitCost)
		l.CostCents = httpx.Cents(cost)
		out = append(out, l)
	}
	return out, rows.Err()
}

// CreditHeader is the stored header of a credit memo for a write. Totals are
// cents and negative; the rate is the decimal string, nil when the invoice's
// provider priced the tax.
type CreditHeader struct {
	ID         uuid.UUID
	CustomerID uuid.UUID
	InvoiceID  *uuid.UUID
	BranchID   uuid.UUID
	ProjectID  *uuid.UUID
	ShipToID   *uuid.UUID
	Currency   string
	ReasonCode ReasonCode
	Reason     string
	MemoDate   time.Time

	SubtotalCents int64
	TaxCents      int64
	TotalCents    int64
	TaxRate       *string
}

// InsertCreditMemo writes a new draft: no number (it is minted at post).
func (r *PostgresRepository) InsertCreditMemo(ctx context.Context, h *CreditHeader) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO credit_memos (id, customer_id, invoice_id, branch_id, project_id, ship_to_id, currency, reason_code, reason,
			amount, status, memo_date, subtotal, tax_amount, total_amount, tax_rate, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, -($10::numeric / 100), 'DRAFT', $11::date,
			$12::numeric / 100, $13::numeric / 100, $10::numeric / 100, $14::numeric, NOW(), NOW())`,
		h.ID, h.CustomerID, h.InvoiceID, h.BranchID, h.ProjectID, h.ShipToID, h.Currency, string(h.ReasonCode), h.Reason,
		h.TotalCents, h.MemoDate.Format("2006-01-02"), h.SubtotalCents, h.TaxCents, h.TaxRate)
	if err != nil {
		return mapWriteError(err, "failed to insert credit memo")
	}
	return nil
}

// UpdateCreditDraft replaces a draft's header fields and totals and moves the
// revision.
func (r *PostgresRepository) UpdateCreditDraft(ctx context.Context, h *CreditHeader) error {
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE credit_memos SET customer_id = $2, invoice_id = $3, project_id = $4, ship_to_id = $5, reason_code = $6,
			reason = $7, memo_date = $8::date, amount = -($9::numeric / 100), subtotal = $10::numeric / 100,
			tax_amount = $11::numeric / 100, total_amount = $9::numeric / 100, tax_rate = $12::numeric,
			updated_at = NOW(), revision = revision + 1
		WHERE id = $1`,
		h.ID, h.CustomerID, h.InvoiceID, h.ProjectID, h.ShipToID, string(h.ReasonCode), h.Reason,
		h.MemoDate.Format("2006-01-02"), h.TotalCents, h.SubtotalCents, h.TaxCents, h.TaxRate)
	if err != nil {
		return mapWriteError(err, "failed to update credit memo")
	}
	if ct.RowsAffected() == 0 {
		return errCreditNotFound
	}
	return nil
}

// ReplaceCreditLines deletes the memo's lines and writes the given ones.
func (r *PostgresRepository) ReplaceCreditLines(ctx context.Context, memoID uuid.UUID, lines []CreditLine) error {
	exec := r.db.GetExecutor(ctx)
	if _, err := exec.Exec(ctx, `DELETE FROM credit_memo_lines WHERE credit_memo_id = $1`, memoID); err != nil {
		return fmt.Errorf("failed to replace the credit memo lines: %w", err)
	}
	for i := range lines {
		l := &lines[i]
		var qty, uomQty, priceQty, price, priced, discPct, discAmt, total *int64
		if l.Quantity != nil {
			qty = i64p(int64(*l.Quantity))
		}
		if l.UOMQty != nil {
			uomQty = i64p(int64(*l.UOMQty))
		}
		if l.PriceUOMQty != nil {
			priceQty = i64p(int64(*l.PriceUOMQty))
		}
		if l.UnitPrice != nil {
			price = i64p(int64(*l.UnitPrice))
		}
		if l.PricedUnitPrice != nil {
			priced = i64p(int64(*l.PricedUnitPrice))
		}
		if l.DiscountPercent != nil {
			discPct = i64p(int64(*l.DiscountPercent))
		}
		if l.DiscountAmount != nil {
			discAmt = i64p(int64(*l.DiscountAmount))
		}
		if l.LineTotal != nil {
			total = i64p(int64(*l.LineTotal))
		}
		var unitCost *int64
		if l.UnitCost != nil {
			unitCost = i64p(int64(*l.UnitCost))
		}
		_, err := exec.Exec(ctx, `
			INSERT INTO credit_memo_lines (id, credit_memo_id, position, line_type, parent_line_id, product_id, charge_code_id,
				invoice_line_id, sku, description, quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price,
				priced_unit_price, price_source, override_reason, discount_percent, discount_amount, discount_reason,
				price_adjusted_by, line_total, taxable, revenue_account_code, restock, unit_cost, cost, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::numeric / 10000, $12, $13, $14::numeric / 10000,
				$15::numeric / 10000, $16::numeric / 10000, $17::numeric / 10000, $18, $19, $20::numeric / 10000,
				$21::numeric / 100, $22, $23, $24::numeric / 100, $25, $26, $27, $28::numeric / 10000, $29::numeric / 100, NOW())`,
			l.ID, memoID, l.Position, string(l.LineType), l.ParentLineID, l.ProductID, l.ChargeCodeID,
			l.InvoiceLineID, l.SKU, l.Description, qty, l.UOM, l.PriceUOM, uomQty, priceQty, price,
			priced, string(l.PriceSource), l.OverrideReason, discPct,
			discAmt, l.DiscountReason, l.PriceAdjustedBy, total, l.Taxable, l.RevenueAccountCode, l.Restock, unitCost, int64(l.CostCents))
		if err != nil {
			return mapWriteError(err, "failed to insert credit memo line")
		}
	}
	return nil
}

func i64p(v int64) *int64 { return &v }

// UpdateCreditLines rewrites the cost and extension the post recomputed.
func (r *PostgresRepository) UpdateCreditLines(ctx context.Context, memoID uuid.UUID, lines []CreditLine) error {
	return r.ReplaceCreditLines(ctx, memoID, lines)
}

// Credited is what earlier credit memos returned of one invoice line, as
// positive magnitudes: the quantity (scale 4), the extension and the
// discount share (cents).
type Credited struct {
	Quantity      int64
	TotalCents    int64
	DiscountCents int64
	CostCents     int64
	// RestockQuantity is the part of Quantity that went back to stock: the cost
	// that comes back telescopes over it, never over returns without restock.
	RestockQuantity int64
}

// CreditedAgainst is what the credit memos naming an invoice have already
// credited, per invoice line, with the tax they credited and the taxable base
// it covered (cents, positive). exclude is the memo being written; drafts
// count only when includeDrafts is set (the draft-time guard counts them;
// the post recomputes against what was posted).
type CreditedAgainst struct {
	ByLine       map[uuid.UUID]Credited
	TaxCents     int64
	TaxableCents int64
}

func (r *PostgresRepository) CreditedAgainstInvoice(ctx context.Context, invoiceID, exclude uuid.UUID, includeDrafts bool) (CreditedAgainst, error) {
	out := CreditedAgainst{ByLine: map[uuid.UUID]Credited{}}
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT l.invoice_line_id, -ROUND(SUM(l.quantity) * 10000)::bigint, -ROUND(SUM(l.line_total) * 100)::bigint,
		       ROUND(SUM(COALESCE(l.discount_amount, 0)) * 100)::bigint, -ROUND(SUM(l.cost) * 100)::bigint,
		       COALESCE(-ROUND(SUM(l.quantity) FILTER (WHERE l.restock) * 10000), 0)::bigint
		FROM credit_memo_lines l JOIN credit_memos cm ON cm.id = l.credit_memo_id
		WHERE cm.invoice_id = $1 AND cm.id <> $2 AND cm.status <> 'VOID' AND ($3::boolean OR cm.status <> 'DRAFT')
		  AND l.invoice_line_id IS NOT NULL AND l.quantity IS NOT NULL
		GROUP BY l.invoice_line_id`, invoiceID, exclude, includeDrafts)
	if err != nil {
		return out, fmt.Errorf("failed to read what the invoice has had credited: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var c Credited
		if err := rows.Scan(&id, &c.Quantity, &c.TotalCents, &c.DiscountCents, &c.CostCents, &c.RestockQuantity); err != nil {
			return out, fmt.Errorf("failed to scan a credited line: %w", err)
		}
		out.ByLine[id] = c
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	// The tax credited and the taxable base it covered come from the posted
	// memos only (the tax cap of section 3 counts credit memos that are not
	// void; a draft's tax is provisional until its post recomputes it).
	err = r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(-ROUND(SUM(cm.tax_amount) * 100), 0)::bigint,
		       COALESCE((SELECT -ROUND(SUM(l.line_total) * 100) FROM credit_memo_lines l JOIN credit_memos c2 ON c2.id = l.credit_memo_id
		                 WHERE c2.invoice_id = $1 AND c2.id <> $2 AND c2.status NOT IN ('DRAFT', 'VOID') AND l.taxable AND l.line_total IS NOT NULL), 0)::bigint
		FROM credit_memos cm WHERE cm.invoice_id = $1 AND cm.id <> $2 AND cm.status NOT IN ('DRAFT', 'VOID')`,
		invoiceID, exclude).Scan(&out.TaxCents, &out.TaxableCents)
	if err != nil {
		return out, fmt.Errorf("failed to read the tax already credited: %w", err)
	}
	return out, nil
}

// CreditFacts are the facts a credit memo with no invoice resolves its
// currency, branch and tax from.
type CreditFacts struct {
	Currency        string
	PrimaryBranchID uuid.UUID
	Exempt          bool
}

// CustomerCreditFacts reads the customer's effective currency (the override,
// else the dealer default), primary branch and tax exemption.
func (r *PostgresRepository) CustomerCreditFacts(ctx context.Context, customerID uuid.UUID) (CreditFacts, error) {
	var f CreditFacts
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD'),
		       c.primary_branch_id,
		       EXISTS(SELECT 1 FROM tax_exemptions t WHERE t.customer_id = c.id AND t.is_active)
		FROM customers c WHERE c.id = $1`, customerID).Scan(&f.Currency, &f.PrimaryBranchID, &f.Exempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, validationFailed("a referenced record does not exist", httpx.FieldError{Field: "customer_id", Message: "no such customer"})
	}
	if err != nil {
		return f, fmt.Errorf("failed to read the customer: %w", err)
	}
	return f, nil
}

// OwnedBy checks that the ship-to and the job named belong to the customer:
// the foreign keys alone would accept another customer's.
func (r *PostgresRepository) OwnedBy(ctx context.Context, customerID uuid.UUID, shipToID, jobID *uuid.UUID) (shipToOK, jobOK bool, err error) {
	shipToOK, jobOK = true, true
	if shipToID != nil {
		if err = r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM customer_ship_tos WHERE id = $1 AND customer_id = $2)`, *shipToID, customerID).Scan(&shipToOK); err != nil {
			return false, false, fmt.Errorf("failed to check the ship-to: %w", err)
		}
	}
	if jobID != nil {
		if err = r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1 AND customer_id = $2)`, *jobID, customerID).Scan(&jobOK); err != nil {
			return false, false, fmt.Errorf("failed to check the job: %w", err)
		}
	}
	return shipToOK, jobOK, nil
}

// TaxInputs gathers a credit memo with no invoice's rate ingredients
// (section 3): a ship-to's rate, else the branch's.
func (r *PostgresRepository) TaxInputs(ctx context.Context, branchID uuid.UUID, shipToID *uuid.UUID) (shipRate, branchRate *string, err error) {
	if shipToID != nil {
		if err = r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT tax_rate::text FROM customer_ship_tos WHERE id = $1`, *shipToID).Scan(&shipRate); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, fmt.Errorf("failed to read the ship-to rate: %w", err)
		}
	}
	if err = r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT default_tax_rate::text FROM locations WHERE id = $1`, branchID).Scan(&branchRate); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf("failed to read the branch rate: %w", err)
	}
	return shipRate, branchRate, nil
}

// LookupProducts reads the slice of each product a credit line defaults and
// costs from.
func (r *PostgresRepository) LookupProducts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]salesdoc.ProductRef, error) {
	out := map[uuid.UUID]salesdoc.ProductRef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, COALESCE(sku, ''), COALESCE(description, ''), uom_primary::text,
		       ROUND(base_price * 10000)::bigint, ROUND(COALESCE(average_unit_cost, 0) * 10000)::bigint,
		       COALESCE(commission_rate, 0), is_kit, taxable
		FROM products WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to look up products: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			p          salesdoc.ProductRef
			base, cost int64
		)
		if err := rows.Scan(&p.ID, &p.SKU, &p.Description, &p.UOMPrimary, &base, &cost, &p.CommissionRate, &p.IsKit, &p.Taxable); err != nil {
			return nil, fmt.Errorf("failed to scan product: %w", err)
		}
		p.BasePrice, p.AverageCost = httpx.Price(base), httpx.Price(cost)
		out[p.ID] = p
	}
	return out, rows.Err()
}

// ChargeCodeByCode reads an active charge code; false when there is none.
func (r *PostgresRepository) ChargeCodeByCode(ctx context.Context, code string) (salesdoc.ChargeCode, bool, error) {
	var c salesdoc.ChargeCode
	var def *int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT id, code, name, revenue_account_code, taxable, ROUND(default_unit_price * 10000)::bigint, is_active, revision
		FROM charge_codes WHERE code = $1`, code).Scan(&c.ID, &c.Code, &c.Name, &c.RevenueAccountCode, &c.Taxable, &def, &c.IsActive, &c.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, fmt.Errorf("failed to read the charge code: %w", err)
	}
	c.DefaultUnitPrice = salesdoc.PtrPrice(def)
	return c, true, nil
}
