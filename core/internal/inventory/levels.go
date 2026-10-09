// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory

// The inventory read on the wire contract (ADR 0006 7.2, item C3-1b): one
// levels list, keyset paged, every quantity a scale 4 decimal string in the
// product's stocking unit with `available` and `uom` beside it, and the
// product summary embedded under include=product. Inventory writes stay for
// C4-1; this file touches the read side only.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Level is one inventory row on the wire: the quantities of ADR 0001 section
// 7a (decimal strings at scale 4, in the product's stocking unit, `uom` beside
// them), `available` as quantity - allocated (ADR 0006 7.2), the location's
// name beside its id, and the product summary embedded under include=product
// and only then. CreatedAt rides the struct for the cursor and stays off the
// wire.
type Level struct {
	ID           uuid.UUID       `json:"id"`
	ProductID    uuid.UUID       `json:"product_id"`
	LocationID   *uuid.UUID      `json:"location_id"`
	LocationName string          `json:"location_name"`
	Quantity     httpx.Quantity  `json:"quantity"`
	Allocated    httpx.Quantity  `json:"allocated"`
	Available    httpx.Quantity  `json:"available"`
	UOM          string          `json:"uom"`
	Product      *ProductSummary `json:"product,omitempty"`
	CreatedAt    httpx.Timestamp `json:"-"`
	UpdatedAt    httpx.Timestamp `json:"updated_at"`
}

// ProductSummary is the product an inventory row stocks, embedded under
// include=product (ADR 0001 section 1 names this expansion): the identity
// fields a levels screen shows beside the numbers, with the stocking unit the
// quantities are counted in.
type ProductSummary struct {
	ID          uuid.UUID `json:"id"`
	SKU         string    `json:"sku"`
	Description string    `json:"description"`
	StockUOM    string    `json:"stock_uom"`
}

// LevelFilters are the levels list's declared filters; nil is no filter.
type LevelFilters struct {
	ProductID  *uuid.UUID
	LocationID *uuid.UUID
}

// LevelStore is the levels read's store. It sits beside Repository rather
// than inside it so the module's test fakes (and the portal's) keep
// compiling; *PostgresRepository satisfies it.
type LevelStore interface {
	ListLevelsPage(ctx context.Context, filters LevelFilters, after *time.Time, afterID *uuid.UUID, limit int) ([]Level, error)
	CountLevels(ctx context.Context, filters LevelFilters) (int64, error)
}

// ErrNoLevelStore answers a levels read against a store that cannot serve it
// (a unit test fake); serve always wires the real repository.
var ErrNoLevelStore = errors.New("inventory: this deployment's inventory store cannot serve the levels read")

// ListLevelsPage is the levels list's keyset page, `created_at DESC, id DESC`
// (migration 098 gave the table the ordering), limit+1 rows read by the
// service so the handler knows whether another page exists. The branch wall
// is the list rule PR 44 settled and this read keeps: a context branch lists
// its own rows; with no context branch a bound non-admin user lists the
// branches granted to the user, none granted listing none; an administrator
// without a header, an unbound key, the single-branch switch and callers with
// no branch context at all list every branch's.
func (r *PostgresRepository) ListLevelsPage(ctx context.Context, filters LevelFilters, after *time.Time, afterID *uuid.UUID, limit int) ([]Level, error) {
	query := `
		SELECT i.id, i.product_id, i.location_id,
		       COALESCE(NULLIF(l.path, ''), i.location, '') AS location_name,
		       i.quantity::text, i.allocated::text, (i.quantity - i.allocated)::text,
		       p.uom_primary::text, p.sku, p.description,
		       i.created_at, i.updated_at
		  FROM inventory i
		  LEFT JOIN locations l ON l.id = i.location_id
		  JOIN products p ON p.id = i.product_id
		 WHERE ($1::uuid IS NULL OR i.product_id = $1)
		   AND ($2::uuid IS NULL OR i.location_id = $2)
		   AND (
			($3::uuid IS NOT NULL AND l.branch_id = $3)
			OR ($3::uuid IS NULL AND $4::text IS NOT NULL AND l.branch_id IN
			    (SELECT branch_id FROM user_locations WHERE user_sub = $4))
			OR ($3::uuid IS NULL AND $4::text IS NULL))
		   AND ($5::timestamptz IS NULL OR (i.created_at, i.id) < ($5, $6))
		 ORDER BY i.created_at DESC, i.id DESC
		 LIMIT $7`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query,
		filters.ProductID, filters.LocationID,
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx),
		after, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list inventory levels: %w", err)
	}
	defer rows.Close()
	var levels []Level
	for rows.Next() {
		lv, err := scanLevel(rows)
		if err != nil {
			return nil, err
		}
		levels = append(levels, *lv)
	}
	return levels, rows.Err()
}

