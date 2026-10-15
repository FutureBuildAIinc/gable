// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package collections holds the one definition of a yard's collections for a
// business day, read by the dashboard's Today's Revenue card, yesterday's
// comparison, the revenue trend and the Daily Till so the four always agree:
// the day's NET collections at the yard are the payments received that day
// (the payment's own branch_id, its created_at instant inside the branch's
// day, never a voided payment) less the refunds paid out that day (by their
// refunded_on date, never a refund of a voided payment).
package collections

import (
	"fmt"

	"github.com/gablelbm/gable/pkg/middleware"
)

// DefaultBranchZoneSQL is the default branch's IANA zone (UTC when it has
// none): the zone of the business day, the aging's default.
const DefaultBranchZoneSQL = `COALESCE((SELECT l.timezone FROM locations l
	WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')), 'UTC')`

// TodaySQL is the branch's business date now.
const TodaySQL = `(NOW() AT TIME ZONE ` + DefaultBranchZoneSQL + `)::date`

// ReceivedFrom is the FROM and WHERE of the payments received on the business
// day day (a SQL date expression) at the caller's reach: payments p, POSTED,
// created inside the day's half open range of instants (so the index on
// created_at serves it and a 23 or 25 hour day is whole), walled by the
// payment's own yard. branch and grants are the parameter positions.
func ReceivedFrom(day string, branch, grants int) string {
	return fmt.Sprintf(`payments p
		WHERE p.status = 'POSTED'
		  AND p.created_at >= ((%[1]s)::date::timestamp AT TIME ZONE %[2]s)
		  AND p.created_at < (((%[1]s)::date + 1)::timestamp AT TIME ZONE %[2]s)
		  AND %[3]s`, day, DefaultBranchZoneSQL, middleware.Wall("p", branch, grants))
}

// RefundedFrom is the FROM and WHERE of the refunds paid out on the business
// day day: payment_refunds r, COMPLETE, refunded that date, of a payment that
// is not voided (a voided payment never counts, nor what was refunded out of
// it) or of a credit memo, walled by the yard of the payment or the memo.
func RefundedFrom(day string, branch, grants int) string {
	return fmt.Sprintf(`payment_refunds r
		LEFT JOIN payments rp ON rp.id = r.payment_id
		LEFT JOIN credit_memos rm ON rm.id = r.credit_memo_id
		WHERE r.status = 'COMPLETE'
		  AND r.refunded_on = (%[1]s)::date
		  AND (rp.id IS NULL OR rp.status = 'POSTED')
		  AND (%[2]s OR %[3]s)`, day, middleware.Wall("rp", branch, grants), middleware.Wall("rm", branch, grants))
}

// NetCentsAt is a scalar subquery: the day's net collections in cents.
func NetCentsAt(day string, branch, grants int) string {
	return `(
		(SELECT COALESCE(SUM(ROUND(p.amount * 100)::bigint), 0) FROM ` + ReceivedFrom(day, branch, grants) + `)
		- (SELECT COALESCE(SUM(ROUND(r.amount * 100)::bigint), 0) FROM ` + RefundedFrom(day, branch, grants) + `)
	)`
}
