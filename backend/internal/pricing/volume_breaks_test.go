// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/google/uuid"
)

// VolumeBreaks is a projection of CalculatePriceWithQty, so these tests are as
// much about what it REFUSES to advertise as about what it returns. A break
// table that promises a price the waterfall will not honour is worse than no
// break table: the contractor buys 100 expecting one number and is invoiced
// another.

func ptr[T any](v T) *T { return &v }

func breakRule(name string, productID uuid.UUID, minQty, discountPct float64, priority int) PricingRule {
	return PricingRule{
		ID:          uuid.New(),
		Name:        name,
		RuleType:    RuleTypeQuantityBreak,
		ProductID:   &productID,
		DiscountPct: ptr(discountPct),
		MinQuantity: minQty,
		IsActive:    true,
		Priority:    priority,
	}
}

// CORRECTNESS: a straightforward ladder comes back in ascending quantity order
// with the prices the waterfall actually produces, and SavesPerUnit is measured
// against the single-unit price the catalog already shows.
func TestVolumeBreaks_BuildsTheLadder(t *testing.T) {
	prod := uuid.New()
	repo := &MockRepository{
		contracts: map[string]CustomerContract{},
		rules: []PricingRule{
			breakRule("100+", prod, 100, 20, 10),
			breakRule("20+", prod, 20, 10, 20), // higher priority, shallower discount
		},
	}
	svc := NewService(repo)
	cust := &customer.Customer{ID: uuid.New()}

	breaks, err := svc.VolumeBreaks(context.Background(), cust, prod, 10.00)
	if err != nil {
		t.Fatalf("VolumeBreaks: %v", err)
	}
	if len(breaks) != 1 {
		t.Fatalf("got %d rungs (%+v), want 1 — the 100+ rung is masked by the higher-priority 20+ rule, so advertising it would be a promise the engine breaks", len(breaks), breaks)
	}
	got := breaks[0]
	if got.MinQuantity != 20 {
		t.Errorf("MinQuantity = %v, want 20", got.MinQuantity)
	}
	if got.UnitPrice != 9.00 {
		t.Errorf("UnitPrice = %v, want 9.00 (10.00 less 10%%)", got.UnitPrice)
	}
	if got.Source != SourceQuantityBreak {
		t.Errorf("Source = %v, want QUANTITY_BREAK", got.Source)
	}
	if got.SavesPerUnit != 1.00 {
		t.Errorf("SavesPerUnit = %v, want 1.00", got.SavesPerUnit)
	}
}

// CORRECTNESS: a rung whose price is not better than the single-unit price is
// not returned. A "break" that saves nothing is noise on a screen and a lie in
// a tooltip.
func TestVolumeBreaks_DropsRungsThatDoNotBeatTheUnitPrice(t *testing.T) {
	prod := uuid.New()
	custID := uuid.New()

	// A contract price wins the waterfall at every quantity, so no quantity
	// break can ever improve on it.
	repo := &MockRepository{
		contracts: map[string]CustomerContract{
			custID.String() + ":" + prod.String(): {CustomerID: custID, ProductID: prod, ContractPrice: 4.00},
		},
		rules: []PricingRule{breakRule("50+", prod, 50, 15, 0)},
	}
	svc := NewService(repo)

	breaks, err := svc.VolumeBreaks(context.Background(), &customer.Customer{ID: custID}, prod, 10.00)
	if err != nil {
		t.Fatalf("VolumeBreaks: %v", err)
	}
	if len(breaks) != 0 {
		t.Fatalf("got %+v, want no rungs: a contract price already beats every break", breaks)
	}
}

// CORRECTNESS: a break rule scoped to a DIFFERENT product must not appear on
// this product's ladder, and a break rule scoped to a different CUSTOMER must
// not appear on this customer's.
func TestVolumeBreaks_RespectsProductAndCustomerScope(t *testing.T) {
	prod, otherProd := uuid.New(), uuid.New()
	custID, otherCust := uuid.New(), uuid.New()

	otherCustRule := breakRule("someone else's deal", prod, 25, 30, 50)
	otherCustRule.CustomerID = &otherCust

	repo := &MockRepository{
		contracts: map[string]CustomerContract{},
		rules: []PricingRule{
			breakRule("other product", otherProd, 10, 25, 0),
			otherCustRule,
		},
	}
	svc := NewService(repo)

	breaks, err := svc.VolumeBreaks(context.Background(), &customer.Customer{ID: custID}, prod, 10.00)
	if err != nil {
		t.Fatalf("VolumeBreaks: %v", err)
	}
	if len(breaks) != 0 {
		t.Fatalf("got %+v, want none: neither rule is scoped to this (customer, product)", breaks)
	}
}

