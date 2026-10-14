// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory

import (
	"context"
	"fmt"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Repository interface {
	GetInventory(ctx context.Context, productID uuid.UUID, locationID *uuid.UUID) (*Inventory, error)
	UpdateInventory(ctx context.Context, inv *Inventory) error
	CreateInventory(ctx context.Context, inv *Inventory) error
	ListInventoryByProduct(ctx context.Context, productID uuid.UUID) ([]Inventory, error)
	ListInventoryByProductAndBranch(ctx context.Context, productID uuid.UUID, branchID *uuid.UUID) ([]Inventory, error)
	LocationBranchID(ctx context.Context, locationID uuid.UUID) (*uuid.UUID, error)
	AllocateStock(ctx context.Context, inventoryID uuid.UUID, delta float64) error
	DeallocateStock(ctx context.Context, inventoryID uuid.UUID, delta float64) error
	FulfillStock(ctx context.Context, inventoryID uuid.UUID, delta float64) error
	RevertFulfillStock(ctx context.Context, inventoryID uuid.UUID, delta float64) error
	ExecuteInTx(ctx context.Context, fn func(context.Context) error) error

	// The scale 4 quantity reads and writes of ADR 0005 5.4. LockBranchInventory
	// takes the product's rows at the branch FOR UPDATE in inventory id order
	// (the lock order of section 11, step 6); the Qty updates carry the
	// quantity as the scale 4 integer, the division by 10000 in SQL.
	LockBranchInventory(ctx context.Context, productID, branchID uuid.UUID) ([]Inventory, error)
	AllocateStockQty(ctx context.Context, inventoryID uuid.UUID, delta int64) error
	DeallocateStockQty(ctx context.Context, inventoryID uuid.UUID, delta int64) error
	FulfillStockQty(ctx context.Context, inventoryID uuid.UUID, delta int64) error
	RestockQty(ctx context.Context, inventoryID uuid.UUID, delta int64) error
}

type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func (r *PostgresRepository) ExecuteInTx(ctx context.Context, fn func(context.Context) error) error {
	return r.db.RunInTx(ctx, fn)
}

func (r *PostgresRepository) GetInventory(ctx context.Context, productID uuid.UUID, locationID *uuid.UUID) (*Inventory, error) {
	query := `
		SELECT id, product_id, location_id, location, quantity, allocated, updated_at
		FROM inventory
		WHERE product_id = $1 AND (($2::uuid IS NULL AND location_id IS NULL) OR location_id = $2)
	`
	var inv Inventory
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, productID, locationID).Scan(
		&inv.ID,
		&inv.ProductID,
		&inv.LocationID,
		&inv.Location,
		&inv.Quantity,
		&inv.Allocated,
		&inv.UpdatedAt,
	)

	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get inventory: %w", err)
	}

	return &inv, nil
}

func (r *PostgresRepository) CreateInventory(ctx context.Context, inv *Inventory) error {
	query := `
		INSERT INTO inventory (product_id, location_id, location, quantity, allocated)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, updated_at
	`
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, inv.ProductID, inv.LocationID, inv.Location, inv.Quantity, inv.Allocated).Scan(
		&inv.ID,
		&inv.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create inventory: %w", err)
	}
	r.markDirtyByProductLocation(ctx, inv.ProductID, inv.LocationID)
	return nil
}

func (r *PostgresRepository) UpdateInventory(ctx context.Context, inv *Inventory) error {
	query := `
		UPDATE inventory
		SET quantity = $1, allocated = $2, updated_at = NOW()
		WHERE id = $3
		RETURNING updated_at
	`
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, inv.Quantity, inv.Allocated, inv.ID).Scan(&inv.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to update inventory: %w", err)
	}
	return nil
}

