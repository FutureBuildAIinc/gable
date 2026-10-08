// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
)

// QuantityScale is the fixed scale of quantities and of both sides of a
// conversion pair on the wire: four fraction digits, the scale of the
// database's quantity columns and of the price (ADR 0001 §7a records why the
// wire type is fixed now).
const QuantityScale = 4

// Quantity is a quantity or one side of a conversion pair on the wire: a
// JSON string holding a plain decimal with at most QuantityScale fraction
// digits ("12.5", "1000", "0.1875"), carried internally as the scale 4
// integer. A JSON number never decodes into it, so no client can send a
// float where a decimal string is the contract.
type Quantity int64

// QuantityMax is the largest magnitude a NUMERIC(12,4) column holds,
// 99999999.9999: the quantity columns' own bound, enforced at the parse
// boundary instead of surfacing as a database fault on store.
const QuantityMax Quantity = 999_999_999_999

// ParseQuantity parses a plain decimal string, from the wire or from a
// NUMERIC(12,4) column, into a Quantity exactly. Trailing zeros beyond
// scale 4 are padding; a nonzero fifth digit is precision this type refuses
// rather than rounds. One canonical spelling is enforced, the same posture
// as limit: no leading zeros ("0012" is refused, "0.5" is the form below
// one) and no negative zero ("-0"), because two spellings of one value on
// the wire is exactly the normalizer's job this contract removes. A
// magnitude past QuantityMax is refused here, a 400 through the validator,
// never a 500 from the column on store.
func ParseQuantity(s string) (Quantity, error) {
	v, err := parseFixed(s, QuantityScale)
	if err != nil {
		return 0, err
	}
	body := strings.TrimPrefix(s, "-")
	intPart := body
	if i := strings.IndexByte(body, '.'); i >= 0 {
		intPart = body[:i]
	}
	if len(intPart) > 1 && intPart[0] == '0' {
		return 0, errNotCanonical
	}
	if v == 0 && strings.HasPrefix(s, "-") {
		return 0, errNotCanonical
	}
	if v > int64(QuantityMax) || v < -int64(QuantityMax) {
		return 0, errQuantityBound
	}
	return Quantity(v), nil
}

// DecimalString renders the quantity as the fixed four-digit decimal a
// NUMERIC(12,4) column takes: Quantity(125000) is "12.5000". Exact; never
// float.
func (q Quantity) DecimalString() string {
	return formatFixed(int64(q), QuantityScale)
}

// WireString renders the quantity as the shortest exact decimal string:
// Quantity(125000) is "12.5", Quantity(10000000) is "1000". Trailing zero
// padding is dropped so a round trip through ParseQuantity returns the
// same string.
func (q Quantity) WireString() string {
	s := q.DecimalString()
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// MarshalJSON writes the wire form: the decimal string, quoted.
func (q Quantity) MarshalJSON() ([]byte, error) {
	return strconv.AppendQuote(nil, q.WireString()), nil
}

// UnmarshalJSON reads the wire form: a JSON string holding a plain decimal
// with at most four fraction digits. A JSON number, JSON null, an empty
// string, an exponent, or precision beyond the scale is an error; null is
// an error like it is for the money types (an optional quantity field is
// the *Quantity pointer).
func (q *Quantity) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		return errNullIsNotZero
	}
	var s string
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&s); err != nil {
		return errNotADecimal
	}
	v, err := ParseQuantity(s)
	if err != nil {
		return err
	}
	*q = v
	return nil
}

// extendScaleDivisor is the power of ten between the scaled product and
// cents: quantity ten-thousandths times price-unit ten-thousandths times
// price ten-thousandths lands at scale 12 of the major unit, dividing by
// the sale units' own scale 4 lands at scale 8, and a cent is scale 2, so
// the divisor carries 10^6.
const extendScaleDivisor = 1_000_000

// Extend prices one line (ADR 0001 §7a). The conversion between the line's
// sale unit and its price unit is the pair (uomQty, priceUomQty), the wire
// fields uom_qty and price_uom_qty: uomQty of the sale unit is the same
// goods as priceUomQty of the price unit, 187.5 and 1 for lumber sold by
// the piece and priced per MBF (187.5 PCS = 1 MBF), 1 and 1 when the units
// agree. A single scale 4 factor cannot carry such a conversion (1/187.5
// is 0.0053 at scale 4, half a percent off a whole MBF); the pair holds
// both sides exactly. The extension is quantity x unit price x priceUomQty
// / uomQty, rounded once, to cents, half away from zero, exact in big
// arithmetic until that one rounding; every module prices lines through
// here and nowhere else. Both sides of the pair are positive (ADR 0001
// §7a makes a zero or negative side a 400 validation_failed on that
// field): a zero side is no conversion, and a negative side would mint a
// credit through the conversion instead of through the line's own kind.
func Extend(qty, uomQty, priceUomQty Quantity, price Price) (Cents, error) {
	if uomQty <= 0 || priceUomQty <= 0 {
		return 0, errZeroConversion
	}
	n := new(big.Int).Mul(big.NewInt(int64(qty)), big.NewInt(int64(priceUomQty)))
	n.Mul(n, big.NewInt(int64(price)))
	div := new(big.Int).Mul(big.NewInt(int64(uomQty)), big.NewInt(extendScaleDivisor))
	neg := n.Sign() < 0

	var mag big.Int
	mag.Abs(n)
	// Half away from zero: add half the divisor to the magnitude before
	// the one division, which lands .5 (and only .5 or more) on the
	// farther side of zero.
	mag.Add(&mag, new(big.Int).Rsh(div, 1))
	mag.Div(&mag, div)
	if !mag.IsInt64() {
		return 0, errOverflow
	}
	cents := mag.Int64()
	if neg {
		cents = -cents
	}
	return Cents(cents), nil
}

// CheckLineSign enforces the sign rule of ADR 0001 §7a: a quantity is
// negative only on a return or credit line, and a unit price is never
// negative. The rule itself belongs to the converting module's validator,
// which knows the line's kind; this helper is the one place the check is
// written. path is the line's JSON path prefix ("lines[2]"), so the offences
// collect into the request's one 400 beside the line's other field errors
// (ADR 0001 §4), named lines[2].quantity and
// lines[2].unit_price_ten_thousandths.
func CheckLineSign(v *Validator, path string, qty Quantity, price Price, returnOrCredit bool) {
	v.Check(qty >= 0 || returnOrCredit, path+".quantity",
		"a negative quantity belongs to a return or credit line")
	v.Check(price >= 0, path+".unit_price_ten_thousandths",
		"a unit price is never negative")
}
