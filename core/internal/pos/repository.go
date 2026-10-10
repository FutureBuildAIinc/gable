// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Repository is the store half of the counter. Every statement goes through
// GetExecutor (the caller's transaction when one is open), money is written
// as `$n::numeric / 100` and read as `ROUND(col * 100)::bigint`, never
// through float64, and every read and lock carries the branch wall.
type Repository interface {
	// Sales
	CreateSale(ctx context.Context, s *Sale) error
	LockSale(ctx context.Context, id uuid.UUID) error
	BumpSaleRevision(ctx context.Context, id uuid.UUID) error
	GetSale(ctx context.Context, id uuid.UUID) (*Sale, error)
	UpdateSaleTotals(ctx context.Context, id uuid.UUID, subtotal, tax, total int64) error
	CompleteSale(ctx context.Context, id, invoiceID uuid.UUID, subtotal, tax, total, change int64) error
	VoidSale(ctx context.Context, id uuid.UUID) error
	NextSaleNumber(ctx context.Context) (string, error)
	NextReturnNumber(ctx context.Context) (string, error)
	ListSales(ctx context.Context, f SaleFilter, limit int) ([]SaleSummary, error)

	// Lines
	AddLines(ctx context.Context, saleID uuid.UUID, lines []salesdoc.Line) error
	RemoveLine(ctx context.Context, saleID, lineID uuid.UUID) error
	GetLines(ctx context.Context, saleID uuid.UUID) ([]salesdoc.Line, error)

	// Tenders
	AddTender(ctx context.Context, t *Tender) error
	GetTenders(ctx context.Context, saleID uuid.UUID) ([]Tender, error)

	// Returns
	CreateReturn(ctx context.Context, ret *Return, lines []ReturnLine) error
	GetReturn(ctx context.Context, id uuid.UUID) (*Return, error)
	ListReturns(ctx context.Context, f ReturnFilter, limit int) ([]Return, error)

	// Till sessions
	CreateTillSession(ctx context.Context, s *TillSession) error
	GetTillSession(ctx context.Context, id uuid.UUID) (*TillSession, error)
	GetOpenTillSession(ctx context.Context, registerID string) (*TillSession, error)
	CloseTillSession(ctx context.Context, s *TillSession) error
	AggregateTillSession(ctx context.Context, sessionID uuid.UUID) (*TillAggregate, error)

	// Z-reports
	CreateZReport(ctx context.Context, z *ZReport) error
	GetZReportBySession(ctx context.Context, sessionID uuid.UUID) (*ZReport, error)
	ListZReports(ctx context.Context, registerID string, date time.Time) ([]ZReport, error)

	// Lookups
	GetRegisterBranch(ctx context.Context, registerID string) (*uuid.UUID, error)
	LookupProducts(ctx context.Context, ids []uuid.UUID) (map[string]salesdoc.ProductRef, error)
	LookupKitComponents(ctx context.Context, kitIDs []uuid.UUID) (map[string][]salesdoc.KitComponent, error)
	LookupChargeCodes(ctx context.Context, codes []string) (map[string]salesdoc.ChargeCode, error)
	SearchProducts(ctx context.Context, query string, limit int) ([]QuickSearchResult, error)
	GetProductCatalog(ctx context.Context) ([]CatalogProduct, error)
	WalkInCustomer(ctx context.Context) (uuid.UUID, string, error)
	CustomerFacts(ctx context.Context, customerID uuid.UUID) (CustomerFacts, error)
	CustomerExempt(ctx context.Context, customerID uuid.UUID) (bool, error)
	OpenReceivableCents(ctx context.Context, customerID uuid.UUID) (int64, error)
	BranchLocalDate(ctx context.Context, branchID uuid.UUID, at time.Time) (time.Time, error)
	BranchTaxRate(ctx context.Context, branchID *uuid.UUID) (string, bool, error)

	// Offline sync
	SaleExists(ctx context.Context, id uuid.UUID) (bool, error)
	LogSyncBatch(ctx context.Context, batch LogBatch) error
}

// CustomerFacts is what the counter reads about the sale's customer: the
// effective currency and the credit limit the ACCOUNT tender checks.
type CustomerFacts struct {
	CreditLimitCents *int64
	Name             string
	Currency         string
}

