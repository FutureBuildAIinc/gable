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
	// ReturnedQtyByLine sums, per sale line, the quantity every earlier
	// return of the sale brought back (the return's cap).
	ReturnedQtyByLine(ctx context.Context, saleID uuid.UUID) (map[uuid.UUID]httpx.Quantity, error)
	// RefundedCentsByLine sums the cents every earlier return refunded per
	// sale line, and RefundedTaxCents the tax they took back: the remainder
	// rule of repeated partial returns reads them. CardRefundedCents sums
	// what the card returns took, for the card refund's cap.
	// LegacyReturnedQtyByProduct sums the unlinked (pre contract) return
	// lines of a sale by product, so the cap counts them too.
	// LegacyRefundedCentsByProduct sums the cents those lines refunded, so
	// the remainder rule counts them too.
	RefundedCentsByLine(ctx context.Context, saleID uuid.UUID) (map[uuid.UUID]int64, error)
	RefundedTaxCents(ctx context.Context, saleID uuid.UUID) (int64, error)
	CardRefundedCents(ctx context.Context, saleID uuid.UUID) (int64, error)
	LegacyReturnedQtyByProduct(ctx context.Context, saleID uuid.UUID) (map[uuid.UUID]httpx.Quantity, error)
	LegacyRefundedCentsByProduct(ctx context.Context, saleID uuid.UUID) (map[uuid.UUID]int64, error)
	SaleHasReturns(ctx context.Context, saleID uuid.UUID) (bool, error)
	// InvoiceLineCosts reads an invoice's lines with the unit cost the sale
	// relieved (a linked return's restock cost, found by the stored link).
	InvoiceLineCosts(ctx context.Context, invoiceID uuid.UUID) ([]InvoiceLineCost, error)
	// LinkInvoiceLines records on each sale line the invoice line completion
	// built from it; InvoiceLineIDsBySaleLine reads the links back.
	LinkInvoiceLines(ctx context.Context, saleID uuid.UUID, links map[uuid.UUID]uuid.UUID) error
	InvoiceLineIDsBySaleLine(ctx context.Context, saleID uuid.UUID) (map[uuid.UUID]uuid.UUID, error)

	// Till sessions
	// LockTillSession locks a session row (FOR SHARE for the acts that live
	// in it, FOR UPDATE for the close), inside the caller's transaction.
	LockTillSession(ctx context.Context, id uuid.UUID, forUpdate bool) error
	CreateTillSession(ctx context.Context, s *TillSession) error
	GetTillSession(ctx context.Context, id uuid.UUID) (*TillSession, error)
	GetOpenTillSession(ctx context.Context, registerID string) (*TillSession, error)
	CloseTillSession(ctx context.Context, s *TillSession) error
	AggregateTillSession(ctx context.Context, sessionID uuid.UUID) (*TillAggregate, error)

	// Z-reports
	CreateZReport(ctx context.Context, z *ZReport) error
	GetZReportBySession(ctx context.Context, sessionID uuid.UUID) (*ZReport, error)
	ListZReports(ctx context.Context, registerID string, date time.Time) ([]ZReport, error)

	// LockCustomerCredit serializes the acts that read one customer's
	// credit exposure (section 11, step 1a: the same advisory lock the
	// confirm and the fulfilment take).
	LockCustomerCredit(ctx context.Context, customerID uuid.UUID) error

	// Refund intents (ADR 0005 section 11: the gateway is called before the
	// transaction, so a committed claim is what serializes the card refunds
	// of a tender). InsertRefundIntent writes one PENDING claim through the
	// caller's transaction (a live claim of the same tender answers 409
	// refund_in_flight before any gateway call); CompleteRefundIntent marks
	// the claim done inside the money transaction that posts the refund;
	// FailRefundIntent releases the claim of a gateway call that failed.
	// RefundIntentFacts reads the table's authority for one tender (what its
	// live claims hold and whether a void already refunded it whole);
	// SaleHasPendingRefundIntent answers whether any claim of a sale still
	// stands; SessionPendingRefundIntentIDs lists the ids of the standing
	// claims of a session's sales, so the close's refusal can name them.
	InsertRefundIntent(ctx context.Context, intent RefundIntent) error
	CompleteRefundIntent(ctx context.Context, saleID, tenderID uuid.UUID, refundTxID string) error
	FailRefundIntent(ctx context.Context, saleID, tenderID uuid.UUID) error
	RefundIntentFacts(ctx context.Context, saleID, tenderID uuid.UUID) (RefundIntentFacts, error)
	SaleHasPendingRefundIntent(ctx context.Context, saleID uuid.UUID) (bool, error)
	PendingVoidClaimsOfInvoice(ctx context.Context, invoiceID uuid.UUID) ([]uuid.UUID, error)
	// HeldTenderCreditOfPayment answers the declined void tender credit a
	// payment still holds (the account core's held credit guard, C2-5,
	// FROM-PRINCIPAL 60): the void completed, one of its card tenders' refund
	// claims was released (declined or recorded as a no refund), and that
	// tender's payment still carries the unapplied cents the completion
	// reversed onto it.
	HeldTenderCreditOfPayment(ctx context.Context, paymentID uuid.UUID) (TenderCreditFacts, bool, error)
	// TenderPaymentFacts reads a tender's payment and its unapplied cents
	// (the payback route's credit read).
	TenderPaymentFacts(ctx context.Context, tenderID uuid.UUID) (uuid.UUID, int64, error)
	// LockTenderPaymentFacts is that read under the payment row's lock (the
	// payback's claim transaction rechecks the credit there); TenderHasDonePayback
	// answers whether the tender's credit was already paid back.
	LockTenderPaymentFacts(ctx context.Context, tenderID uuid.UUID) (uuid.UUID, int64, error)
	TenderHasDonePayback(ctx context.Context, tenderID uuid.UUID) (bool, error)
	SessionPendingRefundIntentIDs(ctx context.Context, sessionID uuid.UUID) ([]uuid.UUID, error)
	// PendingRefundIntentIDs lists the ids of the sale's standing claims (the
	// per tender void resolution reads them to know when every tender is
	// resolved); SaleHasVoidIntentDone answers whether the sale's void already
	// booked a tender's refund (its goods were paid back at the terminal).
	PendingRefundIntentIDs(ctx context.Context, saleID uuid.UUID) ([]uuid.UUID, error)
	SaleHasVoidIntentDone(ctx context.Context, saleID uuid.UUID) (bool, error)
	// MarkRefundIntentRefunded writes the gateway's own refund id onto the
	// claim in its own committed write, the moment the terminal answers,
	// whatever state the claim is in; StampRefundIntentAnswered records that
	// the call came back carrying no verdict, and
	// MarkRefundIntentUnansweredUnknown marks a claim a resolution settles on
	// an expired lease (the terminal never spoke); GetRefundIntent reads one
	// claim whole, LockRefundIntent reads one claim whole under its row lock
	// (the resolution's transaction), and FailRefundIntentByID releases one
	// claim by its id (the operator resolution's no refund leg).
	MarkRefundIntentRefunded(ctx context.Context, intentID uuid.UUID, refundTxID string) error
	StampRefundIntentAnswered(ctx context.Context, intentID uuid.UUID) error
	MarkRefundIntentUnansweredUnknown(ctx context.Context, intentID uuid.UUID) error
	AbandonRefundIntent(ctx context.Context, intentID uuid.UUID) error
	GetRefundIntent(ctx context.Context, id uuid.UUID) (*RefundIntentRecord, error)
	LockRefundIntent(ctx context.Context, id uuid.UUID) (*RefundIntentRecord, error)
	FailRefundIntentByID(ctx context.Context, id uuid.UUID) error
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
	SaleCount        int
	SalesTotalCents  int64
	TaxTotalCents    int64
	ChangeCents      int64
	TenderedByMethod map[string]int64
	CashRefundsCents int64
	CardRefundsCents int64
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
		lineType := l.LineType
		if lineType == "" {
			lineType = "PRODUCT"
		}
		_, err := r.ex(ctx).Exec(ctx, `
			INSERT INTO pos_return_lines (id, return_id, position, line_type, product_id, description, quantity, uom,
				unit_price, line_total, restock, sale_line_id, created_at)
			VALUES ($1, $2, $3, $12, $4, $5, -($6::numeric / 10000), $7, $8::numeric / 10000, $9::numeric / 100, $10, $11, NOW())`,
			l.ID, ret.ID, l.Position, l.ProductID, l.Description, qtyArg(l.Quantity), l.UOM, priceArg(l.UnitPrice),
			centsArg(l.LineTotal), l.Restock, l.SaleLineID, lineType)
		if err != nil {
			return fmt.Errorf("failed to record the return line: %w", mapWriteError(err))
		}
	}
	return nil
}

