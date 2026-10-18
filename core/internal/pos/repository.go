// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/customer/customerlink"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/platform/pictures"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/units"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
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
	// AttachSaleToTill puts a cart that has no drawer into one.
	AttachSaleToTill(ctx context.Context, saleID, sessionID uuid.UUID) error
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
	// The till's movements (CP-08).
	CreateTillMovement(ctx context.Context, m *TillMovement) error
	ListTillMovements(ctx context.Context, sessionID uuid.UUID) ([]TillMovement, error)
	ListTillMovementsPage(ctx context.Context, sessionID uuid.UUID, after *time.Time, afterID *uuid.UUID, limit int) ([]TillMovement, bool, error)
	TillMovementTotals(ctx context.Context, sessionID uuid.UUID) (map[string]int64, error)
	// The variance threshold and the sign off queue (CP-08).
	VarianceThreshold(ctx context.Context, branch *uuid.UUID) (int64, bool, error)
	SignOffTillSession(ctx context.Context, s *TillSession) error
	ListPendingSignoffs(ctx context.Context, branch *uuid.UUID, after *time.Time, afterID *uuid.UUID, limit int) ([]TillSession, bool, error)
	GetVariancePolicy(ctx context.Context, branch uuid.UUID) (*VariancePolicy, error)
	SetVariancePolicy(ctx context.Context, branch uuid.UUID, cents *int64, actor string) error
	// The branch day (CP-09).
	BranchZone(ctx context.Context, branch uuid.UUID) (string, bool, error)
	LockBranchDay(ctx context.Context, branch uuid.UUID) error
	BranchDayClosed(ctx context.Context, branch uuid.UUID) (bool, error)
	ListBranchDaySessions(ctx context.Context, branch uuid.UUID, date string) ([]TillSession, error)
	ListUnsettledSessions(ctx context.Context, branch uuid.UUID, date string) ([]TillSession, error)
	BranchCollectionsNet(ctx context.Context, branch uuid.UUID, date string) (httpx.Cents, error)
	DayCloseBranch(ctx context.Context, id uuid.UUID) (uuid.UUID, bool, error)
	GetDayClose(ctx context.Context, branch uuid.UUID, date string) (*DayClose, error)
	GetDayCloseByID(ctx context.Context, id uuid.UUID, forUpdate bool) (*DayClose, error)
	CreateDayClose(ctx context.Context, c *DayClose) error
	MatchDayClose(ctx context.Context, id uuid.UUID, m *DepositMatch) error
	ReopenDayClose(ctx context.Context, id uuid.UUID, actor, reason string, reversalID *uuid.UUID) error
	// BranchToday is the branch's business date now in its own zone.
	BranchToday(ctx context.Context, branch uuid.UUID) (time.Time, error)

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
	// RestampRefundIntentLease renews one claim's lease immediately before
	// its own terminal call (the void's sequential calls).
	RestampRefundIntentLease(ctx context.Context, intentID uuid.UUID) error
	StampRefundIntentAnswered(ctx context.Context, intentID uuid.UUID) error
	MarkRefundIntentUnansweredUnknown(ctx context.Context, intentID uuid.UUID) error
	AbandonRefundIntent(ctx context.Context, intentID uuid.UUID) error
	GetRefundIntent(ctx context.Context, id uuid.UUID) (*RefundIntentRecord, error)
	LockRefundIntent(ctx context.Context, id uuid.UUID) (*RefundIntentRecord, error)
	FailRefundIntentByID(ctx context.Context, id uuid.UUID) error
	// Lookups
	GetRegisterBranch(ctx context.Context, registerID string) (*uuid.UUID, error)
	// SaleBranch and TillSessionBranch read a record's own branch with no
	// wall, for the act hold that must decide before the walled read hides
	// the record (found false: the record does not exist).
	SaleBranch(ctx context.Context, id uuid.UUID) (branch uuid.UUID, found bool, err error)
	TillSessionBranch(ctx context.Context, id uuid.UUID) (branch *uuid.UUID, found bool, err error)
	LookupProducts(ctx context.Context, ids []uuid.UUID) (map[string]salesdoc.ProductRef, error)
	LookupProductUnits(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]salesdoc.ProductUnitSet, error)
	LookupCatalogue(ctx context.Context, codes []string) (map[string]units.CatalogueUnit, error)
	LookupKitComponents(ctx context.Context, kitIDs []uuid.UUID) (map[string][]salesdoc.KitComponent, error)
	LookupChargeCodes(ctx context.Context, codes []string) (map[string]salesdoc.ChargeCode, error)
	SearchProducts(ctx context.Context, query string, limit int) ([]QuickSearchResult, error)
	GetProductCatalog(ctx context.Context) ([]CatalogProduct, error)
	// CounterTiles reads the counter's catalogue for the caller's yard: the
	// category chips, the tiles in top seller order and the picture setting.
	CounterTiles(ctx context.Context, q TileQuery) (TileSet, error)
	WalkInCustomer(ctx context.Context) (uuid.UUID, string, error)
	CustomerFacts(ctx context.Context, customerID uuid.UUID) (CustomerFacts, error)
	// FindCounterCustomers searches the customers the caller's wall reaches
	// by name or account number, standing figures left to the service.
	FindCounterCustomers(ctx context.Context, q string, limit int) ([]CounterCustomer, error)
	// CounterCustomerByID reads one customer through the same wall; false
	// when it does not exist or the wall hides it.
	CounterCustomerByID(ctx context.Context, id uuid.UUID) (*CounterCustomer, bool, error)
	// ListRegisters reads the registers the caller's branch reach holds.
	ListRegisters(ctx context.Context) ([]CounterRegister, error)
	// SetSaleAccount names the customer, currency, purchase order and job of
	// an open sale and moves its revision.
	SetSaleAccount(ctx context.Context, saleID uuid.UUID, customerID *uuid.UUID, currency string, po *string, jobID *uuid.UUID) error
	CustomerExempt(ctx context.Context, customerID uuid.UUID) (bool, error)
	OpenReceivableCents(ctx context.Context, customerID uuid.UUID) (int64, error)
	// The counter's account sale reads and writes (migration 126): the open
	// store credit, the database wide check limit, the buyer roster and the
	// sale's buyer, and the manager override (one live per sale and kind,
	// spent by a single statement).
	OpenCreditMemoCents(ctx context.Context, customerID uuid.UUID) (int64, error)
	LockOpenCreditMemos(ctx context.Context, customerID uuid.UUID, currency string) ([]openMemo, error)
	LockCreditMemo(ctx context.Context, memoID uuid.UUID) (openMemo, error)
	LockCustomer(ctx context.Context, customerID uuid.UUID) error
	DefaultCheckLimitCents(ctx context.Context) (*int64, error)
	Buyers(ctx context.Context, customerID uuid.UUID) ([]Buyer, error)
	BuyerOf(ctx context.Context, customerID, contactID uuid.UUID) (*Buyer, bool, error)
	SetSaleBuyer(ctx context.Context, saleID uuid.UUID, contactID *uuid.UUID) error
	InsertSignature(ctx context.Context, sig *storedSignature) error
	GetSignature(ctx context.Context, saleID uuid.UUID) (*storedSignature, bool, error)
	BumpPrintCount(ctx context.Context, saleID uuid.UUID) (int, error)
	UnbumpPrintCount(ctx context.Context, saleID uuid.UUID) error
	ReceiptSaleByInvoice(ctx context.Context, invoiceID uuid.UUID) (*receiptSaleFacts, error)
	RestockingFeeBps(ctx context.Context) (map[string]int, error)
	LockStock(ctx context.Context, productIDs []uuid.UUID, branchID uuid.UUID) error
	LinkExchange(ctx context.Context, returnID, saleID uuid.UUID, method RefundMethod) error
	InsertOverride(ctx context.Context, o *AccountOverride) error
	LiveOverride(ctx context.Context, saleID uuid.UUID, kind string) (*AccountOverride, error)
	ConsumeOverride(ctx context.Context, saleID uuid.UUID, kind string, need int64) (*AccountOverride, bool, error)
	BranchLocalDate(ctx context.Context, branchID uuid.UUID, at time.Time) (time.Time, error)
	BranchTaxRate(ctx context.Context, branchID *uuid.UUID) (string, bool, error)

	// Offline sync
	SaleExists(ctx context.Context, id uuid.UUID) (bool, error)
	LogSyncBatch(ctx context.Context, batch LogBatch) error
}

