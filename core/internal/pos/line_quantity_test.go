// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// A counter line's quantity changes in place (item 130, note N2):
// PATCH /api/v1/pos/transactions/{id}/items/{itemId} reprices the line, its
// tax and the sale's totals through the code AddItem uses, so the result is
// the same as removing the line and adding it at the new quantity.

import (
	"context"
	"errors"
	"fmt"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/branchctx"
	"net/http"
	"strings"
	"sync"
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

// RULE: three changes racing on one revision at pool size 4 leave one
// winner; the others are told the sale moved, and the sale's totals are
// those of the winner's quantity alone.
func TestQuantityChangeRaceHasOneWinner(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	s := f.startSale(nil)
	added := f.addLine(s, f.productLine("1"))
	line := lineIDs(t, added)[0]
	revision := rev(t, added)

	quantities := []string{"4", "5", "6"}
	results := make([]resp, len(quantities))
	var wg sync.WaitGroup
	for i := range quantities {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = f.patchQty(s, line, quantities[i], revision)
		}()
	}
	wg.Wait()

	winner := -1
	for i, r := range results {
		switch r.status {
		case http.StatusOK:
			if winner != -1 {
				t.Errorf("two changes won on one revision")
			}
			winner = i
		case http.StatusConflict:
		default:
			t.Errorf("contender %d = %d: %s", i, r.status, r.raw)
		}
	}
	if winner == -1 {
		t.Fatalf("no change won")
	}
	ref := f.startSale(nil)
	want := f.addLine(ref, f.productLine(quantities[winner]))
	got := f.do("GET", "/api/v1/pos/transactions/"+s, nil)
	if totalsOf(t, got) != totalsOf(t, want) {
		t.Errorf("totals %v, want the winner's quantity alone %v", totalsOf(t, got), totalsOf(t, want))
	}
}