// TillAggregate is a session's raw sums, taken from the payments its sales
// became: change never subtracts a second time because a cash payment is
// already the money kept (ADR 0005 section 14.2 C2-5).
type TillAggregate struct {
	SaleCount         int
	SalesTotalCents   int64
	TaxTotalCents     int64
	ChangeCents       int64
	TenderedByMethod  map[string]int64
	CashRefundsCents  int64
}

// SaleFilter is the sale list's filters. Date is a YYYY-MM-DD string, so
// the day boundary is the caller's and never the session's timezone.
type SaleFilter struct {
	RegisterID string
	Date       string
	Status     string
}

// ReturnFilter is the return list's filters.
type ReturnFilter struct {
	RegisterID string
	Date       string
}

// LogBatch is one offline sync's outcome record: Errors lists the failed
// items, Details the pending ones beside them.
type LogBatch struct {
	BatchID, RegisterID                     string
	Synced, Duplicates, ErrorCount, Pending int
	Errors                                  []SyncItemResult
	Details                                 []SyncItemResult
}

// SyncItemResult is one offline sale's outcome in the sync log.
type SyncItemResult struct {
	ClientID string `json:"client_id"`
	Reason   string `json:"reason"`
}

// PostgresRepository implements Repository.
type PostgresRepository struct {
	db *database.DB
}

// NewRepository creates the counter's repository.
func NewRepository(db *database.DB) *PostgresRepository { return &PostgresRepository{db: db} }

func (r *PostgresRepository) ex(ctx context.Context) database.Executor { return r.db.GetExecutor(ctx) }

// dayBounds turns a YYYY-MM-DD into the day's opening and closing absolute
// instants in the server's zone.
func dayBounds(day string) (time.Time, time.Time) {
	if day == "" {
		day = time.Now().Format("2006-01-02")
	}
	start, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		start = time.Now().Truncate(time.Hour)
	}
	return start, start.Add(24 * time.Hour)
}

// posLineCols is the shared line projection over pos_line_items.
var posLineCols = salesdoc.LineColumns(`false, NULL::uuid, NULL::bigint`)

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "23503" {
			return fmt.Errorf("a referenced record does not exist: %s", pgErr.ConstraintName)
		}
	}
	return err
}

func (r *PostgresRepository) CreateSale(ctx context.Context, s *Sale) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	number, err := httpx.NextDocumentNumber(ctx, r.ex(ctx), "pos_transaction_number_seq", "POS", httpx.DefaultDocNumberWidth)
	if err != nil {
		return err
	}
	s.Number = number
	// The register's branch is resolved first: reusing the register
	// parameter inside the insert's subquery trips Postgres' type inference.
	registerBranch, err := r.GetRegisterBranch(ctx, s.RegisterID)
	if err != nil {
		return err
	}
	var created time.Time
	var branch uuid.UUID
	switch {
	case registerBranch != nil:
		branch = *registerBranch
	case branchctx.IDForQuery(ctx) != nil:
		branch = *branchctx.IDForQuery(ctx)
	default:
		if err := r.ex(ctx).QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branch); err != nil {
			return fmt.Errorf("no branch for the sale: %w", err)
		}
	}
	s.BranchID = branch
	err = r.ex(ctx).QueryRow(ctx, `
		INSERT INTO pos_transactions (id, number, register_id, cashier_id, customer_id, currency, subtotal, tax_amount,
			total, change_due, status, till_session_id, branch_id, created_at, updated_at, revision)
		VALUES ($1, $2, $3, $4, $5, $6, 0, 0, 0, 0, $7, $8, $9, NOW(), NOW(), 1)
		RETURNING created_at, revision`,
		s.ID, s.Number, s.RegisterID, s.CashierID, s.CustomerID, s.Currency, StatusOpen, s.TillSessionID, s.BranchID,
	).Scan(&created, &s.Revision)
	s.CreatedAt = httpx.TimestampOf(created)
	if err != nil {
		return fmt.Errorf("failed to create the sale: %w", mapWriteError(err))
	}
	return nil
}

func (r *PostgresRepository) LockSale(ctx context.Context, id uuid.UUID) error {
	var one int
	err := r.ex(ctx).QueryRow(ctx, `SELECT 1 FROM pos_transactions WHERE id = $1 AND ($2::uuid IS NULL OR branch_id = $2)
		FOR NO KEY UPDATE`, id, branchctx.IDForQuery(ctx)).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("sale not found")
	}
	return err
}

