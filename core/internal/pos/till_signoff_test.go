// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-08: a blind close whose over or short passes the branch's variance
// threshold waits for a manager. The manager is a different person from the
// closer and the cashier, signs off with a reason, and the variance posts in
// the sign off's transaction. Inside the threshold the close posts as before.

import (
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// closeAs counts the drawer's cash as the cashier sub and answers the close.
func (f *fixture) closeAs(sessionID string, cash int64, sub string) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": cash}},
		"X-Test-Role", "cashier", "X-Test-Sub", sub)
}

// signOff releases a counted drawer as a manager of the role and sub.
func (f *fixture) signOff(sessionID, reason, role, sub string) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/till/"+sessionID+"/signoff", map[string]any{"reason": reason},
		"X-Test-Role", role, "X-Test-Sub", sub)
}

// setThreshold sets the fixture branch's own variance threshold.
func (f *fixture) setThreshold(cents any) resp {
	f.t.Helper()
	return f.do("PUT", "/api/v1/pos/till/policy", map[string]any{"branch_id": f.branchID.String(), "variance_threshold_cents": cents},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(f.t))
}

func sessionOf(t *testing.T, r resp) map[string]any {
	t.Helper()
	s, ok := r.body["session"].(map[string]any)
	if !ok {
		t.Fatalf("no session in %s", r.raw)
	}
	return s
}

// RULE: inside the threshold (and at it) the close posts the variance as
// before; the session records who closed it and the threshold in force.
func TestACloseWithinTheThresholdPostsAsBefore(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	if r := f.setThreshold(300); r.status != http.StatusOK {
		t.Fatalf("set threshold = %d: %s", r.status, r.raw)
	}
	closer := mustUUID(t)
	sessionID := f.openTill(10000)
	r := f.closeAs(sessionID, 9700, closer) // short exactly 300: at the threshold
	if r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	session := sessionOf(t, r)
	if got := session["status"]; got != "closed" {
		t.Errorf("status = %v, want closed (a variance at the threshold is not over it)", got)
	}
	if got := num(t, session, "over_short_cents"); got != -300 {
		t.Errorf("over_short_cents = %d, want -300", got)
	}
	if got := session["closed_by"]; got != closer {
		t.Errorf("closed_by = %v, want %s", got, closer)
	}
	if got := num(t, session, "variance_threshold_cents"); got != 300 {
		t.Errorf("variance_threshold_cents = %d, want 300", got)
	}
	if session["gl_entry_id"] == nil {
		t.Errorf("no ledger entry for the short: %v", session)
	}
	if _, legs := f.legsFor(sessionID); legs["5030"].debit != 300 || legs["1010"].credit != 300 {
		t.Errorf("legs = %v, want 5030 debited and 1010 credited 300", legs)
	}
	if session["sign_off"] != nil {
		t.Errorf("sign_off = %v on a close nobody had to sign off", session["sign_off"])
	}
	if z := f.do("GET", "/api/v1/pos/till/"+sessionID+"/zreport", nil); z.status != http.StatusOK {
		t.Errorf("zreport = %d, want the frozen snapshot", z.status)
	}
}

// RULE: a close over the threshold, short or over, is not closed: it waits in
// pending sign off, posts nothing, takes no sale and no movement, and has no
// Z report yet.
func TestACloseOverTheThresholdWaitsForSignOff(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	if r := f.setThreshold(300); r.status != http.StatusOK {
		t.Fatalf("set threshold = %d: %s", r.status, r.raw)
	}
	for name, c := range map[string]struct {
		counted, overShort int64
	}{"short": {9699, -301}, "over": {10301, 301}} {
		sessionID := f.openTill(10000)
		r := f.closeAs(sessionID, c.counted, mustUUID(t))
		if r.status != http.StatusOK {
			t.Fatalf("%s: close = %d: %s", name, r.status, r.raw)
		}
		session := sessionOf(t, r)
		if got := session["status"]; got != "pending_signoff" {
			t.Errorf("%s: status = %v, want pending_signoff", name, got)
		}
		if got := num(t, session, "over_short_cents"); got != c.overShort {
			t.Errorf("%s: over_short_cents = %d, want %d", name, got, c.overShort)
		}
		if session["gl_entry_id"] != nil {
			t.Errorf("%s: a ledger entry before the sign off: %v", name, session["gl_entry_id"])
		}
		if entries, _ := f.legsFor(sessionID); entries != 0 {
			t.Errorf("%s: %d entries posted before the sign off, want none", name, entries)
		}
		if z := f.do("GET", "/api/v1/pos/till/"+sessionID+"/zreport", nil); z.status != http.StatusNotFound {
			t.Errorf("%s: zreport = %d, want 404 until the sign off freezes it", name, z.status)
		}
		// the drawer is out of use: no movement, no sale
		if m := f.movement(sessionID, map[string]any{"kind": "safe_drop", "amount_cents": 100}, mustUUID(t)); m.status != http.StatusConflict {
			t.Errorf("%s: movement on a pending session = %d, want 409", name, m.status)
		}
		if s := f.startSaleNoTill(); s.status != http.StatusConflict {
			t.Errorf("%s: sale at a register whose drawer waits = %d, want 409 till_not_open: %s", name, s.status, s.raw)
		}
		if again := f.closeAs(sessionID, 10000, mustUUID(t)); again.status != http.StatusConflict {
			t.Errorf("%s: a second close of the pending session = %d, want 409", name, again.status)
		}
	}
}

