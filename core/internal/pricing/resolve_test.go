// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

// The six rung waterfall of ADR 0006 section 5.3, priced against a fake
// repository: each rung wins in its order with the rungs above it absent; a
// branch row beats the all branch row; an explicit level price beats a
// derived one; each basis of a level answers from what it names;
// REPLACEMENT_COST falls back to the average cost, named in the details, and
// a cost basis with no cost falls to the next rung; and the compatibility
// adapters assert ErrPriceUnitNotStock when a fake repository answers in
// another unit, the price the database cannot hold.

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// fakeResolveRepo is the in-memory ResolveRepository.
type fakeResolveRepo struct {
	facts     map[uuid.UUID]*ProductPricingFacts
	unitRows  map[uuid.UUID]map[string]UnitSetRowFact
	contracts map[string]*CustomerContract // customer:product[:branch]
	prices    map[string]*ProductPrice     // product:level:branch, with "" for nil
	programs  map[uuid.UUID]*PricingProgram
	costs     map[string]*fakeVendorCost
}

// fakeVendorCost is one vendor cost row with the default flag the wire type
// leaves to the vendor_price_levels table.
type fakeVendorCost struct {
	row       *VendorCostRow
	isDefault bool
}

func newFakeResolveRepo() *fakeResolveRepo {
	return &fakeResolveRepo{
		facts:     map[uuid.UUID]*ProductPricingFacts{},
		unitRows:  map[uuid.UUID]map[string]UnitSetRowFact{},
		contracts: map[string]*CustomerContract{},
		prices:    map[string]*ProductPrice{},
		programs:  map[uuid.UUID]*PricingProgram{},
		costs:     map[string]*fakeVendorCost{},
	}
}

func (f *fakeResolveRepo) withProduct(id uuid.UUID, base float64, stockUOM string) *fakeResolveRepo {
	f.facts[id] = &ProductPricingFacts{
		BasePrice: priceOf(base), PriceUOM: stockUOM, StockUOM: stockUOM, SaleUOM: stockUOM,
	}
	f.unitRows[id] = map[string]UnitSetRowFact{
		stockUOM: {UOM: stockUOM, UnitQty: qtyOf(1), StockQty: qtyOf(1)},
	}
	return f
}

func (f *fakeResolveRepo) withUnit(productID uuid.UUID, uom string, unit, stock float64) *fakeResolveRepo {
	f.unitRows[productID][uom] = UnitSetRowFact{UOM: uom, UnitQty: qtyOf(unit), StockQty: qtyOf(stock)}
	return f
}

func (f *fakeResolveRepo) withCost(productID uuid.UUID, cost float64) *fakeResolveRepo {
	f.facts[productID].AverageUnitCost = priceOf(cost)
	return f
}

func (f *fakeResolveRepo) withVendor(productID uuid.UUID, vendor uuid.UUID) *fakeResolveRepo {
	f.facts[productID].PrimaryVendorID = &vendor
	return f
}

func branchKey(prefix string, customerID, productID uuid.UUID, branch *uuid.UUID) string {
	key := prefix + customerID.String() + ":" + productID.String()
	if branch != nil {
		key += ":" + branch.String()
	}
	return key
}

func priceKey(productID uuid.UUID, level, branch *uuid.UUID) string {
	key := productID.String()
	if level == nil {
		key += ":"
	} else {
		key += ":" + level.String()
	}
	if branch == nil {
		key += ":"
	} else {
		key += ":" + branch.String()
	}
	return key
}

func (f *fakeResolveRepo) ProductPricingFacts(_ context.Context, productID uuid.UUID) (*ProductPricingFacts, error) {
	if f2, ok := f.facts[productID]; ok {
		return f2, nil
	}
	return nil, httpx.NotFound("no such product")
}

func (f *fakeResolveRepo) UnitSetRow(_ context.Context, productID uuid.UUID, uom string) (*UnitSetRowFact, error) {
	if rows, ok := f.unitRows[productID]; ok {
		if row, ok := rows[uom]; ok {
			return &row, nil
		}
	}
	return nil, nil
}

