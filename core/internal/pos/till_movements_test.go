// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-08: drops, pickups, paid in and paid out against an open till. Each moves
// the drawer's expected cash, posts a balanced entry inside its own act, and
// is refused once the session is no longer open.

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
)

// movement posts a movement as a cashier and answers the response.
func (f *fixture) movement(sessionID string, body map[string]any, sub string) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/till/"+sessionID+"/movements", body, "X-Test-Role", "cashier", "X-Test-Sub", sub)
}

// mustMovement posts a movement that must be accepted and answers its id.
func (f *fixture) mustMovement(sessionID string, body map[string]any, sub string) string {
	f.t.Helper()
	r := f.movement(sessionID, body, sub)
	if r.status != http.StatusCreated {
		f.t.Fatalf("movement %v = %d: %s", body, r.status, r.raw)
	}
	return str(f.t, r.body, "id")
}

// reportOf reads the X report of a session.
func (f *fixture) reportOf(sessionID string) map[string]any {
	f.t.Helper()
	r := f.do("GET", "/api/v1/pos/till/"+sessionID+"/report", nil)
	if r.status != http.StatusOK {
		f.t.Fatalf("till report = %d: %s", r.status, r.raw)
	}
	return r.body
}

func expectedCash(t *testing.T, report map[string]any) int64 {
	t.Helper()
	exp, ok := report["expected_by_method"].(map[string]any)
	if !ok {
		t.Fatalf("no expected_by_method in %v", report)
	}
	return num(t, exp, "CASH")
}

// RULE: each movement kind changes the expected cash (drops, pickups and paid
// out lower it, paid in raises it), posts a balanced entry in its own act, and
// shows in the till report with its totals.
func TestEachMovementKindChangesExpectedCashAndPostsABalancedEntry(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(10000)
	f.saleOf("1", tender("cash", 599))
	cashier := mustUUID(t)
	manager := mustUUID(t)

	if got := expectedCash(t, f.reportOf(sessionID)); got != 10599 {
		t.Fatalf("expected cash before any movement = %d, want 10599", got)
	}
	drop := f.mustMovement(sessionID, map[string]any{"kind": "safe_drop", "amount_cents": 2000}, cashier)
	if got := expectedCash(t, f.reportOf(sessionID)); got != 8599 {
		t.Errorf("expected cash after a drop = %d, want 8599", got)
	}
	// a pickup is the taker's own confirmation plus the cashier's record
	if r := f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": manager}, manager); r.status != http.StatusAccepted {
		t.Fatalf("the taker's confirmation = %d: %s", r.status, r.raw)
	}
	pickup := f.mustMovement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": manager}, cashier)
	if got := expectedCash(t, f.reportOf(sessionID)); got != 7599 {
		t.Errorf("expected cash after a pickup = %d, want 7599", got)
	}
	paidOut := f.mustMovement(sessionID, map[string]any{"kind": "paid_out", "amount_cents": 300, "reason_code": "supplies"}, cashier)
	if got := expectedCash(t, f.reportOf(sessionID)); got != 7299 {
		t.Errorf("expected cash after a paid out = %d, want 7299", got)
	}
	paidIn := f.mustMovement(sessionID, map[string]any{"kind": "paid_in", "amount_cents": 500, "reason_code": "fee_received"}, cashier)
	report := f.reportOf(sessionID)
	if got := expectedCash(t, report); got != 7799 {
		t.Errorf("expected cash after a paid in = %d, want 7799", got)
	}
	totals := report["movement_totals_by_kind"].(map[string]any)
	for kind, want := range map[string]int64{"safe_drop": 2000, "pickup": 1000, "paid_out": 300, "paid_in": 500} {
		if got := num(t, totals, kind); got != want {
			t.Errorf("movement total %s = %d, want %d", kind, got, want)
		}
	}
	if got := len(report["movements"].([]any)); got != 4 {
		t.Errorf("the report lists %d movements, want 4", got)
	}

	// each posts one balanced entry, drawer cash against the right account
	for _, c := range []struct {
		name, id      string
		debit, credit map[string]int64
	}{
		{"drop", drop, map[string]int64{"1015": 2000}, map[string]int64{"1010": 2000}},
		{"pickup", pickup, map[string]int64{"1015": 1000}, map[string]int64{"1010": 1000}},
		{"paid out", paidOut, map[string]int64{"5020": 300}, map[string]int64{"1010": 300}},
		{"paid in", paidIn, map[string]int64{"1010": 500}, map[string]int64{"4030": 500}},
	} {
		entries, legs := f.legsFor(c.id)
		if entries != 1 {
			t.Errorf("%s posted %d entries, want 1", c.name, entries)
		}
		var debit, credit int64
		for code, l := range legs {
			debit += l.debit
			credit += l.credit
			if l.debit != c.debit[code] || l.credit != c.credit[code] {
				t.Errorf("%s: account %s debit %d credit %d, want debit %d credit %d", c.name, code, l.debit, l.credit, c.debit[code], c.credit[code])
			}
		}
		if debit != credit || debit == 0 {
			t.Errorf("%s entry debit %d credit %d, want balanced and not empty", c.name, debit, credit)
		}
		var glID *string
		if err := f.db.Pool.QueryRow(contextB(), `SELECT gl_entry_id::text FROM till_movements WHERE id = $1`, c.id).Scan(&glID); err != nil || glID == nil {
			t.Errorf("%s: the movement does not name its entry (%v)", c.name, err)
		}
	}
	// the audit row and the event ride the act
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.till.movement' AND entity_id = $1::uuid`, drop); got != 1 {
		t.Errorf("%d audit rows for the drop, want 1", got)
	}
	if got := eventTypes(t, f.db, "till_movement", drop); len(got) != 1 || got[0] != "till.movement_recorded" {
		t.Errorf("drop events = %v, want till.movement_recorded", got)
	}

	// the Z report shows each movement, and the close counts them
	r := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": 7799}},
		"X-Test-Role", "cashier", "X-Test-Sub", cashier)
	if r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body["session"].(map[string]any), "over_short_cents"); got != 0 {
		t.Errorf("over_short after counting the movements = %d, want 0", got)
	}
	z := f.do("GET", "/api/v1/pos/till/"+sessionID+"/zreport", nil)
	if z.status != http.StatusOK {
		t.Fatalf("zreport = %d: %s", z.status, z.raw)
	}
	if got := len(z.body["payload"].(map[string]any)["movements"].([]any)); got != 4 {
		t.Errorf("the Z report lists %d movements, want 4", got)
	}
}

