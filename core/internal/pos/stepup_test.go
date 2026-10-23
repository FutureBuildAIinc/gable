// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The manager step up at the terminal (CP-04 slice 3, with CP-08's pickup
// and sign off): a cashier asks, the manager picks themselves from the
// roster and types their counter PIN, and the one act spends the single use
// approval the PIN mints.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/staff"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// seedManager puts one roster row on the staff table with a counter PIN and
// answers its id, name and staff number.
func seedManager(t *testing.T, f *fixture, role, pin string) (string, string, string) {
	t.Helper()
	return seedManagerAs(t, f, uuid.NewString(), role, pin)
}

// seedManagerAs puts a roster row on the staff table under an explicit id
// (a subject a sale or drawer may already name), with a counter PIN.
func seedManagerAs(t *testing.T, f *fixture, id, role, pin string) (string, string, string) {
	t.Helper()
	name := "Manager " + id[:8]
	staffNo := "STF-" + id[:8]
	hash, err := staff.HashCounterPIN(pin)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `INSERT INTO staff (id, email, full_name, staff_no, role, active, counter_pin_hash, counter_pin_set_at)
		VALUES ($1::uuid, $2, $3, $4, $5, TRUE, $6, NOW())`,
		id, "mgr-"+id[:8]+"@example.com", name, staffNo, role, hash)
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM pos_step_up_approvals WHERE manager_staff_id = $1::uuid`, id)
		mustExec(t, f.db, `DELETE FROM staff WHERE id = $1::uuid`, id)
	})
	return id, name, staffNo
}

// openTillBy opens the fixture register's drawer as the cashier subject
// given (the seed's own open helper picks its own subject).
func (f *fixture) openTillBy(sub string, floatCents int64) string {
	f.t.Helper()
	f.ensureTillClosed()
	r := f.do("POST", "/api/v1/pos/till/open",
		map[string]any{"register_id": f.register, "opening_float_cents": floatCents},
		"X-Test-Role", "cashier", "X-Test-Sub", sub)
	if r.status != http.StatusCreated {
		f.t.Fatalf("open till as %s = %d: %s", sub, r.status, r.raw)
	}
	return str(f.t, r.body, "id")
}

// ensureTillClosed closes the register's open drawer, if any, so a test can
// open its own.
func (f *fixture) ensureTillClosed() {
	f.t.Helper()
	var id string
	var expected int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id::text, COALESCE(expected_by_method->>'CASH','0')::bigint FROM till_sessions
		 WHERE register_id = $1 AND status = 'OPEN' ORDER BY opened_at DESC LIMIT 1`, f.register).Scan(&id, &expected); err != nil {
		return
	}
	f.do("POST", "/api/v1/pos/till/"+id+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": expected}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// stepUp asks a manager's approval at the terminal, as the caller given.
func (f *fixture) stepUp(act, manager, pin, target string, amount any, headers ...string) resp {
	f.t.Helper()
	body := map[string]any{"act": act, "manager": manager, "pin": pin, "target_id": target}
	if amount != nil {
		body["amount_cents"] = amount
	}
	return f.do("POST", "/api/v1/pos/step-up", body, headers...)
}

// tokenOf reads the token of a minted approval.
func tokenOf(t *testing.T, r resp) string {
	t.Helper()
	if r.status != http.StatusCreated {
		t.Fatalf("step up = %d: %s", r.status, r.raw)
	}
	return str(t, r.body, "token")
}

// RULE: a cashier at the terminal gets a manager's approval with the
// manager's own PIN, and the override records the manager by name.
func TestCP04_StepUpApprovesOverrideByPIN(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	cashier := uuid.NewString()
	sale := f.startSaleBy(cashier, &f.customerID)
	_, mgrName, staffNo := seedManager(t, f, "admin", "7361")

	// the cashier is no manager: the claims path alone refuses them
	if r := f.override(sale, "account", "cashier tries", 100, "cashier", cashier); r.status != http.StatusForbidden {
		t.Fatalf("a cashier approving by claims alone = %d, want 403: %s", r.status, r.raw)
	}
	// the step up: the manager picked by staff number, the PIN typed at the
	// terminal, the cashier's own session asking
	r := f.stepUp("override", staffNo, "7361", sale, nil, "X-Test-Role", "cashier", "X-Test-Sub", cashier)
	if r.status != http.StatusCreated {
		t.Fatalf("step up = %d: %s", r.status, r.raw)
	}
	if got := str(t, r.body, "manager_name"); got != mgrName {
		t.Errorf("manager_name = %q, want %q", got, mgrName)
	}
	token := str(t, r.body, "token")
	ov := f.do("POST", "/api/v1/pos/transactions/"+sale+"/overrides",
		map[string]any{"kind": "account", "reason": "the manager agreed", "cap_cents": 100, "approval_token": token},
		"X-Test-Role", "cashier", "X-Test-Sub", cashier)
	if ov.status != http.StatusCreated {
		t.Fatalf("override by PIN = %d: %s", ov.status, ov.raw)
	}
	if got := str(t, ov.body, "approved_by"); got != mgrName {
		t.Errorf("approved_by = %q, want the manager's name %q", got, mgrName)
	}
	// the audit row names the manager
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.override.approved'
		AND entity_id = $1::uuid AND user_id = $2`, sale, mgrName); got != 1 {
		t.Errorf("%d override audit rows by the manager's name, want 1", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.step_up.approved' AND entity_id = $1::uuid`, sale); got != 1 {
		t.Errorf("%d step up audit rows, want 1", got)
	}
}