func (f *fakeResolveRepo) ContractForBranch(_ context.Context, customerID, productID uuid.UUID, branch *uuid.UUID) (*CustomerContract, error) {
	if branch != nil {
		if c, ok := f.contracts[branchKey("", customerID, productID, branch)]; ok {
			return c, nil
		}
	}
	if c, ok := f.contracts[branchKey("", customerID, productID, nil)]; ok {
		return c, nil
	}
	return nil, nil
}

func (f *fakeResolveRepo) ProductPrice(_ context.Context, productID uuid.UUID, levelID, branch *uuid.UUID) (*ProductPrice, error) {
	if p, ok := f.prices[priceKey(productID, levelID, branch)]; ok {
		return p, nil
	}
	return nil, nil
}

func (f *fakeResolveRepo) CustomerProgram(_ context.Context, customerID uuid.UUID) (*PricingProgram, error) {
	return f.programs[customerID], nil
}

func (f *fakeResolveRepo) VendorCost(_ context.Context, vendorID, productID uuid.UUID, branch, level *uuid.UUID, _ time.Time) (*VendorCostRow, error) {
	defaultRow := func() *VendorCostRow {
		for _, c := range f.costs {
			if c.row.ProductID == productID && c.row.VendorID == vendorID && c.isDefault {
				return c.row
			}
		}
		return nil
	}
	if level != nil {
		if c, ok := f.costs[priceKey(productID, level, branch)]; ok {
			return c.row, nil
		}
		// The requested level has none: the default level's.
		return defaultRow(), nil
	}
	return defaultRow(), nil
}

// setContract plants a contract row; a nil branch is the all branch row.
func (f *fakeResolveRepo) setContract(customerID, productID uuid.UUID, branch *uuid.UUID, price float64, uom string) *fakeResolveRepo {
	f.contracts[branchKey("", customerID, productID, branch)] = &CustomerContract{
		ID: uuid.New(), CustomerID: customerID, ProductID: productID,
		ContractPrice: priceOf(price), PriceUOM: uom, BranchID: branch,
	}
	return f
}

func (f *fakeResolveRepo) setPrice(productID uuid.UUID, level, branch *uuid.UUID, price float64, uom string) *fakeResolveRepo {
	f.prices[priceKey(productID, level, branch)] = &ProductPrice{
		ID: uuid.New(), ProductID: productID, PriceLevelID: level, BranchID: branch,
		PriceUOM: uom, UnitPrice: priceOf(price),
	}
	return f
}

func (f *fakeResolveRepo) setProgram(customerID uuid.UUID, p PricingProgram) *fakeResolveRepo {
	f.programs[customerID] = &p
	return f
}

func (f *fakeResolveRepo) setVendorCost(vendorID, productID uuid.UUID, branch *uuid.UUID, cost float64, uom string, isDefault bool, levelID uuid.UUID, levelCode string) *fakeResolveRepo {
	c := &fakeVendorCost{
		row: &VendorCostRow{
			ID: uuid.New(), VendorID: vendorID, VendorLevelID: levelID, VendorLevelCode: levelCode,
			ProductID: productID, BranchID: branch, PurchaseUOM: uom, UnitCost: priceOf(cost),
		},
		isDefault: isDefault,
	}
	f.costs[priceKey(productID, &levelID, branch)] = c
	if isDefault {
		f.costs[priceKey(productID, nil, branch)] = c
	}
	return f
}

// The fixture ids.
var (
	fxCust     = uuid.MustParse("40000000-0000-4000-8000-000000000001")
	fxProd     = uuid.MustParse("40000000-0000-4000-8000-000000000002")
	fxBranch   = uuid.MustParse("40000000-0000-4000-8000-000000000003")
	fxLevel    = uuid.MustParse("40000000-0000-4000-8000-000000000004")
	fxVendor   = uuid.MustParse("40000000-0000-4000-8000-000000000005")
	fxVndLevel = uuid.MustParse("40000000-0000-4000-8000-000000000006")
)

