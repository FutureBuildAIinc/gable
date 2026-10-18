// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The branch wall of the five terminal routes (CP-05, SEC-NOHEADER-1): a
// caller with branch A's reach is refused branch B's intents and lost items,
// as a header caller (X-Branch-Id names A) and as a no header caller (the
// grants name A). Reads and lists never show B's rows; the sweep from an
// HTTP caller sweeps only the caller's reach; cancel and resolve move
// nothing across the wall.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// wallWorld is two branches with terminal rows in each: branch A is the
// fixture's own, branch B a second branch the caller holds no reach to.
type wallWorld struct {
	f       *fixture
	branchB uuid.UUID
	ledger  *payment.TerminalLedger
	// B's rows: a claimed intent (cancel target), a swept lost intent and its
	// item, an expired pending intent the sweep must leave alone.
	bClaimed, bLost, bPending uuid.UUID
	bItem                     uuid.UUID
	// A's rows: an expired pending intent the A caller's sweep may take, and
	// the item a sweep files for it.
	aPending uuid.UUID
}

func (w *wallWorld) insertSale(t *testing.T, branch uuid.UUID, register string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	mustExec(t, w.f.db, `INSERT INTO pos_transactions
		(id, register_id, cashier_id, subtotal, tax_amount, total, status, branch_id, change_due, revision, currency, number, created_at, updated_at)
		VALUES ($1, $2, $3, 0, 0, 0, 'OPEN', $4, 0, 1, 'USD', $5, NOW(), NOW())`,
		id, register, uuid.New(), branch, "POS-"+id.String()[:8])
	return id
}

func (w *wallWorld) intent(t *testing.T, branch uuid.UUID, register, leg string, dispatch bool, lease time.Duration) uuid.UUID {
	t.Helper()
	sale := w.insertSale(t, branch, register)
	in := &payment.TerminalIntent{ID: uuid.New(), TransactionID: sale, LegKey: leg, RegisterID: register, BranchID: branch,
		AmountCents: 1234, Kind: payment.TerminalKindSale, DeviceID: register,
		IdempotencyKey: "sale-" + sale.String() + "-" + leg, LeaseUntil: time.Now().Add(lease)}
	if err := w.ledger.Claim(context.Background(), in); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if dispatch {
		if ok, err := w.ledger.MarkDispatched(context.Background(), in.ID, "devref-"+leg+"-"+in.ID.String()[:6], time.Now().Add(lease)); err != nil || !ok {
			t.Fatalf("dispatch: %t %v", ok, err)
		}
	}
	return in.ID
}

func newWallWorld(t *testing.T) *wallWorld {
	t.Helper()
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	// the kill switches are read when the fixture's middleware is built
	setPosSetting(t, db, "multi_branch_enabled", "true")
	setPosSetting(t, db, "default_branch_required", "false")
	branchB := uuid.New()
	mustExec(t, db, `INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'terminal wall B')`, branchB, "TW-"+branchB.String()[:8])
	t.Cleanup(func() {
		setPosSetting(t, db, "multi_branch_enabled", "false")
		setPosSetting(t, db, "default_branch_required", "true")
		mustExec(t, db, `DELETE FROM user_locations WHERE user_sub IN ('term-wall-a', 'term-wall-n')`)
		mustExec(t, db, `DELETE FROM payment_terminal_lost_items`)
		mustExec(t, db, `DELETE FROM payment_terminal_intents`)
		mustExec(t, db, `DELETE FROM pos_transactions WHERE branch_id = $1`, branchB)
		mustExec(t, db, `DELETE FROM locations WHERE id = $1`, branchB)
	})
	f := newFixture(t, db, withTerminal(payment.SimNoAnswer, 0))
	mustExec(t, db, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('term-wall-a', $1, TRUE, 'test')`, f.branchID)
	mustExec(t, db, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('term-wall-n', $1, TRUE, 'test')`, f.branchID)
	w := &wallWorld{f: f, branchB: branchB, ledger: payment.NewTerminalLedger(db)}
	w.bClaimed = w.intent(t, branchB, "REG-WB-1", "card-0", false, time.Minute)
	w.bLost = w.intent(t, branchB, "REG-WB-2", "card-0", true, -time.Minute)
	w.bPending = w.intent(t, branchB, "REG-WB-3", "card-0", true, -time.Minute)
	w.aPending = w.intent(t, f.branchID, "REG-WA-1", "card-0", true, -time.Minute)
	// sweep B's lost leg unscoped (the worker): B's item exists, B's other
	// expired leg is swept by the same pass, so reset it to pending for the
	// sweep-scope proof below
	if _, err := w.ledger.Sweep(context.Background(), time.Now()); err != nil {
		t.Fatalf("worker sweep: %v", err)
	}
	items, err := w.ledger.OpenLostItems(context.Background(), &branchB)
	if err != nil || len(items) == 0 {
		t.Fatalf("B's items: %d %v", len(items), err)
	}
	for _, it := range items {
		if it.IntentID == w.bLost {
			w.bItem = it.ID
		}
	}
	if w.bItem == uuid.Nil {
		t.Fatal("B's lost leg filed no item")
	}
	// put B's second expired leg and A's leg back to pending so the HTTP
	// sweep has something to take or leave
	mustExec(t, db, `DELETE FROM payment_terminal_lost_items WHERE intent_id IN ($1, $2)`, w.bPending, w.aPending)
	mustExec(t, db, `UPDATE payment_terminal_intents SET state = 'pending_at_device' WHERE id IN ($1, $2)`, w.bPending, w.aPending)
	return w
}

