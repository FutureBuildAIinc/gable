// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"strconv"
	"strings"
	"time"
)

// style is the one way every printed document writes an amount and a date,
// matching what the desk shows (web/apps/desk/src/lib/dates.ts, formatDay):
// amounts with digit groups and the minus before the currency sign, dates with
// the month as a short word in the dealer's locale, never a numeric month
// first or day first form. The locale is the dealer profile's.
//
// The desk asks the browser's Intl for any tag. Go has no such table, so the
// locales listed in dayLayouts are matched to what the desk gives, and any
// other tag reads as en-US in words, the same fallback the desk uses when a
// tag is missing.
type style struct {
	layout string
	months *[12]string
}

var (
	monthsShort = [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	// the day first English locales spell September "Sept"
	monthsSept = [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sept", "Oct", "Nov", "Dec"}
)

// layoutDay holds the parts of a day in the order a locale reads them.
const (
	layoutMonthFirst = "M D, Y"
	layoutDayFirst   = "D M Y"
)

var dayLayouts = map[string]style{
	"en-US": {layoutMonthFirst, &monthsShort},
	"en-CA": {layoutMonthFirst, &monthsShort},
	"en-GB": {layoutDayFirst, &monthsSept},
	"en-NZ": {layoutDayFirst, &monthsSept},
	"en-IE": {layoutDayFirst, &monthsSept},
}

// newStyle returns the style for a BCP 47 tag; an empty, unknown or
// unsupported tag reads as en-US.
func newStyle(tag string) style {
	if s, ok := dayLayouts[canonicalTag(tag)]; ok {
		return s
	}
	return dayLayouts["en-US"]
}

// canonicalTag writes "en_gb" and "EN-gb" as "en-GB"; a tag with a script or
// further subtags is cut to its language and region.
func canonicalTag(tag string) string {
	parts := strings.FieldsFunc(tag, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) < 2 {
		return ""
	}
	return strings.ToLower(parts[0]) + "-" + strings.ToUpper(parts[len(parts)-1])
}

// Day writes a business day (YYYY-MM-DD, or a midnight UTC timestamp of one)
// as words. An empty value stays empty and one that does not read is returned
// as it came.
func (s style) Day(day string) string {
	if len(day) >= 10 && (len(day) == 10 || day[10] == 'T') {
		if t, err := time.Parse("2006-01-02", day[:10]); err == nil && (len(day) == 10 || strings.HasPrefix(day[10:], "T00:00:00")) {
			return s.Date(t)
		}
	}
	if day == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, day); err == nil {
		return s.Date(t)
	}
	return day
}

// Date writes a moment's own calendar day (in its own location) as words.
func (s style) Date(t time.Time) string {
	y, m, d := t.Date()
	month := s.months[m-1]
	day := strconv.Itoa(d)
	year := strconv.Itoa(y)
	if s.layout == layoutDayFirst {
		return day + " " + month + " " + year
	}
	return month + " " + day + ", " + year
}

// decimalStringer is every money type that prints as a fixed decimal.
type decimalStringer interface{ DecimalString() string }

// Money writes an amount with digit groups and the minus before the sign:
// "$3,929.97", "-$2,487.63".
func (style) Money(c decimalStringer) string { return groupAmount(c.DecimalString()) }

// MoneyFloat is Money for a float amount (a purchase order's cost), rounded
// to the cent.
func (style) MoneyFloat(f float64) string { return groupAmount(formatCents(f)) }

// groupAmount takes a plain decimal ("-3929.97") and returns "-$3,929.97".
func groupAmount(d string) string {
	sign := ""
	if strings.HasPrefix(d, "-") {
		sign, d = "-", d[1:]
	}
	whole, frac := d, ""
	if i := strings.IndexByte(d, '.'); i >= 0 {
		whole, frac = d[:i], d[i:]
	}
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + "$" + b.String() + frac
}

// formatCents rounds a float to two decimals as text. A rounded zero is "0.00"
// with no sign.
func formatCents(f float64) string {
	neg := f < 0
	if neg {
		f = -f
	}
	n := int64(f*100 + 0.5)
	s := strconv.FormatInt(n/100, 10) + "." + string([]byte{byte('0' + n%100/10), byte('0' + n%10)})
	if neg && n != 0 {
		return "-" + s
	}
	return s
}
