// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The sweep read of PR 178 (SWEEP-PR178-OPUS): the blind count must be
// complete and never net across methods, the open report hides the expected
// drawer from a cashier, and a pickup's second person is a signed in taker
// who confirmed under their own login.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// closeOf closes a session counting the methods given, as a cashier.
func (f *fixture) closeOf(sessionID string, counted map[string]any) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": counted},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// RULE (P1-1): the drawer is counted blind and whole. Cash must be counted,
// checks must be counted when checks were tendered, a card count is refused
// when no card tenders were collected, and a shortage on one drawer method
// is never netted away by an overage on another: any method's variance past
// the threshold holds the close for a manager.
func TestSweepPR178CountRequiresExpectedDrawerMethodsAndDoesNotNetAcrossMethods(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	mustExec(t, f.db, `INSERT INTO till_variance_policy (branch_id, threshold_cents) VALUES ($1, 500)
		ON CONFLICT (branch_id) DO UPDATE SET threshold_cents = EXCLUDED.threshold_cents`, f.branchID)
	sessionID := f.openTill(20000)
	// a sale tendered by check, so the session expects CASH 20000 and CHECK 599
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	checkTender := f.cashFor("1")
	checkTender["method"] = "check"
	if r := f.completeSale(saleID, checkTender); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}

	for name, tc := range map[string]struct {
		counted map[string]any
		field   string
	}{
		"no count at all":            {map[string]any{}, "counted_by_method.cash"},
		"cash left out of the count": {map[string]any{"check": 599}, "counted_by_method.cash"},
		"checks left out":            {map[string]any{"cash": 20000}, "counted_by_method.check"},
		"a card no one tendered":     {map[string]any{"cash": 10000, "check": 599, "card": 10000}, "counted_by_method.card"},
	} {
		r := f.closeOf(sessionID, tc.counted)
		if r.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, r.status, r.raw)
			continue
		}
		if _, _, fields := errorOf(t, r); len(fields) != 1 || fields[0] != tc.field {
			t.Errorf("%s fields = %v, want [%s]", name, fields, tc.field)
		}
	}
	if got := f.scalar(`SELECT status FROM till_sessions WHERE id = $1`, sessionID); got != "OPEN" {
		t.Fatalf("a refused count closed the drawer: status %v", got)
	}

	// a 10000 short cash count beside a 10000 over check count does not close
	// clean: each method's own variance past the threshold holds the close for
	// a manager, who reads the counted cash against the expected cash
	r := f.closeOf(sessionID, map[string]any{"cash": 10000, "check": 10599})
	if r.status != http.StatusOK {
		t.Fatalf("the offsetting count = %d: %s", r.status, r.raw)
	}
	session := r.body["session"].(map[string]any)
	if session["status"] != "pending_signoff" {
		t.Errorf("status = %v, want pending_signoff", session["status"])
	}
	counted := session["counted_by_method"].(map[string]any)
	expected := session["expected_by_method"].(map[string]any)
	if got := num(t, counted, "CASH"); got != 10000 {
		t.Errorf("counted CASH = %d, want 10000", got)
	}
	if got := num(t, expected, "CASH"); got != 20000 {
		t.Errorf("expected CASH = %d, want 20000", got)
	}
}

// RULE (P2-1): while the drawer is open a cashier reads the live report
// without the expected figures, so the close stays blind; a manager reads
// them in full.
func TestSweepPR178OpenTillReportRedactsExpectedFiguresForCashier(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(12345)

	r := f.do("GET", "/api/v1/pos/till/"+sessionID+"/report", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("cashier report = %d: %s", r.status, r.raw)
	}
	if exp, ok := r.body["expected_by_method"].(map[string]any); !ok || len(exp) != 0 {
		t.Errorf("cashier expected_by_method = %v, want it empty while the drawer is open", r.body["expected_by_method"])
	}

	r = f.do("GET", "/api/v1/pos/till/"+sessionID+"/report", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("finance report = %d: %s", r.status, r.raw)
	}
	exp, ok := r.body["expected_by_method"].(map[string]any)
	if !ok {
		t.Fatalf("finance expected_by_method = %v, want the figures", r.body["expected_by_method"])
	}
	if got := num(t, exp, "CASH"); got != 12345 {
		t.Errorf("finance expected CASH = %d, want 12345", got)
	}
}

// RULE (P2-2): a pickup needs the taker's own login. The cashier alone, naming
// anyone, is refused and nothing is written or posted; the taker confirms
// under their own subject (202, nothing moves yet), and only then does the
// cashier's record of that confirmed pickup land (201).
func TestSweepPR178PickupRequiresAuthenticatedSecondPerson(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(5000)
	cashier := mustUUID(t)
	manager := mustUUID(t)

	// the cashier alone names a taker no login confirmed
	r := f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": mustUUID(t)}, cashier)
	if r.status != http.StatusConflict {
		t.Fatalf("pickup by the cashier alone = %d, want 409: %s", r.status, r.raw)
	}
	if _, blockers, _ := errorOf(t, r); len(blockers) != 1 || blockers[0] != "pickup_needs_second_person" {
		t.Errorf("blockers = %v, want pickup_needs_second_person", blockers)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1`, sessionID); got != 0 {
		t.Errorf("%d movements written by the refused pickup, want none", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM gl_journal_entries e JOIN gl_journal_lines l ON l.journal_entry_id = e.id
		JOIN gl_accounts a ON a.id = l.account_id WHERE a.code = '1015' AND e.memo LIKE '%' || $1 || '%'`, sessionID); got != 0 {
		t.Errorf("%d pickup entries posted by the refused pickup, want none", got)
	}

	// the taker confirms under their own login: nothing moves yet
	r = f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": manager}, manager)
	if r.status != http.StatusAccepted {
		t.Fatalf("the taker's confirmation = %d, want 202: %s", r.status, r.raw)
	}
	if str(t, r.body, "status") != "pending_pickup_confirmation" || str(t, r.body, "taken_by") != manager {
		t.Errorf("confirmation body = %s", r.raw)
	}
	if got := num(t, r.body, "amount_cents"); got != 1000 {
		t.Errorf("confirmation amount = %d, want 1000", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1`, sessionID); got != 0 {
		t.Errorf("%d movements written by the confirmation, want none", got)
	}

	// the cashier records the confirmed pickup
	r = f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": manager}, cashier)
	if r.status != http.StatusCreated {
		t.Fatalf("the confirmed pickup = %d, want 201: %s", r.status, r.raw)
	}
	if got := str(t, r.body, "taken_by"); got != manager {
		t.Errorf("taken_by = %s, want the taker's own subject %s", got, manager)
	}
	if got := str(t, r.body, "actor"); got != cashier {
		t.Errorf("actor = %s, want the cashier %s", got, cashier)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1`, sessionID); got != 1 {
		t.Errorf("%d movements, want the one recorded pickup", got)
	}

	// an amount no taker confirmed is still refused
	r = f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 700, "taken_by": mustUUID(t)}, cashier)
	if r.status != http.StatusConflict {
		t.Errorf("an unconfirmed amount = %d, want 409: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1`, sessionID); got != 1 {
		t.Errorf("%d movements after the refused amount, want 1", got)
	}
}
