// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"math"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
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
		// baseCommit is the FinalPrice the base commit's float64 engine
		// answered for the fixture at quantity 20 (recorded by running the
		// fixture against it). The compatibility entry point answers the
		// same value apart from the cent rounding this item removes: never
		// more than a cent apart, and equal where the base did not round.
		baseCommit float64
	}{
		{name: "retail no rules", cust: &customer.Customer{ID: uuid.New()}, base: 10.00, baseCommit: 10},
		{name: "gold tier", cust: gold, base: subCentBase, baseCommit: 1.166625},
		{name: "price level", cust: levelled, base: subCentBase, baseCommit: 1.25926875},
		{name: "quantity break", cust: &customer.Customer{ID: uuid.New()},
			rules: []PricingRule{{ID: uuid.New(), Name: "20+", RuleType: RuleTypeQuantityBreak,
				DiscountPct: pctQtyPtr(8), MinQuantity: qtyOf(20), IsActive: true}},
			base: 10.00, baseCommit: 9.2},
		{name: "fixed price rule", cust: &customer.Customer{ID: uuid.New()},
			rules: []PricingRule{{ID: uuid.New(), Name: "Flat", RuleType: RuleTypePromotional,
				FixedPrice: pricePtrOf(9.4261), IsActive: true}},
			base: 10.00, baseCommit: 9.43},
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
			if diff := math.Abs(compat.FinalPrice - fx.baseCommit); diff >= 0.005 {
				t.Fatalf("the compatibility answer %v is %v from the base commit's %v, more than the cent rounding removed", compat.FinalPrice, diff, fx.baseCommit)
			}
			if compat.Source != scaled.Source || compat.Details != scaled.Details {
				t.Fatalf("the two entry points disagree: %+v vs %+v", compat, scaled)
			}
		})
	}
}

// TestCentsOfRoundsHalfAwayInIntegers: a price ending in 5 at the third
// decimal has no exact float form (20.025 is 2002.4999... cents), so the
// conversion for callers that hold cents must not go through a float.
func TestCentsOfRoundsHalfAwayInIntegers(t *testing.T) {
	for _, c := range []struct {
		price httpx.Price
		cents int64
	}{
		{200250, 2003}, {80082, 801}, {80049, 800}, {80050, 801}, {0, 0}, {49, 0}, {50, 1}, {-50, -1}, {-49, 0}, {-200250, -2003},
	} {
		if got := CentsOf(c.price); got != c.cents {
			t.Errorf("CentsOf(%d) = %d, want %d", c.price, got, c.cents)
		}
	}
}

// TestContractReturnedAsStoredSubCent: a contract price is a fixed price and
// comes back exactly as stored, never rounded (ADR 0006 R4.2).
func TestContractReturnedAsStoredSubCent(t *testing.T) {
	cust := &customer.Customer{ID: uuid.New()}
	prod := uuid.New()
	repo := &MockRepository{contracts: map[string]CustomerContract{
		cust.ID.String() + ":" + prod.String(): {ID: uuid.New(), CustomerID: cust.ID, ProductID: prod, ContractPrice: priceOf(1.2345)},
	}}
	got, err := NewService(repo).CalculateScaled(context.Background(), cust, prod, 10, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Price != priceOf(1.2345) || got.Source != SourceContract {
		t.Fatalf("contract = %v from %s, want 1.2345 as stored from the contract", got.Price, got.Source)
	}
}

// TestCategoryFixedReturnedAsStored: a category FIXED rule's value is a fixed
// price, exact; a margin floor compares exactly (30 percent off 1.3725 with a
// 20 percent floor is exactly 1.0980).
func TestCategoryFixedReturnedAsStored(t *testing.T) {
	svc := NewCategoryPricingService(nil)
	fixed := &CategoryPricingRule{RuleType: CategoryRuleFixed, ValuePrice: pricePtrOf(4.1235)}
	if got := priceOfRat(svc.ApplyRule(fixed, rat(subCentBase), rat(0))); got != priceOf(4.1235) {
		t.Fatalf("a FIXED 4.1235 rule answered %v", got)
	}
	repo := &MockRepository{contracts: map[string]CustomerContract{}, rules: []PricingRule{{
		ID: uuid.New(), Name: "Thirty off", RuleType: RuleTypePromotional,
		DiscountPct: pctQtyPtr(30), MarginFloorPct: pctQtyPtr(20), IsActive: true,
	}}}
	got, err := NewService(repo).CalculateScaled(context.Background(), &customer.Customer{ID: uuid.New()}, uuid.New(), subCentBase, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Price != priceOf(1.0980) {
		t.Fatalf("30 percent off with a 20 percent floor on 1.3725 = %v, want exactly 1.0980", got.Price)
	}
}

// TestNonNumericQuantityIsAnErrorNotZero: a NaN quantity used to price as
// quantity 0; an infinite or out of bound one is refused the same way.
func TestNonNumericQuantityIsAnErrorNotZero(t *testing.T) {
	svc := NewService(&MockRepository{contracts: map[string]CustomerContract{}})
	cust := &customer.Customer{ID: uuid.New()}
	for _, q := range []float64{math.NaN(), math.Inf(1), 1e9} {
		if _, err := svc.CalculateScaled(context.Background(), cust, uuid.New(), 10, q, nil); err == nil {
			t.Errorf("quantity %v priced without an error", q)
		}
	}
}

// TestPriceBeyondTheBoundIsRefused: a derived price past what a NUMERIC(12,4)
// column holds (ADR 0006 R2.4) is an error, never a wrapped or oversized value.
func TestPriceBeyondTheBoundIsRefused(t *testing.T) {
	repo := &MockRepository{contracts: map[string]CustomerContract{}, rules: []PricingRule{{
		ID: uuid.New(), Name: "Huge", RuleType: RuleTypePromotional, MarkupPct: pctQtyPtr(99999999), IsActive: true,
	}}}
	if _, err := NewService(repo).CalculateScaled(context.Background(), &customer.Customer{ID: uuid.New()}, uuid.New(), 99999999.9999, 1, nil); err == nil {
		t.Fatal("a price past 99999999.9999 was answered")
	}
}
