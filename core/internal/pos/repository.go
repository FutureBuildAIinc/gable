// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RegisterBranchRefusedError is the refusal a write answers when the caller's
// branch context disagrees with the register's own branch, or when the
// register's branch is outside the grants a caller with no branch in context
// is held to (SEC-NOHEADER-1). It is a typed error so the HTTP layer can
// answer it 403 instead of the 500 a plain service error maps to, nothing
// written either way.
type RegisterBranchRefusedError struct {
	RegisterID string
}

func (e *RegisterBranchRefusedError) Error() string {
	return fmt.Sprintf("register %s belongs to a different branch than the request", e.RegisterID)
}

// RecordBranchRefusedError is the refusal an act on an existing record (a
// till session, a sale) answers when the record's own branch is outside the
// grants a caller with no branch in context is held to (SEC-NOHEADER-1).
// It is a typed error so the HTTP layer can answer it 403, nothing written.
type RecordBranchRefusedError struct {
	What string
	ID   uuid.UUID
}

func (e *RecordBranchRefusedError) Error() string {
	return fmt.Sprintf("%s %s belongs to a branch this caller may not target", e.What, e.ID)
}

// UnknownRegisterError is the refusal a write answers when the register it
// names does not exist, an empty register_id included, which names no register
// either (r19 P3-1). It is a typed error so the HTTP layer can answer it 400
// instead of the 500 a plain service error maps to; the offline sync
// refuses a batch whose register does not resolve before any item runs and
// before the log row, so the log never names a register nobody made (r17
// P3-3).
type UnknownRegisterError struct {
	RegisterID string
}

func (e *UnknownRegisterError) Error() string {
	if e.RegisterID == "" {
		return "the batch register_id is empty and names no register"
	}
	return fmt.Sprintf("register %s does not exist", e.RegisterID)
}

// MissingRecordError is the answer an act addressing a record that does not
// exist gets (a void of a missing sale, a close of a missing session, r4
// P3-1 and P3-2): the module's own 404, typed so the HTTP layer never
// answers it with the record branch refusal's 403 (which would claim the
// missing record belongs to another branch) nor a bare 500.
type MissingRecordError struct {
	What string
	ID   uuid.UUID
}

func (e *MissingRecordError) Error() string {
	return fmt.Sprintf("%s %s not found", e.What, e.ID)
}

// Repository handles POS data persistence.
type Repository interface {
	CreateTransaction(ctx context.Context, tx *POSTransaction) error
	GetTransaction(ctx context.Context, id uuid.UUID) (*POSTransaction, error)
	UpdateTransaction(ctx context.Context, tx *POSTransaction) error
	ListTransactions(ctx context.Context, registerID string, date time.Time) ([]TransactionSummary, error)

	AddLineItem(ctx context.Context, item *POSLineItem) error
	RemoveLineItem(ctx context.Context, itemID uuid.UUID) error
	GetLineItems(ctx context.Context, txID uuid.UUID) ([]POSLineItem, error)

	AddTender(ctx context.Context, tender *POSTender) error
	GetTenders(ctx context.Context, txID uuid.UUID) ([]POSTender, error)

	// Till sessions
	CreateTillSession(ctx context.Context, s *TillSession) error
	GetTillSession(ctx context.Context, id uuid.UUID) (*TillSession, error)
	GetOpenTillSession(ctx context.Context, registerID string) (*TillSession, error)
	CloseTillSession(ctx context.Context, s *TillSession) error
	SetTillSessionGLEntry(ctx context.Context, sessionID, glEntryID uuid.UUID) error
	AggregateTillSession(ctx context.Context, sessionID uuid.UUID) (*TillAggregate, error)

	// Z-reports
	CreateZReport(ctx context.Context, z *ZReport) error
	GetZReportBySession(ctx context.Context, sessionID uuid.UUID) (*ZReport, error)
	ListZReports(ctx context.Context, registerID string, date time.Time) ([]ZReport, error)

	// Returns
	GetRegisterBranch(ctx context.Context, registerID string) (*uuid.UUID, error)
	CreateReturn(ctx context.Context, ret *POSReturn) error
	SetReturnGLEntry(ctx context.Context, returnID, glEntryID uuid.UUID) error
	GetReturn(ctx context.Context, id uuid.UUID) (*POSReturn, error)
	ListReturns(ctx context.Context, registerID string, date time.Time) ([]POSReturn, error)

	SearchProducts(ctx context.Context, query string, limit int) ([]QuickSearchResult, error)

	// Offline sync
	TransactionExists(ctx context.Context, id uuid.UUID) (bool, error)
	GetProductCatalog(ctx context.Context) ([]CatalogProduct, error)
	LogSyncBatch(ctx context.Context, batchID, registerID string, synced, duplicates, errors int, errorDetails []SyncError) error

	// TransactionBranch and TillSessionBranch read a record's own branch
	// with no wall, for the act hold that must decide before the walled
	// read hides the record from a caller held to the grants. The second
	// return tells a record that does not exist (found false) from one
	// that carries no branch (found true, nil branch), so the act can
	// answer the module's 404 rather than a branch refusal (r4 P3-1).
	TransactionBranch(ctx context.Context, id uuid.UUID) (branch *uuid.UUID, found bool, err error)
	TillSessionBranch(ctx context.Context, id uuid.UUID) (branch *uuid.UUID, found bool, err error)
}

