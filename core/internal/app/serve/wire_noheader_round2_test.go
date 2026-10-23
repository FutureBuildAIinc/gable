// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// SEC-NOHEADER-1 round 2: the families the first round's sweep missed. The
// order number spelling, the create that fell back to the default branch,
// the dashboard, the exposure routes that mount no branch middleware, the
// customer family, and the reporting surface the principal's ruling item 64
// brings under the same rule: a user with no context branch sees and writes
// the caller's granted branches only, and a cache entry of one reach never
// serves a caller of another.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gablelbm/gable/internal/testutil"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// headerA is the one branch header the granted callers of this file may send.
func headerA(f *keyPinFixture) map[string]string {
	return map[string]string{"X-Branch-Id": f.branchA.String()}
}

// userSubAs sends one request as the claims a JWT would carry, with the
// subject given: the exposure list scopes a sales role to the salesperson
// its subject names, so its subject has to be a UUID.
func (f *keyPinFixture) userSubAs(t *testing.T, sub, role, method, path, body string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Role", role)
	req.Header.Set("X-Test-Sub", sub)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, buf
}

// TestNoHeader_OrderByNumber is the number spelling of the order read (r1
// P2, r2 P1-1): a user granted only A, no header, reads B's order by its
// document number on the order route and the link resolver, and both answer
// the record refusal. A's number keeps answering, and an owner with no
// header still reads B by number (the section 2.3 carve out).
func TestNoHeader_OrderByNumber(t *testing.T) {
	f := newKeyPinFixture(t)
	ctx := context.Background()
	w := f.seedWalledTwins(t)
	noheaderGrant(t, f, "pin-sales", f.branchA)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	numA, numB := fmt.Sprintf("SO-9%06d", rng.Intn(1000000)), fmt.Sprintf("SO-8%06d", rng.Intn(1000000))
	if _, err := f.db.Pool.Exec(ctx, `UPDATE orders SET number = $1 WHERE id = $2`, numA, w.ordA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE orders SET number = $1 WHERE id = $2`, numB, w.ordB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `UPDATE orders SET number = NULL WHERE id IN ($1, $2)`, w.ordA, w.ordB)
	})

	for _, p := range []string{"/api/v1/orders/" + numB, "/api/v1/links/orders/" + numB} {
		status, buf := f.userAs(t, "sales", "GET", p, "", nil)
		if status != http.StatusForbidden || !strings.Contains(string(buf), "outside the branches this caller may target") {
			t.Errorf("no header %s for B's order number: %d %s, want the 403 the record rule serves", p, status, buf)
		}
	}
	// The same caller with the one branch header meets the repository's
	// equality wall: a foreign number reads as missing.
	status, buf := f.userAs(t, "sales", "GET", "/api/v1/orders/"+numB, "", headerA(f))
	if status != http.StatusNotFound {
		t.Errorf("header A read of B's order number: %d %s, want 404", status, buf)
	}
	// The controls: A's number answers, and an owner with no header keeps
	// B by number.
	status, buf = f.userAs(t, "sales", "GET", "/api/v1/orders/"+numA, "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), w.ordA.String()) {
		t.Errorf("no header read of A's order number: %d %s, want A's order", status, buf)
	}
	status, buf = f.userAs(t, "owner", "GET", "/api/v1/orders/"+numB, "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), w.ordB.String()) {
		t.Errorf("owner no header read of B's order number: %d %s, want B's order", status, buf)
	}
}

// TestNoHeader_CreateWithNoBranchHoldsTheGrants is the create family (r2
// P1-2): with no context branch and no body branch_id, a user's create takes
// its branch from the user's grants (exactly one granted branch: that one),
// never the deployment default; several granted branches refuse with a 400
// naming branch_id; no grants at all refuses. The payment family already
// resolves and holds its branch; quotes, orders and both draft kinds follow.
func TestNoHeader_CreateWithNoBranchHoldsTheGrants(t *testing.T) {
	f := newKeyPinFixture(t)
	ctx := context.Background()
	w := f.seedWalledTwins(t)
	var def string
	if err := f.db.Pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = 'default_branch_id'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	// The manual purchase orders this test creates on the twins' vendor,
	// outside the twins' own two rows.
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id IN
			(SELECT id FROM purchase_orders WHERE vendor_id = $1 AND id NOT IN ($2, $3))`, w.vendor, w.poA, w.poB)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE vendor_id = $1 AND id NOT IN ($2, $3)`, w.vendor, w.poA, w.poB)
	})
	// The order create needs a tax answer that needs no branch rate: an
	// exemption on the customer (the resolver's first arm).
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO tax_exemptions (id, customer_id, exempt_reason, certificate_number, issuing_state)
		 VALUES ($1, $2, 'pin noheader', 'PIN-NH-1', 'WA')`, uuid.New(), f.custID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM tax_exemptions WHERE customer_id = $1 AND certificate_number = 'PIN-NH-1'`, f.custID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM quote_lines WHERE quote_id IN (SELECT id FROM quotes WHERE customer_id = $1 AND branch_id = $2)`, f.custID, def)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id::text FROM quotes WHERE customer_id = $1 AND branch_id = $2)`, f.custID, def)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1 AND branch_id = $2`, f.custID, def)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1 AND branch_id = $2)`, f.custID, def)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'order' AND entity_id IN (SELECT id::text FROM orders WHERE customer_id = $1 AND branch_id = $2)`, f.custID, def)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1 AND branch_id = $2`, f.custID, def)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM draft_events WHERE draft_id IN (SELECT id FROM drafts WHERE payload->>'customer_id' = $1::text AND branch_id = $2)`, f.custID, def)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM drafts WHERE payload->>'customer_id' = $1::text AND branch_id = $2`, f.custID, def)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM quote_lines WHERE quote_id IN (SELECT id FROM quotes WHERE customer_id = $1 AND branch_id = $2)`, f.custID, f.branchA)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id::text FROM quotes WHERE customer_id = $1 AND branch_id = $2)`, f.custID, f.branchA)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1 AND branch_id = $2`, f.custID, f.branchA)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1 AND branch_id = $2)`, f.custID, f.branchA)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'order' AND entity_id IN (SELECT id::text FROM orders WHERE customer_id = $1 AND branch_id = $2)`, f.custID, f.branchA)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1 AND branch_id = $2`, f.custID, f.branchA)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM draft_events WHERE draft_id IN (SELECT id FROM drafts WHERE payload->>'customer_id' = $1::text AND branch_id = $2)`, f.custID, f.branchA)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM drafts WHERE payload->>'customer_id' = $1::text AND branch_id = $2`, f.custID, f.branchA)
	})

	var sku string
	if err := f.db.Pool.QueryRow(ctx, `SELECT sku FROM products WHERE id = $1`, f.posProd).Scan(&sku); err != nil {
		t.Fatal(err)
	}
	line := `{"sku":"` + sku + `","description":"pin noheader","quantity":"1","uom":"EA","unit_price_ten_thousandths":10000}`
	quoteBody := `{"customer_id":"` + f.custID.String() + `","lines":[` + line + `]}`
	orderBody := `{"customer_id":"` + f.custID.String() + `","delivery_type":"pickup","lines":[` + line + `]}`
	poBody := `{"vendor_id":"` + w.vendor.String() + `","lines":[]}`
	creates := []struct {
		path  string
		body  string
		table string
		field string
	}{
		{"/api/v1/quotes", quoteBody, "quotes", "branch_id"},
		{"/api/v1/orders", orderBody, "orders", "branch_id"},
		{"/api/v1/drafts/quotes", `{"payload":{"customer_id":"` + f.custID.String() + `"}}`, "drafts", "payload.branch_id"},
		{"/api/v1/drafts/orders", `{"payload":{"customer_id":"` + f.custID.String() + `","delivery_type":"pickup"}}`, "drafts", "payload.branch_id"},
	}
	countAt := func(table, branch string) int {
		return noheaderCount(t, f, `SELECT COUNT(*) FROM `+table+` WHERE branch_id = $1`, branch)
	}
	// The manual purchase order create (r4 P1-2, r3 P2-3) runs as the
	// purchasing role its route guard admits.
	poCreate := func(body string) (int, []byte) {
		return f.userAs(t, "purchasing", "POST", "/api/v1/purchase-orders", body, nil)
	}
	// The AP bill create (r4 P3-5) runs as the finance role its route guard
	// admits, on the twins' vendor and the seeded 5020 expense account.
	var expense string
	if err := f.db.Pool.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE code = '5020'`).Scan(&expense); err != nil {
		t.Fatal(err)
	}
	billRun := uuid.NewString()[:8]
	// The dates come from the current date,, not a literal one.
	billInvoiceDate := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	billDueDate := time.Now().AddDate(0, 0, 23).Format("2006-01-02")
	billBody := `{"vendor_id":"` + w.vendor.String() + `","vendor_invoice_number":"PIN-NH3-` + billRun +
		`","invoice_date":"` + billInvoiceDate + `","due_date":"` + billDueDate + `","lines":[{"description":"pin noheader","quantity":"1",` +
		`"unit_price_ten_thousandths":10000,"gl_account_id":"` + expense + `"}]}`
	billCreate := func() (int, []byte) {
		return f.userAs(t, "finance", "POST", "/api/v1/ap/invoices", billBody, nil)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'vendor_invoice'
			AND entity_id IN (SELECT id::text FROM vendor_invoices WHERE vendor_invoice_number LIKE 'PIN-NH3-%')`)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type = 'vendor_invoice'
			AND entity_id IN (SELECT id FROM vendor_invoices WHERE vendor_invoice_number LIKE 'PIN-NH3-%')`)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM vendor_invoice_lines WHERE invoice_id IN
			(SELECT id FROM vendor_invoices WHERE vendor_invoice_number LIKE 'PIN-NH3-%')`)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM vendor_invoices WHERE vendor_invoice_number LIKE 'PIN-NH3-%'`)
	})

	// The customer create (r4 P2-3) runs as the sales role its route guard
	// admits; the body names no primary_branch_id, so the grants decide.
	custRun := uuid.NewString()[:8]
	custBody := `{"name":"pin noheader cust ` + custRun + `","account_number":"PIN-NH3-C` + custRun + `"}`
	custCreate := func() (int, []byte) {
		return f.userAs(t, "sales", "POST", "/api/v1/customers", custBody, nil)
	}
	custCount := func() int {
		return noheaderCount(t, f, `SELECT COUNT(*) FROM customers WHERE account_number = $1`, "PIN-NH3-C"+custRun)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id IN
			(SELECT id FROM customers WHERE account_number = $1)`, "PIN-NH3-C"+custRun)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customers WHERE account_number = $1`, "PIN-NH3-C"+custRun)
	})

	// No grants at all: every create refuses, nothing lands anywhere.
	for _, c := range creates {
		before := countAt(c.table, def)
		status, buf := f.userAs(t, "sales", "POST", c.path, c.body, nil)
		if status != http.StatusForbidden || !strings.Contains(string(buf), c.field) {
			t.Errorf("no grants %s: %d %s, want the 403 naming %s", c.path, status, buf, c.field)
		}
		if after := countAt(c.table, def); after != before {
			t.Errorf("no grants %s wrote %d rows at the default branch, want none", c.path, after-before)
		}
	}
	{
		before := countAt("purchase_orders", def)
		status, buf := poCreate(poBody)
		if status != http.StatusForbidden || !strings.Contains(string(buf), "branch_id") {
			t.Errorf("no grants PO: %d %s, want the 403 naming branch_id", status, buf)
		}
		if after := countAt("purchase_orders", def); after != before {
			t.Errorf("no grants PO wrote %d rows at the default branch, want none", after-before)
		}
		status, buf = billCreate()
		if status != http.StatusForbidden || !strings.Contains(string(buf), "branch_id") {
			t.Errorf("no grants AP bill: %d %s, want the 403 naming branch_id", status, buf)
		}
		status, buf = custCreate()
		if status != http.StatusForbidden || !strings.Contains(string(buf), "primary_branch_id") {
			t.Errorf("no grants customer: %d %s, want the 403 naming primary_branch_id", status, buf)
		}
		if n := custCount(); n != 0 {
			t.Errorf("no grants customer wrote %d rows, want none", n)
		}
	}

	// Several granted branches: the create cannot pick one, so it refuses
	// with a 400 naming branch_id, nothing written. The second grant is not
	// a home branch (one home per user).
	noheaderGrant(t, f, "pin-sales", f.branchA)
	noheaderGrant(t, f, "pin-purchasing", f.branchA)
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('pin-sales', $1, FALSE, 'sec-noheader')`, f.branchC); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('pin-purchasing', $1, FALSE, 'sec-noheader')`, f.branchC); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('pin-finance', $1, FALSE, 'sec-noheader')`, f.branchC); err != nil {
		t.Fatal(err)
	}
	noheaderGrant(t, f, "pin-finance", f.branchA)
	for _, c := range creates {
		before := countAt(c.table, def)
		status, buf := f.userAs(t, "sales", "POST", c.path, c.body, nil)
		if status != http.StatusBadRequest || !strings.Contains(string(buf), c.field) {
			t.Errorf("two grants %s: %d %s, want the 400 naming %s", c.path, status, buf, c.field)
		}
		if after := countAt(c.table, def); after != before {
			t.Errorf("two grants %s wrote %d rows at the default branch, want none", c.path, after-before)
		}
	}
	{
		before := countAt("purchase_orders", def)
		status, buf := poCreate(poBody)
		if status != http.StatusBadRequest || !strings.Contains(string(buf), "branch_id") {
			t.Errorf("two grants PO: %d %s, want the 400 naming branch_id", status, buf)
		}
		if after := countAt("purchase_orders", def); after != before {
			t.Errorf("two grants PO wrote %d rows at the default branch, want none", after-before)
		}
		status, buf = billCreate()
		if status != http.StatusBadRequest || !strings.Contains(string(buf), "branch_id") {
			t.Errorf("two grants AP bill: %d %s, want the 400 naming branch_id", status, buf)
		}
		status, buf = custCreate()
		if status != http.StatusBadRequest || !strings.Contains(string(buf), "primary_branch_id") {
			t.Errorf("two grants customer: %d %s, want the 400 naming primary_branch_id", status, buf)
		}
		if n := custCount(); n != 0 {
			t.Errorf("two grants customer wrote %d rows, want none", n)
		}
	}
	// A create that names one of the two granted branches writes there: the
	// manual purchase order is the family that gains the body branch.
	poNamingA := `{"vendor_id":"` + w.vendor.String() + `","branch_id":"` + f.branchA.String() + `","lines":[]}`
	status, buf := poCreate(poNamingA)
	if status != http.StatusCreated {
		t.Errorf("two grants PO naming branch_id: %d %s, want 201 at the named branch", status, buf)
	} else {
		var row struct {
			BranchID string `json:"branch_id"`
		}
		if err := json.Unmarshal(buf, &row); err != nil {
			t.Fatal(err)
		}
		if row.BranchID != f.branchA.String() {
			t.Errorf("two grants PO naming branch_id wrote at %s, want the named %s", row.BranchID, f.branchA)
		}
	}
	// A body naming a branch the user is not granted is refused 403 naming
	// branch_id with no header and with header A, nothing written (r6 P2-2:
	// the hold on the field this PR introduced).
	poNamingB := `{"vendor_id":"` + w.vendor.String() + `","branch_id":"` + f.branchB.String() + `","lines":[]}`
	for label, hdr := range map[string]map[string]string{"no header": nil, "header A": headerA(f)} {
		before := countAt("purchase_orders", f.branchB.String())
		status, buf := f.userAs(t, "purchasing", "POST", "/api/v1/purchase-orders", poNamingB, hdr)
		if status != http.StatusForbidden || !strings.Contains(string(buf), "branch_id") {
			t.Errorf("%s PO naming an ungranted branch_id: %d %s, want the 403 naming branch_id", label, status, buf)
		}
		if after := countAt("purchase_orders", f.branchB.String()); after != before {
			t.Errorf("%s PO naming an ungranted branch_id wrote %d row(s) at that branch, want none", label, after-before)
		}
	}
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = 'pin-sales' AND branch_id = $1`, f.branchC); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = 'pin-purchasing' AND branch_id = $1`, f.branchC); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = 'pin-finance' AND branch_id = $1`, f.branchC); err != nil {
		t.Fatal(err)
	}

	// Exactly one granted branch: the create lands at that branch and the
	// creator reads it back.
	for _, c := range []struct {
		path  string
		body  string
		table string
		read  string
	}{
		{"/api/v1/quotes", quoteBody, "quotes", "/api/v1/quotes"},
		{"/api/v1/orders", orderBody, "orders", "/api/v1/orders"},
		{"/api/v1/drafts/quotes", `{"payload":{"customer_id":"` + f.custID.String() + `"}}`, "drafts", "/api/v1/drafts/quotes"},
	} {
		before := countAt(c.table, def)
		status, buf := f.userAs(t, "sales", "POST", c.path, c.body, nil)
		if status != http.StatusCreated {
			t.Errorf("one grant %s: %d %s, want 201 at the granted branch", c.path, status, buf)
			continue
		}
		var row struct {
			BranchID string `json:"branch_id"`
		}
		if err := json.Unmarshal(buf, &row); err != nil {
			t.Fatalf("%s: decode %v", c.path, err)
		}
		if row.BranchID != f.branchA.String() {
			t.Errorf("one grant %s wrote at branch %s, want the granted %s", c.path, row.BranchID, f.branchA)
		}
		if after := countAt(c.table, def); after != before {
			t.Errorf("one grant %s wrote %d rows at the default branch, want none", c.path, after-before)
		}
		status, buf = f.userAs(t, "sales", "GET", c.read, "", nil)
		if status != http.StatusOK {
			t.Errorf("one grant read back %s: %d, want 200", c.read, status)
		}
	}
	{
		before := countAt("purchase_orders", def)
		status, buf := poCreate(poBody)
		if status != http.StatusCreated {
			t.Errorf("one grant PO: %d %s, want 201 at the granted branch", status, buf)
		} else {
			var row struct {
				BranchID string `json:"branch_id"`
			}
			if err := json.Unmarshal(buf, &row); err != nil {
				t.Fatal(err)
			}
			if row.BranchID != f.branchA.String() {
				t.Errorf("one grant PO wrote at branch %s, want the granted %s", row.BranchID, f.branchA)
			}
			if after := countAt("purchase_orders", def); after != before {
				t.Errorf("one grant PO wrote %d rows at the default branch, want none", after-before)
			}
		}
		status, buf = f.userAs(t, "purchasing", "GET", "/api/v1/purchase-orders", "", nil)
		if status != http.StatusOK {
			t.Errorf("one grant PO read back: %d, want 200", status)
		}
	}
	// The single grant customer create takes the grant and the creator
	// reads it back (r4 P2-3: it answered 404 and rolled back before).
	{
		status, buf := custCreate()
		if status != http.StatusCreated {
			t.Errorf("one grant customer: %d %s, want 201 at the granted branch", status, buf)
		} else {
			var row struct {
				PrimaryBranchID string `json:"primary_branch_id"`
			}
			if err := json.Unmarshal(buf, &row); err != nil {
				t.Fatal(err)
			}
			if row.PrimaryBranchID != f.branchA.String() {
				t.Errorf("one grant customer wrote at %s, want the granted %s", row.PrimaryBranchID, f.branchA)
			}
		}
		if n := custCount(); n != 1 {
			t.Errorf("one grant customer wrote %d rows, want one", n)
		}
		status, buf = f.userAs(t, "sales", "GET", "/api/v1/customers?limit=50", "", nil)
		if status != http.StatusOK || !strings.Contains(string(buf), "PIN-NH3-C"+custRun) {
			t.Errorf("one grant customer read back: %d %s, want the created row", status, buf)
		}
	}
	// The single grant AP bill takes the grant: the refusal the default
	// fallback used to serve (r4 P3-5) is gone.
	{
		status, buf := billCreate()
		if status != http.StatusCreated {
			t.Errorf("one grant AP bill: %d %s, want 201 at the granted branch", status, buf)
		} else {
			var row struct {
				BranchID string `json:"branch_id"`
			}
			if err := json.Unmarshal(buf, &row); err != nil {
				t.Fatal(err)
			}
			if row.BranchID != f.branchA.String() {
				t.Errorf("one grant AP bill wrote at %s, want the granted %s", row.BranchID, f.branchA)
			}
		}
	}
}

