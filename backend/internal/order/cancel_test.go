// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// CancelOrder is the first write path the contractor portal exposes that can
// undo something, so its refusals are the interesting part, not its happy path.
// These tests drive the state machine through a fake repository: no Postgres,
// no inventory service, so what is under test is exactly the transition table
// and nothing else.
//
// The CONFIRMED -> CANCELLED path also releases allocated stock. That needs a
// real *inventory.Service (a concrete type, not an interface) and is therefore
// verified against the live database rather than here; the unit test below
// pins that CONFIRMED is *accepted*, and the release itself is asserted in the
// live transcript (available 507 -> 502 on confirm -> 507 after cancel).

type cancelFakeRepo struct {
	order    *Order
	statuses []OrderStatus
	getErr   error
}

func (f *cancelFakeRepo) CreateOrder(context.Context, *Order) error { return nil }
func (f *cancelFakeRepo) GetOrder(_ context.Context, _ uuid.UUID) (*Order, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.order, nil
}
func (f *cancelFakeRepo) ListOrders(context.Context) ([]Order, error) { return nil, nil }
func (f *cancelFakeRepo) ListOrdersPaginated(context.Context, int, int) ([]Order, int, error) {
	return nil, 0, nil
}
func (f *cancelFakeRepo) UpdateStatus(_ context.Context, _ uuid.UUID, s OrderStatus) error {
	f.statuses = append(f.statuses, s)
	if f.order != nil {
		f.order.Status = s
	}
	return nil
}

func newCancelSvc(status OrderStatus) (*Service, *cancelFakeRepo) {
	repo := &cancelFakeRepo{order: &Order{
		ID:         uuid.New(),
		CustomerID: uuid.New(),
		Status:     status,
		Lines:      []OrderLine{{ProductID: uuid.New(), Quantity: 3, PriceEach: 1000}},
	}}
	// Nil inventory/invoice/customer/PO services: CancelOrder must not reach
	// any of them for the statuses these tests exercise. A nil-pointer panic
	// would be the signal that it did.
	return NewService(repo, nil, nil, nil, nil), repo
}

// CORRECTNESS: the statuses that hold no allocation and no invoice cancel
// cleanly, and the recorded status is CANCELLED exactly once.
func TestCancelOrder_AllowsDraftOnHoldAndConfirmed(t *testing.T) {
	for _, from := range []OrderStatus{StatusDraft, StatusOnHold, StatusConfirmed} {
		t.Run(string(from), func(t *testing.T) {
			svc, repo := newCancelSvc(from)
			if err := svc.CancelOrder(context.Background(), repo.order.ID, "wrong length"); err != nil {
				t.Fatalf("CancelOrder from %s: %v", from, err)
			}
			if len(repo.statuses) != 1 || repo.statuses[0] != StatusCancelled {
				t.Fatalf("status writes = %v, want exactly one CANCELLED", repo.statuses)
			}
		})
	}
}

// CORRECTNESS: a FULFILLED order has shipped stock, issued an invoice and
// posted to AR/GL. Undoing that is a credit memo, not a cancel. It must be
// refused with a distinguishable error, and NOTHING may be written.
func TestCancelOrder_RefusesFulfilled(t *testing.T) {
	svc, repo := newCancelSvc(StatusFulfilled)

	err := svc.CancelOrder(context.Background(), repo.order.ID, "")
	if err == nil {
		t.Fatal("a FULFILLED order was cancelled")
	}
	if !errors.Is(err, ErrOrderNotCancellable) {
		t.Errorf("err = %v, want it to wrap ErrOrderNotCancellable so the handler can render 409", err)
	}
	if len(repo.statuses) != 0 {
		t.Errorf("status was written %v on a refused cancel", repo.statuses)
	}
	if repo.order.Status != StatusFulfilled {
		t.Errorf("status = %s, want it left at FULFILLED", repo.order.Status)
	}
}

// CORRECTNESS: cancelling twice must fail, and must fail DIFFERENTLY from a
// state-machine refusal. A second 200/204 would tell a client it had just
// cancelled an order when it had done nothing — the exact confusion a
// contractor hitting a button twice on a slow connection would hit.
func TestCancelOrder_RefusesASecondCancel(t *testing.T) {
	svc, repo := newCancelSvc(StatusDraft)
	ctx := context.Background()

	if err := svc.CancelOrder(ctx, repo.order.ID, ""); err != nil {
		t.Fatalf("first cancel: %v", err)
	}

	err := svc.CancelOrder(ctx, repo.order.ID, "")
	if err == nil {
		t.Fatal("the second cancel succeeded")
	}
	if !errors.Is(err, ErrOrderAlreadyCancelled) {
		t.Errorf("err = %v, want ErrOrderAlreadyCancelled", err)
	}
	if errors.Is(err, ErrOrderNotCancellable) {
		t.Error("a repeat cancel must be distinguishable from a state-machine refusal")
	}
	if len(repo.statuses) != 1 {
		t.Errorf("status writes = %v, want exactly one — the second cancel wrote again", repo.statuses)
	}
}

// CORRECTNESS: an unreadable order is not silently treated as cancellable. The
// repository error propagates rather than being swallowed into a refusal that
// would look like policy.
func TestCancelOrder_PropagatesLoadFailure(t *testing.T) {
	repo := &cancelFakeRepo{getErr: errors.New("connection reset")}
	svc := NewService(repo, nil, nil, nil, nil)

	err := svc.CancelOrder(context.Background(), uuid.New(), "")
	if err == nil {
		t.Fatal("a failed load was treated as a successful cancel")
	}
	if errors.Is(err, ErrOrderAlreadyCancelled) || errors.Is(err, ErrOrderNotCancellable) {
		t.Errorf("a load failure was reported as a policy refusal: %v", err)
	}
	if len(repo.statuses) != 0 {
		t.Errorf("status was written after a failed load: %v", repo.statuses)
	}
}
