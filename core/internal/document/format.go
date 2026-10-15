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
	// padDay writes the day with two digits ("01 Sept 2026"), as en-ZA does.
	padDay bool
}

var (
	monthsShort = [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	// the day first English locales spell September "Sept"
	monthsSept = [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sept", "Oct", "Nov", "Dec"}
	// en-AU also writes June and July in full
	monthsAU = [12]string{"Jan", "Feb", "Mar", "Apr", "May", "June", "July", "Aug", "Sept", "Oct", "Nov", "Dec"}
)

// layoutDay holds the parts of a day in the order a locale reads them.
const (
	layoutMonthFirst = "M D, Y"
	layoutDayFirst   = "D M Y"
)

var dayLayouts = map[string]style{
	"en-US": {layoutMonthFirst, &monthsShort, false},
	"en-CA": {layoutMonthFirst, &monthsShort, false},
	"en-GB": {layoutDayFirst, &monthsSept, false},
	"en-NZ": {layoutDayFirst, &monthsSept, false},
	"en-IE": {layoutDayFirst, &monthsSept, false},
	"en-IN": {layoutDayFirst, &monthsSept, false},
	"en-AU": {layoutDayFirst, &monthsAU, false},
	"en-ZA": {layoutDayFirst, &monthsSept, true},
}

// newStyle returns the style for a BCP 47 tag; an empty, unknown or
// unsupported tag reads as en-US.
func newStyle(tag string) style {
	if s, ok := dayLayouts[canonicalTag(tag)]; ok {
		return s
	}
	return dayLayouts["en-US"]
}

// canonicalTag writes "en_gb" and "EN-gb" as "en-GB": the language and the
// region, which is the second subtag when it is two letters. A three digit
// region ("en-150") is not in the table and falls back to en-US words, as the
// desk's Intl gives for the tags this table lacks.
// Variants and extensions after the region ("en-GB-oxendict") do not hide it;
// a script in its place ("en-Latn-GB") is no region, and reads as en-US words
// as the desk's Intl does.
func canonicalTag(tag string) string {
	parts := strings.FieldsFunc(tag, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) < 2 {
		return ""
	}
	region := parts[1]
	if len(region) != 2 {
		return ""
	}
	return strings.ToLower(parts[0]) + "-" + strings.ToUpper(region)
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
	if s.padDay && d < 10 {
		day = "0" + day
	}
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

// MoneyFloat is Money for a float amount (a purchase order's cost), rounded to
// the cent exactly as "%.2f" rounds it; only the digit groups are new.
func (style) MoneyFloat(f float64) string {
	d := strconv.FormatFloat(f, 'f', 2, 64)
	if d == "-0.00" {
		d = "0.00"
	}
	return groupAmount(d)
}

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
