// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// Ruling A (ADR 0007): customers are dealer wide accounts. Every user role
// that sells or looks a customer up reads every customer, with a header, with
// none, or as an administrator; an unbound key reads them too. A key bound to
// a branch keeps PR 83's pin, and the finance surfaces keep following grants.

import (
	"net/http"
	"strings"
	"testing"
)

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