// ReturnedQtyByLine sums what earlier returns brought back per sale line of
// one sale: the stored quantities are negative, so the sum is negated back.
func (r *PostgresRepository) ReturnedQtyByLine(ctx context.Context, saleID uuid.UUID) (map[uuid.UUID]httpx.Quantity, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT rl.sale_line_id, -ROUND(SUM(rl.quantity) * 10000)::bigint
		FROM pos_return_lines rl
		JOIN pos_returns r ON r.id = rl.return_id
		WHERE r.original_transaction_id = $1 AND rl.sale_line_id IS NOT NULL
		GROUP BY rl.sale_line_id`, saleID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the sale's earlier returns: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]httpx.Quantity{}
	for rows.Next() {
		var id uuid.UUID
		var qty httpx.Quantity
		if err := rows.Scan(&id, &qty); err != nil {
			return nil, err
		}
		out[id] = qty
	}
	return out, rows.Err()
}

// SaleHasReturns answers whether any return names the sale.
func (r *PostgresRepository) SaleHasReturns(ctx context.Context, saleID uuid.UUID) (bool, error) {
	var exists bool
	err := r.ex(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pos_returns WHERE original_transaction_id = $1)`, saleID).Scan(&exists)
	return exists, err
}

// RefundedCentsByLine sums, per sale line, the cents every earlier return of
// the sale refunded against it (the stored line totals are negative, so the
// sum is negated back): the remainder rule reads it, so repeated partial
// returns never refund more than the line was paid.
func (r *PostgresRepository) RefundedCentsByLine(ctx context.Context, saleID uuid.UUID) (map[uuid.UUID]int64, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT rl.sale_line_id, -ROUND(SUM(rl.line_total) * 100)::bigint
		FROM pos_return_lines rl
		JOIN pos_returns r ON r.id = rl.return_id
		WHERE r.original_transaction_id = $1 AND rl.sale_line_id IS NOT NULL
		GROUP BY rl.sale_line_id`, saleID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the sale's refunded cents: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]int64{}
	for rows.Next() {
		var id uuid.UUID
		var cents int64
		if err := rows.Scan(&id, &cents); err != nil {
			return nil, err
		}
		out[id] = cents
	}
	return out, rows.Err()
}

// RefundedTaxCents sums the tax every earlier return of the sale took back.
// Each row is read with its own sign: the contract writes negative amounts,
// the legacy rows migration 105 leaves carry positive ones (105 negates only
// while copying into credit memos), and either way the tax came back.
func (r *PostgresRepository) RefundedTaxCents(ctx context.Context, saleID uuid.UUID) (int64, error) {
	var cents int64
	err := r.ex(ctx).QueryRow(ctx,
		`SELECT COALESCE(ROUND(SUM(ABS(tax_amount)) * 100)::bigint, 0) FROM pos_returns WHERE original_transaction_id = $1`,
		saleID).Scan(&cents)
	if err != nil {
		return 0, fmt.Errorf("failed to read the sale's refunded tax: %w", err)
	}
	return cents, nil
}

// CardRefundedCents sums the cents the card returns of the sale already
// took, each row with its own sign (the contract negative, the legacy rows
// migration 105 leaves positive): a card return is capped at the card tender
// less this.
func (r *PostgresRepository) CardRefundedCents(ctx context.Context, saleID uuid.UUID) (int64, error) {
	var cents int64
	err := r.ex(ctx).QueryRow(ctx,
		`SELECT COALESCE(ROUND(SUM(ABS(total)) * 100)::bigint, 0) FROM pos_returns
		 WHERE original_transaction_id = $1 AND refund_method = 'CARD'`, saleID).Scan(&cents)
	if err != nil {
		return 0, fmt.Errorf("failed to read the sale's card refunds: %w", err)
	}
	return cents, nil
}

// LegacyReturnedQtyByProduct sums, per product, the quantity the sale's
// legacy return lines brought back: the lines migration 105 could not link
// to a sale line (their sale_line_id is null). Each line is read with its
// own sign: the base commit wrote positive quantities and 105 leaves them,
// while a free (unlinked) return the contract writes is negative.
func (r *PostgresRepository) LegacyReturnedQtyByProduct(ctx context.Context, saleID uuid.UUID) (map[uuid.UUID]httpx.Quantity, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT rl.product_id, ROUND(SUM(ABS(rl.quantity)) * 10000)::bigint
		FROM pos_return_lines rl
		JOIN pos_returns r ON r.id = rl.return_id
		WHERE r.original_transaction_id = $1 AND rl.sale_line_id IS NULL AND rl.product_id IS NOT NULL
		GROUP BY rl.product_id`, saleID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the sale's unlinked return lines: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]httpx.Quantity{}
	for rows.Next() {
		var id uuid.UUID
		var qty httpx.Quantity
		if err := rows.Scan(&id, &qty); err != nil {
			return nil, err
		}
		out[id] = qty
	}
	return out, rows.Err()
}

