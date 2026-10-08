// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNotFound is the read answer for a row that is not there.
var ErrNotFound = errors.New("product not found")

// ErrStaleRevision is a write's answer when the row moved past the revision
// the caller built on (ADR 0001 section 11).
var ErrStaleRevision = errors.New("stale revision")

// Repository defines the interface for product data access
type Repository interface {
	CreateProduct(ctx context.Context, p *Product) error
	GetProduct(ctx context.Context, id uuid.UUID) (*Product, error)
	ListProducts(ctx context.Context) ([]Product, error)
	ListProductsPage(ctx context.Context, after *time.Time, afterID *uuid.UUID, limit int) ([]Product, error)
	CountProducts(ctx context.Context) (int64, error)
	ListBelowReorder(ctx context.Context) ([]ReorderAlert, error)
	UpdateAverageCost(ctx context.Context, id uuid.UUID, avgCost float64) error
	UpdateMarginRules(ctx context.Context, id uuid.UUID, targetMargin float64, commissionRate float64, revision int64) (int64, error)
	UpdateReorderTargets(ctx context.Context, id uuid.UUID, reorderPoint, reorderQty float64) error
	UpdateVendor(ctx context.Context, id uuid.UUID, vendorName *string, vendorID *uuid.UUID) error
	UpdateDimensions(ctx context.Context, id uuid.UUID, g Geometry, revision int64) (int64, error)
	UpdateLeadTime(ctx context.Context, id uuid.UUID, leadTimeDays *int, revision int64) (int64, error)
}

// PostgresRepository implements Repository using pgx
type PostgresRepository struct {
	db *database.DB
}

// NewRepository creates a new PostgresRepository
func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// productColumns reads every column the domain row carries, with the scaled
// and quantity columns read as text so the scan is exact (ADR 0001 section
// 7: never through float64).
const productColumns = `p.id, p.sku, p.description, p.uom_primary, p.base_price::text, p.vendor, p.vendor_id, p.upc,
	       COALESCE(p.weight_lbs, 0)::text,
	       p.length_in, p.width_in, p.height_in, p.stackable, p.geometry_source,
	       COALESCE(p.reorder_point, 0)::text, COALESCE(p.reorder_qty, 0)::text,
	       p.lead_time_days, p.revision, p.created_at, p.updated_at,
	       COALESCE(p.average_unit_cost, 0)::text, COALESCE(p.target_margin, 0), COALESCE(p.commission_rate, 0)`

// scanProduct fills a domain row from the shared column list.
func scanProduct(scanner interface{ Scan(dest ...any) error }) (*Product, error) {
	var p Product
	var basePrice, weight, reorderPoint, reorderQty, avgCost string
	var created, updated time.Time
	if err := scanner.Scan(
		&p.ID, &p.SKU, &p.Description, &p.UOMPrimary, &basePrice, &p.Vendor, &p.VendorID, &p.UPC,
		&weight,
		&p.LengthIn, &p.WidthIn, &p.HeightIn, &p.Stackable, &p.GeometrySource,
		&reorderPoint, &reorderQty,
		&p.LeadTimeDays, &p.Revision, &created, &updated,
		&avgCost, &p.TargetMargin, &p.CommissionRate,
	); err != nil {
		return nil, err
	}
	var err error
	if p.BasePriceScaled, err = httpx.ParsePrice(basePrice); err != nil {
		return nil, fmt.Errorf("read base price: %w", err)
	}
	p.BasePrice = float64(p.BasePriceScaled) / 10_000
	if p.ReorderPointQ, err = httpx.ParseQuantity(reorderPoint); err != nil {
		return nil, fmt.Errorf("read reorder point: %w", err)
	}
	p.ReorderPoint = qtyFloat(p.ReorderPointQ)
	if p.ReorderQtyQ, err = httpx.ParseQuantity(reorderQty); err != nil {
		return nil, fmt.Errorf("read reorder qty: %w", err)
	}
	p.ReorderQty = qtyFloat(p.ReorderQtyQ)
	if p.AverageUnitCostScaled, err = httpx.ParsePrice(avgCost); err != nil {
		return nil, fmt.Errorf("read average cost: %w", err)
	}
	p.AverageUnitCost = float64(p.AverageUnitCostScaled) / 10_000
	if w, err := httpx.ParseQuantity(weight); err == nil {
		p.WeightLbs = qtyFloat(w)
	}
	p.CreatedAt = httpx.TimestampOf(created)
	p.UpdatedAt = httpx.TimestampOf(updated)
	return &p, nil
}

