// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/google/uuid"
)

// The engine's exact arithmetic (ADR 0006 sections 1, 5.4 and 9.2's C3-1
// list): every derived price is the exact scale 4 rounding of the exact
// value, never a cent rounding, and the tier path is rounded to scale 4
// where it was unrounded before. Each test states the ADR's worked values.
// Every one of them failed against the base commit, whose engine rounded
// rule and category prices to cents and left tier prices as raw float64
// products (ADR 0006 section 10).

const subCentBase = 1.3725

// TestRuleDiscountExactScale4: a 10 percent rule discount on a base of
// 1.3725 is exactly 1.23525, whose one scale 4 rounding is 1.2353. The base
// commit answered 1.24, a cent rounding.
func TestRuleDiscountExactScale4(t *testing.T) {
	repo := &MockRepository{contracts: map[string]CustomerContract{}, rules: []PricingRule{{
		ID: uuid.New(), Name: "Ten off", RuleType: RuleTypePromotional,
		DiscountPct: pctQtyPtr(10), IsActive: true,
	}}}
	svc := NewService(repo)
	cust := &customer.Customer{ID: uuid.New()}
	res, err := svc.CalculatePriceWithQty(context.Background(), cust, uuid.New(), subCentBase, 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalPrice != 1.2353 {
		t.Fatalf("a 10 percent discount on 1.3725 is exactly 1.23525, which rounds once to scale 4 as 1.2353; got %v", res.FinalPrice)
	}
}

// TestRuleMarkupExactScale4: a 5 percent markup on 1.3725 is exactly
// 1.441125, whose scale 4 rounding is 1.4411, not the 1.44 of a cent
// rounding.
func TestRuleMarkupExactScale4(t *testing.T) {
	repo := &MockRepository{contracts: map[string]CustomerContract{}, rules: []PricingRule{{
		ID: uuid.New(), Name: "Five up", RuleType: RuleTypePromotional,
		MarkupPct: pctQtyPtr(5), IsActive: true,
	}}}
	svc := NewService(repo)
	cust := &customer.Customer{ID: uuid.New()}
	res, err := svc.CalculatePriceWithQty(context.Background(), cust, uuid.New(), subCentBase, 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalPrice != 1.4411 {
		t.Fatalf("a 5 percent markup on 1.3725 is exactly 1.441125, which rounds once to scale 4 as 1.4411; got %v", res.FinalPrice)
	}
}

// TestTierRoundedToScale4: the Silver multiplier (0.90) on 1.3725 is exactly
// 1.23525; the answer is 1.2353, where the base commit returned the
// unrounded float product.
func TestTierRoundedToScale4(t *testing.T) {
	repo := &MockRepository{contracts: map[string]CustomerContract{}}
	svc := NewService(repo)
	cust := &customer.Customer{ID: uuid.New(), Tier: customer.TierSilver}
	res, err := svc.CalculatePriceWithQty(context.Background(), cust, uuid.New(), subCentBase, 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalPrice != 1.2353 {
		t.Fatalf("the Silver tier on 1.3725 is exactly 1.23525, which rounds once to scale 4 as 1.2353; got %v", res.FinalPrice)
	}
}

// TestCategoryRulesExactScale4 walks ADR 0006 9.2's category list through
// ApplyRule: a markdown on the base price, a markup and a margin on a cost,
// and a markup on a zero cost falling back to the base price, each the exact
// scale 4 rounding of the exact value.
func TestCategoryRulesExactScale4(t *testing.T) {
	svc := NewCategoryPricingService(nil)
	markdown := &CategoryPricingRule{RuleType: CategoryRuleMarkdown, ValuePct: pctQtyPtr(10)}
	if got := priceOfRat(svc.ApplyRule(markdown, rat(subCentBase), rat(0))); got != priceOf(1.2353) {
		t.Fatalf("a 10 percent markdown on 1.3725 is 1.23525, rounded once to 1.2353; got %v", got)
	}
	markup := &CategoryPricingRule{RuleType: CategoryRuleMarkup, ValuePct: pctQtyPtr(25)}
	if got := priceOfRat(svc.ApplyRule(markup, rat(subCentBase), rat(0.8))); got != priceOf(1.0000) {
		t.Fatalf("a 25 percent markup on a 0.80 cost is exactly 1.0000; got %v", got)
	}
	margin := &CategoryPricingRule{RuleType: CategoryRuleMargin, ValuePct: pctQtyPtr(40)}
	if got := priceOfRat(svc.ApplyRule(margin, rat(subCentBase), rat(0.6))); got != priceOf(1.0000) {
		t.Fatalf("a 40 percent margin on a 0.60 cost is 0.6 / 0.6 = exactly 1.0000; got %v", got)
	}
	// A markup with no cost to read marks up the reference price instead
	// (ADR 0006 5.3 rung 3): 1.3725 at 5 percent is 1.441125 -> 1.4411.
	zeroCost := &CategoryPricingRule{RuleType: CategoryRuleMarkup, ValuePct: pctQtyPtr(5)}
	if got := priceOfRat(svc.ApplyRule(zeroCost, rat(subCentBase), rat(0))); got != priceOf(1.4411) {
		t.Fatalf("a 5 percent markup with no cost falls back to the base price: 1.441125 -> 1.4411; got %v", got)
	}
	// A margin of 100 or more answers the reference price unchanged.
	fullMargin := &CategoryPricingRule{RuleType: CategoryRuleMargin, ValuePct: pctQtyPtr(100)}
	if got := priceOfRat(svc.ApplyRule(fullMargin, rat(subCentBase), rat(0.6))); got != priceOf(subCentBase) {
		t.Fatalf("a 100 percent margin answers the reference price unchanged; got %v", got)
	}
}

// TestVolumeBreakSavingExactScale4: the ladder's saving is the exact scale 4
// difference, not a cent rounding (ADR 0006 section 10). A 12.345 percent
// break on 1.0000 prices the rung at exactly 0.87655 -> 0.8766, saving
// 0.1234; the base commit rounded the saving to 0.12.
func TestVolumeBreakSavingExactScale4(t *testing.T) {
	repo := &MockRepository{contracts: map[string]CustomerContract{}, rules: []PricingRule{{
		ID: uuid.New(), Name: "Break 10+", RuleType: RuleTypeQuantityBreak,
		DiscountPct: pctQtyPtr(12.345), MinQuantity: qtyOf(10), IsActive: true,
	}}}
	svc := NewService(repo)
	cust := &customer.Customer{ID: uuid.New()}
	breaks, err := svc.VolumeBreaks(context.Background(), cust, uuid.New(), 1.0000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(breaks) != 1 {
		t.Fatalf("expected one rung, got %d", len(breaks))
	}
	if breaks[0].SavesPerUnit != 0.1234 {
		t.Fatalf("the saving on a 0.8766 rung against a 1.0000 unit price is 0.1234 at scale 4, never a cent rounding; got %v", breaks[0].SavesPerUnit)
	}
	if breaks[0].UnitPrice != 0.8766 {
		t.Fatalf("the rung price is the exact 0.87655 rounded once to 0.8766; got %v", breaks[0].UnitPrice)
	}
}

// TestCalculateScaledMatchesCompatibilityEntry: for a fixture of rules,
// levels and tiers, the float64 entry point answers the same price
// CalculateScaled does, and apart from the cent rounding this item removes,
// the base commit's engine (ADR 0006 9.2).
func TestCalculateScaledMatchesCompatibilityEntry(t *testing.T) {
	prod := uuid.New()
	gold := &customer.Customer{ID: uuid.New(), Tier: customer.TierGold}
	levelled := &customer.Customer{ID: uuid.New(), PriceLevel: &customer.PriceLevel{Name: "Contractor", Multiplier: 0.9175}}
	fixtures := []struct {
		name  string
		cust  *customer.Customer
		rules []PricingRule
		base  float64
	}{
		{name: "retail no rules", cust: &customer.Customer{ID: uuid.New()}, base: 10.00},
		{name: "gold tier", cust: gold, base: subCentBase},
		{name: "price level", cust: levelled, base: subCentBase},
		{name: "quantity break", cust: &customer.Customer{ID: uuid.New()},
			rules: []PricingRule{{ID: uuid.New(), Name: "20+", RuleType: RuleTypeQuantityBreak,
				DiscountPct: pctQtyPtr(8), MinQuantity: qtyOf(20), IsActive: true}},
			base: 10.00},
		{name: "fixed price rule", cust: &customer.Customer{ID: uuid.New()},
			rules: []PricingRule{{ID: uuid.New(), Name: "Flat", RuleType: RuleTypePromotional,
				FixedPrice: pricePtrOf(9.4261), IsActive: true}},
			base: 10.00},
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			repo := &MockRepository{contracts: map[string]CustomerContract{}, rules: fx.rules}
			svc := NewService(repo)
			compat, err := svc.CalculatePriceWithQty(context.Background(), fx.cust, prod, fx.base, 20, nil)
			if err != nil {
				t.Fatalf("CalculatePriceWithQty: %v", err)
			}
			scaled, err := svc.CalculateScaled(context.Background(), fx.cust, prod, fx.base, 20, nil)
			if err != nil {
				t.Fatalf("CalculateScaled: %v", err)
			}
			if compat.FinalPrice != scaled.Float64() {
				t.Fatalf("CalculatePriceWithQty answered %v while CalculateScaled answered %v", compat.FinalPrice, scaled.Float64())
			}
			if compat.Source != scaled.Source || compat.Details != scaled.Details {
				t.Fatalf("the two entry points disagree: %+v vs %+v", compat, scaled)
			}
		})
	}
}
