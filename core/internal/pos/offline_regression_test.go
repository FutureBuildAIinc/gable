// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Regressions a deciding read of the offline sync found: a held sale sold
// past a warning, a held account sale counted against itself, and the
// offline posting's credit lock order.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// A held sale to an account with an acknowledge required warning, re-offered
// once its predecessor arrives, posts and the sync answers; it must not wedge.
func TestDecide235_HeldSaleSoldPastAWarningIsReoffered(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	mustExec(t, f.db, `INSERT INTO customer_alerts (id, customer_id, kind, text, show_at_counter, acknowledge_required,
		created_by, created_at) VALUES ($1, $2, 'warning', 'check the yard first', TRUE, TRUE, 'office', NOW())`,
		uuid.New(), f.customerID)
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	r3, s3 := b.build(t, saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "1")}})
	res := b.sync(t, r1, r3)
	wantOutcome(t, res, s1, pos.OfflinePosted, "")
	wantOutcome(t, res, s3, pos.OfflineHeld, "broken_chain")
	res, err := b.trySync(r2)
	if err != nil {
		r4, _ := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
		_, err2 := b.trySync(r4)
		seq, _ := b.deviceMark(t)
		t.Fatalf("the sync that brings the predecessor failed: %v; the next sync: %v; mark %d", err, err2, seq)
	}
	wantOutcome(t, res, s2, pos.OfflinePosted, "")
	wantOutcome(t, res, s3, pos.OfflinePosted, "")
	if seq, _ := b.deviceMark(t); seq != 3 {
		t.Errorf("mark = %d, want 3", seq)
	}
}

// A held account sale within the room, re-offered, is judged against the
// room without its own held row: it posts.
func TestDecide235_HeldAccountSaleIsNotCountedAgainstItself(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	b.snap.Accounts[0].OfflineRoomCents = 1500
	b.issue(t)
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	// 2 at 5.00 and tax: 1089 on account, inside the room of 1500
	r3, s3 := b.build(t, saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "2")},
		tenders: []pos.SignedSaleTender{{Method: "account"}}})
	if s3.TotalCents != 1089 {
		t.Fatalf("the bench made %d, want 1089", s3.TotalCents)
	}
	res := b.sync(t, r1, r3)
	wantOutcome(t, res, s1, pos.OfflinePosted, "")
	wantOutcome(t, res, s3, pos.OfflineHeld, "broken_chain")
	res = b.sync(t, r2)
	wantOutcome(t, res, s2, pos.OfflinePosted, "")
	wantOutcome(t, res, s3, pos.OfflinePosted, "")
	var n int64
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pos_transactions WHERE id = $1 AND status = 'COMPLETED'`,
		s3.ClientID).Scan(&n); err != nil || n != 1 {
		t.Errorf("the held account sale posted %d times (%v), want 1", n, err)
	}
}

// The offline posting takes its locks in ADR 0005 section 11's order beside
// a live completion of the same customer: the live side holds step 1a (the
// credit advisory lock) and then takes the customer row FOR UPDATE (step 7,
// the invoice); the offline side must not hold a lock on that customer row
// while it waits for step 1a. Neither side may die of a deadlock.
func TestDecide235_OfflinePostingKeepsTheCreditLockOrder(t *testing.T) {
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	raw, sale := b.build(t, saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "1")}})
	ctx := context.Background()
	live, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Rollback(ctx) }()
	// the live completion's step 1a, exactly as LockCustomerCredit takes it
	if _, err := live.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('order-credit:' || $1::text, 0))`,
		f.customerID.String()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		res *pos.OfflineSyncResult
		err error
	}, 1)
	go func() {
		res, err := syncOnce(f, b.register, []json.RawMessage{raw})
		done <- struct {
			res *pos.OfflineSyncResult
			err error
		}{res, err}
	}()
	// wait until the offline posting waits on a lock
	waitForALockWaiter(t, f)
	// the live completion's step 7
	_, liveErr := live.Exec(ctx, `SELECT id FROM customers WHERE id = $1 FOR UPDATE`, f.customerID)
	if liveErr == nil {
		_ = live.Commit(ctx)
	} else {
		_ = live.Rollback(ctx)
	}
	got := <-done
	if liveErr != nil {
		t.Errorf("the live side's customer row lock failed: %v", liveErr)
	}
	if got.err != nil {
		t.Fatalf("sync: %v", got.err)
	}
	for _, o := range got.res.Items {
		if o.ClientID == sale.ClientID && o.Outcome != pos.OfflinePosted {
			t.Errorf("the offline sale answered %s: %s", o.Outcome, derefStr(o.ReasonMessage))
		}
	}
}

// The same order with a replaced cart whose id sorts after the offline
// sale's: a live completion of that cart holds the cart row (step 1) and
// step 1a, then takes the customer row FOR UPDATE (step 7); the offline side
// must not hold a lock on the customer row while it waits for the cart.
func TestDecide235_OfflinePostingKeepsTheCartLockOrder(t *testing.T) {
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	cart := f.startSale(nil)
	if r := f.addLine(cart, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	// a client id that sorts before the cart's
	first := "00000000-0000-4000-8000-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	raw, sale := b.build(t, saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "1")}},
		func(s *pos.SignedOfflineSale) {
			s.ClientID = first
			s.ReplacesSaleID = &cart
		})
	if sale.ClientID >= cart {
		t.Fatalf("the sale's id %s does not sort before the cart's %s", sale.ClientID, cart)
	}
	ctx := context.Background()
	live, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Rollback(ctx) }()
	// the live completion's step 1 and step 1a
	if _, err := live.Exec(ctx, `SELECT 1 FROM pos_transactions WHERE id = $1 FOR NO KEY UPDATE`, cart); err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('order-credit:' || $1::text, 0))`,
		f.customerID.String()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		res *pos.OfflineSyncResult
		err error
	}, 1)
	go func() {
		res, err := syncOnce(f, b.register, []json.RawMessage{raw})
		done <- struct {
			res *pos.OfflineSyncResult
			err error
		}{res, err}
	}()
	waitForALockWaiter(t, f)
	// the live completion's step 7
	_, liveErr := live.Exec(ctx, `SELECT id FROM customers WHERE id = $1 FOR UPDATE`, f.customerID)
	if liveErr == nil {
		_ = live.Commit(ctx)
	} else {
		_ = live.Rollback(ctx)
	}
	got := <-done
	if liveErr != nil {
		t.Errorf("the live side's customer row lock failed: %v", liveErr)
	}
	if got.err != nil {
		t.Fatalf("sync: %v", got.err)
	}
	for _, o := range got.res.Items {
		if o.ClientID == sale.ClientID && o.Outcome != pos.OfflinePosted {
			t.Errorf("the offline sale answered %s: %s", o.Outcome, derefStr(o.ReasonMessage))
		}
	}
	if st := str(t, f.getSale(t, cart), "status"); st != "abandoned" {
		t.Errorf("the replaced cart reads %s, want abandoned", st)
	}
}

// waitForALockWaiter waits until a session on this test's database waits on
// a lock (other databases on a shared server are not counted).
func waitForALockWaiter(t *testing.T, f *fixture) {
	t.Helper()
	for i := 0; ; i++ {
		var waiting int
		_ = f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND pid <> pg_backend_pid()`).Scan(&waiting)
		if waiting > 0 {
			return
		}
		if i > 200 {
			t.Fatal("the offline posting never waited on a lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