// inventoryTotals reads a product's stock totals into the domain row: the
// stocking unit sums of quantity, allocated and their difference (ADR 0006
// 7.1), with the legacy float copies beside them.
func (r *PostgresRepository) inventoryTotals(ctx context.Context, p *Product) error {
	row := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(i.quantity), 0)::text, COALESCE(SUM(i.allocated), 0)::text
		FROM inventory i WHERE i.product_id = $1`, p.ID)
	var onHand, allocated string
	if err := row.Scan(&onHand, &allocated); err != nil {
		return fmt.Errorf("failed to read stock totals: %w", err)
	}
	q, err := httpx.ParseQuantity(onHand)
	if err != nil {
		return fmt.Errorf("failed to read on hand: %w", err)
	}
	a, err := httpx.ParseQuantity(allocated)
	if err != nil {
		return fmt.Errorf("failed to read allocated: %w", err)
	}
	p.OnHand, p.Allocated, p.Available = q, a, q-a
	p.TotalQuantity, p.TotalAllocated = qtyFloat(q), qtyFloat(a)
	return nil
}

// qtyFloat renders a quantity as the float the unconverted readers expect.
func qtyFloat(q httpx.Quantity) float64 { return float64(q) / 10_000 }

// CreateProduct inserts a new product into the database
func (r *PostgresRepository) CreateProduct(ctx context.Context, p *Product) error {
	query := `
		INSERT INTO products (sku, description, uom_primary, base_price, vendor, vendor_id, upc,
		                      weight_lbs, length_in, width_in, height_in, stackable, geometry_source,
		                      reorder_point, reorder_qty)
		VALUES ($1, $2, $3, $4::numeric, $5, $6, $7, $8::numeric, $9, $10, $11, $12, $13, $14::numeric, $15::numeric)
		RETURNING id, created_at, updated_at, revision, average_unit_cost::text, target_margin, commission_rate`

	var avgCost string
	var created, updated time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, p.SKU, p.Description, p.UOMPrimary,
		p.BasePriceScaled.DecimalString(), p.Vendor, p.VendorID, p.UPC,
		p.WeightLbs, p.LengthIn, p.WidthIn, p.HeightIn, p.Stackable, p.GeometrySource,
		p.ReorderPointQ.DecimalString(), p.ReorderQtyQ.DecimalString(),
	).Scan(
		&p.ID, &created, &updated, &p.Revision, &avgCost, &p.TargetMargin, &p.CommissionRate,
	)
	if err != nil {
		return mapWriteError(err)
	}
	if p.AverageUnitCostScaled, err = httpx.ParsePrice(avgCost); err != nil {
		return fmt.Errorf("read average cost: %w", err)
	}
	p.AverageUnitCost = float64(p.AverageUnitCostScaled) / 10_000
	p.CreatedAt, p.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

// mapWriteError turns a database refusal into the boundary error the handler
// answers: a unique violation names the field that collided, a foreign key
// violation names the reference that does not exist (the recipe's rule: a
// bad reference is a 400, never a 500).
func mapWriteError(err error) error {
	return fmt.Errorf("failed to create product: %w", err)
}

// GetProduct retrieves a product by its ID
func (r *PostgresRepository) GetProduct(ctx context.Context, id uuid.UUID) (*Product, error) {
	query := `
		SELECT ` + productColumns + `
		FROM products p
		WHERE p.id = $1`

	p, err := scanProduct(r.db.GetExecutor(ctx).QueryRow(ctx, query, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get product: %w", err)
	}
	if err := r.inventoryTotals(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// ListProducts retrieves all products (the reorder scheduler's read)
func (r *PostgresRepository) ListProducts(ctx context.Context) ([]Product, error) {
	query := `
		SELECT ` + productColumns + `
		FROM products p
		ORDER BY p.sku ASC`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list products: %w", err)
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan product: %w", err)
		}
		products = append(products, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row error: %w", err)
	}
	return products, nil
}

// ListProductsPage is the list's keyset query: `created_at DESC, id DESC`,
// the ordering migration 093 built its index on. after is nil for the first
// page; the caller asks for limit+1 rows and reads whether another page
// exists.
func (r *PostgresRepository) ListProductsPage(ctx context.Context, after *time.Time, afterID *uuid.UUID, limit int) ([]Product, error) {
	query := `SELECT ` + productColumns + ` FROM products p`
	args := []any{}
	if after != nil {
		query += ` WHERE (p.created_at, p.id) < ($1, $2)`
		args = append(args, *after, *afterID)
	}
	query += ` ORDER BY p.created_at DESC, p.id DESC`
	args = append(args, limit)
	query += fmt.Sprintf(` LIMIT $%d`, len(args))

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list products: %w", err)
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan product: %w", err)
		}
		if err := r.inventoryTotals(ctx, p); err != nil {
			return nil, err
		}
		products = append(products, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row error: %w", err)
	}
	return products, nil
}

// CountProducts is the include=total query.
func (r *PostgresRepository) CountProducts(ctx context.Context) (int64, error) {
	var total int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT COUNT(*) FROM products`).Scan(&total); err != nil {
		return 0, fmt.Errorf("failed to count products: %w", err)
	}
	return total, nil
}

