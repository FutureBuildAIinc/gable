// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// The category scope predicate is SQL — an ltree ancestor walk OR'd with a
// match on the flat products.category string — so the only honest test of it is
// one that runs against Postgres. MockRepository mirrors the predicate in Go so
// the waterfall tests can run without a database; this file pins the thing the
// mock is a mirror OF.
//
// Everything is created under a per-run suffix and deleted again, so the test
// is safe to run repeatedly against a working database and never collides with
// seeded catalog rows. Assertions only ever look at rules this test created:
// GetMatchingRules legitimately returns seeded rules too.

type scopeFixture struct {
	suffix string

	parentName string // a category with a child
	parentSlug string
	childName  string // its descendant
	flatOnly   string // a category string with NO node in the tree

	childProd uuid.UUID // filed under the child node
	flatProd  uuid.UUID // category_id NULL, flat display string only
}

// execFn runs one statement. Passing it in keeps the fixture free of any
// opinion about pools versus transactions.
type execFn func(sql string, args ...any) error

func setupScopeFixture(t *testing.T, exec execFn) *scopeFixture {
	t.Helper()

	// ltree labels accept [A-Za-z0-9_] only, so the suffix is a hex slice of a
	// fresh UUID rather than the UUID itself.
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	f := &scopeFixture{
		suffix:     suffix,
		parentName: "ScopeTest Roofing " + suffix,
		parentSlug: "scopetest_roofing_" + suffix,
		childName:  "ScopeTest Shingles " + suffix,
		flatOnly:   "ScopeTest Sheet Goods " + suffix,
		childProd:  uuid.New(),
		flatProd:   uuid.New(),
	}

	parentID, childID := uuid.New(), uuid.New()
	parentPath := "scopetest" + suffix
	childPath := parentPath + ".shingles"

	t.Cleanup(func() {
		_ = exec(`DELETE FROM pricing_rules WHERE name LIKE $1`, "scopetest-%-"+suffix)
		_ = exec(`DELETE FROM products WHERE id = ANY($1::uuid[])`, []uuid.UUID{f.childProd, f.flatProd})
		_ = exec(`DELETE FROM product_categories WHERE id = ANY($1::uuid[])`, []uuid.UUID{childID, parentID})
	})

	if err := exec(`INSERT INTO product_categories (id, name, slug, path, parent_id)
		VALUES ($1, $2, $3, $4::ltree, NULL)`,
		parentID, f.parentName, f.parentSlug, parentPath); err != nil {
		t.Fatalf("insert parent category: %v", err)
	}
	if err := exec(`INSERT INTO product_categories (id, name, slug, path, parent_id)
		VALUES ($1, $2, $3, $4::ltree, $5)`,
		childID, f.childName, "scopetest_shingles_"+suffix, childPath, parentID); err != nil {
		t.Fatalf("insert child category: %v", err)
	}

	// A product filed under the CHILD node. A rule on the parent has to reach
	// it through `pc.path <@ anc.path`.
	if err := exec(`INSERT INTO products (id, sku, description, uom_primary, base_price, category, category_id)
		VALUES ($1, $2, $3, 'PCS', 10.0000, $4, $5)`,
		f.childProd, "SCOPETEST-CHILD-"+suffix, "Scope test shingle", f.childName, childID); err != nil {
		t.Fatalf("insert child product: %v", err)
	}

	// A product with NO tree link at all, carrying only the flat display
	// string — the state migration 085 documents for 'Cornice', 'Millwork' and
	// 'Sheet Goods'.
	if err := exec(`INSERT INTO products (id, sku, description, uom_primary, base_price, category, category_id)
		VALUES ($1, $2, $3, 'PCS', 10.0000, $4, NULL)`,
		f.flatProd, "SCOPETEST-FLAT-"+suffix, "Scope test sheet good", f.flatOnly); err != nil {
		t.Fatalf("insert flat product: %v", err)
	}

	rules := []struct {
		key, ruleType, category string
		minQty                  float64
	}{
		{"parent", "PROMOTIONAL", f.parentName, 0},
		{"parentslug", "PROMOTIONAL", f.parentSlug, 0},
		{"flat", "PROMOTIONAL", f.flatOnly, 0},
		{"unscoped", "PROMOTIONAL", "", 0},
		{"elsewhere", "PROMOTIONAL", "ScopeTest Nowhere " + suffix, 0},
		{"parentbreak", "QUANTITY_BREAK", f.parentName, 100},
	}
	for _, r := range rules {
		// The unscoped rule goes in as SQL NULL, not '', and that is load
		// bearing: pricing_rules.category is nullable while
		// PricingRule.Category is a plain string, so a NULL row used to fail
		// the whole scan — and CalculatePriceWithQty swallows that error and
		// falls through to tier pricing. One hand-inserted NULL row silently
		// switched off every pricing rule in the system. The repository now
		// COALESCEs it, and this row is what proves it.
		var cat any
		if r.category != "" {
			cat = r.category
		}
		if err := exec(`INSERT INTO pricing_rules (id, name, rule_type, category, discount_pct, min_quantity, is_active)
			VALUES ($1, $2, $3, $4, 5.0, $5, true)`,
			uuid.New(), "scopetest-"+r.key+"-"+suffix, r.ruleType, cat, r.minQty); err != nil {
			t.Fatalf("insert rule %s: %v", r.key, err)
		}
	}

	return f
}