// BumpSaleRevision moves a sale's revision in process (the recipe's rule for
// an act that touches a document it does not own the status of): a return
// against the sale does it, so a void built on the earlier revision is
// refused stale instead of also restocking the goods.
func (r *PostgresRepository) BumpSaleRevision(ctx context.Context, id uuid.UUID) error {
	_, err := r.ex(ctx).Exec(ctx, `UPDATE pos_transactions SET revision = revision + 1, updated_at = NOW() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to move the sale's revision: %w", err)
	}
	return nil
}

const saleCols = `t.id, t.number, t.revision, t.branch_id, t.register_id, t.cashier_id, t.customer_id, t.currency,
	ROUND(t.subtotal * 100)::bigint, ROUND(t.tax_amount * 100)::bigint, ROUND(t.total * 100)::bigint,
	ROUND(t.change_due * 100)::bigint, t.till_session_id, t.status, t.invoice_id, t.completed_at, t.created_at`

func scanSale(row pgx.Row, s *Sale) error {
	var completed *time.Time
	var created time.Time
	if err := row.Scan(&s.ID, &s.Number, &s.Revision, &s.BranchID, &s.RegisterID, &s.CashierID, &s.CustomerID, &s.Currency,
		&s.SubtotalCents, &s.TaxCents, &s.TotalCents, &s.ChangeCents, &s.TillSessionID, &s.Status, &s.InvoiceID,
		&completed, &created); err != nil {
		return err
	}
	s.CompletedAt = httpx.PtrTimestamp(completed)
	s.CreatedAt = httpx.TimestampOf(created)
	return nil
}

func (r *PostgresRepository) GetSale(ctx context.Context, id uuid.UUID) (*Sale, error) {
	s := &Sale{}
	err := scanSale(r.ex(ctx).QueryRow(ctx, `SELECT `+saleCols+` FROM pos_transactions t
		WHERE t.id = $1 AND ($2::uuid IS NULL OR t.branch_id = $2)`, id, branchctx.IDForQuery(ctx)), s)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("sale not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the sale: %w", err)
	}
	return s, nil
}

func (r *PostgresRepository) UpdateSaleTotals(ctx context.Context, id uuid.UUID, subtotal, tax, total int64) error {
	_, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_transactions SET subtotal = $2::numeric / 100, tax_amount = $3::numeric / 100, total = $4::numeric / 100,
			updated_at = NOW(), revision = revision + 1 WHERE id = $1`, id, subtotal, tax, total)
	if err != nil {
		return fmt.Errorf("failed to update the sale's totals: %w", err)
	}
	return nil
}

func (r *PostgresRepository) CompleteSale(ctx context.Context, id, invoiceID uuid.UUID, subtotal, tax, total, change int64) error {
	tag, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_transactions SET invoice_id = $2, subtotal = $3::numeric / 100, tax_amount = $4::numeric / 100,
			total = $5::numeric / 100, change_due = $6::numeric / 100, status = 'COMPLETED', completed_at = NOW(),
			updated_at = NOW(), revision = revision + 1 WHERE id = $1 AND status IN ('OPEN', 'HELD')`, id, invoiceID, subtotal, tax, total, change)
	if err != nil {
		return fmt.Errorf("failed to complete the sale: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.InvalidStateTransition("only an open or held sale can be completed")
	}
	return nil
}

func (r *PostgresRepository) VoidSale(ctx context.Context, id uuid.UUID) error {
	tag, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_transactions SET status = 'VOIDED', updated_at = NOW(), revision = revision + 1
		WHERE id = $1 AND status = 'COMPLETED'`, id)
	if err != nil {
		return fmt.Errorf("failed to void the sale: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.InvalidStateTransition("only a completed sale can be voided")
	}
	return nil
}

func (r *PostgresRepository) NextSaleNumber(ctx context.Context) (string, error) {
	return httpx.NextDocumentNumber(ctx, r.ex(ctx), "pos_transaction_number_seq", "POS", httpx.DefaultDocNumberWidth)
}

func (r *PostgresRepository) NextReturnNumber(ctx context.Context) (string, error) {
	return httpx.NextDocumentNumber(ctx, r.ex(ctx), "pos_return_number_seq", "RTN", httpx.DefaultDocNumberWidth)
}

