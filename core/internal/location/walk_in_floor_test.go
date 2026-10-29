// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package location_test

// The branch act's walk-in identity floor (CP-17a, item 296): the branch's
// own amount over which the counter asks a walk-in who paid cash to identify
// themselves, so the walk-in's cash sales are counted together. The branch
// act saves the floor, the audit row names the floor the branch had and the
// floor it has, and a negative amount is refused by name.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// TestBranchAct_SavesTheWalkInIdentityFloor: the floor is the branch's own
// setting, unset means the branch country's default, and the audit row names
// the old and the new amount.
func TestBranchAct_SavesTheWalkInIdentityFloor(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := wired(t, db)

	created := branchDraft()
	if err := svc.CreateLocation(ctx, created); err != nil {
		t.Fatal(err)
	}
	defer cleanLocation(t, db, created.ID)

	// a new branch leaves the floor unset: the branch country's default
	var floor *int64
	if err := db.Pool.QueryRow(ctx, `SELECT walk_in_identity_cents FROM locations WHERE id = $1`, created.ID).Scan(&floor); err != nil {
		t.Fatal(err)
	}
	if floor != nil {
		t.Fatalf("a new branch's floor = %v, want it unset (the country default)", *floor)
	}

	// the branch act saves the floor
	floorCents := int64(500_000)
	created.WalkInIdentityCents = &floorCents
	if err := svc.UpdateLocation(ctx, created, created.Revision); err != nil {
		t.Fatal(err)
	}
	var saved *int64
	if err := db.Pool.QueryRow(ctx, `SELECT walk_in_identity_cents FROM locations WHERE id = $1`, created.ID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved == nil || *saved != floorCents {
		t.Fatalf("the saved floor = %v, want %d", saved, floorCents)
	}

	// the audit row names the floor the branch had and the floor it has
	var raw []byte
	if err := db.Pool.QueryRow(ctx,
		`SELECT changes FROM audit_log WHERE entity_type = 'location' AND entity_id = $1 AND action = 'location.updated'`,
		created.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var changes map[string]any
	if err := json.Unmarshal(raw, &changes); err != nil {
		t.Fatal(err)
	}
	before, _ := changes["before"].(map[string]any)
	after, _ := changes["after"].(map[string]any)
	if before == nil || before["walk_in_identity_cents"] != nil {
		t.Errorf("the audit row's before = %v, want the unset floor", changes["before"])
	}
	if after == nil || after["walk_in_identity_cents"] != float64(floorCents) {
		t.Errorf("the audit row's after = %v, want the saved floor", changes["after"])
	}

	// the floor off is a number, not an absent value
	off := int64(0)
	created.WalkInIdentityCents = &off
	if err := svc.UpdateLocation(ctx, created, created.Revision); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT walk_in_identity_cents FROM locations WHERE id = $1`, created.ID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved == nil || *saved != 0 {
		t.Fatalf("the saved floor = %v, want zero (the floor off)", saved)
	}
}

// TestBranchAct_WalkInIdentityFloorOnTheWire: the branch act takes the floor
// as a number of cents, null is the branch country default, an absent field
// keeps the stored floor, and a negative amount is refused by name.
func TestBranchAct_WalkInIdentityFloorOnTheWire(t *testing.T) {
	f := newFixture(t)
	created := f.createBranch("WIRE-floor-" + uuid.NewString()[:8])
	if created.status != http.StatusCreated {
		t.Fatalf("create: %d %s", created.status, created.raw)
	}
	id := created.body["id"].(string)
	defer func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, id)
	}()
	put := func(body map[string]any, revision int) resp {
		body["revision"] = revision
		return f.do("PUT", "/api/v1/branches/"+id, body)
	}
	base := map[string]any{"code": created.body["code"], "name": created.body["name"], "active": true}

	// the number
	withFloor := map[string]any{"code": base["code"], "name": base["name"], "active": true,
		"walk_in_identity_cents": 500000}
	res := put(withFloor, 1)
	if res.status != http.StatusOK {
		t.Fatalf("the floor write: %d %s", res.status, res.raw)
	}
	if res.body["walk_in_identity_cents"] != json.Number("500000") {
		t.Fatalf("the branch's floor = %v, want 500000", res.body["walk_in_identity_cents"])
	}

	// an absent field keeps the stored floor
	res = put(map[string]any{"code": base["code"], "name": base["name"], "active": true}, 2)
	if res.status != http.StatusOK {
		t.Fatalf("the write without the floor: %d %s", res.status, res.raw)
	}
	if res.body["walk_in_identity_cents"] != json.Number("500000") {
		t.Fatalf("the branch's floor = %v, want the stored 500000", res.body["walk_in_identity_cents"])
	}

	// null is the branch country default
	res = put(map[string]any{"code": base["code"], "name": base["name"], "active": true,
		"walk_in_identity_cents": nil}, 3)
	if res.status != http.StatusOK {
		t.Fatalf("the write with a null floor: %d %s", res.status, res.raw)
	}
	if res.body["walk_in_identity_cents"] != nil {
		t.Fatalf("the branch's floor = %v, want it unset", res.body["walk_in_identity_cents"])
	}

	// a negative amount is refused by name
	res = put(map[string]any{"code": base["code"], "name": base["name"], "active": true,
		"walk_in_identity_cents": -1}, 4)
	if res.status != http.StatusBadRequest || !fieldNames(res)["walk_in_identity_cents"] {
		t.Fatalf("a negative floor: %d %s, want 400 naming walk_in_identity_cents", res.status, res.raw)
	}
}
