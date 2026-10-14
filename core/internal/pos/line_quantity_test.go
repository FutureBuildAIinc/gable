// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// A counter line's quantity changes in place (item 130, note N2):
// PATCH /api/v1/pos/transactions/{id}/items/{itemId} reprices the line, its
// tax and the sale's totals through the code AddItem uses, so the result is
// the same as removing the line and adding it at the new quantity.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// patchQty changes the line's quantity on the sale's current revision.
func (f *fixture) patchQty(saleID, lineID string, qty any, revision int64) resp {
	f.t.Helper()
	return f.do("PATCH", "/api/v1/pos/transactions/"+saleID+"/items/"+lineID,
		map[string]any{"quantity": qty, "revision": revision}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// lineIDs reads the sale's top level line ids in position order.
func lineIDs(t *testing.T, r resp) []string {
	t.Helper()
	raw, _ := r.body["lines"].([]any)
	var ids []string
	for _, l := range raw {
		m := l.(map[string]any)
		if m["parent_line_id"] == nil {
			ids = append(ids, m["id"].(string))
		}
	}
	return ids
}

func totalsOf(t *testing.T, r resp) [3]int64 {
	t.Helper()
	return [3]int64{num(t, r.body, "subtotal_cents"), num(t, r.body, "tax_cents"), num(t, r.body, "total_cents")}
}

func lineTotal(t *testing.T, r resp, i int) int64 {
	t.Helper()
	raw, _ := r.body["lines"].([]any)
	return num(t, raw[i].(map[string]any), "line_total_cents")
}

// RULE: a quantity change gives the line total, tax and sale total that
// removing the line and adding it at the new quantity gives.
func TestQuantityChangeEqualsRemoveAndAdd(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	a := f.startSale(nil)
	added := f.addLine(a, f.productLine("2"))
	if added.status != http.StatusOK {
		t.Fatalf("add = %d: %s", added.status, added.raw)
	}
	line := lineIDs(t, added)[0]
	changed := f.patchQty(a, line, "7", rev(t, added))
	if changed.status != http.StatusOK {
		t.Fatalf("patch = %d: %s", changed.status, changed.raw)
	}
	if got := rev(t, changed); got <= rev(t, added) {
		t.Errorf("revision %d did not move past %d", got, rev(t, added))
	}

	b := f.startSale(nil)
	first := f.addLine(b, f.productLine("2"))
	removed := f.do("DELETE", "/api/v1/pos/transactions/"+b+"/items/"+lineIDs(t, first)[0], nil,
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if removed.status != http.StatusOK {
		t.Fatalf("remove = %d: %s", removed.status, removed.raw)
	}
	again := f.addLine(b, f.productLine("7"))

	if got, want := totalsOf(t, changed), totalsOf(t, again); got != want {
		t.Errorf("subtotal, tax, total after the change = %v, remove and add = %v", got, want)
	}
	if got, want := lineTotal(t, changed, 0), lineTotal(t, again, 0); got != want || got == 0 {
		t.Errorf("line total after the change = %d, remove and add = %d", got, want)
	}
	if q := changed.body["lines"].([]any)[0].(map[string]any)["quantity"]; q != "7" && q != "7.0000" {
		t.Errorf("quantity = %v, want 7", q)
	}
}

// RULE: the line keeps its place among the sale's lines.
func TestQuantityChangeKeepsThePosition(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	s := f.startSale(nil)
	f.addLine(s, f.productLine("1"))
	two := f.addLine(s, f.productLine("2"))
	f.addLine(s, f.productLine("3"))
	cur := f.do("GET", "/api/v1/pos/transactions/"+s, nil)
	ids := lineIDs(t, cur)
	if len(ids) != 3 {
		t.Fatalf("lines = %d, want 3 (%s)", len(ids), two.raw)
	}
	r := f.patchQty(s, ids[0], "9", rev(t, cur))
	if r.status != http.StatusOK {
		t.Fatalf("patch = %d: %s", r.status, r.raw)
	}
	got := lineIDs(t, r)
	for i := range ids {
		if got[i] != ids[i] {
			t.Errorf("line %d is %s, want %s: the changed line moved", i, got[i], ids[i])
		}
	}
}

// RULE: each refusal says why, with the status the sale's other writes use.
func TestQuantityChangeRefusals(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(1000)
	s := f.startSale(nil)
	added := f.addLine(s, f.productLine("2"))
	line := lineIDs(t, added)[0]
	revision := rev(t, added)

	if r := f.patchQty(s, line, "3", revision-1); r.status != http.StatusConflict {
		t.Errorf("stale revision = %d, want 409: %s", r.status, r.raw)
	} else if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("stale revision code = %q, want stale_revision", code)
	}
	for _, q := range []any{"0", "-1", "abc"} {
		if r := f.patchQty(s, line, q, revision); r.status != http.StatusBadRequest {
			t.Errorf("quantity %v = %d, want 400: %s", q, r.status, r.raw)
		}
	}
	other := f.startSale(nil)
	if r := f.patchQty(other, line, "3", 1); r.status != http.StatusNotFound {
		t.Errorf("a line not on this sale = %d, want 404: %s", r.status, r.raw)
	}
	if r := f.patchQty(s, uuid.NewString(), "3", revision); r.status != http.StatusNotFound {
		t.Errorf("an unknown line = %d, want 404: %s", r.status, r.raw)
	}
	if r := f.do("PATCH", "/api/v1/pos/transactions/"+s+"/items/"+line, map[string]any{"quantity": "3"},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusPreconditionRequired {
		t.Errorf("no revision = %d, want 428: %s", r.status, r.raw)
	}

	// a sale that is no longer open
	done := f.completeSale(s, tender("cash", num(t, f.getSale(t, s), "total_cents")))
	if done.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", done.status, done.raw)
	}
	if r := f.patchQty(s, line, "3", rev(t, done)); r.status != http.StatusConflict {
		t.Errorf("a completed sale = %d, want 409: %s", r.status, r.raw)
	} else if code, _, _ := errorOf(t, r); code != "invalid_state_transition" {
		t.Errorf("a completed sale code = %q, want invalid_state_transition", code)
	}
}

// RULE: the change carries the walls AddItem carries: a caller scoped to
// another branch is answered as it is when it adds an item.
func TestQuantityChangeCarriesTheBranchWall(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	setPosSetting(t, db, "multi_branch_enabled", "true")
	branchB := uuid.New()
	mustExecB := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(contextB(), sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExecB(`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'WL-QTY-B', 'quantity wall branch B')`, branchB)
	mustExecB(`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('qty-wall-b', $1, TRUE, 'test')`, branchB)
	t.Cleanup(func() {
		setPosSetting(t, db, "multi_branch_enabled", "false")
		mustExecB(`DELETE FROM user_locations WHERE user_sub = 'qty-wall-b' AND branch_id = $1`, branchB)
		mustExecB(`DELETE FROM locations WHERE id = $1`, branchB)
	})
	f := newFixture(t, db)
	// the owner's own branch, as the till wall test reaches it: no claims
	var own []string
	started := f.do("POST", "/api/v1/pos/transactions", map[string]any{"register_id": f.register}, own...)
	if started.status != http.StatusCreated {
		t.Fatalf("start sale = %d: %s", started.status, started.raw)
	}
	s := str(t, started.body, "id")
	added := f.do("POST", "/api/v1/pos/transactions/"+s+"/items", map[string]any{"line": f.productLine("2")}, own...)
	if added.status != http.StatusOK {
		t.Fatalf("add = %d: %s", added.status, added.raw)
	}
	line := lineIDs(t, added)[0]
	scoped := []string{"X-Test-Role", "cashier", "X-Test-Sub", "qty-wall-b", "X-Branch-Id", branchB.String()}

	viaAdd := f.do("POST", "/api/v1/pos/transactions/"+s+"/items", map[string]any{"line": f.productLine("1")}, scoped...)
	if viaAdd.status < 400 {
		t.Fatalf("the add through the wall = %d, want a refusal: %s", viaAdd.status, viaAdd.raw)
	}
	viaPatch := f.do("PATCH", "/api/v1/pos/transactions/"+s+"/items/"+line,
		map[string]any{"quantity": "3", "revision": rev(t, added)}, scoped...)
	if viaPatch.status != viaAdd.status {
		t.Errorf("the change through the wall = %d, the add = %d: %s", viaPatch.status, viaAdd.status, viaPatch.raw)
	}
	cur := f.do("GET", "/api/v1/pos/transactions/"+s, nil, own...)
	if q := cur.body["lines"].([]any)[0].(map[string]any)["quantity"]; q == "3" || q == "3.0000" {
		t.Errorf("the wall let the quantity change")
	}
}
