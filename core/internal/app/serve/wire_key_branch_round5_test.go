// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// Fix round 5 of the branch bound key pin: the findings of reviews 10 and 11
// and the principal's item 48 ruling, every leg through the real serve chain
// with bodies that pass validation, each foreign leg shadowed by an A control
// and measured by the rows that name the twin customer, never by whole table
// counts.

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

// round5FirstMint is the fixture's first mint (the pos and pricing scopes),
// kept for the legs that need it once the fixture has swapped to the document
// family mint.
type round5FirstMint struct{ raw, id string }

// round5Fixture is the pin fixture with the branch tax rates and the walled
// twins seeded, swapped to the document family mint; the first mint stays
// reachable for the pos legs.
func round5Fixture(t *testing.T) (*keyPinFixture, *walledTwins, round5FirstMint) {
	t.Helper()
	f := newKeyPinFixture(t)
	first := round5FirstMint{raw: f.bound, id: f.boundKeyID}
	f.bound, f.unbound, f.boundKeyID = f.bound2, f.unbound2, f.bound2KeyID
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE locations SET default_tax_rate = 0.05 WHERE id IN ($1, $2)`, f.branchA, f.branchB); err != nil {
		t.Fatalf("seed the branch tax rate: %v", err)
	}
	return f, f.seedWalledTwins(t), first
}

// auditRowsFor counts the given key's refusal rows.
func (f *keyPinFixture) auditRowsFor(t *testing.T, keyID string) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action = 'key.branch_refused' AND actor_id = $1`, keyID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestKeyBranchPin_JobOfAnotherCustomer is round 11 P1-1 and round 10 P2-2:
// an order or a quote took a job_id of another customer's project, and the
// order was priced by that customer's job rule. Every caller's build now
// refuses a job_id whose project belongs to a different customer than the
// document's (400 naming job_id, the credit memo's words), and a bound key is
// refused at the wall before that, the body's job_id held through
// projects.customer_id on the create and the update alike.
func TestKeyBranchPin_JobOfAnotherCustomer(t *testing.T) {
	f, _, _ := round5Fixture(t)
	ctx := context.Background()
	// B's job carries a fixed price JOB_OVERRIDE rule naming no customer, so a
	// document that takes the job is priced by it: without the refusal the
	// leak shows as the other customer's price.
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO pricing_rules (name, rule_type, product_id, job_id, fixed_price)
		 VALUES ('pin job override', 'JOB_OVERRIDE', $1, $2, 0.42)`, f.posProd, f.jobB); err != nil {
		t.Fatalf("seed the job rule: %v", err)
	}
	A := f.custID.String()
	orderBody := func(customer, job string) string {
		return fmt.Sprintf(`{"customer_id":%q,"job_id":%q,"delivery_type":"pickup","lines":[%s]}`,
			customer, job, f.productLine())
	}
	quoteBody := func(customer, job string) string {
		return fmt.Sprintf(`{"customer_id":%q,"job_id":%q,"lines":[%s]}`, customer, job, f.quoteLine())
	}
	refuseJob := func(label, method, path, body string) {
		t.Helper()
		for _, who := range []string{"unbound", "user"} {
			status, buf := f.send(t, who, method, path, body, nil)
			if status != http.StatusBadRequest || !strings.Contains(string(buf), "job_id") ||
				!strings.Contains(string(buf), "is not a job of this customer") {
				t.Errorf("%s: %s naming the foreign job: %d %s, want the 400 naming job_id", label, who, status, buf)
			}
		}
		before := f.auditRows(t)
		status, buf := f.key(t, f.bound, method, path, body, nil)
		if status != http.StatusForbidden || !strings.Contains(string(buf), "a branch bound key may act only on its own branch") {
			t.Errorf("%s: bound key naming the foreign job: %d %s, want the pin's 403", label, status, buf)
		}
		if after := f.auditRows(t); after != before+1 {
			t.Errorf("%s: bound key refusal rows %d then %d, want one new row", label, before, after)
		}
	}

	// The creates: the foreign job is refused for every caller, the A control
	// on the pin's own job completes and is priced by the catalogue, not by
	// the override.
	refuseJob("order create", "POST", "/api/v1/orders", orderBody(A, f.jobB.String()))
	if id := f.made(t, "bound", "orders", "POST", "/api/v1/orders", orderBody(A, f.jobA.String()), http.StatusCreated); id == uuid.Nil {
		t.Fatal("the A control order create did not answer its id")
	} else {
		doc := f.readJSON(t, "/api/v1/orders/"+id.String())
		if doc["job_id"] != f.jobA.String() {
			t.Errorf("the A control order carries job %v, want the pin's own", doc["job_id"])
		}
		if doc["subtotal_cents"] != float64(100) {
			t.Errorf("the A control order's subtotal is %v, want the catalogue's 100 (the override is on the foreign job)", doc["subtotal_cents"])
		}
	}
	refuseJob("quote create", "POST", "/api/v1/quotes", quoteBody(A, f.jobB.String()))
	f.made(t, "bound", "quotes", "POST", "/api/v1/quotes", quoteBody(A, f.jobA.String()), http.StatusCreated)

	// The updates move an existing document onto the foreign job: every caller
	// gets the same refusal on its own fresh A document (the revision is read
	// per document), and the control PUT keeps the pin's own job.
	freshOrder := func() uuid.UUID {
		return f.made(t, "bound", "orders", "POST", "/api/v1/orders", orderBody(A, f.jobA.String()), http.StatusCreated)
	}
	freshQuote := func() uuid.UUID {
		return f.made(t, "bound", "quotes", "POST", "/api/v1/quotes", quoteBody(A, f.jobA.String()), http.StatusCreated)
	}
	docPut := func(kind string, doc uuid.UUID, job string) string {
		rev := f.readJSON(t, "/api/v1/"+kind+"/"+doc.String())["revision"]
		if kind == "orders" {
			return fmt.Sprintf(`{"customer_id":%q,"job_id":%q,"delivery_type":"pickup","revision":%v,"lines":[%s]}`,
				A, job, rev, f.productLine())
		}
		return fmt.Sprintf(`{"customer_id":%q,"job_id":%q,"revision":%v,"lines":[%s]}`, A, job, rev, f.quoteLine())
	}
	for _, who := range []string{"unbound", "user"} {
		doc := freshOrder()
		if status, buf := f.send(t, who, "PUT", "/api/v1/orders/"+doc.String(), docPut("orders", doc, f.jobB.String()), nil); status != http.StatusBadRequest {
			t.Errorf("order update: %s naming the foreign job: %d %s, want the 400 naming job_id", who, status, buf)
		}
		doc = freshQuote()
		if status, buf := f.send(t, who, "PUT", "/api/v1/quotes/"+doc.String(), docPut("quotes", doc, f.jobB.String()), nil); status != http.StatusBadRequest {
			t.Errorf("quote update: %s naming the foreign job: %d %s, want the 400 naming job_id", who, status, buf)
		}
	}
	orderDoc, quoteDoc := freshOrder(), freshQuote()
	refuseJob("order update", "PUT", "/api/v1/orders/"+orderDoc.String(), docPut("orders", orderDoc, f.jobB.String()))
	refuseJob("quote update", "PUT", "/api/v1/quotes/"+quoteDoc.String(), docPut("quotes", quoteDoc, f.jobB.String()))
	f.allow(t, "bound", "PUT", "/api/v1/orders/"+orderDoc.String(), docPut("orders", orderDoc, f.jobA.String()), nil, http.StatusOK)
	f.allow(t, "bound", "PUT", "/api/v1/quotes/"+quoteDoc.String(), docPut("quotes", quoteDoc, f.jobA.String()), nil, http.StatusOK)

	// Nothing of B's side moved: no document names the foreign job (the wire's
	// job_id is the tables' project_id).
	for _, table := range []string{"orders", "quotes"} {
		var n int
		if err := f.db.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM `+table+` WHERE project_id = $1`, f.jobB).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s naming the foreign job: %d rows, want none", table, n)
		}
	}
}

