// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The tint saved colours through serve's own mount (row CP-16): the counter
// dialogue offers saving a colour, so the cashier writes one, and the saved
// colours are customer rows, so the write and the read are walled to the
// customer they name.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// seedTintColor seeds a tint system and one colour, and answers the system
// and colour ids.
func seedTintColor(t *testing.T, f *wallFixture) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	sys := uuid.New()
	color := uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO paint_tint_systems (id, code, name, manufacturer, unit_denominator)
		VALUES ($1, $2, 'Wall tint', 'Wall Paints', 384)`, sys, "WALL"+sys.String()[:8]); err != nil {
		t.Fatalf("seed tint system: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO paint_colors (id, tint_system_id, manufacturer_code, name)
		VALUES ($1, $2, 'HC-188', 'Essex Green')`, color, sys); err != nil {
		t.Fatalf("seed tint colour: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM paint_colors WHERE id = $1`, color)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM paint_tint_systems WHERE id = $1`, sys)
	})
	return sys, color
}

// RULE: the counter may save a colour (row CP-16): the dialogue offers it to
// the counter, so the cashier writes one. The saved colours are customer rows,
// so the write is walled to the customer it names: a customer of another
// branch is refused, and the read of another branch's customer reads as
// missing.
func TestTintSavedColoursAreTheCounterSAndWalledByBranch(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()
	sys, color := seedTintColor(t, f)

	own := fmt.Sprintf(`{"customer_id":%q,"color_id":%q,"name":"Living room"}`, f.crmCustA, color)
	foreign := fmt.Sprintf(`{"customer_id":%q,"color_id":%q,"name":"Jobsite"}`, f.crmCustB, color)

	// the counter saves a colour for a customer of its own branch
	if got := f.call(t, "POST", "/api/v1/tint/saved-colors", own, "cashier", "u-a", A); got != http.StatusCreated {
		t.Errorf("the counter saving a colour = %d, want 201", got)
	}
	// a customer of another branch is refused: the wall is the customer's
	if got := f.call(t, "POST", "/api/v1/tint/saved-colors", foreign, "cashier", "u-a", A); got != http.StatusForbidden {
		t.Errorf("the counter saving a colour for another branch's customer = %d, want 403", got)
	}
	// the catalogue stays with admin and owner: the counter writes no colour
	// of the dealer's own
	if got := f.call(t, "POST", "/api/v1/tint/colors",
		fmt.Sprintf(`{"tint_system_id":%q,"manufacturer_code":"HC-189","name":"Essex Blue"}`, sys),
		"cashier", "u-a", A); got != http.StatusForbidden {
		t.Errorf("the counter writing the catalogue = %d, want 403", got)
	}
	// the read is walled the same way: the counter reads its own customer's
	// saved colours and finds another branch's customer missing
	status, buf := f.callBody(t, "GET", "/api/v1/tint/saved-colors?customer_id="+f.crmCustA.String(),
		"", "cashier", "u-a", A)
	if status != http.StatusOK {
		t.Fatalf("the counter reading its customer's saved colours = %d: %s", status, buf)
	}
	if !strings.Contains(string(buf), "Living room") {
		t.Errorf("the counter's own customer's saved colour is missing: %s", buf)
	}
	if got := f.call(t, "GET", "/api/v1/tint/saved-colors?customer_id="+f.crmCustB.String(),
		"", "cashier", "u-a", A); got != http.StatusNotFound {
		t.Errorf("the counter reading another branch's customer's saved colours = %d, want 404", got)
	}
}

// RULE: the saved colours read is walled to what the caller may see: a user
// reads every customer's saved colours (a customer is a dealer wide account,
// ADR 0007 section 5.8 ruling A), while a key bound to a branch reads only the
// saved colours of the customers that hold its pin.
func TestTintSavedColoursReadCarriesTheCustomerWall(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()
	_, color := seedTintColor(t, f)

	// a saved colour of the other branch's customer, written outside the wall
	// (the dealer wide write the admin does)
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO paint_saved_colors (customer_id, color_id, name)
		VALUES ($1, $2, 'Jobsite')`, f.crmCustB, color); err != nil {
		t.Fatalf("seed the foreign saved colour: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM paint_saved_colors WHERE color_id = $1`, color)
	})
	// the counter reads every customer's saved colours: a customer is a dealer
	// wide account (ruling A)
	status, buf := f.callBody(t, "GET", "/api/v1/tint/saved-colors", "", "cashier", "u-a", A)
	if status != http.StatusOK {
		t.Fatalf("the counter reading the saved colours = %d: %s", status, buf)
	}
	if !strings.Contains(string(buf), "Jobsite") {
		t.Errorf("the dealer wide read lost another customer's saved colour: %s", buf)
	}
}

// RULE: the base update is a catalogue write (row CP-16, item 341): admin and
// owner change a base and its product link, and no other desk role does, the
// cashier included.
func TestTintBaseUpdateIsAdminAndOwnerOnly(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()
	sys, _ := seedTintColor(t, f)
	base := uuid.New()
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO paint_bases (id, tint_system_id, code, name, fill_volume_oz)
		VALUES ($1, $2, 'DEEP', 'Deep base', 122)`, base, sys); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM paint_bases WHERE id = $1`, base)
	})
	path := "/api/v1/tint/bases/" + base.String()
	body := `{"name":"Deep base, walled"}`
	for _, role := range []string{"cashier", "sales", "warehouse", "finance", "purchasing"} {
		if got := f.call(t, "PATCH", path, body, role, "u-a", A); got != http.StatusForbidden {
			t.Errorf("%s updating a base = %d, want 403", role, got)
		}
	}
	for _, role := range []string{"admin", "owner"} {
		if got := f.call(t, "PATCH", path, body, role, "u-a", A); got != http.StatusOK {
			t.Errorf("%s updating a base = %d, want 200", role, got)
		}
	}
	if got := f.call(t, "PATCH", "/api/v1/tint/bases/"+uuid.NewString(), body, "admin", "u-a", A); got != http.StatusNotFound {
		t.Errorf("updating an unknown base = %d, want 404", got)
	}
	for what, body := range map[string]string{
		"an unknown product": `{"product_id":"` + uuid.NewString() + `"}`,
		"a long name":        `{"name":"` + strings.Repeat("n", 121) + `"}`,
	} {
		if got := f.call(t, "PATCH", path, body, "admin", "u-a", A); got != http.StatusUnprocessableEntity {
			t.Errorf("updating a base with %s = %d, want 422", what, got)
		}
	}
	if got := f.call(t, "PATCH", path, `{}`, "admin", "u-a", A); got != http.StatusUnprocessableEntity && got != http.StatusBadRequest {
		t.Errorf("an empty update = %d, want a 4xx refusal", got)
	}
}
