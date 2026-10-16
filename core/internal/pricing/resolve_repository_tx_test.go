// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// The resolve side reads against the real database (ADR 0006 sections 5.3
// and 5.5): the contract's branch row before its all branch row, the
// explicit price at a level and branch, the vendor cost's branch, level,
// default level fallback and effective date rules, and ResolveCost
// answering the pair between a PCS purchase line and an MBF cost.

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func TestResolveRepositoryContractBranchBeforeAllBranch(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	repo := pricing.NewResolveRepository(db)

	cust := uuid.MustParse("a1000000-0122-4000-8000-000000000001")
	prod := uuid.MustParse("a1000000-0122-4000-8000-000000000002")
	branch := uuid.MustParse("a1000000-0122-4000-8000-000000000003")

	// The cleanup runs whatever the seeds managed to write, so a failed seed
	// never leaves a row a rerun would collide with.
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_contracts WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, prod)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, branch)
	})

	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)
		ON CONFLICT (id) DO NOTHING`, branch, "rr-"+branch.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, 'RR-P1', 'resolve repo product', 'EA', 10) ON CONFLICT (id) DO NOTHING`, prod); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'rr customer', 'RR-C1', $2) ON CONFLICT (id) DO NOTHING`, cust, branch); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	// The all branch row alone answers.
	allBranch := 8.5
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO customer_contracts (customer_id, product_id, contract_price, price_uom)
		VALUES ($1, $2, $3, 'EA')`, cust, prod, allBranch); err != nil {
		t.Fatalf("seed contract: %v", err)
	}
	c, err := repo.ContractForBranch(ctx, cust, prod, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.ContractPrice.DecimalString() != "8.5000" {
		t.Fatalf("all branch contract: %+v", c)
	}

	// A branch row added later beats it.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO customer_contracts (customer_id, product_id, contract_price, price_uom, branch_id)
		VALUES ($1, $2, 8, 'EA', $3)`, cust, prod, branch); err != nil {
		t.Fatalf("seed branch contract: %v", err)
	}
	c, err = repo.ContractForBranch(ctx, cust, prod, &branch)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.ContractPrice.DecimalString() != "8.0000" || c.BranchID == nil || *c.BranchID != branch {
		t.Fatalf("branch contract: %+v", c)
	}
	// The all branch read still answers the all branch row.
	c, err = repo.ContractForBranch(ctx, cust, prod, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.ContractPrice.DecimalString() != "8.5000" {
		t.Fatalf("all branch read: %+v", c)
	}
	// Another branch sees only the all branch row.
	other := uuid.MustParse("a1000000-0122-4000-8000-000000000004")
	c, err = repo.ContractForBranch(ctx, cust, prod, &other)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.ContractPrice.DecimalString() != "8.5000" {
		t.Fatalf("other branch read: %+v", c)
	}
}

func TestResolveCostBranchLevelDateRules(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	repo := pricing.NewResolveRepository(db)
	svc := pricing.NewService(nil).WithRepository(repo)

	vendor := uuid.MustParse("a2000000-0122-4000-8000-000000000001")
	product := uuid.MustParse("a2000000-0122-4000-8000-000000000002")
	branch := uuid.MustParse("a2000000-0122-4000-8000-000000000003")
	truckLevel := uuid.MustParse("a2000000-0122-4000-8000-000000000004")

	// The cleanup runs whatever the seeds managed to write, so a failed seed
	// never leaves a row a rerun would collide with.
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendor_product_costs WHERE vendor_id = $1`, vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendor_price_levels WHERE vendor_id = $1`, vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, branch)
	})

	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)
		ON CONFLICT (id) DO NOTHING`, branch, "rc-"+branch.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, 'RC-P1', 'resolve cost product', 'EA', 10) ON CONFLICT (id) DO NOTHING`, product); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO vendors (id, name) VALUES ($1, 'rc vendor')
		ON CONFLICT (id) DO NOTHING`, vendor); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO vendor_price_levels (id, vendor_id, code, name, is_default)
		VALUES ($2, $1, 'LIST', 'List', TRUE), ($3, $1, 'TRUCK', 'Truckload', FALSE)
		ON CONFLICT (vendor_id, code) DO NOTHING`,
		vendor, uuid.MustParse("a2000000-0122-4000-8000-0000000000a1"), truckLevel); err != nil {
		t.Fatalf("seed vendor levels: %v", err)
	}

	seed := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed cost: %v", err)
		}
	}
	today := time.Now().Format("2006-01-02")
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")

	// The default level's cost, all branch, effective today.
	seed(`INSERT INTO vendor_product_costs (vendor_id, vendor_price_level_id, product_id, purchase_uom, unit_cost, effective_from)
		VALUES ($1, (SELECT id FROM vendor_price_levels WHERE vendor_id = $1 AND code = 'LIST'), $2, 'EA', 4, $3)`,
		vendor, product, today)

	// No level named: the default level's.
	cost, err := svc.ResolveCost(ctx, vendor, product, nil, nil, time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	if cost == nil || cost.UnitCost.DecimalString() != "4.0000" || cost.LevelCode != "LIST" {
		t.Fatalf("default level cost: %+v", cost)
	}

	// A truckload cost beats the default when the level is named.
	seed(`INSERT INTO vendor_product_costs (vendor_id, vendor_price_level_id, product_id, purchase_uom, unit_cost, effective_from)
		VALUES ($1, $2, $3, 'EA', 3.5, $4)`, vendor, truckLevel, product, today)
	cost, err = svc.ResolveCost(ctx, vendor, product, nil, &truckLevel, time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	if cost == nil || cost.UnitCost.DecimalString() != "3.5000" || cost.LevelCode != "TRUCK" {
		t.Fatalf("named level cost: %+v", cost)
	}

	// An older cost of the default level, so the fallback below has a row
	// in effect at the date asked.
	seed(`INSERT INTO vendor_product_costs (vendor_id, vendor_price_level_id, product_id, purchase_uom, unit_cost, effective_from)
		VALUES ($1, (SELECT id FROM vendor_price_levels WHERE vendor_id = $1 AND code = 'LIST'), $2, 'EA', 3.9, $3)`,
		vendor, product, time.Now().AddDate(0, 0, -3).Format("2006-01-02"))

	// The named level without a row in effect falls back to the default.
	cost, err = svc.ResolveCost(ctx, vendor, product, nil, &truckLevel, time.Now().AddDate(0, 0, -2), "")
	if err != nil {
		t.Fatal(err)
	}
	if cost == nil || cost.LevelCode != "LIST" || cost.UnitCost.DecimalString() != "3.9000" {
		t.Fatalf("named level out of effect, want the default level's older cost: %+v", cost)
	}

	// P1-2 (review r1 argon): an all-branch (branch_id IS NULL) vendor cost
	// answers a branch-scoped read. This leg runs before the branch row is
	// seeded, so the all-branch row is the only candidate.
	cost, err = svc.ResolveCost(ctx, vendor, product, &branch, nil, time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	if cost == nil || cost.UnitCost.DecimalString() != "4.0000" {
		t.Fatalf("all-branch cost for a branch read: %+v, want 4.0000", cost)
	}

	// The branch row beats the all branch row.
	seed(`INSERT INTO vendor_product_costs (vendor_id, vendor_price_level_id, product_id, branch_id, purchase_uom, unit_cost, effective_from)
		VALUES ($1, (SELECT id FROM vendor_price_levels WHERE vendor_id = $1 AND code = 'LIST'), $2, $3, 'EA', 3.75, $4)`,
		vendor, product, branch, today)
	cost, err = svc.ResolveCost(ctx, vendor, product, &branch, nil, time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	if cost == nil || cost.UnitCost.DecimalString() != "3.7500" {
		t.Fatalf("branch cost: %+v", cost)
	}

	// The latest effective_from wins among the rows in effect: asked on the
	// day after tomorrow, the row dated for that day beats today's.
	seed(`INSERT INTO vendor_product_costs (vendor_id, vendor_price_level_id, product_id, purchase_uom, unit_cost, effective_from)
		VALUES ($1, (SELECT id FROM vendor_price_levels WHERE vendor_id = $1 AND code = 'LIST'), $2, 'EA', 4.25, $3)`,
		vendor, product, tomorrow)
	cost, err = svc.ResolveCost(ctx, vendor, product, nil, nil, time.Now().AddDate(0, 0, 2), "")
	if err != nil {
		t.Fatal(err)
	}
	if cost == nil || cost.UnitCost.DecimalString() != "4.2500" {
		t.Fatalf("latest effective cost: %+v", cost)
	}

	// A day before the earliest row, no cost is in effect and the answer is
	// none.
	cost, err = svc.ResolveCost(ctx, vendor, product, nil, nil, time.Now().AddDate(0, 0, -4), "")
	if err != nil {
		t.Fatal(err)
	}
	if cost != nil {
		t.Fatalf("a cost dated after the asked day answered: %+v", cost)
	}
	_ = tomorrow

	// A purchase line in a unit other than the cost's unit answers the pair:
	// 1 MBF of the product (stocked EA, with an MBF row of 1000 to 1) costs
	// 4.2500 per EA, so the pair is 1000 EA to 1 MBF.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
		VALUES ($1, 'MBF', 1, 1000, FALSE, TRUE, FALSE)`, product); err != nil {
		t.Fatalf("seed the MBF row of the unit set: %v", err)
	}
	cost, err = svc.ResolveCost(ctx, vendor, product, nil, nil, time.Now(), "MBF")
	if err != nil {
		t.Fatal(err)
	}
	if cost == nil {
		t.Fatal("no cost for the MBF purchase line")
	}
	// One MBF of the line is 1000 EA of the cost: the pair says so, and the
	// extension multiplies the cost by the second side.
	if cost.UOMQty.DecimalString() != "1.0000" || cost.PurchaseUOMQty.DecimalString() != "1000.0000" {
		t.Errorf("the MBF purchase line's pair is %s to %s, want 1 to 1000", cost.UOMQty.WireString(), cost.PurchaseUOMQty.WireString())
	}
	if cost.PurchaseUOM != "EA" {
		t.Errorf("the cost stays per its own unit: %d per %s", cost.UnitCost, cost.PurchaseUOM)
	}
}
