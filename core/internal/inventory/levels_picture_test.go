// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory_test

// The levels list's product summary carries the picture path: the path while
// catalog.product_images is on, null while it is off or the product has none.

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/platform/pictures"
	"github.com/gablelbm/gable/internal/testutil"
)

func TestInventoryLevelsProductSummaryImageURL(t *testing.T) {
	testutil.LockProductImageSetting(t)
	f := newFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM system_settings WHERE key = $1`, pictures.Setting)
	})
	if _, err := f.db.Pool.Exec(ctx, `UPDATE products SET image_url = '/catalog-illustrations/levels-wire.svg' WHERE id = $1`, f.product); err != nil {
		t.Fatal(err)
	}
	set := func(v string) {
		if _, err := f.db.Pool.Exec(ctx, `INSERT INTO system_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, pictures.Setting, v); err != nil {
			t.Fatal(err)
		}
	}
	image := func(product string) any {
		res := f.get("/api/v1/inventory?include=product&product_id=" + product)
		if res.status != 200 {
			t.Fatalf("list: %d %s", res.status, res.raw)
		}
		items := res.body["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("no rows for %s: %s", product, res.raw)
		}
		summary := items[0].(map[string]any)["product"].(map[string]any)
		v, ok := summary["image_url"]
		if !ok {
			t.Fatalf("image_url missing from the product summary: %s", res.raw)
		}
		return v
	}
	set("off")
	if v := image(f.product.String()); v != nil {
		t.Errorf("setting off: image_url = %v, want null", v)
	}
	set("on")
	if v := image(f.product.String()); v != "/catalog-illustrations/levels-wire.svg" {
		t.Errorf("setting on: image_url = %v", v)
	}
	// the second product has no picture and no rows; give it a row to read
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated)
		VALUES ($1, $2, 'wl-inv-none', 1, 0)`, f.other, f.yards[0]); err != nil {
		t.Fatal(err)
	}
	if v := image(f.other.String()); v != nil {
		t.Errorf("setting on, no picture: image_url = %v, want null", v)
	}
}