// CountLevels is the include=total count under the same filters and the same
// branch wall as the page.
func (r *PostgresRepository) CountLevels(ctx context.Context, filters LevelFilters) (int64, error) {
	var total int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COUNT(*)
		  FROM inventory i
		  LEFT JOIN locations l ON l.id = i.location_id
		 WHERE ($1::uuid IS NULL OR i.product_id = $1)
		   AND ($2::uuid IS NULL OR i.location_id = $2)
		   AND (
			($3::uuid IS NOT NULL AND l.branch_id = $3)
			OR ($3::uuid IS NULL AND $4::text IS NOT NULL AND l.branch_id IN
			    (SELECT branch_id FROM user_locations WHERE user_sub = $4))
			OR ($3::uuid IS NULL AND $4::text IS NULL))`,
		filters.ProductID, filters.LocationID,
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count inventory levels: %w", err)
	}
	return total, nil
}

// scanLevel reads one row of the levels page: the NUMERIC columns as text and
// through the package's fixed scale parse, never float64 (ADR 0001 section 7).
func scanLevel(rows pgx.Rows) (*Level, error) {
	var lv Level
	var quantity, allocated, available string
	var sku, description *string
	var created, updated time.Time
	if err := rows.Scan(&lv.ID, &lv.ProductID, &lv.LocationID, &lv.LocationName,
		&quantity, &allocated, &available, &lv.UOM, &sku, &description,
		&created, &updated); err != nil {
		return nil, fmt.Errorf("scan inventory level: %w", err)
	}
	var err error
	if lv.Quantity, err = httpx.ParseQuantity(quantity); err != nil {
		return nil, fmt.Errorf("read inventory quantity: %w", err)
	}
	if lv.Allocated, err = httpx.ParseQuantity(allocated); err != nil {
		return nil, fmt.Errorf("read inventory allocated: %w", err)
	}
	if lv.Available, err = httpx.ParseQuantity(available); err != nil {
		return nil, fmt.Errorf("read inventory available: %w", err)
	}
	lv.CreatedAt, lv.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	lv.Product = &ProductSummary{ID: lv.ProductID, StockUOM: lv.UOM}
	if sku != nil {
		lv.Product.SKU = *sku
	}
	if description != nil {
		lv.Product.Description = *description
	}
	return &lv, nil
}

// ListLevelsPage serves one page of the levels list; the handler embeds the
// product summary only when include=product asked for it.
func (s *Service) ListLevelsPage(ctx context.Context, filters LevelFilters, after *time.Time, afterID *uuid.UUID, limit int) ([]Level, bool, error) {
	if s.levels == nil {
		return nil, false, ErrNoLevelStore
	}
	rows, err := s.levels.ListLevelsPage(ctx, filters, after, afterID, limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	return rows, more, nil
}

// CountLevels is the include=total count under the filters and the wall.
func (s *Service) CountLevels(ctx context.Context, filters LevelFilters) (int64, error) {
	if s.levels == nil {
		return 0, ErrNoLevelStore
	}
	return s.levels.CountLevels(ctx, filters)
}
