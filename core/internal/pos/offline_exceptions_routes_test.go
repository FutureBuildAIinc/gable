// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// RULE (ADR 0011 3.4, the routes): the list and the read are the managers'
// (admin, owner, finance), the resolution admin and owner only; a cashier is
// refused both; the list carries each row's actions and holds open rows by
// default; the resolution answers the row with its new ETag.
func TestOfflineResolve_TheRoutes(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	id, _ := refusedForStock(t, f, b)
	list := "/api/v1/pos/offline/exceptions?branch_id=" + f.branchID.String()
	item := "/api/v1/pos/offline/exceptions/" + id.String()
	hdr := func(role string) []string { return []string{"X-Test-Role", role, "X-Test-Sub", "sub-" + role} }

	if r := f.do("GET", list, nil, hdr("cashier")...); r.status != http.StatusForbidden {
		t.Errorf("cashier list = %d, want 403", r.status)
	}
	r := f.do("GET", list, nil, hdr("finance")...)
	if r.status != http.StatusOK {
		t.Fatalf("finance list = %d: %s", r.status, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list = %d items, want 1: %s", len(items), r.raw)
	}
	row := items[0].(map[string]any)
	if acts, _ := row["actions"].([]any); len(acts) != 2 || acts[0] != "retry" || acts[1] != "write_off" {
		t.Errorf("actions = %v, want retry and write_off", row["actions"])
	}
	if r := f.do("GET", item, nil, hdr("admin")...); r.status != http.StatusOK || r.header.Get("ETag") == "" {
		t.Errorf("admin read = %d etag %q", r.status, r.header.Get("ETag"))
	}
	body := map[string]any{"action": "write_off", "reason": "counted as over", "revision": 1}
	for _, role := range []string{"cashier", "finance"} {
		if r := f.do("POST", item+"/resolve", body, hdr(role)...); r.status != http.StatusForbidden {
			t.Errorf("%s resolve = %d, want 403", role, r.status)
		}
	}
	if r := f.do("POST", item+"/resolve", map[string]any{"action": "write_off", "revision": 1}, hdr("admin")...); r.status != http.StatusBadRequest {
		t.Errorf("resolve with no reason = %d, want 400", r.status)
	}
	r = f.do("POST", item+"/resolve", body, hdr("owner")...)
	if r.status != http.StatusOK || str(t, r.body, "status") != "resolved" || r.header.Get("ETag") == "" {
		t.Fatalf("owner resolve = %d: %s", r.status, r.raw)
	}
	r = f.do("GET", list, nil, hdr("admin")...)
	if items, _ := r.body["items"].([]any); len(items) != 0 {
		t.Errorf("the resolved row is still on the open list")
	}
	r = f.do("GET", list+"&status=resolved", nil, hdr("admin")...)
	if items, _ := r.body["items"].([]any); len(items) != 1 {
		t.Errorf("the resolved list = %s", r.raw)
	}
}