// PostgresRepository implements Repository for PostgreSQL.
type PostgresRepository struct {
	db     *database.DB
	grants *middleware.BranchGuard
}

// NewRepository creates a new POS repository.
func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db, grants: middleware.NewBranchGuard(db)}
}

// holdRegisterBranch holds the register's branch to the caller's grants when
// the context carries no branch (SEC-NOHEADER-1): a caller that sends the
// header keeps the equality check the caller already ran, and an
// administrator keeps its reach, so only a no header user is held.
func (r *PostgresRepository) holdRegisterBranch(ctx context.Context, registerID string, branch *uuid.UUID) error {
	if err := r.grants.CheckGrantsBranch(ctx, branch); err != nil {
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			return &RegisterBranchRefusedError{RegisterID: registerID}
		}
		return err
	}
	return nil
}

func (r *PostgresRepository) CreateTransaction(ctx context.Context, tx *POSTransaction) error {
	if tx.ID == uuid.Nil {
		tx.ID = uuid.New()
	}
	tx.CreatedAt = time.Now()

	// Derive the register's branch_id (its location's branch, else its own pos_registers.branch_id).
	var registerBranch *uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT COALESCE(l.branch_id, r.branch_id)
		 FROM pos_registers r
		 LEFT JOIN locations l ON l.id = r.location_id
		 WHERE r.id = $1`, tx.RegisterID).Scan(&registerBranch)
	if err != nil {
		return fmt.Errorf("failed to resolve register branch: %w", err)
	}

	// Reject if the caller's branch context disagrees with the register's branch.
	if reqBranch := branchctx.IDForQuery(ctx); reqBranch != nil && registerBranch != nil && *reqBranch != *registerBranch {
		return &RegisterBranchRefusedError{RegisterID: tx.RegisterID}
	}
	// A caller with no branch in context is held to the caller's grants.
	if err := r.holdRegisterBranch(ctx, tx.RegisterID, registerBranch); err != nil {
		return err
	}

	query := `
		INSERT INTO pos_transactions (id, register_id, cashier_id, customer_id, subtotal, tax_amount, total, status, created_at, synced_from, client_created_at, branch_id, till_session_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, COALESCE($12::uuid, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')), $13)
		RETURNING branch_id
	`
	err = r.db.GetExecutor(ctx).QueryRow(ctx, query,
		tx.ID, tx.RegisterID, tx.CashierID, tx.CustomerID,
		float64(tx.Subtotal)/100.0, float64(tx.TaxAmount)/100.0, float64(tx.Total)/100.0,
		tx.Status, tx.CreatedAt, tx.SyncedFrom, tx.ClientCreatedAt, registerBranch, tx.TillSessionID,
	).Scan(&tx.BranchID)
	if err != nil {
		return fmt.Errorf("failed to create POS transaction: %w", err)
	}
	return nil
}

// TransactionExists checks if a transaction with the given ID already exists (for idempotent sync).
func (r *PostgresRepository) TransactionExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pos_transactions WHERE id = $1)`, id,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check transaction existence: %w", err)
	}
	return exists, nil
}

// GetProductCatalog returns all active products for offline caching.
func (r *PostgresRepository) GetProductCatalog(ctx context.Context) ([]CatalogProduct, error) {
	query := `
		SELECT p.id, p.sku, p.description, COALESCE(p.base_price, 0) as price,
			COALESCE(p.uom_primary::text, 'EA') as uom,
			COALESCE(i.quantity, 0) as in_stock
		FROM products p
		LEFT JOIN inventory i ON i.product_id = p.id
		WHERE p.is_active = true OR p.is_active IS NULL
		ORDER BY p.sku ASC
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get product catalog: %w", err)
	}
	defer rows.Close()

	var products []CatalogProduct
	for rows.Next() {
		var p CatalogProduct
		if err := rows.Scan(&p.ProductID, &p.SKU, &p.Description, &p.Price, &p.UOM, &p.InStock); err != nil {
			return nil, fmt.Errorf("failed to scan catalog product: %w", err)
		}
		products = append(products, p)
	}
	return products, nil
}

// syncErrorJSON is used for safe JSON marshalling of sync error details.
type syncErrorJSON struct {
	ClientID string `json:"client_id"`
	Reason   string `json:"reason"`
}

// LogSyncBatch records a sync batch result for auditing.
func (r *PostgresRepository) LogSyncBatch(ctx context.Context, batchID, registerID string, synced, duplicates, errors int, errorDetails []SyncError) error {
	errJSON := "[]"
	if len(errorDetails) > 0 {
		items := make([]syncErrorJSON, len(errorDetails))
		for i, e := range errorDetails {
			items[i] = syncErrorJSON{ClientID: e.ClientID, Reason: e.Reason}
		}
		data, err := json.Marshal(items)
		if err != nil {
			return fmt.Errorf("failed to marshal sync errors: %w", err)
		}
		errJSON = string(data)
	}
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`INSERT INTO pos_sync_log (batch_id, register_id, synced_count, duplicate_count, error_count, errors) VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
		batchID, registerID, synced, duplicates, errors, errJSON,
	)
	return err
}

