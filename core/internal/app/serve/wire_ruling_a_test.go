// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// Ruling A (ADR 0007): customers are dealer wide accounts. Every user role
// that sells or looks a customer up reads every customer, with a header, with
// none, or as an administrator; an unbound key reads them too. A key bound to
// a branch keeps PR 83's pin, and the finance surfaces keep following grants.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// madeAs is made for a user role with headers.
func (f *keyPinFixture) madeAs(t *testing.T, role, table, method, path, body string, hdr map[string]string, want int) uuid.UUID {
	t.Helper()
	status, buf := f.userAs(t, role, method, path, body, hdr)
	if status != want {
		t.Errorf("%s %s %s: %d %s, want %d", role, method, path, status, buf, want)
		return uuid.Nil
	}
	var doc struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(buf, &doc); err != nil || doc.ID == uuid.Nil {
		return uuid.Nil
	}
	if table != "" {
		f.madeRow(t, table, doc.ID)
	}
	return doc.ID
}

// TestRulingA_CustomerReadMatrix is the read matrix: B's customer, read by
// each caller held to branch A.
func TestRulingA_CustomerReadMatrix(t *testing.T) {
	f := newKeyPinFixture(t)
	w := f.seedWalledTwins(t)
	noheaderGrant(t, f, "pin-sales", f.branchA)
	scopes := []string{"customers:read", "contacts:read", "ship-tos:read"}
	bound := f.mintWithScopes(t, "ruling-a-bound", scopes, &f.branchA)
	unbound := f.mintWithScopes(t, "ruling-a-unbound", scopes, nil)

	paths := []string{
		"/api/v1/customers/" + f.custB.String(),
		"/api/v1/contacts/" + w.conB.String(),
		"/api/v1/ship-tos/" + w.shipB.String(),
		"/api/v1/customers/" + f.custB.String() + "/contacts",
		"/api/v1/customers/" + f.custB.String() + "/ship-tos",
	}
	for _, p := range paths {
		for _, c := range []struct {
			label string
			hdr   map[string]string
		}{{"sales with header A", headerA(f)}, {"sales without header", nil}} {
			if status, buf := f.userAs(t, "sales", "GET", p, "", c.hdr); status != http.StatusOK {
				t.Errorf("%s reading %s: %d %s, want 200", c.label, p, status, buf)
			}
		}
		if status, buf := f.userAs(t, "admin", "GET", p, "", nil); status != http.StatusOK {
			t.Errorf("admin reading %s: %d %s, want 200", p, status, buf)
		}
		if status, buf := f.key(t, unbound, "GET", p, "", nil); status != http.StatusOK {
			t.Errorf("unbound key reading %s: %d %s, want 200", p, status, buf)
		}
		if status, buf := f.key(t, bound, "GET", p, "", nil); status != http.StatusNotFound && status != http.StatusForbidden {
			t.Errorf("key bound to A reading %s: %d %s, want the pin's 404 or 403", p, status, buf)
		}
	}

	// The directory: the list and the search carry B's customer for a user,
	// and never for the bound key.
	for _, p := range []string{"/api/v1/customers", "/api/v1/customers?q=pin"} {
		status, buf := f.userAs(t, "sales", "GET", p, "", headerA(f))
		if status != http.StatusOK || !strings.Contains(string(buf), f.custB.String()) {
			t.Errorf("sales with header A listing %s misses B's customer: %d %s", p, status, buf)
		}
		status, buf = f.userAs(t, "sales", "GET", p, "", nil)
		if status != http.StatusOK || !strings.Contains(string(buf), f.custB.String()) {
			t.Errorf("sales without header listing %s misses B's customer: %d %s", p, status, buf)
		}
	}
	status, buf := f.key(t, bound, "GET", "/api/v1/customers", "", nil)
	if status == http.StatusOK && strings.Contains(string(buf), f.custB.String()) {
		t.Errorf("key bound to A lists B's customer: %s", buf)
	}
}

