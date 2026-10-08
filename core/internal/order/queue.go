// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/eventbus"
	"github.com/google/uuid"
)

// SubjectPurchaseOrderReceived is the outbox subject the receive writes and
// the allocation subscriber matches.
const SubjectPurchaseOrderReceived = "purchase_order.received"

// HandlePurchaseOrderReceived is the order module's drain subscriber for
// purchase_order.received (ADR 0005 5.4). It does one thing: it inserts one
// allocation request per back ordered order that waits on the received
// products, in (confirmed_at, id) order, ON CONFLICT DO NOTHING. It takes no
// lock but the request rows' own, reads through the pass's transaction and
// writes no event, which keeps the drain inside ADR 0003 section 2: a handler
// that locked orders and inventory and wrote an event would hold the global
// outbox lock while waiting on rows a desk confirm holds. A replay inserts
// nothing twice.
func (s *Service) HandlePurchaseOrderReceived(ctx context.Context, ev eventbus.Event) error {
	var data struct {
		BranchID   uuid.UUID   `json:"branch_id"`
		ProductIDs []uuid.UUID `json:"product_ids"`
	}
	if err := json.Unmarshal(ev.Payload, &data); err != nil {
		return fmt.Errorf("purchase_order.received: payload: %w", err)
	}
	_, err := s.repo.QueueAllocationRequests(ctx, data.BranchID, data.ProductIDs)
	return err
}

// ServeAllocationRequest serves the oldest allocation request nobody holds, in
// one transaction (ADR 0005 5.4, 11): the request row (step 0), the order row,
// then inventory by (product id, inventory id). It allocates the available
// stock to the order's back ordered lines, derives the status, deletes the
// request, and writes order.backorder_released last when the back order
// cleared. A request that finds nothing to allocate, or an order no longer
// back ordered, is deleted all the same. It answers false when the queue is
// empty. One order per transaction; the worker calls it until it answers false.
func (s *Service) ServeAllocationRequest(ctx context.Context) (bool, error) {
	served := false
	err := s.inTx(branchctx.WithSystem(ctx), func(ctx context.Context) error {
		orderID, ok, err := s.repo.ClaimAllocationRequest(ctx)
		if err != nil || !ok {
			return err
		}
		served = true
		if err := s.repo.LockOrder(ctx, orderID); err != nil {
			// the order is gone: the cascade took the request with it
			return nil
		}
		cur, err := s.repo.GetOrder(ctx, orderID)
		if err != nil {
			return err
		}
		if cur.Status == StatusBackordered {
			if _, _, err := s.allocateAndDerive(ctx, cur, "system:allocation"); err != nil {
				return err
			}
		}
		return s.repo.DeleteAllocationRequest(ctx, orderID)
	})
	return served, err
}