func (r *PostgresRepository) ListSales(ctx context.Context, f SaleFilter, limit int) ([]SaleSummary, error) {
	// The day's bounds are absolute timestamps computed from the date
	// string in the server's zone: a timestamptz never meets a date cast,
	// whose midnight belongs to the session's zone, not the caller's.
	start, end := dayBounds(f.Date)
	predicate := `WHERE ($1 = '' OR t.register_id = $1)
		AND ($2::text IS NULL OR t.status = $2)
		AND t.created_at >= $3 AND t.created_at < $4
		AND ($5::uuid IS NULL OR t.branch_id = $5)`
	args := []any{f.RegisterID, nil, start, end, branchctx.IDForQuery(ctx)}
	if f.Status != "" {
		args[1] = f.Status
	}
	rows, err := r.ex(ctx).Query(ctx, `SELECT `+saleCols+`,
		(SELECT count(*) FROM pos_line_items l WHERE l.transaction_id = t.id AND l.line_type <> 'TEXT')
		FROM pos_transactions t `+predicate+` ORDER BY t.created_at DESC, t.id DESC LIMIT $6`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("failed to list sales: %w", err)
	}
	defer rows.Close()
	var out []SaleSummary
	for rows.Next() {
		s := &Sale{}
		var count int
		var completed *time.Time
		var created time.Time
		if err := rows.Scan(&s.ID, &s.Number, &s.Revision, &s.BranchID, &s.RegisterID, &s.CashierID, &s.CustomerID,
			&s.Currency, &s.SubtotalCents, &s.TaxCents, &s.TotalCents, &s.ChangeCents, &s.TillSessionID, &s.Status,
			&s.InvoiceID, &completed, &created, &count); err != nil {
			return nil, err
		}
		s.CompletedAt = httpx.PtrTimestamp(completed)
		s.CreatedAt = httpx.TimestampOf(created)
		out = append(out, SaleSummary{ID: s.ID, Number: s.Number, Revision: s.Revision, BranchID: s.BranchID,
			RegisterID: s.RegisterID, CashierID: s.CashierID, CustomerID: s.CustomerID, Currency: s.Currency,
			TotalCents: s.TotalCents, Status: s.Status, InvoiceID: s.InvoiceID, CompletedAt: s.CompletedAt,
			CreatedAt: s.CreatedAt, ItemCount: count})
	}
	return out, rows.Err()
}

func (r *PostgresRepository) AddLines(ctx context.Context, saleID uuid.UUID, lines []salesdoc.Line) error {
	for i := range lines {
		l := &lines[i]
		if l.ID == uuid.Nil {
			l.ID = uuid.New()
		}
		_, err := r.ex(ctx).Exec(ctx, `
			INSERT INTO pos_line_items (id, transaction_id, position, line_type, parent_line_id, product_id, charge_code_id,
				sku, description, quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, priced_unit_price,
				price_source, override_reason, discount_percent, discount_amount, discount_reason, price_adjusted_by,
				line_total, taxable, revenue_account_code, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::numeric / 10000, $11, $12, $13::numeric / 10000,
				$14::numeric / 10000, $15::numeric / 10000, $16::numeric / 10000, $17, $18, $19::numeric / 10000,
				$20::numeric / 100, $21, $22, $23::numeric / 100, $24, $25, NOW())`,
			l.ID, saleID, l.Position, string(l.LineType), l.ParentLineID, l.ProductID, l.ChargeCodeID,
			l.SKU, l.Description, qtyArg(l.Quantity), l.UOM, l.PriceUOM, qtyArg(l.UOMQty), qtyArg(l.PriceUOMQty),
			priceArg(l.UnitPrice), priceArg(l.PricedUnitPrice), string(l.PriceSource), l.OverrideReason,
			qtyArg(l.DiscountPercent), centsArg(l.DiscountAmount), l.DiscountReason, l.PriceAdjustedBy,
			centsArg(l.LineTotal), l.Taxable, l.RevenueAccountCode)
		if err != nil {
			return fmt.Errorf("failed to add the sale line: %w", mapWriteError(err))
		}
	}
	return nil
}

func qtyArg(q *httpx.Quantity) any {
	if q == nil {
		return nil
	}
	return int64(*q)
}

func priceArg(p *httpx.Price) any {
	if p == nil {
		return nil
	}
	return int64(*p)
}

