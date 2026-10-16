// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// A unit price prints with two places, and a third or fourth only when the
// price truly has it, grouped like every other amount (visual review V2 2).
// The price is held in ten thousandths; no figure changes, only its text.

func TestUnitPricePrintsTwoPlacesUnlessItTrulyHasMore(t *testing.T) {
	st := newStyle("en-US")
	cases := []struct {
		price httpx.Price
		want  string
	}{
		{60000, "$6.00"},
		{980000, "$98.00"},
		{51000, "$5.10"},
		{14025, "$1.4025"},
		{525, "$0.0525"},
		{1250, "$0.125"},
		{1, "$0.0001"},
		{100, "$0.01"},
		{0, "$0.00"},
		{12345678, "$1,234.5678"},
		{39299700, "$3,929.97"},
		{10000000, "$1,000.00"},
		{-14025, "-$1.4025"},
		{-60000, "-$6.00"},
	}
	for _, c := range cases {
		if got := st.UnitPrice(c.price); got != c.want {
			t.Errorf("UnitPrice(%d) = %q, want %q", int64(c.price), got, c.want)
		}
	}
}
