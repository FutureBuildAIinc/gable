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

// RULE (ADR 0001 §7a): one canonical spelling on the wire, the same
// posture as limit. Leading zeros and negative zero are refused; trailing
// fraction zeros are the column's own padding and stay accepted, because
// the same parser reads what the database sends back.
func TestParseQuantityCanonicalForm(t *testing.T) {
	for _, in := range []string{
		"00012",  // a leading zero run
		"0012.5", // the same, with a fraction
		"00",     // all zeros with a run of them
		"-0",     // negative zero
		"-0.0000",
		"+1", // a sign the form does not carry
	} {
		if _, err := ParseQuantity(in); err == nil {
			t.Errorf("ParseQuantity(%q) succeeded, want a refusal", in)
		}
	}
	for _, in := range []string{"0", "0.5", "-0.5", "12.5000", "0.0000"} {
		if _, err := ParseQuantity(in); err != nil {
			t.Errorf("ParseQuantity(%q): %v", in, err)
		}
	}
}

// RULE: a magnitude the NUMERIC(12,4) column cannot hold is refused at the
// parse boundary (a 400 through the validator), not stored as a database
// fault later. The bound is the column's own: 99999999.9999 either way.
func TestParseQuantityBound(t *testing.T) {
	if q, err := ParseQuantity("99999999.9999"); err != nil || q != QuantityMax {
		t.Errorf(`ParseQuantity("99999999.9999") = %d, %v; want QuantityMax`, q, err)
	}
	if q, err := ParseQuantity("-99999999.9999"); err != nil || q != -QuantityMax {
		t.Errorf(`ParseQuantity("-99999999.9999") = %d, %v; want -QuantityMax`, q, err)
	}
	for _, in := range []string{
		"100000000", // one past the column's digits
		"-100000000",
		"100000000.0000",
		strings.Repeat("9", 20),
	} {
		if _, err := ParseQuantity(in); err == nil {
			t.Errorf("ParseQuantity(%q) succeeded, want a refusal past the column bound", in)
		}
	}
}

