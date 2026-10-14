// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// ErrNothingAllocated is returned by Release when the product has no allocated
// stock to give back. It is a sentinel because a caller unwinding a workflow —
// cancelling an order, for instance — must be able to tell "there was nothing
// to release" apart from a real inventory failure. The first is a bookkeeping
// mismatch it should survive; the second is not.
var ErrNothingAllocated = errors.New("inventory: no allocated stock")

// The stock level events of ADR 0008 section 10.2: edge triggered, written
// by the stock-levels job when a product and branch crosses its reorder
// point, never by the money paths.
const (
	EventStockLow       = "stock.low"
	EventStockRecovered = "stock.recovered"
)

// EventRecorder writes a domain event into the transactional outbox (ADR
// 0003 section 2).
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

type Service struct {
	repo   Repository
	levels LevelStore // the levels read's store (C3-1b), when the repo can serve it
	stock  StockWriter
	events EventRecorder
}

func NewService(repo Repository) *Service {
	s := &Service{repo: repo}
	// The levels read's store, the pattern product's KitStore set: beside the
	// Repository interface so the module's test fakes and the portal's keep
	// compiling; the real repository always serves it.
	if ls, ok := any(repo).(LevelStore); ok {
		s.levels = ls
	}
	if sw, ok := any(repo).(StockWriter); ok {
		s.stock = sw
	}
	return s
}

// WithOutbox wires the outbox the stock level job writes its edge triggered
// events to, and the adjust and transfer routes theirs. Optional: nil writes
// no event (unit tests).
func (s *Service) WithOutbox(events EventRecorder) *Service {
	s.events = events
	return s
}

// AdjustStock handles receipt (Add) or cycle count (Set/Adjust)
func (s *Service) AdjustStock(ctx context.Context, req StockAdjustmentRequest) error {
	// 1. Check if inventory record exists
	inv, err := s.repo.GetInventory(ctx, req.ProductID, req.LocationID)
	if err != nil {
		return err
	}

	if inv == nil {
		// Create new record. On a non-existent record a delta starts from base 0.
		newQty := req.Quantity
		if newQty < 0 {
			return fmt.Errorf("adjustment would create negative stock for product %s (resulting quantity %g)", req.ProductID, newQty)
		}

		inv = &Inventory{
			ProductID:  req.ProductID,
			LocationID: req.LocationID,
			Quantity:   newQty,
			Location:   "", // Legacy field empty
		}
		return s.repo.CreateInventory(ctx, inv)
	}

	// Update existing
	if req.IsDelta {
		inv.Quantity += req.Quantity
	} else {
		inv.Quantity = req.Quantity
	}

	// Floor on-hand stock at zero. A negative result means an out-adjustment (or
	// over-large move) exceeded what is physically on hand — reject it rather
	// than persist negative inventory, which corrupts availability math
	// downstream (Allocate/Fulfill) and the double-entry move total.
	if inv.Quantity < 0 {
		return fmt.Errorf("adjustment would drive stock negative for product %s (resulting quantity %g)", req.ProductID, inv.Quantity)
	}

	return s.repo.UpdateInventory(ctx, inv)
}

