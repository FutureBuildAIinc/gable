// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
)

// Decide read PR 239: a resolution's posting of a named customer's sale
// beside a live completion of that customer (1a, then the customer row FOR
// UPDATE at step 7), the order PR 235 fixed for the sync.
func TestDecide239_ResolutionKeepsTheCreditLockOrder(t *testing.T) {
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	raw, sale := b.build(t, saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "101")}})
	id := *wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "insufficient_stock").ExceptionID
	mustExec(t, f.db, `UPDATE inventory SET quantity = quantity + 10 WHERE product_id = $1`, f.productID)
	ctx := context.Background()
	live, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Rollback(ctx) }()
	if _, err := live.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('order-credit:' || $1::text, 0))`,
		f.customerID.String()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := resolveAs(t, f, id, pos.ResolveRetry, offlineManager)
		done <- err
	}()
	for i := 0; ; i++ {
		var waiting int
		_ = f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting)
		if waiting > 0 {
			break
		}
		if i > 200 {
			t.Fatal("the resolution never reached the credit lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, liveErr := live.Exec(ctx, `SELECT id FROM customers WHERE id = $1 FOR UPDATE`, f.customerID)
	if liveErr == nil {
		_ = live.Commit(ctx)
	} else {
		_ = live.Rollback(ctx)
	}
	resErr := <-done
	if liveErr != nil {
		t.Errorf("the live side's customer row lock failed: %v", liveErr)
	}
	if resErr != nil {
		t.Errorf("the retry failed: %v", resErr)
	}
}

// Decide read PR 239: ADR 0011 6.2, the room after a snapshot counts every
// account tender recorded after it, posted or refused. An account sale
// refused and written off still left the room.
func TestDecide239_AWrittenOffAccountSaleStillLeavesTheRoom(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	b.snap.Accounts[0].OfflineRoomCents = 1500
	b.issue(t)
	onAccount := saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "2")},
		tenders: []pos.SignedSaleTender{{Method: "account"}}}
	mustExec(t, f.db, `UPDATE inventory SET quantity = 1 WHERE product_id = $1`, f.productID)
	rA, sA := b.build(t, onAccount)
	idA := *wantOutcome(t, b.sync(t, rA), sA, pos.OfflineRefused, "insufficient_stock").ExceptionID
	mustResolve(t, f, idA, pos.ResolveWriteOff)
	mustExec(t, f.db, `UPDATE inventory SET quantity = 100 WHERE product_id = $1`, f.productID)
	rB, sB := b.build(t, onAccount)
	wantOutcome(t, b.sync(t, rB), sB, pos.OfflineRefused, "cap_exceeded")
}