// LegacyRefundedCentsByProduct sums, per product, the cents the sale's
// legacy (unlinked) return lines refunded, each line with its own sign: the
// remainder rule counts them, so the goods bound the money whichever route
// brought them back.
func (r *PostgresRepository) LegacyRefundedCentsByProduct(ctx context.Context, saleID uuid.UUID) (map[uuid.UUID]int64, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT rl.product_id, ROUND(SUM(ABS(rl.line_total)) * 100)::bigint
		FROM pos_return_lines rl
		JOIN pos_returns r ON r.id = rl.return_id
		WHERE r.original_transaction_id = $1 AND rl.sale_line_id IS NULL AND rl.product_id IS NOT NULL
		GROUP BY rl.product_id`, saleID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the sale's unlinked refund cents: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]int64{}
	for rows.Next() {
		var id uuid.UUID
		var cents int64
		if err := rows.Scan(&id, &cents); err != nil {
			return nil, err
		}
		out[id] = cents
	}
	return out, rows.Err()
}

// RefundIntent is one committed claim to refund a tender at the gateway: the
// row that serializes the card refunds of a tender (sale id, tender id,
// amount, state pending) because the gateway is called before the money
// transaction (ADR 0005 section 11) and no row lock can span the call.
// Request carries a return claim's own request shape (JSON), so the operator
// resolution can replay the money transaction the lost act never ran.
type RefundIntent struct {
	ID          uuid.UUID
	SaleID      uuid.UUID
	TenderID    uuid.UUID
	GatewayTxID string
	AmountCents int64
	// Act names the act behind the refund ("void" or "return").
	Act string
	// Request is the stored return request of a return claim, nil on a void.
	Request []byte
}

// InsertRefundIntent writes one PENDING refund intent through the caller's
// transaction (the claim's own short transaction: never inside the money
// transaction). The partial unique index (sale id, tender id) makes the
// claim exclusive: a second void of the sale, or a card return of the same
// tender, is refused here with 409 refund_in_flight before it ever reaches
// the terminal. The insert's foreign keys take only FOR KEY SHARE, so the
// claim never blocks behind a money transaction's sale row lock.
func (r *PostgresRepository) InsertRefundIntent(ctx context.Context, intent RefundIntent) error {
	if intent.ID == uuid.Nil {
		intent.ID = uuid.New()
	}
	_, err := r.ex(ctx).Exec(ctx, `
		INSERT INTO pos_refund_intents (id, sale_id, tender_id, gateway_tx_id, amount, act, state, request, answer_due_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::numeric / 100, $6, 'PENDING', $7, NOW() + make_interval(secs => $8), NOW(), NOW())`,
		intent.ID, intent.SaleID, intent.TenderID, intent.GatewayTxID, intent.AmountCents, intent.Act, intent.Request,
		int64(refundAnswerLease.Seconds()))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return conflict("refund_in_flight",
				"a card refund of this sale's tender is already in flight: only one refund of a tender runs at a time")
		}
		return fmt.Errorf("failed to open the refund intent: %w", mapWriteError(err))
	}
	return nil
}

// CompleteRefundIntent marks the tender's live intent done, inside the money
// transaction that posts the refund, carrying the gateway's own refund id.
// A refund the transaction then refuses rolls this back with everything
// else: the intent stays PENDING beside its orphaned refund row, recording
// the money that moved. A call that settles no row (the claim left PENDING
// underneath, a concurrent resolution winning it) refuses 409
// intent_not_pending, so no booking ever stands beside a settled claim
// (ninth review P2-2, tenth review P2-1).
func (r *PostgresRepository) CompleteRefundIntent(ctx context.Context, saleID, tenderID uuid.UUID, refundTxID string) error {
	tag, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_refund_intents SET state = 'DONE', refund_tx_id = NULLIF($3, ''), updated_at = NOW()
		WHERE sale_id = $1 AND tender_id = $2 AND state = 'PENDING'`, saleID, tenderID, refundTxID)
	if err != nil {
		return fmt.Errorf("failed to complete the refund intent: %w", mapWriteError(err))
	}
	if tag.RowsAffected() == 0 {
		return conflict("intent_not_pending", "the refund claim was settled by another resolution first")
	}
	return nil
}