// RULE: the queue lists the counted drawers waiting for a manager, in the
// managers' roles only, and a signed off one leaves it.
func TestTheSignOffQueueListsPendingDrawers(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setThreshold(100)
	sessionID := f.openTill(10000)
	f.closeAs(sessionID, 9000, mustUUID(t))
	queue := func(role string) resp {
		return f.do("GET", "/api/v1/pos/till/signoffs?branch_id="+f.branchID.String(), nil, "X-Test-Role", role, "X-Test-Sub", mustUUID(t))
	}
	if r := queue("cashier"); r.status != http.StatusForbidden {
		t.Errorf("the queue for a cashier = %d, want 403", r.status)
	}
	r := queue("finance")
	if r.status != http.StatusOK {
		t.Fatalf("queue = %d: %s", r.status, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != sessionID {
		t.Fatalf("queue = %s, want the one pending session", r.raw)
	}
	if s := f.signOff(sessionID, "counted twice", "finance", mustUUID(t)); s.status != http.StatusOK {
		t.Fatalf("sign off = %d: %s", s.status, s.raw)
	}
	if got := len(queue("owner").body["items"].([]any)); got != 0 {
		t.Errorf("%d sessions in the queue after the sign off, want none", got)
	}
	if r := f.do("GET", "/api/v1/pos/till/signoffs?bogus=1", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusBadRequest {
		t.Errorf("an unknown query name = %d, want 400", r.status)
	}
}

// RULE: the closer cannot sign off their own close, nor can the drawer's
// cashier; a different manager with a reason does, the over and short posts
// in that act, and the report names who and why.
func TestTheSignOffPostsTheVarianceAndNamesTheManager(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setThreshold(100)
	closer := mustUUID(t)
	sessionID := f.openTill(10000)
	if r := f.closeAs(sessionID, 9500, closer); r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	var opener string
	if err := f.db.Pool.QueryRow(contextB(), `SELECT cashier_id::text FROM till_sessions WHERE id = $1`, sessionID).Scan(&opener); err != nil {
		t.Fatal(err)
	}
	for name, sub := range map[string]string{"the closer": closer, "the closer under capitals": upper(closer), "the drawer's cashier": opener} {
		r := f.signOff(sessionID, "my own count", "finance", sub)
		if r.status != http.StatusConflict {
			t.Errorf("sign off by %s = %d, want 409: %s", name, r.status, r.raw)
			continue
		}
		if _, blockers, _ := errorOf(t, r); len(blockers) != 1 || blockers[0] != "signoff_same_person" {
			t.Errorf("sign off by %s: blockers = %v, want signoff_same_person", name, blockers)
		}
	}
	manager := mustUUID(t)
	if r := f.signOff(sessionID, "   ", "finance", manager); r.status != http.StatusBadRequest {
		t.Errorf("a blank reason = %d, want 400", r.status)
	}
	if r := f.signOff(sessionID, "a reason", "cashier", manager); r.status != http.StatusForbidden {
		t.Errorf("sign off in the cashier role = %d, want 403", r.status)
	}
	if entries, _ := f.legsFor(sessionID); entries != 0 {
		t.Fatalf("%d entries posted by refused sign offs", entries)
	}
	r := f.signOff(sessionID, "recounted, till roll confirms", "finance", manager)
	if r.status != http.StatusOK {
		t.Fatalf("sign off = %d: %s", r.status, r.raw)
	}
	session := sessionOf(t, r)
	if got := session["status"]; got != "closed" {
		t.Errorf("status = %v, want closed", got)
	}
	signOff, ok := session["sign_off"].(map[string]any)
	if !ok || signOff["signed_off_by"] != manager || signOff["reason"] != "recounted, till roll confirms" || signOff["signed_off_at"] == nil {
		t.Errorf("sign_off = %v, want the manager, the reason and the time", session["sign_off"])
	}
	if session["gl_entry_id"] == nil {
		t.Errorf("the sign off linked no entry: %v", session)
	}
	if entries, legs := f.legsFor(sessionID); entries != 1 || legs["5030"].debit != 500 || legs["1010"].credit != 500 {
		t.Errorf("entries %d legs %v, want one entry debiting 5030 and crediting 1010 500", entries, legs)
	}
	// the till report shows who signed off and why, and the Z report is frozen
	rep := f.reportOf(sessionID)
	if so, ok := rep["session"].(map[string]any)["sign_off"].(map[string]any); !ok || so["signed_off_by"] != manager {
		t.Errorf("the till report's sign_off = %v", rep["session"].(map[string]any)["sign_off"])
	}
	z := f.do("GET", "/api/v1/pos/till/"+sessionID+"/zreport", nil)
	if z.status != http.StatusOK {
		t.Fatalf("zreport = %d: %s", z.status, z.raw)
	}
	zs := z.body["payload"].(map[string]any)["session"].(map[string]any)
	if zs["status"] != "closed" || zs["sign_off"] == nil {
		t.Errorf("the Z snapshot's session = %v, want closed with its sign off", zs)
	}
	if num(t, z.body, "over_short_cents") != -500 {
		t.Errorf("zreport over_short_cents = %v, want -500", z.body["over_short_cents"])
	}
	// once signed off it cannot be signed off again, and the audit row names the manager
	if again := f.signOff(sessionID, "again", "finance", mustUUID(t)); again.status != http.StatusConflict {
		t.Errorf("a second sign off = %d, want 409", again.status)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.till.signed_off' AND entity_id = $1::uuid AND user_id = $2`, sessionID, manager); got != 1 {
		t.Errorf("%d sign off audit rows by the manager, want 1", got)
	}
}

// RULE: the threshold is per branch over a database wide default; null clears
// the branch's own.
func TestTheVarianceThresholdIsPerBranchOverADefault(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	read := func() map[string]any {
		r := f.do("GET", "/api/v1/pos/till/policy?branch_id="+f.branchID.String(), nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
		if r.status != http.StatusOK {
			t.Fatalf("policy = %d: %s", r.status, r.raw)
		}
		return r.body
	}
	p := read()
	if p["source"] != "default" || num(t, p, "variance_threshold_cents") != 500 {
		t.Errorf("policy with no override = %v, want the default 500", p)
	}
	if r := f.setThreshold(250); r.status != http.StatusOK {
		t.Fatalf("set = %d: %s", r.status, r.raw)
	}
	p = read()
	if p["source"] != "branch" || num(t, p, "variance_threshold_cents") != 250 || num(t, p, "default_threshold_cents") != 500 {
		t.Errorf("policy with an override = %v, want the branch's 250 over the default 500", p)
	}
	if r := f.setThreshold(nil); r.status != http.StatusOK {
		t.Fatalf("clear = %d: %s", r.status, r.raw)
	}
	if p = read(); p["source"] != "default" || p["branch_threshold_cents"] != nil {
		t.Errorf("policy after clearing = %v, want the default again", p)
	}
	for name, body := range map[string]map[string]any{
		"a negative threshold": {"branch_id": f.branchID.String(), "variance_threshold_cents": -1},
		"no threshold key":     {"branch_id": f.branchID.String()},
		"no branch":            {"variance_threshold_cents": 100},
	} {
		if r := f.do("PUT", "/api/v1/pos/till/policy", body, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, r.status, r.raw)
		}
	}
	if r := f.do("PUT", "/api/v1/pos/till/policy", map[string]any{"branch_id": f.branchID.String(), "variance_threshold_cents": 5},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusForbidden {
		t.Errorf("a cashier setting the policy = %d, want 403", r.status)
	}
}

// RULE (concurrency, pool size 4, three contenders): three managers racing to
// sign off one pending drawer end with exactly one sign off and one entry.
func TestRacingSignOffsPostTheVarianceOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	f.setThreshold(100)
	sessionID := f.openTill(10000)
	f.closeAs(sessionID, 9000, mustUUID(t))
	var wg sync.WaitGroup
	statuses := make([]int, 3)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.signOff(sessionID, "racing", "finance", mustUUID(t)).status
		}(i)
	}
	wg.Wait()
	wins, conflicts := 0, 0
	for _, st := range statuses {
		switch st {
		case http.StatusOK:
			wins++
		case http.StatusConflict:
			conflicts++
		}
	}
	if wins != 1 || conflicts != 2 {
		t.Errorf("statuses = %v, want one 200 and two 409", statuses)
	}
	if entries, legs := f.legsFor(sessionID); entries != 1 || legs["5030"].debit != 1000 {
		t.Errorf("entries %d legs %v, want the one 1000 short entry", entries, legs)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_z_reports WHERE till_session_id = $1`, sessionID); got != 1 {
		t.Errorf("%d Z reports, want 1", got)
	}
}

// RULE (ADR 0005 section 11): the sign off, its entry, its Z snapshot, its
// audit row and its event are one transaction: a failing event write leaves
// the drawer waiting and nothing posted.
func TestASignOffIsOneTransaction(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setThreshold(100)
	sessionID := f.openTill(10000)
	f.closeAs(sessionID, 9000, mustUUID(t))
	f.events.setFail("till.closed")
	r := f.signOff(sessionID, "will roll back", "finance", mustUUID(t))
	if r.status == http.StatusOK {
		t.Fatal("the sign off committed with a failing event write")
	}
	if entries, _ := f.legsFor(sessionID); entries != 0 {
		t.Errorf("%d entries survived the rollback", entries)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_sessions WHERE id = $1 AND status = 'PENDING_SIGNOFF' AND signoff_by IS NULL`, sessionID); got != 1 {
		t.Errorf("the session is not back waiting for sign off")
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_z_reports WHERE till_session_id = $1`, sessionID); got != 0 {
		t.Errorf("a Z report survived the rollback")
	}
	f.events.setFail("")
	if r := f.signOff(sessionID, "retry", "finance", mustUUID(t)); r.status != http.StatusOK {
		t.Errorf("the retry = %d: %s", r.status, r.raw)
	}
}

// RULE: a variance signed off into a closed fiscal period is the 409
// period_closed, and the drawer stays waiting.
func TestASignOffIntoAClosedPeriodIsTheBlocker(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setThreshold(100)
	sessionID := f.openTill(10000)
	f.closeAs(sessionID, 9000, mustUUID(t))
	f.closeFiscalPeriod()
	r := f.signOff(sessionID, "into a closed period", "finance", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("sign off into a closed period = %d, want 409: %s", r.status, r.raw)
	}
	if _, blockers, _ := errorOf(t, r); len(blockers) != 1 || blockers[0] != "period_closed" {
		t.Errorf("blockers = %v, want period_closed", blockers)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_sessions WHERE id = $1 AND status = 'PENDING_SIGNOFF'`, sessionID); got != 1 {
		t.Errorf("the session left pending_signoff on a refused sign off")
	}
}

// RULE: the policy of a branch that does not exist is a 404, read or written.
func TestThePolicyOfAnUnknownBranchIsAMissingRecord(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ghost := mustUUID(t)
	if r := f.do("GET", "/api/v1/pos/till/policy?branch_id="+ghost, nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusNotFound {
		t.Errorf("policy read of an unknown branch = %d, want 404: %s", r.status, r.raw)
	}
	if r := f.do("PUT", "/api/v1/pos/till/policy", map[string]any{"branch_id": ghost, "variance_threshold_cents": 100},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusNotFound {
		t.Errorf("policy write of an unknown branch = %d, want 404: %s", r.status, r.raw)
	}
}

// RULE (concurrency, pool size 4, three contenders): three writers of one
// branch's threshold all answer, and the row ends as exactly one of theirs.
func TestRacingPolicyWritesEndOnOneValue(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	auditRows := func() int64 {
		return countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.till.variance_policy' AND entity_id = $1::uuid`, f.branchID.String())
	}
	before := auditRows()
	var wg sync.WaitGroup
	statuses := make([]int, 3)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.setThreshold(100 * (i + 1)).status
		}(i)
	}
	wg.Wait()
	for i, st := range statuses {
		if st != http.StatusOK {
			t.Errorf("writer %d answered %d, want 200", i, st)
		}
	}
	var n, v int
	if err := f.db.Pool.QueryRow(contextB(), `SELECT count(*), COALESCE(max(threshold_cents), 0) FROM till_variance_policy WHERE branch_id = $1`, f.branchID).Scan(&n, &v); err != nil {
		t.Fatal(err)
	}
	if n != 1 || (v != 100 && v != 200 && v != 300) {
		t.Errorf("%d rows with threshold %d, want one row holding 100, 200 or 300", n, v)
	}
	if got := auditRows() - before; got != 3 {
		t.Errorf("%d audit rows for three writes, want 3", got)
	}
}
