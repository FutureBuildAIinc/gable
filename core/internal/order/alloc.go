// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"context"
	"fmt"
	"math/big"
	"sort"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// Inventory is the stock seam the order module depends on (ADR 0005 section
// 5.4): the scale 4 functions of the inventory service, each taking an
// explicit branch and running inside the caller's transaction.
type Inventory interface {
	// AvailableQty answers the product's unallocated stock at the branch and
	// takes its rows FOR UPDATE, so a plan made from it holds until commit.
	AvailableQty(ctx context.Context, productID, branchID uuid.UUID) (httpx.Quantity, error)
	AllocateQty(ctx context.Context, productID, branchID uuid.UUID, want httpx.Quantity) (httpx.Quantity, error)
	ReleaseQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error
	FulfillQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error
}

// Event types of the stock side of the lifecycle (ADR 0005 section 12).
const (
	EventBackordered       = "order.backordered"
	EventBackorderReleased = "order.backorder_released"
)

func (s *Service) WithInventory(inv Inventory) *Service { s.inventory = inv; return s }

// stockedIndexes lists the lines that move stock (a product line that names a
// product, and a kit component) in (product id, line id) order, so two orders
// never lock the same inventory rows in opposite orders.
func stockedIndexes(lines []OrderLine) []int {
	var idx []int
	for i := range lines {
		if lines[i].IsStocked() && lines[i].Quantity != nil {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		la, lb := &lines[idx[a]], &lines[idx[b]]
		if la.ProductID.String() != lb.ProductID.String() {
			return la.ProductID.String() < lb.ProductID.String()
		}
		return la.ID.String() < lb.ID.String()
	})
	return idx
}

// unallocated is the quantity a stocked line still needs stock for: what was
// ordered less what is allocated and what shipped.
func unallocated(l *OrderLine) int64 {
	n := int64(*l.Quantity) - int64(l.QuantityAllocated) - int64(l.QuantityFulfilled)
	if n < 0 {
		return 0
	}
	return n
}

// allocateLines allocates every stocked line of the order from the branch's
// inventory (ADR 0005 5.4): min(available, needed) for a plain line, whole
// kits for a kit's components, in (product id, line id) order, recording the
// rest as quantity_backordered. It writes the line quantities and answers
// whether anything is left on back order. The lines' fulfilled quantities and
// closed remainders are never touched.
func (s *Service) allocateLines(ctx context.Context, cur *Order) (backordered bool, err error) {
	if s.inventory == nil {
		return false, nil
	}
	order := stockedIndexes(cur.Lines)
	if len(order) == 0 {
		return false, nil
	}

	// Take every product's rows first, in product id order, and read what is
	// available: the plan below is then made against rows this transaction holds.
	avail := map[uuid.UUID]int64{}
	var products []uuid.UUID
	for _, i := range order {
		p := *cur.Lines[i].ProductID
		if _, seen := avail[p]; !seen {
			a, err := s.inventory.AvailableQty(ctx, p, cur.BranchID)
			if err != nil {
				return false, err
			}
			avail[p] = int64(a)
			products = append(products, p)
		}
	}

	// Kits: the components of one kit line allocate together, in whole kits.
	kitOf := map[uuid.UUID][]int{} // kit line id -> component line indexes
	kitIdx := map[uuid.UUID]int{}
	for i := range cur.Lines {
		l := &cur.Lines[i]
		if l.LineType == salesdoc.LineKit {
			kitIdx[l.ID] = i
		}
		if l.LineType == salesdoc.LineComponent && l.ParentLineID != nil {
			kitOf[*l.ParentLineID] = append(kitOf[*l.ParentLineID], i)
		}
	}
	take := map[int]int64{} // line index -> quantity to allocate
	kitDone := map[uuid.UUID]bool{}
	for _, i := range order {
		l := &cur.Lines[i]
		if l.LineType == salesdoc.LineComponent && l.ParentLineID != nil {
			kitID := *l.ParentLineID
			if kitDone[kitID] {
				continue
			}
			kitDone[kitID] = true
			planKit(cur.Lines, kitIdx[kitID], kitOf[kitID], avail, take)
			continue
		}
		need := unallocated(l)
		t := need
		if a := avail[*l.ProductID]; a < t {
			t = a
		}
		if t > 0 {
			take[i] = t
			avail[*l.ProductID] -= t
		}
	}

	// Allocate for real, in the same lock order, and record the line quantities.
	for _, i := range order {
		l := &cur.Lines[i]
		if t := take[i]; t > 0 {
			got, err := s.inventory.AllocateQty(ctx, *l.ProductID, cur.BranchID, httpx.Quantity(t))
			if err != nil {
				return false, err
			}
			if int64(got) != t {
				return false, fmt.Errorf("inventory allocated %d of the %d planned for line %s: the rows were not held", got, t, l.ID)
			}
			l.QuantityAllocated += got
		}
	}
	for _, i := range order {
		l := &cur.Lines[i]
		back := int64(*l.Quantity) - int64(l.QuantityAllocated) - int64(l.QuantityFulfilled)
		if back < 0 {
			back = 0
		}
		l.QuantityBackordered = httpx.Quantity(back)
		if back > 0 {
			backordered = true
		}
	}
	if err := s.repo.SaveLineQuantities(ctx, cur.Lines); err != nil {
		return false, err
	}
	return backordered, nil
}