// CustomerFacts is what the counter reads about the sale's customer: the
// effective currency and the credit limit the ACCOUNT tender checks. The read
// is unwalled: a caller applies the customer wall first (Service.namedCustomer).
type CustomerFacts struct {
	CreditLimitCents *int64
	Name             string
	Currency         string
	PORequired       bool
	// TermsKind is the terms master's kind of the customer's payment terms
	// (NET_DAYS, DAY_OF_MONTH, DUE_ON_RECEIPT), read from the master and never
	// from a text.
	TermsKind string
	// NamedBuyerRequired asks every on account sale to name a roster buyer.
	NamedBuyerRequired bool
	// CheckLimitCents is the customer's own check limit; null defers to the
	// database wide setting.
	CheckLimitCents *int64
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
	// MovementTotals sums the session's movements by kind (CP-08).
	MovementTotals map[string]int64
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
	db     *database.DB
	grants *middleware.BranchGuard
}

// NewRepository creates the counter's repository.
func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db, grants: middleware.NewBranchGuard(db)}
}

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
	// The sale, the customer's link to its yard and the link's audit row
	// commit together or not at all (ruling A, ADR 0007 section 5.8).
	return r.db.RunInTx(ctx, func(ctx context.Context) error {
		var created time.Time
		err := r.ex(ctx).QueryRow(ctx, `
		INSERT INTO pos_transactions (id, number, register_id, cashier_id, customer_id, currency, subtotal, tax_amount,
			total, change_due, status, till_session_id, branch_id, created_at, updated_at, revision)
		VALUES ($1, $2, $3, $4, $5, $6, 0, 0, 0, 0, $7, $8, $9, NOW(), NOW(), 1)
		RETURNING created_at, revision`,
			s.ID, s.Number, s.RegisterID, s.CashierID, s.CustomerID, s.Currency, StatusOpen, s.TillSessionID, s.BranchID,
		).Scan(&created, &s.Revision)
		if err != nil {
			return fmt.Errorf("failed to create the sale: %w", mapWriteError(err))
		}
		s.CreatedAt = httpx.TimestampOf(created)
		return customerlink.Ensure(ctx, r.db, "pos_transactions", s.ID)
	})
}