func (s *Service) MoveStock(ctx context.Context, req StockMovementRequest) error {
	// Cross-branch moves are not supported. Require both endpoints to share
	// a branch_id (looked up from the locations table). A nil source location
	// means "unassigned/legacy" which we treat as branch-unknown and reject
	// unless the destination is also unassigned.
	var fromBranch, toBranch *uuid.UUID
	var err error
	if req.FromLocationID != nil {
		fromBranch, err = s.repo.LocationBranchID(ctx, *req.FromLocationID)
		if err != nil {
			return fmt.Errorf("failed to resolve source branch: %w", err)
		}
	}
	toBranch, err = s.repo.LocationBranchID(ctx, req.ToLocationID)
	if err != nil {
		return fmt.Errorf("failed to resolve destination branch: %w", err)
	}
	if fromBranch != nil && toBranch != nil && *fromBranch != *toBranch {
		return fmt.Errorf("cross-branch stock moves are not allowed: source=%s destination=%s", fromBranch, toBranch)
	}

	if req.Quantity <= 0 {
		return fmt.Errorf("move quantity must be positive")
	}

	// Only unallocated (available) stock may be relocated. Moving reserved stock
	// would strand the allocation at the source (available goes negative) while
	// the moved units arrive unreserved at the destination and can be re-sold —
	// double-promising the same physical units.
	if req.FromLocationID != nil {
		src, err := s.repo.GetInventory(ctx, req.ProductID, req.FromLocationID)
		if err != nil {
			return fmt.Errorf("failed to read source inventory: %w", err)
		}
		if src == nil {
			return fmt.Errorf("no inventory at source location for product %s", req.ProductID)
		}
		if avail := src.Quantity - src.Allocated; req.Quantity > avail {
			return fmt.Errorf("cannot move %g: only %g unallocated at source (reserved stock cannot be moved)", req.Quantity, avail)
		}
	}

	return s.repo.ExecuteInTx(ctx, func(ctx context.Context) error {
		// Subtract from source
		err := s.AdjustStock(ctx, StockAdjustmentRequest{
			ProductID:  req.ProductID,
			LocationID: req.FromLocationID,
			Quantity:   -req.Quantity,
			IsDelta:    true,
			Reason:     "Move Out: " + req.Reason,
		})
		if err != nil {
			return fmt.Errorf("failed to remove stock from source: %w", err)
		}

		// Add to dest
		err = s.AdjustStock(ctx, StockAdjustmentRequest{
			ProductID:  req.ProductID,
			LocationID: &req.ToLocationID,
			Quantity:   req.Quantity,
			IsDelta:    true,
			Reason:     "Move In: " + req.Reason,
		})
		if err != nil {
			return fmt.Errorf("failed to add stock to destination: %w", err)
		}

		return nil
	})
}

// Allocate reserves stock for a product, spanning multiple locations within the
// active branch when no single location has enough free stock (mirroring how
// Fulfill consumes across locations). A nil branch context means "all branches".
func (s *Service) Allocate(ctx context.Context, productID uuid.UUID, quantity float64) error {
	if quantity <= 0 {
		return fmt.Errorf("allocation quantity must be positive")
	}

	items, err := s.repo.ListInventoryByProductAndBranch(ctx, productID, branchctx.IDForQuery(ctx))
	if err != nil {
		return fmt.Errorf("failed to list inventory: %w", err)
	}

	if len(items) == 0 {
		return fmt.Errorf("no inventory found for product %s", productID)
	}

	// Reject up-front if total available across all locations is insufficient,
	// so we never leave a partial allocation on a non-transactional caller.
	var totalAvail float64
	for i := range items {
		if a := items[i].Quantity - items[i].Allocated; a > 0 {
			totalAvail += a
		}
	}
	if totalAvail < quantity {
		return fmt.Errorf("insufficient available stock for product %s: need %g, have %g across %d location(s)", productID, quantity, totalAvail, len(items))
	}

	// Allocate fullest-location-first until the quantity is satisfied. Each
	// AllocateStock is atomic with a `(quantity-allocated) >= delta` guard, so a
	// concurrent allocation that drains a row surfaces as an error and (within
	// the caller's transaction) rolls back the whole allocation.
	sort.SliceStable(items, func(a, b int) bool {
		return (items[a].Quantity - items[a].Allocated) > (items[b].Quantity - items[b].Allocated)
	})

	remaining := quantity
	for i := range items {
		if remaining <= 0 {
			break
		}
		avail := items[i].Quantity - items[i].Allocated
		if avail <= 0 {
			continue
		}
		take := remaining
		if avail < remaining {
			take = avail
		}
		if err := s.repo.AllocateStock(ctx, items[i].ID, take); err != nil {
			return fmt.Errorf("failed to allocate %g from inventory %s: %w", take, items[i].ID, err)
		}
		remaining -= take
	}

	if remaining > 0 {
		return fmt.Errorf("insufficient available stock for product %s (could not allocate remaining %g)", productID, remaining)
	}
	return nil
}