// CORRECTNESS: no customer, no ladder. Returning a retail ladder for a nil
// customer would show one contractor a price that is not theirs.
func TestVolumeBreaks_NilCustomerReturnsNothing(t *testing.T) {
	svc := NewService(&MockRepository{contracts: map[string]CustomerContract{}})
	breaks, err := svc.VolumeBreaks(context.Background(), nil, uuid.New(), 10.00)
	if err != nil {
		t.Fatalf("VolumeBreaks: %v", err)
	}
	if len(breaks) != 0 {
		t.Fatalf("got %+v, want none", breaks)
	}
}

// CORRECTNESS: the ladder must be monotonically cheaper as quantity rises.
func TestVolumeBreaks_LadderIsMonotonic(t *testing.T) {
	prod := uuid.New()
	repo := &MockRepository{
		contracts: map[string]CustomerContract{},
		rules: []PricingRule{
			breakRule("500+", prod, 500, 25, 30),
			breakRule("100+", prod, 100, 15, 20),
			breakRule("20+", prod, 20, 5, 10),
		},
	}
	svc := NewService(repo)

	breaks, err := svc.VolumeBreaks(context.Background(), &customer.Customer{ID: uuid.New()}, prod, 10.00)
	if err != nil {
		t.Fatalf("VolumeBreaks: %v", err)
	}
	if len(breaks) != 3 {
		t.Fatalf("got %d rungs (%+v), want 3", len(breaks), breaks)
	}
	for i := 1; i < len(breaks); i++ {
		if breaks[i].MinQuantity <= breaks[i-1].MinQuantity {
			t.Errorf("quantities are not ascending: %v then %v", breaks[i-1].MinQuantity, breaks[i].MinQuantity)
		}
		if breaks[i].UnitPrice >= breaks[i-1].UnitPrice {
			t.Errorf("rung at qty %v costs %v, which is not cheaper than %v at qty %v",
				breaks[i].MinQuantity, breaks[i].UnitPrice, breaks[i-1].UnitPrice, breaks[i-1].MinQuantity)
		}
	}
}

// CORRECTNESS: the deepest applicable quantity break wins a tie.
//
// CalculatePriceWithQty used to return on the FIRST rule GetMatchingRules
// yields, and the ordering is `priority DESC, rule_type ASC` (repository.go).
// Two QUANTITY_BREAK rules on the same product at the same priority therefore
// tied, and the tie was broken by whatever order Postgres happened to return —
// so a customer buying 100 could be charged the 20+ price. It was first-match,
// not best-price, and nothing in the ordering ranked a rule by how specific its
// quantity band is.
//
// Observed live before the fix: two rules created on CORN2006 at priority 10
// (20+ at 8% off, 100+ at 12% off) produced $21.39 at BOTH qty 20 and qty 100 —
// the 12% rule was unreachable.
//
// selectRule now breaks that tie, and only that tie: the leading candidate
// fixes the (priority, rule_type) band and the best rule inside the band wins.
//
// VolumeBreaks did the right thing in the face of the bug: it dropped the 100+
// rung rather than advertising a price the engine would not honour, which is
// why the ladder tests above passed either way. This test pins the ENGINE.
func TestCalculatePriceWithQty_DeepestBreakShouldWin(t *testing.T) {
	prod := uuid.New()
	repo := &MockRepository{
		contracts: map[string]CustomerContract{},
		rules: []PricingRule{
			breakRule("20+", prod, 20, 8, 10),
			breakRule("100+", prod, 100, 12, 10), // same priority — the tie
		},
	}
	svc := NewService(repo)
	cust := &customer.Customer{ID: uuid.New()}

	got, err := svc.CalculatePriceWithQty(context.Background(), cust, prod, 10.00, 100, nil)
	if err != nil {
		t.Fatalf("CalculatePriceWithQty: %v", err)
	}
	if got.FinalPrice != 8.80 {
		t.Errorf("at qty 100 the price is %v, want 8.80 (the 12%% break); the 8%% break at $9.20 means the deeper rung was skipped", got.FinalPrice)
	}
	if got.Details != "100+" {
		t.Errorf("Details = %q, want %q — the winning rule should be named in the quote", got.Details, "100+")
	}

	// The shallow buyer is unaffected: at qty 20 the 100+ rung does not apply
	// at all, so the 8% rule is the only candidate.
	got20, err := svc.CalculatePriceWithQty(context.Background(), cust, prod, 10.00, 20, nil)
	if err != nil {
		t.Fatalf("CalculatePriceWithQty(qty 20): %v", err)
	}
	if got20.FinalPrice != 9.20 {
		t.Errorf("at qty 20 the price is %v, want 9.20 (the 8%% break)", got20.FinalPrice)
	}
}