// TestNoHeader_Dashboard is the dashboard family (r2 P1-3 and item 64): a
// finance user granted only A, no header, sees A's figures only, and the
// cache entry an administrator primes never serves the finance caller.
// Every arm of the summary (the payments through their documents, the
// active orders, the pending dispatch, the outstanding AR), the revenue
// trend, the inventory alerts and the order status breakdown each carry a
// leg, so no single wall can be reverted unseen (r3 P2-2).
func TestNoHeader_Dashboard(t *testing.T) {
	noHeaderDashboardLeg(t)
}

// RULE: the same leg passes whichever zone the default branch is in, since
// the fixtures date their documents by the branch's own day and the dashboard
// reads that day. A UTC dated fixture fails only when the branch is behind UTC,
// and testutil.AlwaysBehindUTC (UTC minus 25 hours) is behind at every hour;
// Pacific/Pago_Pago is behind from 00:00 to 11:00 UTC only.
func TestNoHeader_DashboardInAnyYardZone(t *testing.T) {
	for _, zone := range []string{"Pacific/Kiritimati", "Pacific/Pago_Pago", testutil.AlwaysBehindUTC} {
		t.Run(zone, func(t *testing.T) {
			testutil.SetBranchDayZone(t, testutil.RequireDB(t), zone)
			noHeaderDashboardLeg(t)
		})
	}
}

