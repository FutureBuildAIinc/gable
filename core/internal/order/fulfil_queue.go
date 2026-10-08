// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"context"
	"errors"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// Event type written when a request parks (ADR 0005 section 12).
const EventFulfillmentParked = "order.fulfillment_parked"

// EnqueueFulfilment queues a completed delivery for billing (ADR 0005 5.5):
// one row ON CONFLICT DO NOTHING, in the transaction that writes the delivered
// status, so a completed delivery always has its request. The delivery service
// calls it; it runs inside the caller's transaction.
func (s *Service) EnqueueFulfilment(ctx context.Context, deliveryID, orderID uuid.UUID) error {
	return s.repo.InsertFulfillmentRequest(ctx, deliveryID, orderID)
}

// OrderDeliveryType answers an order's delivery type (pickup or delivery) for
// the delivery module's refusal of a pickup order (ADR 0005 5.5).
func (s *Service) OrderDeliveryType(ctx context.Context, orderID uuid.UUID) (string, error) {
	o, err := s.repo.GetOrder(branchctx.WithSystem(ctx), orderID)
	if err != nil {
		return "", notFound(err)
	}
	return string(o.DeliveryType), nil
}

// ServeFulfilmentRequest serves the oldest request that is not parked, once
// (ADR 0005 5.5). It prices the tax with the provider BEFORE its transaction (a
// fresh price on every attempt), then in one transaction in section 11's order
// claims the request (step 0), locks the order and runs the fulfilment for the
// order's allocated unfulfilled quantity, method delivery, deleting the request
// in that transaction. A request that finds nothing left to fulfil is deleted.
// A failure rolls the fulfilment back and, in a separate short transaction,
// counts the attempt and keeps its error; the tenth failure parks the request
// and writes order.fulfillment_parked. It answers false when the queue holds
// nothing to serve; a failure is returned after it is recorded, so the worker
// logs it and waits.
func (s *Service) ServeFulfilmentRequest(ctx context.Context) (bool, error) {
	ctx = branchctx.WithSystem(ctx)
	req, err := s.repo.NextFulfillmentRequest(ctx)
	if err != nil || req == nil {
		return false, err
	}
	fr := FulfilRequest{DeliveryID: &req.DeliveryID, Actor: "system:delivery", InProcess: true}

	claim := func(ctx context.Context) (bool, error) { return s.repo.ClaimFulfillmentRequest(ctx, req.DeliveryID) }
	priced, err := s.prepareFulfilTax(ctx, req.OrderID, fr)
	if err == nil {
		_, err = s.fulfil(ctx, req.OrderID, nil, fr, priced, func(ctx context.Context) (bool, error) {
			ok, err := claim(ctx)
			if err != nil || !ok {
				return ok, err
			}
			return true, s.repo.DeleteFulfillmentRequest(ctx, req.DeliveryID)
		})
	}
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errRequestGone):
		// another worker holds it, or it was served: move on to the next
		return true, nil
	case IsNothingToFulfil(err):
		// nothing left to bill (the desk billed it, or it is already
		// fulfilled): the request is done.
		return true, s.inTx(ctx, func(ctx context.Context) error {
			ok, err := claim(ctx)
			if err != nil || !ok {
				return err
			}
			return s.repo.DeleteFulfillmentRequest(ctx, req.DeliveryID)
		})
	}

	// A failed attempt: count it in its own short transaction.
	msg := err.Error()
	var he *httpx.Error
	if errors.As(err, &he) {
		msg = he.Message
		for _, d := range he.Details {
			if d.Code != "" {
				msg += " [" + d.Code + "]"
			}
		}
	}
	if rerr := s.inTx(ctx, func(ctx context.Context) error {
		parked, err := s.repo.RecordFulfillmentFailure(ctx, req.DeliveryID, msg, maxFulfilmentAttempts)
		if err != nil || !parked {
			return err
		}
		o, err := s.repo.GetOrder(ctx, req.OrderID)
		if err != nil {
			return err
		}
		return s.record(ctx, o, EventFulfillmentParked, "")
	}); rerr != nil {
		return true, rerr
	}
	return true, err
}

// ListFulfilmentRequests lists the queue for the desk (ADR 0005 5.5).
func (s *Service) ListFulfilmentRequests(ctx context.Context, f RequestFilter) (items []FulfillmentRequest, hasMore bool, err error) {
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.ListFulfillmentRequests(ctx, f)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > limit {
		rows, hasMore = rows[:limit], true
	}
	return rows, hasMore, nil
}

// RetryFulfilmentRequest clears a parked request's parked_at and attempts so
// the worker takes it again (ADR 0005 5.5). The order's branch is held to the
// caller's wall first (fail closed).
func (s *Service) RetryFulfilmentRequest(ctx context.Context, deliveryID uuid.UUID) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		orderID, ok, err := s.repo.DeliveryRequestOrder(ctx, deliveryID)
		if err != nil {
			return err
		}
		if !ok {
			return httpx.NotFound("fulfilment request not found")
		}
		o, err := s.repo.GetOrder(ctx, orderID)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkOrderBranch(ctx, o); err != nil {
			return err
		}
		_, found, err := s.repo.RetryFulfillmentRequest(ctx, deliveryID)
		if err != nil {
			return err
		}
		if !found {
			return httpx.NotFound("fulfilment request not found")
		}
		return nil
	})
}
