// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"context"
	"math"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// The seeder writes prices. A number in demoPricingRules that the pricing
// engine reads differently from the way the seeder meant it is not a cosmetic
// defect — it is the demo quoting the wrong money, and it shipped: discount_pct
// was written as 0.10 for "10% off" while internal/pricing/service.go divides
// by 100, so a 10% rule delivered a tenth of a percent.
//
// These tests do not restate the arithmetic. They push each seeded row through
// the actual pricing.Service, so the seeder and the engine cannot disagree
// about units without a red test.

// oneRuleRepo is a pricing.Repository holding exactly one rule and no
// contracts, so a CalculatePriceWithQty call resolves to that rule or to
// retail. Category scope is not modelled here: it belongs to the repository
// layer and is tested in internal/pricing against Postgres.
type oneRuleRepo struct{ rule pricing.PricingRule }

func (r *oneRuleRepo) GetContract(ctx context.Context, customerID, productID uuid.UUID) (*pricing.CustomerContract, error) {
	return nil, nil
}
func (r *oneRuleRepo) CreateContract(ctx context.Context, c *pricing.CustomerContract) error {
	return nil
}
func (r *oneRuleRepo) GetMatchingRules(ctx context.Context, productID uuid.UUID, customerID, jobID *uuid.UUID, quantity float64) ([]pricing.PricingRule, error) {
	if r.rule.MinQuantity > quantity {
		return nil, nil
	}
	return []pricing.PricingRule{r.rule}, nil
}
func (r *oneRuleRepo) ListBreakQuantities(ctx context.Context, productID uuid.UUID, customerID *uuid.UUID) ([]float64, error) {
	return nil, nil
}
func (r *oneRuleRepo) CreateRule(ctx context.Context, rule *pricing.PricingRule) error { return nil }
func (r *oneRuleRepo) ListRules(ctx context.Context) ([]pricing.PricingRule, error) {
	return nil, nil
}

// priceWithSeededRule prices one line at basePrice/quantity using a single
// seeded rule, through the same service the API and the order path use.
func priceWithSeededRule(t *testing.T, r pricingRule, basePrice, quantity float64) pricing.CalculatedPrice {
	t.Helper()
	rule := pricing.PricingRule{
		ID:             uuid.New(),
		Name:           r.Name,
		RuleType:       pricing.RuleType(r.RuleType),
		Category:       r.Category,
		DiscountPct:    r.DiscPct,
		MinQuantity:    r.MinQty,
		MarginFloorPct: r.MarginFloor,
		IsActive:       true,
	}
	svc := pricing.NewService(&oneRuleRepo{rule: rule})
	got, err := svc.CalculatePriceWithQty(context.Background(),
		&customer.Customer{ID: uuid.New()}, uuid.New(), basePrice, quantity, nil)
	if err != nil {
		t.Fatalf("CalculatePriceWithQty(%s): %v", r.Name, err)
	}
	return got
}

// CORRECTNESS: every seeded discount is a PERCENT, and the engine delivers it.
//
// Before the fix these rows held 0.10/0.15/0.05 and the engine's
// `basePrice * (1 - DiscountPct/100)` turned "10% off" into 0.1% off. The
// assertion that catches it is not "the field equals 10" but "the customer
// actually saves what the rule advertises", which is the property that matters
// and the one a future edit is most likely to break.
func TestDemoPricingRules_DeliverTheDiscountTheyAdvertise(t *testing.T) {
	const basePrice = 23.25 // the SKU from the live transcript

	for _, r := range demoPricingRules {
		if r.DiscPct == nil {
			continue
		}
		// Price at the rule's own threshold so it is guaranteed to apply.
		qty := math.Max(r.MinQty, 1)
		got := priceWithSeededRule(t, r, basePrice, qty)

		if got.Source == pricing.SourceRetail {
			t.Errorf("%s: rule did not apply at qty %v (got %+v)", r.Name, qty, got)
			continue
		}

		delivered := (basePrice - got.FinalPrice) / basePrice * 100
		if delivered < 1.0 {
			t.Errorf("%s: advertises %.2f%% off but the engine delivers %.4f%% — a fraction was written into a column the engine reads as a percent",
				r.Name, *r.DiscPct, delivered)
		}
		if math.Abs(delivered-*r.DiscPct) > 0.05 {
			t.Errorf("%s: advertises %.2f%% off, engine delivers %.4f%% (final %.4f on base %.2f)",
				r.Name, *r.DiscPct, delivered, got.FinalPrice, basePrice)
		}
	}
}