func (r *PostgresRepository) GetTransaction(ctx context.Context, id uuid.UUID) (*POSTransaction, error) {
	query := `
		SELECT id, register_id, cashier_id, customer_id, subtotal, tax_amount, total, change_due, till_session_id, status, completed_at, created_at, branch_id
		FROM pos_transactions
		WHERE id = $1
		  AND ` + middleware.Wall("", 2, 3) + `
	`
	var tx POSTransaction
	var subtotal, taxAmount, total, changeDue float64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).Scan(
		&tx.ID, &tx.RegisterID, &tx.CashierID, &tx.CustomerID,
		&subtotal, &taxAmount, &total, &changeDue, &tx.TillSessionID,
		&tx.Status, &tx.CompletedAt, &tx.CreatedAt, &tx.BranchID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get POS transaction: %w", err)
	}
	tx.Subtotal = int64(subtotal*100.0 + 0.5)
	tx.TaxAmount = int64(taxAmount*100.0 + 0.5)
	tx.Total = int64(total*100.0 + 0.5)
	tx.ChangeDue = int64(changeDue*100.0 + 0.5)
	return &tx, nil
}

func (r *PostgresRepository) UpdateTransaction(ctx context.Context, tx *POSTransaction) error {
	query := `
		UPDATE pos_transactions
		SET subtotal = $2, tax_amount = $3, total = $4, status = $5, completed_at = $6, customer_id = $7, change_due = $9
		WHERE id = $1
		  AND ` + middleware.Wall("", 8, 10) + `
	`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query,
		tx.ID,
		float64(tx.Subtotal)/100.0, float64(tx.TaxAmount)/100.0, float64(tx.Total)/100.0,
		tx.Status, tx.CompletedAt, tx.CustomerID, middleware.BranchIDForQuery(ctx),
		float64(tx.ChangeDue)/100.0,
		middleware.GrantsSubForQuery(ctx),
	)
	if err != nil {
		return fmt.Errorf("failed to update POS transaction: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListTransactions(ctx context.Context, registerID string, date time.Time) ([]TransactionSummary, error) {
	query := `
		SELECT t.id, t.register_id, t.total, t.status, t.completed_at, t.created_at,
			(SELECT COUNT(*) FROM pos_line_items li WHERE li.transaction_id = t.id) as item_count
		FROM pos_transactions t
		WHERE ($1 = '' OR t.register_id = $1)
		  AND t.created_at >= $2 AND t.created_at < $3
		  AND ` + middleware.Wall("t", 4, 5) + `
		ORDER BY t.created_at DESC
	`
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, registerID, startOfDay, endOfDay,
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to list POS transactions: %w", err)
	}
	defer rows.Close()

	var summaries []TransactionSummary
	for rows.Next() {
		var s TransactionSummary
		var total float64
		if err := rows.Scan(&s.ID, &s.RegisterID, &total, &s.Status, &s.CompletedAt, &s.CreatedAt, &s.ItemCount); err != nil {
			return nil, fmt.Errorf("failed to scan transaction summary: %w", err)
		}
		s.Total = int64(total*100.0 + 0.5)
		summaries = append(summaries, s)
	}
	return summaries, nil
}

