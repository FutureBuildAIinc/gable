// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// SEC-NOHEADER-1 round 4: the findings the r5 Sonnet and r6 Opus reviews
// name on the round 3 head. Each leg is red when the guard it pins is
// reverted alone.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestNoHeader_TaxExemptionsHoldTheCustomerWall is the tax exemption family
// (r6 P1-1, r5 P2-2): a finance user granted only A, with no header or with
// header A, and a finance user with no grants at all, read, create and
// delete nothing of B's customer and preview no tax for it or its ship to,
// while A's customer answers as it did and an owner with no header keeps
// every branch.
func TestNoHeader_TaxExemptionsHoldTheCustomerWall(t *testing.T) {
	f := newKeyPinFixture(t)
	ctx := context.Background()
	noheaderGrant(t, f, "pin-finance", f.branchA)
	A, B := f.custID.String(), f.custB.String()

	shipB := uuid.New()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customer_ship_tos (id, customer_id, code, name, line1) VALUES ($1, $2, 'pin', 'pin ship to', 'pin')`,
		shipB, f.custB); err != nil {
		t.Fatalf("seed ship to: %v", err)
	}
	create := func(customer string) string {
		return fmt.Sprintf(`{"customer_id":%q,"exempt_reason":"pin"}`, customer)
	}
	preview := func(customer string) string {
		return fmt.Sprintf(`{"customer_id":%q,"document_type":"SalesInvoice","lines":[{"amount":100}]}`, customer)
	}
	previewShip := fmt.Sprintf(`{"ship_to_id":%q,"document_type":"SalesInvoice","lines":[{"amount":100}]}`, shipB)

	type caller struct {
		label string
		send  func(method, path, body string) (int, []byte)
	}
	callers := []caller{
		{"finance granted A, no header", func(m, p, b string) (int, []byte) {
			return f.userAs(t, "finance", m, p, b, nil)
		}},
		{"finance granted A, header A", func(m, p, b string) (int, []byte) {
			return f.userAs(t, "finance", m, p, b, headerA(f))
		}},
		{"finance with no grants", func(m, p, b string) (int, []byte) {
			return f.userSubAs(t, "pin-nogrants-tax", "finance", m, p, b, nil)
		}},
	}
	for _, c := range callers {
		before := f.exemptionsOf(t, f.custB)
		if status, buf := c.send("GET", "/api/v1/tax/exemptions/"+B, ""); status != http.StatusNotFound {
			t.Errorf("%s: read of B's exemptions: %d %s, want 404", c.label, status, buf)
		}
		status, buf := c.send("POST", "/api/v1/tax/exemptions", create(B))
		if status != http.StatusForbidden || !strings.Contains(string(buf), "customer_id") {
			t.Errorf("%s: create on B's customer: %d %s, want the 403 naming customer_id", c.label, status, buf)
		}
		if status, buf := c.send("DELETE", "/api/v1/tax/exemptions/"+f.taxExempt.String(), ""); status != http.StatusNotFound {
			t.Errorf("%s: delete of B's exemption: %d %s, want 404", c.label, status, buf)
		}
		if n := f.exemptionsOf(t, f.custB); n != before {
			t.Errorf("%s: B's customer holds %d exemptions after the refused verbs, want %d", c.label, n, before)
		}
		for name, body := range map[string]string{"customer": preview(B), "ship to": previewShip} {
			status, buf := c.send("POST", "/api/v1/tax/preview", body)
			if status != http.StatusForbidden || !strings.Contains(string(buf), "customer_id") {
				t.Errorf("%s: preview naming B's %s: %d %s, want the 403 naming customer_id", c.label, name, status, buf)
			}
		}
	}

	// The control: A's customer answers every verb for the granted caller.
	for _, c := range callers[:2] {
		if status, buf := c.send("GET", "/api/v1/tax/exemptions/"+A, ""); status != http.StatusOK {
			t.Errorf("%s: read of A's exemptions: %d %s, want 200", c.label, status, buf)
		}
		if status, buf := c.send("POST", "/api/v1/tax/exemptions", create(A)); status != http.StatusCreated {
			t.Errorf("%s: create on A's customer: %d %s, want 201", c.label, status, buf)
		}
		own := uuid.New()
		f.seedTaxExempt(t, own, f.custID)
		if status, buf := c.send("DELETE", "/api/v1/tax/exemptions/"+own.String(), ""); status != http.StatusNoContent {
			t.Errorf("%s: delete of A's exemption: %d %s, want 204", c.label, status, buf)
		}
		if status, buf := c.send("POST", "/api/v1/tax/preview", preview(A)); status != http.StatusOK {
			t.Errorf("%s: preview naming A's customer: %d %s, want 200", c.label, status, buf)
		}
	}

	// The owner with no header keeps every branch.
	if status, buf := f.userAs(t, "owner", "GET", "/api/v1/tax/exemptions/"+B, "", nil); status != http.StatusOK {
		t.Errorf("owner no header read of B's exemptions: %d %s, want 200", status, buf)
	}
	if status, buf := f.userAs(t, "owner", "POST", "/api/v1/tax/exemptions", create(B), nil); status != http.StatusCreated {
		t.Errorf("owner no header create on B's customer: %d %s, want 201", status, buf)
	}
	row := uuid.New()
	f.seedTaxExempt(t, row, f.custB)
	if status, buf := f.userAs(t, "owner", "DELETE", "/api/v1/tax/exemptions/"+row.String(), "", nil); status != http.StatusNoContent {
		t.Errorf("owner no header delete of B's exemption: %d %s, want 204", status, buf)
	}
	if status, buf := f.userAs(t, "owner", "POST", "/api/v1/tax/preview", preview(B), nil); status != http.StatusOK {
		t.Errorf("owner no header preview naming B's customer: %d %s, want 200", status, buf)
	}
}