// FailRefundIntent releases the tender's live intent after its gateway call
// failed: no money moved for it, so the tender may be refunded again. The
// decline is the terminal's own answer, so the claim's answer is stamped and
// its state leaves IN_FLIGHT. A call that settles no row answers the same
// intent_not_pending refusal; the decline path's caller treats it as settled
// by another hand, never as a failure of its own release.
func (r *PostgresRepository) FailRefundIntent(ctx context.Context, saleID, tenderID uuid.UUID) error {
	tag, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_refund_intents SET state = 'FAILED', answered_at = NOW(), answer_state = 'ANSWERED', updated_at = NOW()
		WHERE sale_id = $1 AND tender_id = $2 AND state = 'PENDING'`, saleID, tenderID)
	if err != nil {
		return fmt.Errorf("failed to release the refund intent: %w", mapWriteError(err))
	}
	if tag.RowsAffected() == 0 {
		return conflict("intent_not_pending", "the refund claim was settled by another resolution first")
	}
	return nil
}

// MarkRefundIntentRefunded writes the gateway's own refund id onto the
// claim, whatever state the claim is in (fourteenth review P1-1: a wrong early release can
// never orphan the terminal's id), and stamps the answer in its own committed
// write, made the moment the terminal answers and before the money
// transaction opens: a claim carrying a refund id is a refund the terminal
// moved, one without it is a refund that may not have moved, so a claim that
// outlives its act can be told apart and resolved (seventh review P2-3).
func (r *PostgresRepository) MarkRefundIntentRefunded(ctx context.Context, intentID uuid.UUID, refundTxID string) error {
	tag, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_refund_intents SET refund_tx_id = NULLIF($2, ''), answered_at = NOW(), answer_state = 'ANSWERED', updated_at = NOW()
		WHERE id = $1`, intentID, refundTxID)
	if err != nil {
		return fmt.Errorf("failed to record the refund id on the intent: %w", mapWriteError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("the refund claim %s is gone: the terminal's refund id %s was not recorded", intentID, refundTxID)
	}
	return nil
}

