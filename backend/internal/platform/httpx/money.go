// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"bytes"
	"encoding/json"
	"math"
)

// PriceScale is the fixed scale of the wire unit price: four decimal
// places, the scale the database already keeps wherever sub cent precision
// exists, its NUMERIC(12,4) unit price columns (ADR 0001 §7 records why
// scale 4 and not something finer). A module exposing a
// _ten_thousandths field against a price column still at scale 2 widens
// the column in the same change, so no wire value is rounded on store.
const PriceScale = 4

// Cents is a monetary amount in minor units: int64 on the wire, field name
// carrying the suffix _cents. No float ever carries money: a JSON number
// with a fraction does not decode into it.
type Cents int64

// UnmarshalJSON keeps the value type required: JSON null is an error, not a
// silent zero, because a missing amount and a zero amount are different
// facts (ADR 0001 §7). A field the contract documents optional uses the
// *Cents pointer type, where null decodes to nil.
func (c *Cents) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		return errNullIsNotZero
	}
	return json.Unmarshal(data, (*int64)(c))
}

// Price is a unit price at fixed scale 4, in ten-thousandths of the major
// unit: int64 on the wire, field name carrying the suffix
// _ten_thousandths. Price(13725) is a unit price of 1.3725.
type Price int64

// UnmarshalJSON keeps the value type required, like Cents: null is an
// error, not a silent zero; an optional price field is the *Price pointer.
func (p *Price) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		return errNullIsNotZero
	}
	return json.Unmarshal(data, (*int64)(p))
}

// DecimalString renders the cents amount as the fixed two-digit decimal a
// DECIMAL column takes: Cents(1234) is "12.34". Exact; never float.
func (c Cents) DecimalString() string {
	return formatFixed(int64(c), 2)
}

// DecimalString renders the price as the fixed four-digit decimal a
// NUMERIC(12,4) column takes: Price(13725) is "1.3725". Exact; never float.
func (p Price) DecimalString() string {
	return formatFixed(int64(p), PriceScale)
}

// formatFixed renders value at a fixed scale without going near a float:
// sign, integer digits, then exactly scale fraction digits. The magnitude
// walks as uint64 because -math.MinInt64 overflows int64.
func formatFixed(value int64, scale int) string {
	neg := value < 0
	u := uint64(value)
	if neg {
		u = -u
	}
	digits := []byte{}
	if u == 0 {
		digits = append(digits, '0')
	}
	for v := u; v > 0; v /= 10 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
	}
	for len(digits) <= scale {
		digits = append([]byte{'0'}, digits...)
	}
	cut := len(digits) - scale
	out := make([]byte, 0, len(digits)+2)
	if neg {
		out = append(out, '-')
	}
	out = append(out, digits[:cut]...)
	out = append(out, '.')
	out = append(out, digits[cut:]...)
	return string(out)
}

// parseFixed parses a database decimal string into a scaled int64 without
// going near a float64. Accepted: an optional minus, one or more integer
// digits, and an optional fraction of up to any length whose digits beyond
// the scale are all zero (Postgres pads DECIMAL columns to their scale, so
// "12.3400" is a cents value of 1234, not a precision question). A nonzero
// digit beyond the scale is an error, not a rounding: a money column
// holding sub-cent (or sub-ten-thousandth) precision is a data bug that
// must surface. Everything else (exponents, separators, NaN, whitespace,
// bare punctuation) is an error.
func parseFixed(s string, scale int) (int64, error) {
	rest := s
	neg := false
	if len(rest) > 0 && rest[0] == '-' {
		neg = true
		rest = rest[1:]
	}

	intPart, fracPart := rest, ""
	hadDot := false
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' {
			intPart, fracPart = rest[:i], rest[i+1:]
			hadDot = true
			break
		}
	}
	if len(intPart) == 0 || (hadDot && len(fracPart) == 0) {
		return 0, errNotADecimal
	}

	var intValue uint64
	for i := 0; i < len(intPart); i++ {
		d := intPart[i]
		if d < '0' || d > '9' {
			return 0, errNotADecimal
		}
		if intValue > (math.MaxUint64-uint64(d-'0'))/10 {
			return 0, errOverflow
		}
		intValue = intValue*10 + uint64(d-'0')
	}

	var fracValue uint64
	seen := 0
	for i := 0; i < len(fracPart); i++ {
		d := fracPart[i]
		if d < '0' || d > '9' {
			return 0, errNotADecimal
		}
		if i < scale {
			fracValue = fracValue*10 + uint64(d-'0')
			seen = i + 1
		} else if d != '0' {
			// A nonzero digit past the scale is precision this type cannot
			// keep. Refuse it; rounding silently changes money.
			return 0, errBeyondScale
		}
	}
	// A short fraction ("12.3") pads with zeros to the fixed scale.
	for ; seen < scale; seen++ {
		fracValue *= 10
	}

	mult := uint64(1)
	for i := 0; i < scale; i++ {
		mult *= 10
	}
	if intValue > (math.MaxUint64-fracValue)/mult {
		return 0, errOverflow
	}
	magnitude := intValue*mult + fracValue
	if neg {
		// int64's negative range reaches one further than its positive
		// range: -2^63 parses, +2^63 does not.
		if magnitude > 1<<63 {
			return 0, errOverflow
		}
		if magnitude == 1<<63 {
			return math.MinInt64, nil
		}
		return -int64(magnitude), nil
	}
	if magnitude > math.MaxInt64 {
		return 0, errOverflow
	}
	return int64(magnitude), nil
}

type parseError string

func (e parseError) Error() string { return string(e) }

const (
	errNotADecimal    parseError = "not a plain decimal number"
	errBeyondScale    parseError = "carries precision beyond the fixed scale"
	errNotCanonical   parseError = "not the canonical decimal form (no leading zeros, no negative zero)"
	errQuantityBound  parseError = "beyond what a NUMERIC(12,4) column holds"
	errOverflow       parseError = "beyond the range of a 64-bit integer at this scale"
	errZeroConversion parseError = "the conversion pair has a zero side"
	errNullIsNotZero  parseError = "the field is required and null is not zero; a field documented optional is the pointer type"
)

// ParseCents parses a database decimal string into Cents exactly. The
// fraction may carry trailing zeros beyond two digits ("12.3400"); a
// nonzero third digit ("12.345") is an error.
func ParseCents(s string) (Cents, error) {
	v, err := parseFixed(s, 2)
	if err != nil {
		return 0, err
	}
	return Cents(v), nil
}

// ParsePrice parses a database decimal string into a scale-4 Price exactly.
// The fraction may carry trailing zeros beyond four digits ("1.37250"); a
// nonzero fifth digit ("1.37256") is an error.
func ParsePrice(s string) (Price, error) {
	v, err := parseFixed(s, PriceScale)
	if err != nil {
		return 0, err
	}
	return Price(v), nil
}