// CORRECTNESS: priority is an override lever and the tie-break must not become
// a back door around it. A dealer who forces a shallow rule to the top of the
// stack gets that rule, even though a cheaper break is sitting right there.
func TestCalculatePriceWithQty_PriorityStillOverridesADeeperBreak(t *testing.T) {
	prod := uuid.New()
	repo := &MockRepository{
		contracts: map[string]CustomerContract{},
		rules: []PricingRule{
			breakRule("20+ forced", prod, 20, 8, 99),    // dealer override
			breakRule("100+ deeper", prod, 100, 12, 10), // deeper AND cheaper, but outranked
		},
	}
	svc := NewService(repo)

	got, err := svc.CalculatePriceWithQty(context.Background(), &customer.Customer{ID: uuid.New()}, prod, 10.00, 100, nil)
	if err != nil {
		t.Fatalf("CalculatePriceWithQty: %v", err)
	}
	if got.FinalPrice != 9.20 {
		t.Errorf("FinalPrice = %v, want 9.20: priority 99 forces the 8%% rule, so picking the cheaper 12%% rule would delete the override mechanism", got.FinalPrice)
	}
	if got.Details != "20+ forced" {
		t.Errorf("Details = %q, want %q", got.Details, "20+ forced")
	}
}

// CORRECTNESS: a tie between rule TYPES is still resolved by the documented
// waterfall order (job override, then promotional, then quantity break), not by
// price. The tie-break only reaches inside one (priority, rule_type) band.
func TestCalculatePriceWithQty_PromoOutranksABreakAtTheSamePriority(t *testing.T) {
	prod := uuid.New()
	promo := PricingRule{
		ID:          uuid.New(),
		Name:        "Fall Promo",
		RuleType:    RuleTypePromotional,
		ProductID:   &prod,
		DiscountPct: ptr(5.0),
		IsActive:    true,
		Priority:    10,
	}
	repo := &MockRepository{
		contracts: map[string]CustomerContract{},
		rules:     []PricingRule{promo, breakRule("100+", prod, 100, 12, 10)},
	}
	svc := NewService(repo)

	got, err := svc.CalculatePriceWithQty(context.Background(), &customer.Customer{ID: uuid.New()}, prod, 10.00, 100, nil)
	if err != nil {
		t.Fatalf("CalculatePriceWithQty: %v", err)
	}
	if got.Source != SourcePromotional || got.FinalPrice != 9.50 {
		t.Errorf("got %v at %v, want PROMOTIONAL at 9.50: rule_type ASC puts PROMOTIONAL ahead of QUANTITY_BREAK in the same priority band", got.Source, got.FinalPrice)
	}
}

// CORRECTNESS: a misconfigured ladder — a deeper rung priced ABOVE the
// shallower rung beside it at the same priority — must not charge the larger
// buyer more than the smaller one. The qty-100 customer could get 15% by
// splitting the order into five qty-20 lines, so honouring 15% costs the
// dealer nothing they were not already going to lose, and it keeps the engine
// consistent with the ladder VolumeBreaks advertises.
func TestCalculatePriceWithQty_MisconfiguredLadderNeverPunishesTheBiggerBuyer(t *testing.T) {
	prod := uuid.New()
	repo := &MockRepository{
		contracts: map[string]CustomerContract{},
		rules: []PricingRule{
			breakRule("20+", prod, 20, 15, 10),
			breakRule("100+ (typo: shallower discount)", prod, 100, 5, 10),
		},
	}
	svc := NewService(repo)
	cust := &customer.Customer{ID: uuid.New()}

	at20, err := svc.CalculatePriceWithQty(context.Background(), cust, prod, 10.00, 20, nil)
	if err != nil {
		t.Fatalf("CalculatePriceWithQty(20): %v", err)
	}
	at100, err := svc.CalculatePriceWithQty(context.Background(), cust, prod, 10.00, 100, nil)
	if err != nil {
		t.Fatalf("CalculatePriceWithQty(100): %v", err)
	}
	if at100.FinalPrice > at20.FinalPrice {
		t.Errorf("qty 100 costs %v/unit but qty 20 costs %v/unit — buying more must never cost more per unit", at100.FinalPrice, at20.FinalPrice)
	}
	if at100.FinalPrice != 8.50 {
		t.Errorf("at qty 100 the price is %v, want 8.50 (the 15%% rung the customer already qualifies for)", at100.FinalPrice)
	}
}