// TestKeyBranchPin_QuoteConvertCustomerWall is round 11 P2-1: a key pinned
// to A converted a quote at branch A whose customer was held only by B, and
// the order took B's tax exemption. The conversion now rechecks the quote's
// stored customer under the customer wall, as the draft promotion rechecks
// its payload: a quote a caller moved onto a foreign customer is refused
// where it would become an order. The A control converts and is taxed at the
// branch rate.
func TestKeyBranchPin_QuoteConvertCustomerWall(t *testing.T) {
	f, _, _ := round5Fixture(t)
	ctx := context.Background()
	ordersOn := func(customer uuid.UUID) (n int) {
		t.Helper()
		if err := f.db.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM orders WHERE customer_id = $1`, customer).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	quoteFor := func(customer uuid.UUID) uuid.UUID {
		t.Helper()
		body := fmt.Sprintf(`{"customer_id":%q,"branch_id":%q,"lines":[%s]}`,
			customer.String(), f.branchA.String(), f.quoteLine())
		id := f.made(t, "user", "quotes", "POST", "/api/v1/quotes", body, http.StatusCreated)
		// Ruling A: the user's create linked the customer to the quote's
		// yard. The shape under test is the legacy one, a quote at A whose
		// customer A does not hold, so the link is taken back.
		if customer != f.custID {
			unlink(t, f, customer, f.branchA)
		}
		return id
	}
	convert := func(id uuid.UUID) (int, []byte) {
		t.Helper()
		rev := f.readJSON(t, "/api/v1/quotes/"+id.String())["revision"]
		return f.key(t, f.bound, "POST", "/api/v1/quotes/"+id.String()+"/convert", "",
			map[string]string{"If-Match": fmt.Sprintf(`"%v"`, rev)})
	}

	foreign := quoteFor(f.custB)
	before := ordersOn(f.custB)
	rows := f.auditRows(t)
	status, buf := convert(foreign)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "a branch bound key may act only on its own branch") {
		t.Errorf("bound key converting the quote moved onto the foreign customer: %d %s, want the pin's 403", status, buf)
	}
	if !strings.Contains(string(buf), "customer_id") {
		t.Errorf("the convert refusal does not name customer_id: %s", buf)
	}
	if after := f.auditRows(t); after != rows+1 {
		t.Errorf("convert refusal: key.branch_refused rows %d then %d, want one new row", rows, after)
	}
	if after := ordersOn(f.custB); after != before {
		t.Errorf("the refused convert still wrote %d orders on the foreign customer", after-before)
	}

	// The A control: the same quote shape on the pin's own customer converts,
	// and its tax is the branch rate, not an exemption.
	own := quoteFor(f.custID)
	status, buf = convert(own)
	if status != http.StatusCreated {
		t.Fatalf("bound key converting its own customer's quote: %d %s, want 201", status, buf)
	}
	var order struct {
		ID        uuid.UUID `json:"id"`
		TaxSource string    `json:"tax_source"`
		TaxCents  int64     `json:"tax_cents"`
	}
	if err := json.Unmarshal(buf, &order); err != nil {
		t.Fatal(err)
	}
	f.madeRow(t, "orders", order.ID)
	if order.TaxSource != "branch_rate" || order.TaxCents != 5 {
		t.Errorf("the control convert's tax is %s %d, want the branch rate and 5 cents", order.TaxSource, order.TaxCents)
	}
}

// TestKeyBranchPin_ActivityContactOwnership is round 11 P2-3: a CRM activity
// of customer A took a contact of customer B, for every caller. The create and
// the update now refuse a contact_id the activity's customer does not own, 400
// naming contact_id with the order's words, so one branch's record can never
// point at another's contact.
func TestKeyBranchPin_ActivityContactOwnership(t *testing.T) {
	f, w, _ := round5Fixture(t)
	A, conA, conB := f.custID.String(), w.conA.String(), w.conB.String()
	create := func(contact string) string {
		return fmt.Sprintf(`{"activity_type":"call","description":"pin","contact_id":%q}`, contact)
	}
	for _, who := range []string{"bound", "unbound", "user"} {
		status, buf := f.send(t, who, "POST", "/api/v1/customers/"+A+"/activities", create(conB), nil)
		if status != http.StatusBadRequest || !strings.Contains(string(buf), "contact_id") ||
			!strings.Contains(string(buf), "is not a contact of this customer") {
			t.Errorf("%s activity naming the foreign contact: %d %s, want the 400 naming contact_id", who, status, buf)
		}
	}
	created := f.made(t, "bound", "crm_activities", "POST", "/api/v1/customers/"+A+"/activities", create(conA), http.StatusCreated)
	if created == uuid.Nil {
		t.Fatal("the A control activity create did not answer its id")
	}
	put := func(contact string) string {
		return fmt.Sprintf(`{"activity_type":"call","description":"pin","contact_id":%q,"revision":1}`, contact)
	}
	for _, who := range []string{"bound", "unbound", "user"} {
		status, buf := f.send(t, who, "PUT", "/api/v1/activities/"+created.String(), put(conB), nil)
		if status != http.StatusBadRequest || !strings.Contains(string(buf), "is not a contact of this customer") {
			t.Errorf("%s activity update naming the foreign contact: %d %s, want the 400 naming contact_id", who, status, buf)
		}
	}
	f.allow(t, "bound", "PUT", "/api/v1/activities/"+created.String(), put(conA), nil, http.StatusOK)
	// No activity of the pin's customer points at the foreign contact.
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM crm_activities WHERE customer_id = $1 AND contact_id = $2`, f.custID, w.conB).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d activities of the pin's customer name the foreign contact", n)
	}
}

