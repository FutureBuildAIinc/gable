// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/units"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ResolveRepository is the read side pricing.Resolve and pricing.ResolveCost
// price through: the product's pricing facts and unit set, the contract, the
// explicit prices, the customer's program (profile and level), and the
// vendor costs. The writes keep the main Repository interface.
type ResolveRepository interface {
	// ProductPricingFacts reads the columns every rung starts from: the base
	// price and its unit, the stocking unit, the default sale unit, the
	// average cost and the primary vendor.
	ProductPricingFacts(ctx context.Context, productID uuid.UUID) (*ProductPricingFacts, error)
	// UnitSetRow reads one row of the product's unit set; nil when the unit
	// is not a row of the set.
	UnitSetRow(ctx context.Context, productID uuid.UUID, uom string) (*UnitSetRowFact, error)
	// ContractForBranch answers the customer's contract for the product, the
	// branch's row before the all branch row.
	ContractForBranch(ctx context.Context, customerID, productID uuid.UUID, branch *uuid.UUID) (*CustomerContract, error)
	// ProductPrice answers one explicit price: the level and branch named,
	// each nullable.
	ProductPrice(ctx context.Context, productID uuid.UUID, levelID, branch *uuid.UUID) (*ProductPrice, error)
	// CustomerProgram answers the customer's pricing program: the profile and
	// the level it names, with the level's basis and percent. Nil when the
	// customer is anonymous (an anonymous price read runs rungs 5 and 6).
	CustomerProgram(ctx context.Context, customerID uuid.UUID) (*PricingProgram, error)
	// VendorCost answers the vendor's cost for the product per section 5.5:
	// the level named, or the vendor's default level when none is, in effect
	// on the date, the branch's row before the all branch row, the latest
	// effective_from first.
	VendorCost(ctx context.Context, vendorID, productID uuid.UUID, branch, level *uuid.UUID, on time.Time) (*VendorCost, error)
}

// ProductPricingFacts is what every rung starts from.
type ProductPricingFacts struct {
	BasePrice       httpx.Price // zero when the product has none
	PriceUOM        string      // the base price's unit
	StockUOM        string      // the stocking unit
	SaleUOM         string      // the default sale unit
	AverageUnitCost httpx.Price // zero when there is none
	PrimaryVendorID *uuid.UUID
}

// UnitSetRowFact is one row of the product's unit set: unit_qty of the row's
// unit is stock_qty of the stocking unit, canonical.
type UnitSetRowFact struct {
	UOM      string
	UnitQty  httpx.Quantity
	StockQty httpx.Quantity
}

// PairOf is the row's conversion pair: unit_qty of the row's unit is
// stock_qty of the stocking unit.
func (r UnitSetRowFact) PairOf() units.Pair { return units.Pair{A: r.UnitQty, B: r.StockQty} }

// ProductPrice is one explicit level or branch price.
type ProductPrice struct {
	ID           uuid.UUID
	ProductID    uuid.UUID
	PriceLevelID *uuid.UUID // nil: the branch's base price
	BranchID     *uuid.UUID // nil: every branch
	PriceUOM     string
	UnitPrice    httpx.Price
	Revision     int64
}

// PricingProgram is the customer's pricing program: the profile it is on and
// the level the profile names.
type PricingProgram struct {
	ProfileID   uuid.UUID
	ProfileCode string
	ProfileName string
	LevelID     uuid.UUID
	LevelCode   string
	LevelName   string
	LevelBasis  string
	LevelAdjust httpx.Quantity
	LevelActive bool
}

// VendorCost is one vendor cost row with its level's code.
type VendorCost struct {
	ID                uuid.UUID
	VendorID          uuid.UUID
	VendorLevelID     uuid.UUID
	VendorLevelCode   string
	ProductID         uuid.UUID
	BranchID          *uuid.UUID
	PurchaseUOM       string
	UnitCost          httpx.Price
	VendorSKU         *string
	EffectiveFrom     time.Time
	EffectiveTo       *time.Time
	Revision          int64
}

// PostgresResolveRepository reads the pricing tables.
type PostgresResolveRepository struct {
	db *database.DB
}

// NewResolveRepository builds the resolve side reads.
func NewResolveRepository(db *database.DB) *PostgresResolveRepository {
	return &PostgresResolveRepository{db: db}
}

func (r *PostgresResolveRepository) ProductPricingFacts(ctx context.Context, productID uuid.UUID) (*ProductPricingFacts, error) {
	var f ProductPricingFacts
	var base, cost string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT base_price::text, price_uom, uom_primary, sale_uom,
		       COALESCE(average_unit_cost, 0)::text, vendor_id
		FROM products WHERE id = $1`, productID).
		Scan(&base, &f.PriceUOM, &f.StockUOM, &f.SaleUOM, &cost, &f.PrimaryVendorID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, httpx.NotFound("no such product")
		}
		return nil, fmt.Errorf("product pricing facts: %w", err)
	}
	if f.BasePrice, err = httpx.ParsePrice(base); err != nil {
		return nil, fmt.Errorf("product base price: %w", err)
	}
	if f.AverageUnitCost, err = httpx.ParsePrice(cost); err != nil {
		return nil, fmt.Errorf("product average cost: %w", err)
	}
	return &f, nil
}

func (r *PostgresResolveRepository) UnitSetRow(ctx context.Context, productID uuid.UUID, uom string) (*UnitSetRowFact, error) {
	var row UnitSetRowFact
	var unitQty, stockQty string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT uom, unit_qty::text, stock_qty::text
		FROM product_units WHERE product_id = $1 AND uom = $2`, productID, uom).
		Scan(&row.UOM, &unitQty, &stockQty)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("unit set row: %w", err)
	}
	if row.UnitQty, err = httpx.ParseQuantity(unitQty); err != nil {
		return nil, fmt.Errorf("unit set pair: %w", err)
	}
	if row.StockQty, err = httpx.ParseQuantity(stockQty); err != nil {
		return nil, fmt.Errorf("unit set pair: %w", err)
	}
	return &row, nil
}