func newResolveService(repo *fakeResolveRepo) *Service {
	return NewService(&MockRepository{}).WithRepository(repo)
}

// anyProductResolveRepo answers every product at one base price: the tests
// of the rule waterfall files price every fixture at the same base, and the
// adapters read the reference from the repository now.
type anyProductResolveRepo struct{ base float64 }

func (a anyProductResolveRepo) ProductPricingFacts(_ context.Context, _ uuid.UUID) (*ProductPricingFacts, error) {
	return &ProductPricingFacts{BasePrice: priceOf(a.base), PriceUOM: "EA", StockUOM: "EA", SaleUOM: "EA"}, nil
}

func (a anyProductResolveRepo) UnitSetRow(_ context.Context, _ uuid.UUID, uom string) (*UnitSetRowFact, error) {
	if uom == "EA" {
		return &UnitSetRowFact{UOM: uom, UnitQty: qtyOf(1), StockQty: qtyOf(1)}, nil
	}
	return nil, nil
}

func (a anyProductResolveRepo) ContractForBranch(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) (*CustomerContract, error) {
	return nil, nil
}

func (a anyProductResolveRepo) ProductPrice(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) (*ProductPrice, error) {
	return nil, nil
}

func (a anyProductResolveRepo) CustomerProgram(context.Context, uuid.UUID) (*PricingProgram, error) {
	return nil, nil
}

func (a anyProductResolveRepo) VendorCost(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, *uuid.UUID, time.Time) (*VendorCostRow, error) {
	return nil, nil
}

// wiredService wires a mock based rule test at the base price its fixtures
// price against.
func wiredService(repo *MockRepository, base float64) *Service {
	if repo.contracts == nil {
		repo.contracts = map[string]CustomerContract{}
	}
	return NewService(repo).WithRepository(anyProductResolveRepo{base: base})
}

func resolveReq(customerID, productID uuid.UUID, branch *uuid.UUID, qty float64) Request {
	return Request{CustomerID: customerID, ProductID: productID, BranchID: branch, Quantity: qtyOf(qty)}
}

func TestResolveSixRungsInOrder(t *testing.T) {
	branch := fxBranch
	level := fxLevel

	t.Run("contract wins over everything", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA")
		repo.setContract(fxCust, fxProd, nil, 7.5, "EA")
		repo.setProgram(fxCust, PricingProgram{LevelID: level, LevelCode: "SILVER", LevelBasis: "BASE", LevelAdjust: qtyOf(-10), LevelActive: true})
		res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, fxProd, &branch, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.UnitPrice != priceOf(7.5) || res.PriceBasis != SourceContract {
			t.Errorf("contract rung: price %d basis %s, want 7.5000 contract", res.UnitPrice, res.PriceBasis)
		}
	})

	t.Run("the branch contract row beats the all branch row", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA")
		repo.setContract(fxCust, fxProd, nil, 9, "EA")
		repo.setContract(fxCust, fxProd, &branch, 8, "EA")
		res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, fxProd, &branch, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.UnitPrice != priceOf(8) {
			t.Errorf("branch contract %d, want 8.0000", res.UnitPrice)
		}
		if res.RuleID == nil {
			t.Errorf("the contract's row id is nil")
		}
	})

	t.Run("an anonymous read runs rungs five and six", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA")
		res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(uuid.Nil, fxProd, &branch, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceBasis != SourceBase || res.UnitPrice != priceOf(10) {
			t.Errorf("anonymous: basis %s price %d, want base 10.0000", res.PriceBasis, res.UnitPrice)
		}
	})

	t.Run("the branch base price answers branch", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA")
		repo.setPrice(fxProd, nil, &branch, 9.25, "EA")
		res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(uuid.Nil, fxProd, &branch, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceBasis != SourceBranch || res.UnitPrice != priceOf(9.25) {
			t.Errorf("branch base: basis %s price %d, want branch 9.2500", res.PriceBasis, res.UnitPrice)
		}
		if res.ReferencePrice != priceOf(9.25) {
			t.Errorf("the reference price is the branch base when it exists, got %d", res.ReferencePrice)
		}
	})

	t.Run("an inactive level falls to the branch base", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA")
		repo.setPrice(fxProd, nil, &branch, 9.25, "EA")
		repo.setProgram(fxCust, PricingProgram{LevelID: level, LevelBasis: "BASE", LevelAdjust: qtyOf(-10), LevelActive: false})
		res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, fxProd, &branch, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceBasis != SourceBranch {
			t.Errorf("an inactive level's customer: basis %s, want branch", res.PriceBasis)
		}
	})
}

