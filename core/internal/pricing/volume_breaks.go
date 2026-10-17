// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"fmt"
	"math/big"
	"sort"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// OneQuantity is the quantity 1 at the wire scale, the ladder's baseline.
const OneQuantity httpx.Quantity = 10_000

// VolumeBreak is one rung of the "buy N and the unit price drops" ladder for a
// (customer, product) pair.
//
// The float64 fields keep the result shape the portal reads (ADR 0006 section
// 9.1 keeps VolumeBreaks' signature and result shape); each now carries the
// exact scale 4 value the engine answered, never a cent rounding.
type VolumeBreak struct {
	MinQuantity float64       `json:"min_quantity"`
	UnitPrice   float64       `json:"unit_price"`
	Source      PricingSource `json:"price_source"`
	Details     string        `json:"details"`

	// SavesPerUnit is UnitPrice's improvement over the price this customer
	// would pay for a single unit, per unit. It is always > 0: a rung that
	// does not actually beat the current unit price is not returned at all
	// (see VolumeBreaks).
	SavesPerUnit float64 `json:"saves_per_unit"`
}

// VolumeBreaks returns the quantity thresholds at which this customer's unit
// price for this product actually improves, and the price at each.
//
// This is a PROJECTION OF THE EXISTING WATERFALL, not a second pricing path,
// and the distinction is the whole design:
//
//   - The candidate quantities come from the same pricing_rules predicate
//     GetMatchingRules uses, restricted to QUANTITY_BREAK rows.
//   - The price at each candidate comes from CalculateScaled, the very
//     engine that will price the line when the order is placed.
//   - A rung is only returned when its price is strictly better than the
//     single-unit price. So a break that some higher-priority promotional or
//     contract rule masks is silently dropped rather than advertised as a
//     saving the customer will not receive.
//
// The consequence worth stating plainly: this endpoint cannot promise a price
// the engine would not honour, because it asks the engine. If the waterfall
// changes, the ladder changes with it.
//
// jobID is deliberately not a parameter. A portal caller browsing a catalog is
// not on a job override, and accepting one here would let a consumer probe for
// another customer's job pricing.
func (s *Service) VolumeBreaks(ctx context.Context, cust *customer.Customer, productID uuid.UUID, basePrice float64) ([]VolumeBreak, error) {
	if cust == nil {
		return []VolumeBreak{}, nil
	}

	quantities, err := s.repo.ListBreakQuantities(ctx, productID, &cust.ID)
	if err != nil {
		return nil, err
	}
	if len(quantities) == 0 {
		return []VolumeBreak{}, nil
	}

	// The single-unit price is the baseline every rung has to beat. It is the
	// same number the catalog already shows as customer_price.
	unit, err := s.resolveLine(ctx, cust.ID, productID, OneQuantity, "")
	if err != nil {
		return nil, err
	}

	sort.Slice(quantities, func(i, j int) bool { return quantities[i] < quantities[j] })

	// The comparisons run per stocking unit (ADR 0006 section 5.3): two
	// answers may sit in different units, and only the exact rational per
	// stocking unit compares them; the winner keeps its own value and unit.
	unitPerStock, err := s.perStock(ctx, productID, unit.UnitPrice, unit.PriceUOM)
	if err != nil {
		return nil, err
	}
	breaks := make([]VolumeBreak, 0, len(quantities))
	prevPerStock := new(big.Rat).Set(unitPerStock)
	prevPrice := unit.UnitPrice
	for _, qty := range quantities {
		if qty <= 1 {
			continue
		}
		cp, err := s.resolveLine(ctx, cust.ID, productID, qty, "")
		if err != nil {
			return nil, err
		}
		cpPerStock, err := s.perStock(ctx, productID, cp.UnitPrice, cp.PriceUOM)
		if err != nil {
			return nil, err
		}
		// Strictly better than the single-unit price, and better than the rung
		// before it — a ladder that repeats or reverses is noise on a screen.
		// Each side of the comparison is the engine's own answer, itself the
		// one scale 4 rounding of the exact price, so the ladder advertises
		// exactly what the engine bills.
		if cpPerStock.Cmp(unitPerStock) >= 0 || cpPerStock.Cmp(prevPerStock) >= 0 {
			continue
		}
		saves := new(big.Rat).Sub(unitPerStock, cpPerStock)
		savesCents := new(big.Int).Quo(new(big.Int).Add(new(big.Int).Mul(saves.Num(), big.NewInt(10_000)), new(big.Int).Div(saves.Denom(), big.NewInt(2))), saves.Denom())
		breaks = append(breaks, VolumeBreak{
			MinQuantity:  qtyFloat(qty),
			UnitPrice:    priceFloat(cp.UnitPrice),
			Source:       cp.PriceBasis,
			Details:      cp.Details,
			SavesPerUnit: priceFloatOfRatCents(savesCents),
		})
		prevPerStock = cpPerStock
		prevPrice = cp.UnitPrice
	}
	_ = prevPrice

	return breaks, nil
}

// resolveLine is VolumeBreaks' view of one Resolve answer.
func (s *Service) resolveLine(ctx context.Context, customerID, productID uuid.UUID, qty httpx.Quantity, uom string) (Resolved, error) {
	if s.resolveRepo == nil {
		return Resolved{}, fmt.Errorf("the pricing engine has no resolve repository wired")
	}
	return s.Resolve(ctx, Request{CustomerID: customerID, ProductID: productID, Quantity: qty, UOM: uom})
}

// perStock converts a price in uom into an exact rational per the product's
// stocking unit, for comparisons only (R3): the winner keeps its own value
// and unit.
func (s *Service) perStock(ctx context.Context, productID uuid.UUID, price httpx.Price, uom string) (*big.Rat, error) {
	facts, err := s.resolveRepo.ProductPricingFacts(ctx, productID)
	if err != nil {
		return nil, err
	}
	p := ratOfPrice(price)
	if uom == "" || uom == facts.StockUOM {
		return p, nil
	}
	row, err := s.resolveRepo.UnitSetRow(ctx, productID, uom)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("the unit %s is not a row of the product's unit set", uom)
	}
	// One unit is stock_qty / unit_qty stocking units, so a price per unit
	// times that ratio is a price per stocking unit.
	return p.Mul(p, new(big.Rat).SetFrac64(int64(row.StockQty), int64(row.UnitQty))), nil
}

// priceFloat renders a scale 4 price as the float the result shape carries.
func priceFloat(p httpx.Price) float64 { return float64(p) / 10_000 }

// priceFloatOfRatCents renders an exact rational of cents as a float.
func priceFloatOfRatCents(c *big.Int) float64 {
	f, _ := new(big.Rat).SetFrac(c, big.NewInt(10_000)).Float64()
	return f
}

// qtyFloat renders a quantity as the float the result shape carries; the
// value is exact at scale 4.
func qtyFloat(q httpx.Quantity) float64 { return float64(q) / 10_000 }