// CORRECTNESS: pricing_rules.category is a real scope column.
//
// CreateRule wrote it and GetMatchingRules selected it back into
// PricingRule.Category, but it appeared in NO WHERE clause anywhere in this
// package. A rule the dealer scoped to "Roofing" therefore priced every
// product in the catalog.
//
// This was live in the seeded database: cmd/seed/main.go creates
// "Spring Roofing Promo" (category "Roofing") and "Lumber Qty Break 100+"
// (category "Lumber") with product_id NULL, and both matched a cornice flashing
// SKU.
//
// The scope match now lives in repository.go's categoryScopePredicate, which
// MockRepository mirrors in Go (see ruleCategoryMatches). The Postgres half —
// the ltree ancestor walk and the flat-string arm — is exercised against a real
// database in repository_category_scope_test.go.
func TestGetMatchingRules_ShouldHonourCategoryScope(t *testing.T) {
	prod := uuid.New()
	roofingOnly := PricingRule{
		ID:          uuid.New(),
		Name:        "Spring Roofing Promo",
		RuleType:    RuleTypePromotional,
		Category:    "Roofing",
		DiscountPct: ptr(5.0),
		IsActive:    true,
	}
	repo := &MockRepository{contracts: map[string]CustomerContract{}, rules: []PricingRule{roofingOnly}}
	svc := NewService(repo)

	// prod is not a roofing product. The rule must not reach it.
	got, err := svc.CalculatePriceWithQty(context.Background(), &customer.Customer{ID: uuid.New()}, prod, 10.00, 1, nil)
	if err != nil {
		t.Fatalf("CalculatePriceWithQty: %v", err)
	}
	if got.Source == SourcePromotional {
		t.Errorf("a Roofing-scoped promo priced a non-roofing product: %+v", got)
	}
}

// CORRECTNESS: the flip side — a category-scoped rule DOES reach a product in
// that category, including through an ancestor. Scope that only ever says "no"
// is not scope, it is an outage.
func TestGetMatchingRules_CategoryScopeReachesItsOwnCategory(t *testing.T) {
	roofProd, lumberProd := uuid.New(), uuid.New()
	roofingPromo := PricingRule{
		ID:          uuid.New(),
		Name:        "Spring Roofing Promo",
		RuleType:    RuleTypePromotional,
		Category:    "Roofing",
		DiscountPct: ptr(5.0),
		IsActive:    true,
	}
	repo := &MockRepository{
		contracts: map[string]CustomerContract{},
		rules:     []PricingRule{roofingPromo},
		productCategories: map[uuid.UUID][]string{
			// A roofing SKU: own node plus its slug.
			roofProd: {"Roofing", "roofing"},
			// A framing SKU: own node, its slug, and its ANCESTOR — which is
			// what `pc.path <@ anc.path` resolves in SQL. 'Roofing' is not in
			// this list, so the promo must not reach it.
			lumberProd: {"Framing Lumber", "framing_lumber", "Lumber", "lumber"},
		},
	}
	svc := NewService(repo)
	cust := &customer.Customer{ID: uuid.New()}

	onRoofing, err := svc.CalculatePriceWithQty(context.Background(), cust, roofProd, 10.00, 1, nil)
	if err != nil {
		t.Fatalf("CalculatePriceWithQty(roofing): %v", err)
	}
	if onRoofing.Source != SourcePromotional || onRoofing.FinalPrice != 9.50 {
		t.Errorf("roofing product priced %v from %v, want 9.50 from PROMOTIONAL", onRoofing.FinalPrice, onRoofing.Source)
	}

	onLumber, err := svc.CalculatePriceWithQty(context.Background(), cust, lumberProd, 10.00, 1, nil)
	if err != nil {
		t.Fatalf("CalculatePriceWithQty(lumber): %v", err)
	}
	if onLumber.Source == SourcePromotional {
		t.Errorf("the Roofing promo reached a framing SKU: %+v", onLumber)
	}
}

// CORRECTNESS: a rule scoped to a PARENT category covers its descendants. This
// is the ltree arm of the predicate, expressed through the ancestor strings the
// mock carries: a "Lumber" rule has to price a product filed under
// 'lumber.framing' without the dealer restating it per leaf.
func TestGetMatchingRules_ParentCategoryRuleCoversDescendants(t *testing.T) {
	framingProd := uuid.New()
	lumberBreak := breakRule("Lumber Qty Break 100+", uuid.New(), 100, 10, 0)
	lumberBreak.ProductID = nil // catalog-wide within the category
	lumberBreak.Category = "Lumber"

	repo := &MockRepository{
		contracts: map[string]CustomerContract{},
		rules:     []PricingRule{lumberBreak},
		productCategories: map[uuid.UUID][]string{
			framingProd: {"Framing Lumber", "framing_lumber", "Lumber", "lumber"},
		},
	}
	svc := NewService(repo)

	got, err := svc.CalculatePriceWithQty(context.Background(), &customer.Customer{ID: uuid.New()}, framingProd, 10.00, 100, nil)
	if err != nil {
		t.Fatalf("CalculatePriceWithQty: %v", err)
	}
	if got.Source != SourceQuantityBreak || got.FinalPrice != 9.00 {
		t.Errorf("got %v at %v, want QUANTITY_BREAK at 9.00: a rule on the parent category must reach a product in a child category", got.Source, got.FinalPrice)
	}
}