func (r *PostgresRepository) LockSale(ctx context.Context, id uuid.UUID) error {
	var one int
	err := r.ex(ctx).QueryRow(ctx, `SELECT 1 FROM pos_transactions WHERE id = $1 AND `+middleware.Wall("", 2, 3)+`
		FOR NO KEY UPDATE`, id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).Scan(&one)
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

// AttachSaleToTill puts a cart made before the open till rule into the
// register's open drawer, inside the completion's transaction.
func (r *PostgresRepository) AttachSaleToTill(ctx context.Context, saleID, sessionID uuid.UUID) error {
	if _, err := r.ex(ctx).Exec(ctx, `UPDATE pos_transactions SET till_session_id = $2 WHERE id = $1 AND till_session_id IS NULL`,
		saleID, sessionID); err != nil {
		return fmt.Errorf("failed to put the sale into the drawer: %w", err)
	}
	return nil
}

const saleHeadCols = `t.id, t.number, t.revision, t.branch_id, t.register_id, t.cashier_id, t.customer_id, t.currency,
	ROUND(t.subtotal * 100)::bigint, ROUND(t.tax_amount * 100)::bigint, ROUND(t.total * 100)::bigint,
	ROUND(t.change_due * 100)::bigint, t.till_session_id, t.status, t.invoice_id, t.completed_at, t.created_at`

// saleCols is the full sale's columns: the list item's head and the purchase
// order and job a charge sale names.
const saleCols = saleHeadCols + `, t.customer_po, t.job_id, t.buyer_contact_id, t.print_count, t.exchange_return_id`

func scanSale(row pgx.Row, s *Sale) error {
	var completed *time.Time
	var created time.Time
	if err := row.Scan(&s.ID, &s.Number, &s.Revision, &s.BranchID, &s.RegisterID, &s.CashierID, &s.CustomerID, &s.Currency,
		&s.SubtotalCents, &s.TaxCents, &s.TotalCents, &s.ChangeCents, &s.TillSessionID, &s.Status, &s.InvoiceID,
		&completed, &created, &s.CustomerPO, &s.JobID, &s.BuyerContactID, &s.PrintCount, &s.ExchangeReturnID); err != nil {
		return err
	}
	s.CompletedAt = httpx.PtrTimestamp(completed)
	s.CreatedAt = httpx.TimestampOf(created)
	return nil
}

func (r *PostgresRepository) GetSale(ctx context.Context, id uuid.UUID) (*Sale, error) {
	s := &Sale{}
	err := scanSale(r.ex(ctx).QueryRow(ctx, `SELECT `+saleCols+` FROM pos_transactions t
		WHERE t.id = $1 AND `+middleware.Wall("t", 2, 3), id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)), s)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("sale not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the sale: %w", err)
	}
	return s, nil
}

// SaleBranch reads a sale's own branch with no wall, for the act hold that
// must decide before the walled read hides the record from a caller held to
// the grants. A sale that does not exist answers found false.
func (r *PostgresRepository) SaleBranch(ctx context.Context, id uuid.UUID) (uuid.UUID, bool, error) {
	var branch uuid.UUID
	err := r.ex(ctx).QueryRow(ctx, `SELECT branch_id FROM pos_transactions WHERE id = $1`, id).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to read the sale's branch: %w", err)
	}
	return branch, true, nil
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
		AND ` + middleware.Wall("t", 5, 6)
	args := []any{f.RegisterID, nil, start, end, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)}
	if f.Status != "" {
		args[1] = f.Status
	}
	rows, err := r.ex(ctx).Query(ctx, `SELECT `+saleHeadCols+`,
		(SELECT count(*) FROM pos_line_items l WHERE l.transaction_id = t.id AND l.line_type <> 'TEXT')
		FROM pos_transactions t `+predicate+` ORDER BY t.created_at DESC, t.id DESC LIMIT $7`, append(args, limit)...)
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
				sku, description, quantity, uom, price_uom, uom_qty, price_uom_qty,
				stock_uom, stock_quantity, board_thickness_in, board_width_in,
				unit_price, priced_unit_price, price_source, price_basis, override_reason,
				discount_percent, discount_amount, discount_reason, price_adjusted_by,
				line_total, taxable, revenue_account_code, cost_source, unit_cost, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::numeric / 10000, $11, $12, $13::numeric / 10000,
				$14::numeric / 10000, $15, $16::numeric / 10000, $17::numeric / 10000, $18::numeric / 10000,
				$19::numeric / 10000, $20::numeric / 10000, $21, $22, $23, $24::numeric / 10000,
				$25::numeric / 100, $26, $27, $28::numeric / 100, $29, $30, $31, $32::numeric / 10000, NOW())`,
			l.ID, saleID, l.Position, string(l.LineType), l.ParentLineID, l.ProductID, l.ChargeCodeID,
			l.SKU, l.Description, qtyArg(l.Quantity), l.UOM, l.PriceUOM, qtyArg(l.UOMQty), qtyArg(l.PriceUOMQty),
			strArg(l.StockUOM), qtyArg(l.StockQuantity), qtyArg(l.TallyThickness()), qtyArg(l.TallyWidth()),
			priceArg(l.UnitPrice), priceArg(l.PricedUnitPrice), string(l.PriceSource), strArg(l.PriceBasis), l.OverrideReason,
			qtyArg(l.DiscountPercent), centsArg(l.DiscountAmount), l.DiscountReason, l.PriceAdjustedBy,
			centsArg(l.LineTotal), l.Taxable, l.RevenueAccountCode, strArg(l.CostSource), priceArg(l.StockingUnitCost))
		if err != nil {
			return fmt.Errorf("failed to add the sale line: %w", mapWriteError(err))
		}
		if err := r.insertTallyRows(ctx, l.ID, l.Tally); err != nil {
			return err
		}
	}
	return nil
}