func centsArg(c *httpx.Cents) any {
	if c == nil {
		return nil
	}
	return int64(*c)
}

func (r *PostgresRepository) RemoveLine(ctx context.Context, saleID, lineID uuid.UUID) error {
	tag, err := r.ex(ctx).Exec(ctx, `DELETE FROM pos_line_items WHERE id = $1 AND transaction_id = $2`, lineID, saleID)
	if err != nil {
		return fmt.Errorf("failed to remove the line: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("line not found on this sale")
	}
	return nil
}

func (r *PostgresRepository) GetLines(ctx context.Context, saleID uuid.UUID) ([]salesdoc.Line, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT `+posLineCols+` FROM pos_line_items l
		LEFT JOIN charge_codes cc ON cc.id = l.charge_code_id
		WHERE l.transaction_id = $1 ORDER BY l.position, l.created_at, l.id`, saleID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the sale's lines: %w", err)
	}
	defer rows.Close()
	out := []salesdoc.Line{}
	for rows.Next() {
		var l salesdoc.Line
		var sc salesdoc.LineScan
		if err := rows.Scan(sc.Dests(&l)...); err != nil {
			return nil, err
		}
		sc.Finish(&l)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) AddTender(ctx context.Context, t *Tender) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	var created time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		INSERT INTO pos_tenders (id, transaction_id, method, amount, payment_id, reference, card_last4, card_brand,
			gateway_tx_id, auth_code, created_at)
		VALUES ($1, $2, $3, $4::numeric / 100, $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), NOW())
		RETURNING created_at`,
		t.ID, t.SaleID, string(t.Method), int64(t.AmountCents), t.PaymentID, t.Reference, t.CardLast4, t.CardBrand,
		t.GatewayTxID, t.AuthCode).Scan(&created)
	t.CreatedAt = httpx.TimestampOf(created)
	if err != nil {
		return fmt.Errorf("failed to record the tender: %w", mapWriteError(err))
	}
	return nil
}