// planKit plans the whole kits a kit's components can all be allocated for:
// n = the most whole kits that are still unallocated and that every
// component's available stock covers, each component taking n times its
// per kit quantity (the component line's quantity over the kit line's).
func planKit(lines []OrderLine, kitAt int, comps []int, avail map[uuid.UUID]int64, take map[int]int64) {
	kit := &lines[kitAt]
	if kit.Quantity == nil || *kit.Quantity <= 0 {
		return
	}
	k4 := big.NewInt(int64(*kit.Quantity))
	var best *big.Int // the kits supported so far
	for _, ci := range comps {
		c := &lines[ci]
		c4 := big.NewInt(int64(*c.Quantity))
		if c4.Sign() <= 0 {
			return
		}
		// per kit quantity p = c4 / k4 (the component's real units per kit);
		// kits already covered = (allocated + fulfilled) / p; kits still
		// needed = K - covered; kits the stock covers = avail / p.
		have := big.NewInt(int64(c.QuantityAllocated) + int64(c.QuantityFulfilled))
		// kits remaining, as a rational in real kits: K4/10000 - have*k4/(c4*10000)
		remaining := new(big.Rat).SetFrac(new(big.Int).Sub(new(big.Int).Mul(k4, c4), new(big.Int).Mul(have, k4)), new(big.Int).Mul(c4, big.NewInt(10000)))
		supported := new(big.Rat).SetFrac(new(big.Int).Mul(big.NewInt(avail[*c.ProductID]), k4), new(big.Int).Mul(c4, big.NewInt(10000)))
		n := floorRat(minRat(remaining, supported))
		if best == nil || n.Cmp(best) < 0 {
			best = n
		}
	}
	if best == nil || best.Sign() <= 0 {
		return
	}
	for _, ci := range comps {
		c := &lines[ci]
		// n kits take n * c4 / K (real) of the component: n*c4*10000/k4 in scale 4
		num := new(big.Int).Mul(new(big.Int).Mul(best, big.NewInt(int64(*c.Quantity))), big.NewInt(10000))
		t := new(big.Int).Div(num, k4).Int64()
		if t > 0 {
			take[ci] = t
			avail[*c.ProductID] -= t
		}
	}
}

func minRat(a, b *big.Rat) *big.Rat {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}

func floorRat(r *big.Rat) *big.Int {
	if r.Sign() <= 0 {
		return new(big.Int)
	}
	return new(big.Int).Div(r.Num(), r.Denom())
}

// releaseAllocations gives back every allocated unit of the order and zeroes
// its back orders (a cancel, a reopen, a close short). Fulfilled quantities
// stay.
func (s *Service) releaseAllocations(ctx context.Context, cur *Order) error {
	if s.inventory == nil {
		return nil
	}
	for _, i := range stockedIndexes(cur.Lines) {
		l := &cur.Lines[i]
		if l.QuantityAllocated > 0 {
			if err := s.inventory.ReleaseQty(ctx, *l.ProductID, cur.BranchID, l.QuantityAllocated); err != nil {
				return err
			}
		}
		l.QuantityAllocated, l.QuantityBackordered = 0, 0
	}
	return s.repo.SaveLineQuantities(ctx, cur.Lines)
}