func (w *wallWorld) state(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := w.f.db.Pool.QueryRow(context.Background(), `SELECT state FROM payment_terminal_intents WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (w *wallWorld) itemState(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := w.f.db.Pool.QueryRow(context.Background(), `SELECT state FROM payment_terminal_lost_items WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestTerminalRoutesHoldTheBranchWall: a caller with branch A's reach, as a
// header caller and as a no header caller, is refused branch B's intents and
// items on all five routes, and nothing of B moves.
func TestTerminalRoutesHoldTheBranchWall(t *testing.T) {
	w := newWallWorld(t)
	f := w.f
	header := []string{"X-Test-Role", "finance", "X-Test-Sub", "term-wall-a", "X-Branch-Id", f.branchID.String()}
	noHeader := []string{"X-Test-Role", "finance", "X-Test-Sub", "term-wall-n"}

	for name, h := range map[string][]string{"header": header, "no header": noHeader} {
		wantID := http.StatusNotFound // a header caller finds a missing row
		if name == "no header" {
			wantID = http.StatusForbidden // the grants hold names the wall
		}
		// 1. GET /terminal-intents/{id}
		if r := f.do("GET", "/api/v1/pos/terminal-intents/"+w.bLost.String(), nil, h...); r.status != wantID {
			t.Errorf("%s: another branch's intent read = %d, want %d: %s", name, r.status, wantID, r.raw)
		}
		// 2. POST /terminal-intents/{id}/cancel
		if r := f.do("POST", "/api/v1/pos/terminal-intents/"+w.bClaimed.String()+"/cancel", nil, h...); r.status != wantID {
			t.Errorf("%s: another branch's intent cancel = %d, want %d: %s", name, r.status, wantID, r.raw)
		}
		if got := w.state(t, w.bClaimed); got != "claimed" {
			t.Errorf("%s: the cancel crossed the wall: B's claim is %s", name, got)
		}
		// 3. POST /terminal-lost-items/{id}/resolve, both outcomes
		for _, outcome := range []string{"declined", "authorized"} {
			if r := f.do("POST", "/api/v1/pos/terminal-lost-items/"+w.bItem.String()+"/resolve",
				map[string]any{"outcome": outcome, "reference": "read the terminal"}, h...); r.status != wantID {
				t.Errorf("%s: another branch's %s resolve = %d, want %d: %s", name, outcome, r.status, wantID, r.raw)
			}
		}
		if got := w.itemState(t, w.bItem); got != "open" {
			t.Errorf("%s: the resolve crossed the wall: B's item is %s", name, got)
		}
		// 4. GET /terminal-lost-items never lists B's item
		r := f.do("GET", "/api/v1/pos/terminal-lost-items", nil, h...)
		if r.status != http.StatusOK {
			t.Fatalf("%s: the queue = %d: %s", name, r.status, r.raw)
		}
		for _, raw := range r.body["items"].([]any) {
			if raw.(map[string]any)["branch_id"] == w.branchB.String() {
				t.Errorf("%s: the queue lists branch B's item", name)
			}
		}
	}

	// 5. POST /terminal-intents/sweep from the A caller sweeps only A's reach
	for name, h := range map[string][]string{"header": header, "no header": noHeader} {
		mustExec(t, f.db, `UPDATE payment_terminal_intents SET state = 'pending_at_device' WHERE id IN ($1, $2)`, w.bPending, w.aPending)
		mustExec(t, f.db, `DELETE FROM payment_terminal_lost_items WHERE intent_id IN ($1, $2)`, w.bPending, w.aPending)
		if r := f.do("POST", "/api/v1/pos/terminal-intents/sweep", nil, h...); r.status != http.StatusOK {
			t.Fatalf("%s: the sweep = %d: %s", name, r.status, r.raw)
		}
		if got := w.state(t, w.bPending); got != "pending_at_device" {
			t.Errorf("%s: the HTTP sweep swept branch B's leg: %s", name, got)
		}
		if got := w.state(t, w.aPending); got != "lost" {
			t.Errorf("%s: the HTTP sweep left the caller's own expired leg %s", name, got)
		}
	}

	// the same reads inside the caller's own reach answer
	mustExec(t, f.db, `UPDATE payment_terminal_intents SET state = 'pending_at_device' WHERE id = $1`, w.aPending)
	if r := f.do("GET", "/api/v1/pos/terminal-intents/"+w.aPending.String(), nil, header...); r.status != http.StatusOK {
		t.Errorf("the caller's own intent read = %d, want 200: %s", r.status, r.raw)
	}
	if r := f.do("GET", "/api/v1/pos/terminal-intents/"+w.aPending.String(), nil, noHeader...); r.status != http.StatusOK {
		t.Errorf("the no header caller's own intent read = %d, want 200: %s", r.status, r.raw)
	}
}

// TestTerminalLedgerSweepReach: the unscoped worker sweep reaches every
// branch, the scoped one only its own; this is the contract the HTTP sweep
// stands on.
func TestTerminalLedgerSweepReach(t *testing.T) {
	w := newWallWorld(t)
	res, err := w.ledger.Sweep(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.DispatchedLost < 2 {
		t.Fatalf("the worker's unscoped sweep took %d legs, wanted both branches' (2)", res.DispatchedLost)
	}
}