func (r *PostgresResolveRepository) ContractForBranch(ctx context.Context, customerID, productID uuid.UUID, branch *uuid.UUID) (*CustomerContract, error) {
	var c CustomerContract
	var price string
	var branchID *uuid.UUID
	var created, updated time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT id, customer_id, product_id, contract_price::text, price_uom, branch_id, revision, created_at, updated_at
		FROM customer_contracts
		WHERE customer_id = $1 AND product_id = $2
		  AND (branch_id IS NOT DISTINCT FROM $3 OR ($3::uuid IS NOT NULL AND branch_id IS NULL))
		ORDER BY branch_id NULLS LAST
		LIMIT 1`, customerID, productID, branch).
		Scan(&c.ID, &c.CustomerID, &c.ProductID, &price, &c.PriceUOM, &branchID, &c.Revision, &created, &updated)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("contract lookup: %w", err)
	}
	c.BranchID = branchID
	if c.ContractPrice, err = httpx.ParsePrice(price); err != nil {
		return nil, fmt.Errorf("contract price: %w", err)
	}
	c.CreatedAt = httpx.TimestampOf(created)
	c.UpdatedAt = httpx.TimestampOf(updated)
	return &c, nil
}

func (r *PostgresResolveRepository) ProductPrice(ctx context.Context, productID uuid.UUID, levelID, branch *uuid.UUID) (*ProductPrice, error) {
	var p ProductPrice
	var price string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT id, product_id, price_level_id, branch_id, price_uom, unit_price::text, revision
		FROM product_prices
		WHERE product_id = $1 AND price_level_id IS NOT DISTINCT FROM $2 AND branch_id IS NOT DISTINCT FROM $3`,
		productID, levelID, branch).
		Scan(&p.ID, &p.ProductID, &p.PriceLevelID, &p.BranchID, &p.PriceUOM, &price, &p.Revision)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("product price lookup: %w", err)
	}
	if p.UnitPrice, err = httpx.ParsePrice(price); err != nil {
		return nil, fmt.Errorf("product price: %w", err)
	}
	return &p, nil
}

func (r *PostgresResolveRepository) CustomerProgram(ctx context.Context, customerID uuid.UUID) (*PricingProgram, error) {
	var p PricingProgram
	var adjust string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT pp.id, pp.code, pp.name, pl.id, pl.code, pl.name, pl.basis,
		       pl.adjust_percent::text, pl.is_active
		FROM customers c
		JOIN price_profiles pp ON pp.id = c.price_profile_id
		JOIN price_levels pl ON pl.id = pp.price_level_id
		WHERE c.id = $1`, customerID).
		Scan(&p.ProfileID, &p.ProfileCode, &p.ProfileName, &p.LevelID, &p.LevelCode, &p.LevelName,
			&p.LevelBasis, &adjust, &p.LevelActive)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("customer pricing program: %w", err)
	}
	if p.LevelAdjust, err = httpx.ParseQuantity(adjust); err != nil {
		return nil, fmt.Errorf("level adjust percent: %w", err)
	}
	return &p, nil
}

func (r *PostgresResolveRepository) VendorCost(ctx context.Context, vendorID, productID uuid.UUID, branch, level *uuid.UUID, on time.Time) (*VendorCost, error) {
	row, err := r.vendorCostRow(ctx, vendorID, productID, branch, level, on)
	if err != nil {
		return nil, err
	}
	if row == nil && level != nil {
		// When the requested level has none, the default level's.
		row, err = r.vendorCostRow(ctx, vendorID, productID, branch, nil, on)
		if err != nil {
			return nil, err
		}
	}
	return row, nil
}

func (r *PostgresResolveRepository) vendorCostRow(ctx context.Context, vendorID, productID uuid.UUID, branch, level *uuid.UUID, on time.Time) (*VendorCost, error) {
	var c VendorCost
	var cost string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT vc.id, vc.vendor_id, vpl.id, vpl.code, vc.product_id, vc.branch_id,
		       vc.purchase_uom, vc.unit_cost::text, vc.vendor_sku, vc.effective_from,
		       vc.effective_to, vc.revision
		FROM vendor_product_costs vc
		JOIN vendor_price_levels vpl ON vpl.id = vc.vendor_price_level_id
		WHERE vc.vendor_id = $1 AND vc.product_id = $2
		  AND vpl.vendor_id = $1
		  AND (vpl.id = $4 OR ($4 IS NULL AND vpl.is_default))
		  AND vc.branch_id IS NOT DISTINCT FROM $3
		  AND vc.effective_from <= $5 AND (vc.effective_to IS NULL OR vc.effective_to >= $5)
		ORDER BY vc.branch_id NULLS LAST, vc.effective_from DESC
		LIMIT 1`, vendorID, productID, branch, level, on).
		Scan(&c.ID, &c.VendorID, &c.VendorLevelID, &c.VendorLevelCode, &c.ProductID, &c.BranchID,
			&c.PurchaseUOM, &cost, &c.VendorSKU, &c.EffectiveFrom, &c.EffectiveTo, &c.Revision)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("vendor cost lookup: %w", err)
	}
	if c.UnitCost, err = httpx.ParsePrice(cost); err != nil {
		return nil, fmt.Errorf("vendor cost: %w", err)
	}
	return &c, nil
}