// RULE: five wrong PINs in a row lock the manager's PIN; the answer never
// says which part was wrong, and a locked PIN stays locked until an admin
// unlocks it.
func TestCP04_StepUpWrongPINLocksAfterFive(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sale := f.startSaleBy(uuid.NewString(), &f.customerID)
	mgrID, _, staffNo := seedManager(t, f, "admin", "5581")
	for i := 0; i < 5; i++ {
		r := f.stepUp("override", staffNo, "0000", sale, nil)
		if r.status != http.StatusForbidden {
			t.Fatalf("wrong PIN %d = %d, want 403: %s", i, r.status, r.raw)
		}
		if got := r.body["error"].(map[string]any)["message"]; got != "That PIN is not right" {
			t.Errorf("wrong PIN message = %v, want the plain refusal", got)
		}
	}
	// even the right PIN is refused now: the lock stands
	r := f.stepUp("override", staffNo, "5581", sale, nil)
	if r.status != http.StatusForbidden {
		t.Fatalf("the locked PIN's right answer = %d, want 403: %s", r.status, r.raw)
	}
	if got := r.body["error"].(map[string]any)["message"]; got != "This manager's PIN is locked: an admin can unlock it" {
		t.Errorf("locked message = %v", got)
	}
	var locked bool
	var failures int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT counter_pin_locked, counter_pin_failures FROM staff WHERE id = $1::uuid`, mgrID).Scan(&locked, &failures); err != nil {
		t.Fatal(err)
	}
	if !locked || failures != 5 {
		t.Errorf("locked=%v failures=%d, want true 5", locked, failures)
	}
	// an unknown manager answers the same plain refusal, never which part
	if r := f.stepUp("override", "NO-SUCH-MGR", "5581", sale, nil); r.status != http.StatusForbidden ||
		r.body["error"].(map[string]any)["message"] != "That PIN is not right" {
		t.Errorf("an unknown manager = %d: %s, want the plain refusal", r.status, r.raw)
	}
	// an admin unlocks on the staff screen and the PIN works again
	mustExec(t, f.db, `UPDATE staff SET counter_pin_locked = FALSE, counter_pin_failures = 0 WHERE id = $1::uuid`, mgrID)
	if r := f.stepUp("override", staffNo, "5581", sale, nil); r.status != http.StatusCreated {
		t.Errorf("after the unlock = %d: %s", r.status, r.raw)
	}
}

// RULE: the manager cannot approve their own sale.
func TestCP04_StepUpRefusesCashierApprovingOwnSale(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	mgrID, _, staffNo := seedManager(t, f, "admin", "8142")
	// the manager's own staff id is the sale's cashier: a subject may be the
	// roster row's id, its staff number or its email
	sale := f.startSaleBy(mgrID, &f.customerID)
	r := f.stepUp("override", staffNo, "8142", sale, nil)
	if r.status != http.StatusForbidden {
		t.Fatalf("own sale = %d, want 403: %s", r.status, r.raw)
	}
	if got := r.body["error"].(map[string]any)["message"]; got != "A manager cannot approve their own sale" {
		t.Errorf("own sale message = %v", got)
	}
	// another manager is free to
	_, _, otherNo := seedManager(t, f, "admin", "6039")
	if r := f.stepUp("override", otherNo, "6039", sale, nil); r.status != http.StatusCreated {
		t.Errorf("another manager = %d: %s", r.status, r.raw)
	}
}

// RULE: the act's approving roles bind the step up: an override needs an
// admin or owner, a sign off needs finance or owner, a pickup is taken by
// any of the movement route's own roles.
func TestCP04_StepUpRoleRequired(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sale := f.startSaleBy(uuid.NewString(), &f.customerID)
	_, _, noRole := seedManager(t, f, "cashier", "3927")
	if r := f.stepUp("override", noRole, "3927", sale, nil); r.status != http.StatusForbidden {
		t.Errorf("a cashier stepping up for an override = %d, want 403: %s", r.status, r.raw)
	}
	// the picker's list holds only the roles the act admits
	r := f.do("GET", "/api/v1/pos/step-up/managers?act=override", nil)
	if r.status != http.StatusOK {
		t.Fatalf("managers = %d: %s", r.status, r.raw)
	}
	for _, item := range r.body["items"].([]any) {
		role := item.(map[string]any)["role"]
		if role != "admin" && role != "owner" {
			t.Errorf("the override picker lists role %v", role)
		}
	}
}

// RULE: one approval, one use, the right act and target only, unexpired.
func TestCP04_ApprovalSingleUseRightActRightTarget(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	cashier := uuid.NewString()
	sale := f.startSaleBy(cashier, &f.customerID)
	_, mgrName, staffNo := seedManager(t, f, "admin", "6294")
	token := tokenOf(t, f.stepUp("override", staffNo, "6294", sale, nil))
	post := func(saleID, tok string) resp {
		return f.do("POST", "/api/v1/pos/transactions/"+saleID+"/overrides",
			map[string]any{"kind": "account", "reason": "agreed", "cap_cents": 100, "approval_token": tok},
			"X-Test-Role", "cashier", "X-Test-Sub", cashier)
	}
	// a replay is refused
	if r := post(sale, token); r.status != http.StatusCreated {
		t.Fatalf("first use = %d: %s", r.status, r.raw)
	}
	if r := post(sale, token); r.status != http.StatusConflict {
		t.Errorf("replay = %d, want 409: %s", r.status, r.raw)
	}
	// another sale's target is refused
	other := f.startSaleBy(cashier, &f.customerID)
	token2 := tokenOf(t, f.stepUp("override", staffNo, "6294", other, nil))
	if r := post(sale, token2); r.status != http.StatusConflict {
		t.Errorf("another sale's approval = %d, want 409: %s", r.status, r.raw)
	}
	// an expired approval is refused
	token3 := tokenOf(t, f.stepUp("override", staffNo, "6294", other, nil))
	sum := sha256.Sum256([]byte(token3))
	mustExec(t, f.db, `UPDATE pos_step_up_approvals SET expires_at = NOW() - INTERVAL '1 minute'
		WHERE token_hash = $1`, hex.EncodeToString(sum[:]))
	if r := post(other, token3); r.status != http.StatusConflict {
		t.Errorf("expired approval = %d, want 409: %s", r.status, r.raw)
	}
	// the wrong act is refused: a sign off cannot spend a pickup approval
	if r := f.setThreshold(300); r.status != http.StatusOK {
		t.Fatalf("set threshold = %d: %s", r.status, r.raw)
	}
	cashier2 := uuid.NewString()
	sessionID := f.openTillBy(cashier2, 10000)
	if r := f.closeAs(sessionID, 9699, uuid.NewString()); r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	pickupToken := tokenOf(t, f.stepUp("pickup", staffNo, "6294", sessionID, 500))
	if r := f.do("POST", "/api/v1/pos/till/"+sessionID+"/signoff",
		map[string]any{"reason": "x", "approval_token": pickupToken}); r.status != http.StatusConflict {
		t.Errorf("the wrong act = %d, want 409: %s", r.status, r.raw)
	}
	_ = mgrName
}

// RULE (CP-08): a pickup's taker is a manager who stepped up with their PIN;
// the approval binds the session and the amount, and the row names the taker.
// The drawer's own cashier cannot be the taker, by PIN or otherwise.
func TestCP08_PickupTakerByPIN(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	cashier := uuid.NewString()
	sessionID := f.openTillBy(cashier, 5000)
	// the drawer's own cashier is on the roster with a PIN: still no taker
	seedManagerAs(t, f, cashier, "owner", "2846")
	if r := f.stepUp("pickup", "STF-"+cashier[:8], "2846", sessionID, 1000); r.status != http.StatusForbidden {
		t.Fatalf("the drawer's cashier taking the pickup = %d, want 403: %s", r.status, r.raw)
	}
	_, mgrName, staffNo := seedManager(t, f, "owner", "1739")
	token := tokenOf(t, f.stepUp("pickup", staffNo, "1739", sessionID, 1000))
	r := f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 1000, "approval_token": token}, cashier)
	if r.status != http.StatusCreated {
		t.Fatalf("pickup by PIN = %d: %s", r.status, r.raw)
	}
	if got := str(t, r.body, "taken_by"); got != mgrName {
		t.Errorf("taken_by = %q, want the manager's name %q", got, mgrName)
	}
	// the approval binds the amount: 2000 on a 1000 approval is refused
	token2 := tokenOf(t, f.stepUp("pickup", staffNo, "1739", sessionID, 1000))
	if r := f.movement(sessionID, map[string]any{"kind": "pickup", "amount_cents": 2000, "approval_token": token2}, cashier); r.status != http.StatusConflict {
		t.Errorf("a pickup of a different amount = %d, want 409: %s", r.status, r.raw)
	}
}

// RULE (CP-08): a held variance is signed off by a manager's PIN; the
// closer and the cashier are refused.
func TestCP08_SignoffByPIN(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	if r := f.setThreshold(300); r.status != http.StatusOK {
		t.Fatalf("set threshold = %d: %s", r.status, r.raw)
	}
	cashier := uuid.NewString()
	closer := uuid.NewString()
	sessionID := f.openTillBy(cashier, 10000)
	r := f.closeAs(sessionID, 9699, closer)
	if r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	_, mgrName, staffNo := seedManager(t, f, "finance", "4517")
	// the closer cannot sign off their own count by PIN
	_, _, closerNo := seedManagerAs(t, f, closer, "finance", "9028")
	if r := f.stepUp("signoff", closerNo, "9028", sessionID, nil); r.status != http.StatusForbidden {
		t.Fatalf("the closer signing off = %d, want 403: %s", r.status, r.raw)
	}
	// the cashier cannot either
	_, _, cashierNo := seedManagerAs(t, f, cashier, "finance", "3381")
	if r := f.stepUp("signoff", cashierNo, "3381", sessionID, nil); r.status != http.StatusForbidden {
		t.Fatalf("the cashier signing off = %d, want 403: %s", r.status, r.raw)
	}
	// a finance manager signs it off by PIN, named in the row
	token := tokenOf(t, f.stepUp("signoff", staffNo, "4517", sessionID, nil))
	r = f.do("POST", "/api/v1/pos/till/"+sessionID+"/signoff",
		map[string]any{"reason": "counted twice", "approval_token": token},
		"X-Test-Role", "cashier", "X-Test-Sub", cashier)
	if r.status != http.StatusOK {
		t.Fatalf("sign off by PIN = %d: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_sessions WHERE id = $1::uuid
		AND status = 'CLOSED' AND signoff_by = $2`, sessionID, mgrName); got != 1 {
		t.Errorf("the closed session does not name the manager %q", mgrName)
	}
	// a plain cashier call with neither claims of the role nor a token is refused
	session2 := f.openTillBy(cashier, 10000)
	if r := f.closeAs(session2, 9699, closer); r.status != http.StatusOK {
		t.Fatalf("close 2 = %d: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/pos/till/"+session2+"/signoff",
		map[string]any{"reason": "tries alone"}, "X-Test-Role", "cashier", "X-Test-Sub", cashier); r.status != http.StatusForbidden {
		t.Errorf("a cashier signing off with no approval = %d, want 403: %s", r.status, r.raw)
	}
}

// RULE (the lane rule on transactions): three acts race to spend one
// approval at pool size 4; exactly one wins.
func TestCP04_ThreeActsRacingOneApprovalExactlyOneWins(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	cashier := uuid.NewString()
	sale := f.startSaleBy(cashier, &f.customerID)
	_, _, staffNo := seedManager(t, f, "admin", "5053")
	token := tokenOf(t, f.stepUp("override", staffNo, "5053", sale, nil))
	statuses := make([]int, 3)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := f.do("POST", "/api/v1/pos/transactions/"+sale+"/overrides",
				map[string]any{"kind": "account", "reason": "agreed", "cap_cents": 100, "approval_token": token},
				"X-Test-Role", "cashier", "X-Test-Sub", cashier)
			statuses[i] = r.status
		}(i)
	}
	wg.Wait()
	won := 0
	for _, st := range statuses {
		switch st {
		case http.StatusCreated:
			won++
		case http.StatusConflict:
		default:
			t.Errorf("statuses = %v, want only 201 and 409", statuses)
		}
	}
	if won != 1 {
		t.Errorf("statuses = %v, want exactly one winner", statuses)
	}
}