func noHeaderDashboardLeg(t *testing.T) {
	t.Helper()
	f := newKeyPinFixture(t)
	w := f.seedWalledTwins(t)
	noheaderGrant(t, f, "pin-finance", f.branchA)
	// The payments arms read a payment through the invoice and the order it
	// settles, so the twins' payments carry their invoices, and the alerts
	// arm needs the product's reorder point above the yards' quantities.
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE payments SET invoice_id = (CASE id WHEN $1 THEN $3 WHEN $2 THEN $4 END)::uuid WHERE id IN ($1, $2)`,
		w.payA, w.payB, w.invA, w.invB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE products SET reorder_point = 10 WHERE id = $1`, f.posProd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `UPDATE payments SET invoice_id = NULL WHERE id IN ($1, $2)`, w.payA, w.payB)
		_, _ = f.db.Pool.Exec(context.Background(), `UPDATE products SET reorder_point = 0 WHERE id = $1`, f.posProd)
	})

	// The cache poison: an administrator with no header primes the all
	// branch entries first, so a shared cache key would hand the finance
	// caller the administrator's figures.
	for _, p := range []string{"/api/v1/dashboard/summary", "/api/v1/dashboard/order-activity",
		"/api/v1/dashboard/top-customers", "/api/v1/dashboard/revenue-trend", "/api/v1/dashboard/inventory-alerts"} {
		if status, buf := f.userAs(t, "admin", "GET", p, "", nil); status != http.StatusOK {
			t.Fatalf("admin prime %s: %d %s", p, status, buf)
		}
	}

	status, buf := f.userAs(t, "finance", "GET", "/api/v1/dashboard/summary", "", nil)
	if status != http.StatusOK {
		t.Fatalf("no header dashboard summary: %d %s", status, buf)
	}
	var sum struct {
		ActiveOrders    int   `json:"active_orders"`
		PendingDispatch int   `json:"pending_dispatch"`
		TodayRevenue    int64 `json:"today_revenue"`
		OutstandingAR   int64 `json:"outstanding_ar"`
		OutstandingCnt  int   `json:"outstanding_ar_count"`
	}
	if err := json.Unmarshal(buf, &sum); err != nil {
		t.Fatal(err)
	}
	// A and B each hold one CONFIRMED order, one PENDING delivery on it,
	// one UNPAID invoice of 10 and one settled payment of 5; the grants
	// hold A only.
	if sum.ActiveOrders != 1 {
		t.Errorf("no header summary counts %d active orders, want A's one", sum.ActiveOrders)
	}
	if sum.PendingDispatch != 1 {
		t.Errorf("no header summary counts %d pending dispatches, want A's one", sum.PendingDispatch)
	}
	if sum.TodayRevenue != 500 {
		t.Errorf("no header summary collects %d today cents, want A's 500", sum.TodayRevenue)
	}
	// Outstanding AR is the net open receivable as aging reads it: A's
	// invoice of 10, less A's open credit memo of 1 and A's unapplied cash
	// of 5, over one open invoice; B's documents do not count.
	if sum.OutstandingAR != 400 || sum.OutstandingCnt != 1 {
		t.Errorf("no header summary carries %d outstanding cents over %d invoices, want A's net 400 over one",
			sum.OutstandingAR, sum.OutstandingCnt)
	}
	status, adminBuf := f.userAs(t, "admin", "GET", "/api/v1/dashboard/summary", "", nil)
	if status != http.StatusOK {
		t.Fatalf("admin summary: %d %s", status, adminBuf)
	}
	var adminSum struct {
		ActiveOrders    int   `json:"active_orders"`
		PendingDispatch int   `json:"pending_dispatch"`
		TodayRevenue    int64 `json:"today_revenue"`
	}
	if err := json.Unmarshal(adminBuf, &adminSum); err != nil {
		t.Fatal(err)
	}
	if adminSum.ActiveOrders < 2 || adminSum.PendingDispatch < 2 || adminSum.TodayRevenue < 1000 {
		t.Errorf("admin summary counts %d orders, %d dispatches, %d today cents, want both branches' at least",
			adminSum.ActiveOrders, adminSum.PendingDispatch, adminSum.TodayRevenue)
	}
	status, buf = f.userAs(t, "finance", "GET", "/api/v1/dashboard/order-activity", "", nil)
	if status != http.StatusOK {
		t.Fatalf("no header order activity: %d %s", status, buf)
	}
	if strings.Contains(string(buf), w.ordB.String()) || strings.Contains(string(buf), "pin cust B") {
		t.Errorf("no header order activity names B's order or customer: %s", buf)
	}
	if !strings.Contains(string(buf), w.ordA.String()) {
		t.Errorf("no header order activity misses A's order: %s", buf)
	}
	// The status breakdown arm: A's one CONFIRMED order only.
	var activity struct {
		StatusBreakdown map[string]int `json:"status_breakdown"`
	}
	if err := json.Unmarshal(buf, &activity); err != nil {
		t.Fatal(err)
	}
	if activity.StatusBreakdown["CONFIRMED"] != 1 {
		t.Errorf("no header status breakdown counts %d CONFIRMED, want A's one: %v", activity.StatusBreakdown["CONFIRMED"], activity.StatusBreakdown)
	}
	status, buf = f.userAs(t, "finance", "GET", "/api/v1/dashboard/top-customers", "", nil)
	if status != http.StatusOK || strings.Contains(string(buf), f.custB.String()) {
		t.Errorf("no header top customers names B's customer: %d %s", status, buf)
	}
	// The revenue trend arm: A's payment only, by its figure (the trend
	// carries no customer names to match on).
	status, buf = f.userAs(t, "finance", "GET", "/api/v1/dashboard/revenue-trend", "", nil)
	if status != http.StatusOK {
		t.Fatalf("no header revenue trend: %d %s", status, buf)
	}
	var trend []struct {
		Revenue int64 `json:"revenue"`
	}
	if err := json.Unmarshal(buf, &trend); err != nil {
		t.Fatal(err)
	}
	var trendTotal int64
	for _, p := range trend {
		trendTotal += p.Revenue
	}
	if trendTotal != 500 {
		t.Errorf("no header revenue trend sums %d cents, want A's 500", trendTotal)
	}
	// The inventory alerts arm: A's yard only, through the stocking
	// location's branch; an alert is one row per product and yard, and its
	// location is the yard (the branch), the sum of the yard's own row and
	// every zone and bin under it (item 130, N20).
	status, buf = f.userAs(t, "finance", "GET", "/api/v1/dashboard/inventory-alerts", "", nil)
	if status != http.StatusOK {
		t.Fatalf("no header inventory alerts: %d %s", status, buf)
	}
	var alerts []struct {
		ProductID  string `json:"product_id"`
		CurrentQty int    `json:"current_qty"`
		LocationID string `json:"location_id"`
	}
	if err := json.Unmarshal(buf, &alerts); err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, a := range alerts {
		if a.ProductID == f.posProd.String() {
			rows++
			if a.LocationID != f.branchA.String() {
				t.Errorf("no header inventory alert for the product sits at %s, want A's yard", a.LocationID)
			}
		}
	}
	if rows != 1 {
		t.Errorf("no header inventory alerts carry %d rows for the product, want A's yard alone", rows)
	}

	// The control: the administrator's own read still carries every branch.
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/dashboard/order-activity", "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), w.ordB.String()) {
		t.Errorf("admin no header order activity misses B's order: %d %s", status, buf)
	}
}

