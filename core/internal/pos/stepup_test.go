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
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/staff"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// seedManager puts one roster row on the staff table with a counter PIN and
// answers its id, name and staff number.
func seedManager(t *testing.T, f *fixture, role, pin string) (string, string, string, string) {
	t.Helper()
	return seedManagerAs(t, f, uuid.NewString(), role, pin)
}

// seedManagerAs puts a roster row on the staff table under an explicit id
// (a subject a sale or drawer may already name), with a counter PIN. It
// answers the id, the name, the staff number and the email.
func seedManagerAs(t *testing.T, f *fixture, id, role, pin string) (string, string, string, string) {
	t.Helper()
	name := "Manager " + id[:8]
	staffNo := "STF-" + id[:8]
	email := "mgr-" + id[:8] + "@example.com"
	hash, err := staff.HashCounterPIN(pin)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `INSERT INTO staff (id, email, full_name, staff_no, role, active, counter_pin_hash, counter_pin_set_at)
		VALUES ($1::uuid, $2, $3, $4, $5, TRUE, $6, NOW())`,
		id, email, name, staffNo, role, hash)
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM pos_step_up_approvals WHERE manager_staff_id = $1::uuid`, id)
		mustExec(t, f.db, `DELETE FROM staff WHERE id = $1::uuid`, id)
	})
	return id, name, staffNo, email
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

// stepUp asks a manager's approval at the terminal, as the caller given. An
// override names the kind and the cap the manager approves.
func (f *fixture) stepUp(act, manager, pin, target string, amount any, headers ...string) resp {
	return f.stepUpKind(act, "", manager, pin, target, amount, headers...)
}

func (f *fixture) stepUpKind(act, kind, manager, pin, target string, amount any, headers ...string) resp {
	f.t.Helper()
	body := map[string]any{"act": act, "manager": manager, "pin": pin, "target_id": target}
	if kind != "" {
		body["kind"] = kind
	}
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
	mgrID, mgrName, staffNo, _ := seedManager(t, f, "admin", "7361")

	// the cashier is no manager: the claims path alone refuses them
	if r := f.override(sale, "account", "cashier tries", 100, "cashier", cashier); r.status != http.StatusForbidden {
		t.Fatalf("a cashier approving by claims alone = %d, want 403: %s", r.status, r.raw)
	}
	// the step up: the manager picked by staff number, the PIN typed at the
	// terminal, the cashier's own session asking
	r := f.stepUpKind("override", "account", staffNo, "7361", sale, 100, "X-Test-Role", "cashier", "X-Test-Sub", cashier)
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
	// the audit row names the manager: the staff id as its user (the subject
	// shaped column the users picker reads) and the name beside it
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.override.approved'
		AND entity_id = $1::uuid AND user_id = $2 AND changes->>'manager_name' = $3`, sale, mgrID, mgrName); got != 1 {
		t.Errorf("%d override audit rows by the manager's staff id and name, want 1", got)
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
	mgrID, _, staffNo, _ := seedManager(t, f, "admin", "5581")
	for i := 0; i < 5; i++ {
		r := f.stepUpKind("override", "account", staffNo, "0000", sale, 100)
		if r.status != http.StatusForbidden {
			t.Fatalf("wrong PIN %d = %d, want 403: %s", i, r.status, r.raw)
		}
		if got := r.body["error"].(map[string]any)["message"]; got != "That PIN is not right" {
			t.Errorf("wrong PIN message = %v, want the plain refusal", got)
		}
	}
	// even the right PIN is refused now: the lock stands
	r := f.stepUpKind("override", "account", staffNo, "5581", sale, 100)
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
	if r := f.stepUpKind("override", "account", "NO-SUCH-MGR", "5581", sale, 100); r.status != http.StatusForbidden ||
		r.body["error"].(map[string]any)["message"] != "That PIN is not right" {
		t.Errorf("an unknown manager = %d: %s, want the plain refusal", r.status, r.raw)
	}
	// an amount on an act that carries none is refused before anything is
	// minted: a minted approval must always be spendable
	sale2 := f.startSaleBy(uuid.NewString(), &f.customerID)
	if r := f.stepUp("signoff", staffNo, "5581", sale2, 100); r.status != http.StatusBadRequest {
		t.Errorf("a sign up with an amount = %d, want 400: %s", r.status, r.raw)
	}
	// an admin unlocks on the staff screen and the PIN works again
	mustExec(t, f.db, `UPDATE staff SET counter_pin_locked = FALSE, counter_pin_failures = 0 WHERE id = $1::uuid`, mgrID)
	if r := f.stepUpKind("override", "account", staffNo, "5581", sale, 100); r.status != http.StatusCreated {
		t.Errorf("after the unlock = %d: %s", r.status, r.raw)
	}
}

