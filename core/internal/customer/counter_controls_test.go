// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer_test

// The counter's account controls on the customer wire: the
// named buyer requirement and the check limit on the customer, the authorized
// buyer flag and per sale cap on a contact. Each is optional: left out of a
// PUT it keeps its value, and a change to one is audited with the other
// controls.

import (
	"context"
	"fmt"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestWire_CounterControlsOnTheCustomer(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	plain := f.create().body
	if plain["named_buyer_required"] != false || plain["check_limit_cents"] != nil {
		t.Errorf("defaults = %v / %v, want false and null", plain["named_buyer_required"], plain["check_limit_cents"])
	}
	c := f.create(map[string]any{"named_buyer_required": true, "check_limit_cents": 50000}).body
	id, acct := str(t, c, "id"), str(t, c, "account_number")
	if c["named_buyer_required"] != true || num(t, c, "check_limit_cents") != 50000 {
		t.Fatalf("created = %v / %v, want true and 50000", c["named_buyer_required"], c["check_limit_cents"])
	}
	var stored string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT check_limit::text FROM customers WHERE id = $1`, id).Scan(&stored); err != nil || stored != "500.00" {
		t.Errorf("stored check_limit = %q (%v), want 500.00", stored, err)
	}

	put := func(rev int64, extra map[string]any) resp {
		b := map[string]any{"account_number": acct, "name": "Wire Test Co"}
		for k, v := range extra {
			b[k] = v
		}
		return f.do("PUT", "/api/v1/customers/"+id, b, "If-Match", ifMatch(rev))
	}
	// left out, both keep their value
	r := put(1, map[string]any{"phone": "555"})
	if r.status != 200 || r.body["named_buyer_required"] != true || num(t, r.body, "check_limit_cents") != 50000 {
		t.Fatalf("a PUT that leaves them out = %d: %s, want both kept", r.status, r.raw)
	}
	// named, they change; null defers the check limit to the database default
	r = put(2, map[string]any{"named_buyer_required": false, "check_limit_cents": nil})
	if r.status != 200 || r.body["named_buyer_required"] != false || r.body["check_limit_cents"] != nil {
		t.Fatalf("a PUT that clears them = %d: %s", r.status, r.raw)
	}
	r = put(3, map[string]any{"check_limit_cents": 0})
	if r.status != 200 || num(t, r.body, "check_limit_cents") != 0 {
		t.Fatalf("a check limit of zero (no checks) = %d: %s", r.status, r.raw)
	}
	if r := put(4, map[string]any{"check_limit_cents": -1}); r.status != 400 || !hasField(detailsOf(t, r), "check_limit_cents") {
		t.Errorf("a negative check limit = %d: %s, want 400 naming check_limit_cents", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/customers", f.body(map[string]any{"check_limit_cents": "ten"})); r.status != 400 {
		t.Errorf("a check limit that is no integer = %d: %s, want 400", r.status, r.raw)
	}

	// each change is audited with the other controls
	actions, changes := f.auditRows(id)
	if len(actions) != 2 {
		t.Fatalf("audit rows = %v, want the clearing PUT and the zero limit PUT", actions)
	}
	named, _ := changes[0]["named_buyer_required"].(map[string]any)
	limit, _ := changes[0]["check_limit_cents"].(map[string]any)
	if named["from"] != true || named["to"] != false || fmt.Sprint(limit["from"]) != "50000" || limit["to"] != nil {
		t.Errorf("audit row = %v, want the named buyer and check limit changes", changes[0])
	}
}

func TestWire_CounterControlsOnAContact(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	cid := str(t, f.create().body, "id")
	base := "/api/v1/customers/" + cid + "/contacts"

	plain := f.do("POST", base, map[string]any{"first_name": "Pat", "last_name": "Lee"})
	if plain.status != 201 || plain.body["authorized_buyer"] != false || plain.body["buyer_sale_cap_cents"] != nil {
		t.Fatalf("a contact's defaults = %d: %s, want not a buyer, no cap", plain.status, plain.raw)
	}
	buyer := f.do("POST", base, map[string]any{"first_name": "Sam", "last_name": "Ray", "authorized_buyer": true, "buyer_sale_cap_cents": 12500})
	if buyer.status != 201 || buyer.body["authorized_buyer"] != true || num(t, buyer.body, "buyer_sale_cap_cents") != 12500 {
		t.Fatalf("an authorized buyer = %d: %s", buyer.status, buyer.raw)
	}
	var stored string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT buyer_sale_cap::text FROM customer_contacts WHERE id = $1`, buyer.body["id"]).Scan(&stored); err != nil || stored != "125.00" {
		t.Errorf("stored buyer_sale_cap = %q (%v), want 125.00", stored, err)
	}
	if r := f.do("POST", base, map[string]any{"first_name": "Al", "last_name": "Zed", "buyer_sale_cap_cents": 0}); r.status != 400 || !hasField(detailsOf(t, r), "buyer_sale_cap_cents") {
		t.Errorf("a zero cap = %d: %s, want 400 naming buyer_sale_cap_cents", r.status, r.raw)
	}

	path := "/api/v1/contacts/" + str(t, buyer.body, "id")
	// left out, both keep their value
	r := f.do("PUT", path, map[string]any{"first_name": "Sam", "last_name": "Ray", "title": "Buyer"}, "If-Match", `"1"`)
	if r.status != 200 || r.body["authorized_buyer"] != true || num(t, r.body, "buyer_sale_cap_cents") != 12500 {
		t.Fatalf("a PUT that leaves them out = %d: %s, want both kept", r.status, r.raw)
	}
	if actions, _ := f.auditRows(cid); len(actions) != 1 || actions[0] != "contact.authority_changed" {
		t.Errorf("a contact edit that changed no authority wrote unexpected audit rows %v, want only the buyer create row", actions)
	}
	// named, they change and the change is audited
	r = f.do("PUT", path, map[string]any{"first_name": "Sam", "last_name": "Ray", "authorized_buyer": false, "buyer_sale_cap_cents": nil}, "If-Match", `"2"`)
	if r.status != 200 || r.body["authorized_buyer"] != false || r.body["buyer_sale_cap_cents"] != nil {
		t.Fatalf("a PUT that removes the buyer = %d: %s", r.status, r.raw)
	}
	actions, changes := f.auditRows(cid)
	if len(actions) != 2 || actions[1] != "contact.authority_changed" {
		t.Fatalf("audit rows = %v, want two contact.authority_changed rows (create and clear)", actions)
	}
	flag, _ := changes[1]["authorized_buyer"].(map[string]any)
	if flag["from"] != true || flag["to"] != false {
		t.Errorf("audit row = %v, want the authorized_buyer change", changes[1])
	}
}
