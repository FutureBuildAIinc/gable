// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestKeyBranchPin_DocumentRoutes drives the two classes item M3 added to the
// allowlist beside the walled prints: the statement routes are CONFINED (the
// customer in the path must hold the key's pin) and the dealer profile writes
// are REFUSED (one dealer wide row, no branch column), while its reads stay
// stated limits.
func TestKeyBranchPin_DocumentRoutes(t *testing.T) {
	f := newKeyPinFixture(t)
	// The document family mint holds the documents and dealer profile scopes.
	f.bound, f.unbound, f.boundKeyID = f.bound2, f.unbound2, f.bound2KeyID
	custA, custB := f.custID.String(), f.custB.String()

	// The statement of the foreign customer is refused the bound key, both to
	// print and to email; the pin's own customer prints; the unbound key and a
	// user reach the foreign one (the wall is the pin's alone).
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/documents/print/statement/" + custB},
		{"POST", "/api/v1/ar/customers/" + custB + "/statement/email"},
	} {
		if status, buf := f.key(t, f.bound, c.method, c.path, "", nil); status != http.StatusForbidden {
			t.Errorf("bound key %s %s: %d %s, want 403", c.method, c.path, status, buf)
		}
	}
	buf := f.allow(t, "bound", "GET", "/api/v1/documents/print/statement/"+custA, "", nil, http.StatusOK)
	if !strings.HasPrefix(string(buf), "%PDF-") {
		t.Errorf("the pin's own statement is not a PDF: %.20q", buf)
	}
	f.allow(t, "unbound", "GET", "/api/v1/documents/print/statement/"+custB, "", nil, http.StatusOK)
	f.allow(t, "user", "GET", "/api/v1/documents/print/statement/"+custB, "", nil, http.StatusOK)

	// The dealer profile: the reads pass the bound key, the writes are
	// refused, and the profile is untouched.
	var before int64
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT revision FROM dealer_profile WHERE id = 1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f.allow(t, "bound", "GET", "/api/v1/dealer-profile", "", nil, http.StatusOK)
	for _, c := range []struct{ method, path string }{
		{"PUT", "/api/v1/dealer-profile"},
		{"PUT", "/api/v1/dealer-profile/logo"},
	} {
		if status, buf := f.key(t, f.bound, c.method, c.path, `{"legal_name":"Bound Key Ltd"}`, map[string]string{"If-Match": `"` + "1" + `"`}); status != http.StatusForbidden {
			t.Errorf("bound key %s %s: %d %s, want 403", c.method, c.path, status, buf)
		}
	}
	var after int64
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT revision FROM dealer_profile WHERE id = 1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("a refused write moved the profile revision from %d to %d", before, after)
	}
}