// mine reduces a result set to the keys of the rules this fixture created.
func (f *scopeFixture) mine(rules []PricingRule) map[string]bool {
	got := map[string]bool{}
	for _, r := range rules {
		if strings.HasPrefix(r.Name, "scopetest-") && strings.HasSuffix(r.Name, f.suffix) {
			got[strings.TrimSuffix(strings.TrimPrefix(r.Name, "scopetest-"), "-"+f.suffix)] = true
		}
	}
	return got
}

func assertRuleSet(t *testing.T, got map[string]bool, want ...string) {
	t.Helper()
	wanted := map[string]bool{}
	for _, w := range want {
		wanted[w] = true
	}
	for name := range got {
		if !wanted[name] {
			t.Errorf("rule %q reached a product outside its category scope (got %v, want %v)", name, keys(got), want)
		}
	}
	for name := range wanted {
		if !got[name] {
			t.Errorf("rule %q did not reach the product it is scoped to (got %v, want %v)", name, keys(got), want)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestGetMatchingRules_CategoryScope_Postgres(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) error {
		_, err := db.Pool.Exec(ctx, sql, args...)
		return err
	}
	f := setupScopeFixture(t, exec)
	repo := NewRepository(db)

	t.Run("an ancestor-category rule reaches a product in a child category", func(t *testing.T) {
		rules, err := repo.GetMatchingRules(ctx, f.childProd, nil, nil, 1)
		if err != nil {
			t.Fatalf("GetMatchingRules: %v", err)
		}
		// Both the parent's NAME and its SLUG name the same node, and the
		// unscoped rule applies to everything. 'flat' and 'elsewhere' name
		// categories this product is not in.
		assertRuleSet(t, f.mine(rules), "parent", "parentslug", "unscoped")
	})

	t.Run("a flat category string scopes a product with no tree node", func(t *testing.T) {
		rules, err := repo.GetMatchingRules(ctx, f.flatProd, nil, nil, 1)
		if err != nil {
			t.Fatalf("GetMatchingRules: %v", err)
		}
		assertRuleSet(t, f.mine(rules), "flat", "unscoped")
	})

	t.Run("the break ladder uses the same scope", func(t *testing.T) {
		// ListBreakQuantities' documented contract is that its candidates are
		// exactly the rules the waterfall could reach, so the parent-scoped
		// 100+ break belongs on the child product's ladder and nowhere else.
		onChild, err := repo.ListBreakQuantities(ctx, f.childProd, nil)
		if err != nil {
			t.Fatalf("ListBreakQuantities(child): %v", err)
		}
		if !containsQty(onChild, 100) {
			t.Errorf("qty 100 missing from the child product's ladder: %v", onChild)
		}

		// The flat product is brand new in a category nothing else names, so
		// no rule at all — seeded or otherwise — should put a rung on it.
		onFlat, err := repo.ListBreakQuantities(ctx, f.flatProd, nil)
		if err != nil {
			t.Fatalf("ListBreakQuantities(flat): %v", err)
		}
		if len(onFlat) != 0 {
			t.Errorf("a category-scoped break leaked onto an unrelated product's ladder: %v", onFlat)
		}
	})
}

func containsQty(qtys []float64, want float64) bool {
	for _, q := range qtys {
		if q == want {
			return true
		}
	}
	return false
}
