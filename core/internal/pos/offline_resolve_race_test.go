// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// The resolutions and the closes under contention (ADR 0011 3.2 and 3.4,
// slice O4): pool size 4, three contenders, the real services and routes.

// race runs the contenders at once and waits for them all.
func race(fns ...func()) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, fn := range fns {
		wg.Add(1)
		go func(fn func()) {
			defer wg.Done()
			<-start
			fn()
		}(fn)
	}
	close(start)
	wg.Wait()
}

// resolveAt runs a resolution at a revision the caller read before the race.
func resolveAt(f *fixture, id uuid.UUID, action, actor string, rev int64) (*pos.OfflineException, error) {
	return f.service.ResolveOfflineException(branchctx.WithSystem(context.Background()), id,
		&pos.OfflineResolveIn{Action: action, Reason: "race", Revision: &rev}, "", actor)
}

// isRefusal reports whether err is a 409 (a lost race, a stale revision, a
// refusal of the live checks), as opposed to a fault.
func isRefusal(err error) bool {
	var hx *httpx.Error
	return errors.As(err, &hx) && hx.Status == http.StatusConflict
}

func storedStock(t *testing.T, f *fixture) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT ROUND(SUM(quantity))::bigint FROM inventory WHERE product_id = $1`, f.productID)
}

// RULE (ADR 0011 3.2 locks 0a and 0b, MONEY-CHECKLIST 1, 2 and 4): a retry
// and a write off of one exception by two managers beside a replay of the
// same item: exactly one resolution wins, the other is refused, the replay
// never posts, and the stored stock and the sale rows are the winner's.
func TestOfflineResolve_TwoManagersAndAReplay(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	offlinePolicyOn(t, f)
	for round := 0; round < 4; round++ {
		b := newOfflineBench(t, f, "REG-OXR-"+uuid.NewString()[:8])
		raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "101")}})
		id := *wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "insufficient_stock").ExceptionID
		mustExec(t, f.db, `UPDATE inventory SET quantity = quantity + 101 WHERE product_id = $1`, f.productID)
		before := storedStock(t, f)
		_, rev, _ := exceptionState(t, f, id)

		var retryErr, offErr, syncErr error
		var replay *pos.OfflineSyncResult
		race(
			func() { _, retryErr = resolveAt(f, id, pos.ResolveRetry, "manager-one", rev) },
			func() { _, offErr = resolveAt(f, id, pos.ResolveWriteOff, "manager-two", rev) },
			func() { replay, syncErr = syncOnce(f, b.register, []json.RawMessage{raw}) },
		)
		if syncErr != nil {
			t.Fatalf("round %d: replay: %v", round, syncErr)
		}
		for _, o := range replay.Items {
			if o.Outcome == pos.OfflinePosted || o.Outcome == "error" {
				t.Errorf("round %d: the replay answered %s", round, o.Outcome)
			}
		}
		if (retryErr == nil) == (offErr == nil) {
			t.Fatalf("round %d: retry %v and write off %v: want exactly one to win", round, retryErr, offErr)
		}
		for _, err := range []error{retryErr, offErr} {
			if err != nil && !isRefusal(err) {
				var hx *httpx.Error
				if !errors.As(err, &hx) || hx.Status != http.StatusPreconditionFailed {
					t.Errorf("round %d: the losing resolution failed with a fault: %v", round, err)
				}
			}
		}
		sales := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, sale.ClientID)
		after := storedStock(t, f)
		if retryErr == nil {
			if sales != 1 || before-after != 101 {
				t.Errorf("round %d: retry won: %d sale rows, stock moved %d, want 1 and 101", round, sales, before-after)
			}
		} else if sales != 0 || before != after {
			t.Errorf("round %d: write off won: %d sale rows, stock moved %d, want none", round, sales, before-after)
		}
		if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = 'pos.offline_exception.resolved'`, id); n != 1 {
			t.Errorf("round %d: %d resolution audit rows, want 1", round, n)
		}
	}
}