// RULE (money): for every kind of line the counter sells, a quantity change
// gives the line total, its taxable flag, its tax and the sale's totals that
// removing the line and adding it at the new quantity gives.
func TestQuantityChangeEqualsRemoveAndAddForEveryLineKind(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	var chargeCode string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT code FROM charge_codes WHERE is_active ORDER BY code LIMIT 1`).Scan(&chargeCode); err != nil {
		t.Fatalf("no active charge code: %v", err)
	}
	product := func(extra map[string]any) func(qty string) map[string]any {
		return func(qty string) map[string]any {
			l := map[string]any{"product_id": f.productID.String(), "quantity": qty}
			for k, v := range extra {
				l[k] = v
			}
			return l
		}
	}
	cases := []struct {
		name     string
		line     func(qty string) map[string]any
		from, to string
	}{
		{"plain", product(nil), "2", "7"},
		{"fractional", product(nil), "2.5", "3.3333"},
		{"kit", func(qty string) map[string]any {
			return map[string]any{"product_id": f.kitID.String(), "quantity": qty}
		}, "1", "3"},
		{"override price", product(map[string]any{"unit_price_ten_thousandths": 70000, "override_reason": "matched a quote"}), "2", "5"},
		{"percent discount", product(map[string]any{"discount_percent": "10", "discount_reason": "contractor"}), "2", "5"},
		{"cents discount", product(map[string]any{"discount_cents": 150, "discount_reason": "damaged edge"}), "2", "5"},
		{"unit ratio", product(map[string]any{"price_uom": "M", "uom_qty": "1000", "price_uom_qty": "1"}), "500", "1500"},
		{"non stock untaxed", func(qty string) map[string]any {
			return map[string]any{"description": "cut to length", "uom": "EA", "quantity": qty,
				"unit_price_ten_thousandths": 100000, "taxable": false}
		}, "2", "5"},
		{"non stock taxed", func(qty string) map[string]any {
			return map[string]any{"description": "cut to length", "uom": "EA", "quantity": qty,
				"unit_price_ten_thousandths": 100000, "taxable": true}
		}, "2", "5"},
		{"charge", func(qty string) map[string]any {
			return map[string]any{"charge_code": chargeCode, "description": "freight", "quantity": qty, "unit_price_ten_thousandths": 250000}
		}, "1", "3"},
	}
	topLine := func(r resp) map[string]any {
		raw, _ := r.body["lines"].([]any)
		for _, l := range raw {
			m := l.(map[string]any)
			if m["parent_line_id"] == nil {
				return m
			}
		}
		return nil
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := f.startSale(nil)
			added := f.addLine(a, c.line(c.from))
			if added.status != http.StatusOK {
				t.Fatalf("add = %d: %s", added.status, added.raw)
			}
			changed := f.patchQty(a, lineIDs(t, added)[0], c.to, rev(t, added))
			if changed.status != http.StatusOK {
				t.Fatalf("change = %d: %s", changed.status, changed.raw)
			}

			b := f.startSale(nil)
			first := f.addLine(b, c.line(c.from))
			if r := f.do("DELETE", "/api/v1/pos/transactions/"+b+"/items/"+lineIDs(t, first)[0], nil,
				"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
				t.Fatalf("remove = %d: %s", r.status, r.raw)
			}
			again := f.addLine(b, c.line(c.to))
			if again.status != http.StatusOK {
				t.Fatalf("re-add = %d: %s", again.status, again.raw)
			}
			if got, want := totalsOf(t, changed), totalsOf(t, again); got != want {
				t.Errorf("subtotal, tax, total = %v, remove and add = %v", got, want)
			}
			got, want := topLine(changed), topLine(again)
			for _, k := range []string{"line_total_cents", "taxable", "unit_price_ten_thousandths", "price_source", "quantity"} {
				if fmt.Sprint(got[k]) != fmt.Sprint(want[k]) {
					t.Errorf("%s = %v, remove and add = %v", k, got[k], want[k])
				}
			}
			if n, m := len(changed.body["lines"].([]any)), len(again.body["lines"].([]any)); n != m {
				t.Errorf("%d lines, remove and add %d", n, m)
			}
		})
	}
}

// RULE (audit): a reprice writes no override row under a line id that is not
// on the sale, keeps who adjusted the price, and logs the change as a
// quantity change naming the line, the old and the new quantity.
func TestQuantityChangeKeepsTheAuditTrailTrue(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	manager, cashierSub := mustUUID(t), mustUUID(t)
	s := f.startSale(nil)
	added := f.do("POST", "/api/v1/pos/transactions/"+s+"/items", map[string]any{"line": f.productLine("2")},
		"X-Test-Role", "cashier", "X-Test-Sub", manager)
	if added.status != http.StatusOK {
		t.Fatalf("add = %d: %s", added.status, added.raw)
	}
	// an override price, by the manager
	cur := f.getSale(t, s)
	f.do("DELETE", "/api/v1/pos/transactions/"+s+"/items/"+lineIDs(t, resp{body: cur})[0], nil,
		"X-Test-Role", "cashier", "X-Test-Sub", manager)
	line := f.productLine("2")
	line["unit_price_ten_thousandths"] = 70000
	line["override_reason"] = "matched a quote"
	added = f.do("POST", "/api/v1/pos/transactions/"+s+"/items", map[string]any{"line": line},
		"X-Test-Role", "cashier", "X-Test-Sub", manager)
	if added.status != http.StatusOK {
		t.Fatalf("override add = %d: %s", added.status, added.raw)
	}
	lineID := lineIDs(t, added)[0]
	r := f.do("PATCH", "/api/v1/pos/transactions/"+s+"/items/"+lineID,
		map[string]any{"quantity": "5", "revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", cashierSub)
	if r.status != http.StatusOK {
		t.Fatalf("patch = %d: %s", r.status, r.raw)
	}
	raw := r.body["lines"].([]any)[0].(map[string]any)
	if got := fmt.Sprint(raw["price_adjusted_by"]); got != manager {
		t.Errorf("price_adjusted_by = %s, want the manager %s who set the price", got, manager)
	}
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT action, changes->>'line_id', changes FROM audit_log WHERE entity_id = $1 AND action LIKE 'pos_transaction.line_%'`, s)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	changes := 0
	for rows.Next() {
		var action string
		var lid *string
		var body []byte
		if err := rows.Scan(&action, &lid, &body); err != nil {
			t.Fatal(err)
		}
		if lid != nil && *lid != lineID && !strings.Contains(string(body), "removed") {
			// the first, removed line's override row may name its own line
			n := countOf(t, f.db, `SELECT count(*) FROM pos_line_items WHERE id = $1`, *lid)
			if n == 0 && *lid != lineIDs(t, resp{body: cur})[0] {
				t.Errorf("%s names line %s, which is no line of the sale", action, *lid)
			}
		}
		if action == "pos_transaction.line_quantity_changed" {
			changes++
			if lid == nil || *lid != lineID {
				t.Errorf("quantity change row names line %v, want %s", lid, lineID)
			}
		}
	}
	if changes != 1 {
		t.Errorf("quantity change audit rows = %d, want 1", changes)
	}
}

// RULE (SEC-NOHEADER-1): a caller with no branch header whose grants miss the
// sale's branch is refused 403 by name, as the line add refuses it, never
// answered as a missing sale. This holds the change's own holdSaleBranch.
func TestQuantityChangeNoHeaderArmRefusesByName(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	branchB := uuid.New()
	sub := "qty-nh-" + uuid.NewString()[:8]
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'quantity no header branch B')`, []any{branchB, "QNH-" + branchB.String()[:8]}},
		{`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, []any{sub, branchB}},
	} {
		if _, err := db.Pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, sub)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, branchB)
	})
	f := newFixture(t, db)
	s := f.startSale(nil)
	added := f.addLine(s, f.productLine("2"))
	line := uuid.MustParse(lineIDs(t, added)[0])
	saleID := uuid.MustParse(s)
	revision := rev(t, added)

	held := branchctx.With(ctx, &branchctx.Context{UserSub: sub})
	_, addErr := f.service.AddLine(held, saleID, nil, sub)
	_, err := f.service.ChangeLineQuantity(held, saleID, line, httpx.Quantity(50000), "", &revision, sub)
	var he, addHE *httpx.Error
	if !errors.As(err, &he) || !errors.As(addErr, &addHE) {
		t.Fatalf("change = %v, add = %v, want typed refusals", err, addErr)
	}
	if he.Status != http.StatusForbidden || he.Status != addHE.Status {
		t.Errorf("change refused %d, add refused %d, want both 403 by name", he.Status, addHE.Status)
	}
}