// TestRulingA_FinanceKeepsGrants: the account, transactions and AR statement
// of B's customer stay missing for a finance user granted A only.
func TestRulingA_FinanceKeepsGrants(t *testing.T) {
	f := newKeyPinFixture(t)
	noheaderGrant(t, f, "pin-finance", f.branchA)
	for _, p := range accountSurface(f.custB) {
		for _, hdr := range []map[string]string{nil, headerA(f)} {
			if status, buf := f.userAs(t, "finance", "GET", p, "", hdr); status != http.StatusNotFound {
				t.Errorf("finance granted A reading %s (header %t): %d %s, want 404", p, hdr != nil, status, buf)
			}
		}
	}
}

// linkRows reads the customer's link to a yard and the audit rows that name
// the link (action customer.branch_linked).
func linkRows(t *testing.T, f *keyPinFixture, customer, branch uuid.UUID) (links int, audits []map[string]any) {
	t.Helper()
	ctx := context.Background()
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM customer_branches WHERE customer_id = $1 AND branch_id = $2`, customer, branch).Scan(&links); err != nil {
		t.Fatal(err)
	}
	rows, err := f.db.Pool.Query(ctx,
		`SELECT changes FROM audit_log WHERE action = 'customer.branch_linked' AND entity_type = 'customer'
		   AND entity_id = $1 AND changes->>'branch_id' = $2 ORDER BY created_at`, customer, branch.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		audits = append(audits, m)
	}
	return links, audits
}

// unlink puts the fixture back to B only, between two legs of one test.
func unlink(t *testing.T, f *keyPinFixture, customer, branch uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = $1 AND branch_id = $2`, customer, branch); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'customer.branch_linked' AND entity_id = $1`, customer); err != nil {
		t.Fatal(err)
	}
}

// TestRulingA_TheLinkFollowsTrade: a document created at yard A for a
// customer that holds only yard B adds the customer_branches row and one
// audit row naming the actor, the document and the yard, in the same
// transaction; a second document at A adds nothing.
func TestRulingA_TheLinkFollowsTrade(t *testing.T) {
	f := newKeyPinFixture(t)
	B := f.custB.String()
	line := f.productLine()
	creates := []struct {
		name, table, path string
		body              string
	}{
		{"quote", "quotes", "/api/v1/quotes", fmt.Sprintf(`{"customer_id":%q,"lines":[%s]}`, B, f.quoteLine())},
		{"order", "orders", "/api/v1/orders", fmt.Sprintf(`{"customer_id":%q,"delivery_type":"pickup","lines":[%s]}`, B, line)},
		{"credit memo", "credit_memos", "/api/v1/credit-memos", fmt.Sprintf(`{"customer_id":%q,"reason_code":"other","reason":"ruling a","lines":[`+
			`{"description":"adjustment","quantity":"-1","uom":"EA","unit_price_ten_thousandths":10000}]}`, B)},
		{"payment", "payments", "/api/v1/payments", fmt.Sprintf(`{"customer_id":%q,"amount_cents":100,"method":"cash"}`, B)},
	}
	for _, c := range creates {
		unlink(t, f, f.custB, f.branchA)
		if n, _ := linkRows(t, f, f.custB, f.branchA); n != 0 {
			t.Fatalf("%s: customer B already holds A before the document", c.name)
		}
		doc := f.madeAs(t, "admin", c.table, "POST", c.path, c.body, headerA(f), http.StatusCreated)
		if doc == uuid.Nil {
			continue
		}
		links, audits := linkRows(t, f, f.custB, f.branchA)
		if links != 1 || len(audits) != 1 {
			t.Errorf("%s at A for B's customer: %d link(s), %d audit row(s), want 1 and 1", c.name, links, len(audits))
			continue
		}
		a := audits[0]
		if a["document_id"] != doc.String() || a["document_type"] == nil || a["branch_id"] != f.branchA.String() {
			t.Errorf("%s: the audit row does not name the document and the yard: %v (document %s)", c.name, a, doc)
		}
		// A second document at A for the now linked customer adds nothing.
		f.madeAs(t, "admin", c.table, "POST", c.path, c.body, headerA(f), http.StatusCreated)
		if links, audits := linkRows(t, f, f.custB, f.branchA); links != 1 || len(audits) != 1 {
			t.Errorf("%s: a second document at A left %d link(s) and %d audit row(s), want 1 and 1", c.name, links, len(audits))
		}
	}
}
