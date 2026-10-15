// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Every printed document reads an amount and a date the way the desk does:
// digit groups and a minus before the currency sign, and the month as a short
// word in the dealer's locale (the desk's formatDay).

func TestAmountsGroupDigitsWithTheMinusBeforeTheSign(t *testing.T) {
	st := newStyle("en-US")
	cases := []struct {
		cents httpx.Cents
		want  string
	}{
		{0, "$0.00"},
		{5, "$0.05"},
		{392997, "$3,929.97"},
		{-248763, "-$2,487.63"},
		{99999, "$999.99"},
		{100000, "$1,000.00"},
		{123456789, "$1,234,567.89"},
		{-100000000, "-$1,000,000.00"},
	}
	for _, c := range cases {
		if got := st.Money(c.cents); got != c.want {
			t.Errorf("Money(%d) = %q, want %q", int64(c.cents), got, c.want)
		}
	}
}

func TestPricesKeepTheirFourDigitsAndGroupTheWhole(t *testing.T) {
	st := newStyle("en-US")
	if got, want := st.Money(httpx.Price(12345678)), "$1,234.5678"; got != want {
		t.Errorf("Money(price) = %q, want %q", got, want)
	}
}

func TestFloatAmountsGroupToo(t *testing.T) {
	st := newStyle("en-US")
	for in, want := range map[float64]string{0: "$0.00", 3.5: "$3.50", 1234567.891: "$1,234,567.89", -2487.6: "-$2,487.60"} {
		if got := st.MoneyFloat(in); got != want {
			t.Errorf("MoneyFloat(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestDatesAreWordsInTheDealersLocale(t *testing.T) {
	cases := []struct{ locale, day, want string }{
		{"en-US", "2026-06-30", "Jun 30, 2026"},
		{"en-CA", "2026-06-30", "Jun 30, 2026"},
		{"en-US", "2026-01-01", "Jan 1, 2026"},
		{"en-CA", "2026-12-31", "Dec 31, 2026"},
		{"en-US", "2026-02-28", "Feb 28, 2026"},
		{"en-US", "2026-09-05", "Sep 5, 2026"},
		{"en-GB", "2026-06-30", "30 Jun 2026"},
		{"en-GB", "2026-09-05", "5 Sept 2026"},
		{"en-NZ", "2026-01-01", "1 Jan 2026"},
		{"en-IE", "2026-12-31", "31 Dec 2026"},
		// a locale the module has no table for reads en-US in words, never numerals
		{"fr-CA", "2026-06-30", "Jun 30, 2026"},
		{"", "2026-06-30", "Jun 30, 2026"},
		{"xx-ZZ", "2026-06-30", "Jun 30, 2026"},
	}
	for _, c := range cases {
		if got := newStyle(c.locale).Day(c.day); got != c.want {
			t.Errorf("Day(%q) under %q = %q, want %q", c.day, c.locale, got, c.want)
		}
	}
}

func TestDayTakesAMidnightTimestampAndLeavesTheRestAlone(t *testing.T) {
	st := newStyle("en-US")
	if got, want := st.Day("2026-06-30T00:00:00Z"), "Jun 30, 2026"; got != want {
		t.Errorf("Day(timestamp) = %q, want %q", got, want)
	}
	if got := st.Day(""); got != "" {
		t.Errorf("Day(\"\") = %q, want empty", got)
	}
	if got := st.Day("not a day"); got != "not a day" {
		t.Errorf("Day(unreadable) = %q, want it as it came", got)
	}
}

func TestDateOfAMomentUsesItsOwnDay(t *testing.T) {
	st := newStyle("en-CA")
	if got, want := st.Date(time.Date(2026, 6, 30, 23, 59, 0, 0, time.UTC)), "Jun 30, 2026"; got != want {
		t.Errorf("Date = %q, want %q", got, want)
	}
}