// CORRECTNESS: no seeded margin floor claws back its own rule's discount.
//
// margin_floor_pct is read as the deepest discount a rule may reach
// (`minPrice := basePrice * (1 - MarginFloorPct/100)`). It shares the units bug
// and the failure mode is nastier: with discount_pct corrected to 10 and the
// floor left at the old 0.20, the floor clamps the 10% break back to 0.2% and
// the bug survives the fix wearing a different hat. A floor below its rule's
// discount is always a units error or a typo.
func TestDemoPricingRules_MarginFloorsDoNotClampTheirOwnDiscount(t *testing.T) {
	for _, r := range demoPricingRules {
		if r.MarginFloor == nil || r.DiscPct == nil {
			continue
		}
		if *r.MarginFloor < *r.DiscPct {
			t.Errorf("%s: margin floor %.2f%% is shallower than the %.2f%% discount it guards, so the floor silently overrides the rule",
				r.Name, *r.MarginFloor, *r.DiscPct)
		}
		got := priceWithSeededRule(t, r, 23.25, math.Max(r.MinQty, 1))
		if got.Details != r.Name {
			t.Errorf("%s: margin floor fired on the rule's own discount (details %q)", r.Name, got.Details)
		}
	}
}

// REGRESSION: the exact line from the live transcript.
//
// "Spring Roofing Promo" is 5% off. Written as 0.05 it delivered 0.05%, and a
// $23.25 board came back at $23.24 — a saving of one cent, on a rule the
// dealer thinks takes $1.16 off. Written as 5 it comes back at $22.09.
func TestSpringRoofingPromo_TakesFivePercentNotFiveHundredthsOfOne(t *testing.T) {
	var promo pricingRule
	for _, r := range demoPricingRules {
		if r.Name == "Spring Roofing Promo" {
			promo = r
		}
	}
	if promo.Name == "" {
		t.Fatal("Spring Roofing Promo is no longer in demoPricingRules")
	}

	got := priceWithSeededRule(t, promo, 23.25, 1)
	if got.FinalPrice != 22.09 {
		t.Errorf("FinalPrice = %.2f, want 22.09 (5%% off 23.25); 23.24 is the old 0.05%%", got.FinalPrice)
	}
	if got.Source != pricing.SourcePromotional {
		t.Errorf("Source = %v, want PROMOTIONAL", got.Source)
	}
}

// CORRECTNESS: re-seeding updates the rule it already wrote instead of adding
// a second copy of it.
//
// The old clause was `ON CONFLICT DO NOTHING` on a table with no unique
// constraint — nothing for the conflict to be ON, so every row inserted every
// time and a working database accumulated a fresh set of six rules per run.
// This runs the seeder's actual statement twice and counts.
//
// DB-gated: it is a test of a Postgres constraint, so it needs Postgres.
func TestPricingRuleUpsert_IsIdempotentAndRepairsValues(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	name := "seedtest-upsert-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pricing_rules WHERE name = $1`, name)
	})

	upsert := func(discount float64) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, pricingRuleUpsertSQL,
			name, "QUANTITY_BREAK", "Lumber", discount, 100.0, 20.0); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	upsert(10)
	upsert(10)
	upsert(10)

	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM pricing_rules WHERE name = $1`, name).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("after three seed runs there are %d copies of %q, want 1 — the upsert is inserting instead of updating", count, name)
	}

	// And it is a real UPDATE, not DO NOTHING: a demo database holding the old
	// fractional 0.10 has to be repaired by the next seed run, not left as is.
	upsert(0.10)
	upsert(15)
	var discount float64
	if err := db.Pool.QueryRow(ctx, `SELECT discount_pct FROM pricing_rules WHERE name = $1`, name).Scan(&discount); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if discount != 15 {
		t.Errorf("discount_pct = %v after re-seeding at 15, want 15 — DO NOTHING would have left the stale value", discount)
	}
}
