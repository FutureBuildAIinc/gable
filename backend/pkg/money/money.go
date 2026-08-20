// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package money holds the conversions between the float64 dollar amounts that
// arrive from JSON payloads and DECIMAL database columns and the int64 cents
// the ledger is denominated in.
//
// The idiom this package replaces, `int64(x*100.0 + 0.5)`, is wrong for
// negative amounts: Go truncates float→int toward zero, so the +0.5 rounds a
// negative value the wrong way and -$100.00 becomes -9999 cents instead of
// -10000. That single cent is enough to unbalance a balance sheet (once per
// contra/unnatural-balance account) or to open an overdrawn bank
// reconciliation short. Every conversion goes through math.Round here, which
// rounds half away from zero in both directions.
package money

import "math"

// DollarsToCents converts a dollar amount to whole cents, rounding half away
// from zero: 8.125 -> 813, -8.125 -> -813. It is the only correct way to turn
// a float64 dollar amount into ledger cents.
func DollarsToCents(dollars float64) int64 {
	return int64(math.Round(dollars * 100.0))
}

// RoundToCents rounds an already-cent-denominated float — a line extension, a
// tax amount, a proration — to whole cents, half away from zero. Use it when
// the multiplication happened in cents rather than dollars, so the value is
// not scaled a second time.
func RoundToCents(cents float64) int64 {
	return int64(math.Round(cents))
}

// CentsToDollars converts ledger cents back to a dollar amount for payloads
// and DECIMAL columns.
func CentsToDollars(cents int64) float64 {
	return float64(cents) / 100.0
}