// StampRefundIntentAnswered records that the claim's own terminal call came
// back carrying no verdict (a transport error, a status that proves
// nothing): the call is off the wire, so the claim is stranded and
// resolvable, exactly as the unknown outcome row records it (fourteenth review P1-1).
func (r *PostgresRepository) StampRefundIntentAnswered(ctx context.Context, intentID uuid.UUID) error {
	_, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_refund_intents SET answered_at = NOW(), answer_state = 'ANSWERED', updated_at = NOW()
		WHERE id = $1 AND answered_at IS NULL`, intentID)
	if err != nil {
		return fmt.Errorf("failed to stamp the claim's answer: %w", mapWriteError(err))
	}
	return nil
}

// MarkRefundIntentUnansweredUnknown marks a claim UNKNOWN when a resolution
// proceeds on its in flight lease expiring with no answer at all: the
// terminal never spoke, and the claim's own record must say so (fourteenth review P1-1).
// A claim the terminal did answer is left alone.
func (r *PostgresRepository) MarkRefundIntentUnansweredUnknown(ctx context.Context, intentID uuid.UUID) error {
	_, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_refund_intents SET answer_state = 'UNKNOWN', updated_at = NOW()
		WHERE id = $1 AND answered_at IS NULL`, intentID)
	if err != nil {
		return fmt.Errorf("failed to mark the unanswered claim: %w", mapWriteError(err))
	}
	return nil
}

// AbandonRefundIntent records that the claim's act ended before the terminal
// was ever asked for it (a void that stopped at an earlier tender's refusal,
// fourteenth review P1-1): no answer can ever come back, so the claim is stranded from now
// and its record says the terminal never spoke.
func (r *PostgresRepository) AbandonRefundIntent(ctx context.Context, intentID uuid.UUID) error {
	_, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_refund_intents SET answered_at = NOW(), answer_state = 'UNKNOWN', updated_at = NOW()
		WHERE id = $1 AND answered_at IS NULL`, intentID)
	if err != nil {
		return fmt.Errorf("failed to abandon the never called claim: %w", mapWriteError(err))
	}
	return nil
}

// RefundIntentRecord is one claim row read whole, for the operator resolving
// it: what it claimed, where it stands, and whether the terminal answered.
type RefundIntentRecord struct {
	RefundIntent
	State      string
	RefundTxID string
	// The in flight lease (fourteenth review P1-1): AnswerDueAt is when the claim may be
	// resolved if its own terminal call never settles it; AnsweredAt is
	// stamped by every path that ends that call; AnswerState carries
	// IN_FLIGHT, ANSWERED or UNKNOWN (a resolution on an expired lease).
	AnswerDueAt time.Time
	AnsweredAt  *time.Time
	AnswerState string
	ResolvedAt  *time.Time
	CreatedAt   time.Time
}

// GetRefundIntent reads one claim whole.
func (r *PostgresRepository) GetRefundIntent(ctx context.Context, id uuid.UUID) (*RefundIntentRecord, error) {
	var rec RefundIntentRecord
	var request []byte
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT id, sale_id, tender_id, gateway_tx_id, ROUND(amount * 100)::bigint, act, state,
			COALESCE(refund_tx_id, ''), answer_due_at, answered_at, answer_state, request, created_at, updated_at
		FROM pos_refund_intents WHERE id = $1`, id).
		Scan(&rec.ID, &rec.SaleID, &rec.TenderID, &rec.GatewayTxID, &rec.AmountCents, &rec.Act, &rec.State,
			&rec.RefundTxID, &rec.AnswerDueAt, &rec.AnsweredAt, &rec.AnswerState, &request, &rec.CreatedAt, &rec.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("refund claim not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the refund intent: %w", err)
	}
	rec.Request = request
	return &rec, nil
}