// RULE: a pickup is cash taken by a second signed-in person; the cashier
// alone, naming anyone, is refused, and nothing is written or posted until
// the taker has confirmed under their own login.
func TestAPickupByTheCashierAloneIsRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(5000)
	cashier := mustUUID(t)
	var opener string
	if err := f.db.Pool.QueryRow(contextB(), `SELECT cashier_id::text FROM till_sessions WHERE id = $1`, sessionID).Scan(&opener); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]map[string]any{
		"no second person":      {"kind": "pickup", "amount_cents": 1000},
		"blank second person":   {"kind": "pickup", "amount_cents": 1000, "taken_by": "  "},
		"the acting cashier":    {"kind": "pickup", "amount_cents": 1000, "taken_by": opener},
		"a drop naming a taker": {"kind": "safe_drop", "amount_cents": 1000, "taken_by": mustUUID(t)},
	} {
		r := f.movement(sessionID, body, opener)
		if r.status < 400 || r.status >= 500 {
			t.Errorf("%s = %d, want a refusal: %s", name, r.status, r.raw)
		}
	}
	// the same person under another spelling is still the cashier
	r := f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": upper(opener)}, opener)
	if r.status != http.StatusConflict {
		t.Fatalf("the cashier under capitals = %d, want 409: %s", r.status, r.raw)
	}
	if _, blockers, _ := errorOf(t, r); len(blockers) != 1 || blockers[0] != "pickup_needs_second_person" {
		t.Errorf("blockers = %v, want pickup_needs_second_person", blockers)
	}
	// the session's own cashier, acting through another login, is no second person
	if r := f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": opener}, mustUUID(t)); r.status != http.StatusConflict {
		t.Errorf("the session's cashier as the taker = %d, want 409: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1`, sessionID); got != 0 {
		t.Errorf("%d movements written by refused requests, want none", got)
	}
	// the cashier alone naming a taker no login confirmed is refused the same
	// way, and nothing is written
	if r := f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": mustUUID(t)}, cashier); r.status != http.StatusConflict {
		t.Errorf("pickup by an unconfirmed taker = %d, want 409: %s", r.status, r.raw)
	}
	// a second person confirms under their own login, and the cashier records
	manager := mustUUID(t)
	if r := f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": manager}, manager); r.status != http.StatusAccepted {
		t.Errorf("the taker's confirmation = %d, want 202: %s", r.status, r.raw)
	}
	if r := f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "taken_by": manager}, cashier); r.status != http.StatusCreated {
		t.Errorf("pickup by a confirmed second person = %d: %s", r.status, r.raw)
	}
}

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'f' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}

