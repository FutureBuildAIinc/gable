// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// Fix round 7 of the branch bound key pin: the r15 P2 and P3s, the r14 notes
// and the principal's item 57 rulings, every leg through the real serve chain.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// applyMigration108 runs the migration file's SQL on the fixture's database:
// the legacy rows a test plants after the migrations ran are the rows the
// migration backfills at upgrade, so the test applies the same statements.
func (f *keyPinFixture) applyMigration108(t *testing.T) {
	t.Helper()
	sql, err := os.ReadFile("../../../migrations/108_pos_branch_backfill.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("apply migration 108: %v", err)
	}
}

// r15reg seeds a pos_registers row with the location and branch given and
// removes it when the test ends.
func (f *keyPinFixture) r15reg(t *testing.T, name string, location, branch uuid.UUID) string {
	t.Helper()
	reg := "R15-" + name + "-" + uuid.NewString()[:8]
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO pos_registers (id, location_id, branch_id, name, is_active) VALUES ($1, $2, $3, $4, true)`,
		reg, location, branch, reg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM pos_registers WHERE id = $1`, reg)
	})
	return reg
}

// Fix round 7 (r15 P2-1): a till session written before the branch filter was
// added, on a register whose location names no branch, carried branch NULL and
// so vanished from every caller with a branch context: the drawer read as none
// and could be neither closed nor replaced. Migration 108 backfills those rows
// from their register, and the drawer is reachable and closable again, with
// the register's own branch selected in the desk.
func TestKeyBranchPin_Migration108LegacyTillSessionStaysReachable(t *testing.T) {
	f, _, _ := round5Fixture(t)
	ctx := context.Background()
	var def uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	reg := f.r15reg(t, "repro", uuid.New(), def) // location_id names no location
	sess := uuid.New()
	t.Cleanup(func() { _, _ = f.db.Pool.Exec(ctx, `DELETE FROM till_sessions WHERE id = $1`, sess) })
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO till_sessions (id, register_id, branch_id, cashier_id, status, opening_float)
		VALUES ($1, $2, NULL, $3, 'OPEN', 50)`, sess, reg, uuid.New()); err != nil {
		t.Fatal(err)
	}
	f.applyMigration108(t)
	hdr := map[string]string{"X-Branch-Id": def.String()}
	status, buf := f.userAs(t, "admin", "GET", "/api/v1/pos/till/current?register_id="+reg, "", hdr)
	if status != http.StatusOK || !strings.Contains(string(buf), sess.String()) {
		t.Errorf("current till with X-Branch-Id the register's branch: %d %s, want the open session", status, buf)
	}
	status, buf = f.userAs(t, "admin", "POST", "/api/v1/pos/till/"+sess.String()+"/close", `{"counted_by_method":{"cash":50}}`, hdr)
	if status != http.StatusOK {
		t.Errorf("closing the open drawer with X-Branch-Id the register's branch: %d %s, want 200", status, buf)
	}
}

// Fix round 7 (r15 P2-1), the Z report half: the legacy closed sessions' Z
// reports vanished with them from the branch scoped reads. The backfill brings
// the history back: the session's Z report reads by session and the list finds
// it under the register, for the branch the register belongs to.
func TestKeyBranchPin_Migration108LegacyZReportStaysReachable(t *testing.T) {
	f, _, _ := round5Fixture(t)
	ctx := context.Background()
	var def uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	reg := f.r15reg(t, "zrep", uuid.New(), def)
	sess, zr := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM till_z_reports WHERE id = $1`, zr)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM till_sessions WHERE id = $1`, sess)
	})
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO till_sessions (id, register_id, branch_id, cashier_id, status, opening_float, closed_at)
		 VALUES ($1, $2, NULL, $3, 'CLOSED', 50, NOW())`, sess, reg, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO till_z_reports (id, till_session_id, register_id, branch_id, over_short, payload)
		 VALUES ($1, $2, $3, NULL, 0, '{"legacy":true}')`, zr, sess, reg); err != nil {
		t.Fatal(err)
	}
	f.applyMigration108(t)
	hdr := map[string]string{"X-Branch-Id": def.String()}
	status, buf := f.userAs(t, "admin", "GET", "/api/v1/pos/till/"+sess.String()+"/zreport", "", hdr)
	if status != http.StatusOK || !strings.Contains(string(buf), zr.String()) {
		t.Errorf("the legacy Z report by session with X-Branch-Id the register's branch: %d %s, want the report", status, buf)
	}
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/pos/zreports?register_id="+reg, "", hdr)
	if status != http.StatusOK || !strings.Contains(string(buf), zr.String()) {
		t.Errorf("the legacy Z report list with X-Branch-Id the register's branch: %d %s, want the report listed", status, buf)
	}
}

// Fix round 7 (r15 P2-1), the return half: the legacy returns vanished from
// the branch scoped reads the same way. The backfill brings them back.
func TestKeyBranchPin_Migration108LegacyReturnStaysReachable(t *testing.T) {
	f, _, _ := round5Fixture(t)
	ctx := context.Background()
	var def uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	reg := f.r15reg(t, "ret", uuid.New(), def)
	ret := uuid.New()
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_return_lines WHERE return_id = $1`, ret)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_returns WHERE id = $1`, ret)
	})
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO pos_returns (id, register_id, branch_id, cashier_id, subtotal, tax_amount, total, refund_method, status, currency, number)
		 VALUES ($1, $2, NULL, $3, 10.11, 0.99, 11.10, 'CASH', 'COMPLETED', 'USD', $4)`, ret, reg, uuid.New(), "RTPIN7-"+ret.String()[:8]); err != nil {
		t.Fatal(err)
	}
	f.applyMigration108(t)
	hdr := map[string]string{"X-Branch-Id": def.String()}
	status, buf := f.userAs(t, "admin", "GET", "/api/v1/pos/returns/"+ret.String(), "", hdr)
	if status != http.StatusOK || !strings.Contains(string(buf), ret.String()) {
		t.Errorf("the legacy return with X-Branch-Id the register's branch: %d %s, want the return", status, buf)
	}
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/pos/returns?register_id="+reg, "", hdr)
	if status != http.StatusOK || !strings.Contains(string(buf), ret.String()) {
		t.Errorf("the legacy return list with X-Branch-Id the register's branch: %d %s, want the return listed", status, buf)
	}
}

