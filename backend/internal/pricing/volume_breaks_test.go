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

// KNOWN BUG — the deepest applicable quantity break does not win.
//
// CalculatePriceWithQty returns on the FIRST rule GetMatchingRules yields, and
// the ordering is `priority DESC, rule_type ASC` (repository.go). Two
// QUANTITY_BREAK rules on the same product at the same priority therefore tie,
// and the tie is broken by whatever order Postgres happens to return — so a
// customer buying 100 can be charged the 20+ price. It is first-match, not
// best-price, and nothing in the ordering ranks a rule by how specific its
// quantity band is.
//
// Observed live: two rules created on CORN2006 at priority 10 (20+ at 8% off,
// 100+ at 12% off) produced $21.39 at BOTH qty 20 and qty 100 — the 12% rule
// was unreachable.
//
// VolumeBreaks does the right thing in the face of this: it drops the 100+
// rung rather than advertising a price the engine will not honour, which is
// why the ladder tests above pass. This test pins the behaviour the ENGINE
// should have.
func TestCalculatePriceWithQty_DeepestBreakShouldWin(t *testing.T) {
	t.Skip("KNOWN BUG: pricing.CalculatePriceWithQty returns the first rule from GetMatchingRules (ORDER BY priority DESC, rule_type ASC) instead of the best-priced applicable one, so two QUANTITY_BREAK rules at the same priority tie and the deeper break is unreachable")

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
}

// KNOWN BUG — pricing_rules.category is a dead scope column.
//
// CreateRule writes it and GetMatchingRules selects it back into
// PricingRule.Category, but it appears in NO WHERE clause anywhere in this
// package. A rule the dealer scoped to "Roofing" therefore prices every
// product in the catalog.
//
// This is live in the seeded database: cmd/seed/main.go creates
// "Spring Roofing Promo" (category "Roofing") and "Lumber Qty Break 100+"
// (category "Lumber") with product_id NULL, and both match a cornice flashing
// SKU. Fixing it changes prices on the ERP side, so it is reported rather than
// patched here.
func TestGetMatchingRules_ShouldHonourCategoryScope(t *testing.T) {
	t.Skip("KNOWN BUG: pricing_rules.category is written by CreateRule and read back by GetMatchingRules but never appears in a WHERE clause, so a category-scoped rule applies to every product")

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
