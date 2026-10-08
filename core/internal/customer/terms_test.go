// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import (
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func ptrInt(n int) *int { return &n }

// RULE (ADR 0005 7.2): a DAY_OF_MONTH term is due on that day of the month
// after the invoice month, clamped to the month's length, so day 31 across a
// short month is the last day of it, in a leap year and out of one.
func TestDueDate_DayOfMonthAcrossAShortMonth(t *testing.T) {
	terms := &PaymentTerms{Kind: TermsDayOfMonth, DayOfMonth: ptrInt(31)}
	for _, c := range []struct {
		name    string
		invoice time.Time
		want    time.Time
	}{
		{"January into a common February", day(2027, time.January, 15), day(2027, time.February, 28)},
		{"January into a leap February", day(2028, time.January, 15), day(2028, time.February, 29)},
		{"the last day of January", day(2027, time.January, 31), day(2027, time.February, 28)},
		{"March into April (30 days)", day(2027, time.March, 3), day(2027, time.April, 30)},
		{"April into May (31 days)", day(2027, time.April, 30), day(2027, time.May, 31)},
		{"December into January of the next year", day(2027, time.December, 10), day(2028, time.January, 31)},
	} {
		if got := terms.DueDate(c.invoice); !got.Equal(c.want) {
			t.Errorf("%s: due %s, want %s", c.name, got.Format("2006-01-02"), c.want.Format("2006-01-02"))
		}
	}

	fifth := &PaymentTerms{Kind: TermsDayOfMonth, DayOfMonth: ptrInt(5)}
	if got, want := fifth.DueDate(day(2027, time.December, 31)), day(2028, time.January, 5); !got.Equal(want) {
		t.Errorf("day 5 from December 31: due %s, want %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	// Only the calendar date of the invoice counts, not the time of day or the zone's offset.
	late := time.Date(2027, time.January, 31, 23, 59, 0, 0, time.UTC)
	if got := terms.DueDate(late); !got.Equal(day(2027, time.February, 28)) {
		t.Errorf("a late evening invoice: due %s", got.Format("2006-01-02"))
	}
}

func TestDueDate_NetDaysAndOnReceipt(t *testing.T) {
	net30 := &PaymentTerms{Kind: TermsNetDays, NetDays: ptrInt(30)}
	if got, want := net30.DueDate(day(2027, time.January, 31)), day(2027, time.March, 2); !got.Equal(want) {
		t.Errorf("net 30 from January 31: due %s, want %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	if got, want := net30.DueDate(day(2028, time.January, 31)), day(2028, time.March, 1); !got.Equal(want) {
		t.Errorf("net 30 from January 31 in a leap year: due %s, want %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	net0 := &PaymentTerms{Kind: TermsNetDays, NetDays: ptrInt(0)}
	if got := net0.DueDate(day(2027, time.June, 1)); !got.Equal(day(2027, time.June, 1)) {
		t.Errorf("net 0 is due the invoice day, got %s", got.Format("2006-01-02"))
	}
	receipt := &PaymentTerms{Kind: TermsDueOnReceipt}
	if got := receipt.DueDate(day(2027, time.June, 9)); !got.Equal(day(2027, time.June, 9)) {
		t.Errorf("due on receipt is due the invoice day, got %s", got.Format("2006-01-02"))
	}
}

// The early payment discount runs from the invoice date and exists only when
// the terms carry both a percent and days.
func TestDiscountDueDate(t *testing.T) {
	pct := httpx.Quantity(20000)
	terms := &PaymentTerms{Kind: TermsNetDays, NetDays: ptrInt(30), DiscountPercent: &pct, DiscountDays: ptrInt(10)}
	got := terms.DiscountDueDate(day(2027, time.January, 25))
	if got == nil || !got.Equal(day(2027, time.February, 4)) {
		t.Errorf("discount due %v, want 2027-02-04", got)
	}
	if none := (&PaymentTerms{Kind: TermsNetDays, NetDays: ptrInt(30)}).DiscountDueDate(day(2027, time.January, 25)); none != nil {
		t.Errorf("terms without a discount gave %v, want none", none)
	}
}
