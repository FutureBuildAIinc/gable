// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"sort"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

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
	// would pay for a single unit, per unit. It is always > 0 — a rung that
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
//   - The price at each candidate comes from CalculateScaled — the very
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
	unit, err := s.CalculateScaled(ctx, cust, productID, basePrice, 1, nil)
	if err != nil {
		return nil, err
	}

	sort.Slice(quantities, func(i, j int) bool { return quantities[i] < quantities[j] })

	breaks := make([]VolumeBreak, 0, len(quantities))
	prevPrice := unit.Price
	for _, qty := range quantities {
		if qty <= 1 {
			continue
		}
		cp, err := s.CalculateScaled(ctx, cust, productID, basePrice, qtyFloat(qty), nil)
		if err != nil {
			return nil, err
		}
		// Strictly better than the single-unit price, and better than the rung
		// before it — a ladder that repeats or reverses is noise on a screen.
		// Each side of the comparison is the engine's own answer, itself the
		// one scale 4 rounding of the exact price, so the ladder advertises
		// exactly what the engine bills.
		if cp.Price >= unit.Price || cp.Price >= prevPrice {
			continue
		}
		saves := unit.Price - cp.Price
		breaks = append(breaks, VolumeBreak{
			MinQuantity:  qtyFloat(qty),
			UnitPrice:    cp.Float64(),
			Source:       cp.Source,
			Details:      cp.Details,
			SavesPerUnit: float64(saves) / 10_000,
		})
		prevPrice = cp.Price
	}

	return breaks, nil
}

// qtyFloat renders a quantity as the float the result shape carries; the
// value is exact at scale 4.
func qtyFloat(q httpx.Quantity) float64 { return float64(q) / 10_000 }
