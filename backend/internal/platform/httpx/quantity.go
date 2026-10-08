// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"bytes"
	"encoding/json"
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
// with at most four fraction digits. A JSON number, an empty string, an
// exponent, or precision beyond the scale is an error.
func (q *Quantity) UnmarshalJSON(data []byte) error {
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