// Fix round 7 (r15 P3-1): a user's sale on a register whose branch disagrees
// with the request's branch context is refused (the round 6 rule), but the
// handler answered it with the 500 it maps every service error to. The refusal
// answers 403 like the other register refusals, nothing written; the control
// sale on the caller's own register still answers 201 at its branch.
func TestKeyBranchPin_UserRegisterBranchRefusalAnswers403(t *testing.T) {
	f, _, _ := round5Fixture(t)
	ctx := context.Background()
	regA := f.r15reg(t, "txa", uuid.New(), f.branchA)
	regB := f.r15reg(t, "txb", uuid.New(), f.branchB)
	hdr := map[string]string{"X-Branch-Id": f.branchA.String()}
	created := []uuid.UUID{}
	t.Cleanup(func() {
		for _, id := range created {
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_line_items WHERE transaction_id = $1`, id)
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id = $1`, id)
		}
	})

	status, buf := f.userAs(t, "admin", "POST", "/api/v1/pos/transactions", `{"register_id":"`+regB+`"}`, hdr)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a different branch than the request") {
		t.Errorf("a user's sale on another branch's register: %d %s, want the 403 naming the register rule", status, buf)
	}
	if n := f.transactionsOn(t, regB); n != 0 {
		t.Errorf("the refused sale wrote %d transactions on the register, want none", n)
	}

	// The control: the caller's own register.
	body := `{"register_id":"` + regA + `","cashier_id":"` + uuid.NewString() + `"}`
	status, buf = f.userAs(t, "admin", "POST", "/api/v1/pos/transactions", body, hdr)
	var tx struct {
		ID       uuid.UUID  `json:"id"`
		BranchID *uuid.UUID `json:"branch_id"`
	}
	if err := json.Unmarshal(buf, &tx); err == nil && tx.ID != uuid.Nil {
		created = append(created, tx.ID)
	}
	if status != http.StatusCreated || tx.BranchID == nil || *tx.BranchID != f.branchA {
		t.Errorf("a user's sale on its own register: %d %s, want 201 at branch A", status, buf)
	}
}