// RULE: the reason code is from the closed lowercase list, a paid kind names
// one, the code other carries a note, and the amount is positive.
func TestMovementReasonCodesAreAClosedList(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(5000)
	cashier := mustUUID(t)
	for name, body := range map[string]map[string]any{
		"paid out with no reason":     {"kind": "paid_out", "amount_cents": 100},
		"paid out with an unknown":    {"kind": "paid_out", "amount_cents": 100, "reason_code": "lunch"},
		"paid out with capitals":      {"kind": "paid_out", "amount_cents": 100, "reason_code": "Supplies"},
		"paid out with an income one": {"kind": "paid_out", "amount_cents": 100, "reason_code": "fee_received"},
		"paid in with an expense one": {"kind": "paid_in", "amount_cents": 100, "reason_code": "supplies"},
		"other with no note":          {"kind": "paid_out", "amount_cents": 100, "reason_code": "other"},
		"other with a blank note":     {"kind": "paid_in", "amount_cents": 100, "reason_code": "other", "note": "   "},
		"a drop with a reason":        {"kind": "safe_drop", "amount_cents": 100, "reason_code": "supplies"},
		"an unknown kind":             {"kind": "lodgement", "amount_cents": 100},
		"a zero amount":               {"kind": "safe_drop", "amount_cents": 0},
		"a negative amount":           {"kind": "safe_drop", "amount_cents": -5},
		"a fractional amount":         {"kind": "safe_drop", "amount_cents": 1.5},
	} {
		if r := f.movement(sessionID, body, cashier); r.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, r.status, r.raw)
		}
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1`, sessionID); got != 0 {
		t.Errorf("%d movements written by refused requests, want none", got)
	}
	// other with a note is accepted, on the expense side for paid out and the
	// income side for paid in
	out := f.mustMovement(sessionID, map[string]any{"kind": "paid_out", "amount_cents": 100, "reason_code": "other", "note": "locksmith"}, cashier)
	in := f.mustMovement(sessionID, map[string]any{"kind": "paid_in", "amount_cents": 250, "reason_code": "other", "note": "found cash"}, cashier)
	if _, legs := f.legsFor(out); legs["5020"].debit != 100 {
		t.Errorf("paid out other legs = %v, want 5020 debited 100", legs)
	}
	if _, legs := f.legsFor(in); legs["4090"].credit != 250 {
		t.Errorf("paid in other legs = %v, want 4090 credited 250", legs)
	}
}

// RULE: a movement is refused on a session that is not open, and on one that
// does not exist, and writes nothing.
func TestMovementsOnAClosedSessionAreRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(5000)
	cashier := mustUUID(t)
	if r := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": 5000}},
		"X-Test-Role", "cashier", "X-Test-Sub", cashier); r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	r := f.movement(sessionID, map[string]any{"kind": "safe_drop", "amount_cents": 1000}, cashier)
	if r.status != http.StatusConflict {
		t.Fatalf("movement on a closed session = %d, want 409: %s", r.status, r.raw)
	}
	if _, blockers, _ := errorOf(t, r); len(blockers) != 1 || blockers[0] != "session_closed" {
		t.Errorf("blockers = %v, want session_closed", blockers)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1`, sessionID); got != 0 {
		t.Errorf("%d movements written on a closed session", got)
	}
	if r := f.movement("00000000-0000-4000-8000-0000000000aa", map[string]any{"kind": "safe_drop", "amount_cents": 1000}, cashier); r.status != http.StatusNotFound {
		t.Errorf("movement on a missing session = %d, want 404: %s", r.status, r.raw)
	}
	if r := f.movement("not-a-uuid", map[string]any{"kind": "safe_drop", "amount_cents": 1000}, cashier); r.status != http.StatusBadRequest {
		t.Errorf("movement on a malformed id = %d, want 400: %s", r.status, r.raw)
	}
}

