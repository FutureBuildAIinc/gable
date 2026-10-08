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

// QuantityScale is the fixed scale of quantities and unit conversion
// factors on the wire: four fraction digits, the scale of the database's
// quantity columns and of the price (ADR 0001 §7a records why the wire
// type is fixed now).
const QuantityScale = 4

// Quantity is a quantity or a unit conversion factor on the wire: a JSON
// string holding a plain decimal with at most QuantityScale fraction digits
// ("12.5", "1000", "0.001"), carried internally as the scale 4 integer.
// A JSON number never decodes into it, so no client can send a float where
// a decimal string is the contract.
type Quantity int64

// ParseQuantity parses a plain decimal string, from the wire or from a
// NUMERIC(12,4) column, into a Quantity exactly. Trailing zeros beyond
// scale 4 are padding; a nonzero fifth digit is precision this type refuses
// rather than rounds.
func ParseQuantity(s string) (Quantity, error) {
	v, err := parseFixed(s, QuantityScale)
	if err != nil {
		return 0, err
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

// extendScaleDivisor is the power of ten between the product of two scale
// 4 values and cents: quantity ten-thousandths times factor ten-thousandths
// times price ten-thousandths lands at scale 12 of the major unit, and a
// cent is scale 2, so the divisor is 10^10.
const extendScaleDivisor = 10_000_000_000

// Extend prices one line (ADR 0001 §7a): the quantity, converted from its
// sale unit into the price unit by factor (factor 1 when the units agree),
// multiplied by the unit price, rounded once, to cents, half away from
// zero. The product is exact in big arithmetic until that one rounding;
// every module prices lines through here and nowhere else.
func Extend(qty, factor Quantity, price Price) (Cents, error) {
	n := new(big.Int).Mul(big.NewInt(int64(qty)), big.NewInt(int64(factor)))
	n.Mul(n, big.NewInt(int64(price)))
	neg := n.Sign() < 0

	var mag big.Int
	mag.Abs(n)
	// Half away from zero: add half the divisor to the magnitude before
	// the one division, which lands .5 (and only .5 or more) on the
	// farther side of zero.
	mag.Add(&mag, big.NewInt(extendScaleDivisor/2))
	mag.Div(&mag, big.NewInt(extendScaleDivisor))
	if !mag.IsInt64() {
		return 0, errOverflow
	}
	cents := mag.Int64()
	if neg {
		cents = -cents
	}
	return Cents(cents), nil
}