// transactionsOn counts the transactions a register holds.
func (f *keyPinFixture) transactionsOn(t *testing.T, register string) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM pos_transactions WHERE register_id = $1`, register).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// transactionsWithID counts the transactions carrying the id given.
func (f *keyPinFixture) transactionsWithID(t *testing.T, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM pos_transactions WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Fix round 7 (r15 P3-2): till open and a return stamped the register's
// branch whatever the caller's branch context named, so a cashier granted only
// A, sending X-Branch-Id A, opened a till and recorded a cash return at B.
// (The register on a branched location half of that predates this PR; the
// locationless half came with round 6's fallback.) Both now hold the request's
// branch against the register's the way the sale already does: 403, nothing
// written, and the caller's own register passes.
func TestKeyBranchPin_TillOpenAndReturnsHoldTheRequestBranch(t *testing.T) {
	f, _, _ := round5Fixture(t)
	ctx := context.Background()
	regA := f.r15reg(t, "t3a", uuid.New(), f.branchA)
	regB := f.r15reg(t, "t3b", uuid.New(), f.branchB)
	hdr := map[string]string{"X-Branch-Id": f.branchA.String()}
	sessionsOn := func(reg string) (n int) {
		if err := f.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM till_sessions WHERE register_id = $1`, reg).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	returnsOn := func(reg string) (n int) {
		if err := f.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM pos_returns WHERE register_id = $1`, reg).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	t.Cleanup(func() {
		for _, reg := range []string{regA, regB} {
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_return_lines WHERE return_id IN (SELECT id FROM pos_returns WHERE register_id = $1)`, reg)
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_returns WHERE register_id = $1`, reg)
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM till_sessions WHERE register_id = $1`, reg)
		}
	})

	// The till open on another branch's register: 403, no session.
	status, buf := f.userAs(t, "admin", "POST", "/api/v1/pos/till/open", `{"register_id":"`+regB+`","opening_float_cents":5000}`, hdr)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a different branch than the request") {
		t.Errorf("a user opening a till on another branch's register: %d %s, want the 403 naming the register rule", status, buf)
	}
	if n := sessionsOn(regB); n != 0 {
		t.Errorf("the refused till open wrote %d sessions on the register, want none", n)
	}

	// The return on another branch's register: 403, no return.
	retBody := `{"register_id":"` + regB + `","refund_method":"cash","reason":"pin",` +
		`"customer_id":"` + f.custID.String() + `","lines":[{"product_id":"` +
		f.posProd.String() + `","quantity":"1","unit_price_ten_thousandths":10000,"restock":false}]}`
	status, buf = f.userAs(t, "admin", "POST", "/api/v1/pos/returns", retBody, hdr)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a different branch than the request") {
		t.Errorf("a user recording a return on another branch's register: %d %s, want the 403 naming the register rule", status, buf)
	}
	if n := returnsOn(regB); n != 0 {
		t.Errorf("the refused return wrote %d returns on the register, want none", n)
	}

	// The A controls: the caller's own register passes for both.
	status, buf = f.userAs(t, "admin", "POST", "/api/v1/pos/till/open", `{"register_id":"`+regA+`","opening_float_cents":5000}`, hdr)
	var sess struct {
		ID       uuid.UUID  `json:"id"`
		BranchID *uuid.UUID `json:"branch_id"`
	}
	_ = json.Unmarshal(buf, &sess)
	if status != http.StatusCreated || sess.BranchID == nil || *sess.BranchID != f.branchA {
		t.Errorf("a user opening a till on its own register: %d %s, want 201 at branch A", status, buf)
	}
	retBody = `{"register_id":"` + regA + `","refund_method":"cash","reason":"pin",` +
		`"customer_id":"` + f.custID.String() + `","lines":[{"product_id":"` +
		f.posProd.String() + `","quantity":"1","unit_price_ten_thousandths":10000,"restock":false}]}`
	status, buf = f.userAs(t, "admin", "POST", "/api/v1/pos/returns", retBody, hdr)
	var ret struct {
		ID       uuid.UUID  `json:"id"`
		BranchID *uuid.UUID `json:"branch_id"`
	}
	_ = json.Unmarshal(buf, &ret)
	if status != http.StatusCreated || ret.BranchID == nil || *ret.BranchID != f.branchA {
		t.Errorf("a user recording a return on its own register: %d %s, want 201 at branch A", status, buf)
	}
}