func TestResolveExplicitLevelPriceBeatsDerived(t *testing.T) {
	branch := fxBranch
	level := fxLevel
	repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA")
	repo.setProgram(fxCust, PricingProgram{
		ProfileID: uuid.New(), LevelID: level, LevelCode: "SILVER", LevelName: "Silver",
		LevelBasis: "BASE", LevelAdjust: qtyOf(-10), LevelActive: true,
	})
	// An explicit price at every branch beats the derived 10 percent off.
	repo.setPrice(fxProd, &level, nil, 8.75, "EA")

	res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, fxProd, &branch, 1))
	if err != nil {
		t.Fatal(err)
	}
	if res.PriceBasis != SourceLevel || res.UnitPrice != priceOf(8.75) {
		t.Errorf("explicit level price: basis %s price %d, want level 8.7500", res.PriceBasis, res.UnitPrice)
	}

	// The branch's explicit level price beats the every branch one.
	repo.setPrice(fxProd, &level, &branch, 8.5, "EA")
	res, err = newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, fxProd, &branch, 1))
	if err != nil {
		t.Fatal(err)
	}
	if res.UnitPrice != priceOf(8.5) {
		t.Errorf("branch level %d, want 8.5000", res.UnitPrice)
	}

	// Without an explicit price the BASE level derives: 10 percent off the
	// reference, rounded once to scale 4.
	repo2 := newFakeResolveRepo().withProduct(fxProd, 1.3725, "EA")
	repo2.setProgram(fxCust, PricingProgram{LevelID: level, LevelBasis: "BASE", LevelAdjust: qtyOf(-10), LevelActive: true})
	res, err = newResolveService(repo2).Resolve(context.Background(), resolveReq(fxCust, fxProd, nil, 1))
	if err != nil {
		t.Fatal(err)
	}
	if res.PriceBasis != SourceLevel || res.UnitPrice != priceOf(1.2353) {
		t.Errorf("derived level price: basis %s price %d, want level 1.2353 (1.3725 less ten percent, rounded once to scale 4)", res.PriceBasis, res.UnitPrice)
	}
	if res.ReferencePrice != priceOf(1.3725) {
		t.Errorf("reference %d, want 1.3725", res.ReferencePrice)
	}
}

