// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"context"
	"fmt"
	"math/big"
	"sort"

	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// The order's half of an invoice void (ADR 0005 6.2), called by the invoice
// module inside the void's transaction.

// LockForInvoiceVoid takes the order row (section 11, step 1) and the
// customer's credit serialization (step 1a): a void re-opens billed quantity
// on the order, which adds to the exposure the credit check reads.
func (s *Service) LockForInvoiceVoid(ctx context.Context, orderID uuid.UUID) error {
	if err := s.repo.LockOrder(ctx, orderID); err != nil {
		return notFound(err)
	}
	cur, err := s.repo.GetOrder(ctx, orderID)
	if err != nil {
		return notFound(err)
	}
	return s.repo.LockCustomerCredit(ctx, cur.CustomerID)
}

// ReturnBilled returns a voided invoice's billed quantities to its order: the
// stock back on hand (in (product id, line id) order, step 6), each line's
// quantity_fulfilled reduced, allocation re-run for exactly those quantities
// (what stock cannot cover goes back on order as a back order), and the
// status re-derived for an order still open or fulfilled. The order's own
// events come back for the invoice module to write last.
func (s *Service) ReturnBilled(ctx context.Context, orderID uuid.UUID, billed []invoice.BilledLine, actor string) (invoice.ReopenResult, error) {
	var res invoice.ReopenResult
	cur, err := s.repo.GetOrder(ctx, orderID)
	if err != nil {
		return res, notFound(err)
	}
	// A closed short order (fulfilled with a line short of its quantity) keeps
	// its closed remainder only as that shortfall: re-opening billed quantity
	// would bring the remainder back to life. The act is refused; the order is
	// re-opened by its own transition first.
	if cur.Status == StatusFulfilled {
		for i := range cur.Lines {
			if l := &cur.Lines[i]; l.Quantity != nil && l.LineType != salesdoc.LineText && l.QuantityFulfilled < *l.Quantity {
				return res, conflictBlocker("order_closed_short", "the invoice's order was closed short: voiding the invoice would reopen the closed remainder")
			}
		}
	}
	byID := map[uuid.UUID]int{}
	for i := range cur.Lines {
		byID[cur.Lines[i].ID] = i
	}
	scope := map[uuid.UUID]int64{} // order line id -> the quantity the void returns
	for _, b := range billed {
		i, ok := byID[b.OrderLineID]
		if !ok {
			continue // the order line was removed with its draft edit; nothing to return
		}
		l := &cur.Lines[i]
		if l.QuantityFulfilled < b.Quantity {
			return res, fmt.Errorf("order %s: line %s was fulfilled %s, the void returns %s", cur.ID, l.ID, l.QuantityFulfilled.DecimalString(), b.Quantity.DecimalString())
		}
		l.QuantityFulfilled -= b.Quantity
		scope[l.ID] += int64(b.Quantity)
	}

	// Step 6: the billed stock back on hand, then allocated again.
	if s.inventory != nil {
		for _, i := range stockedIndexes(cur.Lines) {
			l := &cur.Lines[i]
			if q := scope[l.ID]; q > 0 {
				if err := s.inventory.RestockQty(ctx, *l.ProductID, cur.BranchID, httpx.Quantity(q)); err != nil {
					return res, err
				}
			}
		}
		if err := s.allocateReturned(ctx, cur, scope); err != nil {
			return res, err
		}
	}

	from := cur.Status
	next := from
	switch from {
	case StatusConfirmed, StatusBackordered, StatusFulfilled:
		next = deriveStatus(cur)
	}
	cur.Status = next
	if err := s.repo.SaveLineQuantities(ctx, cur.Lines); err != nil {
		return res, err
	}
	if err := s.repo.SaveTransition(ctx, cur); err != nil { // moves the revision
		return res, err
	}
	updated, err := s.repo.GetOrder(ctx, cur.ID)
	if err != nil {
		return res, err
	}
	res.Status = updated.Status.Status()
	if from != StatusBackordered && next == StatusBackordered {
		ev, err := s.eventFor(updated, EventBackordered, from.Status())
		if err != nil {
			return res, err
		}
		res.Events = append(res.Events, ev)
	}
	return res, nil
}

// allocateReturned allocates the returned quantities of the stocked lines in
// (product id, line id) order, min(available, returned) for a plain line and
// whole kits for a kit's components, and records the rest as back ordered. It
// touches no other quantity: a line's closed remainder (after a close short)
// stays closed, and an older back order is neither served nor lost.
func (s *Service) allocateReturned(ctx context.Context, cur *Order, scope map[uuid.UUID]int64) error {
	var order []int
	for _, i := range stockedIndexes(cur.Lines) {
		if scope[cur.Lines[i].ID] > 0 {
			order = append(order, i)
		}
	}
	if len(order) == 0 {
		return nil
	}
	avail := map[uuid.UUID]int64{}
	var products []uuid.UUID
	for _, i := range order {
		p := *cur.Lines[i].ProductID
		if _, seen := avail[p]; !seen {
			a, err := s.inventory.AvailableQty(ctx, p, cur.BranchID)
			if err != nil {
				return err
			}
			avail[p] = int64(a)
			products = append(products, p)
		}
	}
	sort.Slice(products, func(a, b int) bool { return products[a].String() < products[b].String() })

	kitOf := map[uuid.UUID][]int{}
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
	take := map[int]int64{}
	kitDone := map[uuid.UUID]bool{}
	for _, i := range order {
		l := &cur.Lines[i]
		if l.LineType == salesdoc.LineComponent && l.ParentLineID != nil {
			kitID := *l.ParentLineID
			if kitDone[kitID] {
				continue
			}
			kitDone[kitID] = true
			ki, ok := kitIdx[kitID]
			if !ok {
				continue
			}
			// the whole kits the void returned
			limit := new(big.Int).Div(big.NewInt(scope[kitID]), big.NewInt(int64(salesdoc.One)))
			planKit(cur.Lines, ki, kitOf[kitID], avail, take, limit)
			continue
		}
		t := scope[l.ID]
		if a := avail[*l.ProductID]; a < t {
			t = a
		}
		if t > 0 {
			take[i] = t
			avail[*l.ProductID] -= t
		}
	}
	for _, i := range order {
		l := &cur.Lines[i]
		got := int64(0)
		if t := take[i]; t > 0 {
			g, err := s.inventory.AllocateQty(ctx, *l.ProductID, cur.BranchID, httpx.Quantity(t))
			if err != nil {
				return err
			}
			if int64(g) != t {
				return fmt.Errorf("inventory allocated %d of the %d planned for line %s: the rows were not held", g, t, l.ID)
			}
			l.QuantityAllocated += g
			got = int64(g)
		}
		l.QuantityBackordered += httpx.Quantity(scope[l.ID] - got)
	}
	return nil
}

// eventFor builds the order's event for the caller to write.
func (s *Service) eventFor(o *Order, eventType, fromStatus string) (outbox.Event, error) {
	data := map[string]any{
		"number": o.Number, "customer_id": o.CustomerID, "status": o.Status.Status(), "revision": o.Revision,
		"currency": o.Currency, "total_cents": int64(o.TotalCents),
	}
	if fromStatus != "" {
		data["from_status"] = fromStatus
	}
	raw, err := jsonMarshal(data)
	if err != nil {
		return outbox.Event{}, err
	}
	branch := o.BranchID
	return outbox.Event{Type: eventType, EntityType: "order", EntityID: o.ID, BranchID: &branch, Data: raw}, nil
}