func (r *PostgresRepository) AddLineItem(ctx context.Context, item *POSLineItem) error {
	if item.ID == uuid.Nil {
		item.ID = uuid.New()
	}
	item.CreatedAt = time.Now()

	query := `
		INSERT INTO pos_line_items (id, transaction_id, product_id, description, quantity, uom, unit_price, line_total, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query,
		item.ID, item.TransactionID, item.ProductID, item.Description,
		item.Quantity, item.UOM,
		float64(item.UnitPrice)/100.0, float64(item.LineTotal)/100.0,
		item.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to add POS line item: %w", err)
	}
	return nil
}

// RemoveLineItem deletes a line through its transaction's branch: a bound
// key's branch context (or a user's grants) hides a transaction of another
// branch, so its line deletes as missing and nothing is written.
func (r *PostgresRepository) RemoveLineItem(ctx context.Context, itemID uuid.UUID) error {
	query := `
		DELETE FROM pos_line_items li
		USING pos_transactions t
		WHERE li.id = $1
		  AND t.id = li.transaction_id
		  AND ` + middleware.Wall("t", 2, 3) + `
	`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, itemID, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return fmt.Errorf("failed to remove POS line item: %w", err)
	}
	return nil
}

// TransactionBranch reads a transaction's own branch with no wall, for the
// act hold that must decide before the walled read hides the record. A
// transaction that does not exist answers found false, and one that exists
// with no branch answers found true and a nil branch.
func (r *PostgresRepository) TransactionBranch(ctx context.Context, id uuid.UUID) (*uuid.UUID, bool, error) {
	var branch *uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT branch_id FROM pos_transactions WHERE id = $1`, id).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read transaction branch: %w", err)
	}
	return branch, true, nil
}