// TestKeyBranchPin_SharedRestrictedScope is the principal's item 48 ruling: a
// shared customer's restricted fields change only with the finance or owner
// role (a user) or an unbound machine key carrying the dedicated
// customers:restricted scope, minted only by an owner and audited when it is
// minted. A sales user, an admin user and every unbound key without the scope
// are refused in caller specific words naming the other branch (the lead in
// names the caller, item 57); a branch bound key is
// refused even with the scope; the allowed key's change keeps its
// customer.shared_restricted_changed audit row.
func TestKeyBranchPin_SharedRestrictedScope(t *testing.T) {
	f, _, _ := round5Fixture(t)
	s := seedSharedCustomer(t, f, f.branchA, f.branchB)
	sh := "/api/v1/customers/" + s.id.String()
	change := func() string {
		return f.customerReplace(t, s.id, map[string]any{"credit_limit_cents": 777000 + int(time.Now().UnixNano()%1000)})
	}

	// The mint: only an owner may grant the scope, and the mint is audited.
	mintBody := `{"name":"pin restricted","scopes":["customers:write","customers:restricted"]}`
	if status, buf := f.userAs(t, "admin", "POST", "/api/v1/admin/keys", mintBody, nil); status != http.StatusForbidden {
		t.Errorf("admin user minting the restricted scope: %d %s, want 403 (only an owner may grant it)", status, buf)
	}
	var minted struct {
		APIKey string `json:"api_key"`
		Key    struct {
			ID string `json:"id"`
		} `json:"key"`
	}
	status, buf := f.userAs(t, "owner", "POST", "/api/v1/admin/keys", mintBody, nil)
	if status != http.StatusCreated {
		t.Fatalf("owner minting the restricted scope: %d %s, want 201", status, buf)
	}
	if err := json.Unmarshal(buf, &minted); err != nil || minted.APIKey == "" {
		t.Fatalf("the mint answer carries no raw key: %s", buf)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM audit_log WHERE entity_id::text = $1`, minted.Key.ID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id::text = $1`, minted.Key.ID)
	})
	var mintAudit int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action = 'key.created' AND entity_id::text = $1 AND changes->'scopes' ? 'customers:restricted'`,
		minted.Key.ID).Scan(&mintAudit); err != nil {
		t.Fatal(err)
	}
	if mintAudit != 1 {
		t.Errorf("the restricted scope mint left %d key.created audit rows naming it, want one", mintAudit)
	}

	// The bound key with the scope is still refused (the pin rule comes first).
	if status, buf := f.key(t, f.bound, "PUT", sh, change(), nil); status != http.StatusForbidden ||
		!strings.Contains(string(buf), "this key may act only on its own branch") {
		t.Errorf("bound key changing a restricted field: %d %s, want the pin's 403", status, buf)
	}

	// An unbound key without the scope is refused in the caller words; with
	// the scope it changes the field and the change is audited.
	// The caller refusals name the lowest numbered branch holding the
	// customer (a caller without a pin has no "other" to exclude).
	lowest := f.branchA
	if f.branchB.String() < f.branchA.String() {
		lowest = f.branchB
	}
	callerRefusal := "this key may not change a shared customer's restricted fields: the customer is also held by branch " + lowest.String()
	if status, buf := f.key(t, f.unbound, "PUT", sh, change(), nil); status != http.StatusForbidden ||
		!strings.Contains(string(buf), callerRefusal) {
		t.Errorf("unbound key without the scope changing a restricted field: %d %s, want the 403 in the key's caller words", status, buf)
	}
	roleRefusal := "this role may not change a shared customer's restricted fields: the customer is also held by branch " + lowest.String()
	before := len(f.sharedChangeRows(t, s.id))
	if status, buf := f.key(t, minted.APIKey, "PUT", sh, change(), nil); status != http.StatusOK {
		t.Errorf("unbound key with the scope changing a restricted field: %d %s, want 200", status, buf)
	}
	if rows := f.sharedChangeRows(t, s.id); len(rows) != before+1 {
		t.Errorf("the scoped key's change left %d audit rows, want one new row", len(rows)-before)
	}

	// The user legs: sales and admin are refused in the role's caller words,
	// finance and owner change the field (no audit row for a user).
	for _, role := range []string{"sales", "admin"} {
		if status, buf := f.userAs(t, role, "PUT", sh, change(), nil); status != http.StatusForbidden ||
			!strings.Contains(string(buf), roleRefusal) {
			t.Errorf("%s user changing a restricted field: %d %s, want the 403 in the role's caller words", role, status, buf)
		}
	}
	rowsBefore := len(f.sharedChangeRows(t, s.id))
	for _, role := range []string{"finance", "owner"} {
		if status, buf := f.userAs(t, role, "PUT", sh, change(), nil); status != http.StatusOK {
			t.Errorf("%s user changing a restricted field: %d %s, want 200", role, status, buf)
		}
	}
	if rows := len(f.sharedChangeRows(t, s.id)); rows != rowsBefore {
		t.Errorf("the users' changes left %d shared customer audit rows, want none", rows-rowsBefore)
	}

	// The scope is never a default grant: a mint that names no scopes mints a
	// key with none, and no route requires the scope.
	plain := `{"name":"pin plain"}`
	status, buf = f.userAs(t, "owner", "POST", "/api/v1/admin/keys", plain, nil)
	if status != http.StatusCreated {
		t.Fatalf("owner minting a plain key: %d %s, want 201", status, buf)
	}
	var plainKey struct {
		Key struct {
			ID     string   `json:"id"`
			Scopes []string `json:"scopes"`
		} `json:"key"`
	}
	if err := json.Unmarshal(buf, &plainKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id::text = $1`, plainKey.Key.ID)
	})
	if len(plainKey.Key.Scopes) != 0 {
		t.Errorf("a mint naming no scopes granted %v, want none", plainKey.Key.Scopes)
	}
}