// TestNoHeader_ExposureOnAnotherBranchQuote is the exposure family (r2
// P1-4): the exposure routes mount the branch wall, so a user granted only A
// neither reads nor writes B's quote, with or without the header, and the
// two lists carry A's rows only.
func TestNoHeader_ExposureOnAnotherBranchQuote(t *testing.T) {
	f := newKeyPinFixture(t)
	ctx := context.Background()
	noheaderGrant(t, f, "pin-sales", f.branchA)
	setExposure := func(id uuid.UUID) {
		if _, err := f.db.Pool.Exec(ctx,
			`UPDATE quotes SET state = 'SENT', exposure_state = 'ACK_REQUIRED', exposure_dollars = 12.34 WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	setExposure(f.quoteA)
	setExposure(f.quoteB)
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `UPDATE quotes SET state = 'DRAFT', exposure_state = 'OK', exposure_dollars = 0 WHERE id IN ($1, $2)`, f.quoteA, f.quoteB)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote_exposure_event'
			AND entity_id IN (SELECT id::text FROM quote_exposure_events WHERE quote_id IN ($1, $2))`, f.quoteA, f.quoteB)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM quote_exposure_events WHERE quote_id IN ($1, $2)`, f.quoteA, f.quoteB)
	})

	before := noheaderCount(t, f, `SELECT COUNT(*) FROM quote_exposure_events WHERE quote_id = $1`, f.quoteB)
	for _, hdr := range []map[string]string{nil, headerA(f)} {
		status, buf := f.userAs(t, "sales", "POST", "/api/v1/quotes/"+f.quoteB.String()+"/exposure/request-ack", "", hdr)
		if status != http.StatusForbidden && status != http.StatusNotFound {
			t.Errorf("request-ack on B's quote (header %v): %d %s, want 403 or 404", hdr != nil, status, buf)
		}
		status, buf = f.userAs(t, "sales", "GET", "/api/v1/quotes/"+f.quoteB.String()+"/exposure", "", hdr)
		if status != http.StatusNotFound {
			t.Errorf("exposure read of B's quote (header %v): %d %s, want 404", hdr != nil, status, buf)
		}
		status, buf = f.userAs(t, "sales", "POST", "/api/v1/quotes/"+f.quoteB.String()+"/exposure/escalate-now", "", hdr)
		if status != http.StatusForbidden && status != http.StatusNotFound {
			t.Errorf("escalate-now on B's quote (header %v): %d %s, want 403 or 404", hdr != nil, status, buf)
		}
	}
	if after := noheaderCount(t, f, `SELECT COUNT(*) FROM quote_exposure_events WHERE quote_id = $1`, f.quoteB); after != before {
		t.Errorf("the refused exposure acts wrote %d events on B's quote, want none", after-before)
	}

	// The at-risk list carries A's rows only. Its sales arm scopes to the
	// salesperson the subject names, so the caller is a UUID subject both
	// customers' salesperson is, granted A alone.
	sp := uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO sales_team (id, name) VALUES ($1, 'pin noheader sp')`, sp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE customers SET salesperson_id = $1 WHERE id IN ($2, $3)`, sp, f.custID, f.custB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `UPDATE customers SET salesperson_id = NULL WHERE id IN ($1, $2)`, f.custID, f.custB)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM sales_team WHERE id = $1`, sp)
	})
	noheaderGrant(t, f, sp.String(), f.branchA)
	noheaderGrant(t, f, "pin-sales", f.branchA)
	status, buf := f.userSubAs(t, sp.String(), "sales", "GET", "/api/v1/quotes/exposure", "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), f.quoteA.String()) {
		t.Errorf("no header exposure list misses A's quote: %d %s", status, buf)
	}
	if strings.Contains(string(buf), f.quoteB.String()) {
		t.Errorf("no header exposure list carries B's quote: %s", buf)
	}
	// The portfolio rollup by salesperson carries the same wall (r3 P2-2):
	// the shared salesperson's row counts the granted branch's quote only,
	// and the owner's wide rollup counts both. The rollup runs for a caller
	// whose subject is not a salesperson id, so the caller is the non UUID
	// subject the rollup branch takes.
	status, buf = f.userAs(t, "sales", "GET", "/api/v1/reports/exposure", "", nil)
	if status != http.StatusOK {
		t.Fatalf("no header exposure rollup: %d %s", status, buf)
	}
	var bySp struct {
		BySalesperson []struct {
			SalespersonID string `json:"salesperson_id"`
			QuoteCount    int    `json:"quote_count"`
		} `json:"by_salesperson"`
	}
	if err := json.Unmarshal(buf, &bySp); err != nil {
		t.Fatal(err)
	}
	spCount := 0
	for _, row := range bySp.BySalesperson {
		if row.SalespersonID == sp.String() {
			spCount = row.QuoteCount
		}
	}
	if spCount != 1 {
		t.Errorf("no header rollup counts %d quotes for the shared salesperson, want A's one: %s", spCount, buf)
	}
	status, buf = f.userAs(t, "owner", "GET", "/api/v1/reports/exposure", "", nil)
	if status != http.StatusOK {
		t.Fatalf("owner exposure rollup: %d %s", status, buf)
	}
	if err := json.Unmarshal(buf, &bySp); err != nil {
		t.Fatal(err)
	}
	spCount = 0
	for _, row := range bySp.BySalesperson {
		if row.SalespersonID == sp.String() {
			spCount = row.QuoteCount
		}
	}
	if spCount != 2 {
		t.Errorf("owner rollup counts %d quotes for the shared salesperson, want both branches' two: %s", spCount, buf)
	}
	status, buf = f.userAs(t, "sales", "GET", "/api/v1/reports/exposure?summary=true", "", nil)
	if status != http.StatusOK {
		t.Fatalf("no header exposure report: %d %s", status, buf)
	}
	var rollup struct {
		TotalQuotes int `json:"total_quotes"`
	}
	if err := json.Unmarshal(buf, &rollup); err != nil {
		t.Fatal(err)
	}
	if rollup.TotalQuotes != 1 {
		t.Errorf("no header exposure report counts %d quotes, want A's one", rollup.TotalQuotes)
	}

	// The control: A's quote answers, and an owner with no header keeps B.
	status, buf = f.userAs(t, "sales", "GET", "/api/v1/quotes/"+f.quoteA.String()+"/exposure", "", nil)
	if status != http.StatusOK {
		t.Errorf("no header exposure read of A's quote: %d %s, want 200", status, buf)
	}
	status, buf = f.userAs(t, "owner", "GET", "/api/v1/quotes/"+f.quoteB.String()+"/exposure", "", nil)
	if status != http.StatusOK {
		t.Errorf("owner no header exposure read of B's quote: %d %s, want 200", status, buf)
	}
}