// PendingRefundIntentIDs lists the ids of the sale's standing claims in
// creation order: the per tender void resolution completes the sale's void
// only when the list is empty.
func (r *PostgresRepository) PendingRefundIntentIDs(ctx context.Context, saleID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.ex(ctx).Query(ctx,
		`SELECT id FROM pos_refund_intents WHERE sale_id = $1 AND state = 'PENDING' ORDER BY created_at, id`, saleID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the sale's standing claims: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SaleHasVoidIntentDone answers whether the sale's void already booked a
// card tender's refund: the goods' money moved at the terminal, so no other
// method may pay the goods back.
func (r *PostgresRepository) SaleHasVoidIntentDone(ctx context.Context, saleID uuid.UUID) (bool, error) {
	var done bool
	err := r.ex(ctx).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pos_refund_intents WHERE sale_id = $1 AND act = 'void' AND state = 'DONE')`, saleID).
		Scan(&done)
	if err != nil {
		return false, fmt.Errorf("failed to read the sale's void claims: %w", err)
	}
	return done, nil
}

// LockRefundIntent reads one claim whole under its row lock: the operator
// resolution's transaction re-reads the claim it is about to settle, so a
// concurrent resolution of the same claim loses here instead of booking
// beside it.
func (r *PostgresRepository) LockRefundIntent(ctx context.Context, id uuid.UUID) (*RefundIntentRecord, error) {
	var rec RefundIntentRecord
	var request []byte
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT id, sale_id, tender_id, gateway_tx_id, ROUND(amount * 100)::bigint, act, state,
			COALESCE(refund_tx_id, ''), answer_due_at, answered_at, answer_state, request, created_at, updated_at
		FROM pos_refund_intents WHERE id = $1 FOR UPDATE`, id).
		Scan(&rec.ID, &rec.SaleID, &rec.TenderID, &rec.GatewayTxID, &rec.AmountCents, &rec.Act, &rec.State,
			&rec.RefundTxID, &rec.AnswerDueAt, &rec.AnsweredAt, &rec.AnswerState, &request, &rec.CreatedAt, &rec.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("refund claim not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock the refund intent: %w", err)
	}
	rec.Request = request
	return &rec, nil
}

// FailRefundIntentByID releases one claim by its id: the operator
// resolution's no refund leg, which moves nothing.
func (r *PostgresRepository) FailRefundIntentByID(ctx context.Context, id uuid.UUID) error {
	tag, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_refund_intents SET state = 'FAILED', updated_at = NOW()
		WHERE id = $1 AND state = 'PENDING'`, id)
	if err != nil {
		return fmt.Errorf("failed to release the refund intent: %w", mapWriteError(err))
	}
	if tag.RowsAffected() == 0 {
		return conflict("intent_not_pending", "the refund claim was settled by another resolution first")
	}
	return nil
}

// RefundIntentFacts is the intent table's authority on one tender: what its
// standing claims hold, whether a void's intent already refunded the tender
// whole, and the cents of refunds the terminal moved that never booked.
type RefundIntentFacts struct {
	PendingCents int64
	DoneVoid     bool
	// UnbookedRefundedCents sums the settled claims that carry the terminal's
	// refund id without a booking of their own (the fourteenth review's P1-1 defence in depth:
	// a late refund behind a wrongly released claim). A PENDING claim's own
	// booking names its refund id, so PENDING never counts here; the tender
	// that paid once at the terminal is never asked to pay again.
	UnbookedRefundedCents int64
}

// RefundIntentFacts reads the tender's intents: the cents its PENDING claims
// hold (claimed at the terminal, not yet booked, so no pos_returns row counts
// them), whether any DONE intent of act void refunded the tender whole, and
// the cents of recorded refunds that never booked. The DONE return intents
// need no field of their own: their booking commits the pos_returns row
// beside them in one transaction, so CardRefundedCents already counts them.
func (r *PostgresRepository) RefundIntentFacts(ctx context.Context, saleID, tenderID uuid.UUID) (RefundIntentFacts, error) {
	var facts RefundIntentFacts
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT COALESCE(ROUND(SUM(CASE WHEN state = 'PENDING' THEN ABS(amount) ELSE 0 END) * 100)::bigint, 0),
			COALESCE(BOOL_OR(act = 'void' AND state = 'DONE'), FALSE),
			COALESCE(ROUND(SUM(CASE WHEN refund_tx_id IS NOT NULL AND state NOT IN ('DONE', 'PENDING') THEN ABS(amount) ELSE 0 END) * 100)::bigint, 0)
		FROM pos_refund_intents WHERE sale_id = $1 AND tender_id = $2`, saleID, tenderID).
		Scan(&facts.PendingCents, &facts.DoneVoid, &facts.UnbookedRefundedCents)
	if err != nil {
		return facts, fmt.Errorf("failed to read the tender's refund intents: %w", err)
	}
	return facts, nil
}