func (r *PostgresRepository) GetTenders(ctx context.Context, saleID uuid.UUID) ([]Tender, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, method, ROUND(amount * 100)::bigint, payment_id, reference, card_last4, card_brand, gateway_tx_id,
			auth_code, created_at
		FROM pos_tenders WHERE transaction_id = $1 ORDER BY created_at, method, id`, saleID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the tenders: %w", err)
	}
	defer rows.Close()
	out := []Tender{}
	for rows.Next() {
		var t Tender
		var method string
		var createdAt time.Time
		if err := rows.Scan(&t.ID, &method, &t.AmountCents, &t.PaymentID, &t.Reference, &t.CardLast4, &t.CardBrand,
			&t.GatewayTxID, &t.AuthCode, &createdAt); err != nil {
			return nil, err
		}
		t.Method = TenderMethod(method)
		t.SaleID = saleID
		t.CreatedAt = httpx.TimestampOf(createdAt)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CreateReturn(ctx context.Context, ret *Return, lines []ReturnLine) error {
	if ret.ID == uuid.Nil {
		ret.ID = uuid.New()
	}
	number, err := httpx.NextDocumentNumber(ctx, r.ex(ctx), "pos_return_number_seq", "RTN", httpx.DefaultDocNumberWidth)
	if err != nil {
		return err
	}
	ret.Number = number
	var createdAt time.Time
	err = r.ex(ctx).QueryRow(ctx, `
		INSERT INTO pos_returns (id, number, register_id, till_session_id, original_transaction_id, customer_id, branch_id,
			cashier_id, currency, subtotal, tax_amount, total, refund_method, reason, status, credit_memo_id, created_at, updated_at, revision)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::numeric / 100, $11::numeric / 100, $12::numeric / 100, $13, $14,
			'COMPLETED', $15, NOW(), NOW(), 1)
		RETURNING created_at`,
		ret.ID, ret.Number, ret.RegisterID, ret.TillSessionID, ret.OriginalSaleID, ret.CustomerID, ret.BranchID,
		ret.CashierID, ret.Currency, int64(ret.SubtotalCents), int64(ret.TaxCents), int64(ret.TotalCents),
		string(ret.RefundMethod), ret.Reason, ret.CreditMemoID).Scan(&createdAt)
	ret.CreatedAt = httpx.TimestampOf(createdAt)
	if err != nil {
		return fmt.Errorf("failed to record the return: %w", mapWriteError(err))
	}
	for i := range lines {
		l := &lines[i]
		if l.ID == uuid.Nil {
			l.ID = uuid.New()
		}
		_, err := r.ex(ctx).Exec(ctx, `
			INSERT INTO pos_return_lines (id, return_id, position, line_type, product_id, description, quantity, uom,
				unit_price, line_total, restock, created_at)
			VALUES ($1, $2, $3, 'PRODUCT', $4, $5, -($6::numeric / 10000), $7, $8::numeric / 10000, $9::numeric / 100, $10, NOW())`,
			l.ID, ret.ID, l.Position, l.ProductID, l.Description, qtyArg(l.Quantity), l.UOM, priceArg(l.UnitPrice),
			centsArg(l.LineTotal), l.Restock)
		if err != nil {
			return fmt.Errorf("failed to record the return line: %w", mapWriteError(err))
		}
	}
	return nil
}

func (r *PostgresRepository) GetReturn(ctx context.Context, id uuid.UUID) (*Return, error) {
	ret := &Return{}
	var method string
	var createdAt time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT id, number, revision, branch_id, register_id, till_session_id, original_transaction_id, customer_id,
			cashier_id, currency, ROUND(subtotal * 100)::bigint, ROUND(tax_amount * 100)::bigint, ROUND(total * 100)::bigint,
			refund_method, reason, credit_memo_id, created_at
		FROM pos_returns WHERE id = $1 AND ($2::uuid IS NULL OR branch_id = $2)`, id, branchctx.IDForQuery(ctx)).
		Scan(&ret.ID, &ret.Number, &ret.Revision, &ret.BranchID, &ret.RegisterID, &ret.TillSessionID, &ret.OriginalSaleID,
			&ret.CustomerID, &ret.CashierID, &ret.Currency, &ret.SubtotalCents, &ret.TaxCents, &ret.TotalCents,
			&method, &ret.Reason, &ret.CreditMemoID, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("return not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the return: %w", err)
	}
	ret.CreatedAt = httpx.TimestampOf(createdAt)
	ret.RefundMethod = RefundMethod(method)
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, position, product_id, description, ROUND(-quantity * 10000)::bigint, uom,
			ROUND(unit_price * 10000)::bigint, ROUND(-line_total * 100)::bigint, restock
		FROM pos_return_lines WHERE return_id = $1 ORDER BY position, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l ReturnLine
		if err := rows.Scan(&l.ID, &l.Position, &l.ProductID, &l.Description, &l.Quantity, &l.UOM, &l.UnitPrice,
			&l.LineTotal, &l.Restock); err != nil {
			return nil, err
		}
		ret.Lines = append(ret.Lines, l)
	}
	return ret, rows.Err()
}

