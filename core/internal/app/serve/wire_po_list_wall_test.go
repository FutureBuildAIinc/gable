// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The purchase order list wall (ADR 0008 section 11, the STATUS's queued
// item 5 named as C4-1a's test): with default_branch_required false and no
// branch header, a bound purchasing user granted only branch A must not see
// every branch's purchase orders. The three arm branch predicate the quotes
// list uses applies: a context branch lists its own, a bound caller with no
// context branch lists its grants, none granted lists none, an administrator
// without a header lists every branch's. At the base commit the list filtered
// on the context branch alone, so the bound caller with no header saw every
// branch: this test is red there.

import (
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestBranchWall_PurchaseOrderListGrants(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()

	for _, c := range []struct {
		name, role, sub, header string
		wantA, wantB            bool
	}{
		{"purchasing, header A", "purchasing", "u-a", A, true, false},
		{"purchasing, no header", "purchasing", "u-a", "", true, false},
		{"purchasing u-none, no header", "purchasing", "u-none", "", false, false},
		{"admin, header A", "admin", "boss", A, true, false},
		{"admin, no header", "admin", "boss", "", true, true},
	} {
		status, body := f.callBody(t, "GET", "/api/v1/purchase-orders?limit=200", "", c.role, c.sub, c.header)
		if status != 200 {
			t.Errorf("purchase order list, %s: %d, want 200", c.name, status)
			continue
		}
		if got := strings.Contains(string(body), f.poA.String()); got != c.wantA {
			t.Errorf("purchase order list, %s: branch A's purchase order present = %v, want %v", c.name, got, c.wantA)
		}
		if got := strings.Contains(string(body), f.poB.String()); got != c.wantB {
			t.Errorf("purchase order list, %s: branch B's purchase order present = %v, want %v", c.name, got, c.wantB)
		}
	}
}