func (s *Service) Release(ctx context.Context, productID uuid.UUID, quantity float64) error {
	if quantity <= 0 {
		return fmt.Errorf("release quantity must be positive")
	}

	items, err := s.repo.ListInventoryByProductAndBranch(ctx, productID, branchctx.IDForQuery(ctx))
	if err != nil {
		return fmt.Errorf("failed to list inventory: %w", err)
	}

	if len(items) == 0 {
		return fmt.Errorf("no inventory found for product %s", productID)
	}

	// Pick the item with the most allocated stock
	var best *Inventory
	var maxAlloc float64 = -1

	for i := range items {
		if items[i].Allocated > maxAlloc {
			maxAlloc = items[i].Allocated
			best = &items[i]
		}
	}

	if best == nil || best.Allocated <= 0 {
		return fmt.Errorf("%w: product %s", ErrNothingAllocated, productID)
	}

	return s.repo.DeallocateStock(ctx, best.ID, quantity)
}

// ListByProduct is the in-process read the portal's availability sum uses
// (the unconverted float seam, the pattern of product's QtyFloat); the levels
// route reads ListLevelsPage (C3-1b).
func (s *Service) ListByProduct(ctx context.Context, productIDStr string) ([]Inventory, error) {
	id, err := uuid.Parse(productIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid product id: %w", err)
	}
	return s.repo.ListInventoryByProduct(ctx, id)
}

func (s *Service) Fulfill(ctx context.Context, productID uuid.UUID, quantity float64) error {
	if quantity <= 0 {
		return fmt.Errorf("quantity must be positive")
	}

	items, err := s.repo.ListInventoryByProductAndBranch(ctx, productID, branchctx.IDForQuery(ctx))
	if err != nil {
		return fmt.Errorf("failed to list inventory: %w", err)
	}

	remaining := quantity

	// Consume allocated stock
	for i := range items {
		if remaining <= 0 {
			break
		}

		// We prefer to take from where it was allocated.
		available := items[i].Allocated
		if available > 0 {
			take := remaining
			if available < remaining {
				take = available
			}

			if err := s.repo.FulfillStock(ctx, items[i].ID, take); err != nil {
				return createError(fmt.Errorf("failed to fulfill stock from inv %s: %w", items[i].ID, err))
			}
			remaining -= take
		}
	}

	if remaining > 0 {
		return fmt.Errorf("insufficient allocated stock to fulfill %f (remaining: %f)", quantity, remaining)
	}

	return nil
}

func (s *Service) RevertFulfillment(ctx context.Context, productID uuid.UUID, quantity float64) error {
	if quantity <= 0 {
		return fmt.Errorf("revert quantity must be positive")
	}

	items, err := s.repo.ListInventoryByProductAndBranch(ctx, productID, branchctx.IDForQuery(ctx))
	if err != nil {
		return fmt.Errorf("failed to list inventory: %w", err)
	}

	if len(items) == 0 {
		return fmt.Errorf("no inventory found for product %s", productID)
	}

	// Pick the first item to revert fulfillment into
	// In a more sophisticated system this would track which item was fulfilled from
	return s.repo.RevertFulfillStock(ctx, items[0].ID, quantity)
}

func createError(err error) error {
	// Helper to handle error wrapping
	return err
}

// ReceiveIntoLocation is the purchase order receive's stock write (C4-1a,
// until the stock ledger of C4-2 A): the row is created where absent and the
// update takes the row lock; the stock level goes dirty for the job.
func (s *Service) ReceiveIntoLocation(ctx context.Context, productID, locationID uuid.UUID, quantity httpx.Quantity) error {
	if s.stock == nil {
		return fmt.Errorf("inventory service has no stock writer")
	}
	return s.stock.ReceiveStockQty(ctx, productID, locationID, int64(quantity))
}

