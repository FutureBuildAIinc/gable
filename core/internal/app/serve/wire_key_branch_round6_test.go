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
	"time"

	"github.com/google/uuid"
)

// Fix round 6 (r13 P2-1): a register whose location_id names no branched
// location belongs to the branch its own pos_registers.branch_id names (the
// seeded REG-01 is one). A bound key's pos sync on it must be held to the pin,
// not written at the default branch.
func TestKeyBranchPin_PosSyncRegisterWithoutBranchedLocation(t *testing.T) {
	f, _, first := round5Fixture(t)
	ctx := context.Background()
	client := uuid.New()
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_line_items WHERE transaction_id = $1`, client)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id = $1`, client)
	})
	body := fmt.Sprintf(`{"batch_id":"pin-reg01","register_id":"REG-01","device_epoch":1,"reported_seq":0,"items":[{"client_id":%q,"register_id":"REG-01",`+
		`"cashier_id":%q,"customer_id":%q,"items":[],"tenders":[],"client_created_at":%q}]}`,
		client, uuid.NewString(), f.custID, time.Now().UTC().Format(time.RFC3339))
	status, buf := f.key(t, first.raw, "POST", "/api/v1/pos/sync", body, nil)
	var branch *uuid.UUID
	err := f.db.Pool.QueryRow(ctx, `SELECT branch_id FROM pos_transactions WHERE id = $1`, client).Scan(&branch)
	if err == nil && branch != nil && *branch != f.branchA {
		t.Errorf("bound key sync on REG-01: %d %s; the replayed transaction landed at branch %s, not the pin %s",
			status, buf, branch, f.branchA)
	}
}

// sharedRefusalRows counts the customer.shared_restricted_refused rows of a
// customer, the audit row of a user's or an unbound key's refusal.
func (f *keyPinFixture) sharedRefusalRows(t *testing.T, customer uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action = 'customer.shared_restricted_refused' AND entity_id = $1`,
		customer).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Fix round 6 (r13 P3-1) and round 7 (item 57 question 2): the unbound key
// and the user refused on a shared customer's restricted fields get a refusal
// whose lead in names the caller ("this key" for a machine key, "this role"
// for a user) ahead of the one clause naming the other branch, and leave a
// refusal row of their own; the callers the rule admits leave none.
func TestKeyBranchPin_SharedCallerRefusalRow(t *testing.T) {
	f, _, _ := round5Fixture(t)
	s := seedSharedCustomer(t, f, f.branchA, f.branchB)
	sh := "/api/v1/customers/" + s.id.String()
	change := func() string {
		return f.customerReplace(t, s.id, map[string]any{"credit_limit_cents": nextCreditLimit(555000)})
	}
	lowest := f.branchA
	if f.branchB.String() < f.branchA.String() {
		lowest = f.branchB
	}
	clause := "the customer is also held by branch " + lowest.String()

	check := func(who, lead string, status int, buf []byte, rowsBefore int) {
		t.Helper()
		if status != http.StatusForbidden || !strings.Contains(string(buf), lead+": "+clause) {
			t.Errorf("%s changing a restricted field: %d %s, want the 403 %q", who, status, buf, lead+": "+clause)
		}
		if got := f.sharedRefusalRows(t, s.id) - rowsBefore; got != 1 {
			t.Errorf("%s refusal left %d refusal rows, want one", who, got)
		}
	}
	before := f.sharedRefusalRows(t, s.id)
	status, buf := f.key(t, f.unbound, "PUT", sh, change(), nil)
	check("unbound key without the scope", "this key may not change a shared customer's restricted fields", status, buf, before)
	for _, role := range []string{"sales", "admin"} {
		before = f.sharedRefusalRows(t, s.id)
		status, buf = f.userAs(t, role, "PUT", sh, change(), nil)
		check(role+" user", "this role may not change a shared customer's restricted fields", status, buf, before)
	}
	// The row names the actor, the fields and the method.
	var kind, raw string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT changes->>'actor_kind', changes::text FROM audit_log
		 WHERE action = 'customer.shared_restricted_refused' AND entity_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
		s.id).Scan(&kind, &raw); err != nil {
		t.Fatal(err)
	}
	if kind != "user" || !strings.Contains(raw, "credit_limit_cents") || !strings.Contains(raw, "PUT") {
		t.Errorf("the refusal row reads %s, want a user actor, the refused field and the method", raw)
	}
	// Admitted callers leave no refusal row.
	before = f.sharedRefusalRows(t, s.id)
	for _, role := range []string{"finance", "owner"} {
		if status, buf := f.userAs(t, role, "PUT", sh, change(), nil); status != http.StatusOK {
			t.Errorf("%s user: %d %s, want 200", role, status, buf)
		}
	}
	if got := f.sharedRefusalRows(t, s.id) - before; got != 0 {
		t.Errorf("admitted callers left %d refusal rows, want none", got)
	}
}

// Fix round 6 (r12 P3-1): the conversion rechecks the quote's stored job
// against the quote's customer, so a legacy row whose job belongs to another
// customer cannot price the converted order. No order is written.
func TestKeyBranchPin_QuoteConvertRechecksStoredJob(t *testing.T) {
	f, _, _ := round5Fixture(t)
	ctx := context.Background()
	body := fmt.Sprintf(`{"customer_id":%q,"branch_id":%q,"lines":[%s]}`,
		f.custID.String(), f.branchA.String(), f.quoteLine())
	quote := f.made(t, "user", "quotes", "POST", "/api/v1/quotes", body, http.StatusCreated)
	if _, err := f.db.Pool.Exec(ctx, `UPDATE quotes SET project_id = $2 WHERE id = $1`, quote, f.jobB); err != nil {
		t.Fatalf("store the foreign job on the quote: %v", err)
	}
	ordersNaming := func() (n int) {
		if err := f.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM orders WHERE project_id = $1`, f.jobB).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := ordersNaming()
	rev := f.readJSON(t, "/api/v1/quotes/"+quote.String())["revision"]
	status, buf := f.send(t, "user", "POST", "/api/v1/quotes/"+quote.String()+"/convert", "",
		map[string]string{"If-Match": fmt.Sprintf(`"%v"`, rev)})
	if status != http.StatusBadRequest || !strings.Contains(string(buf), "job_id") {
		t.Errorf("converting a quote whose stored job is another customer's: %d %s, want the 400 naming job_id", status, buf)
	}
	if after := ordersNaming(); after != before {
		t.Errorf("the refused convert wrote %d orders naming the foreign job", after-before)
	}
}

