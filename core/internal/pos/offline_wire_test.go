// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// The wire route of the offline sync (ADR 0011 section 3, slice C2): the
// register's shell posts its signed sales to POST /api/v1/pos/sync, and the
// route answers what the rebuilt sync answers, behind the branch's offline
// policy (off unless a dealer sets it, when it keeps 409 money_never_queues).

// syncWire posts one batch the way the shell's core does and answers the
// response.
func (b *offlineBench) syncWire(t *testing.T, items ...json.RawMessage) resp {
	t.Helper()
	body := map[string]any{"batch_id": uuid.NewString(), "register_id": b.register,
		"device_epoch": b.epoch, "reported_seq": b.seq, "items": items}
	return b.f.do("POST", "/api/v1/pos/sync", body, "X-Test-Role", "cashier", "X-Test-Sub", uuid.NewString())
}

func wireOutcome(t *testing.T, r resp, clientID string) map[string]any {
	t.Helper()
	items, _ := r.body["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["client_id"] == clientID {
			return m
		}
	}
	t.Fatalf("no outcome for %s in %s", clientID, r.raw)
	return nil
}

// RULE (ADR 0011 section 3, C2 lifts money_never_queues): at a branch whose
// dealer opted in, a signed offline sale posted to the route posts once, and
// the answer carries the confirmed mark and no open exception; the same batch
// again answers duplicate and posts nothing more.
func TestOfflineSyncWire_PostsThroughTheRoute(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "2")}})
	r := b.syncWire(t, raw)
	if r.status != http.StatusOK {
		t.Fatalf("sync = %d: %s, want 200", r.status, r.raw)
	}
	out := wireOutcome(t, r, sale.ClientID)
	if out["outcome"] != pos.OfflinePosted || out["invoice_number"] == nil {
		t.Errorf("outcome %v, want posted with an invoice number", out)
	}
	if got := num(t, r.body, "confirmed_seq"); got != sale.DeviceSeq {
		t.Errorf("confirmed_seq = %d, want %d", got, sale.DeviceSeq)
	}
	if got := num(t, r.body, "open_exception_count"); got != 0 {
		t.Errorf("open_exception_count = %d, want 0", got)
	}
	again := b.syncWire(t, raw)
	if again.status != http.StatusOK || wireOutcome(t, again, sale.ClientID)["outcome"] != pos.OfflineDuplicate {
		t.Errorf("the replay = %d %s, want a duplicate", again.status, again.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1 AND status = 'COMPLETED'`, sale.ClientID); n != 1 {
		t.Errorf("%d completed sales for the item, want 1", n)
	}
}

// RULE (ADR 0011 section 4): with no policy at the branch the route keeps its
// refusal for a batch in the signed shape, and nothing of it is written.
func TestOfflineSyncWire_PolicyOffKeepsTheRefusal(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	mustExec(t, f.db, `UPDATE pos_offline_policy SET enabled = FALSE WHERE branch_id = $1`, f.branchID)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	r := b.syncWire(t, raw)
	if r.status != http.StatusConflict {
		t.Fatalf("sync = %d: %s, want 409", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "money_never_queues" {
		t.Errorf("code = %q, want money_never_queues", code)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, sale.ClientID); n != 0 {
		t.Errorf("%d sales written, want 0", n)
	}
}

// RULE (ADR 0011 6.3): the sync is a person's act, never a machine key; a
// key is refused before anything of the batch is read or written.
func TestOfflineSyncWire_AMachineKeyIsRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	keyed := keyedServer(f, uuid.NewString())
	body, _ := json.Marshal(map[string]any{"batch_id": uuid.NewString(), "register_id": b.register,
		"device_epoch": b.epoch, "reported_seq": b.seq, "items": []json.RawMessage{raw}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pos/sync", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	keyed.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("sync with a machine key = %d: %s, want 403", rec.Code, rec.Body.String())
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, sale.ClientID); n != 0 {
		t.Errorf("%d sales written, want 0", n)
	}
}

// RULE (ADR 0011 section 3): with the policy on, the batch names its epoch
// and the device's own mark; a batch without them is a 400 naming the field.
func TestOfflineSyncWire_TheBatchNamesItsEpochAndMark(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	raw, _ := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	r := f.do("POST", "/api/v1/pos/sync", map[string]any{"batch_id": uuid.NewString(), "register_id": b.register,
		"items": []json.RawMessage{raw}}, "X-Test-Role", "cashier", "X-Test-Sub", uuid.NewString())
	if r.status != http.StatusBadRequest {
		t.Fatalf("sync without the epoch = %d: %s, want 400", r.status, r.raw)
	}
	for _, field := range []string{"device_epoch", "reported_seq"} {
		if !strings.Contains(string(r.raw), field) {
			t.Errorf("the refusal does not name %s: %s", field, r.raw)
		}
	}
}

// RULE (CP-13 OW, items 283 and 284): a sale that cannot be accepted goes to
// the manager's list and its money still balances the drawer: the session's
// expected cash carries the cash the clerk took, shown apart as held offline,
// and the answer counts the open exception for the counter. Nothing posts.
func TestOfflineSyncWire_ARefusedSaleKeepsTheDrawerBalanced(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	before := offlineDrawerCash(t, f, b)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "101")}})
	r := b.syncWire(t, raw)
	if r.status != http.StatusOK {
		t.Fatalf("sync = %d: %s", r.status, r.raw)
	}
	out := wireOutcome(t, r, sale.ClientID)
	if out["outcome"] != pos.OfflineRefused || out["reason_code"] != "insufficient_stock" {
		t.Fatalf("outcome %v, want refused insufficient_stock", out)
	}
	if got := num(t, r.body, "open_exception_count"); got != 1 {
		t.Errorf("open_exception_count = %d, want 1", got)
	}
	if got := offlineDrawerCash(t, f, b) - before; got != sale.TotalCents {
		t.Errorf("expected cash moved by %d, want %d (the cash taken on the refused sale)", got, sale.TotalCents)
	}
	rep := f.do("GET", "/api/v1/pos/till/"+b.session.String()+"/report", nil, "X-Test-Role", "admin", "X-Test-Sub", uuid.NewString())
	held, _ := rep.body["offline_held_by_method"].(map[string]any)
	if held == nil || num(t, held, "CASH") != sale.TotalCents {
		t.Errorf("offline_held_by_method = %v, want CASH %d", rep.body["offline_held_by_method"], sale.TotalCents)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, sale.ClientID); n != 0 {
		t.Errorf("%d sales written for the refused item, want 0", n)
	}
	// resolved, the held money leaves the expected figure again
	mustExec(t, f.db, `UPDATE pos_offline_exceptions SET status = 'resolved', resolution_action = 'write_off',
		resolved_by = 'test', resolved_at = NOW() WHERE sale_id = $1`, sale.ClientID)
	if got := offlineDrawerCash(t, f, b) - before; got != 0 {
		t.Errorf("after the resolution expected cash moved by %d, want 0", got)
	}
}

// RULE (CP-13 OW, item 284, through the route): a sale rung offline for an
// account with an open warning that asks to be acknowledged is accepted and
// posted, stamped on the manager's list with the warning's id, and does not
// count as an open exception that holds the till.
func TestOfflineSyncWire_SoldPastAnOpenWarningPosts(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	alert := uuid.New()
	mustExec(t, f.db, `INSERT INTO customer_alerts (id, customer_id, kind, text, show_at_counter, acknowledge_required,
		created_by) VALUES ($1, $2, 'warning', 'call the office first', TRUE, TRUE, 'office')`, alert, f.customerID)
	before := offlineDrawerCash(t, f, b)
	raw, sale := b.build(t, saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "1")}})
	r := b.syncWire(t, raw)
	if r.status != http.StatusOK || wireOutcome(t, r, sale.ClientID)["outcome"] != pos.OfflinePosted {
		t.Fatalf("sync = %d %s, want posted", r.status, r.raw)
	}
	if got := num(t, r.body, "open_exception_count"); got != 0 {
		t.Errorf("open_exception_count = %d, want 0 (a posted warning row does not hold the till)", got)
	}
	var stamp string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT warning_alert_id::text FROM pos_offline_exceptions
		WHERE sale_id = $1 AND reason_code = 'sold_past_warning' AND status = 'open'`, sale.ClientID).Scan(&stamp); err != nil {
		t.Fatalf("the warning row: %v", err)
	}
	if stamp != alert.String() {
		t.Errorf("stamped %s, want %s", stamp, alert)
	}
	// the posted sale's cash is in the drawer once, never again as held
	if got := offlineDrawerCash(t, f, b) - before; got != sale.TotalCents {
		t.Errorf("expected cash moved by %d, want %d", got, sale.TotalCents)
	}
}