// SaleHasPendingRefundIntent answers whether any card refund claim of the
// sale still stands: a claim whose terminal call or money transaction has not
// settled yet.
func (r *PostgresRepository) SaleHasPendingRefundIntent(ctx context.Context, saleID uuid.UUID) (bool, error) {
	var pending bool
	err := r.ex(ctx).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pos_refund_intents WHERE sale_id = $1 AND state = 'PENDING')`, saleID).
		Scan(&pending)
	if err != nil {
		return false, fmt.Errorf("failed to read the sale's refund intents: %w", err)
	}
	return pending, nil
}

// TenderCreditFacts is the declined void tender credit one payment still
// holds: the origin names the void's sale and the claim the void released,
// and Cents is what the payment still carries unapplied of it.
type TenderCreditFacts struct {
	IntentID   uuid.UUID
	TenderID   uuid.UUID
	SaleID     uuid.UUID
	SaleNumber string
	Cents      int64
}

// HeldTenderCreditOfPayment answers whether the payment's unapplied cash is
// a credit held to one of the counter's card tenders (C2-5,
// FROM-PRINCIPAL 60): the tender's sale was voided, the tender's own refund
// claim was released by the terminal's decline (or a recorded no refund),
// and the void's completion reversed the tender's application onto the
// payment, where the cents wait for their one way back. A payment nothing
// of the counter holds, or one whose credit was already paid back (its
// unapplied cash is gone), answers false.
func (r *PostgresRepository) HeldTenderCreditOfPayment(ctx context.Context, paymentID uuid.UUID) (TenderCreditFacts, bool, error) {
	var facts TenderCreditFacts
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT i.id, i.tender_id, i.sale_id, t.number, ROUND(p.amount_unapplied * 100)::bigint
		FROM payments p
			JOIN pos_tenders d ON d.payment_id = p.id AND d.method = 'CARD'
			JOIN pos_transactions t ON t.id = d.transaction_id AND t.status = 'VOIDED'
			JOIN pos_refund_intents i ON i.sale_id = t.id AND i.tender_id = d.id
				AND i.act = 'void' AND i.state = 'FAILED'
		WHERE p.id = $1 AND p.status = 'POSTED' AND ROUND(p.amount_unapplied * 100)::bigint > 0
		ORDER BY i.created_at, i.id
		LIMIT 1`, paymentID).
		Scan(&facts.IntentID, &facts.TenderID, &facts.SaleID, &facts.SaleNumber, &facts.Cents)
	if errors.Is(err, pgx.ErrNoRows) {
		return facts, false, nil
	}
	if err != nil {
		return facts, false, fmt.Errorf("failed to read the payment's held tender credit: %w", err)
	}
	return facts, true, nil
}

// TenderPaymentFacts reads the tender's payment and the unapplied cents it
// carries: the payback route's credit read, before any terminal call.
func (r *PostgresRepository) TenderPaymentFacts(ctx context.Context, tenderID uuid.UUID) (uuid.UUID, int64, error) {
	var paymentID uuid.UUID
	var unapplied int64
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT d.payment_id, ROUND(p.amount_unapplied * 100)::bigint
		FROM pos_tenders d JOIN payments p ON p.id = d.payment_id
		WHERE d.id = $1 AND d.payment_id IS NOT NULL AND p.status = 'POSTED'`, tenderID).
		Scan(&paymentID, &unapplied)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, 0, conflict("no_held_credit", "the tender names no posted payment: no credit is held on it")
	}
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("failed to read the tender's payment: %w", err)
	}
	return paymentID, unapplied, nil
}

// LockTenderPaymentFacts is TenderPaymentFacts under the payment row's lock,
// for the payback's claim transaction: the credit is rechecked on the row
// the money transaction books it out of, so a payback whose earlier read saw
// the credit standing opens no claim beside a first payback's booking (the
// sixteenth review's P1-1). Runs through the caller's transaction.
func (r *PostgresRepository) LockTenderPaymentFacts(ctx context.Context, tenderID uuid.UUID) (uuid.UUID, int64, error) {
	var paymentID uuid.UUID
	var unapplied int64
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT d.payment_id, ROUND(p.amount_unapplied * 100)::bigint
		FROM pos_tenders d JOIN payments p ON p.id = d.payment_id
		WHERE d.id = $1 AND d.payment_id IS NOT NULL AND p.status = 'POSTED'
		FOR UPDATE OF p`, tenderID).
		Scan(&paymentID, &unapplied)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, 0, conflict("no_held_credit", "the tender names no posted payment: no credit is held on it")
	}
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("failed to lock the tender's payment: %w", err)
	}
	return paymentID, unapplied, nil
}

// TenderHasDonePayback answers whether the tender's credit was already paid
// back: a DONE payback claim stands on it. The payback's claim transaction
// rechecks this under the payment row's lock, and migration 111's partial
// unique index makes the same rule structural.
func (r *PostgresRepository) TenderHasDonePayback(ctx context.Context, tenderID uuid.UUID) (bool, error) {
	var done bool
	err := r.ex(ctx).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pos_refund_intents WHERE tender_id = $1 AND act = 'payback' AND state = 'DONE')`, tenderID).
		Scan(&done)
	if err != nil {
		return false, fmt.Errorf("failed to read the tender's payback claims: %w", err)
	}
	return done, nil
}

// PendingVoidClaimsOfInvoice lists the standing void claims of the sale that
// owns the invoice, in creation order: the account core's application guard
// (C2-5, the fourteenth review's P2-1) names them when it refuses an application to
// the invoice's interim open cents.
func (r *PostgresRepository) PendingVoidClaimsOfInvoice(ctx context.Context, invoiceID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT i.id FROM pos_refund_intents i
			JOIN pos_transactions t ON t.id = i.sale_id
		WHERE t.invoice_id = $1 AND i.act = 'void' AND i.state = 'PENDING'
		ORDER BY i.created_at, i.id`, invoiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the invoice's void claims: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SessionPendingRefundIntentIDs lists the ids of the card refund claims of
