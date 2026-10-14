// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pictures_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/platform/pictures"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func TestImageSQL(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	tag := uuid.NewString()[:8]
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE sku LIKE $1`, "PICSQL-"+tag+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key = $1`, pictures.Setting)
	})
	add := func(n, image string) string {
		var img any
		if image != "" {
			img = image
		}
		sku := "PICSQL-" + tag + n
		if _, err := db.Pool.Exec(ctx, `INSERT INTO products (sku, description, uom_primary, base_price, image_url)
			VALUES ($1, 'picture sql', 'PCS', 1, $2)`, sku, img); err != nil {
			t.Fatal(err)
		}
		return sku
	}
	with, without := add("A", "/catalog-illustrations/x.svg"), add("B", "")
	read := func(sku string) *string {
		var got *string
		if err := db.Pool.QueryRow(ctx, `SELECT `+pictures.ImageSQL("p")+` FROM products p WHERE p.sku = $1`, sku).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	set := func(v string) {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO system_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, pictures.Setting, v); err != nil {
			t.Fatal(err)
		}
	}
	if read(with) != nil {
		t.Error("setting absent: want null")
	}
	set("off")
	if read(with) != nil {
		t.Error("setting off: want null")
	}
	set("on")
	if got := read(with); got == nil || *got != "/catalog-illustrations/x.svg" {
		t.Errorf("setting on: got %v", got)
	}
	if read(without) != nil {
		t.Error("setting on, no picture: want null")
	}
}
