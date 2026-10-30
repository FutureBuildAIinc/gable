// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

// The offline price sheet's two request options (ADR 0011 section 5.4, slice
// O2): SkipRules answers the engine with rung 2 (promotions, quantity breaks
// and job prices) skipped and every other rung as it stands, and Level
// answers a level's own resolution, the fallback row an account takes where
// the sheet carries no row of its own. Neither option changes a request that
// does not set it.

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func offlineResolveService(rules []PricingRule, repo *fakeResolveRepo) *Service {
	return NewService(&MockRepository{contracts: map[string]CustomerContract{}, rules: rules}).WithRepository(repo)
}

func TestResolveSkipRulesSkipsRungTwoOnly(t *testing.T) {
	branch := fxBranch
	promo := priceOf(6)
	rules := []PricingRule{{ID: uuid.New(), Name: "spring", RuleType: RuleTypePromotional, ProductID: &fxProd,
		FixedPrice: &promo, PriceUOM: "EA", IsActive: true}}

	t.Run("the live path still takes the promotion", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA")
		res, err := offlineResolveService(rules, repo).Resolve(context.Background(), resolveReq(uuid.Nil, fxProd, &branch, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceBasis != SourcePromotional || res.UnitPrice != promo {
			t.Errorf("live: basis %s price %d, want the promotion at 6.0000", res.PriceBasis, res.UnitPrice)
		}
	})

	t.Run("skipping rung two answers the walk in base", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA")
		req := resolveReq(uuid.Nil, fxProd, &branch, 1)
		req.SkipRules = true
		res, err := offlineResolveService(rules, repo).Resolve(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceBasis != SourceBase || res.UnitPrice != priceOf(10) {
			t.Errorf("skip rules: basis %s price %d, want base 10.0000", res.PriceBasis, res.UnitPrice)
		}
	})

	t.Run("skipping rung two keeps the contract and the level", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA")
		repo.setProgram(fxCust, PricingProgram{LevelID: fxLevel, LevelCode: "SILVER", LevelBasis: "BASE",
			LevelAdjust: qtyOf(-10), LevelActive: true})
		custRules := []PricingRule{{ID: uuid.New(), Name: "job", RuleType: RuleTypeJobOverride, ProductID: &fxProd,
			CustomerID: &fxCust, FixedPrice: &promo, PriceUOM: "EA", IsActive: true}}
		req := resolveReq(fxCust, fxProd, &branch, 1)
		req.SkipRules = true
		res, err := offlineResolveService(custRules, repo).Resolve(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceBasis != SourceLevel || res.UnitPrice != priceOf(9) {
			t.Errorf("skip rules, level: basis %s price %d, want level 9.0000", res.PriceBasis, res.UnitPrice)
		}
		repo.setContract(fxCust, fxProd, nil, 7.5, "EA")
		res, err = offlineResolveService(custRules, repo).Resolve(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceBasis != SourceContract || res.UnitPrice != priceOf(7.5) {
			t.Errorf("skip rules, contract: basis %s price %d, want contract 7.5000", res.PriceBasis, res.UnitPrice)
		}
	})
}

func TestResolveLevelAnswersTheLevelNotTheAccount(t *testing.T) {
	branch := fxBranch
	level := PricingProgram{LevelID: fxLevel, LevelCode: "SILVER", LevelName: "Silver", LevelBasis: "BASE",
		LevelAdjust: qtyOf(-10), LevelActive: true}
	repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA")
	repo.setProgram(fxCust, level)
	repo.setContract(fxCust, fxProd, nil, 7.5, "EA")

	// the account's own answer is its contract
	own := resolveReq(fxCust, fxProd, &branch, 1)
	own.SkipRules = true
	res, err := offlineResolveService(nil, repo).Resolve(context.Background(), own)
	if err != nil {
		t.Fatal(err)
	}
	if res.PriceBasis != SourceContract {
		t.Fatalf("own key: basis %s, want contract", res.PriceBasis)
	}

	// the level's key answers the level, never the account's contract
	lv := level
	req := resolveReq(uuid.Nil, fxProd, &branch, 1)
	req.SkipRules = true
	req.Level = &lv
	res, err = offlineResolveService(nil, repo).Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.PriceBasis != SourceLevel || res.UnitPrice != priceOf(9) {
		t.Errorf("level key: basis %s price %d, want level 9.0000", res.PriceBasis, res.UnitPrice)
	}

	// an explicit level price at the branch answers as stored
	repo.setPrice(fxProd, &fxLevel, &branch, 8.25, "EA")
	res, err = offlineResolveService(nil, repo).Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.PriceBasis != SourceLevel || res.UnitPrice != priceOf(8.25) {
		t.Errorf("level key, explicit: basis %s price %d, want level 8.2500", res.PriceBasis, res.UnitPrice)
	}

	// an inactive level falls to the branch base, as it does for an account
	inactive := level
	inactive.LevelActive = false
	req.Level = &inactive
	repo.setPrice(fxProd, nil, &branch, 9.75, "EA")
	res, err = offlineResolveService(nil, repo).Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.PriceBasis != SourceBranch || res.UnitPrice != priceOf(9.75) {
		t.Errorf("inactive level key: basis %s price %d, want branch 9.7500", res.PriceBasis, res.UnitPrice)
	}

	// a level with a customer named is refused: the two keys never mix
	mixed := resolveReq(fxCust, fxProd, &branch, 1)
	mixed.Level = &lv
	if _, err := offlineResolveService(nil, repo).Resolve(context.Background(), mixed); err == nil {
		t.Errorf("a request naming a customer and a level resolved; want a refusal")
	}
}