// RULE (ADR 0005 section 11): the movement, its entry, its audit row and its
// event are one transaction: a failing event write leaves no row and no entry.
func TestAMovementIsOneTransaction(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(5000)
	f.events.setFail("till.movement_recorded")
	r := f.movement(sessionID, map[string]any{"kind": "safe_drop", "amount_cents": 1000}, mustUUID(t))
	if r.status == http.StatusCreated {
		t.Fatal("the movement committed with a failing event write")
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1`, sessionID); got != 0 {
		t.Errorf("%d movements after the rollback, want none", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM gl_journal_entries e JOIN gl_journal_lines l ON l.journal_entry_id = e.id
		JOIN gl_accounts a ON a.id = l.account_id WHERE a.code = '1015' AND e.memo LIKE '%' || $1 || '%'`, sessionID); got != 0 {
		t.Errorf("%d entries survived the rollback, want none", got)
	}
	if got := expectedCash(t, f.reportOf(sessionID)); got != 5000 {
		t.Errorf("expected cash = %d after the rollback, want the 5000 float", got)
	}
}

// RULE: the movements list is a cursor page, oldest first, and a session the
// wall hides lists as missing.
func TestTheMovementsListPagesByCursor(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(5000)
	cashier := mustUUID(t)
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, f.mustMovement(sessionID, map[string]any{"kind": "safe_drop", "amount_cents": 100 + i}, cashier))
	}
	r := f.do("GET", "/api/v1/pos/till/"+sessionID+"/movements?limit=2", nil)
	if r.status != http.StatusOK {
		t.Fatalf("list = %d: %s", r.status, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"] != ids[0] || items[1].(map[string]any)["id"] != ids[1] {
		t.Fatalf("first page = %v, want the first two oldest first", items)
	}
	next, _ := r.body["next_cursor"].(string)
	if next == "" {
		t.Fatalf("no next_cursor on a page with a row after it: %s", r.raw)
	}
	r = f.do("GET", "/api/v1/pos/till/"+sessionID+"/movements?limit=2&cursor="+next, nil)
	items = r.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != ids[2] || r.body["next_cursor"] != nil {
		t.Errorf("second page = %s, want the last row and no cursor", r.raw)
	}
	if r := f.do("GET", "/api/v1/pos/till/"+sessionID+"/movements?cursor=garbage", nil); r.status != http.StatusBadRequest {
		t.Errorf("a malformed cursor = %d, want 400", r.status)
	}
}

