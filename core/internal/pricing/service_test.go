// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Test helpers for the exact fields: a plain decimal float is the scale 4
// value these fixtures mean, parsed the way the wire would parse it.
func qtyOf(f float64) httpx.Quantity {
	q, err := httpx.ParseQuantity(strconv.FormatFloat(f, 'f', -1, 64))
	if err != nil {
		panic(err)
	}
	return q
}

func pctQtyPtr(f float64) *httpx.Quantity {
	q := qtyOf(f)
	return &q
}

func priceOf(f float64) httpx.Price {
	p, err := httpx.ParsePrice(strconv.FormatFloat(f, 'f', -1, 64))
	if err != nil {
		panic(err)
	}
	return p
}

func pricePtrOf(f float64) *httpx.Price {
	p := priceOf(f)
	return &p
}

type MockRepository struct {
	contracts map[string]CustomerContract

	// rules, when populated, is filtered by the same predicate the Postgres
	// repository applies, so tests exercise the waterfall's rule branch
	// without a database. An empty slice keeps the pre-existing behaviour
	// (no rules match, fall through to tier/retail) unchanged.
	rules []PricingRule

	// productCategories is the in-memory stand-in for the category scope
	// resolution repository.go does in SQL (categoryScopePredicate): for a
	// given product it lists every category string that should match a rule's
	// `category` column — the product's own flat category, its tree node's
	// name and slug, and the name and slug of every ANCESTOR of that node,
	// which is the `pc.path <@ anc.path` arm.
	//
	// A product absent from this map has no resolvable category, so no
	// category-scoped rule reaches it. That is the same fail-closed answer
	// Postgres gives for a product with category_id NULL and a NULL flat
	// category, and it is the safe direction: an unscopable product loses a
	// discount rather than being handed one meant for a different aisle.
	productCategories map[uuid.UUID][]string
}

// ruleCategoryMatches mirrors categoryScopePredicate in Go. A rule with no
// category is unscoped; otherwise it matches when its category equals one of
// the product's scope strings, compared the way the SQL compares them
// (case-insensitively, trimmed).
func ruleCategoryMatches(ruleCategory string, productScopes []string) bool {
	want := strings.ToLower(strings.TrimSpace(ruleCategory))
	if want == "" {
		return true
	}
	for _, s := range productScopes {
		if strings.ToLower(strings.TrimSpace(s)) == want {
			return true
		}
	}
	return false
}

func (m *MockRepository) GetContract(ctx context.Context, customerID, productID uuid.UUID) (*CustomerContract, error) {
	key := customerID.String() + ":" + productID.String()
	if c, ok := m.contracts[key]; ok {
		return &c, nil
	}
	return nil, nil // Not found
}

func (m *MockRepository) CreateContract(ctx context.Context, c *CustomerContract) error {
	return nil
}

func (m *MockRepository) GetMatchingRules(ctx context.Context, productID uuid.UUID, customerID *uuid.UUID, jobID *uuid.UUID, quantity httpx.Quantity) ([]PricingRule, error) {
	var out []PricingRule
	for _, r := range m.rules {
		if !r.IsActive {
			continue
		}
		if r.ProductID != nil && *r.ProductID != productID {
			continue
		}
		if r.CustomerID != nil && (customerID == nil || *r.CustomerID != *customerID) {
			continue
		}
		if r.MinQuantity > quantity {
			continue
		}
		if r.MaxQuantity != nil && *r.MaxQuantity < quantity {
			continue
		}
		if !ruleCategoryMatches(r.Category, m.productCategories[productID]) {
			continue
		}
		out = append(out, r)
	}
	// Same ordering the SQL applies: priority DESC, then rule_type ASC.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].RuleType < out[j].RuleType
	})
	return out, nil
}

func (m *MockRepository) ListBreakQuantities(ctx context.Context, productID uuid.UUID, customerID *uuid.UUID) ([]httpx.Quantity, error) {
	seen := map[httpx.Quantity]bool{}
	out := make([]httpx.Quantity, 0)
	for _, r := range m.rules {
		if !r.IsActive || r.RuleType != RuleTypeQuantityBreak || r.MinQuantity <= 1 {
			continue
		}
		if r.ProductID != nil && *r.ProductID != productID {
			continue
		}
		if r.CustomerID != nil && (customerID == nil || *r.CustomerID != *customerID) {
			continue
		}
		if !ruleCategoryMatches(r.Category, m.productCategories[productID]) {
			continue
		}
		if seen[r.MinQuantity] {
			continue
		}
		seen[r.MinQuantity] = true
		out = append(out, r.MinQuantity)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (m *MockRepository) CreateRule(ctx context.Context, r *PricingRule) error {
	return nil
}

func (m *MockRepository) ListRules(ctx context.Context) ([]PricingRule, error) {
	return nil, nil
}

func (m *MockRepository) ListRulesPage(ctx context.Context, after *RuleCursor, limit int) ([]PricingRule, error) {
	return nil, nil
}

func (m *MockRepository) CountRules(ctx context.Context) (int64, error) {
	return 0, nil
}

func TestCalculatePrice(t *testing.T) {
	repo := &MockRepository{
		contracts: make(map[string]CustomerContract),
	}
	svc := NewService(repo)

	custID := uuid.New()
	prodID := uuid.New()
	basePrice := 10.00

	// Case 1: Retail (No contract, no tier)
	t.Run("Retail Price", func(t *testing.T) {
		cust := &customer.Customer{ID: custID}
		res, err := svc.CalculatePrice(context.Background(), cust, prodID, basePrice)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.FinalPrice != basePrice {
			t.Errorf("expected %.2f, got %.2f", basePrice, res.FinalPrice)
		}
		if res.Source != "RETAIL" {
			t.Errorf("expected RETAIL source, got %s", res.Source)
		}
	})

	// Case 2: Silver Tier (10% off)
	t.Run("Silver Tier", func(t *testing.T) {
		tier := customer.TierSilver
		cust := &customer.Customer{ID: custID, Tier: tier}

		// Expected: 10 * 0.9 = 9.00
		expected := 9.00

		res, err := svc.CalculatePrice(context.Background(), cust, prodID, basePrice)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.FinalPrice != expected {
			t.Errorf("expected %.2f, got %.2f", expected, res.FinalPrice)
		}
		if res.Source != "TIER" {
			t.Errorf("expected TIER source, got %s", res.Source)
		}
	})

	// Case 3: Contract Price
	t.Run("Contract Price", func(t *testing.T) {
		contractPrice := priceOf(5.00)
		repo.contracts[custID.String()+":"+prodID.String()] = CustomerContract{
			CustomerID:    custID,
			ProductID:     prodID,
			ContractPrice: contractPrice,
		}

		cust := &customer.Customer{ID: custID} // Even if tier exists, contract wins

		res, err := svc.CalculatePrice(context.Background(), cust, prodID, basePrice)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.FinalPrice != float64(contractPrice)/10_000 {
			t.Errorf("expected %.2f, got %.2f", float64(contractPrice)/10_000, res.FinalPrice)
		}
		if res.Source != "CONTRACT" {
			t.Errorf("expected CONTRACT source, got %s", res.Source)
		}
	})
}