func TestResolveLevelBases(t *testing.T) {
	branch := fxBranch
	level := fxLevel

	t.Run("average cost basis", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA").withCost(fxProd, 6)
		repo.setProgram(fxCust, PricingProgram{LevelID: level, LevelBasis: "AVERAGE_COST", LevelAdjust: qtyOf(25), LevelActive: true})
		res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, fxProd, &branch, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.UnitPrice != priceOf(7.5) || res.PriceUOM != "EA" {
			t.Errorf("AVERAGE_COST: %d per %s, want 7.5000 per EA (6 plus 25 percent)", res.UnitPrice, res.PriceUOM)
		}
	})

	t.Run("replacement cost from the primary vendor's default level", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA").withCost(fxProd, 6).withVendor(fxProd, fxVendor)
		repo.setVendorCost(fxVendor, fxProd, nil, 5, "EA", true, fxVndLevel, "LIST")
		repo.setProgram(fxCust, PricingProgram{LevelID: level, LevelBasis: "REPLACEMENT_COST", LevelAdjust: qtyOf(20), LevelActive: true})
		res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, fxProd, &branch, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.UnitPrice != priceOf(6) {
			t.Errorf("REPLACEMENT_COST: %d, want 6.0000 (5 plus 20 percent)", res.UnitPrice)
		}
	})

	t.Run("replacement cost in another purchase unit answers in that purchase unit with the pair", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "PCS").withUnit(fxProd, "MBF", 1, 187.5).withCost(fxProd, 6).withVendor(fxProd, fxVendor)
		repo.setVendorCost(fxVendor, fxProd, nil, 500, "MBF", true, fxVndLevel, "LIST")
		repo.setProgram(fxCust, PricingProgram{LevelID: level, LevelBasis: "REPLACEMENT_COST", LevelAdjust: qtyOf(20), LevelActive: true})
		res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, fxProd, &branch, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.UnitPrice != priceOf(600) || res.PriceUOM != "MBF" {
			t.Errorf("REPLACEMENT_COST per MBF: %d per %s, want 600.0000 per MBF (500 plus 20 percent)", res.UnitPrice, res.PriceUOM)
		}
		if res.UOMQty != qtyOf(187.5) || res.PriceUOMQty != qtyOf(1) {
			t.Errorf("PCS to MBF pair: %d to %d, want 187.5 to 1", res.UOMQty, res.PriceUOMQty)
		}
	})

	t.Run("replacement cost with no vendor cost falls back to average cost", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "PCS").withCost(fxProd, 6).withVendor(fxProd, fxVendor)
		repo.setProgram(fxCust, PricingProgram{LevelID: level, LevelBasis: "REPLACEMENT_COST", LevelAdjust: qtyOf(20), LevelActive: true})
		res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, fxProd, &branch, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.UnitPrice != priceOf(7.2) {
			t.Errorf("REPLACEMENT_COST fallback: %d, want 7.2000 (the average cost 6 plus 20 percent)", res.UnitPrice)
		}
		if res.Details == "" || !contains(res.Details, "average cost") {
			t.Errorf("the fallback names itself in the details; got %q", res.Details)
		}
	})

	t.Run("a cost basis with no cost falls to the next rung", func(t *testing.T) {
		repo := newFakeResolveRepo().withProduct(fxProd, 10, "EA")
		repo.setProgram(fxCust, PricingProgram{LevelID: level, LevelBasis: "AVERAGE_COST", LevelAdjust: qtyOf(25), LevelActive: true})
		res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, fxProd, &branch, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceBasis != SourceBase {
			t.Errorf("no cost to read: basis %s, want base", res.PriceBasis)
		}
	})
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestResolvePairForTheRequestUnit(t *testing.T) {
	branch := fxBranch
	// The 2x4x8: stocked in PCS, priced per MBF, sold by the piece. The pair
	// between PCS and MBF is 187.5 to 1 (ADR 0006 R2's example).
	repo := newFakeResolveRepo().withProduct(fxProd, 525, "PCS")
	repo.withUnit(fxProd, "LF", 8, 1).withUnit(fxProd, "BF", 1, 0.1875).withUnit(fxProd, "MBF", 1, 187.5)
	req := resolveReq(uuid.Nil, fxProd, &branch, 2)
	req.UOM = "PCS"
	res, err := newResolveService(repo).Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.PriceUOM != "PCS" || res.UOMQty != qtyOf(1) || res.PriceUOMQty != qtyOf(1) {
		t.Errorf("same unit: %d/%d, want 1/1", res.UOMQty, res.PriceUOMQty)
	}

	// A price per MBF (a level price) answers the pair 187.5 PCS to 1 MBF.
	level := fxLevel
	repo.setPrice(fxProd, &level, nil, 500, "MBF")
	repo.setProgram(fxCust, PricingProgram{LevelID: level, LevelBasis: "BASE", LevelAdjust: qtyOf(0), LevelActive: true})
	res, err = newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, fxProd, &branch, 1))
	if err != nil {
		t.Fatal(err)
	}
	if res.PriceUOM != "MBF" || res.UnitPrice != priceOf(500) {
		t.Errorf("level price per MBF: %d per %s, want 500.0000 per MBF", res.UnitPrice, res.PriceUOM)
	}
	if res.UOMQty != qtyOf(187.5) || res.PriceUOMQty != qtyOf(1) {
		t.Errorf("the PCS to MBF pair is %d to %d, want 187.5 to 1", res.UOMQty, res.PriceUOMQty)
	}
}