func (r *PostgresRepository) ListReturns(ctx context.Context, f ReturnFilter, limit int) ([]Return, error) {
	start, end := dayBounds(f.Date)
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, number, revision, branch_id, register_id, till_session_id, original_transaction_id, customer_id,
			cashier_id, currency, ROUND(subtotal * 100)::bigint, ROUND(tax_amount * 100)::bigint, ROUND(total * 100)::bigint,
			refund_method, reason, credit_memo_id, created_at
		FROM pos_returns
		WHERE ($1 = '' OR register_id = $1)
			AND created_at >= $2 AND created_at < $3
			AND ($4::uuid IS NULL OR branch_id = $4)
		ORDER BY created_at DESC, id DESC LIMIT $5`, f.RegisterID, start, end, branchctx.IDForQuery(ctx), limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list returns: %w", err)
	}
	defer rows.Close()
	out := []Return{}
	for rows.Next() {
		ret := &Return{Lines: []ReturnLine{}}
		var method string
		var createdAt time.Time
		if err := rows.Scan(&ret.ID, &ret.Number, &ret.Revision, &ret.BranchID, &ret.RegisterID, &ret.TillSessionID,
			&ret.OriginalSaleID, &ret.CustomerID, &ret.CashierID, &ret.Currency, &ret.SubtotalCents, &ret.TaxCents,
			&ret.TotalCents, &method, &ret.Reason, &ret.CreditMemoID, &createdAt); err != nil {
			return nil, err
		}
		ret.CreatedAt = httpx.TimestampOf(createdAt)
		ret.RefundMethod = RefundMethod(method)
		out = append(out, *ret)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) GetRegisterBranch(ctx context.Context, registerID string) (*uuid.UUID, error) {
	var branch *uuid.UUID
	err := r.ex(ctx).QueryRow(ctx, `SELECT l.branch_id FROM pos_registers pr
		LEFT JOIN locations l ON l.id = pr.location_id WHERE pr.id = $1`, registerID).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("no such register")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the register's branch: %w", err)
	}
	return branch, nil
}

func (r *PostgresRepository) LookupProducts(ctx context.Context, ids []uuid.UUID) (map[string]salesdoc.ProductRef, error) {
	out := map[string]salesdoc.ProductRef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, COALESCE(sku, ''), COALESCE(description, ''), COALESCE(uom_primary::text, 'EA'),
			COALESCE(ROUND(base_price * 10000)::bigint, 0), COALESCE(ROUND(average_unit_cost * 10000)::bigint, 0),
			COALESCE(is_kit, FALSE), COALESCE(taxable, TRUE)
		FROM products WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref salesdoc.ProductRef
		if err := rows.Scan(&ref.ID, &ref.SKU, &ref.Description, &ref.UOMPrimary, &ref.BasePrice, &ref.AverageCost,
			&ref.IsKit, &ref.Taxable); err != nil {
			return nil, err
		}
		out[ref.ID.String()] = ref
	}
	return out, rows.Err()
}

func (r *PostgresRepository) LookupKitComponents(ctx context.Context, kitIDs []uuid.UUID) (map[string][]salesdoc.KitComponent, error) {
	out := map[string][]salesdoc.KitComponent{}
	if len(kitIDs) == 0 {
		return out, nil
	}
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT kit_product_id, component_product_id, ROUND(quantity * 10000)::bigint, position
		FROM product_kit_components WHERE kit_product_id = ANY($1) ORDER BY position`, kitIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c salesdoc.KitComponent
		if err := rows.Scan(&c.KitProductID, &c.ComponentProductID, &c.Quantity, &c.Position); err != nil {
			return nil, err
		}
		out[c.KitProductID.String()] = append(out[c.KitProductID.String()], c)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) LookupChargeCodes(ctx context.Context, codes []string) (map[string]salesdoc.ChargeCode, error) {
	out := map[string]salesdoc.ChargeCode{}
	if len(codes) == 0 {
		return out, nil
	}
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, code, name, revenue_account_code, taxable, ROUND(default_unit_price * 10000)::bigint, is_active, revision
		FROM charge_codes WHERE code = ANY($1)`, codes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c salesdoc.ChargeCode
		if err := rows.Scan(&c.ID, &c.Code, &c.Name, &c.RevenueAccountCode, &c.Taxable, &c.DefaultUnitPrice,
			&c.IsActive, &c.Revision); err != nil {
			return nil, err
		}
		out[c.Code] = c
	}
	return out, rows.Err()
}

func (r *PostgresRepository) SearchProducts(ctx context.Context, query string, limit int) ([]QuickSearchResult, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT p.id, COALESCE(p.sku, ''), COALESCE(p.description, ''), COALESCE(ROUND(p.base_price * 100)::bigint, 0),
			COALESCE(p.uom_primary::text, 'EA'), COALESCE(ROUND(SUM(i.quantity - i.allocated) * 10000)::bigint, 0)
		FROM products p LEFT JOIN inventory i ON i.product_id = p.id
		WHERE (p.sku ILIKE $1 OR p.description ILIKE $1)
		GROUP BY p.id, p.sku, p.description, p.base_price, p.uom_primary
		ORDER BY p.sku LIMIT $2`, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuickSearchResult
	for rows.Next() {
		var q QuickSearchResult
		if err := rows.Scan(&q.ProductID, &q.SKU, &q.Description, &q.UnitPriceCents, &q.UOM, &q.InStock); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) GetProductCatalog(ctx context.Context) ([]CatalogProduct, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT p.id, COALESCE(p.sku, ''), COALESCE(p.description, ''), COALESCE(ROUND(p.base_price * 100)::bigint, 0),
			COALESCE(p.uom_primary::text, 'EA'), COALESCE(ROUND(SUM(i.quantity - i.allocated) * 10000)::bigint, 0)
		FROM products p LEFT JOIN inventory i ON i.product_id = p.id
		GROUP BY p.id, p.sku, p.description, p.base_price, p.uom_primary
		ORDER BY p.sku`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogProduct
	for rows.Next() {
		var c CatalogProduct
		if err := rows.Scan(&c.ProductID, &c.SKU, &c.Description, &c.UnitPriceCents, &c.UOM, &c.InStock); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// WalkInCustomer answers the walk-in customer's id and the dealer's default
// currency (ADR 0005 section 13 C2-5 step 4).
func (r *PostgresRepository) WalkInCustomer(ctx context.Context) (uuid.UUID, string, error) {
	var id uuid.UUID
	var currency string
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT c.id, COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD')
		FROM customers c WHERE c.account_number = 'WALK-IN'`).Scan(&id, &currency)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("the walk-in customer is not configured: %w", err)
	}
	return id, currency, nil
}

