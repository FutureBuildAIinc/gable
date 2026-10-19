// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The sweep read of PR 178 (SWEEP-PR178-OPUS): the blind count must be
// complete and never net across methods, the open report hides the expected
// drawer from a cashier, and a pickup's second person is a signed in taker
// who confirmed under their own login.

import (
	"net/http"
	"sync"
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
	// the offsetting count records the cash shortage, never a clean net: a
	// check overage does not cancel a cash shortage (CP-08)
	if got := num(t, session, "over_short_cents"); got != -10000 {
		t.Errorf("over_short_cents = %d, want -10000", got)
	}
	// the manager's sign off posts that variance: the shortage reaches the
	// ledger instead of a drawer the books say was right
	r = f.signOff(sessionID, "cash short beside checks over", "finance", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("the sign off = %d: %s", r.status, r.raw)
	}
	session = sessionOf(t, r)
	if got := num(t, session, "over_short_cents"); got != -10000 {
		t.Errorf("signed off over_short_cents = %d, want -10000", got)
	}
	byMethod, ok := session["over_short_by_method"].(map[string]any)
	if !ok {
		t.Fatalf("no over_short_by_method in %s", r.raw)
	}
	if got := num(t, byMethod, "CASH"); got != -10000 {
		t.Errorf("signed off over_short_by_method CASH = %d, want -10000", got)
	}
	if got := num(t, byMethod, "CHECK"); got != 10000 {
		t.Errorf("signed off over_short_by_method CHECK = %d, want 10000", got)
	}
	if session["gl_entry_id"] == nil {
		t.Errorf("no ledger entry posted for the signed off variance: %s", r.raw)
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

// RULE (P1-1, race): one taker confirmation backs exactly one recorded
// pickup. Concurrent records of the same confirmed amount cannot consume the
// same approval twice: exactly one lands (201) and the rest are refused
// (409), and only one pickup movement exists.
func TestSweepPR178OneConfirmationBacksExactlyOnePickupUnderRace(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(5000)
	cashier := mustUUID(t)
	taker := mustUUID(t)

	if r := f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": taker}, taker); r.status != http.StatusAccepted {
		t.Fatalf("the taker's confirmation = %d: %s", r.status, r.raw)
	}

	const contenders = 3
	statuses := make([]int, contenders)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			statuses[i] = f.movement(sessionID,
				map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": taker}, cashier).status
		}(i)
	}
	close(start)
	wg.Wait()

	created, refused := 0, 0
	for _, s := range statuses {
		switch s {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			refused++
		default:
			t.Errorf("a contender answered %d, want 201 or 409", s)
		}
	}
	if created != 1 || refused != contenders-1 {
		t.Errorf("contender statuses = %v, want exactly one 201 and %d 409", statuses, contenders-1)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1 AND kind = 'pickup'`, sessionID); got != 1 {
		t.Errorf("%d pickup movements recorded, want 1", got)
	}
}

// RULE (P2-1): while the drawer is open a caller whose roles are cashier
// only (a token with roles ["cashier"] and no single role) reads no money
// figure from which the expected drawer can be rebuilt: not the expected
// figures, not the float, not the tendered cash or the movement totals.
func TestSweepPR178OpenTillFiguresBlindForRolesOnlyCashier(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(10000)
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, f.cashFor("1")); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	f.mustMovement(sessionID, map[string]any{"kind": "safe_drop", "amount_cents": 700}, mustUUID(t))

	rolesCashier := []string{"X-Test-Roles", "cashier", "X-Test-Sub", mustUUID(t)}
	r := f.do("GET", "/api/v1/pos/till/"+sessionID+"/report", nil, rolesCashier...)
	if r.status != http.StatusOK {
		t.Fatalf("cashier report = %d: %s", r.status, r.raw)
	}
	if m, ok := r.body["expected_by_method"].(map[string]any); !ok || len(m) != 0 {
		t.Errorf("cashier expected_by_method = %v, want it empty while the drawer is open", r.body["expected_by_method"])
	}
	session, ok := r.body["session"].(map[string]any)
	if !ok {
		t.Fatalf("no session in %s", r.raw)
	}
	if m, ok := session["expected_by_method"].(map[string]any); !ok || len(m) != 0 {
		t.Errorf("cashier session expected_by_method = %v, want it empty", session["expected_by_method"])
	}
	if got := num(t, session, "opening_float_cents"); got != 0 {
		t.Errorf("cashier opening_float_cents = %d, want 0", got)
	}
	if m, ok := r.body["tendered_by_method"].(map[string]any); !ok || len(m) != 0 {
		t.Errorf("cashier tendered_by_method = %v, want it empty", r.body["tendered_by_method"])
	}
	if m, ok := r.body["movement_totals_by_kind"].(map[string]any); !ok || len(m) != 0 {
		t.Errorf("cashier movement_totals_by_kind = %v, want it empty", r.body["movement_totals_by_kind"])
	}
	if m, ok := r.body["movements"].([]any); !ok || len(m) != 0 {
		t.Errorf("cashier movements = %v, want it empty", r.body["movements"])
	}
	if got := num(t, r.body, "sales_total_cents"); got != 0 {
		t.Errorf("cashier sales_total_cents = %d, want 0", got)
	}
	if got := num(t, r.body, "change_cents"); got != 0 {
		t.Errorf("cashier change_cents = %d, want 0", got)
	}

	r = f.do("GET", "/api/v1/pos/till/current?register_id="+f.register, nil, rolesCashier...)
	if r.status != http.StatusOK {
		t.Fatalf("cashier current = %d: %s", r.status, r.raw)
	}
	if m, ok := r.body["expected_by_method"].(map[string]any); !ok || len(m) != 0 {
		t.Errorf("cashier current expected_by_method = %v, want it empty", r.body["expected_by_method"])
	}
	if got := num(t, r.body, "opening_float_cents"); got != 0 {
		t.Errorf("cashier current opening_float_cents = %d, want 0", got)
	}

	// a manager still reads the figures in full
	r = f.do("GET", "/api/v1/pos/till/"+sessionID+"/report", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("finance report = %d: %s", r.status, r.raw)
	}
	if got := num(t, sessionOf(t, r), "opening_float_cents"); got != 10000 {
		t.Errorf("finance session opening_float_cents = %d, want 10000", got)
	}
}