// RULE: the manager cannot approve their own sale, under real sign in (where
// the auth subject is a distinct id and the email links the caller to the
// roster row) or when the subject itself names the roster row.
func TestCP04_StepUpRefusesCashierApprovingOwnSale(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	mgrID, _, staffNo, mgrEmail := seedManager(t, f, "admin", "8142")
	// under real sign in the auth subject is NOT the staff row's id
	cashierSub := uuid.NewString()
	if cashierSub == mgrID {
		t.Fatal("subject collided with staff id")
	}
	f.ensureTill()
	startR := f.do("POST", "/api/v1/pos/transactions",
		map[string]any{"register_id": f.register, "cashier_id": cashierSub, "customer_id": f.customerID.String()},
		"X-Test-Role", "admin", "X-Test-Sub", cashierSub, "X-Test-Email", mgrEmail)
	if startR.status != http.StatusCreated {
		t.Fatalf("start sale = %d: %s", startR.status, startR.raw)
	}
	sale := str(t, startR.body, "id")

	// the claims path refuses the manager on their own sale
	if r := f.do("POST", "/api/v1/pos/transactions/"+sale+"/overrides",
		map[string]any{"kind": "account", "reason": "own sale", "cap_cents": 100},
		"X-Test-Role", "admin", "X-Test-Sub", cashierSub, "X-Test-Email", mgrEmail); r.status != http.StatusForbidden {
		t.Fatalf("claims path own sale = %d, want 403: %s", r.status, r.raw)
	}
	// the step up refuses the same manager when they are the caller at the terminal
	r := f.stepUpKind("override", "account", staffNo, "8142", sale, 100,
		"X-Test-Role", "admin", "X-Test-Sub", cashierSub, "X-Test-Email", mgrEmail)
	if r.status != http.StatusForbidden {
		t.Fatalf("own sale as caller = %d, want 403: %s", r.status, r.raw)
	}
	if got := r.body["error"].(map[string]any)["message"]; got != "A manager cannot approve their own sale" {
		t.Errorf("own sale message = %v", got)
	}
	// and even when another cashier is at the terminal, the sale's cashier is
	// resolved to the manager's staff id, so the manager still cannot approve
	// the sale they rang
	r = f.stepUpKind("override", "account", staffNo, "8142", sale, 100,
		"X-Test-Role", "cashier", "X-Test-Sub", uuid.NewString(), "X-Test-Email", "other-cashier@example.com")
	if r.status != http.StatusForbidden {
		t.Fatalf("own sale under another caller = %d, want 403: %s", r.status, r.raw)
	}
	if got := r.body["error"].(map[string]any)["message"]; got != "A manager cannot approve their own sale" {
		t.Errorf("own sale message = %v", got)
	}
	// another manager is free to approve it
	_, _, otherNo, _ := seedManager(t, f, "admin", "6039")
	if r := f.stepUpKind("override", "account", otherNo, "6039", sale, 100,
		"X-Test-Role", "admin", "X-Test-Sub", cashierSub, "X-Test-Email", mgrEmail); r.status != http.StatusCreated {
		t.Errorf("another manager = %d: %s", r.status, r.raw)
	}
}

// RULE: under real sign in the cashier's subject is an opaque id that never
// equals the roster row's; the caller's own email names the person all the
// same, and the manager who is the caller is the one person refused.
func TestCP04_StepUpRefusesTheCallerWhoIsTheManager(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	mgrID, _, staffNo, mgrEmail := seedManager(t, f, "admin", "1946")
	sale := f.startSaleBy(uuid.NewString(), &f.customerID)
	// the cashier's subject is nothing like the staff id, but the claims
	// carry the manager's email: the person at the terminal is the manager
	r := f.stepUpKind("override", "account", staffNo, "1946", sale, 100,
		"X-Test-Role", "cashier", "X-Test-Sub", uuid.NewString(), "X-Test-Email", mgrEmail)
	if r.status != http.StatusForbidden {
		t.Fatalf("the caller who is the manager = %d, want 403: %s", r.status, r.raw)
	}
	if got := r.body["error"].(map[string]any)["message"]; got != "A manager cannot approve their own sale" {
		t.Errorf("own sale message = %v", got)
	}
	_ = mgrID
}