// insertTallyRows writes one counter line's tally rows (ADR 0006 section
// 4.2), in request order.
func (r *PostgresRepository) insertTallyRows(ctx context.Context, lineID uuid.UUID, t *salesdoc.Tally) error {
	if t == nil {
		return nil
	}
	for j, row := range t.Rows {
		if _, err := r.ex(ctx).Exec(ctx, `
			INSERT INTO line_tally_rows (pos_line_item_id, position, pieces, length_ft)
			VALUES ($1, $2, $3, $4::numeric / 10000)`,
			lineID, j, row.Pieces, int64(row.LengthFT)); err != nil {
			return fmt.Errorf("failed to insert a tally row: %w", mapWriteError(err))
		}
	}
	return nil
}

func strArg(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.attachTallyRows(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// attachTallyRows reads every line's tally rows (ADR 0006 section 4.2) into
// the lines' tallies and completes the exact linear feet and the display
// board feet the wire carries.
func (r *PostgresRepository) attachTallyRows(ctx context.Context, lines []salesdoc.Line) error {
	ids := make([]uuid.UUID, 0, len(lines))
	for i := range lines {
		ids = append(ids, lines[i].ID)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT pos_line_item_id, pieces, ROUND(length_ft * 10000)::bigint
		FROM line_tally_rows WHERE pos_line_item_id = ANY($1) ORDER BY pos_line_item_id, position`, ids)
	if err != nil {
		return fmt.Errorf("failed to get tally rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var lineID uuid.UUID
		var row salesdoc.TallyRow
		var length int64
		if err := rows.Scan(&lineID, &row.Pieces, &length); err != nil {
			return fmt.Errorf("failed to scan a tally row: %w", err)
		}
		row.LengthFT = httpx.Quantity(length)
		for i := range lines {
			if lines[i].ID == lineID {
				if lines[i].Tally == nil {
					lines[i].Tally = &salesdoc.Tally{Rows: []salesdoc.TallyRow{}}
				}
				lines[i].Tally.Rows = append(lines[i].Tally.Rows, row)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to walk tally rows: %w", err)
	}
	salesdoc.CompleteTallies(lines)
	return nil
}

func (r *PostgresRepository) AddTender(ctx context.Context, t *Tender) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	var created time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		INSERT INTO pos_tenders (id, transaction_id, method, amount, payment_id, reference, card_last4, card_brand,
			gateway_tx_id, auth_code, id_checked, id_type, created_at)
		VALUES ($1, $2, $3, $4::numeric / 100, $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''),
			$11, $12, NOW())
		RETURNING created_at`,
		t.ID, t.SaleID, string(t.Method), int64(t.AmountCents), t.PaymentID, t.Reference, t.CardLast4, t.CardBrand,
		t.GatewayTxID, t.AuthCode, t.IDChecked, t.IDType).Scan(&created)
	t.CreatedAt = httpx.TimestampOf(created)
	if err != nil {
		return fmt.Errorf("failed to record the tender: %w", mapWriteError(err))
	}
	return nil
}

func (r *PostgresRepository) GetTenders(ctx context.Context, saleID uuid.UUID) ([]Tender, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, method, ROUND(amount * 100)::bigint, payment_id, reference, card_last4, card_brand, gateway_tx_id,
			auth_code, id_checked, id_type, created_at
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
			&t.GatewayTxID, &t.AuthCode, &t.IDChecked, &t.IDType, &createdAt); err != nil {
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
			cashier_id, currency, subtotal, tax_amount, total, refund_method, reason, status, credit_memo_id, restocking_fee,
			created_at, updated_at, revision)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::numeric / 100, $11::numeric / 100, $12::numeric / 100, $13, $14,
			'COMPLETED', $15, $16::numeric / 100, NOW(), NOW(), 1)
		RETURNING created_at`,
		ret.ID, ret.Number, ret.RegisterID, ret.TillSessionID, ret.OriginalSaleID, ret.CustomerID, ret.BranchID,
		ret.CashierID, ret.Currency, int64(ret.SubtotalCents), int64(ret.TaxCents), int64(ret.TotalCents),
		string(ret.RefundMethod), ret.Reason, ret.CreditMemoID, int64(ret.RestockingFeeCents)).Scan(&createdAt)
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
				stock_uom, stock_quantity, unit_price, line_total, restock, sale_line_id, reason_code, restocking_fee, created_at)
			VALUES ($1, $2, $3, $12, $4, $5, -($6::numeric / 10000), $7, $13, -($14::numeric / 10000), $8::numeric / 10000, $9::numeric / 100, $10, $11,
				$15, $16::numeric / 100, NOW())`,
			l.ID, ret.ID, l.Position, l.ProductID, l.Description, qtyArg(l.Quantity), l.UOM, priceArg(l.UnitPrice),
			centsArg(l.LineTotal), l.Restock, l.SaleLineID, lineType, l.StockUOM, qtyArg(l.StockQuantity), reasonOf(l.ReasonCode), int64(l.RestockingFeeCents))
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