// RULE (ADR 0011 3.2 and 3.4, the chain under 0a): a manager's accept of a
// held item, the sync delivering its missing predecessor and the till close,
// at once: nothing deadlocks or faults, the held sale posts exactly once, the
// predecessor posts or waits as late_after_gap, the mark ends at the held
// item, and the stored stock is the posted sales'.
func TestOfflineResolve_TheHeldAcceptBesideTheSyncAndTheClose(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	offlinePolicyOn(t, f)
	for round := 0; round < 4; round++ {
		b := newOfflineBench(t, f, "REG-OXH-"+uuid.NewString()[:8])
		r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
		r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
		held := *wantOutcome(t, b.sync(t, r2), s2, pos.OfflineHeld, "broken_chain").ExceptionID
		_, rev, _ := exceptionState(t, f, held)
		before := storedStock(t, f)

		var acceptErr, syncErr error
		var closeRes resp
		race(
			func() { _, acceptErr = resolveAt(f, held, pos.ResolveAccept, offlineManager, rev) },
			func() { _, syncErr = syncOnce(f, b.register, []json.RawMessage{r1}) },
			func() { closeRes = closeBenchTill(f, b, s1.TotalCents+s2.TotalCents) },
		)
		if syncErr != nil {
			t.Fatalf("round %d: sync: %v", round, syncErr)
		}
		if acceptErr != nil && !isRefusal(err412(acceptErr)) {
			t.Fatalf("round %d: accept faulted: %v", round, acceptErr)
		}
		if closeRes.status != http.StatusOK && closeRes.status != http.StatusConflict {
			t.Fatalf("round %d: close = %d: %s", round, closeRes.status, closeRes.raw)
		}
		posted1 := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, s1.ClientID)
		posted2 := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, s2.ClientID)
		late := countOf(t, f.db, `SELECT count(*) FROM pos_offline_exceptions WHERE sale_id = $1 AND reason_code = 'late_after_gap'`, s1.ClientID)
		if posted2 != 1 {
			t.Errorf("round %d: the held sale posted %d times, want once", round, posted2)
		}
		if posted1+late != 1 {
			t.Errorf("round %d: the predecessor posted %d and waits late %d, want exactly one of them", round, posted1, late)
		}
		if seq, _ := b.deviceMark(t); seq != 2 {
			t.Errorf("round %d: mark = %d, want 2", round, seq)
		}
		if moved := before - storedStock(t, f); moved != posted1+posted2 {
			t.Errorf("round %d: stock moved %d for %d posted sales", round, moved, posted1+posted2)
		}
	}
}

// err412 lets a stale revision (412) count as a lost race.
func err412(err error) error {
	var hx *httpx.Error
	if errors.As(err, &hx) && hx.Status == http.StatusPreconditionFailed {
		return &httpx.Error{Status: http.StatusConflict}
	}
	return err
}