// ListInventoryByProduct returns inventory rows for a product, scoped to the
// caller's branches via the joined location row's branch_id (ADR 0007
// section 2.3, the list form of the record rule): a context branch lists its
// own rows; with no context branch a bound non-admin user lists the branches
// granted to the user, none granted listing none; an administrator without a
// header, an unbound key, the single-branch switch, dev mode and callers
// with no branch context at all (portal, background jobs) list every
// branch's.
func (r *PostgresRepository) ListInventoryByProduct(ctx context.Context, productID uuid.UUID) ([]Inventory, error) {
	query := `
        SELECT i.id, i.product_id, i.location_id,
               COALESCE(l.path, i.location, '') as location_name,
               i.quantity, i.allocated, i.updated_at
        FROM inventory i
        LEFT JOIN locations l ON i.location_id = l.id
        WHERE i.product_id = $1
          AND (
            ($2::uuid IS NOT NULL AND l.branch_id = $2)
            OR ($2::uuid IS NULL AND $3::text IS NOT NULL AND l.branch_id IN
                (SELECT branch_id FROM user_locations WHERE user_sub = $3))
            OR ($2::uuid IS NULL AND $3::text IS NULL)
          )
    `
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, productID,
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Inventory
	for rows.Next() {
		var i Inventory
		if err := rows.Scan(&i.ID, &i.ProductID, &i.LocationID, &i.Location, &i.Quantity, &i.Allocated, &i.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, nil
}

// ListInventoryByProductAndBranch returns inventory rows for a product, optionally
// scoped to a branch via the joined location row's branch_id. When branchID is
// nil, all branches are returned (admin "all branches" semantic).
func (r *PostgresRepository) ListInventoryByProductAndBranch(ctx context.Context, productID uuid.UUID, branchID *uuid.UUID) ([]Inventory, error) {
	query := `
        SELECT i.id, i.product_id, i.location_id,
               COALESCE(l.path, i.location, '') as location_name,
               i.quantity, i.allocated, i.updated_at
        FROM inventory i
        LEFT JOIN locations l ON i.location_id = l.id
        WHERE i.product_id = $1
          AND ($2::uuid IS NULL OR l.branch_id = $2)
    `
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, productID, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Inventory
	for rows.Next() {
		var i Inventory
		if err := rows.Scan(&i.ID, &i.ProductID, &i.LocationID, &i.Location, &i.Quantity, &i.Allocated, &i.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, nil
}

// LocationBranchID returns the branch_id for a given location row, or nil if
// the location does not have one assigned (e.g. legacy rows or branch rows
// where branch_id is the location itself — caller handles both).
func (r *PostgresRepository) LocationBranchID(ctx context.Context, locationID uuid.UUID) (*uuid.UUID, error) {
	var branch *uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT branch_id FROM locations WHERE id = $1`, locationID).Scan(&branch)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to resolve location branch: %w", err)
	}
	return branch, nil
}

func (r *PostgresRepository) AllocateStock(ctx context.Context, inventoryID uuid.UUID, delta float64) error {
	query := `
		UPDATE inventory
		SET allocated = allocated + $1, updated_at = NOW()
		WHERE id = $2 AND (quantity - allocated) >= $1
	`
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, query, delta, inventoryID)
	if err != nil {
		return fmt.Errorf("failed to allocate stock: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("insufficient available stock for allocation")
	}
	r.markDirtyByRow(ctx, inventoryID)
	return nil
}

func (r *PostgresRepository) DeallocateStock(ctx context.Context, inventoryID uuid.UUID, delta float64) error {
	query := `
		UPDATE inventory
		SET allocated = allocated - $1, updated_at = NOW()
		WHERE id = $2 AND allocated >= $1
	`
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, query, delta, inventoryID)
	if err != nil {
		return fmt.Errorf("failed to deallocate stock: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("insufficient allocated stock for deallocation")
	}
	r.markDirtyByRow(ctx, inventoryID)
	return nil
}

func (r *PostgresRepository) RevertFulfillStock(ctx context.Context, inventoryID uuid.UUID, delta float64) error {
	query := `
		UPDATE inventory
		SET quantity = quantity + $1, allocated = allocated + $1, updated_at = NOW()
		WHERE id = $2
	`
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, query, delta, inventoryID)
	if err != nil {
		return fmt.Errorf("failed to revert fulfillment: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("inventory record not found for revert")
	}
	r.markDirtyByRow(ctx, inventoryID)
	return nil
}

func (r *PostgresRepository) FulfillStock(ctx context.Context, inventoryID uuid.UUID, delta float64) error {
	query := `
		UPDATE inventory
		SET quantity = quantity - $1, allocated = allocated - $1, updated_at = NOW()
		WHERE id = $2 AND quantity >= $1 AND allocated >= $1
	`
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, query, delta, inventoryID)
	if err != nil {
		return fmt.Errorf("failed to fulfill stock: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("insufficient stock for fulfillment")
	}
	r.markDirtyByRow(ctx, inventoryID)
	return nil
}

// LockBranchInventory takes a product's rows at a branch FOR UPDATE, in
// inventory id order, through the context's executor.
func (r *PostgresRepository) LockBranchInventory(ctx context.Context, productID, branchID uuid.UUID) ([]Inventory, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT i.id, i.product_id, i.location_id, COALESCE(i.location, ''), i.quantity, i.allocated, i.updated_at
		FROM inventory i
		JOIN locations l ON l.id = i.location_id
		WHERE i.product_id = $1 AND l.branch_id = $2
		ORDER BY i.id
		FOR UPDATE OF i`, productID, branchID)
	if err != nil {
		return nil, fmt.Errorf("failed to lock inventory: %w", err)
	}
	defer rows.Close()
	var items []Inventory
	for rows.Next() {
		var i Inventory
		if err := rows.Scan(&i.ID, &i.ProductID, &i.LocationID, &i.Location, &i.Quantity, &i.Allocated, &i.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

func (r *PostgresRepository) AllocateStockQty(ctx context.Context, inventoryID uuid.UUID, delta int64) error {
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE inventory SET allocated = allocated + $1::numeric / 10000, updated_at = NOW()
		WHERE id = $2 AND (quantity - allocated) >= $1::numeric / 10000`, delta, inventoryID)
	if err != nil {
		return fmt.Errorf("failed to allocate stock: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrInsufficientAvailable
	}
	r.markDirtyByRow(ctx, inventoryID)
	return nil
}

func (r *PostgresRepository) DeallocateStockQty(ctx context.Context, inventoryID uuid.UUID, delta int64) error {
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE inventory SET allocated = allocated - $1::numeric / 10000, updated_at = NOW()
		WHERE id = $2 AND allocated >= $1::numeric / 10000`, delta, inventoryID)
	if err != nil {
		return fmt.Errorf("failed to release stock: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrInsufficientAllocated
	}
	r.markDirtyByRow(ctx, inventoryID)
	return nil
}

func (r *PostgresRepository) FulfillStockQty(ctx context.Context, inventoryID uuid.UUID, delta int64) error {
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE inventory SET quantity = quantity - $1::numeric / 10000, allocated = allocated - $1::numeric / 10000, updated_at = NOW()
		WHERE id = $2 AND quantity >= $1::numeric / 10000 AND allocated >= $1::numeric / 10000`, delta, inventoryID)
	if err != nil {
		return fmt.Errorf("failed to fulfill stock: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrInsufficientAllocated
	}
	r.markDirtyByRow(ctx, inventoryID)
	return nil
}

func (r *PostgresRepository) RestockQty(ctx context.Context, inventoryID uuid.UUID, delta int64) error {
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE inventory SET quantity = quantity + $1::numeric / 10000, updated_at = NOW() WHERE id = $2`, delta, inventoryID)
	if err != nil {
		return fmt.Errorf("failed to restock: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("inventory record not found for restock")
	}
	r.markDirtyByRow(ctx, inventoryID)
	return nil
}

// markDirtyByRow feeds the stock level job's queue (ADR 0008 section 10.2):
// any act that changes a quantity or an allocation marks the product and
// branch dirty. The insert takes no lock on existing rows.
func (r *PostgresRepository) markDirtyByRow(ctx context.Context, inventoryID uuid.UUID) {
	_, _ = r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO stock_level_dirty (product_id, branch_id)
		SELECT i.product_id, l.branch_id
		FROM inventory i JOIN locations l ON l.id = i.location_id
		WHERE i.id = $1 AND l.branch_id IS NOT NULL
		ON CONFLICT DO NOTHING`, inventoryID)
}

func (r *PostgresRepository) markDirtyByProductLocation(ctx context.Context, productID uuid.UUID, locationID *uuid.UUID) {
	_, _ = r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO stock_level_dirty (product_id, branch_id)
		SELECT $1, l.branch_id FROM locations l
		WHERE l.id = $2 AND l.branch_id IS NOT NULL
		ON CONFLICT DO NOTHING`, productID, locationID)
}

// StockWriter is the receipt's locked stock write: the row is created where
// absent and the update itself takes the row lock, so concurrent receipts
// of one product and location serialize on the row (ADR 0008 section 4
// step 2's lock, ahead of the stock ledger C4-2 A builds).
type StockWriter interface {
	ReceiveStockQty(ctx context.Context, productID, locationID uuid.UUID, delta int64) error
}

// ReceiveStockQty adds delta (the scale 4 integer) to the product's stock
// at the location, creating the row where absent, and marks the stock
// level dirty.
func (r *PostgresRepository) ReceiveStockQty(ctx context.Context, productID, locationID uuid.UUID, delta int64) error {
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO inventory (product_id, location_id, location, quantity, allocated)
		VALUES ($1, $2, '', $3::numeric / 10000, 0)
		ON CONFLICT DO NOTHING`, productID, locationID, delta)
	if err != nil {
		return fmt.Errorf("failed to create inventory row: %w", err)
	}
	_ = ct
	ct, err = r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE inventory SET quantity = quantity + $1::numeric / 10000, updated_at = NOW()
		WHERE product_id = $2 AND location_id = $3`, delta, productID, locationID)
	if err != nil {
		return fmt.Errorf("failed to receive stock: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("inventory record not found for receipt")
	}
	r.markDirtyByProductLocation(ctx, productID, &locationID)
	return nil
}

// StockLevel is one stock_levels row the job reads and writes.
type StockLevel struct {
	ProductID       uuid.UUID
	BranchID        uuid.UUID
	ReorderPoint    *float64
	LowSince        *time.Time
	Revision        int64
}

// NextDirtyStockLevel claims the oldest dirty row FOR UPDATE SKIP LOCKED,
// inside the caller's transaction; false when the queue is empty.
func (r *PostgresRepository) NextDirtyStockLevel(ctx context.Context) (productID, branchID uuid.UUID, ok bool, err error) {
	err = r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT d.product_id, d.branch_id
		FROM stock_level_dirty d
		ORDER BY d.product_id, d.branch_id
		LIMIT 1
		FOR UPDATE OF d SKIP LOCKED`).Scan(&productID, &branchID)
	if err == pgx.ErrNoRows {
		return uuid.Nil, uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, false, fmt.Errorf("claim dirty stock level: %w", err)
	}
	return productID, branchID, true, nil
}

// SumAvailable computes the branch's available stock of a product: the sum
// of quantity - allocated over the branch's rows.
func (r *PostgresRepository) SumAvailable(ctx context.Context, productID, branchID uuid.UUID) (string, error) {
	var available string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(i.quantity - i.allocated), 0)::text
		FROM inventory i JOIN locations l ON l.id = i.location_id
		WHERE i.product_id = $1 AND l.branch_id = $2`, productID, branchID).Scan(&available)
	return available, err
}

// GetStockLevel reads the product and branch's stock level row, if any.
func (r *PostgresRepository) GetStockLevel(ctx context.Context, productID, branchID uuid.UUID) (*StockLevel, error) {
	var sl StockLevel
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT product_id, branch_id, reorder_point::float8, low_since, revision
		FROM stock_levels WHERE product_id = $1 AND branch_id = $2`,
		productID, branchID).Scan(&sl.ProductID, &sl.BranchID, &sl.ReorderPoint, &sl.LowSince, &sl.Revision)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sl, nil
}

// UpsertStockLevel keeps the product and branch's stock level row (creating
// it from the product's own targets where absent) and sets low_since.
func (r *PostgresRepository) UpsertStockLevel(ctx context.Context, productID, branchID uuid.UUID, lowSince *time.Time) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO stock_levels (product_id, branch_id, reorder_point, reorder_quantity, low_since, revision, updated_at)
		SELECT $1, $2, p.reorder_point, p.reorder_qty, $3, 1, NOW() FROM products p WHERE p.id = $1
		ON CONFLICT (product_id, branch_id) DO UPDATE
		SET low_since = EXCLUDED.low_since, revision = stock_levels.revision + 1, updated_at = NOW()`,
		productID, branchID, lowSince)
	return err
}

// ClearDirtyStockLevel removes the processed row.
func (r *PostgresRepository) ClearDirtyStockLevel(ctx context.Context, productID, branchID uuid.UUID) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`DELETE FROM stock_level_dirty WHERE product_id = $1 AND branch_id = $2`, productID, branchID)
	return err
}

// ProductStockingUnit reads the product's stocking unit for the event data.
func (r *PostgresRepository) ProductStockingUnit(ctx context.Context, productID uuid.UUID) (string, error) {
	var uom string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT uom_primary::text FROM products WHERE id = $1`, productID).Scan(&uom)
	return uom, err
}