// RestampRefundIntentLease renews one claim's in flight lease to now plus
// the stated timeout, in its own committed write immediately before the
// claim's own terminal call: a void's calls run one after another, each
// bounded at a third of the lease, so a later tender's call would otherwise
// start after the common insert-time lease had run out and the resolve route
// could release a claim whose call is still on the wire (the sixteenth
// review's P1-2).
func (r *PostgresRepository) RestampRefundIntentLease(ctx context.Context, intentID uuid.UUID) error {
	_, err := r.ex(ctx).Exec(ctx, `
		UPDATE pos_refund_intents SET answer_due_at = NOW() + make_interval(secs => $2), updated_at = NOW()
		WHERE id = $1`, intentID, int64(refundAnswerLease.Seconds()))
	if err != nil {
		return fmt.Errorf("failed to restamp the claim's in flight lease: %w", mapWriteError(err))
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
// and Cents is what the payment still carries unapplied of it. RefundTxID,
// when set, is the terminal's own refund id on that claim: money that moved
// at the terminal and never booked.
type TenderCreditFacts struct {
	IntentID   uuid.UUID
	TenderID   uuid.UUID
	SaleID     uuid.UUID
	SaleNumber string
	Cents      int64
	RefundTxID string
}

// HeldTenderCreditOfPayment answers whether the payment's unapplied cash is
// a credit held to one of the counter's card tenders (C2-5,
// FROM-PRINCIPAL 60): the tender's sale was voided, the tender's own refund
// claim was released by the terminal's decline (or a recorded no refund),
// and the void's completion reversed the tender's application onto the
// payment, where the cents wait for their one way back. A payment nothing
// of the counter holds, or one whose credit was already paid back (its
// unapplied cash is gone), answers false. A credit whose released claim
// carries the terminal's refund id (money that moved, never booked: the
// sixteenth review's P1-2) answers false too, with the tender named: it is
// already refunded as far as the card is concerned, never a held credit,
// and every act on it (an application, the online refund route) is refused
// 409 tender_refunded until it is reconciled.
func (r *PostgresRepository) HeldTenderCreditOfPayment(ctx context.Context, paymentID uuid.UUID) (TenderCreditFacts, bool, error) {
	var facts TenderCreditFacts
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT i.id, i.tender_id, i.sale_id, t.number, ROUND(p.amount_unapplied * 100)::bigint, COALESCE(i.refund_tx_id, '')
		FROM payments p
			JOIN pos_tenders d ON d.payment_id = p.id AND d.method = 'CARD'
			JOIN pos_transactions t ON t.id = d.transaction_id AND t.status = 'VOIDED'
			JOIN pos_refund_intents i ON i.sale_id = t.id AND i.tender_id = d.id
				AND i.act = 'void' AND i.state = 'FAILED'
		WHERE p.id = $1 AND p.status = 'POSTED' AND ROUND(p.amount_unapplied * 100)::bigint > 0
		ORDER BY i.created_at, i.id
		LIMIT 1`, paymentID).
		Scan(&facts.IntentID, &facts.TenderID, &facts.SaleID, &facts.SaleNumber, &facts.Cents, &facts.RefundTxID)
	if errors.Is(err, pgx.ErrNoRows) {
		return facts, false, nil
	}
	if err != nil {
		return facts, false, fmt.Errorf("failed to read the payment's held tender credit: %w", err)
	}
	if facts.RefundTxID != "" {
		return facts, false, conflict("tender_refunded",
			"the terminal already refunded this tender under a recorded refund id that never booked: reconcile that claim before anything else is paid or spent on the tender")
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
			refund_method, reason, credit_memo_id, created_at, ROUND(restocking_fee * 100)::bigint, exchange_sale_id
		FROM pos_returns WHERE id = $1 AND `+middleware.Wall("", 2, 3), id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).
		Scan(&ret.ID, &ret.Number, &ret.Revision, &ret.BranchID, &ret.RegisterID, &ret.TillSessionID, &ret.OriginalSaleID,
			&ret.CustomerID, &ret.CashierID, &ret.Currency, &ret.SubtotalCents, &ret.TaxCents, &ret.TotalCents,
			&method, &ret.Reason, &ret.CreditMemoID, &createdAt, &ret.RestockingFeeCents, &ret.ExchangeSaleID)
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
			ROUND(unit_price * 10000)::bigint, ROUND(-line_total * 100)::bigint, restock, reason_code,
			ROUND(restocking_fee * 100)::bigint
		FROM pos_return_lines WHERE return_id = $1 ORDER BY position, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l ReturnLine
		if err := rows.Scan(&l.ID, &l.Position, &l.ProductID, &l.Description, &l.Quantity, &l.UOM, &l.UnitPrice,
			&l.LineTotal, &l.Restock, &l.ReasonCode, &l.RestockingFeeCents); err != nil {
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
			refund_method, reason, credit_memo_id, created_at, ROUND(restocking_fee * 100)::bigint, exchange_sale_id
		FROM pos_returns
		WHERE ($1 = '' OR register_id = $1)
			AND created_at >= $2 AND created_at < $3
			AND `+middleware.Wall("", 4, 5)+`
		ORDER BY created_at DESC, id DESC LIMIT $6`, f.RegisterID, start, end,
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), limit)
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
			&ret.TotalCents, &method, &ret.Reason, &ret.CreditMemoID, &createdAt, &ret.RestockingFeeCents, &ret.ExchangeSaleID); err != nil {
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
	// A caller with no branch in context is held to the caller's grants
	// (SEC-NOHEADER-1): the header caller keeps the equality check above, an
	// administrator keeps its reach, and only a no header user is held.
	if err := r.grants.CheckGrantsBranch(ctx, branch); err != nil {
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			return nil, httpx.Forbidden(
				fmt.Sprintf("register %s belongs to a different branch than the request", registerID))
		}
		return nil, err
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

// LookupProductUnits reads the unit set of every named product (ADR 0006
// sections 3.1 and 3.3): the defaults and cross section a counter line's
// units resolve from, then the set rows.
func (r *PostgresRepository) LookupProductUnits(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]salesdoc.ProductUnitSet, error) {
	out := map[uuid.UUID]salesdoc.ProductUnitSet{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, uom_primary::text, sale_uom::text, price_uom::text, random_length,
		       ROUND(COALESCE(board_thickness_in, 0) * 10000)::bigint,
		       ROUND(COALESCE(board_width_in, 0) * 10000)::bigint,
		       board_thickness_in IS NOT NULL
		FROM products WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var set salesdoc.ProductUnitSet
		var thick, width int64
		var hasSection bool
		if err := rows.Scan(&id, &set.StockUOM, &set.SaleUOM, &set.PriceUOM, &set.RandomLength, &thick, &width, &hasSection); err != nil {
			return nil, err
		}
		if hasSection {
			t, w := httpx.Quantity(thick), httpx.Quantity(width)
			set.BoardThick, set.BoardWidth = &t, &w
		}
		out[id] = set
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	unitRows, err := r.ex(ctx).Query(ctx, `
		SELECT product_id, uom, ROUND(unit_qty * 10000)::bigint, ROUND(stock_qty * 10000)::bigint,
		       sell, purchase, price
		FROM product_units WHERE product_id = ANY($1) ORDER BY uom`, ids)
	if err != nil {
		return nil, err
	}
	defer unitRows.Close()
	for unitRows.Next() {
		var id uuid.UUID
		var row salesdoc.UnitSetRow
		var unitQty, stockQty int64
		if err := unitRows.Scan(&id, &row.UOM, &unitQty, &stockQty, &row.Sell, &row.Purchase, &row.Price); err != nil {
			return nil, err
		}
		row.UnitQty, row.StockQty = httpx.Quantity(unitQty), httpx.Quantity(stockQty)
		set := out[id]
		set.Rows = append(set.Rows, row)
		out[id] = set
	}
	return out, unitRows.Err()
}

// LookupCatalogue reads the unit catalogue rows for the given codes: what a
// counter line's units are checked against (ADR 0006 sections 2.1 and 7.5).
func (r *PostgresRepository) LookupCatalogue(ctx context.Context, codes []string) (map[string]units.CatalogueUnit, error) {
	out := make(map[string]units.CatalogueUnit, len(codes))
	if len(codes) == 0 {
		return out, nil
	}
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT code, dimension,
		       ROUND(COALESCE(std_unit_qty, 0) * 10000)::bigint,
		       ROUND(COALESCE(std_ref_qty, 0) * 10000)::bigint,
		       std_unit_qty IS NOT NULL, is_active
		FROM units WHERE code = ANY($1)`, codes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u units.CatalogueUnit
		var stdUnit, stdRef int64
		if err := rows.Scan(&u.Code, &u.Dimension, &stdUnit, &stdRef, &u.HasStdSize, &u.IsActive); err != nil {
			return nil, err
		}
		u.StdUnitQty, u.StdRefQty = httpx.Quantity(stdUnit), httpx.Quantity(stdRef)
		out[u.Code] = u
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

// counterStockScope is the join condition that holds the counter's on hand to
// the caller's yard, the inventory module's list rule (ListInventoryByProduct):
// a context branch counts every location under that branch; with none, a bound
// non-admin user counts the branches granted to them; an administrator, an
// unbound key and callers with no branch context count the whole dealer's.
// branch and grants are the positions of the two query parameters.
func counterStockScope(branch, grants int) string {
	return fmt.Sprintf(`
		AND (
			($%[1]d::uuid IS NULL AND $%[2]d::text IS NULL)
			OR EXISTS (SELECT 1 FROM locations l WHERE l.id = i.location_id AND (
				($%[1]d::uuid IS NOT NULL AND l.branch_id = $%[1]d)
				OR ($%[1]d::uuid IS NULL AND l.branch_id IN
					(SELECT branch_id FROM user_locations WHERE user_sub = $%[2]d))))
		)`, branch, grants)
}

func (r *PostgresRepository) SearchProducts(ctx context.Context, query string, limit int) ([]QuickSearchResult, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT p.id, COALESCE(p.sku, ''), COALESCE(p.description, ''), `+WalkInPriceCentsSQL+`,
			COALESCE(p.uom_primary::text, 'EA'), COALESCE(ROUND(SUM(i.quantity - i.allocated) * 10000)::bigint, 0)
		FROM products p LEFT JOIN inventory i ON i.product_id = p.id`+counterStockScope(3, 4)+`
		WHERE (p.sku ILIKE $1 OR p.description ILIKE $1)
		GROUP BY p.id, p.sku, p.description, p.base_price, p.uom_primary
		ORDER BY p.sku LIMIT $2`, "%"+query+"%", limit,
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
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

// tileHistoryCTE is the yard's units sold per product, positions $1 and $2
// being the branch and the grants of the caller's reach. Counter lines count
// from completed counter sales; invoice lines count from invoices that are not
// void and not a counter sale's own (origin POS: that sale's lines are already
// counted from the counter side).
const tileHistoryCTE = `
	WITH sold AS (
		SELECT li.product_id, SUM(li.quantity) AS units
		  FROM pos_line_items li
		  JOIN pos_transactions t ON t.id = li.transaction_id
		 WHERE t.status = 'COMPLETED' AND li.line_type IN ('PRODUCT', 'KIT') AND li.product_id IS NOT NULL
		   AND ` + "%s" + `
		 GROUP BY li.product_id
		UNION ALL
		SELECT il.product_id, SUM(il.quantity)
		  FROM invoice_lines il
		  JOIN invoices v ON v.id = il.invoice_id
		 WHERE v.status <> 'VOID' AND v.origin <> 'POS' AND il.line_type IN ('PRODUCT', 'KIT') AND il.product_id IS NOT NULL
		   AND ` + "%s" + `
		 GROUP BY il.product_id
	), top AS (
		SELECT product_id, SUM(units) AS units FROM sold GROUP BY product_id
	)`

func tileHistory() string {
	return fmt.Sprintf(tileHistoryCTE, middleware.Wall("t", 1, 2), middleware.Wall("v", 1, 2))
}

// CounterTiles answers GET /pos/tiles in two reads: the picture setting with
// the chips, then the tiles. History, stock and the chips all follow the
// caller's reach (the context branch, else the grants, else every branch).
func (r *PostgresRepository) CounterTiles(ctx context.Context, q TileQuery) (TileSet, error) {
	out := TileSet{Categories: []TileCategory{}, Items: []CounterTile{}}
	branch, grants := middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)

	rows, err := r.ex(ctx).Query(ctx, tileHistory()+`
		SELECT s.on_flag, c.name, c.n
		  FROM (SELECT `+pictures.FlagSQL+` AS on_flag) s
		  LEFT JOIN LATERAL (
			SELECT p.category AS name, COUNT(*) AS n
			  FROM products p LEFT JOIN top ON top.product_id = p.id
			 WHERE COALESCE(p.category, '') <> ''
			 GROUP BY p.category
			 ORDER BY COALESCE(SUM(top.units), 0) DESC, COUNT(*) DESC, p.category
			 LIMIT 8) c ON TRUE`, branch, grants)
	if err != nil {
		return out, fmt.Errorf("failed to read the tile categories: %w", err)
	}
	for rows.Next() {
		var name *string
		var n *int64
		if err := rows.Scan(&out.Images, &name, &n); err != nil {
			rows.Close()
			return out, err
		}
		if name != nil && n != nil {
			out.Categories = append(out.Categories, TileCategory{Name: *name, Count: *n})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	rows, err = r.ex(ctx).Query(ctx, tileHistory()+`
		SELECT p.id, COALESCE(p.sku, ''), COALESCE(p.description, ''), COALESCE(p.uom_primary::text, 'EA'),
			COALESCE(ROUND(p.base_price * 100)::bigint, 0),
			COALESCE(ROUND(SUM(i.quantity - i.allocated) * 10000)::bigint, 0),
			COALESCE(p.category, ''),
			CASE WHEN $6 THEN NULLIF(p.image_url, '') END
		  FROM products p
		  LEFT JOIN top ON top.product_id = p.id
		  LEFT JOIN inventory i ON i.product_id = p.id`+counterStockScope(1, 2)+`
		 WHERE ($3 = '' OR p.category = $3)
		   AND ($4 = '' OR p.sku ILIKE '%' || $4 || '%' OR p.description ILIKE '%' || $4 || '%')
		 GROUP BY p.id, p.sku, p.description, p.uom_primary, p.base_price, p.category, p.image_url, top.units
		 ORDER BY ($4 <> '' AND LOWER(p.sku) = LOWER($4)) DESC, COALESCE(top.units, 0) DESC, p.description, p.sku
		 LIMIT $5`, branch, grants, q.Category, q.Q, q.Limit, out.Images)
	if err != nil {
		return out, fmt.Errorf("failed to read the tiles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t CounterTile
		if err := rows.Scan(&t.ProductID, &t.SKU, &t.Description, &t.UOM, &t.UnitPriceCents, &t.InStock, &t.Category, &t.ImageURL); err != nil {
			return out, err
		}
		out.Items = append(out.Items, t)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) GetProductCatalog(ctx context.Context) ([]CatalogProduct, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT p.id, COALESCE(p.sku, ''), COALESCE(p.description, ''), `+WalkInPriceCentsSQL+`,
			COALESCE(p.uom_primary::text, 'EA'), COALESCE(ROUND(SUM(i.quantity - i.allocated) * 10000)::bigint, 0)
		FROM products p LEFT JOIN inventory i ON i.product_id = p.id`+counterStockScope(1, 2)+`
		GROUP BY p.id, p.sku, p.description, p.base_price, p.uom_primary
		ORDER BY p.sku`, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
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
		SELECT c.name, ROUND(c.credit_limit * 100)::bigint,
			COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD'),
			COALESCE(c.po_required, FALSE), COALESCE(pt.kind, 'NET_DAYS'), c.named_buyer_required,
			CASE WHEN c.check_limit IS NULL THEN NULL ELSE ROUND(c.check_limit * 100)::bigint END
		FROM customers c LEFT JOIN payment_terms pt ON pt.id = c.payment_terms_id
		WHERE c.id = $1`, customerID).Scan(&f.Name, &limit, &f.Currency, &f.PORequired, &f.TermsKind,
		&f.NamedBuyerRequired, &f.CheckLimitCents)
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
	// A credit memo's open amount is stored negative, so it is ADDED: an open
	// memo lowers the figure, as the order module's credit gate does.
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT COALESCE((SELECT SUM(ROUND(i.amount_open * 100)::bigint) FROM invoices i
			WHERE i.customer_id = $1 AND i.status IN ('UNPAID', 'PARTIAL', 'OVERDUE')), 0)
			+ COALESCE((SELECT SUM(ROUND(m.amount_open * 100)::bigint) FROM credit_memos m
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

// CounterCustomer is a customer as the counter's lookup shows it: who, the
// standing of the account in figures and in words, and the jobs a charge sale
// may name. The wall (customer_branches held to the caller's reach) is the
// lookup's own: a user finds every customer (ruling A), a branch bound key
// only its pin's.
type CounterCustomer struct {
	ID                   uuid.UUID    `json:"id"`
	AccountNumber        string       `json:"account_number"`
	Name                 string       `json:"name"`
	POrequired           bool         `json:"po_required"`
	CreditLimitCents     *httpx.Cents `json:"credit_limit_cents"`
	OpenReceivableCents  httpx.Cents  `json:"open_receivable_cents"`
	AvailableCreditCents *httpx.Cents `json:"available_credit_cents"`
	// Standing is the account's state in a word the screen puts in its own
	// words: good, no_limit, no_credit (a limit of zero: the account is on
	// hold, no credit given) or over_limit.
	Standing string       `json:"standing"`
	Jobs     []CounterJob `json:"jobs"`
}

// CounterJob is one of the customer's open jobs.
type CounterJob struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

const counterCustomerCols = `c.id, c.account_number, c.name, COALESCE(c.po_required, FALSE),
	CASE WHEN c.credit_limit IS NULL THEN NULL ELSE ROUND(c.credit_limit * 100)::bigint END`

// counterCustomerReach is the customer wall's two parameters at the counter
// (ruling A): a user reaches every customer whatever the user's yard or
// grants, so both are nil; a branch bound key keeps its pin.
func counterCustomerReach(ctx context.Context) (*uuid.UUID, *string) {
	if pin, ok := branchctx.KeyBranchFromContext(ctx); ok {
		return &pin, nil
	}
	return nil, nil
}

func (r *PostgresRepository) FindCounterCustomers(ctx context.Context, q string, limit int) ([]CounterCustomer, error) {
	// the query is text to find, never a pattern: %, _ and the escape itself
	// match only themselves
	raw := q
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	branch, grants := counterCustomerReach(ctx)
	rows, err := r.ex(ctx).Query(ctx, `SELECT `+counterCustomerCols+` FROM customers c
		WHERE COALESCE(c.is_active, TRUE) AND c.account_number <> 'WALK-IN'
		  AND (c.name ILIKE '%' || $1 || '%' OR c.account_number ILIKE '%' || $1 || '%')
		  AND `+customer.Wall("c", 2, 3)+`
		ORDER BY (lower(c.account_number) = lower($5)) DESC, c.name, c.id LIMIT $4`,
		q, branch, grants, limit, strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to search customers: %w", err)
	}
	defer rows.Close()
	out := []CounterCustomer{}
	for rows.Next() {
		var c CounterCustomer
		if err := rows.Scan(&c.ID, &c.AccountNumber, &c.Name, &c.POrequired, &c.CreditLimitCents); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		if out[i].Jobs, err = r.customerJobs(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *PostgresRepository) CounterCustomerByID(ctx context.Context, id uuid.UUID) (*CounterCustomer, bool, error) {
	branch, grants := counterCustomerReach(ctx)
	var c CounterCustomer
	err := r.ex(ctx).QueryRow(ctx, `SELECT `+counterCustomerCols+` FROM customers c
		WHERE c.id = $1 AND COALESCE(c.is_active, TRUE) AND `+customer.Wall("c", 2, 3),
		id, branch, grants).
		Scan(&c.ID, &c.AccountNumber, &c.Name, &c.POrequired, &c.CreditLimitCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read the customer: %w", err)
	}
	if c.Jobs, err = r.customerJobs(ctx, c.ID); err != nil {
		return nil, false, err
	}
	return &c, true, nil
}

func (r *PostgresRepository) customerJobs(ctx context.Context, customerID uuid.UUID) ([]CounterJob, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT id, name FROM projects
		WHERE customer_id = $1 AND lower(status) = 'active' ORDER BY name, id LIMIT 50`, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the customer's jobs: %w", err)
	}
	defer rows.Close()
	out := []CounterJob{}
	for rows.Next() {
		var j CounterJob
		if err := rows.Scan(&j.ID, &j.Name); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) SetSaleAccount(ctx context.Context, saleID uuid.UUID, customerID *uuid.UUID, currency string, po *string, jobID *uuid.UUID) error {
	// The account update, the customer's link to the sale's yard and the
	// link's audit row commit together or not at all (ruling A, ADR 0007
	// section 5.8).
	return r.db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := r.ex(ctx).Exec(ctx, `UPDATE pos_transactions SET customer_id = $2, currency = $3, customer_po = $4, job_id = $5,
			updated_at = NOW(), revision = revision + 1 WHERE id = $1`, saleID, customerID, currency, po, jobID)
		if err != nil {
			return fmt.Errorf("failed to name the sale's customer: %w", mapWriteError(err))
		}
		return customerlink.Ensure(ctx, r.db, "pos_transactions", saleID)
	})
}

// ListRegisters reads the registers the caller may use, by branch then name.
// The branch is the register's location's branch, else its own, the way a
// sale resolves it (GetRegisterBranch); the wall is the same branch wall the
// sale reads carry: a context branch (a header, or a bound key's pin), else
// the user's grants, else every branch.
func (r *PostgresRepository) ListRegisters(ctx context.Context) ([]CounterRegister, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT x.id, x.name, x.branch_id, x.branch_code, x.branch_name FROM (
			SELECT pr.id, pr.name, COALESCE(l.branch_id, pr.branch_id) AS branch_id,
			       b.code AS branch_code, COALESCE(b.name, b.code) AS branch_name
			  FROM pos_registers pr
			  LEFT JOIN locations l ON l.id = pr.location_id
			  JOIN locations b ON b.id = COALESCE(l.branch_id, pr.branch_id)
			 WHERE pr.is_active
		) x
		WHERE `+middleware.Wall("x", 1, 2)+`
		ORDER BY x.branch_name, x.branch_code, x.name, x.id LIMIT 200`,
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to list the registers: %w", err)
	}
	defer rows.Close()
	out := []CounterRegister{}
	for rows.Next() {
		var g CounterRegister
		if err := rows.Scan(&g.ID, &g.Name, &g.BranchID, &g.BranchCode, &g.BranchName); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