// RefreshStockLevels serves the stock level job's queue (ADR 0008 section
// 10.2): one dirty product and branch per transaction, available = the sum
// of quantity - allocated over the branch's rows, and the edge triggered
// events: crossing to available <= reorder_point with low_since null sets it
// and writes stock.low; crossing back above clears it and writes
// stock.recovered. A level that stays low writes no further events. It
// returns how many dirty rows it served.
func (s *Service) RefreshStockLevels(ctx context.Context, batch int) (int, error) {
	served := 0
	for i := 0; batch <= 0 || i < batch; i++ {
		done := false
		err := s.repo.ExecuteInTx(ctx, func(txCtx context.Context) error {
			productID, branchID, ok, err := s.levelsNext(txCtx)
			if err != nil {
				return err
			}
			if !ok {
				done = true
				return nil
			}
			availableText, err := s.levelsAvailable(txCtx, productID, branchID)
			if err != nil {
				return err
			}
			available, err := httpx.ParseQuantity(availableText)
			if err != nil {
				available = 0
			}
			level, err := s.levelsGet(txCtx, productID, branchID)
			if err != nil {
				return err
			}
			var point *httpx.Quantity
			if level != nil && level.ReorderPoint != nil {
				p := httpx.Quantity(int64(*level.ReorderPoint * 10000))
				point = &p
			}
			low := point != nil && available <= *point
			wasLow := level != nil && level.LowSince != nil
			if low && !wasLow {
				now := time.Now().UTC()
				if err := s.levelsUpsert(txCtx, productID, branchID, &now); err != nil {
					return err
				}
				if err := s.writeLevelEvent(txCtx, EventStockLow, productID, branchID, available, point); err != nil {
					return err
				}
			} else if !low && wasLow {
				if err := s.levelsUpsert(txCtx, productID, branchID, nil); err != nil {
					return err
				}
				if err := s.writeLevelEvent(txCtx, EventStockRecovered, productID, branchID, available, point); err != nil {
					return err
				}
			}
			return s.levelsClear(txCtx, productID, branchID)
		})
		if err != nil {
			return served, err
		}
		if done {
			break
		}
		served++
	}
	return served, nil
}

// levelStore is the stock level job's store, beside the Repository
// interface so the test fakes keep compiling.
type levelStore interface {
	NextDirtyStockLevel(ctx context.Context) (uuid.UUID, uuid.UUID, bool, error)
	SumAvailable(ctx context.Context, productID, branchID uuid.UUID) (string, error)
	GetStockLevel(ctx context.Context, productID, branchID uuid.UUID) (*StockLevel, error)
	UpsertStockLevel(ctx context.Context, productID, branchID uuid.UUID, lowSince *time.Time) error
	ClearDirtyStockLevel(ctx context.Context, productID, branchID uuid.UUID) error
	ProductStockingUnit(ctx context.Context, productID uuid.UUID) (string, error)
}

func (s *Service) levelsNext(ctx context.Context) (uuid.UUID, uuid.UUID, bool, error) {
	ls, ok := any(s.repo).(levelStore)
	if !ok {
		return uuid.Nil, uuid.Nil, false, fmt.Errorf("inventory service has no stock level store")
	}
	return ls.NextDirtyStockLevel(ctx)
}

func (s *Service) levelsAvailable(ctx context.Context, productID, branchID uuid.UUID) (string, error) {
	ls, _ := any(s.repo).(levelStore)
	return ls.SumAvailable(ctx, productID, branchID)
}

func (s *Service) levelsGet(ctx context.Context, productID, branchID uuid.UUID) (*StockLevel, error) {
	ls, _ := any(s.repo).(levelStore)
	return ls.GetStockLevel(ctx, productID, branchID)
}

func (s *Service) levelsUpsert(ctx context.Context, productID, branchID uuid.UUID, lowSince *time.Time) error {
	ls, _ := any(s.repo).(levelStore)
	return ls.UpsertStockLevel(ctx, productID, branchID, lowSince)
}

func (s *Service) levelsClear(ctx context.Context, productID, branchID uuid.UUID) error {
	ls, _ := any(s.repo).(levelStore)
	return ls.ClearDirtyStockLevel(ctx, productID, branchID)
}

func (s *Service) writeLevelEvent(ctx context.Context, eventType string, productID, branchID uuid.UUID, available httpx.Quantity, point *httpx.Quantity) error {
	if s.events == nil {
		return nil
	}
	ls, _ := any(s.repo).(levelStore)
	unit, _ := ls.ProductStockingUnit(ctx, productID)
	data := map[string]any{
		"product_id": productID, "branch_id": branchID,
		"available": available.WireString(), "unit": unit,
	}
	if point != nil {
		data["reorder_point"] = point.WireString()
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	branch := branchID
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: "product", EntityID: productID, BranchID: &branch, Data: raw,
	})
}