// ListBelowReorder returns products whose current stock is below their reorder point
func (r *PostgresRepository) ListBelowReorder(ctx context.Context) ([]ReorderAlert, error) {
	query := `
		SELECT p.id, p.sku, p.description, p.vendor, p.vendor_id,
		       p.reorder_point::text, COALESCE(p.reorder_qty, 0)::text,
		       COALESCE(SUM(i.quantity), 0)::text AS current_stock,
		       (p.reorder_point - COALESCE(SUM(i.quantity), 0))::text AS deficit
		FROM products p
		LEFT JOIN inventory i ON p.id = i.product_id
		WHERE p.reorder_point > 0
		GROUP BY p.id
		HAVING COALESCE(SUM(i.quantity), 0) < p.reorder_point
		ORDER BY (p.reorder_point - COALESCE(SUM(i.quantity), 0)) DESC`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list reorder alerts: %w", err)
	}
	defer rows.Close()

	var alerts []ReorderAlert
	for rows.Next() {
		var a ReorderAlert
		var rp, rq, stock, deficit string
		if err := rows.Scan(
			&a.ProductID, &a.SKU, &a.Description, &a.Vendor, &a.VendorID,
			&rp, &rq, &stock, &deficit,
		); err != nil {
			return nil, fmt.Errorf("failed to scan reorder alert: %w", err)
		}
		var err error
		if a.ReorderPoint, err = httpx.ParseQuantity(rp); err != nil {
			return nil, fmt.Errorf("failed to read reorder point: %w", err)
		}
		if a.ReorderQty, err = httpx.ParseQuantity(rq); err != nil {
			return nil, fmt.Errorf("failed to read reorder qty: %w", err)
		}
		if a.CurrentStock, err = httpx.ParseQuantity(stock); err != nil {
			return nil, fmt.Errorf("failed to read current stock: %w", err)
		}
		if a.Deficit, err = httpx.ParseQuantity(deficit); err != nil {
			return nil, fmt.Errorf("failed to read deficit: %w", err)
		}
		alerts = append(alerts, a)
	}
	return alerts, nil
}

// UpdateVendor writes both vendor (display name) and vendor_id (FK) atomically.
func (r *PostgresRepository) UpdateVendor(ctx context.Context, id uuid.UUID, vendorName *string, vendorID *uuid.UUID) error {
	query := `UPDATE products SET vendor = $1, vendor_id = $2, updated_at = NOW() WHERE id = $3`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, vendorName, vendorID, id)
	return err
}

func (r *PostgresRepository) UpdateAverageCost(ctx context.Context, id uuid.UUID, avgCost float64) error {
	query := `UPDATE products SET average_unit_cost = $1, updated_at = NOW() WHERE id = $2`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, avgCost, id)
	return err
}

