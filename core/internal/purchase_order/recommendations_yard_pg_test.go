// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/gablelbm/gable/pkg/branchctx"
)

// recRows is the seeded product's recommendation rows for ctx as yard -> stock.
func recRows(t *testing.T, s *createBranchSeed, ctx context.Context) map[string]float64 {
	t.Helper()
	rs := purchase_order.NewRecommendationService(purchase_order.NewRepository(s.db),
		inventory.NewService(inventory.NewRepository(s.db)),
		product.NewService(product.NewRepository(s.db)),
		vendor.NewService(vendor.NewRepository(s.db)))
	sum, err := rs.GenerateRecommendations(ctx)
	if err != nil {
		t.Fatalf("recommendations: %v", err)
	}
	raw, _ := json.Marshal(sum)
	var out struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(raw, &out)
	rows := map[string]float64{}
	for _, it := range out.Items {
		if it["product_id"] != s.product.String() {
			continue
		}
		yard, _ := it["branch_id"].(string)
		stock, _ := it["current_stock"].(float64)
		rows[yard] = stock
	}
	return rows
}

// Recommendations read per yard like the reorder list: 2 at yard A and 3 at
// yard B are two rows, one per yard, each from its own yard's stock; the
// company's 5 is not a figure the page shows.
func TestRecommendations_ARowPerYardWithNoHeader(t *testing.T) {
	s := seedCreateBranch(t)
	stockAt(t, s, s.branchB, 3)
	if _, err := s.db.Pool.Exec(context.Background(),
		`UPDATE inventory SET quantity = 2 WHERE product_id = $1 AND location_id = $2`, s.product, s.branchA); err != nil {
		t.Fatal(err)
	}
	got := recRows(t, s, branchctx.With(context.Background(), &branchctx.Context{UserSub: "boss", IsAdmin: true}))
	if len(got) != 2 || got[s.branchA.String()] != 2 || got[s.branchB.String()] != 3 {
		t.Fatalf("rows by yard = %v, want yard A 2 and yard B 3", got)
	}
}

// With a header, the chosen yard only; and a yard well stocked is not
// recommended because the other yard is short (the company's total would hide
// the short yard).
func TestRecommendations_TheChosenYardOnlyAndAShortYardIsNotHidden(t *testing.T) {
	s := seedCreateBranch(t)
	stockAt(t, s, s.branchB, 500)
	if _, err := s.db.Pool.Exec(context.Background(),
		`UPDATE inventory SET quantity = 2 WHERE product_id = $1 AND location_id = $2`, s.product, s.branchA); err != nil {
		t.Fatal(err)
	}
	chosen := s.branchA
	got := recRows(t, s, branchctx.With(context.Background(), &branchctx.Context{UserSub: "u", BranchID: &chosen}))
	if len(got) != 1 || got[s.branchA.String()] != 2 {
		t.Fatalf("rows by yard with a header = %v, want yard A at 2 only", got)
	}
	all := recRows(t, s, branchctx.With(context.Background(), &branchctx.Context{UserSub: "boss", IsAdmin: true}))
	if len(all) != 1 || all[s.branchA.String()] != 2 {
		t.Fatalf("rows by yard with no header = %v, want only the short yard A (yard B's 500 is well stocked)", all)
	}
}

// A recommendation row names its product's vendor by id (the new purchase
// order is created for that id), and a product with no vendor carries none.
func TestRecommendations_ARowCarriesItsVendorsID(t *testing.T) {
	s := seedCreateBranch(t)
	if _, err := s.db.Pool.Exec(context.Background(),
		`UPDATE inventory SET quantity = 2 WHERE product_id = $1 AND location_id = $2`, s.product, s.branchA); err != nil {
		t.Fatal(err)
	}
	vendorOf := func() (any, bool) {
		rs := purchase_order.NewRecommendationService(purchase_order.NewRepository(s.db),
			inventory.NewService(inventory.NewRepository(s.db)),
			product.NewService(product.NewRepository(s.db)),
			vendor.NewService(vendor.NewRepository(s.db)))
		sum, err := rs.GenerateRecommendations(branchctx.With(context.Background(), &branchctx.Context{UserSub: "boss", IsAdmin: true}))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(sum)
		var out struct {
			Items []map[string]any `json:"items"`
		}
		_ = json.Unmarshal(raw, &out)
		for _, it := range out.Items {
			if it["product_id"] == s.product.String() {
				return it["vendor_id"], true
			}
		}
		return nil, false
	}
	got, ok := vendorOf()
	if !ok || got != s.vendor.String() {
		t.Fatalf("vendor_id = %v (row found %t), want the product's vendor %s", got, ok, s.vendor)
	}
	if _, err := s.db.Pool.Exec(context.Background(), `UPDATE products SET vendor_id = NULL, vendor = NULL WHERE id = $1`, s.product); err != nil {
		t.Fatal(err)
	}
	got, ok = vendorOf()
	if !ok || got != nil {
		t.Fatalf("vendor_id = %v (row found %t), want none for a product with no vendor", got, ok)
	}
}
