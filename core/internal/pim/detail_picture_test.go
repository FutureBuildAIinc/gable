// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pim

// The product page's read carries the picture path in its product summary:
// the path while catalog.product_images is on, null while it is off or the
// product has none.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/platform/pictures"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func TestProductDetailCarriesTheImagePath(t *testing.T) {
	testutil.LockProductImageSetting(t)
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	ctx := context.Background()
	tag := uuid.NewString()[:8]
	with, without := uuid.New(), uuid.New()
	for id, img := range map[uuid.UUID]any{with: "/catalog-illustrations/detail-wire.svg", without: nil} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price, image_url)
			VALUES ($1, $2, 'detail picture', 'PCS', 1, $3)`, id, "PIMPIC-"+tag+id.String()[:4], img); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id IN ($1, $2)`, with, without)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key = $1`, pictures.Setting)
	})
	set := func(v string) {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO system_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, pictures.Setting, v); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	NewHandler(NewService(NewRepository(db), product.NewService(product.NewRepository(db)))).RegisterRoutes(mux)
	read := func(id uuid.UUID) any {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products/"+id.String()+"/detail", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("detail: %d %s", rec.Code, rec.Body)
		}
		var body struct {
			Product map[string]any `json:"product"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		v, ok := body.Product["image_url"]
		if !ok {
			t.Fatalf("image_url missing from the detail's product: %s", rec.Body)
		}
		return v
	}
	set("off")
	if v := read(with); v != nil {
		t.Errorf("setting off: image_url = %v, want null", v)
	}
	set("on")
	if v := read(with); v != "/catalog-illustrations/detail-wire.svg" {
		t.Errorf("setting on: image_url = %v", v)
	}
	if v := read(without); v != nil {
		t.Errorf("setting on, no picture: image_url = %v, want null", v)
	}
}