// Fix round 6 (r12 P3-3): a branch bound key that truly carries
// customers:restricted is still refused on a shared customer's restricted
// field (the pin rule comes before the scope), with one refusal row; on a
// customer the pin holds alone it changes the field.
func TestKeyBranchPin_BoundKeyWithTheScopeIsRefused(t *testing.T) {
	f, _, _ := round5Fixture(t)
	scoped := f.mintWithScopes(t, "pin bound restricted",
		[]string{"customers:read", "customers:write", "customers:restricted"}, &f.branchA)
	s := seedSharedCustomer(t, f, f.branchA, f.branchB)
	change := func(id uuid.UUID) string {
		return f.customerReplace(t, id, map[string]any{"credit_limit_cents": nextCreditLimit(444000)})
	}
	// The refusal row names the path, which is this customer's alone.
	refusals := func() (n int) {
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM audit_log WHERE action = 'key.branch_refused' AND changes->>'path' = $1`,
			"/api/v1/customers/"+s.id.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	rows := refusals()
	status, buf := f.key(t, scoped, "PUT", "/api/v1/customers/"+s.id.String(), change(s.id), nil)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "this key may act only on its own branch") {
		t.Errorf("scoped bound key changing a shared customer's restricted field: %d %s, want the pin's 403", status, buf)
	}
	if after := refusals(); after != rows+1 {
		t.Errorf("scoped bound key refusal: key.branch_refused rows %d then %d, want one new row", rows, after)
	}
	// The control: a customer the pin holds alone.
	if status, buf := f.key(t, scoped, "PUT", "/api/v1/customers/"+f.custID.String(), change(f.custID), nil); status != http.StatusOK {
		t.Errorf("scoped bound key changing its own customer's restricted field: %d %s, want 200", status, buf)
	}
}

// Fix round 6 (item 54 question 1) and round 7 (principal item 57 question 1):
// the finance role reaches only the three restricted field routes of a
// customer and the two reads those writes need (the record and its escalation
// policy: finance cannot send If-Match on a record it cannot read), not every
// customer route. Customer and contact create, the other reads and the other
// writes answer finance what refactor/v1 answered (403 insufficient role); the
// restricted changes pass, on revisions finance reads itself.
func TestKeyBranchPin_FinanceReachesOnlyTheRestrictedRoutes(t *testing.T) {
	f, _, _ := round5Fixture(t)
	s := seedSharedCustomer(t, f, f.branchA, f.branchB)
	sh := "/api/v1/customers/" + s.id.String()

	refused := []struct{ method, path, body string }{
		{"POST", "/api/v1/customers", `{"name":"finance create"}`},
		{"POST", sh + "/contacts", `{"name":"finance contact"}`},
		{"POST", sh + "/ship-tos", `{"name":"finance ship to"}`},
		{"GET", "/api/v1/customers", ""},
		{"GET", sh + "/contacts", ""},
		{"GET", sh + "/ship-tos", ""},
		{"GET", "/api/v1/ship-tos/" + s.shipTo.String(), ""},
		{"GET", "/api/v1/contacts/" + s.contact.String(), ""},
		{"GET", sh + "/activities", ""},
		{"POST", sh + "/activities", `{"activity_type":"call","description":"finance activity"}`},
	}
	for _, r := range refused {
		status, buf := f.userAs(t, "finance", r.method, r.path, r.body, nil)
		if status != http.StatusForbidden || !strings.Contains(string(buf), "insufficient role") {
			t.Errorf("finance %s %s: %d %s, want the 403 insufficient role refactor/v1 answers", r.method, r.path, status, buf)
		}
	}
	// The two reads the restricted writes need (item 57 question 1): finance
	// reads the customer and its escalation policy, and nothing else opened.
	for _, path := range []string{sh, sh + "/escalation-policy"} {
		status, buf := f.userAs(t, "finance", "GET", path, "", nil)
		if status != http.StatusOK {
			t.Errorf("finance GET %s: %d %s, want 200, the read the restricted write needs", path, status, buf)
		}
	}
	// The sales user keeps the reads.
	if status, buf := f.userAs(t, "sales", "GET", sh, "", nil); status != http.StatusOK {
		t.Errorf("sales GET customer: %d %s, want 200", status, buf)
	}

	// The three restricted field routes pass for finance on the shared
	// customer, on the revisions finance reads itself.
	if status, buf := f.userAs(t, "finance", "PUT", sh,
		f.customerReplaceAs(t, "finance", s.id, map[string]any{"credit_limit_cents": 333000}), nil); status != http.StatusOK {
		t.Errorf("finance changing the credit limit: %d %s, want 200", status, buf)
	}
	cur := f.readJSONAs(t, "finance", sh)
	raw, _ := json.Marshal(map[string]any{"salesperson_id": s.salesperson.String(), "revision": cur["revision"]})
	if status, buf := f.userAs(t, "finance", "PATCH", sh+"/salesperson", string(raw), nil); status != http.StatusOK {
		t.Errorf("finance changing the salesperson: %d %s, want 200", status, buf)
	}
	esc := f.readJSONAs(t, "finance", sh+"/escalation-policy")
	raw, _ = json.Marshal(map[string]any{"policy": "require_ack", "threshold_percent": "7.5", "revision": esc["revision"]})
	if status, buf := f.userAs(t, "finance", "PUT", sh+"/escalation-policy", string(raw), nil); status != http.StatusOK {
		t.Errorf("finance setting the escalation policy: %d %s, want 200", status, buf)
	}
}

// Fix round 6 (item 54 question 2): activities follow the contact rule. An
// activity carries no branch tag, so on a customer another branch also holds
// every existing one is shared: a bound key adds one and never changes or
// deletes one, refused in the contact rule's words with a refusal row and the
// activity untouched. On a customer the pin holds alone, and for an unbound
// key and a user, nothing changes.
func TestKeyBranchPin_ActivityFollowsTheContactRule(t *testing.T) {
	f, _, _ := round5Fixture(t)
	ctx := context.Background()
	s := seedSharedCustomer(t, f, f.branchA, f.branchB)
	shared := "/api/v1/customers/" + s.id.String() + "/activities"
	create := `{"activity_type":"call","description":"logged by a user"}`

	// A bound key may add one on the shared customer (the A control).
	added := f.made(t, "bound", "crm_activities", "POST", shared, create, http.StatusCreated)
	// An activity the user logged at the other branch's request.
	logged := f.made(t, "user", "crm_activities", "POST", shared, create, http.StatusCreated)
	path := "/api/v1/activities/" + logged.String()
	state := func(id uuid.UUID) (desc string, rev int64, n int) {
		t.Helper()
		if err := f.db.Pool.QueryRow(ctx,
			`SELECT COALESCE((SELECT description FROM crm_activities WHERE id = $1), ''),
			        COALESCE((SELECT revision FROM crm_activities WHERE id = $1), 0),
			        (SELECT COUNT(*) FROM crm_activities WHERE id = $1)`, id).Scan(&desc, &rev, &n); err != nil {
			t.Fatal(err)
		}
		return desc, rev, n
	}
	_, rev, _ := state(logged)
	ifMatch := map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}
	words := "this key may act only on its own branch"

	rows := f.auditRows(t)
	status, buf := f.key(t, f.bound, "PUT", path,
		fmt.Sprintf(`{"activity_type":"call","description":"rewritten by A","revision":%d}`, rev), nil)
	if status != http.StatusForbidden || !strings.Contains(string(buf), words) || !strings.Contains(string(buf), "activity") {
		t.Errorf("bound key changing an activity on a shared customer: %d %s, want the 403 in the contact rule's words", status, buf)
	}
	if after := f.auditRows(t); after != rows+1 {
		t.Errorf("activity change refusal: key.branch_refused rows %d then %d, want one new row", rows, after)
	}
	rows = f.auditRows(t)
	status, buf = f.key(t, f.bound, "DELETE", path, "", ifMatch)
	if status != http.StatusForbidden || !strings.Contains(string(buf), words) {
		t.Errorf("bound key deleting an activity on a shared customer: %d %s, want the 403", status, buf)
	}
	if after := f.auditRows(t); after != rows+1 {
		t.Errorf("activity delete refusal: key.branch_refused rows %d then %d, want one new row", rows, after)
	}
	if desc, r, n := state(logged); n != 1 || desc != "logged by a user" || r != rev {
		t.Errorf("the refused writes changed the activity: %d rows, %q, revision %d (was %d)", n, desc, r, rev)
	}
	// A bound key also cannot change or delete the one it added: no tag says
	// it was logged at its branch.
	if status, _ := f.key(t, f.bound, "DELETE", "/api/v1/activities/"+added.String(), "",
		map[string]string{"If-Match": `"1"`}); status != http.StatusForbidden {
		t.Errorf("bound key deleting the activity it added: %d, want 403 (an activity has no branch tag)", status)
	}

	// The unbound key and the user keep the reach.
	if status, buf := f.send(t, "unbound", "PUT", path,
		fmt.Sprintf(`{"activity_type":"call","description":"changed by a dealer wide key","revision":%d}`, rev), nil); status != http.StatusOK {
		t.Errorf("unbound key changing the activity: %d %s, want 200", status, buf)
	}
	if status, buf := f.userAs(t, "sales", "DELETE", "/api/v1/activities/"+added.String(), "",
		map[string]string{"If-Match": `"1"`}); status != http.StatusNoContent {
		t.Errorf("sales user deleting an activity: %d %s, want 204", status, buf)
	}

	// The A control: a customer the pin holds alone.
	own := f.made(t, "bound", "crm_activities", "POST", "/api/v1/customers/"+f.custID.String()+"/activities", create, http.StatusCreated)
	if status, buf := f.key(t, f.bound, "PUT", "/api/v1/activities/"+own.String(),
		`{"activity_type":"call","description":"own change","revision":1}`, nil); status != http.StatusOK {
		t.Errorf("bound key changing an activity on its own customer: %d %s, want 200", status, buf)
	}
	if status, buf := f.key(t, f.bound, "DELETE", "/api/v1/activities/"+own.String(), "",
		map[string]string{"If-Match": `"2"`}); status != http.StatusNoContent {
		t.Errorf("bound key deleting an activity on its own customer: %d %s, want 204", status, buf)
	}
}
