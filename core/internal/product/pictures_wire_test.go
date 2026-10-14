// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product_test

// The product's picture path on the wire: image_url is the path while the
// catalog.product_images setting is on, null while it is off or absent, and
// null for a product with none. The list and the single read carry it the
// same way (the quote builder's product search, the inventory list and the
// product page all read the product view).

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/platform/pictures"
)

const testPicture = "/catalog-illustrations/pictures-wire.svg"

func (f *fixture) pictureSetting(value string) {
	f.t.Helper()
	ctx := context.Background()
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM system_settings WHERE key = $1`, pictures.Setting)
	})
	if value == "" {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key = $1`, pictures.Setting)
		return
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO system_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, pictures.Setting, value); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) productWithPicture(path string) string {
	f.t.Helper()
	created := f.createProduct(nil)
	if created.status != 201 {
		f.t.Fatalf("create: %d %s", created.status, created.raw)
	}
	id := created.body["id"].(string)
	f.t.Cleanup(func() { f.deleteProduct(id) })
	if path != "" {
		if _, err := f.db.Pool.Exec(context.Background(), `UPDATE products SET image_url = $2 WHERE id = $1`, id, path); err != nil {
			f.t.Fatal(err)
		}
	}
	return id
}

func TestProductImageURL(t *testing.T) {
	f := newFixture(t)
	with, without := f.productWithPicture(testPicture), f.productWithPicture("")

	read := func(id string) any {
		r := f.do("GET", "/api/v1/products/"+id, nil)
		if r.status != 200 {
			t.Fatalf("get: %d %s", r.status, r.raw)
		}
		v, ok := r.body["image_url"]
		if !ok {
			t.Fatalf("image_url is missing from the product read: %s", r.raw)
		}
		return v
	}
	listed := func(id string) any {
		for _, q := range []string{"?limit=200", "?limit=200&include=total"} {
			r := f.do("GET", "/api/v1/products"+q, nil)
			if r.status != 200 {
				t.Fatalf("list: %d %s", r.status, r.raw)
			}
			for _, it := range r.body["items"].([]any) {
				m := it.(map[string]any)
				if m["id"] == id {
					v, ok := m["image_url"]
					if !ok {
						t.Fatalf("image_url is missing from a list item: %v", m)
					}
					return v
				}
			}
		}
		t.Fatalf("product %s not on the list's first page", id)
		return nil
	}

	f.pictureSetting("")
	if v := read(with); v != nil {
		t.Errorf("setting absent: image_url = %v, want null", v)
	}
	f.pictureSetting("off")
	if v := listed(with); v != nil {
		t.Errorf("setting off: list image_url = %v, want null", v)
	}
	f.pictureSetting("on")
	if v := read(with); v != testPicture {
		t.Errorf("setting on: image_url = %v, want %s", v, testPicture)
	}
	if v := listed(with); v != testPicture {
		t.Errorf("setting on: list image_url = %v, want %s", v, testPicture)
	}
	if v := read(without); v != nil {
		t.Errorf("setting on, no picture: image_url = %v, want null", v)
	}
	if v := listed(without); v != nil {
		t.Errorf("setting on, no picture: list image_url = %v, want null", v)
	}
}
