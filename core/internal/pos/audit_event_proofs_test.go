// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The transaction proofs the recipe owes beyond the completion and the void
// (first review P2-4a): a failing audit write rolls the completion, the
// void AND the return back, and a failing event write rolls the return and
// the till close back. The audit failure is driven through a trigger on
// audit_log that refuses one action, so the real logger, the real
// transaction and the real rollback are all in the proof.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// failAuditRows makes every audit_log insert of the given action fail, the
// way a saturated or broken audit table would, until the test ends.
func failAuditRows(t *testing.T, f *fixture, action string) {
	t.Helper()
	mustExec(t, f.db, `CREATE OR REPLACE FUNCTION pos_test_fail_audit() RETURNS TRIGGER AS $fn$
		BEGIN IF NEW.action = TG_ARGV[0] THEN RAISE EXCEPTION 'audit write refused for %', NEW.action; END IF;
		RETURN NEW; END $fn$ LANGUAGE plpgsql`)
	mustExec(t, f.db, `DROP TRIGGER IF EXISTS pos_test_fail_audit ON audit_log`)
	mustExec(t, f.db, `CREATE TRIGGER pos_test_fail_audit BEFORE INSERT ON audit_log
		FOR EACH ROW EXECUTE FUNCTION pos_test_fail_audit('`+action+`')`)
	t.Cleanup(func() {
		mustExec(t, f.db, `DROP TRIGGER IF EXISTS pos_test_fail_audit ON audit_log`)
		mustExec(t, f.db, `DROP FUNCTION IF EXISTS pos_test_fail_audit()`)
	})
}

// RULE: a failing audit write rolls the completion, the void and the return
// back with it (R1-14: a committed act leaves exactly one row, a rolled back
// one leaves none).
func TestAFailingAuditWriteRollsTheActBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	// The completion.
	f := newFixture(t, testutil.RequireDB(t))
	failAuditRows(t, f, "pos.transaction.completed")
	invoicesBefore := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE order_id IS NULL`)
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("3")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 3266)); r.status == http.StatusOK {
		t.Fatal("the completion committed under a failing audit write")
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE order_id IS NULL`); got != invoicesBefore {
		t.Errorf("%d invoices after the refused completion, want %d", got, invoicesBefore)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_tenders WHERE transaction_id = $1`, saleID); got != 0 {
		t.Errorf("%d tenders after the refused completion, want 0", got)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want the 100 untouched", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.transaction.completed' AND entity_id = $1`, saleID); got != 0 {
		t.Errorf("%d completion audit rows, want 0 (the write failed and the act rolled back)", got)
	}

	// The void.
	f2 := newFixture(t, testutil.RequireDB(t))
	failAuditRows(t, f2, "pos.transaction.voided")
	saleID2, body := f2.saleOf("2", tender("cash", 1198))
	if r := f2.do("POST", "/api/v1/pos/transactions/"+saleID2+"/void",
		map[string]any{"reason": "nope", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status == http.StatusOK {
		t.Fatal("the void committed under a failing audit write")
	}
	if got := str(t, f2.getSale(t, saleID2), "status"); got != "completed" {
		t.Errorf("sale status = %q, want completed (the void rolled back)", got)
	}
	if got := f2.stock(); got != "98.0000/0.0000" {
		t.Errorf("stock = %s, want 98 (the goods still gone)", got)
	}
	if got := f2.accountBalance("1010"); got != 1198 {
		t.Errorf("cash balance = %d, want the 1198 still booked", got)
	}

	// The return.
	f3 := newFixture(t, testutil.RequireDB(t))
	failAuditRows(t, f3, "pos.return.completed")
	saleID3, body3 := f3.saleOf("2", tender("cash", 1198))
	lineID := body3.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	memosBefore := countOf(t, f3.db, `SELECT count(*) FROM credit_memos`)
	if r := f3.returnOn(t, saleID3, lineID, "1"); r.status == http.StatusCreated {
		t.Fatal("the return committed under a failing audit write")
	}
	if got := countOf(t, f3.db, `SELECT count(*) FROM credit_memos`); got != memosBefore {
		t.Errorf("%d memos after the refused return, want %d", got, memosBefore)
	}
	if got := countOf(t, f3.db, `SELECT count(*) FROM pos_returns`); got != 0 {
		t.Errorf("%d return rows after the refused return, want 0", got)
	}
	if got := f3.stock(); got != "98.0000/0.0000" {
		t.Errorf("stock = %s, want 98 (the return rolled back)", got)
	}
	if got := f3.accountBalance("1010"); got != 1198 {
		t.Errorf("cash balance = %d, want 1198 (no refund was paid)", got)
	}
	f3.assertARInvariants(t)
}

// RULE: a failing event write rolls the return back: no memo, no refund, no
// restock, no return row.
func TestFailingEventWriteRollsTheReturnBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.events.fail = "pos_return.completed"
	})
	saleID, body := f.saleOf("2", tender("cash", 1198))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	memosBefore := countOf(t, f.db, `SELECT count(*) FROM credit_memos`)
	if r := f.returnOn(t, saleID, lineID, "1"); r.status == http.StatusCreated {
		t.Fatal("the return committed under a failing event write")
	}
	for _, c := range []struct {
		what string
		sql  string
		args []any
		want int64
	}{
		{"memos", `SELECT count(*) FROM credit_memos`, nil, memosBefore},
		{"return rows", `SELECT count(*) FROM pos_returns`, nil, 0},
		{"refund entries", `SELECT count(*) FROM payment_refunds`, nil, 0},
		// the walk-in sale's row is not this customer's: the return's credit never landed here
		{"subledger rows", `SELECT count(*) FROM customer_transactions WHERE customer_id = $1`, []any{f.customerID}, 0},
	} {
		if got := countOf(t, f.db, c.sql, c.args...); got != c.want {
			t.Errorf("%s = %d, want %d", c.what, got, c.want)
		}
	}
	if got := f.stock(); got != "98.0000/0.0000" {
		t.Errorf("stock = %s, want 98 (nothing restocked)", got)
	}
	if got := f.accountBalance("1010"); got != 1198 {
		t.Errorf("cash balance = %d, want 1198 (no refund was paid)", got)
	}
	f.assertARInvariants(t)
}

// RULE: a failing event write rolls the till close back: the session stays
// open, no Z report, no over/short entry.
func TestFailingEventWriteRollsTheTillCloseBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.events.fail = "till.closed"
	})
	sessionID := f.openTill(1000)
	f.saleOf("1", tender("cash", 599))
	r := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{
		"counted_by_method": map[string]any{"cash": 1000}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status == http.StatusOK {
		t.Fatal("the close committed under a failing event write")
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_sessions WHERE id = $1 AND status = 'OPEN'`, sessionID); got != 1 {
		t.Errorf("the session closed under a failing event write")
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_z_reports`); got != 0 {
		t.Errorf("%d Z reports after the refused close, want 0", got)
	}
	if got := f.accountBalance("5030"); got != 0 {
		t.Errorf("5030 balance = %d, want 0 (no variance was booked)", got)
	}
	f.assertARInvariants(t)
}