// TestNoHeader_CustomerFamily is the customer family (r2 P2-1): the
// customer, contact, ship to and activity reads and writes hold the
// customer's branches (its customer_branches rows) to a no header caller's
// grants, the way the header path already answers 404.
func TestNoHeader_CustomerFamily(t *testing.T) {
	f := newKeyPinFixture(t)
	ctx := context.Background()
	w := f.seedWalledTwins(t)
	noheaderGrant(t, f, "pin-sales", f.branchA)
	// B's customer's replace body, read as the administrator the row never
	// hides from: the PUT must carry the terms and the revision it holds.
	status, body := f.userAs(t, "admin", "GET", "/api/v1/customers/"+f.custB.String(), "", nil)
	if status != http.StatusOK {
		t.Fatalf("admin read of B's customer: %d %s", status, body)
	}
	var cur struct {
		Revision      int64  `json:"revision"`
		Terms         string `json:"payment_terms_id"`
		AccountNumber string `json:"account_number"`
		POrequired    *bool  `json:"po_required"`
		JobRequired   *bool  `json:"job_required"`
	}
	if err := json.Unmarshal(body, &cur); err != nil {
		t.Fatal(err)
	}
	po := false
	if cur.POrequired != nil {
		po = *cur.POrequired
	}
	job := false
	if cur.JobRequired != nil {
		job = *cur.JobRequired
	}
	replace := fmt.Sprintf(`{"name":"renamed by noheader","account_number":%q,"payment_terms_id":%q,"po_required":%v,"job_required":%v,"revision":%d}`,
		cur.AccountNumber, cur.Terms, po, job, cur.Revision)

	// Ruling A: the customer, its contact and its ship to are dealer wide
	// reads; only the activity (a CRM record) still follows the grants.
	for _, r := range []struct {
		path string
		want string
		code int
	}{
		{"/api/v1/customers/" + f.custB.String(), "customer", http.StatusOK},
		{"/api/v1/contacts/" + w.conB.String(), "contact", http.StatusOK},
		{"/api/v1/ship-tos/" + w.shipB.String(), "ship to", http.StatusOK},
		{"/api/v1/activities/" + w.actB.String(), "activity", http.StatusNotFound},
	} {
		status, buf := f.userAs(t, "sales", "GET", r.path, "", nil)
		if status != r.code {
			t.Errorf("no header read of B's %s: %d %s, want %d", r.want, status, buf, r.code)
		}
	}
	status, buf := f.userAs(t, "sales", "GET", "/api/v1/customers", "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), f.custID.String()) {
		t.Errorf("no header customer list misses A's customer: %d %s", status, buf)
	}
	if !strings.Contains(string(buf), f.custB.String()) {
		t.Errorf("no header customer list misses B's customer (ruling A): %s", buf)
	}

	// The writes: the rename and the child creates refuse, nothing written.
	var name string
	if err := f.db.Pool.QueryRow(ctx, `SELECT name FROM customers WHERE id = $1`, f.custB).Scan(&name); err != nil {
		t.Fatal(err)
	}
	status, buf = f.userAs(t, "sales", "PUT", "/api/v1/customers/"+f.custB.String(), replace, nil)
	if status != http.StatusNotFound && status != http.StatusForbidden {
		t.Errorf("no header rename of B's customer: %d %s, want 404 or 403", status, buf)
	}
	var after string
	if err := f.db.Pool.QueryRow(ctx, `SELECT name FROM customers WHERE id = $1`, f.custB).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != name {
		t.Errorf("the refused rename changed B's customer name to %q", after)
	}
	for _, c := range []struct{ path, body, table string }{
		{"/api/v1/customers/" + f.custB.String() + "/contacts", `{"first_name":"pin","last_name":"noheader"}`, "customer_contacts"},
		{"/api/v1/customers/" + f.custB.String() + "/ship-tos", `{"code":"nh","name":"pin","line1":"pin"}`, "customer_ship_tos"},
		{"/api/v1/customers/" + f.custB.String() + "/activities", `{"activity_type":"call","description":"pin noheader"}`, "crm_activities"},
	} {
		before := noheaderCount(t, f, `SELECT COUNT(*) FROM `+c.table+` WHERE customer_id = $1`, f.custB)
		status, buf = f.userAs(t, "sales", "POST", c.path, c.body, nil)
		if status != http.StatusNotFound && status != http.StatusForbidden {
			t.Errorf("no header %s create on B's customer: %d %s, want 404 or 403", c.table, status, buf)
		}
		if n := noheaderCount(t, f, `SELECT COUNT(*) FROM `+c.table+` WHERE customer_id = $1`, f.custB); n != before {
			t.Errorf("the refused %s create wrote %d rows on B's customer", c.table, n-before)
		}
	}

	// The control: A's customer answers the same caller.
	status, buf = f.userAs(t, "sales", "GET", "/api/v1/customers/"+f.custID.String(), "", nil)
	if status != http.StatusOK {
		t.Errorf("no header read of A's customer: %d %s, want 200", status, buf)
	}
}

