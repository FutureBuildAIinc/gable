// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// TestTaxPartsRoundsEachPartOnItsOwn pins CP-14 A1's arithmetic: GST and PST
// are levied under different statutes, so each part is rounded once on its
// own and the document's tax is their sum. At $12.50 that is one cent more
// than the combined rate rounded once (GST 0.625 to 63, PST 0.875 to 88, sum
// 151, against 150).
func TestTaxPartsRoundsEachPartOnItsOwn(t *testing.T) {
	gst, pst := int64(50000), int64(70000)
	cases := []struct {
		name    string
		taxable httpx.Cents
		rates   []int64
		want    []httpx.Cents
	}{
		{"$33.00 BC", 3300, []int64{gst, pst}, []httpx.Cents{165, 231}},
		{"$12.50 BC, one cent over the combined rounding", 1250, []int64{gst, pst}, []httpx.Cents{63, 88}},
		{"zero base", 0, []int64{gst, pst}, []httpx.Cents{0, 0}},
		{"negative base rounds away from zero", -1250, []int64{gst, pst}, []httpx.Cents{-63, -88}},
		{"one part", 1250, []int64{130000}, []httpx.Cents{163}},
		{"no parts", 1250, nil, []httpx.Cents{}},
	}
	for _, c := range cases {
		got := TaxParts(c.taxable, c.rates)
		if len(got) != len(c.want) {
			t.Fatalf("%s: %d parts, want %d", c.name, len(got), len(c.want))
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: part %d = %d, want %d", c.name, i, got[i], c.want[i])
			}
		}
	}
	if sum, once := TaxParts(1250, []int64{gst, pst}), TaxAt(1250, gst+pst); sum[0]+sum[1] != 151 || once != 150 {
		t.Errorf("$12.50: parts sum %d and combined %d, want 151 and 150", sum[0]+sum[1], once)
	}
}
