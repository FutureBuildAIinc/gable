// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// The till close and the day close against the offline exception queue
// (ADR 0011 3.2, 3.4 and section 2, slice O4).

// offlineManager is the subject a manager resolves as in these tests: a
// person who is not the bench's cashier.
const offlineManager = "offline-test-manager"

// closeBenchTill closes the bench's session through the route, counting the
// given cash.
func closeBenchTill(f *fixture, b *offlineBench, cash int64) resp {
	return f.do("POST", "/api/v1/pos/till/"+b.session.String()+"/close", map[string]any{
		"counted_by_method": map[string]any{"cash": cash}}, "X-Test-Role", "admin", "X-Test-Sub", uuid.NewString())
}

// refusedForStock syncs one sale the branch cannot fill and answers its
// exception's id.
func refusedForStock(t *testing.T, f *fixture, b *offlineBench) (uuid.UUID, *pos.SignedOfflineSale) {
	t.Helper()
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "101")}})
	out := wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "insufficient_stock")
	return *out.ExceptionID, sale
}

// resolveAs runs one resolution through the service at the row's revision.
func resolveAs(t *testing.T, f *fixture, id uuid.UUID, action, actor string) (*pos.OfflineException, error) {
	t.Helper()
	rev := countOf(t, f.db, `SELECT revision FROM pos_offline_exceptions WHERE id = $1`, id)
	return f.service.ResolveOfflineException(branchctx.WithSystem(context.Background()), id,
		&pos.OfflineResolveIn{Action: action, Reason: "checked by the manager", Revision: &rev}, "", actor)
}

func mustResolve(t *testing.T, f *fixture, id uuid.UUID, action string) *pos.OfflineException {
	t.Helper()
	e, err := resolveAs(t, f, id, action, offlineManager)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return e
}

func wantBlocker(t *testing.T, r resp, status int, blocker string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d: %s", r.status, status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	for _, b := range blockers {
		if b == blocker {
			return
		}
	}
	t.Fatalf("blockers = %v, want %s: %s", blockers, blocker, r.raw)
}

// seedException writes an exception row the way a re-enrolment (the unknown
// tail) or the sync (a sold past warning) would, naming the bench's session.
func seedException(t *testing.T, f *fixture, b *offlineBench, reason string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.db.Pool.QueryRow(context.Background(), `INSERT INTO pos_offline_exceptions (sale_id, register_id,
		branch_id, device_epoch, device_seq, gap_from_seq, gap_to_seq, till_session_id, business_date, reason_code,
		reason_message, status)
		VALUES (gen_random_uuid(), $1, $2, 1, 0, 3, 5, $3, CURRENT_DATE, $4, 'seeded', 'open') RETURNING id`,
		b.register, f.branchID, b.session, reason).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// RULE (ADR 0011 3.4 "How the money already taken is accounted"): CloseTill
// refuses offline_exception_open while an open exception names the session,
// and the drawer stays open; once a manager resolves it the close passes.
func TestOfflineClose_AnOpenExceptionHoldsTheTillClose(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	id, _ := refusedForStock(t, f, b)

	wantBlocker(t, closeBenchTill(f, b, 0), http.StatusConflict, "offline_exception_open")
	if n := countOf(t, f.db, `SELECT count(*) FROM till_sessions WHERE id = $1 AND status = 'OPEN'`, b.session); n != 1 {
		t.Fatalf("the refused close changed the session")
	}
	mustResolve(t, f, id, pos.ResolveWriteOff)
	if r := closeBenchTill(f, b, 0); r.status != http.StatusOK {
		t.Fatalf("close after the write off = %d: %s", r.status, r.raw)
	}
}

// RULE (ADR 0011 section 2 "A rolled back store", lead ruling entry 623):
// the unknown tail item holds the till close until a manager accepts it; a
// posted sold_past_warning row never holds it, and stays on the list until
// it is reviewed.
func TestOfflineClose_TheUnknownTailHoldsAndTheWarningRowDoesNot(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	warn := seedException(t, f, b, "sold_past_warning")
	tail := seedException(t, f, b, "unknown_tail")

	wantBlocker(t, closeBenchTill(f, b, 0), http.StatusConflict, "offline_exception_open")
	if _, err := resolveAs(t, f, tail, pos.ResolveWriteOff, offlineManager); err == nil {
		t.Errorf("an unknown tail item was written off: a manager accepts it")
	}
	e := mustResolve(t, f, tail, pos.ResolveAccept)
	if e.Status != "resolved" || e.ResolutionAction == nil || *e.ResolutionAction != pos.ResolveAccept {
		t.Errorf("the accepted tail = %s, want resolved by accept", e.Status)
	}
	if r := closeBenchTill(f, b, 0); r.status != http.StatusOK {
		t.Fatalf("close beside the warning row = %d: %s", r.status, r.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_offline_exceptions WHERE id = $1 AND status = 'open'`, warn); n != 1 {
		t.Fatalf("the warning row left the list before it was reviewed")
	}
	if e := mustResolve(t, f, warn, pos.ResolveReview); e.Status != "reviewed" {
		t.Errorf("the reviewed warning row = %s, want reviewed", e.Status)
	}
}

// RULE (ADR 0011 section 2 "A rolled back store"): CloseTill for an enrolled
// register refuses queue_not_drained while the server's recorded mark for
// the epoch is below the device's own report.
func TestOfflineClose_AnUndrainedQueueHoldsTheTillClose(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	mustExec(t, f.db, `UPDATE pos_register_devices SET reported_seq = 3, high_seq = 1 WHERE id = $1`, b.deviceID)
	wantBlocker(t, closeBenchTill(f, b, 0), http.StatusConflict, "queue_not_drained")
	mustExec(t, f.db, `UPDATE pos_register_devices SET high_seq = 3 WHERE id = $1`, b.deviceID)
	if r := closeBenchTill(f, b, 0); r.status != http.StatusOK {
		t.Fatalf("close once drained = %d: %s", r.status, r.raw)
	}
}

// RULE (ADR 0011 3.4): CloseBranchDay refuses offline_exception_open while an
// open exception names a session of the day or earlier, beside the
// unsettled sessions it already refuses; once resolved the day closes.
func TestOfflineClose_AnOpenExceptionHoldsTheDayClose(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM till_day_closes WHERE branch_id = $1`, f.branchID)
	})
	mustExec(t, f.db, `UPDATE till_sessions SET status = 'CLOSED' WHERE branch_id = $1 AND status <> 'CLOSED'`, f.branchID)
	// the exception landed after its drawer was counted (the late post case)
	id := seedException(t, f, b, "session_closed")
	date := b.snap.TillSession.BusinessDate
	closeDay := func() resp {
		return f.do("POST", "/api/v1/pos/end-of-day/close", map[string]any{"branch_id": f.branchID.String(), "date": date,
			"deposit_slip_ref": "SLIP-OFF-1"}, "X-Test-Role", "finance", "X-Test-Sub", uuid.NewString())
	}
	wantBlocker(t, closeDay(), http.StatusConflict, "offline_exception_open")
	mustResolve(t, f, id, pos.ResolveWriteOff)
	if r := closeDay(); r.status != http.StatusOK && r.status != http.StatusCreated {
		t.Fatalf("day close after the write off = %d: %s", r.status, r.raw)
	}
}
