// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestKeyBranchPin_LegacyTierRowStaysDealerWide is round 9 P2: a TIER category
// rule that also names a customer (the row migration 107 leaves behind, held
// NOT VALID) is dealer wide however it reads: a key pinned to A cannot delete
// it singly or in bulk, nor read its audit, while the unbound key still can.
func TestKeyBranchPin_LegacyTierRowStaysDealerWide(t *testing.T) {
	f := newKeyPinFixture(t)
	ctx := context.Background()
	seed := func() uuid.UUID {
		id := uuid.New()
		tx, err := f.db.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		for _, stmt := range []string{
			`ALTER TABLE category_pricing_rules DROP CONSTRAINT category_pricing_rules_single_target_check`,
			`INSERT INTO category_pricing_rules (id, target_type, tier, customer_id, category_id, rule_type, rule_value, created_by)
			 VALUES ('` + id.String() + `', 'TIER', 'PIN-TIER', '` + f.custID.String() + `', '` + f.category.String() + `', 'MARKUP', 10, 'test')`,
			`ALTER TABLE category_pricing_rules ADD CONSTRAINT category_pricing_rules_single_target_check
			 CHECK ((target_type = 'ACCOUNT' AND tier IS NULL) OR (target_type = 'TIER' AND customer_id IS NULL)) NOT VALID`,
		} {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				t.Fatalf("seed the legacy row: %v", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return id
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM category_pricing_rules WHERE tier = 'PIN-TIER'`)
		_, _ = f.db.Pool.Exec(ctx, `ALTER TABLE category_pricing_rules VALIDATE CONSTRAINT category_pricing_rules_single_target_check`)
	})
	exists := func(id uuid.UUID) bool {
		var n int
		if err := f.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM category_pricing_rules WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	rev := func(id uuid.UUID) map[string]string { return map[string]string{"If-Match": f.catRuleRev(t, id)} }

	legacy := seed()
	f.refuseForeign(t, "DELETE", "/api/v1/pricing/category-rules/"+legacy.String(), "", rev(legacy))
	f.refuseForeign(t, "DELETE", "/api/v1/pricing/category-rules/bulk", fmt.Sprintf(`{"ids":[%q]}`, legacy.String()), nil)
	f.refuseForeign(t, "GET", "/api/v1/pricing/category-rules/"+legacy.String()+"/audit", "", nil)
	if !exists(legacy) {
		t.Fatal("the pinned key deleted the legacy tier row")
	}
	// An update of the legacy row answers the invariant's own refusal for
	// every caller (round 11 P3-1: the NOT VALID constraint revalidates on
	// the write, which used to surface as a 500), while the bound key is
	// refused at the pin first.
	putBody := `{"rule_type":"markup","value_pct":"12"}`
	f.refuseForeign(t, "PUT", "/api/v1/pricing/category-rules/"+legacy.String(), putBody, rev(legacy))
	for _, who := range []string{"unbound", "user"} {
		status, buf := f.send(t, who, "PUT", "/api/v1/pricing/category-rules/"+legacy.String(), putBody, rev(legacy))
		if status != http.StatusConflict || !strings.Contains(string(buf), "single target") {
			t.Errorf("%s updating the legacy tier row: %d %s, want the 409 naming the single target invariant", who, status, buf)
		}
	}
	if !exists(legacy) {
		t.Fatal("the legacy tier row was updated away")
	}
	// The unbound key keeps the delete, single and bulk, each on a fresh row.
	f.allow(t, "unbound", "DELETE", "/api/v1/pricing/category-rules/"+legacy.String(), "", rev(legacy), http.StatusNoContent)
	again := seed()
	f.allow(t, "unbound", "DELETE", "/api/v1/pricing/category-rules/bulk", fmt.Sprintf(`{"ids":[%q]}`, again.String()), nil, http.StatusNoContent)
	if exists(legacy) || exists(again) {
		t.Error("the unbound key could not delete the legacy tier row")
	}
}