// TillSessionBranch reads a till session's own branch with no wall, for the
// act hold that must decide before the walled read hides the record. A
// session that does not exist answers found false, and one that exists with
// no branch answers found true and a nil branch.
func (r *PostgresRepository) TillSessionBranch(ctx context.Context, id uuid.UUID) (*uuid.UUID, bool, error) {
	var branch *uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT branch_id FROM till_sessions WHERE id = $1`, id).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read till session branch: %w", err)
	}
	return branch, true, nil
}

func (r *PostgresRepository) GetLineItems(ctx context.Context, txID uuid.UUID) ([]POSLineItem, error) {
	query := `
		SELECT id, transaction_id, product_id, description, quantity, uom, unit_price, line_total, created_at
		FROM pos_line_items
		WHERE transaction_id = $1
		ORDER BY created_at ASC
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, txID)
	if err != nil {
		return nil, fmt.Errorf("failed to get POS line items: %w", err)
	}
	defer rows.Close()

	var items []POSLineItem
	for rows.Next() {
		var item POSLineItem
		var unitPrice, lineTotal float64
		if err := rows.Scan(
			&item.ID, &item.TransactionID, &item.ProductID, &item.Description,
			&item.Quantity, &item.UOM, &unitPrice, &lineTotal, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan POS line item: %w", err)
		}
		item.UnitPrice = int64(unitPrice*100.0 + 0.5)
		item.LineTotal = int64(lineTotal*100.0 + 0.5)
		items = append(items, item)
	}
	return items, nil
}

func (r *PostgresRepository) AddTender(ctx context.Context, tender *POSTender) error {
	if tender.ID == uuid.Nil {
		tender.ID = uuid.New()
	}
	tender.CreatedAt = time.Now()

	query := `
		INSERT INTO pos_tenders (id, transaction_id, method, amount, reference, card_last4, card_brand, gateway_tx_id, auth_code, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query,
		tender.ID, tender.TransactionID, tender.Method,
		float64(tender.Amount)/100.0, tender.Reference,
		tender.CardLast4, tender.CardBrand,
		tender.GatewayTxID, tender.AuthCode, tender.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to add POS tender: %w", err)
	}
	return nil
}

func (r *PostgresRepository) GetTenders(ctx context.Context, txID uuid.UUID) ([]POSTender, error) {
	query := `
		SELECT id, transaction_id, method, amount, COALESCE(reference, '') as reference,
			COALESCE(card_last4, '') as card_last4, COALESCE(card_brand, '') as card_brand,
			COALESCE(gateway_tx_id, '') as gateway_tx_id, COALESCE(auth_code, '') as auth_code, created_at
		FROM pos_tenders
		WHERE transaction_id = $1
		ORDER BY created_at ASC
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, txID)
	if err != nil {
		return nil, fmt.Errorf("failed to get POS tenders: %w", err)
	}
	defer rows.Close()

	var tenders []POSTender
	for rows.Next() {
		var t POSTender
		var amount float64
		if err := rows.Scan(
			&t.ID, &t.TransactionID, &t.Method, &amount, &t.Reference,
			&t.CardLast4, &t.CardBrand, &t.GatewayTxID, &t.AuthCode, &t.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan POS tender: %w", err)
		}
		t.Amount = int64(amount*100.0 + 0.5)
		tenders = append(tenders, t)
	}
	return tenders, nil
}

func (r *PostgresRepository) SearchProducts(ctx context.Context, query string, limit int) ([]QuickSearchResult, error) {
	sql := `
		SELECT p.id, p.sku, p.description, COALESCE(p.base_price, 0) as price, COALESCE(p.uom_primary::text, 'EA') as uom,
			COALESCE(SUM(i.quantity), 0) as in_stock
		FROM products p
		LEFT JOIN inventory i ON i.product_id = p.id
		WHERE p.sku ILIKE $1 OR p.description ILIKE $1
		GROUP BY p.id, p.sku, p.description, p.base_price, p.uom_primary
		ORDER BY p.sku ASC
		LIMIT $2
	`
	searchTerm := "%" + query + "%"
	rows, err := r.db.GetExecutor(ctx).Query(ctx, sql, searchTerm, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search products: %w", err)
	}
	defer rows.Close()

	var results []QuickSearchResult
	for rows.Next() {
		var r QuickSearchResult
		if err := rows.Scan(&r.ProductID, &r.SKU, &r.Description, &r.UnitPrice, &r.UOM, &r.InStock); err != nil {
			return nil, fmt.Errorf("failed to scan product: %w", err)
		}
		results = append(results, r)
	}
	return results, nil
}