// RULE (ADR 0011 3.2 "The close reads the exception rows without a row
// lock", MONEY-CHECKLIST 1 and 4): the till close, a manager's retry of one
// of the session's exceptions and the sync writing another, at once: nothing
// deadlocks; a close that passes saw the retry posted (its stored expected
// cash is exactly the retried sale's) and no exception open; a close that is
// refused says offline_exception_open.
func TestOfflineResolve_TheCloseBesideAResolutionAndAnExceptionWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	offlinePolicyOn(t, f)
	f.allowAnyVariance()
	for round := 0; round < 4; round++ {
		b := newOfflineBench(t, f, "REG-OXC-"+uuid.NewString()[:8])
		rawA, saleA := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "500")}})
		idA := *wantOutcome(t, b.sync(t, rawA), saleA, pos.OfflineRefused, "insufficient_stock").ExceptionID
		mustExec(t, f.db, `UPDATE inventory SET quantity = quantity + 500 WHERE product_id = $1`, f.productID)
		rawB, _ := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "100000")}})
		_, rev, _ := exceptionState(t, f, idA)

		var retryErr, syncErr error
		var closeRes resp
		var syncRes *pos.OfflineSyncResult
		race(
			func() { closeRes = closeBenchTill(f, b, saleA.TotalCents) },
			func() { _, retryErr = resolveAt(f, idA, pos.ResolveRetry, offlineManager, rev) },
			func() { syncRes, syncErr = syncOnce(f, b.register, []json.RawMessage{rawB}) },
		)
		if syncErr != nil {
			t.Fatalf("round %d: sync: %v", round, syncErr)
		}
		if o := syncRes.Items[0]; o.Outcome != pos.OfflineRefused {
			t.Errorf("round %d: the stock short item answered %s %s", round, o.Outcome, derefStr(o.ReasonMessage))
		}
		if retryErr != nil && !isRefusal(retryErr) {
			t.Fatalf("round %d: the retry faulted: %v", round, retryErr)
		}
		posted := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1 AND status = 'COMPLETED'`, saleA.ClientID)
		switch closeRes.status {
		case http.StatusOK:
			stored := countOf(t, f.db, `SELECT COALESCE((expected_by_method->>'CASH')::bigint, 0) FROM till_sessions WHERE id = $1`, b.session)
			if posted != 1 || stored != saleA.TotalCents {
				t.Errorf("round %d: the close passed with the retried sale posted %d and expected cash %d, want posted and %d",
					round, posted, stored, saleA.TotalCents)
			}
		case http.StatusConflict:
			wantBlocker(t, closeRes, http.StatusConflict, "offline_exception_open")
		default:
			t.Fatalf("round %d: close = %d: %s", round, closeRes.status, closeRes.raw)
		}
	}
}

// RULE (ADR 0011 3.4 "Late post", the branch day lock after 0b): two late
// posts of a counted day's exceptions beside the day's close: nothing
// deadlocks; a day close that passes saw both resolved; a refused one says
// offline_exception_open; every late post is on a date that was open when it
// posted.
func TestOfflineResolve_TheDayCloseBesideTwoLatePosts(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	offlinePolicyOn(t, f)
	f.allowAnyVariance()
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM till_day_closes WHERE branch_id = $1`, f.branchID)
	})
	for round := 0; round < 3; round++ {
		mustExec(t, f.db, `DELETE FROM till_day_closes WHERE branch_id = $1`, f.branchID)
		b := newOfflineBench(t, f, "REG-OXD-"+uuid.NewString()[:8])
		r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
		r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
		// counted empty, so the day has nothing to deposit
		if r := closeBenchTill(f, b, 0); r.status != http.StatusOK {
			t.Fatalf("round %d: close = %d: %s", round, r.status, r.raw)
		}
		mustExec(t, f.db, `UPDATE till_sessions SET status = 'CLOSED' WHERE branch_id = $1 AND status <> 'CLOSED'`, f.branchID)
		res := b.sync(t, r1, r2)
		id1 := *wantOutcome(t, res, s1, pos.OfflineRefused, "session_closed").ExceptionID
		id2 := *wantOutcome(t, res, s2, pos.OfflineRefused, "session_closed").ExceptionID
		_, rev1, _ := exceptionState(t, f, id1)
		_, rev2, _ := exceptionState(t, f, id2)
		date := b.snap.TillSession.BusinessDate

		var e1, e2 error
		var dayRes resp
		race(
			func() { _, e1 = resolveAt(f, id1, pos.ResolveLatePost, offlineManager, rev1) },
			func() { _, e2 = resolveAt(f, id2, pos.ResolveLatePost, offlineManager, rev2) },
			func() {
				dayRes = f.do("POST", "/api/v1/pos/end-of-day/close", map[string]any{"branch_id": f.branchID.String(),
					"date": date, "deposit_slip_ref": "SLIP-RACE"}, "X-Test-Role", "finance", "X-Test-Sub", uuid.NewString())
			},
		)
		for _, err := range []error{e1, e2} {
			if err != nil {
				t.Errorf("round %d: a late post failed: %v", round, err)
			}
		}
		switch dayRes.status {
		case http.StatusOK, http.StatusCreated:
			if n := countOf(t, f.db, `SELECT count(*) FROM pos_offline_exceptions WHERE id IN ($1, $2) AND status = 'resolved'`, id1, id2); n != 2 {
				t.Errorf("round %d: the day closed over %d unresolved exceptions", round, 2-n)
			}
		case http.StatusConflict:
			wantBlocker(t, dayRes, http.StatusConflict, "offline_exception_open")
		default:
			t.Fatalf("round %d: day close = %d: %s", round, dayRes.status, dayRes.raw)
		}
		// no late post is dated into a day that was closed before it posted
		if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions t JOIN invoices i ON i.id = t.invoice_id
			JOIN till_day_closes c ON c.branch_id = $1 AND c.business_date = i.invoice_date AND c.closed_at < i.created_at
			WHERE t.id IN ($2, $3)`, f.branchID, s1.ClientID, s2.ClientID); n != 0 {
			t.Errorf("round %d: %d late posts dated into a day closed before them", round, n)
		}
	}
}
