// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer_test

// The live failures the refactor inputs name for the customer module, each as
// a test with request shapes that work against the base commit too, so it
// fails there and passes now (recipe step 2). Customers are seeded with raw SQL
// (columns that exist on both sides of migration 091) so a base that cannot
// create one does not hide the failure under test.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// seedRaw makes customers of this fixture straight in the database.
func (f *fixture) seedRaw(active bool, tier string) string {
	f.t.Helper()
	id := uuid.NewString()
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO customers (id, name, account_number, tier, is_active, primary_branch_id)
		VALUES ($1, 'Raw Co', $2, $3::customer_tier, $4, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		id, f.account(), tier, active); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO customer_branches (customer_id, branch_id)
		VALUES ($1, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`, id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// LIVE FAILURE 1 (inputs section 3, item 2, silent filter no-ops): the list
// ignored every parameter it did not know and the ones it did, so a filter on
// is_active or tier returned every customer, and an unsupported parameter was
// not refused.
func TestLiveFailure_SilentFilterNoOps(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	active := f.seedRaw(true, "GOLD")
	inactive := f.seedRaw(false, "RETAIL")

	r := f.do("GET", "/api/v1/customers?is_active=false&limit=200&q="+f.prefix, nil)
	if r.status != http.StatusOK {
		t.Fatalf("is_active=false = %d: %s", r.status, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 || str(t, items[0].(map[string]any), "id") != inactive {
		t.Errorf("is_active=false served %d customers, want exactly the inactive one: %s", len(items), r.raw)
	}
	r = f.do("GET", "/api/v1/customers?tier=gold&limit=200&q="+f.prefix, nil)
	items, _ = r.body["items"].([]any)
	if len(items) != 1 || str(t, items[0].(map[string]any), "id") != active {
		t.Errorf("tier=gold served %d customers, want exactly the gold one: %s", len(items), r.raw)
	}
	if r := f.do("GET", "/api/v1/customers?nope=1", nil); r.status != http.StatusBadRequest {
		t.Errorf("an unsupported parameter = %d, want 400", r.status)
	}
}

// LIVE FAILURE 2 (inputs section 1, item 4: one envelope, never null for an
// empty collection; section 3, item 3: consistent limit and cursor paging):
// the page was {data, total, limit, offset}, the limit was clamped, and paging
// was by offset.
func TestLiveFailure_ListEnvelope(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	f.seedRaw(true, "RETAIL")
	f.seedRaw(true, "RETAIL")

	r := f.do("GET", "/api/v1/customers?limit=1&q="+f.prefix, nil)
	if _, ok := r.body["items"].([]any); !ok {
		t.Fatalf("the page has no items array: %s", r.raw)
	}
	if _, legacy := r.body["data"]; legacy {
		t.Error("the legacy data key is still on the page")
	}
	if cursor, _ := r.body["next_cursor"].(string); cursor == "" {
		t.Errorf("a page of 1 over 2 customers has no next_cursor: %s", r.raw)
	}
	if r := f.do("GET", "/api/v1/customers?limit=0", nil); r.status != http.StatusBadRequest {
		t.Errorf("limit=0 = %d, want 400 (it was clamped to a default)", r.status)
	}
	if r := f.do("GET", "/api/v1/customers?offset=1", nil); r.status != http.StatusBadRequest {
		t.Errorf("offset = %d, want 400 (paging is by cursor)", r.status)
	}
}

// LIVE FAILURE 3 (inputs section 3, item 7): error bodies of several shapes,
// the cause dropped. An unknown customer and an unknown contact are the wire
// envelope with their own message.
func TestLiveFailure_ErrorEnvelope(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	r := f.do("GET", "/api/v1/customers/"+uuid.NewString(), nil)
	if r.status != 404 {
		t.Fatalf("status = %d: %s", r.status, r.raw)
	}
	if code, message, _ := errorOf(t, r); code != "not_found" || message != "customer not found" {
		t.Errorf("error = %q %q, want not_found and the handler's own message", code, message)
	}
	// An unknown contact on an edit was a 500 with no cause.
	r = f.do("PUT", "/api/v1/contacts/"+uuid.NewString(), map[string]any{"first_name": "A", "last_name": "B", "revision": 1})
	if r.status != 404 {
		t.Errorf("editing an unknown contact = %d, want 404 (it was a 500): %s", r.status, r.raw)
	}
}

// LIVE FAILURE 4 (the quote module's missing-uom class): a create the database
// cannot take answered 500. A customer with no tier hit the enum cast, and a
// duplicate account number the unique index.
func TestLiveFailure_CreateWhatTheDatabaseCannotTake(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	first := f.do("POST", "/api/v1/customers", map[string]any{"account_number": f.account(), "name": "No Tier Co"})
	if first.status != http.StatusCreated {
		t.Fatalf("a customer with no tier = %d, want 201 with the default tier: %s", first.status, first.raw)
	}
	dup := f.do("POST", "/api/v1/customers", map[string]any{"account_number": first.body["account_number"], "name": "Again"})
	if dup.status != http.StatusConflict {
		t.Errorf("a duplicate account number = %d, want 409 (it was a 500): %s", dup.status, dup.raw)
	}
}

// LIVE FAILURE 5 (inputs section 2): customer.updated for credit limit changes;
// credit limit math used stale snapshots. A limit edit writes the event with
// the new limit.
func TestLiveFailure_CreditLimitEditIsAnEvent(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id := f.seedRaw(true, "RETAIL")
	var acct string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT account_number FROM customers WHERE id = $1`, id).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	r := f.do("PUT", "/api/v1/customers/"+id, map[string]any{"account_number": acct, "name": "Raw Co", "credit_limit_cents": 750000}, "If-Match", `"1"`)
	if r.status != http.StatusOK {
		t.Fatalf("editing the credit limit = %d, want 200 (the route did not exist): %s", r.status, r.raw)
	}
	types, data := f.events(id)
	if len(types) != 1 || types[0] != "customer.updated" || data[0]["credit_limit_cents"] == nil {
		t.Errorf("events = %v %v, want one customer.updated carrying the new limit", types, data)
	}
}