// RULE (concurrency, pool size 4, three contenders): two movements and a close
// racing on one session end consistent. Whichever order they commit in, the
// close's expected cash equals the float plus the net tenders plus exactly the
// movements that committed; a movement behind the close is refused.
func TestTwoMovementsAndACloseRacingEndConsistent(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	for round := 0; round < 6; round++ {
		sessionID := f.openTill(10000)
		f.saleOf("1", tender("cash", 599))
		cashier := mustUUID(t)
		var wg sync.WaitGroup
		statuses := make([]int, 3)
		wg.Add(3)
		go func() {
			defer wg.Done()
			statuses[0] = f.movement(sessionID, map[string]any{"kind": "safe_drop", "amount_cents": 1000}, cashier).status
		}()
		go func() {
			defer wg.Done()
			statuses[1] = f.movement(sessionID, map[string]any{"kind": "paid_out", "amount_cents": 300, "reason_code": "postage"}, cashier).status
		}()
		go func() {
			defer wg.Done()
			statuses[2] = f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": 0}},
				"X-Test-Role", "cashier", "X-Test-Sub", cashier).status
		}()
		wg.Wait()
		if statuses[2] != http.StatusOK && statuses[2] != http.StatusConflict {
			t.Fatalf("round %d: the close answered %d", round, statuses[2])
		}
		for i, st := range statuses[:2] {
			if st != http.StatusCreated && st != http.StatusConflict {
				t.Fatalf("round %d: movement %d answered %d, want 201 or 409", round, i, st)
			}
		}
		var net, committed int64
		if err := f.db.Pool.QueryRow(contextB(), `SELECT COALESCE(SUM(CASE WHEN kind = 'paid_in' THEN amount_cents ELSE -amount_cents END), 0)::bigint,
			count(*) FROM till_movements WHERE till_session_id = $1`, sessionID).Scan(&net, &committed); err != nil {
			t.Fatal(err)
		}
		var expected int64
		var status string
		if err := f.db.Pool.QueryRow(contextB(), `SELECT status, COALESCE((expected_by_method->>'CASH')::bigint, -1) FROM till_sessions WHERE id = $1`,
			sessionID).Scan(&status, &expected); err != nil {
			t.Fatal(err)
		}
		if status == "OPEN" {
			t.Fatalf("round %d: the session is still open after the close answered %d", round, statuses[2])
		}
		if want := 10000 + 599 + net; expected != want {
			t.Errorf("round %d: expected cash %d, want float + tenders + the %d committed movements = %d", round, expected, committed, want)
		}
		// every movement that committed has its entry; a refused one has none
		if got := countOf(t, f.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1 AND gl_entry_id IS NULL`, sessionID); got != 0 {
			t.Errorf("round %d: %d committed movements without an entry", round, got)
		}
		_ = sessionID
	}
}

// RULE: a movement dated into a closed fiscal period is the 409 period_closed,
// never an internal error, and nothing is written. The same refusal stops the
// sign off's variance.
func TestAMovementIntoAClosedPeriodIsTheBlocker(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(5000)
	f.closeFiscalPeriod()

	r := f.movement(sessionID, map[string]any{"kind": "safe_drop", "amount_cents": 1000}, mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("movement into a closed period = %d, want 409: %s", r.status, r.raw)
	}
	if _, blockers, _ := errorOf(t, r); len(blockers) != 1 || blockers[0] != "period_closed" {
		t.Errorf("blockers = %v, want period_closed", blockers)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1`, sessionID); got != 0 {
		t.Errorf("%d movements written into a closed period", got)
	}
}

// closedDay is a fixed past day no seed period covers. closeFiscalPeriod dates
// this fixture's own ledger entries into it.
var closedDay = time.Date(2036, 8, 19, 0, 0, 0, 0, time.UTC)

// datedLedger posts this fixture's entries on one fixed day, whatever the
// business day is.
type datedLedger struct {
	pos.LedgerPoster
	day time.Time
}

func (l datedLedger) PostEntry(ctx context.Context, in gl.PostingInput) (*gl.JournalEntry, error) {
	in.EntryDate = l.day
	return l.LedgerPoster.PostEntry(ctx, in)
}

// closeFiscalPeriod closes a fixed past period and dates only this fixture's
// ledger entries into it: an entry the counter posts is then refused by the
// period trigger. Today's period, shared by every package's tests on the
// database, is never touched; the closed period is removed when the test ends.
func (f *fixture) closeFiscalPeriod() {
	f.t.Helper()
	id := mustUUID(f.t)
	mustExec(f.t, f.db, `INSERT INTO gl_fiscal_periods (id, name, start_date, end_date, status)
		VALUES ($1, 'closed for a test', $2::date, $2::date, 'CLOSED')`, id, closedDay.Format("2006-01-02"))
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM gl_fiscal_periods WHERE id = $1::uuid`, id)
	})
	f.service.WithLedger(datedLedger{LedgerPoster: f.ledger, day: closedDay})
}
