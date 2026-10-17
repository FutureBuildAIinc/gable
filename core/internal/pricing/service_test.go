// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"

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
	prodID := uuid.New()
	basePrice := 10.00

	// Case 1: base (no contract, no program)
	t.Run("Base Price", func(t *testing.T) {
		custID := uuid.New()
		fake := newFakeResolveRepo().withProduct(prodID, basePrice, "EA")
		svc := NewService(&MockRepository{}).WithRepository(fake)
		res, err := svc.Resolve(context.Background(), resolveReq(custID, prodID, nil, 1))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.UnitPrice != priceOf(basePrice) {
			t.Errorf("expected %v, got %v", priceOf(basePrice), res.UnitPrice)
		}
		if res.PriceBasis != SourceBase {
			t.Errorf("expected base source, got %s", res.PriceBasis)
		}
	})

	// Case 2: the migrated Silver level (a BASE level at -10)
	t.Run("Silver Level", func(t *testing.T) {
		custID := uuid.New()
		fake := newFakeResolveRepo().withProduct(prodID, basePrice, "EA")
		fake.setProgram(custID, PricingProgram{LevelID: uuid.New(), LevelCode: "SILVER", LevelBasis: "BASE", LevelAdjust: qtyOf(-10), LevelActive: true})
		svc := NewService(&MockRepository{}).WithRepository(fake)

		// Expected: 10 * 0.9 = 9.00
		expected := priceOf(9.00)

		res, err := svc.Resolve(context.Background(), resolveReq(custID, prodID, nil, 1))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.UnitPrice != expected {
			t.Errorf("expected %v, got %v", expected, res.UnitPrice)
		}
		if res.PriceBasis != SourceLevel {
			t.Errorf("expected level source, got %s", res.PriceBasis)
		}
	})

	// Case 3: Contract Price
	t.Run("Contract Price", func(t *testing.T) {
		custID := uuid.New()
		fake := newFakeResolveRepo().withProduct(prodID, basePrice, "EA")
		fake.setContract(custID, prodID, nil, 5.00, "EA")
		fake.setProgram(custID, PricingProgram{LevelID: uuid.New(), LevelBasis: "BASE", LevelAdjust: qtyOf(-10), LevelActive: true})
		svc := NewService(&MockRepository{}).WithRepository(fake)

		res, err := svc.Resolve(context.Background(), resolveReq(custID, prodID, nil, 1))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.UnitPrice != priceOf(5.00) {
			t.Errorf("expected 5.0000, got %v", res.UnitPrice)
		}
		if res.PriceBasis != SourceContract {
			t.Errorf("expected contract source, got %s", res.PriceBasis)
		}
	})
}