// RULE: the act's approving roles bind the step up: an override needs an
// admin or owner, a sign off needs finance or owner, a pickup is taken by
// any of the movement route's own roles.
func TestCP04_StepUpRoleRequired(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sale := f.startSaleBy(uuid.NewString(), &f.customerID)
	_, _, noRole, _ := seedManager(t, f, "cashier", "3927")
	if r := f.stepUpKind("override", "account", noRole, "3927", sale, 100); r.status != http.StatusForbidden {
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
	_, mgrName, staffNo, _ := seedManager(t, f, "owner", "6294")
	token := tokenOf(t, f.stepUpKind("override", "account", staffNo, "6294", sale, 100))
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
	// another sale's approval is refused, on a fresh sale that holds no
	// override of its own (the sale's own rules must not be the refusal)
	other := f.startSaleBy(cashier, &f.customerID)
	third := f.startSaleBy(cashier, &f.customerID)
	token2 := tokenOf(t, f.stepUpKind("override", "account", staffNo, "6294", other, 100))
	if r := post(third, token2); r.status != http.StatusConflict ||
		r.body["error"].(map[string]any)["code"] != "step_up_spent" {
		t.Errorf("another sale's approval = %d: %s, want 409 step_up_spent", r.status, r.raw)
	}
	// an override approval binds the kind and the cap the manager saw:
	// spending a cap 100 account approval with cap 101 or as check is refused
	saleTerms := f.startSaleBy(cashier, &f.customerID)
	termsToken := tokenOf(t, f.stepUpKind("override", "account", staffNo, "6294", saleTerms, 100))
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleTerms+"/overrides",
		map[string]any{"kind": "account", "reason": "higher cap", "cap_cents": 101, "approval_token": termsToken},
		"X-Test-Role", "cashier", "X-Test-Sub", cashier); r.status != http.StatusConflict ||
		r.body["error"].(map[string]any)["code"] != "step_up_spent" {
		t.Errorf("cap 101 on cap 100 approval = %d: %s, want 409 step_up_spent", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleTerms+"/overrides",
		map[string]any{"kind": "check", "reason": "wrong kind", "cap_cents": 100, "approval_token": termsToken},
		"X-Test-Role", "cashier", "X-Test-Sub", cashier); r.status != http.StatusConflict ||
		r.body["error"].(map[string]any)["code"] != "step_up_spent" {
		t.Errorf("check on account approval = %d: %s, want 409 step_up_spent", r.status, r.raw)
	}
	if r := post(saleTerms, termsToken); r.status != http.StatusCreated {
		t.Errorf("matching kind and cap after refused mismatches = %d: %s, want 201", r.status, r.raw)
	}
	// an expired approval is refused
	token3 := tokenOf(t, f.stepUpKind("override", "account", staffNo, "6294", other, 100))
	sum := sha256.Sum256([]byte(token3))
	mustExec(t, f.db, `UPDATE pos_step_up_approvals SET expires_at = NOW() - INTERVAL '1 minute'
		WHERE token_hash = $1`, hex.EncodeToString(sum[:]))
	if r := post(other, token3); r.status != http.StatusConflict {
		t.Errorf("expired approval = %d, want 409: %s", r.status, r.raw)
	}
	// the act binding bites on its own: a sign off approval (no amount, no
	// kind) minted for this drawer is not spendable as a pickup of the same
	// drawer with no amount either, everything but the act equal
	if r := f.setThreshold(300); r.status != http.StatusOK {
		t.Fatalf("set threshold = %d: %s", r.status, r.raw)
	}
	cashier2 := uuid.NewString()
	sessionID := f.openTillBy(cashier2, 10000)
	if r := f.closeAs(sessionID, 9699, uuid.NewString()); r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	signoffToken := tokenOf(t, f.stepUp("signoff", staffNo, "6294", sessionID, nil))
	tokenSum := sha256.Sum256([]byte(signoffToken))
	stepUpRepo := pos.NewRepository(f.db)
	if _, spent, err := stepUpRepo.ConsumeStepUpApproval(context.Background(), "pickup", "", uuid.MustParse(sessionID), nil,
		hex.EncodeToString(tokenSum[:])); err != nil || spent {
		t.Errorf("a sign off approval spent as a pickup: spent=%v err=%v, want false nil (the act alone refuses it)", spent, err)
	}
	// and the same token under the right act works: the refusal above was
	// the act, nothing else
	if r := f.do("POST", "/api/v1/pos/till/"+sessionID+"/signoff",
		map[string]any{"reason": "counted twice", "approval_token": signoffToken}); r.status != http.StatusOK {
		t.Errorf("the right act on the same target = %d, want 200: %s", r.status, r.raw)
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
	_, mgrName, staffNo, _ := seedManager(t, f, "owner", "1739")
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
	_, mgrName, staffNo, _ := seedManager(t, f, "finance", "4517")
	// the closer cannot sign off their own count by PIN
	_, _, closerNo, _ := seedManagerAs(t, f, closer, "finance", "9028")
	if r := f.stepUp("signoff", closerNo, "9028", sessionID, nil); r.status != http.StatusForbidden {
		t.Fatalf("the closer signing off = %d, want 403: %s", r.status, r.raw)
	}
	// the cashier cannot either
	_, _, cashierNo, _ := seedManagerAs(t, f, cashier, "finance", "3381")
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
	_, _, staffNo, _ := seedManager(t, f, "admin", "5053")
	token := tokenOf(t, f.stepUpKind("override", "account", staffNo, "5053", sale, 100))
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

// RULE (the lane rule on transactions, and the lock's own): many wrong PINs
// at once count no more than the lock's five, the PIN ends locked, and no
// approval is minted on a PIN that locked while the ask was in flight.
func TestCP04_ConcurrentWrongPINsCountNoMoreThanTheLock(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	sale := f.startSaleBy(uuid.NewString(), &f.customerID)
	mgrID, _, staffNo, _ := seedManager(t, f, "admin", "6813")
	results := make([]resp, 20)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = f.stepUpKind("override", "account", staffNo, "0000", sale, 100)
		}(i)
	}
	wg.Wait()
	refused, lockedCount := 0, 0
	for i, r := range results {
		if r.status != http.StatusForbidden {
			t.Errorf("wrong try %d = %d, want 403", i, r.status)
			continue
		}
		switch r.body["error"].(map[string]any)["code"] {
		case "step_up_refused":
			refused++
		case "step_up_locked":
			lockedCount++
		}
	}
	if refused > 5 {
		t.Errorf("concurrent wrong tries verified %d PINs (step_up_refused), want at most the lock's 5 (locked=%d)", refused, lockedCount)
	}
	var failures int
	var locked bool
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT counter_pin_failures, counter_pin_locked FROM staff WHERE id = $1::uuid`, mgrID).Scan(&failures, &locked); err != nil {
		t.Fatal(err)
	}
	if failures > 5 {
		t.Errorf("concurrent wrong tries counted %d failures, want at most the lock's 5", failures)
	}
	if !locked {
		t.Error("the PIN did not end locked")
	}
	// and the locked PIN mints nothing
	if r := f.stepUpKind("override", "account", staffNo, "6813", sale, 100); r.status != http.StatusForbidden {
		t.Errorf("the right PIN on the locked row = %d, want 403: %s", r.status, r.raw)
	}
}

// RULE (the claims path stays): the variance pair's own guard admits every
// role the claims path allowed, beside the counter's own, so a signed in
// finance manager keeps the queue and the sign off and a cashier reaches
// them to spend a manager's step up approval.
func TestCP04_TheVariancePairKeepsFinanceAndAdmitsTheCounter(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	mux := http.NewServeMux()
	pos.NewHandler(f.service).RegisterRoutes(mux, nil, nil,
		middleware.RequireRole("admin", "owner", "cashier", "finance"))
	srv := httptest.NewServer(roleClaims(mux))
	t.Cleanup(srv.Close)
	do := func(role string) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/pos/till/signoffs", nil)
		req.Header.Set("X-Test-Role", role)
		req.Header.Set("X-Test-Sub", uuid.NewString())
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := do("finance"); got != http.StatusOK {
		t.Errorf("the queue for finance = %d, want 200 (the claims path keeps it)", got)
	}
	if got := do("owner"); got != http.StatusOK {
		t.Errorf("the queue for owner = %d, want 200", got)
	}
	if got := do("cashier"); got != http.StatusOK {
		t.Errorf("the queue for a cashier = %d, want 200 (the step up needs it)", got)
	}
	if got := do("sales"); got != http.StatusForbidden {
		t.Errorf("the queue for sales = %d, want 403", got)
	}
}
