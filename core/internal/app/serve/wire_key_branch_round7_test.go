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
	status, buf = f.userAs(t, "admin", "POST", "/api/v1/pos/till/"+sess.String()+"/close", `{"counted_by_method":{"CASH":0.5}}`, hdr)
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
		`INSERT INTO pos_returns (id, register_id, branch_id, cashier_id, subtotal, tax_amount, total, refund_method, status)
		 VALUES ($1, $2, NULL, $3, 10.11, 0.99, 11.10, 'CASH', 'COMPLETED')`, ret, reg, uuid.New()); err != nil {
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
