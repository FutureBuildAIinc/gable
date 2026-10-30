// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package serve

// The branch act's walk-in identity floor (CP-17a, item 296): the floor is
// the branch's own setting and only an owner or an admin may save it. A
// cashier is refused by the branch act's guard, and the floor never reaches
// the counter through an unprivileged write.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestBranchAct_WalkInIdentityFloorIsOwnerAndAdminOnly(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, false)
	path := "/api/v1/branches/" + f.branchA.String()

	status, raw := f.callBody(t, "GET", path, "", "admin", "u-a", f.branchA.String())
	if status != http.StatusOK {
		t.Fatalf("the branch read: %d %s", status, raw)
	}
	var branch map[string]any
	if err := json.Unmarshal(raw, &branch); err != nil {
		t.Fatal(err)
	}
	write := func(role string, floor any) (int, []byte) {
		// the revision is read fresh: every write moves it on
		got, raw := f.callBody(t, "GET", path, "", "admin", "u-a", f.branchA.String())
		if got != http.StatusOK {
			t.Fatalf("the branch read: %d %s", got, raw)
		}
		if err := json.Unmarshal(raw, &branch); err != nil {
			t.Fatal(err)
		}
		body := map[string]any{
			"code": branch["code"], "name": branch["name"], "active": true,
			"country_code": "US", "revision": branch["revision"],
		}
		if floor != nil {
			body["walk_in_identity_cents"] = floor
		}
		payload, _ := json.Marshal(body)
		return f.callBody(t, "PUT", path, string(payload), role, "u-a", f.branchA.String())
	}

	// a cashier may not save the floor
	status, raw = write("cashier", 500000)
	if status != http.StatusForbidden {
		t.Fatalf("a cashier saving the floor: %d %s, want 403", status, raw)
	}
	var saved *int64
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT walk_in_identity_cents FROM locations WHERE id = $1`, f.branchA).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved != nil {
		t.Fatalf("the refused write left the floor at %d, want it unset", *saved)
	}

	// an owner may
	status, raw = write("owner", 500000)
	if status != http.StatusOK {
		t.Fatalf("an owner saving the floor: %d %s", status, raw)
	}
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT walk_in_identity_cents FROM locations WHERE id = $1`, f.branchA).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved == nil || *saved != 500000 {
		t.Fatalf("the saved floor = %v, want 500000", saved)
	}

	// an admin may
	status, raw = write("admin", 0)
	if status != http.StatusOK {
		t.Fatalf("an admin saving the floor: %d %s", status, raw)
	}
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT walk_in_identity_cents FROM locations WHERE id = $1`, f.branchA).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved == nil || *saved != 0 {
		t.Fatalf("the saved floor = %v, want zero (the floor off)", saved)
	}
}