// RULE (ADR 0001 §7a): signs carry one meaning. A quantity is negative only
// on a return or credit line; a unit price is never negative. Converting
// modules enforce it from their validators through this helper, which
// collects the offences beside the line's other field errors in one pass
// (ADR 0001 §4), each named by the line's JSON path.
func TestCheckLineSign(t *testing.T) {
	clean := &Validator{}
	CheckLineSign(clean, "lines[0]", Quantity(10000), Price(15000), false)
	CheckLineSign(clean, "lines[0]", Quantity(-10000), Price(15000), true)
	CheckLineSign(clean, "lines[0]", Quantity(0), Price(0), false)
	if err := clean.Err(); err != nil {
		t.Errorf("an ordinary line refused: %v", err)
	}

	v := &Validator{}
	CheckLineSign(v, "lines[2]", Quantity(-10000), Price(15000), false)
	CheckLineSign(v, "lines[2]", Quantity(10000), Price(-15000), false)
	e, ok := v.Err().(*Error)
	if !ok {
		t.Fatalf("Err() is %T, want *Error", v.Err())
	}
	if e.Status != 400 || e.Code != CodeValidationFailed {
		t.Errorf("status/code = %d/%q, want 400 validation_failed", e.Status, e.Code)
	}
	want := []FieldError{
		{Field: "lines[2].quantity", Message: "a negative quantity belongs to a return or credit line"},
		{Field: "lines[2].unit_price_ten_thousandths", Message: "a unit price is never negative"},
	}
	if len(e.Details) != len(want) {
		t.Fatalf("details = %+v, want %+v", e.Details, want)
	}
	for i, w := range want {
		if e.Details[i] != w {
			t.Errorf("details[%d] = %+v, want %+v", i, e.Details[i], w)
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
// float where a decimal string is the contract. The conversion between a
// line's sale unit and its price unit is the pair `uom_qty`, `price_uom_qty`
// (ADR 0001 §7a): 187.5 PCS = 1 MBF travels as "187.5" and "1".
func TestQuantityOnTheWireIsAString(t *testing.T) {
	type line struct {
		Quantity    Quantity `json:"quantity"`
		UomQty      Quantity `json:"uom_qty"`
		PriceUomQty Quantity `json:"price_uom_qty"`
		UnitPrice   Price    `json:"unit_price_ten_thousandths"`
		TotalCents  Cents    `json:"total_cents"`
		IgnoreExtra string   `json:"-"`
	}
	out, err := json.Marshal(line{Quantity: 125000, UomQty: 1875000, PriceUomQty: 10000, UnitPrice: 13725, TotalCents: 375})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"quantity":"12.5","uom_qty":"187.5","price_uom_qty":"1","unit_price_ten_thousandths":13725,"total_cents":375}`
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

// RULE (ADR 0001 §7a): the extension of a line is the quantity, converted
// to the price unit through the pair (uomQty of the sale unit = priceUomQty
// of the price unit), times the unit price, rounded once, to cents, half
// away from zero. The product is exact until that one rounding; it is
// computed here, once, for every module. A single scale 4 factor cannot
// carry a conversion like 187.5 pieces per thousand board feet (0.0053 at
// scale 4, half a percent off a whole MBF); the pair holds both sides
// exactly.
func TestExtend(t *testing.T) {
	mustQ := func(s string) Quantity {
		q, err := ParseQuantity(s)
		if err != nil {
			t.Fatalf("ParseQuantity(%q): %v", s, err)
		}
		return q
	}
	cases := []struct {
		name        string
		qty         string
		uomQty      string
		priceUomQty string
		price       Price
		want        Cents
	}{
		{"ten at 1.50 each", "10", "1", "1", 15000, 1500},
		{"1000 each at 3.75 per M", "1000", "1000", "1", 37500, 375},
		{"1600 BF at 450.00 per MBF", "1600", "1000", "1", 4500000, 72000},
		{"a whole MBF of pieces at 500.00 per MBF is exact", "187.5", "187.5", "1", 5000000, 50000},
		{"one piece at 500.00 per MBF rounds to 2.67", "1", "187.5", "1", 5000000, 267},
		{"24 pieces sold as one bundle at 12.00 per bundle", "24", "24", "1", 120000, 1200},
		{"two bundles priced per each through 1 BDL = 24 EA", "2", "1", "24", 5000, 2400},
		{"a credit line", "-100", "1", "1", 15000, -15000},
		{"a credit line of pieces priced per MBF", "-187.5", "187.5", "1", 5000000, -50000},
		{"half a cent rounds away from zero", "1", "1", "1", 50, 1},
		{"minus half a cent rounds away from zero", "1", "1", "1", -50, -1},
		{"exact when the product is whole cents", "4", "1", "1", 125, 5},
		{"a hundredth of a cent rounds to zero", "1", "1", "1", 1, 0},
		{"zero quantity", "0", "1", "1", 13725, 0},
		{"sub cent price on a big quantity", "20000", "1", "1", 1, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Extend(mustQ(tc.qty), mustQ(tc.uomQty), mustQ(tc.priceUomQty), tc.price)
			if err != nil {
				t.Fatalf("Extend: %v", err)
			}
			if got != tc.want {
				t.Errorf("Extend(%s, %s, %s, %d) = %d cents, want %d",
					tc.qty, tc.uomQty, tc.priceUomQty, tc.price, got, tc.want)
			}
		})
	}
}

// RULE (ADR 0001 §7a): both sides of a conversion pair are positive. A zero
// side is not a conversion, and a negative side would mint a credit through
// the conversion instead of the line's own kind; both are refused rather
// than dividing by a sign or pricing the line at nothing.
func TestExtendRefusesZeroPair(t *testing.T) {
	one := Quantity(10000)
	for _, bad := range []Quantity{0, -1, -one} {
		if _, err := Extend(one, bad, one, Price(15000)); err == nil {
			t.Errorf("sale units %d extended, want a refusal", bad)
		}
		if _, err := Extend(one, one, bad, Price(15000)); err == nil {
			t.Errorf("price units %d extended, want a refusal", bad)
		}
	}
}

// RULE: a product past the int64 cent range is refused, not wrapped.
func TestExtendOverflow(t *testing.T) {
	big := Quantity(9_000_000_000_000_000)
	if _, err := Extend(big, big, big, Price(9_000_000_000_000_000)); err == nil {
		t.Error("overflowing product extended, want a refusal")
	}
}

func TestExtendDiscounted(t *testing.T) {
	mustQ := func(s string) Quantity {
		q, err := ParseQuantity(s)
		if err != nil {
			t.Fatalf("ParseQuantity(%q): %v", s, err)
		}
		return q
	}
	cases := []struct {
		name        string
		qty         string
		uomQty      string
		priceUomQty string
		price       Price
		percent     string
		want        Cents
	}{
		{"ten at 1.50 less 10 percent", "10", "1", "1", 15000, "10", 1350},
		{"a whole MBF at 500.00 less 25 percent", "187.5", "187.5", "1", 5000000, "25", 37500},
		{"no discount is the plain extension", "10", "1", "1", 15000, "0", 1500},
		{"a full 100 percent discounts to zero", "10", "1", "1", 15000, "100", 0},
		{"a quarter percent on an odd product rounds once, to the exact product", "1", "1", "1", 19999, "0.25", 199},
		{"one piece at 500.00 per MBF less 10 percent", "1", "187.5", "1", 5000000, "10", 240},
		{"a credit line discounted", "-187.5", "187.5", "1", 5000000, "10", -45000},
		{"a fraction percent at scale 4", "10", "1", "1", 15000, "12.3456", 1315},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtendDiscounted(mustQ(tc.qty), mustQ(tc.uomQty), mustQ(tc.priceUomQty), tc.price, mustQ(tc.percent))
			if err != nil {
				t.Fatalf("ExtendDiscounted: %v", err)
			}
			if got != tc.want {
				t.Errorf("ExtendDiscounted(%s, %s, %s, %d, %s) = %d cents, want %d",
					tc.qty, tc.uomQty, tc.priceUomQty, tc.price, tc.percent, got, tc.want)
			}
		})
	}
}

func TestExtendDiscountedRefusesZeroPair(t *testing.T) {
	one := Quantity(10000)
	for _, bad := range []Quantity{0, -1, -one} {
		if _, err := ExtendDiscounted(one, bad, one, Price(15000), one); err == nil {
			t.Errorf("sale units %d extended, want a refusal", bad)
		}
		if _, err := ExtendDiscounted(one, one, bad, Price(15000), one); err == nil {
			t.Errorf("price units %d extended, want a refusal", bad)
		}
	}
}

// The percent side multiplies the exact product before the one rounding: a
// line whose plain extension is exactly half a cent still rounds away from
// zero after the discount factor, and a discount never produces a different
// rounding mode than Extend.
func TestExtendDiscountedMatchesExtendScaled(t *testing.T) {
	mustQ := func(s string) Quantity {
		q, err := ParseQuantity(s)
		if err != nil {
			t.Fatalf("ParseQuantity(%q): %v", s, err)
		}
		return q
	}
	// 3 at 0.3333 is 99.99 cents plain; less 50 percent the exact product is
	// 49.995, which rounds once to 50.
	got, err := ExtendDiscounted(mustQ("3"), mustQ("1"), mustQ("1"), 3333, mustQ("50"))
	if err != nil {
		t.Fatal(err)
	}
	if got != 50 {
		t.Errorf("ExtendDiscounted(3, 1, 1, 3333, 50) = %d, want 50", got)
	}
}