func TestResolveOtherUnitsAndLineExtension(t *testing.T) {
	branch := fxBranch

	t.Run("299 PC of 2x4x8 at 485.00 per MBF extends to 77341 cents", func(t *testing.T) {
		// ADR 0006 section 1 worked case: 299 PC of 2x4x8 (187.5 PC = 1 MBF)
		// at $485.00/MBF extends to $773.41 (77341 cents), never $773.42.
		repo := newFakeResolveRepo().withProduct(fxProd, 485, "PC")
		repo.facts[fxProd].PriceUOM = "MBF"
		repo.withUnit(fxProd, "MBF", 1, 187.5)
		req := resolveReq(fxCust, fxProd, &branch, 299)
		req.UOM = "PC"
		res, err := newResolveService(repo).Resolve(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceUOM != "MBF" || res.UnitPrice != priceOf(485) {
			t.Fatalf("got %d per %s, want 485.0000 per MBF", res.UnitPrice, res.PriceUOM)
		}
		if res.UOMQty != qtyOf(187.5) || res.PriceUOMQty != qtyOf(1) {
			t.Fatalf("got pair %d/%d, want 187.5/1", res.UOMQty, res.PriceUOMQty)
		}
		cents, err := httpx.Extend(req.Quantity, res.UOMQty, res.PriceUOMQty, res.UnitPrice)
		if err != nil {
			t.Fatal(err)
		}
		if cents != 77341 {
			t.Fatalf("299 PC at $485.00/MBF extended to %d cents, want 77341 ($773.41)", cents)
		}
	})

	t.Run("sheet goods per MSF, steel per CWT, and aggregate per TON", func(t *testing.T) {
		// Sheet goods: 4x8 sheet (32 SF), 31.25 SHT = 1 MSF, priced at $640/MSF.
		shtProd := uuid.New()
		repo := newFakeResolveRepo().withProduct(shtProd, 640, "SHT")
		repo.facts[shtProd].PriceUOM = "MSF"
		repo.withUnit(shtProd, "MSF", 1, 31.25)
		res, err := newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, shtProd, &branch, 10))
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceUOM != "MSF" || res.UOMQty != qtyOf(31.25) || res.PriceUOMQty != qtyOf(1) {
			t.Fatalf("MSF: got %s (%d/%d), want MSF (31.25/1)", res.PriceUOM, res.UOMQty, res.PriceUOMQty)
		}

		// Steel: stocked in LB, priced per CWT (1 CWT = 100 LB) at $85/CWT.
		steelProd := uuid.New()
		repo.withProduct(steelProd, 85, "LB").withUnit(steelProd, "CWT", 1, 100)
		repo.facts[steelProd].PriceUOM = "CWT"
		res, err = newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, steelProd, &branch, 250))
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceUOM != "CWT" || res.UOMQty != qtyOf(100) || res.PriceUOMQty != qtyOf(1) {
			t.Fatalf("CWT: got %s (%d/%d), want CWT (100/1)", res.PriceUOM, res.UOMQty, res.PriceUOMQty)
		}

		// Aggregate: stocked in LB, priced per TON (1 TON = 2000 LB) at $42/TON.
		aggProd := uuid.New()
		repo.withProduct(aggProd, 42, "LB").withUnit(aggProd, "TON", 1, 2000)
		repo.facts[aggProd].PriceUOM = "TON"
		res, err = newResolveService(repo).Resolve(context.Background(), resolveReq(fxCust, aggProd, &branch, 4000))
		if err != nil {
			t.Fatal(err)
		}
		if res.PriceUOM != "TON" || res.UOMQty != qtyOf(2000) || res.PriceUOMQty != qtyOf(1) {
			t.Fatalf("TON: got %s (%d/%d), want TON (2000/1)", res.PriceUOM, res.UOMQty, res.PriceUOMQty)
		}
	})
}
