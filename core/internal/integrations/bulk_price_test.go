// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// The seam quotes cents the order will bill: a sub cent price is rounded half
// away from zero, never truncated (a price of 8.0082 is 801 cents, 20.025 is
// 2003, where a float multiply and truncation answered 800 and 2002).
func TestBulkPrice_RoundsSubCentPricesHalfAwayFromZero(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	customerID := uuid.New()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Bulk Price Co', $2, `+branch+`)`, customerID, "BULK-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
	})
	cases := []struct {
		base  string
		unit  int64
		total int64
	}{{"8.0082", 801, 2403}, {"20.025", 2003, 6009}, {"1.0049", 100, 300}}
	var items []map[string]any
	var ids []uuid.UUID
	for _, c := range cases {
		id := uuid.New()
		ids = append(ids, id)
		if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
			VALUES ($1, $2, 'sub cent', 'PCS', $3::numeric)`, id, "BULK-"+id.String()[:8], c.base); err != nil {
			t.Fatalf("seed product: %v", err)
		}
		items = append(items, map[string]any{"product_id": id.String(), "quantity": 3})
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
		}
	})

	h := NewHandler(db, pricing.NewService(pricing.NewRepository(db)), nil, nil,
		customer.NewService(customer.NewRepository(db)), product.NewService(product.NewRepository(db)), "k")
	body, _ := json.Marshal(map[string]any{"customer_id": customerID.String(), "items": items})
	req := httptest.NewRequest("POST", "/api/integration/quotes/bulk-price", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.BulkCalculatePrice(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk-price = %d %s", rec.Code, rec.Body.String())
	}
	var got []PricedItemResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != len(cases) {
		t.Fatalf("answer %s (%v)", rec.Body.String(), err)
	}
	for i, c := range cases {
		if got[i].UnitPrice != c.unit || got[i].TotalPrice != c.total {
			t.Errorf("base %s: unit %d total %d, want %d and %d", c.base, got[i].UnitPrice, got[i].TotalPrice, c.unit, c.total)
		}
	}
}
