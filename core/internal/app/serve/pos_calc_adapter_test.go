// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// The counter (fenced) turns the adapter's float into cents with
// int64(price*100+0.5). The adapter hands it a price already rounded half
// away from zero to whole cents, so a half cent price rings the cent above:
// 20.025 rings 20.03, where truncating the float 2002.4999 rang 20.02.
func TestPosCalcAdapter_RingsHalfCentsAwayFromZero(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	customerID, productID := uuid.New(), uuid.New()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Counter Co', $2, `+branch+`)`, customerID, "CTR-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, 'half cent', 'PCS', 20.025)`, productID, "CTR-"+productID.String()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
	})

	a := &posCalcAdapter{
		pricingSvc:  pricing.NewService(pricing.NewRepository(db)),
		customerSvc: customer.NewService(customer.NewRepository(db)),
	}
	got, err := a.CalculateItemPrice(ctx, customerID, productID, 20.025, 1)
	if err != nil {
		t.Fatal(err)
	}
	if cents := int64(got*100.0 + 0.5); cents != 2003 {
		t.Fatalf("the counter would ring %d cents from %v, want 2003", cents, got)
	}
	// Every whole cent value survives the counter's own conversion.
	for c := int64(0); c <= 2_000_000; c += 7 {
		if back := int64((float64(c)/100)*100.0 + 0.5); back != c {
			t.Fatalf("%d cents come back as %d through the counter's conversion", c, back)
		}
	}
}
