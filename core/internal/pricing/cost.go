// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/units"
	"github.com/google/uuid"
)

// ResolvedCost is ResolveCost's answer: the vendor's cost for one purchase
// line, its unit, the line's conversion pair for the line's unit and the
// cost's unit, the level the cost answered at, and the row that answered.
type ResolvedCost struct {
	UnitCost       httpx.Price
	PurchaseUOM    string
	UOMQty         httpx.Quantity
	PurchaseUOMQty httpx.Quantity
	LevelCode      string
	LevelID        uuid.UUID
	CostID         uuid.UUID
}

// ResolveCost answers the vendor's current cost for one purchase line (ADR
// 0006 section 5.5): rows at the requested level, or the vendor's default
// level when none is named, in effect on the date, the branch's row before
// the all branch row, the latest effective_from first; when the requested
// level has none, the default level's. lineUOM is the purchase line's unit,
// which the answer's pair names; an empty lineUOM takes the cost's own unit,
// the pair one and one. No cost (nil, nil) sends the purchase line to a
// manual cost. It never rounds.
func (s *Service) ResolveCost(ctx context.Context, vendorID, productID uuid.UUID, branch, level *uuid.UUID, on time.Time, lineUOM string) (*ResolvedCost, error) {
	if on.IsZero() {
		on = time.Now()
	}
	cost, err := s.resolveRepo.VendorCost(ctx, vendorID, productID, branch, level, on)
	if err != nil {
		return nil, err
	}
	if cost == nil {
		return nil, nil
	}

	pair := units.Pair{A: httpx.Quantity(10_000), B: httpx.Quantity(10_000)}
	if lineUOM != "" && lineUOM != cost.PurchaseUOM {
		lRow, err := s.resolveRepo.UnitSetRow(ctx, productID, lineUOM)
		if err != nil {
			return nil, err
		}
		pRow, err := s.resolveRepo.UnitSetRow(ctx, productID, cost.PurchaseUOM)
		if err != nil {
			return nil, err
		}
		if lRow == nil {
			return nil, fmt.Errorf("the unit %s is not a row of the product's unit set", lineUOM)
		}
		if pRow == nil {
			return nil, fmt.Errorf("the purchase unit %s is not a row of the product's unit set", cost.PurchaseUOM)
		}
		pair, err = units.ResolveLinePair(lRow.PairOf(), pRow.PairOf())
		if err != nil {
			return nil, err
		}
	}

	return &ResolvedCost{
		UnitCost:       cost.UnitCost,
		PurchaseUOM:    cost.PurchaseUOM,
		UOMQty:         pair.A,
		PurchaseUOMQty: pair.B,
		LevelCode:      cost.VendorLevelCode,
		LevelID:        cost.VendorLevelID,
		CostID:         cost.ID,
	}, nil
}
