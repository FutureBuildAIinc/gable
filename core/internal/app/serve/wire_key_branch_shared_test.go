// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// sharedCustomerFixture is a customer held by the branches given (with a
// ship-to, a contact and a salesperson to assign).
type sharedCustomerFixture struct {
	id, shipTo, contact uuid.UUID
	salesperson         uuid.UUID
	termsA, termsB      uuid.UUID
	priceLevel          uuid.UUID
}

func seedSharedCustomer(t *testing.T, f *keyPinFixture, branches ...uuid.UUID) *sharedCustomerFixture {
	t.Helper()
	ctx := context.Background()
	s := &sharedCustomerFixture{id: uuid.New(), shipTo: uuid.New(), contact: uuid.New(), salesperson: uuid.New()}
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := f.db.Pool.Exec(ctx, stmt, args...); err != nil {
			t.Fatalf("seed shared customer: %v", err)
		}
	}
	exec(`INSERT INTO customers (id, name, account_number, primary_branch_id, credit_limit) VALUES ($1, 'pin shared', $2, $3, 500)`,
		s.id, "PINSH-"+s.id.String()[:8], f.branchA)
	for _, b := range branches {
		exec(`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, s.id, b)
	}
	exec(`INSERT INTO customer_ship_tos (id, customer_id, code, name, line1) VALUES ($1, $2, 'shared', 'pin shared', 'pin')`, s.shipTo, s.id)
	exec(`INSERT INTO customer_contacts (id, customer_id, first_name, last_name) VALUES ($1, $2, 'pin', 'shared')`, s.contact, s.id)
	exec(`INSERT INTO sales_team (id, name, email, role) VALUES ($1, 'pin rep', $2, 'sales')`, s.salesperson, "pin-rep-"+s.salesperson.String()[:8]+"@example.com")
	rows, err := f.db.Pool.Query(ctx, `SELECT id FROM payment_terms ORDER BY code LIMIT 2`)
	if err != nil {
		t.Fatal(err)
	}
	var terms []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		terms = append(terms, id)
	}
	rows.Close()
	if len(terms) < 2 {
		t.Fatalf("the migrations seed %d payment terms, want 2", len(terms))
	}
	s.termsA, s.termsB = terms[0], terms[1]
	if err := f.db.Pool.QueryRow(ctx, `SELECT id FROM price_levels ORDER BY id LIMIT 1`).Scan(&s.priceLevel); err != nil {
		t.Fatalf("a price level: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1 AND action LIKE 'customer.%'`, s.id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'customer' AND entity_id = $1::text`, s.id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customer_contacts WHERE customer_id = $1`, s.id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customer_ship_tos WHERE customer_id = $1`, s.id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = $1`, s.id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, s.id)
		_, _ = f.db.Pool.Exec(ctx, `UPDATE customers SET salesperson_id = NULL WHERE salesperson_id = $1`, s.salesperson)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM sales_team WHERE id = $1`, s.salesperson)
	})
	return s
}

// customerDigest hashes a customer's whole row, so a refused write that
// changed any column shows.
func (f *keyPinFixture) customerDigest(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var d string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT md5(row_to_json(c)::text) FROM customers c WHERE id = $1`, id).Scan(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

// readJSON reads a record as the admin user, as a map.
func (f *keyPinFixture) readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	status, buf := f.user(t, "GET", path, "", nil)
	if status != http.StatusOK {
		t.Fatalf("read %s: %d %s", path, status, buf)
	}
	var m map[string]any
	if err := json.Unmarshal(buf, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// sharedChangeRows reads the audit rows a dealer wide key's change to a
// shared customer left, oldest first.
func (f *keyPinFixture) sharedChangeRows(t *testing.T, customer uuid.UUID) []map[string]any {
	t.Helper()
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT actor_kind, actor_id::text, changes FROM audit_log
		 WHERE action = 'customer.shared_restricted_changed' AND entity_id = $1 ORDER BY created_at, id`, customer)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var kind, actorID string
		var raw []byte
		if err := rows.Scan(&kind, &actorID, &raw); err != nil {
			t.Fatal(err)
		}
		var changes map[string]any
		if err := json.Unmarshal(raw, &changes); err != nil {
			t.Fatal(err)
		}
		changes["_kind"], changes["_actor"] = kind, actorID
		out = append(out, changes)
	}
	return out
}

// TestKeyBranchPin_SharedCustomerRecord holds principal rulings 43 to 45: on
// a customer held by the pin and another branch, a key bound to the pin reads
// everything and changes only the branch local fields (email, phone), every
// other field of the record being restricted by default; it may add a ship-to
// or a contact but change or delete none. A customer the pin holds alone
// allows all of it. The unbound key changes the restricted fields, each change
// audited with the actor and the before and after values; a user is unchanged.
func TestKeyBranchPin_SharedCustomerRecord(t *testing.T) {
	f, _ := round4Fixture(t)
	s := seedSharedCustomer(t, f, f.branchA, f.branchB)
	own := seedSharedCustomer(t, f, f.branchA)
	sh := "/api/v1/customers/" + s.id.String()
	alone := "/api/v1/customers/" + own.id.String()

	// put replaces the customer's header with its current values and the
	// change given, on the revision it reads.
	put := func(who, path string, change func(cur map[string]any) map[string]any) (int, []byte) {
		cur := f.readJSON(t, path)
		body := map[string]any{"revision": cur["revision"]}
		for _, k := range []string{"account_number", "name", "email", "phone", "address", "tier", "is_active",
			"price_level_id", "salesperson_id", "credit_limit_cents", "currency", "payment_terms_id", "po_required"} {
			body[k] = cur[k]
		}
		for k, v := range change(cur) {
			body[k] = v
		}
		raw, _ := json.Marshal(body)
		return f.send(t, who, "PUT", path, string(raw), nil)
	}
	n := 0
	other := func(cur any, options ...any) any {
		for _, o := range options {
			if o != cur {
				return o
			}
		}
		return nil
	}
	fields := []struct {
		name string
		do   func(who, path string) (int, []byte)
	}{
		{"credit_limit_cents", func(who, path string) (int, []byte) {
			n++
			return put(who, path, func(map[string]any) map[string]any { return map[string]any{"credit_limit_cents": 100000 + n} })
		}},
		{"tier", func(who, path string) (int, []byte) {
			return put(who, path, func(c map[string]any) map[string]any {
				return map[string]any{"tier": other(c["tier"], "silver", "gold")}
			})
		}},
		{"price_level_id", func(who, path string) (int, []byte) {
			return put(who, path, func(c map[string]any) map[string]any {
				return map[string]any{"price_level_id": other(c["price_level_id"], s.priceLevel.String(), nil)}
			})
		}},
		{"payment_terms_id", func(who, path string) (int, []byte) {
			return put(who, path, func(c map[string]any) map[string]any {
				return map[string]any{"payment_terms_id": other(c["payment_terms_id"], s.termsA.String(), s.termsB.String())}
			})
		}},
		{"currency", func(who, path string) (int, []byte) {
			return put(who, path, func(c map[string]any) map[string]any {
				return map[string]any{"currency": other(c["currency"], "USD", nil)}
			})
		}},
		{"po_required", func(who, path string) (int, []byte) {
			return put(who, path, func(c map[string]any) map[string]any {
				return map[string]any{"po_required": !(c["po_required"] == true)}
			})
		}},
		{"name", func(who, path string) (int, []byte) {
			n++
			return put(who, path, func(map[string]any) map[string]any { return map[string]any{"name": fmt.Sprintf("pin shared %d", n)} })
		}},
		{"address", func(who, path string) (int, []byte) {
			n++
			return put(who, path, func(map[string]any) map[string]any { return map[string]any{"address": fmt.Sprintf("%d pin street", n)} })
		}},
		{"account_number", func(who, path string) (int, []byte) {
			n++
			return put(who, path, func(map[string]any) map[string]any {
				return map[string]any{"account_number": fmt.Sprintf("PINAN-%d-%s", n, uuid.NewString()[:6])}
			})
		}},
		{"is_active", func(who, path string) (int, []byte) {
			return put(who, path, func(c map[string]any) map[string]any { return map[string]any{"is_active": !(c["is_active"] == true)} })
		}},
		{"salesperson_id", func(who, path string) (int, []byte) {
			cur := f.readJSON(t, path)
			var sp any = s.salesperson.String()
			if cur["salesperson_id"] != nil {
				sp = nil
			}
			raw, _ := json.Marshal(map[string]any{"salesperson_id": sp, "revision": cur["revision"]})
			return f.send(t, who, "PATCH", path+"/salesperson", string(raw), nil)
		}},
		{"escalation_policy", func(who, path string) (int, []byte) {
			cur := f.readJSON(t, path)
			n++
			raw, _ := json.Marshal(map[string]any{"policy": "require_ack", "threshold_percent": fmt.Sprintf("%d.5", 1+n%40), "revision": cur["revision"]})
			return f.send(t, who, "PUT", path+"/escalation-policy", string(raw), nil)
		}},
	}

	// A branch local field passes on the shared customer for the bound key.
	for _, c := range []string{"phone", "email"} {
		status, buf := put("bound", sh, func(map[string]any) map[string]any {
			n++
			if c == "email" {
				return map[string]any{"email": fmt.Sprintf("pin%d@example.com", n)}
			}
			return map[string]any{"phone": fmt.Sprintf("555-01%02d", n%100)}
		})
		if status != http.StatusOK {
			t.Errorf("bound key changing the branch local %s of the shared customer: %d %s, want 200", c, status, buf)
		}
	}

	// Every restricted field is refused on the shared customer: 403 naming
	// branch B, one key.branch_refused row, the whole row unchanged. The read
	// of the record stays allowed.
	f.allow(t, "bound", "GET", sh, "", nil, http.StatusOK)
	f.allow(t, "bound", "GET", sh+"/escalation-policy", "", nil, http.StatusOK)
	for _, fld := range fields {
		digest, rows := f.customerDigest(t, s.id), f.auditRows(t)
		status, buf := fld.do("bound", sh)
		if status != http.StatusForbidden || !strings.Contains(string(buf), "the customer is also held by branch "+f.branchB.String()) {
			t.Errorf("bound key changing %s of the shared customer: %d %s, want a 403 naming branch B", fld.name, status, buf)
		}
		if after := f.auditRows(t); after != rows+1 {
			t.Errorf("bound key changing %s: key.branch_refused rows %d then %d, want one new row", fld.name, rows, after)
		}
		if f.customerDigest(t, s.id) != digest {
			t.Errorf("the refused change of %s still changed the shared customer's row", fld.name)
		}
	}

	// The customer the pin holds alone allows every one of them.
	for _, fld := range fields {
		if status, buf := fld.do("bound", alone); status != http.StatusOK {
			t.Errorf("bound key changing %s of the customer the pin holds alone: %d %s, want 200", fld.name, status, buf)
		}
	}

	// The dealer wide key and the user change the shared customer's
	// restricted fields; only the key leaves the audit row, naming itself
	// and the before and after value.
	for _, fld := range fields {
		before := len(f.sharedChangeRows(t, s.id))
		if status, buf := fld.do("unbound", sh); status != http.StatusOK {
			t.Errorf("unbound key changing %s of the shared customer: %d %s, want 200", fld.name, status, buf)
			continue
		}
		rows := f.sharedChangeRows(t, s.id)
		if len(rows) != before+1 {
			t.Errorf("unbound key changing %s: %d audit rows then %d, want one new row", fld.name, before, len(rows))
			continue
		}
		last := rows[len(rows)-1]
		change, ok := last[fld.name].(map[string]any)
		if !ok || last["_kind"] != "key" || last["_actor"] == "" || last["actor_key_id"] != last["_actor"] {
			t.Errorf("unbound key changing %s: the audit row does not name the key and the field: %v", fld.name, last)
			continue
		}
		if _, hasFrom := change["from"]; !hasFrom {
			t.Errorf("unbound key changing %s: the audit row has no before value: %v", fld.name, change)
		}
		if _, hasTo := change["to"]; !hasTo {
			t.Errorf("unbound key changing %s: the audit row has no after value: %v", fld.name, change)
		}
	}
	rowsBefore := len(f.sharedChangeRows(t, s.id))
	for _, fld := range fields {
		if status, buf := fld.do("user", sh); status != http.StatusOK {
			t.Errorf("user changing %s of the shared customer: %d %s, want 200", fld.name, status, buf)
		}
	}
	if rows := len(f.sharedChangeRows(t, s.id)); rows != rowsBefore {
		t.Errorf("the user's changes left %d shared customer audit rows, want none", rows-rowsBefore)
	}

	// Ship-tos and contacts: every existing one of a shared customer is
	// shared. The bound key reads and adds, and changes or deletes none.
	shipBody := func(c map[string]any, name string) string {
		raw, _ := json.Marshal(map[string]any{"code": c["code"], "name": name, "line1": c["line1"], "revision": c["revision"]})
		return string(raw)
	}
	contactBody := func(c map[string]any, last string) string {
		body := map[string]any{"last_name": last, "revision": c["revision"]}
		for _, k := range []string{"first_name", "title", "email", "phone", "role", "is_primary", "is_active", "can_place_orders", "order_limit_cents"} {
			body[k] = c[k]
		}
		raw, _ := json.Marshal(body)
		return string(raw)
	}
	f.allow(t, "bound", "GET", sh+"/ship-tos", "", nil, http.StatusOK)
	f.allow(t, "bound", "GET", sh+"/contacts", "", nil, http.StatusOK)
	f.allow(t, "bound", "GET", "/api/v1/ship-tos/"+s.shipTo.String(), "", nil, http.StatusOK)
	f.allow(t, "bound", "GET", "/api/v1/contacts/"+s.contact.String(), "", nil, http.StatusOK)
	if id := f.made(t, "bound", "", "POST", sh+"/ship-tos", `{"code":"pinnew","name":"pin new","line1":"1 pin"}`, http.StatusCreated); id != uuid.Nil {
		t.Cleanup(func() { _, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM customer_ship_tos WHERE id = $1`, id) })
	}
	if id := f.made(t, "bound", "", "POST", sh+"/contacts", `{"first_name":"pin","last_name":"new"}`, http.StatusCreated); id != uuid.Nil {
		t.Cleanup(func() { _, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM customer_contacts WHERE id = $1`, id) })
	}
	shipPath, contactPath := "/api/v1/ship-tos/"+s.shipTo.String(), "/api/v1/contacts/"+s.contact.String()
	childRefusals := []struct {
		label, method, path string
		body                func() (string, map[string]string)
	}{
		{"ship-to update", "PUT", shipPath, func() (string, map[string]string) {
			return shipBody(f.readJSON(t, shipPath), "pin renamed"), nil
		}},
		{"contact update", "PUT", contactPath, func() (string, map[string]string) {
			return contactBody(f.readJSON(t, contactPath), "renamed"), nil
		}},
		{"contact delete", "DELETE", contactPath, func() (string, map[string]string) {
			return "", map[string]string{"If-Match": fmt.Sprintf(`"%v"`, f.readJSON(t, contactPath)["revision"])}
		}},
	}
	childState := func() string {
		var d string
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT md5(COALESCE((SELECT row_to_json(s)::text FROM customer_ship_tos s WHERE id = $1), '') ||
			            COALESCE((SELECT row_to_json(c)::text FROM customer_contacts c WHERE id = $2), 'gone'))`,
			s.shipTo, s.contact).Scan(&d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, c := range childRefusals {
		before, rows := childState(), f.auditRows(t)
		body, hdr := c.body()
		status, buf := f.key(t, f.bound, c.method, c.path, body, hdr)
		if status != http.StatusForbidden || !strings.Contains(string(buf), "the customer is also held by branch "+f.branchB.String()) {
			t.Errorf("bound key %s of the shared customer: %d %s, want a 403 naming branch B", c.label, status, buf)
		}
		if after := f.auditRows(t); after != rows+1 {
			t.Errorf("bound key %s: key.branch_refused rows %d then %d, want one new row", c.label, rows, after)
		}
		if childState() != before {
			t.Errorf("the refused %s still changed the record", c.label)
		}
	}

	// The customer the pin holds alone: its ship-to and contact change and
	// delete freely for the bound key; the unbound key and the user do the
	// same on the shared customer's, each delete on a contact of its own.
	ownShip, ownContact := "/api/v1/ship-tos/"+own.shipTo.String(), "/api/v1/contacts/"+own.contact.String()
	f.allow(t, "bound", "PUT", ownShip, shipBody(f.readJSON(t, ownShip), "pin own"), nil, http.StatusOK)
	f.allow(t, "bound", "PUT", ownContact, contactBody(f.readJSON(t, ownContact), "own"), nil, http.StatusOK)
	f.allow(t, "bound", "DELETE", ownContact, "",
		map[string]string{"If-Match": fmt.Sprintf(`"%v"`, f.readJSON(t, ownContact)["revision"])}, http.StatusNoContent)
	for _, who := range []string{"unbound", "user"} {
		if status, buf := f.send(t, who, "PUT", shipPath, shipBody(f.readJSON(t, shipPath), "by "+who), nil); status != http.StatusOK {
			t.Errorf("%s changing the shared customer's ship-to: %d %s, want 200", who, status, buf)
		}
		if status, buf := f.send(t, who, "PUT", contactPath, contactBody(f.readJSON(t, contactPath), "by "+who), nil); status != http.StatusOK {
			t.Errorf("%s changing the shared customer's contact: %d %s, want 200", who, status, buf)
		}
		extra := uuid.New()
		if _, err := f.db.Pool.Exec(context.Background(),
			`INSERT INTO customer_contacts (id, customer_id, first_name, last_name) VALUES ($1, $2, 'pin', 'extra')`, extra, s.id); err != nil {
			t.Fatal(err)
		}
		status, buf := f.send(t, who, "DELETE", "/api/v1/contacts/"+extra.String(), "",
			map[string]string{"If-Match": fmt.Sprintf(`"%v"`, f.readJSON(t, "/api/v1/contacts/"+extra.String())["revision"])})
		if status != http.StatusNoContent {
			t.Errorf("%s deleting a contact of the shared customer: %d %s, want 204", who, status, buf)
		}
	}
}