// conditionalUpdate runs one revision-carrying update: the check and the
// write are one database act, and zero rows means the caller's revision is
// stale or the row is gone (ADR 0001 section 11).
func (r *PostgresRepository) conditionalUpdate(ctx context.Context, id uuid.UUID, revision int64, sets string, args ...any) (int64, error) {
	query := fmt.Sprintf(`UPDATE products SET %s, revision = revision + 1, updated_at = NOW()
		WHERE id = $1 AND revision = $2 RETURNING revision`, sets)
	all := append([]any{id, revision}, args...)
	var newRevision int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, all...).Scan(&newRevision)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Tell a missing row from a stale revision: the caller answers
			// 404 or 409 from the two errors.
			var exists bool
			if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE id = $1)`, id).Scan(&exists); err == nil && exists {
				return 0, ErrStaleRevision
			}
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("failed to update product: %w", err)
	}
	return newRevision, nil
}

func (r *PostgresRepository) UpdateMarginRules(ctx context.Context, id uuid.UUID, targetMargin float64, commissionRate float64, revision int64) (int64, error) {
	return r.conditionalUpdate(ctx, id, revision, `target_margin = $3, commission_rate = $4`, targetMargin, commissionRate)
}

// UpdateReorderTargets writes the recomputed reorder_point and reorder_qty
// produced by the auto-reorder scheduler's RefreshReorderTargets job. The
// scheduler is a system writer and carries no revision.
func (r *PostgresRepository) UpdateReorderTargets(ctx context.Context, id uuid.UUID, reorderPoint, reorderQty float64) error {
	query := `UPDATE products SET reorder_point = $1, reorder_qty = $2, updated_at = NOW() WHERE id = $3`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, reorderPoint, reorderQty, id)
	return err
}

// UpdateLeadTime writes the dealer-published lead time (migration 084).
//
// leadTimeDays is a POINTER for the same reason the geometry columns are: nil
// must reach the column as SQL NULL. NULL means "the dealer has not published
// a lead time", and the portal catalog renders it as null so a consumer's
// lead-time-vs-delivery-date warning stays silent rather than being computed
// from a zero. Writing 0 here would say "available today", which is a
// different — and schedulable — claim.
func (r *PostgresRepository) UpdateLeadTime(ctx context.Context, id uuid.UUID, leadTimeDays *int, revision int64) (int64, error) {
	return r.conditionalUpdate(ctx, id, revision, `lead_time_days = $3`, leadTimeDays)
}

// updateDimensionsQuery is the geometry write. It is a package-level constant
// rather than a local so TestUpdateDimensionsQuery_WritesRawNulls can assert
// that no COALESCE / NULLIF ever creeps back into it: substituting a default
// here is exactly how the nullable-geometry contract would be lost.
const updateDimensionsQuery = `
	UPDATE products
	SET length_in = $3, width_in = $4, height_in = $5,
	    stackable = $6, geometry_source = $7, revision = revision + 1
	WHERE id = $1 AND revision = $2
	RETURNING revision`

// UpdateDimensions writes the parametric 3D geometry (inches) that the PIM owns
// as the canonical digital-twin source consumed by AI_LM's Load Builder.
//
// Every column is written from a POINTER and is deliberately NOT COALESCEd: a
// nil field lands in Postgres as SQL NULL, which is the only way to record "no
// geometry entered for this SKU". Substituting 0 (or TRUE for stackable) here
// would make GET /api/integration/products report a real zero-volume box, and
// AI_LM's resolveGeometry() would trust it instead of falling back to its own
// defaults. See migration 080_ailm_integration_contract.sql.
func (r *PostgresRepository) UpdateDimensions(ctx context.Context, id uuid.UUID, g Geometry, revision int64) (int64, error) {
	var newRevision int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, updateDimensionsQuery,
		id, revision, g.LengthIn, g.WidthIn, g.HeightIn, g.Stackable, g.GeometrySource).Scan(&newRevision)
	if err != nil {
		if err == pgx.ErrNoRows {
			var exists bool
			if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE id = $1)`, id).Scan(&exists); err == nil && exists {
				return 0, ErrStaleRevision
			}
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("failed to update product dimensions: %w", err)
	}
	return newRevision, nil
}
