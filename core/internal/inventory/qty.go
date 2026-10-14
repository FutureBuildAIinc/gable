// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory

import (
	"context"
	"errors"
	"fmt"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// The scale 4 quantity functions of ADR 0005 section 5.4: AllocateQty,
// ReleaseQty, FulfillQty and RestockQty take an httpx.Quantity and an explicit
// branch (never the context's), read and write through the caller's
// transaction, and take the product's rows at the branch FOR UPDATE in
// inventory id order (section 11, step 6). The float64 versions above stay
// for the callers cycle 4 converts.

// ErrInsufficientAvailable and ErrInsufficientAllocated are the sentinels a
// caller maps: an allocation that found less available than the guard
// allowed, and a release or fulfilment that asked for more than is allocated.
var (
	ErrInsufficientAvailable = errors.New("inventory: insufficient available stock")
	ErrInsufficientAllocated = errors.New("inventory: insufficient allocated stock")
)

// AvailableQty is the product's unallocated stock at the branch. It takes the
// product's rows FOR UPDATE (section 11, step 6), so a plan made from the
// answer holds until the transaction ends.
func (s *Service) AvailableQty(ctx context.Context, productID, branchID uuid.UUID) (httpx.Quantity, error) {
	items, err := s.repo.LockBranchInventory(ctx, productID, branchID)
	if err != nil {
		return 0, err
	}
	var total int64
	for i := range items {
		if a := scaled(items[i].Quantity) - scaled(items[i].Allocated); a > 0 {
			total += a
		}
	}
	return httpx.Quantity(total), nil
}

// AllocateQty reserves up to want of the product at the branch, fullest row
// first, and answers how much it reserved: min(available, want), never an
// error for a short stock. It never reserves a negative or zero quantity.
func (s *Service) AllocateQty(ctx context.Context, productID, branchID uuid.UUID, want httpx.Quantity) (httpx.Quantity, error) {
	if want <= 0 {
		return 0, nil
	}
	items, err := s.repo.LockBranchInventory(ctx, productID, branchID)
	if err != nil {
		return 0, err
	}
	type slot struct {
		id    uuid.UUID
		avail int64
	}
	var slots []slot
	for i := range items {
		if a := scaled(items[i].Quantity) - scaled(items[i].Allocated); a > 0 {
			slots = append(slots, slot{items[i].ID, a})
		}
	}
	// fullest first, ties by id (the order the rows were locked in)
	for i := 1; i < len(slots); i++ {
		for j := i; j > 0 && slots[j].avail > slots[j-1].avail; j-- {
			slots[j], slots[j-1] = slots[j-1], slots[j]
		}
	}
	remaining := int64(want)
	var got int64
	for _, sl := range slots {
		if remaining <= 0 {
			break
		}
		take := remaining
		if sl.avail < take {
			take = sl.avail
		}
		if err := s.repo.AllocateStockQty(ctx, sl.id, take); err != nil {
			return 0, err
		}
		remaining -= take
		got += take
	}
	return httpx.Quantity(got), nil
}

// ReleaseQty gives back qty of allocated stock at the branch, most allocated
// row first; asking for more than is allocated is ErrInsufficientAllocated and
// moves nothing.
func (s *Service) ReleaseQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error {
	if qty <= 0 {
		return nil
	}
	items, err := s.repo.LockBranchInventory(ctx, productID, branchID)
	if err != nil {
		return err
	}
	var total int64
	for i := range items {
		total += scaled(items[i].Allocated)
	}
	if total < int64(qty) {
		return fmt.Errorf("%w: product %s holds %d, release asked for %d", ErrInsufficientAllocated, productID, total, qty)
	}
	alloc := make([]int64, len(items))
	for i := range items {
		alloc[i] = scaled(items[i].Allocated)
	}
	remaining := int64(qty)
	for remaining > 0 {
		best := -1
		for i := range alloc {
			if alloc[i] > 0 && (best < 0 || alloc[i] > alloc[best]) {
				best = i
			}
		}
		take := alloc[best]
		if take > remaining {
			take = remaining
		}
		if err := s.repo.DeallocateStockQty(ctx, items[best].ID, take); err != nil {
			return err
		}
		alloc[best] -= take
		remaining -= take
	}
	return nil
}

// FulfillQty ships qty of allocated stock at the branch: on hand and allocated
// both fall. Asking for more than is allocated is ErrInsufficientAllocated and
// moves nothing.
func (s *Service) FulfillQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error {
	if qty <= 0 {
		return nil
	}
	items, err := s.repo.LockBranchInventory(ctx, productID, branchID)
	if err != nil {
		return err
	}
	var total int64
	for i := range items {
		a, q := scaled(items[i].Allocated), scaled(items[i].Quantity)
		if q < a {
			a = q
		}
		total += a
	}
	if total < int64(qty) {
		return fmt.Errorf("%w: product %s holds %d, fulfilment asked for %d", ErrInsufficientAllocated, productID, total, qty)
	}
	remaining := int64(qty)
	for i := range items {
		if remaining <= 0 {
			break
		}
		a, q := scaled(items[i].Allocated), scaled(items[i].Quantity)
		if q < a {
			a = q
		}
		if a <= 0 {
			continue
		}
		take := remaining
		if a < take {
			take = a
		}
		if err := s.repo.FulfillStockQty(ctx, items[i].ID, take); err != nil {
			return err
		}
		remaining -= take
	}
	return nil
}

// RestockQty returns qty to on hand at the branch (an invoice void, a restock
// credit memo): into the row holding the most stock, else the lowest id. A
// product with no row at the branch is an error. Allocation is a separate act.
func (s *Service) RestockQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error {
	if qty <= 0 {
		return nil
	}
	items, err := s.repo.LockBranchInventory(ctx, productID, branchID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return fmt.Errorf("no inventory row for product %s at branch %s", productID, branchID)
	}
	best := 0
	for i := range items {
		if scaled(items[i].Quantity) > scaled(items[best].Quantity) {
			best = i
		}
	}
	return s.repo.RestockQty(ctx, items[best].ID, int64(qty))
}

// scaled reads a NUMERIC(.,4) column's float64 as its scale 4 integer. The
// float64 only carries the column's own four digits, so rounding recovers the
// exact integer.
func scaled(f float64) int64 {
	if f < 0 {
		return -int64(-f*10000 + 0.5)
	}
	return int64(f*10000 + 0.5)
}

// ErrInsufficientOnHand is the counter's name for a short free stock: a
// sale at the till that asks for more than the branch has free.
var ErrInsufficientOnHand = ErrInsufficientAvailable

// IssueQty takes qty of the product from on hand at the branch for a
// counter sale: a counter sale never allocates (there is no confirm moment
// between the goods leaving the shelf and the money), so it takes from the
// free stock, rows with the most free first, and refuses below what is
// allocated with ErrInsufficientOnHand.
func (s *Service) IssueQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error {
	if qty <= 0 {
		return nil
	}
	items, err := s.repo.LockBranchInventory(ctx, productID, branchID)
	if err != nil {
		return err
	}
	type slot struct {
		id   uuid.UUID
		free int64
	}
	var slots []slot
	var total int64
	for i := range items {
		if f := scaled(items[i].Quantity) - scaled(items[i].Allocated); f > 0 {
			slots = append(slots, slot{items[i].ID, f})
			total += f
		}
	}
	if total < int64(qty) {
		return fmt.Errorf("%w: product %s has %d free, the sale takes %d", ErrInsufficientOnHand, productID, total, qty)
	}
	remaining := int64(qty)
	for remaining > 0 {
		best := 0
		for i := range slots {
			if slots[i].free > slots[best].free {
				best = i
			}
		}
		take := remaining
		if slots[best].free < take {
			take = slots[best].free
		}
		if err := s.repo.UnstockQty(ctx, slots[best].id, take); err != nil {
			return err
		}
		slots[best].free -= take
		remaining -= take
	}
	return nil
}

// UnrestockQty takes qty back off on hand at the branch, the reverse of
// RestockQty (a voided restocking credit memo). It takes from the rows
// holding the most unallocated stock first and never below what is
// allocated: a quantity the branch no longer has free is
// ErrInsufficientAvailable and moves nothing.
func (s *Service) UnrestockQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error {
	if qty <= 0 {
		return nil
	}
	items, err := s.repo.LockBranchInventory(ctx, productID, branchID)
	if err != nil {
		return err
	}
	type slot struct {
		id   uuid.UUID
		free int64
	}
	var slots []slot
	var total int64
	for i := range items {
		if f := scaled(items[i].Quantity) - scaled(items[i].Allocated); f > 0 {
			slots = append(slots, slot{items[i].ID, f})
			total += f
		}
	}
	if total < int64(qty) {
		return fmt.Errorf("%w: product %s has %d free, the void takes back %d", ErrInsufficientAvailable, productID, total, qty)
	}
	remaining := int64(qty)
	for remaining > 0 {
		best := 0
		for i := range slots {
			if slots[i].free > slots[best].free {
				best = i
			}
		}
		take := remaining
		if slots[best].free < take {
			take = slots[best].free
		}
		if err := s.repo.UnstockQty(ctx, slots[best].id, take); err != nil {
			return err
		}
		slots[best].free -= take
		remaining -= take
	}
	return nil
}