// TestNoHeader_Reports is the reporting surface under the principal's
// ruling (FROM-PRINCIPAL 64): a finance user with no context branch sees
// the granted branches' figures only, the customer statement of a customer
// outside the reach reads as missing, the reconciliation's customer rows
// follow the reach, and the report cache an administrator primes never
// serves the finance caller. The administrator and owner keep the all
// branch view.
func TestNoHeader_Reports(t *testing.T) {
	f := newKeyPinFixture(t)
	ctx := context.Background()
	w := f.seedWalledTwins(t)
	noheaderGrant(t, f, "pin-finance", f.branchA)
	// A drift row per customer, so the reconciliation has one row per branch
	// to hold.
	if _, err := f.db.Pool.Exec(ctx, `UPDATE customers SET balance_due = 5 WHERE id IN ($1, $2)`, f.custID, f.custB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `UPDATE customers SET balance_due = 0 WHERE id IN ($1, $2)`, f.custID, f.custB)
	})

	// The cache poison: an administrator with no header primes the reports
	// first, so a shared cache key would hand the finance caller every
	// branch's figures.
	for _, p := range []string{"/api/v1/reports/ar-aging", "/api/v1/reports/daily-till", "/api/v1/reports/sales-summary"} {
		if status, buf := f.userAs(t, "admin", "GET", p, "", nil); status != http.StatusOK {
			t.Fatalf("admin prime %s: %d %s", p, status, buf)
		}
	}

	// The AR aging carries A's bucket only.
	status, buf := f.userAs(t, "finance", "GET", "/api/v1/reports/ar-aging", "", nil)
	if status != http.StatusOK {
		t.Fatalf("no header ar aging: %d %s", status, buf)
	}
	var aging struct {
		Buckets []struct {
			CustomerID string `json:"customer_id"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(buf, &aging); err != nil {
		t.Fatal(err)
	}
	if len(aging.Buckets) != 1 || aging.Buckets[0].CustomerID != f.custID.String() {
		t.Errorf("no header ar aging carries %d buckets, want A's one: %s", len(aging.Buckets), buf)
	}

	// The daily till and the sales summary count A's money only.
	status, buf = f.userAs(t, "finance", "GET", "/api/v1/reports/daily-till", "", nil)
	if status != http.StatusOK {
		t.Fatalf("no header daily till: %d %s", status, buf)
	}
	var till struct {
		TotalCollected float64 `json:"total_collected"`
	}
	if err := json.Unmarshal(buf, &till); err != nil {
		t.Fatal(err)
	}
	if till.TotalCollected != 5 {
		t.Errorf("no header daily till collects %v, want A's 5", till.TotalCollected)
	}
	status, buf = f.userAs(t, "finance", "GET", "/api/v1/reports/sales-summary", "", nil)
	if status != http.StatusOK {
		t.Fatalf("no header sales summary: %d %s", status, buf)
	}
	var summary struct {
		InvoiceCount int     `json:"invoice_count"`
		Collected    float64 `json:"total_collected"`
		Outstanding  float64 `json:"outstanding_ar"`
	}
	if err := json.Unmarshal(buf, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.InvoiceCount != 1 || summary.Collected != 5 {
		t.Errorf("no header sales summary counts %d invoices and %v collected, want A's 1 and 5", summary.InvoiceCount, summary.Collected)
	}
	// The outstanding AR arm carries its own wall (r3 P2-2): A's open
	// invoice only, not both branches'.
	if summary.Outstanding != 10 {
		t.Errorf("no header sales summary carries %v outstanding AR, want A's 10", summary.Outstanding)
	}

	// The customer statement of B's customer reads as missing.
	status, buf = f.userAs(t, "finance", "GET", "/api/v1/reports/customer-statement/"+f.custB.String(), "", nil)
	if status != http.StatusNotFound {
		t.Errorf("no header statement of B's customer: %d %s, want 404", status, buf)
	}
	status, buf = f.userAs(t, "finance", "GET", "/api/v1/reports/customer-statement/"+f.custID.String(), "", nil)
	if status != http.StatusOK {
		t.Errorf("no header statement of A's customer: %d %s, want 200", status, buf)
	}

	// The reconciliation's customer rows follow the reach; its currency
	// half's branch owned totals follow it too, and its pure GL columns are
	// refused to a scoped caller (r4 P2-2, ruled under item 64).
	reconRead := func(role string) (customers []string, rows []map[string]json.RawMessage) {
		status, buf := f.userAs(t, role, "GET", "/api/v1/ar/reconciliation", "", nil)
		if status != http.StatusOK {
			t.Fatalf("%s reconciliation: %d %s", role, status, buf)
		}
		var recon struct {
			Customers []struct {
				CustomerID string `json:"customer_id"`
			} `json:"customers"`
			Currencies []json.RawMessage `json:"currencies"`
		}
		if err := json.Unmarshal(buf, &recon); err != nil {
			t.Fatal(err)
		}
		for _, c := range recon.Customers {
			customers = append(customers, c.CustomerID)
		}
		for _, raw := range recon.Currencies {
			var row map[string]json.RawMessage
			if err := json.Unmarshal(raw, &row); err != nil {
				t.Fatal(err)
			}
			if string(row["currency"]) == `"USD"` {
				rows = append(rows, row)
			}
		}
		return customers, rows
	}
	financeCustomers, financeRows := reconRead("finance")
	for _, c := range financeCustomers {
		if c == f.custB.String() {
			t.Errorf("no header reconciliation names B's customer")
		}
	}
	if len(financeCustomers) != 1 || financeCustomers[0] != f.custID.String() {
		t.Errorf("no header reconciliation carries %d customer rows, want A's one", len(financeCustomers))
	}
	if len(financeRows) != 1 {
		t.Fatalf("finance reconciliation carries %d USD rows, want one", len(financeRows))
	}
	for _, key := range []string{"receivable_ledger_cents", "deposits_ledger_cents", "receivable_in_sync", "deposits_in_sync"} {
		if _, ok := financeRows[0][key]; ok {
			t.Errorf("finance reconciliation carries the GL column %s, want it refused to a scoped caller", key)
		}
	}
	financeBalance := string(financeRows[0]["balance_sum_cents"])
	// r4's 777 shape: raising B's customer's balance must not move the
	// finance caller's dealer figures.
	if _, err := f.db.Pool.Exec(ctx, `UPDATE customers SET balance_due = balance_due + 777 WHERE id = $1`, f.custB); err != nil {
		t.Fatal(err)
	}
	_, after := reconRead("finance")
	if len(after) != 1 || string(after[0]["balance_sum_cents"]) != financeBalance {
		t.Errorf("raising B's customer by 777 moved the finance balance_sum from %s to %s, want it unmoved",
			financeBalance, after)
	}

	// The unapplied cash half (r6 P2-3, r5 P2-3): raising B's payment's
	// unapplied amount must not move finance's figure, with or without the
	// header, while the administrator's dealer wide figure moves with it.
	unappliedOf := func(role string, hdr map[string]string) int64 {
		t.Helper()
		status, buf := f.userAs(t, role, "GET", "/api/v1/ar/reconciliation", "", hdr)
		if status != http.StatusOK {
			t.Fatalf("%s reconciliation: %d %s", role, status, buf)
		}
		var recon struct {
			Currencies []struct {
				Currency  string `json:"currency"`
				Unapplied int64  `json:"unapplied_sum_cents"`
			} `json:"currencies"`
		}
		if err := json.Unmarshal(buf, &recon); err != nil {
			t.Fatal(err)
		}
		for _, c := range recon.Currencies {
			if c.Currency == "USD" {
				return c.Unapplied
			}
		}
		t.Fatalf("%s reconciliation carries no USD row: %s", role, buf)
		return 0
	}
	financeUn, financeUnA, adminUn := unappliedOf("finance", nil), unappliedOf("finance", headerA(f)), unappliedOf("admin", nil)
	if _, err := f.db.Pool.Exec(ctx,
		`UPDATE payments SET amount = amount + 12.34, amount_unapplied = amount_unapplied + 12.34 WHERE id = $1`, w.payB); err != nil {
		t.Fatal(err)
	}
	if got := unappliedOf("finance", nil); got != financeUn {
		t.Errorf("raising B's payment by 1234 moved the finance unapplied_sum_cents (no header) from %d to %d, want it unmoved", financeUn, got)
	}
	if got := unappliedOf("finance", headerA(f)); got != financeUnA {
		t.Errorf("raising B's payment by 1234 moved the finance unapplied_sum_cents (header A) from %d to %d, want it unmoved", financeUnA, got)
	}
	if got := unappliedOf("admin", nil); got-adminUn != 1234 {
		t.Errorf("raising B's payment by 1234 moved the administrator's unapplied_sum_cents by %d, want 1234", got-adminUn)
	}

	_, adminRows := reconRead("admin")
	if len(adminRows) != 1 {
		t.Fatalf("admin reconciliation carries %d USD rows, want one", len(adminRows))
	}
	for _, key := range []string{"receivable_ledger_cents", "deposits_ledger_cents", "receivable_in_sync", "deposits_in_sync"} {
		if _, ok := adminRows[0][key]; !ok {
			t.Errorf("admin reconciliation lost the GL column %s, want the wide caller to keep it", key)
		}
	}

	// The BI export keeps the administrator's all branch view with no
	// header, and a header administrator gets that branch's rows. (The raw
	// export encodes a uuid column as bytes, so the legs match the names.)
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/reporting/export/orders", "", nil)
	if status != http.StatusOK {
		t.Fatalf("admin no header orders export: %d %s", status, buf)
	}
	if !strings.Contains(string(buf), `"customer_name":"pin cust",`) || !strings.Contains(string(buf), `"customer_name":"pin cust B",`) {
		t.Errorf("admin no header orders export misses a branch's order: %s", buf)
	}
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/reporting/export/orders", "", headerA(f))
	if status != http.StatusOK {
		t.Fatalf("admin header A orders export: %d %s", status, buf)
	}
	if strings.Contains(string(buf), "pin cust B") || !strings.Contains(string(buf), `"customer_name":"pin cust",`) {
		t.Errorf("admin header A orders export carries B's order: %s", buf)
	}

	// The control: the administrator with no header keeps every branch's
	// figures on the walled reports.
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/reports/ar-aging", "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), f.custB.String()) {
		t.Errorf("admin no header ar aging misses B's bucket: %d %s", status, buf)
	}
}
