// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestMaskMockGeoKeepsWhatTheMockCannotMake pins the mask's reach: values in
// the mock geocoder's band become the anchor, while a null or a coordinate
// outside the band stays visible in the golden.
func TestMaskMockGeoKeepsWhatTheMockCannotMake(t *testing.T) {
	body := map[string]any{"deliveries": []any{
		map[string]any{"latitude": json.Number("49.9"), "longitude": json.Number("-119.4")},
		map[string]any{"latitude": nil, "longitude": nil},
		map[string]any{"latitude": json.Number("0"), "longitude": json.Number("-119.2")},
		map[string]any{"latitude": json.Number("50.5"), "longitude": json.Number("-119.5")},
	}}
	maskMockGeo(body)
	want := map[string]any{"deliveries": []any{
		map[string]any{"latitude": json.Number("49.888"), "longitude": json.Number("-119.496")},
		map[string]any{"latitude": nil, "longitude": nil},
		map[string]any{"latitude": json.Number("0"), "longitude": json.Number("-119.2")},
		map[string]any{"latitude": json.Number("50.5"), "longitude": json.Number("-119.496")},
	}}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("masked body = %v, want %v", body, want)
	}
}