func (r *PostgresRepository) CustomerFacts(ctx context.Context, customerID uuid.UUID) (CustomerFacts, error) {
	var f CustomerFacts
	var limit *int64
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT name, ROUND(credit_limit * 100)::bigint,
			COALESCE(currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD')
		FROM customers WHERE id = $1`, customerID).Scan(&f.Name, &limit, &f.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, httpx.NotFound("no such customer")
	}
	if err != nil {
		return f, err
	}
	f.CreditLimitCents = limit
	return f, nil
}

func (r *PostgresRepository) CustomerExempt(ctx context.Context, customerID uuid.UUID) (bool, error) {
	var exempt bool
	err := r.ex(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tax_exemptions WHERE customer_id = $1 AND is_active)`, customerID).Scan(&exempt)
	return exempt, err
}

func (r *PostgresRepository) OpenReceivableCents(ctx context.Context, customerID uuid.UUID) (int64, error) {
	var open int64
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT COALESCE((SELECT SUM(ROUND(i.amount_open * 100)::bigint) FROM invoices i
			WHERE i.customer_id = $1 AND i.status IN ('UNPAID', 'PARTIAL', 'OVERDUE')), 0)
			- COALESCE((SELECT SUM(ROUND(m.amount_open * 100)::bigint) FROM credit_memos m
			WHERE m.customer_id = $1 AND m.status IN ('OPEN', 'PARTIAL')), 0)
			- COALESCE((SELECT SUM(ROUND(p.amount_unapplied * 100)::bigint) FROM payments p
			WHERE p.customer_id = $1 AND p.status = 'POSTED'), 0)`, customerID).Scan(&open)
	return open, err
}

func (r *PostgresRepository) BranchLocalDate(ctx context.Context, branchID uuid.UUID, at time.Time) (time.Time, error) {
	var date time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT (timezone(COALESCE((SELECT l.timezone FROM locations l WHERE l.id = $1), 'UTC'), $2))::date`, branchID, at).Scan(&date)
	if err != nil {
		return at.UTC(), nil
	}
	return date, nil
}

// BranchTaxRate reads the branch's configured rate as the decimal string the
// resolver takes.
func (r *PostgresRepository) BranchTaxRate(ctx context.Context, branchID *uuid.UUID) (string, bool, error) {
	var rate *string
	err := r.ex(ctx).QueryRow(ctx, `SELECT default_tax_rate::text FROM locations WHERE id = $1`, branchID).Scan(&rate)
	if errors.Is(err, pgx.ErrNoRows) || rate == nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return *rate, true, nil
}

func (r *PostgresRepository) SaleExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var exists bool
	err := r.ex(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pos_transactions WHERE id = $1)`, id).Scan(&exists)
	return exists, err
}

func (r *PostgresRepository) LogSyncBatch(ctx context.Context, b LogBatch) error {
	// The jsonb carries both the failures and the pending items (a pending
	// offline sale stays visible in the log until a retry completes it).
	details := make([]SyncItemResult, 0, len(b.Details)+len(b.Errors))
	details = append(details, b.Errors...)
	details = append(details, b.Details...)
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = r.ex(ctx).Exec(ctx, `
		INSERT INTO pos_sync_log (batch_id, register_id, synced_count, duplicate_count, error_count, errors)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)`, b.BatchID, b.RegisterID, b.Synced, b.Duplicates, b.ErrorCount, string(raw))
	return err
}
