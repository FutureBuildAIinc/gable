// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"math"
	"sort"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/google/uuid"
)

// VolumeBreak is one rung of the "buy N and the unit price drops" ladder for a
// (customer, product) pair.
//
// Money is float64 DOLLARS, matching the rest of this package and the
// CalculatedPrice it is derived from. Quantity is a physical quantity, not
// money.
type VolumeBreak struct {
	MinQuantity float64       `json:"min_quantity"`
	UnitPrice   float64       `json:"unit_price"`
	Source      PricingSource `json:"price_source"`
	Details     string        `json:"details"`

	// SavesPerUnit is UnitPrice's improvement over the price this customer
	// would pay for a single unit, per unit, in dollars. It is always > 0 —
	// a rung that does not actually beat the current unit price is not
	// returned at all (see VolumeBreaks).
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
//   - The price at each candidate comes from CalculatePriceWithQty — the very
//     function that will price the line when the order is placed.
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

	custID := &cust.ID
	quantities, err := s.repo.ListBreakQuantities(ctx, productID, custID)
	if err != nil {
		return nil, err
	}
	if len(quantities) == 0 {
		return []VolumeBreak{}, nil
	}

	// The single-unit price is the baseline every rung has to beat. It is the
	// same number the catalog already shows as customer_price.
	unit, err := s.CalculatePriceWithQty(ctx, cust, productID, basePrice, 1, nil)
	if err != nil {
		return nil, err
	}

	sort.Float64s(quantities)

	breaks := make([]VolumeBreak, 0, len(quantities))
	prevPrice := unit.FinalPrice
	for _, qty := range quantities {
		if qty <= 1 {
			continue
		}
		cp, err := s.CalculatePriceWithQty(ctx, cust, productID, basePrice, qty, nil)
		if err != nil {
			return nil, err
		}
		// Strictly better than the single-unit price, and better than the rung
		// before it — a ladder that repeats or reverses is noise on a screen.
		if cp.FinalPrice >= unit.FinalPrice || cp.FinalPrice >= prevPrice {
			continue
		}
		breaks = append(breaks, VolumeBreak{
			MinQuantity:  qty,
			UnitPrice:    cp.FinalPrice,
			Source:       cp.Source,
			Details:      cp.Details,
			SavesPerUnit: math.Round((unit.FinalPrice-cp.FinalPrice)*100) / 100,
		})
		prevPrice = cp.FinalPrice
	}

	return breaks, nil
}
