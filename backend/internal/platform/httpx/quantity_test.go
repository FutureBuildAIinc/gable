// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"encoding/json"
	"strings"
	"testing"
)

// RULE (ADR 0001 §7a): a quantity or conversion factor is a plain decimal
// with at most four fraction digits, carried internally at the same fixed
// scale as the price, and parsed exactly, never through float64.
func TestParseQuantity(t *testing.T) {
	cases := []struct {
		in   string
		want Quantity
	}{
		{"0", 0},
		{"1000", 10000000},
		{"12.5", 125000},
		{"-12.5", -125000},
		{"0.0001", 1},
		{"-0.0001", -1},
		{"1600.25", 16002500},
		{"12.5000", 125000}, // trailing zeros are padding, from the wire or the column
		{"0.001", 10},       // the each-into-thousands conversion factor
	}
	for _, tc := range cases {
		got, err := ParseQuantity(tc.in)
		if err != nil {
			t.Errorf("ParseQuantity(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseQuantity(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, in := range []string{
		"12.50005", // a fifth fraction digit is precision this type refuses
		"", "abc", "1e3", "12.", ".5", " 12.5", "12,5", "+12.5",
	} {
		if _, err := ParseQuantity(in); err == nil {
			t.Errorf("ParseQuantity(%q) succeeded, want a refusal", in)
		}
	}
}

// RULE: the database form is the fixed four-digit decimal the NUMERIC(12,4)
// column takes, exactly like the price.
func TestQuantityDecimalString(t *testing.T) {
	cases := []struct {
		q    Quantity
		want string
	}{
		{0, "0.0000"},
		{125000, "12.5000"},
		{1, "0.0001"},
		{-125000, "-12.5000"},
	}
	for _, tc := range cases {
		if got := tc.q.DecimalString(); got != tc.want {
			t.Errorf("Quantity(%d).DecimalString() = %q, want %q", int64(tc.q), got, tc.want)
		}
	}
}

// RULE: the wire form is the shortest exact decimal string: no trailing
// zero padding, no fraction at all when the value is whole.
func TestQuantityWireString(t *testing.T) {
	cases := []struct {
		q    Quantity
		want string
	}{
		{0, "0"},
		{125000, "12.5"},
		{10000000, "1000"},
		{1, "0.0001"},
		{120000, "12"},
		{-125000, "-12.5"},
	}
	for _, tc := range cases {
		if got := tc.q.WireString(); got != tc.want {
			t.Errorf("Quantity(%d).WireString() = %q, want %q", int64(tc.q), got, tc.want)
		}
	}
	for _, s := range []string{"0", "1000", "12.5", "0.0001", "-1600.25"} {
		q, err := ParseQuantity(s)
		if err != nil {
			t.Fatalf("ParseQuantity(%q): %v", s, err)
		}
		if got := q.WireString(); got != s {
			t.Errorf("round trip: %q became %q", s, got)
		}
	}
}

// RULE: on the wire a quantity is a JSON string, never a JSON number: a
// number in a quantity field does not decode, so no client can send a
// float where a decimal string is the contract.
func TestQuantityOnTheWireIsAString(t *testing.T) {
	type line struct {
		Quantity    Quantity `json:"quantity"`
		Converts    Quantity `json:"conversion_factor"`
		UnitPrice   Price    `json:"unit_price_ten_thousandths"`
		PricePerM   Price    `json:"price_per_m_ten_thousandths"`
		TotalCents  Cents    `json:"total_cents"`
		IgnoreExtra string   `json:"-"`
	}
	out, err := json.Marshal(line{Quantity: 125000, Converts: 10, UnitPrice: 13725, PricePerM: 37500, TotalCents: 375})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"quantity":"12.5","conversion_factor":"0.001","unit_price_ten_thousandths":13725,"price_per_m_ten_thousandths":37500,"total_cents":375}`
	if string(out) != want {
		t.Errorf("wire form = %s, want %s", out, want)
	}

	var l line
	if err := json.Unmarshal([]byte(`{"quantity":"1600.25","unit_price_ten_thousandths":4500000}`), &l); err != nil {
		t.Fatalf("string decode: %v", err)
	}
	if l.Quantity != 16002500 || l.UnitPrice != 4500000 {
		t.Errorf("decoded = %+v", l)
	}

	for _, bad := range []string{
		`{"quantity":1600.25}`,       // a JSON number
		`{"quantity":1600}`,          // a whole JSON number is still a number
		`{"quantity":"1e3"}`,         // exponent form
		`{"quantity":"1600.250005"}`, // a sixth fraction digit
		`{"quantity":""}`,
	} {
		var l line
		if err := json.Unmarshal([]byte(bad), &l); err == nil {
			t.Errorf("decoded %s without error; a JSON number or a bad decimal in a quantity field must not decode", bad)
		}
	}
	if strings.Contains(string(out), ":1600") {
		t.Error("a quantity serialized as a JSON number")
	}
}
