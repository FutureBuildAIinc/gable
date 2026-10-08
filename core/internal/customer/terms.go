// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import "time"

// DueDate is when an invoice dated invoiceDate falls due under these terms.
// Only the calendar date of invoiceDate counts; the result is that date at
// midnight UTC.
//
//   - NET_DAYS: invoiceDate plus net_days.
//   - DAY_OF_MONTH: that day of the month after the invoice month, clamped to
//     the month's length (day 31 in February is the last day of February).
//   - DUE_ON_RECEIPT: invoiceDate itself.
func (t *PaymentTerms) DueDate(invoiceDate time.Time) time.Time {
	d := dateOf(invoiceDate)
	switch t.Kind {
	case TermsNetDays:
		n := 0
		if t.NetDays != nil {
			n = *t.NetDays
		}
		return d.AddDate(0, 0, n)
	case TermsDayOfMonth:
		day := 1
		if t.DayOfMonth != nil {
			day = *t.DayOfMonth
		}
		firstOfNext := time.Date(d.Year(), d.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		last := time.Date(firstOfNext.Year(), firstOfNext.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		if day > last {
			day = last
		}
		return time.Date(firstOfNext.Year(), firstOfNext.Month(), day, 0, 0, 0, 0, time.UTC)
	default:
		return d
	}
}

// DiscountDueDate is the last day the early payment discount applies to an
// invoice dated invoiceDate, or nil when the terms carry no discount.
func (t *PaymentTerms) DiscountDueDate(invoiceDate time.Time) *time.Time {
	if t.DiscountPercent == nil || t.DiscountDays == nil {
		return nil
	}
	d := dateOf(invoiceDate).AddDate(0, 0, *t.DiscountDays)
	return &d
}

func dateOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
