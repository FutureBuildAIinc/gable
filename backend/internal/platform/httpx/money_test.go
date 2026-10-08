// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// RULE (ADR 0001 §7): cents parse exactly from database decimal strings,
// never through float64. Postgres pads DECIMAL(19,4) columns to four
// digits, so trailing zeros beyond the cents scale are accepted.
func TestParseCents(t *testing.T) {
	cases := []struct {
		in   string
		want Cents
	}{
		{"0", 0},
		{"0.00", 0},
		{"-0.00", 0},
		{"12", 1200},
		{"12.3", 1230},
		{"12.34", 1234},
		{"12.3400", 1234}, // DECIMAL(19,4) money column
		{"-12.34", -1234},
		{"0.05", 5},
		{"-0.05", -5},
		{"92233720368547758.07", math.MaxInt64},
		{"-92233720368547758.08", math.MinInt64},
	}
	for _, tc := range cases {
		got, err := ParseCents(tc.in)
		if err != nil {
			t.Errorf("ParseCents(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseCents(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// RULE: a nonzero digit beyond the cents scale is refused, not rounded: a
// money column holding sub-cent precision is a data bug and must surface.
func TestParseCentsRefusesSubCentResidue(t *testing.T) {
	for _, in := range []string{"12.345", "12.341", "-12.345"} {
		if _, err := ParseCents(in); err == nil {
			t.Errorf("ParseCents(%q) succeeded, want a refusal", in)
		}
	}
}

// RULE: decimal strings only. No exponents, separators, NaN, whitespace, or
// bare punctuation: refuse everything Postgres would never emit.
func TestParseCentsRefusesGarbage(t *testing.T) {
	for _, in := range []string{
		"", "abc", "12,34", "1e3", "NaN", " 12.34", "12. 34", "-", "+",
		"12.", ".5", "12.34.56", "--12.34", "0x10",
	} {
		if _, err := ParseCents(in); err == nil {
			t.Errorf("ParseCents(%q) succeeded, want a refusal", in)
		}
	}
}

// RULE: a value beyond int64 is refused, not wrapped.
func TestParseCentsOverflow(t *testing.T) {
	for _, in := range []string{
		"9223372036854775808",           // int64 max + 1, unscaled
		strings.Repeat("9", 19) + ".99", // nineteen nines, over any scale
		strings.Repeat("9", 25),         // far over
		"92233720368547758.08",          // one cent past int64 max
	} {
		if _, err := ParseCents(in); err == nil {
			t.Errorf("ParseCents(%q) succeeded, want overflow refusal", in)
		}
	}
}

// RULE: the unit price parses exactly at scale 4, the DECIMAL(19,4) wire
// mirror. Trailing zeros beyond scale 4 are padding; a nonzero fifth digit
// is a value the store cannot keep and is refused.
func TestParsePrice(t *testing.T) {
	cases := []struct {
		in   string
		want Price
	}{
		{"0", 0},
		{"1", 10000},
		{"1.3", 13000},
		{"1.37", 13700},
		{"1.3725", 13725},
		{"-1.3725", -13725},
		{"0.0001", 1},
		{"-0.0001", -1},
		{"1.37250", 13725}, // zero tail beyond scale 4 is padding
		{"1000", 10000000},
	}
	for _, tc := range cases {
		got, err := ParsePrice(tc.in)
		if err != nil {
			t.Errorf("ParsePrice(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParsePrice(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, in := range []string{"1.37256", "-1.37251", "0.00001"} {
		if _, err := ParsePrice(in); err == nil {
			t.Errorf("ParsePrice(%q) succeeded, want a refusal beyond scale 4", in)
		}
	}
}

// RULE: formatting back to the database's decimal form is exact and fixed
// width: two digits for cents, four for prices, matching the column scales.
func TestDecimalStrings(t *testing.T) {
	centsCases := []struct {
		c    Cents
		want string
	}{
		{0, "0.00"}, {5, "0.05"}, {1234, "12.34"}, {1230, "12.30"},
		{-1234, "-12.34"}, {math.MaxInt64, "92233720368547758.07"},
		{math.MinInt64, "-92233720368547758.08"},
	}
	for _, tc := range centsCases {
		if got := tc.c.DecimalString(); got != tc.want {
			t.Errorf("Cents(%d).DecimalString() = %q, want %q", int64(tc.c), got, tc.want)
		}
	}
	priceCases := []struct {
		p    Price
		want string
	}{
		{0, "0.0000"}, {1, "0.0001"}, {13725, "1.3725"}, {-13725, "-1.3725"},
		{13700, "1.3700"}, {10000, "1.0000"},
	}
	for _, tc := range priceCases {
		if got := tc.p.DecimalString(); got != tc.want {
			t.Errorf("Price(%d).DecimalString() = %q, want %q", int64(tc.p), got, tc.want)
		}
	}
}

// RULE: the decimal string of a parsed value round trips.
func TestMoneyRoundTrip(t *testing.T) {
	for _, s := range []string{"0.00", "12.34", "-12.34", "92233720368547758.07", "-92233720368547758.08"} {
		c, err := ParseCents(s)
		if err != nil {
			t.Fatalf("ParseCents(%q): %v", s, err)
		}
		if got := c.DecimalString(); got != s {
			t.Errorf("cents round trip: %q became %q", s, got)
		}
	}
	for _, s := range []string{"0.0000", "1.3725", "-1.3725"} {
		p, err := ParsePrice(s)
		if err != nil {
			t.Fatalf("ParsePrice(%q): %v", s, err)
		}
		if got := p.DecimalString(); got != s {
			t.Errorf("price round trip: %q became %q", s, got)
		}
	}
}

// RULE: the parse errors state their class, so a bad column value says why
// it was refused rather than returning a bare failure.
func TestParseErrorMessages(t *testing.T) {
	if _, err := ParseCents("NaN"); err.Error() != "not a plain decimal number" {
		t.Errorf("NaN error = %q", err.Error())
	}
	if _, err := ParseCents("12.345"); err.Error() != "carries precision beyond the fixed scale" {
		t.Errorf("sub-cent error = %q", err.Error())
	}
	if _, err := ParseCents(strings.Repeat("9", 25)); err.Error() != "beyond the range of a 64-bit integer at this scale" {
		t.Errorf("overflow error = %q", err.Error())
	}
}

// RULE: no float on the wire. Cents and Price serialize as JSON integers,
// and a JSON number with a fraction does not decode into one.
func TestMoneyOnTheWireIsNeverFloat(t *testing.T) {
	type payload struct {
		TotalCents int64 `json:"total_cents"`
		UnitPrice  Price `json:"unit_price_ten_thousandths"`
	}
	out, err := json.Marshal(payload{TotalCents: 1234, UnitPrice: 13725})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"total_cents":1234,"unit_price_ten_thousandths":13725}` {
		t.Errorf("wire form = %s, want integer cents and scaled price", out)
	}

	for _, bad := range []string{
		`{"total_cents":12.34}`,
		`{"total_cents":"1234"}`,
		`{"total_cents":1e3}`,
	} {
		var p payload
		if err := json.Unmarshal([]byte(bad), &p); err == nil {
			t.Errorf("decoded %s without error; a fraction or string in a money field must not decode", bad)
		}
	}
	var p payload
	if err := json.Unmarshal([]byte(`{"total_cents":1234,"unit_price_ten_thousandths":-13725}`), &p); err != nil {
		t.Fatalf("integer decode: %v", err)
	}
	if p.TotalCents != 1234 || p.UnitPrice != -13725 {
		t.Errorf("decoded = %+v", p)
	}
}
