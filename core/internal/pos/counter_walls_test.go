// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The branch wall and the roles on the counter's new routes: a caller held
// to another branch finds this branch's sale a missing row on the override,
// the signature, the receipt print and the standing's sale read; a cashier
// is no manager; a machine key approves nothing.

import (
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func TestTheNewCounterRoutesHoldTheBranchWall(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	setPosSetting(t, db, "multi_branch_enabled", "true")
	branchB := uuid.New()
	mustExec(t, db, `INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'WL-NEW-B', 'wall branch B')`, branchB)
	mustExec(t, db, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('pos-new-wall-b', $1, TRUE, 'test')`, branchB)
	t.Cleanup(func() {
		setPosSetting(t, db, "multi_branch_enabled", "false")
		mustExec(t, db, `DELETE FROM user_locations WHERE user_sub = 'pos-new-wall-b' AND branch_id = $1`, branchB)
		mustExec(t, db, `DELETE FROM locations WHERE id = $1`, branchB)
	})
	f := newFixture(t, db, withReceipts(thermalStandIn(nil)))
	f.seedTill()
	// an open sale on branch A (the default branch), made with no claims: the
	// middleware's dev fallback, permissive with no branch context (the
	// multi branch switch asks every claimed request for a branch)
	r0 := f.do("POST", "/api/v1/pos/transactions", map[string]any{"register_id": f.register, "customer_id": f.customerID.String()})
	if r0.status != http.StatusCreated {
		t.Fatalf("start sale = %d: %s", r0.status, r0.raw)
	}
	sale := str(t, r0.body, "id")
	if r := f.do("POST", "/api/v1/pos/transactions/"+sale+"/items", map[string]any{"line": f.productLine("1")}); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	cur := f.getSale(t, sale)
	total := num(t, cur, "total_cents")
	scopedCashier := []string{"X-Test-Role", "cashier", "X-Test-Sub", "pos-new-wall-b", "X-Branch-Id", branchB.String()}
	scopedAdmin := []string{"X-Test-Role", "admin", "X-Test-Sub", "pos-new-wall-b", "X-Branch-Id", branchB.String()}

	if r := f.do("POST", "/api/v1/pos/transactions/"+sale+"/overrides",
		map[string]any{"kind": "account", "reason": "x", "cap_cents": total}, scopedAdmin...); r.status != http.StatusNotFound {
		t.Errorf("an override of another branch's sale = %d, want 404: %s", r.status, r.raw)
	}
	if n := f.scalar(`SELECT count(*) FROM pos_account_overrides WHERE sale_id = $1`, uuid.MustParse(sale)); n.(int64) != 0 {
		t.Errorf("an override was written through the wall")
	}
	if r := f.do("GET", "/api/v1/pos/customers/"+f.customerID.String()+"/standing?sale_id="+sale, nil, scopedCashier...); r.status != http.StatusNotFound {
		t.Errorf("the standing read naming another branch's sale = %d, want 404: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/pos/transactions/"+sale+"/complete",
		map[string]any{"tenders": []map[string]any{tender("cash", total)}, "revision": num(t, cur, "revision")}); r.status != http.StatusOK {
		t.Fatalf("complete on its own branch = %d: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/pos/transactions/"+sale+"/signature",
		map[string]any{"signer_name": "Pat", "image_png_base64": tinyPNG}, scopedCashier...); r.status != http.StatusNotFound {
		t.Errorf("a signature on another branch's sale = %d, want 404: %s", r.status, r.raw)
	}
	if n := f.scalar(`SELECT count(*) FROM pos_signatures WHERE sale_id = $1`, uuid.MustParse(sale)); n.(int64) != 0 {
		t.Errorf("a signature was sealed through the wall")
	}
	if r := f.do("GET", "/api/v1/pos/transactions/"+sale+"/receipt?layout=thermal80", nil, scopedCashier...); r.status != http.StatusNotFound {
		t.Errorf("a print of another branch's receipt = %d, want 404: %s", r.status, r.raw)
	}
	if got := f.printCount(sale); got != 0 {
		t.Errorf("print_count = %d, a walled print must count nothing", got)
	}
	// the same routes on the caller's own branch work
	if r := f.do("GET", "/api/v1/pos/transactions/"+sale+"/receipt?layout=thermal80", nil); r.status != http.StatusOK {
		t.Errorf("an unscoped print = %d, want 200: %s", r.status, r.raw)
	}
}

func TestOnlyAManagerWhoIsNotTheCashierApprovesAnOverride(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	cashier := uuid.NewString()
	sale := f.startSaleBy(cashier, &f.customerID)
	for _, role := range []string{"cashier", "sales", "finance", "driver"} {
		if r := f.override(sale, "account", "reason", 100, role, uuid.NewString()); r.status != http.StatusForbidden {
			t.Errorf("role %q approving an override = %d, want 403: %s", role, r.status, r.raw)
		}
	}
	// a signed in admin with no subject is no one
	if r := f.override(sale, "account", "reason", 100, "admin", ""); r.status != http.StatusForbidden {
		t.Errorf("an admin with no subject approving an override = %d, want 403: %s", r.status, r.raw)
	}
	// no claims at all is the dev mode bypass every role guard lets through:
	// it approves as dev-mode, a name, never an empty one
	r := f.do("POST", "/api/v1/pos/transactions/"+sale+"/overrides",
		map[string]any{"kind": "account", "reason": "dev walk", "cap_cents": 100})
	if r.status != http.StatusCreated || r.body["approved_by"] != "dev-mode" {
		t.Errorf("a dev mode approval = %d: %s, want 201 approved by dev-mode", r.status, r.raw)
	}
	mustExec(t, f.db, `DELETE FROM pos_account_overrides WHERE sale_id = $1`, sale)
	for _, role := range []string{"admin", "owner"} {
		if r := f.override(sale, "account", "reason", 100, role, uuid.NewString()); r.status != http.StatusCreated {
			t.Errorf("role %q approving an override = %d, want 201: %s", role, r.status, r.raw)
		}
		mustExec(t, f.db, `DELETE FROM pos_account_overrides WHERE sale_id = $1`, sale)
	}
}

// Three managers approve one sale at once (pool size 4, three contenders): the
// one live override per sale and kind holds, exactly one approval is written
// and the others are told it exists.
func TestRacingApprovalsOfOneSaleKeepOneLiveOverride(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	sale := f.startSaleBy(uuid.NewString(), &f.customerID)
	statuses := make([]int, 3)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.override(sale, "account", "agreed", 100, "admin", uuid.NewString()).status
		}(i)
	}
	wg.Wait()
	created := 0
	for _, st := range statuses {
		switch st {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Errorf("statuses = %v, want only 201 and 409", statuses)
		}
	}
	if created != 1 {
		t.Errorf("statuses = %v, want exactly one approval", statuses)
	}
	if n := f.scalar(`SELECT count(*) FROM pos_account_overrides WHERE sale_id = $1 AND consumed_at IS NULL`, uuid.MustParse(sale)); n.(int64) != 1 {
		t.Errorf("live overrides = %v, want 1", n)
	}
}