// deriveStatus is the status a confirmed order's line quantities say (ADR
// 0005 5.2): every non text line fully fulfilled or closed short, FULFILLED;
// otherwise any back ordered line, BACKORDERED; otherwise CONFIRMED.
func deriveStatus(cur *Order) OrderStatus {
	done, backordered, any := true, false, false
	for i := range cur.Lines {
		l := &cur.Lines[i]
		if l.LineType == salesdoc.LineText || l.LineType == salesdoc.LineKit || l.Quantity == nil {
			continue
		}
		any = true
		if l.IsStocked() {
			if l.QuantityAllocated > 0 || l.QuantityBackordered > 0 {
				done = false
			}
			if l.QuantityBackordered > 0 {
				backordered = true
			}
			continue
		}
		if l.QuantityFulfilled < *l.Quantity {
			done = false
		}
	}
	// A kit is done when its components are; a kit with no stocked component
	// left (all fulfilled) needs its own fulfilled quantity to reach its quantity.
	for i := range cur.Lines {
		l := &cur.Lines[i]
		if l.LineType == salesdoc.LineKit && l.Quantity != nil && l.QuantityFulfilled < *l.Quantity {
			done = false
		}
	}
	switch {
	case any && done:
		return StatusFulfilled
	case backordered:
		return StatusBackordered
	default:
		return StatusConfirmed
	}
}

// neverAllocated reports whether a confirmed order has not yet been through
// allocation: no line holds, owes or shipped anything.
func neverAllocated(cur *Order) bool {
	for i := range cur.Lines {
		l := &cur.Lines[i]
		if l.QuantityAllocated != 0 || l.QuantityBackordered != 0 || l.QuantityFulfilled != 0 {
			return false
		}
	}
	return true
}

// AllocateOrder runs the allocation for one order on demand (ADR 0005 5.4,
// POST /orders/{id}/allocate): the same transaction shape as the worker's
// serving of a request, with the revision precondition. It never touches the
// queue; a request left behind finds nothing to allocate and the worker
// deletes it. Event order.backorder_released when it clears the back order.
func (s *Service) AllocateOrder(ctx context.Context, id uuid.UUID, pre Precondition, actor string) (*Order, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Order
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockOrder(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetOrder(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkOrderBranch(ctx, cur); err != nil {
			return err
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		if cur.Status != StatusBackordered && cur.Status != StatusConfirmed {
			return conflictBlocker("order_not_allocatable", "only a confirmed or back ordered order can be allocated")
		}
		out, _, err = s.allocateAndDerive(ctx, cur, actor)
		return err
	})
	return out, err
}

// allocateAndDerive allocates what is on back order, derives the status, writes
// the order and its event when the status moved (backordered to confirmed:
// order.backorder_released). It answers whether anything changed. The caller
// holds the order row lock inside its transaction.
func (s *Service) allocateAndDerive(ctx context.Context, cur *Order, actor string) (*Order, bool, error) {
	before := make(map[uuid.UUID]httpx.Quantity, len(cur.Lines))
	for i := range cur.Lines {
		before[cur.Lines[i].ID] = cur.Lines[i].QuantityAllocated
	}
	if _, err := s.allocateLines(ctx, cur); err != nil {
		return nil, false, err
	}
	changed := false
	for i := range cur.Lines {
		if before[cur.Lines[i].ID] != cur.Lines[i].QuantityAllocated {
			changed = true
		}
	}
	from := cur.Status
	next := deriveStatus(cur)
	if next == StatusFulfilled {
		next = from // allocation never completes an order
	}
	if !changed && next == from {
		return cur, false, nil
	}
	cur.Status = next
	if err := s.repo.SaveTransition(ctx, cur); err != nil {
		return nil, false, err
	}
	if from == StatusBackordered && next == StatusConfirmed {
		if err := s.record(ctx, cur, EventBackorderReleased, from.Status()); err != nil {
			return nil, false, err
		}
	}
	out, err := s.repo.GetOrder(ctx, cur.ID)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// hasBackorders reports whether any line still owes stock.
func hasBackorders(cur *Order) bool {
	for i := range cur.Lines {
		if cur.Lines[i].QuantityBackordered > 0 {
			return true
		}
	}
	return false
}