// the session's sales that still stand, in creation order: the drawer cannot
// be counted and closed while a terminal refund of one of its sales may
// still move money, and the refusal names the claims so the cashier knows
// who unblocks the drawer.
func (r *PostgresRepository) SessionPendingRefundIntentIDs(ctx context.Context, sessionID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT i.id FROM pos_refund_intents i
			JOIN pos_transactions t ON t.id = i.sale_id
			WHERE t.till_session_id = $1 AND i.state = 'PENDING'
			ORDER BY i.created_at, i.id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the session's refund intents: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// InvoiceLineCost is one invoice line's id and the unit cost the sale
// relieved (ten thousandths).
type InvoiceLineCost struct {
	ID       uuid.UUID
	UnitCost int64
}

// InvoiceLineCosts reads an invoice's lines with their unit costs: a linked
// return finds its source line's cost by the invoice line id completion
// stored on the sale line (never by a position: a removed cart line leaves
// the sale's positions gapped while the invoice numbers its own from zero).
func (r *PostgresRepository) InvoiceLineCosts(ctx context.Context, invoiceID uuid.UUID) ([]InvoiceLineCost, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, COALESCE(ROUND(unit_cost * 10000)::bigint, 0)
		FROM invoice_lines WHERE invoice_id = $1 ORDER BY position`, invoiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the invoice's line costs: %w", err)
	}
	defer rows.Close()
	var out []InvoiceLineCost
	for rows.Next() {
		var l InvoiceLineCost
		if err := rows.Scan(&l.ID, &l.UnitCost); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LinkInvoiceLines records on each sale line the invoice line completion
// built from it, inside the completion's transaction.
func (r *PostgresRepository) LinkInvoiceLines(ctx context.Context, saleID uuid.UUID, links map[uuid.UUID]uuid.UUID) error {
	if len(links) == 0 {
		return nil
	}
	saleLines := make([]uuid.UUID, 0, len(links))
	invLines := make([]uuid.UUID, 0, len(links))
	for saleLine, invLine := range links {
		saleLines = append(saleLines, saleLine)
		invLines = append(invLines, invLine)
	}
	if _, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_line_items l SET invoice_line_id = v.inv
		FROM unnest($1::uuid[], $2::uuid[]) AS v(sid, inv)
		WHERE l.id = v.sid AND l.transaction_id = $3`, saleLines, invLines, saleID); err != nil {
		return fmt.Errorf("failed to link the sale lines to their invoice lines: %w", mapWriteError(err))
	}
	return nil
}

// InvoiceLineIDsBySaleLine reads the invoice line each sale line became.
func (r *PostgresRepository) InvoiceLineIDsBySaleLine(ctx context.Context, saleID uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, invoice_line_id FROM pos_line_items
		WHERE transaction_id = $1 AND invoice_line_id IS NOT NULL`, saleID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the sale lines' invoice links: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]uuid.UUID{}
	for rows.Next() {
		var saleLine, invLine uuid.UUID
		if err := rows.Scan(&saleLine, &invLine); err != nil {
			return nil, err
		}
		out[saleLine] = invLine
	}
	return out, rows.Err()
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

// LockCustomerCredit takes the transaction scoped advisory lock keyed on
// the customer, the order module's own key ('order-credit:<customer id>'),
// so a counter ACCOUNT sale and an order confirm serialize on one
// customer's credit exposure.
func (r *PostgresRepository) LockCustomerCredit(ctx context.Context, customerID uuid.UUID) error {
	if _, err := r.ex(ctx).Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('order-credit:' || $1::text, 0))`, customerID.String()); err != nil {
		return fmt.Errorf("failed to serialize the customer's credit acts: %w", err)
	}
	return nil
}

func (r *PostgresRepository) GetRegisterBranch(ctx context.Context, registerID string) (*uuid.UUID, error) {
	var branch *uuid.UUID
	err := r.ex(ctx).QueryRow(ctx, `SELECT COALESCE(l.branch_id, pr.branch_id) FROM pos_registers pr
		LEFT JOIN locations l ON l.id = pr.location_id
		WHERE pr.id = $1`, registerID).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("no such register")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the register's branch: %w", err)
	}
	// The register is held to the caller's branch context (PR 83's round 7
	// predicate): a register of another branch is refused by name, never a
	// missing row, and nothing of the act is written.
	if branch != nil {
		if held := branchctx.IDForQuery(ctx); held != nil && *held != *branch {
			return nil, httpx.Forbidden(
				fmt.Sprintf("register %s belongs to a different branch than the request", registerID))
		}
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