// Fix round 7 (r15 P3-3), under the money never queues rule: the offline
// sync no longer runs its items at all. A batch whose register names another
// branch was refused 403 before any item ran; now every batch that parses is
// refused 409 money_never_queues before any item runs and before a log row
// is written, whatever register it names.
func TestKeyBranchPin_SyncRefusesAnotherBranchsBatchRegister(t *testing.T) {
	f, _, first := round5Fixture(t)
	ctx := context.Background()
	client := uuid.New()
	batch := "pin-r7-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_line_items WHERE transaction_id = $1`, client)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id = $1`, client)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_sync_log WHERE batch_id = $1`, batch)
	})
	syncBody := func(batchRegister, _ string) string {
		return `{"batch_id":"` + batch + `","register_id":"` + batchRegister +
			`","items":[{"client_id":"` + client.String() +
			`","cashier_id":"` + uuid.NewString() + `","customer_id":"` + f.custID.String() +
			`","items":[{"product_id":"` + f.posProd.String() + `","quantity":"1"}],` +
			`"tenders":[{"method":"cash","amount_cents":105}],"client_created_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`
	}
	logRows := func() (n int) {
		if err := f.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM pos_sync_log WHERE batch_id = $1`, batch).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The batch register of another branch: the batch is refused by rule
	// before any item runs and before the log row is written.
	status, buf := f.key(t, first.raw, "POST", "/api/v1/pos/sync", syncBody(f.regB, f.regA), nil)
	if status != http.StatusConflict || !strings.Contains(string(buf), "money_never_queues") {
		t.Errorf("bound key sync with another branch's batch register: %d %s, want the rule's 409", status, buf)
	}
	if n := logRows(); n != 0 {
		t.Errorf("the refused sync wrote %d pos_sync_log rows for the batch, want none", n)
	}
	if n := f.transactionsWithID(t, client); n != 0 {
		t.Errorf("the refused sync wrote %d transactions, want none", n)
	}

	// The control: the pin's own register as the batch register is refused by
	// the same rule, nothing written.
	status, buf = f.key(t, first.raw, "POST", "/api/v1/pos/sync", syncBody(f.regA, f.regA), nil)
	if status != http.StatusConflict || !strings.Contains(string(buf), "money_never_queues") {
		t.Errorf("bound key sync with the pin's own batch register: %d %s, want the rule's 409", status, buf)
	}
	if n := logRows(); n != 0 {
		t.Errorf("the refused control sync wrote %d pos_sync_log rows, want none", n)
	}
	if n := f.transactionsWithID(t, client); n != 0 {
		t.Errorf("the refused control sync wrote %d transactions, want none", n)
	}
}

// Fix round 7 (principal item 57 question 2): the shared customer refusals
// carry one clause naming the other branch behind a lead in that names the
// caller, words true for whoever reads them. A bound key read "a branch bound
// key may act only on its own branch" even as a sales user, which is wrong
// for a user. Now: the bound key's lead names the key and its pin, the
// unbound key's lead names the key and the scope it lacks, and a user's lead
// names the role and the finance or owner roles that may.
func TestKeyBranchPin_SharedRefusalWordsNameTheCaller(t *testing.T) {
	f, _, _ := round5Fixture(t)
	s := seedSharedCustomer(t, f, f.branchA, f.branchB)
	sh := "/api/v1/customers/" + s.id.String()
	change := func() string {
		return f.customerReplace(t, s.id, map[string]any{"credit_limit_cents": nextCreditLimit(666000)})
	}
	lowest := f.branchA
	if f.branchB.String() < f.branchA.String() {
		lowest = f.branchB
	}
	cases := []struct {
		who, lead string // who: "bound key", "unbound key", or a user role
	}{
		{"bound key", "this key may act only on its own branch"},
		{"unbound key", "this key may not change a shared customer's restricted fields"},
		{"sales", "this role may not change a shared customer's restricted fields"},
	}
	for _, c := range cases {
		var status int
		var buf []byte
		if c.who == "bound key" {
			status, buf = f.key(t, f.bound, "PUT", sh, change(), nil)
		} else if c.who == "unbound key" {
			status, buf = f.key(t, f.unbound, "PUT", sh, change(), nil)
		} else {
			status, buf = f.userAs(t, c.who, "PUT", sh, change(), nil)
		}
		// The bound key's refusal names the lowest holder besides the pin
		// (branch B here); the caller refusals name the lowest holder at all.
		named := f.branchB
		if c.who != "bound key" {
			named = lowest
		}
		want := c.lead + ": the customer is also held by branch " + named.String()
		if status != http.StatusForbidden || !strings.Contains(string(buf), want) {
			t.Errorf("%s changing a restricted field: %d %s, want the 403 %q", c.who, status, buf, want)
		}
	}
}