// TestKeyBranchPin_PosSyncCustomerWall is round 10 P1-1: the offline sync
// replays whole sales whose bodies name a customer, so a key pinned to A
// posting a sale on a customer held only by B (its ACCOUNT tender invoicing
// that customer at branch A) is refused at the wall with nothing written. The
// A control completes the same sale on the pin's own customer, and the user
// keeps the reach.
func TestKeyBranchPin_PosSyncCustomerWall(t *testing.T) {
	f, _, first := round5Fixture(t)
	// The register's completion deducts stock from the default (null location)
	// row; the fixture's teardown removes every inventory row of the product.
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO inventory (product_id, location_id, location, quantity) VALUES ($1, NULL, '', 50)`, f.posProd); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
	client := uuid.New()
	freshClient := func() string { client = uuid.New(); return client.String() }
	sync := func(register, customer string) string {
		return fmt.Sprintf(`{"batch_id":"pin-%s","register_id":%q,"items":[{"client_id":%q,"cashier_id":%q,`+
			`"customer_id":%q,"items":[{"product_id":%q,"quantity":"1"}],`+
			`"tenders":[{"method":"account","amount_cents":105}],"client_created_at":%q}]}`,
			uuid.NewString()[:8], register, freshClient(), uuid.NewString(), customer,
			f.posProd.String(), time.Now().UTC().Format(time.RFC3339))
	}
	invoicesOn := func(customer uuid.UUID) (n int) {
		t.Helper()
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM invoices WHERE customer_id = $1`, customer).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	txnExists := func() bool {
		t.Helper()
		var ok bool
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT EXISTS(SELECT 1 FROM pos_transactions WHERE id = $1)`, client).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	before := invoicesOn(f.custB)
	rows := f.auditRowsFor(t, first.id)
	status, buf := f.key(t, first.raw, "POST", "/api/v1/pos/sync", sync(f.regA, f.custB.String()), nil)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "a branch bound key may act only on its own branch") {
		t.Errorf("bound key pos sync naming the foreign customer: %d %s, want the pin's 403", status, buf)
	}
	if !strings.Contains(string(buf), "customer_id") {
		t.Errorf("the pos sync refusal does not name customer_id: %s", buf)
	}
	if after := f.auditRowsFor(t, first.id); after != rows+1 {
		t.Errorf("pos sync refusal: key.branch_refused rows %d then %d, want one new row", rows, after)
	}
	if after := invoicesOn(f.custB); after != before {
		t.Errorf("the refused pos sync still wrote %d invoices naming the foreign customer", after-before)
	}
	if txnExists() {
		t.Errorf("the refused pos sync still wrote its replayed transaction")
	}

	// The A control: the same sale on the pin's own customer completes and its
	// ACCOUNT tender invoices that customer, and the user keeps the same reach
	// on the foreign customer's sale at the foreign register.
	ownBefore := invoicesOn(f.custID)
	status, buf = f.key(t, first.raw, "POST", "/api/v1/pos/sync", sync(f.regA, f.custID.String()), nil)
	if status != http.StatusOK || !strings.Contains(string(buf), `"synced_count":1`) {
		t.Errorf("bound key pos sync on the pin's own customer: %d %s, want the sale synced", status, buf)
	}
	if grown := invoicesOn(f.custID) - ownBefore; grown != 1 {
		t.Errorf("the control pos sync wrote %d invoices on the pin's customer, want its ACCOUNT tender's one", grown)
	}
	f.allow(t, "user", "POST", "/api/v1/pos/sync", sync(f.regB, f.custB.String()), nil, http.StatusOK)
}
