// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product_test

// The density of a weighed product (CP-19): pounds per cubic yard with its
// volume state, set at create, changed by the PATCH, read back on the wire,
// both halves or neither, and the revision precondition on the write.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestDensityCreatePatchAndRead(t *testing.T) {
	f := newFixture(t)
	create := f.do("POST", "/api/v1/products", map[string]any{
		"sku": "DNS-" + uuid.NewString()[:8], "description": "crushed base rock",
		"stock_uom": "CY", "base_price_ten_thousandths": json.RawMessage("420000"),
		"density_lb_per_cy": "2700", "density_volume_state": "loose",
	})
	if create.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", create.status, create.raw)
	}
	defer f.deleteProduct(str(create.body["id"]))
	if got, _ := create.body["density_lb_per_cy"].(string); got != "2700" {
		t.Errorf("create read back density %q", got)
	}
	if got, _ := create.body["density_volume_state"].(string); got != "LOOSE" {
		t.Errorf("create read back state %q", got)
	}
	id := str(create.body["id"])
	revision := create.body["revision"].(json.Number)

	// Half a density is no density.
	half := f.do("POST", "/api/v1/products", map[string]any{
		"sku": "DNS-" + uuid.NewString()[:8], "description": "half a density", "stock_uom": "CY",
		"density_lb_per_cy": "2700",
	})
	if half.status != http.StatusBadRequest {
		t.Errorf("half a density = %d: %s", half.status, half.raw)
	}

	// The PATCH changes it on the revision, and the read carries it.
	patch := f.do("PATCH", "/api/v1/products/"+id+"/density", map[string]any{
		"density_lb_per_cy": "2715.5", "density_volume_state": "compacted", "revision": revision,
	})
	if patch.status != http.StatusOK {
		t.Fatalf("patch = %d: %s", patch.status, patch.raw)
	}
	if got, _ := patch.body["density_lb_per_cy"].(string); got != "2715.5" {
		t.Errorf("patched density %q", got)
	}
	if got, _ := patch.body["density_volume_state"].(string); got != "COMPACTED" {
		t.Errorf("patched state %q", got)
	}
	read := f.do("GET", "/api/v1/products/"+id, nil)
	if got, _ := read.body["density_lb_per_cy"].(string); got != "2715.5" {
		t.Errorf("read density %q", read.body["density_lb_per_cy"])
	}
	stale := f.do("PATCH", "/api/v1/products/"+id+"/density", map[string]any{
		"density_lb_per_cy": "2700", "density_volume_state": "loose", "revision": revision,
	})
	if stale.status != http.StatusConflict {
		t.Errorf("stale patch = %d: %s", stale.status, stale.raw)
	}

	// Null null clears it.
	cleared := f.do("PATCH", "/api/v1/products/"+id+"/density", map[string]any{
		"density_lb_per_cy": nil, "density_volume_state": nil, "revision": patch.body["revision"],
	})
	if cleared.status != http.StatusOK {
		t.Fatalf("clear = %d: %s", cleared.status, cleared.raw)
	}
	if cleared.body["density_lb_per_cy"] != nil {
		t.Errorf("cleared density %v", cleared.body["density_lb_per_cy"])
	}
}
