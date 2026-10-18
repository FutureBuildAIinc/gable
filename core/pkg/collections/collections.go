// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package collections holds the one definition of a yard's collections over
// business days, read by the dashboard's "Collected today" card, yesterday's
// comparison, the collected trend, the Daily Till and its Sales Summary so
// they always agree: NET collections are the payments received (by the
// payment's own business date, received_on, the AR core's date for it, and
// its own branch_id; never a voided payment) less the refunds paid out (by
// their refunded_on date; never a refund of a voided payment). A payment is
// dated by what it names, not by the instant its row was written: the demo
// import stamps one created_at on its whole history.
package collections

import (
	"fmt"

	"github.com/gablelbm/gable/pkg/middleware"
)

// DefaultBranchZoneSQL is the default branch's IANA zone (UTC when it has
// none): the zone of today's business date, the aging's default.
const DefaultBranchZoneSQL = `COALESCE((SELECT l.timezone FROM locations l
	WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')), 'UTC')`

// TodaySQL is the branch's business date now.
const TodaySQL = `(NOW() AT TIME ZONE ` + DefaultBranchZoneSQL + `)::date`

// ReceivedBetween is the FROM and WHERE of the payments received from the
// business date from to the business date to inclusive (SQL date expressions),
// at the caller's reach: payments p, POSTED, walled by the payment's own yard.
// branch and grants are the parameter positions.
func ReceivedBetween(from, to string, branch, grants int) string {
	return fmt.Sprintf(`payments p
		WHERE p.status = 'POSTED'
		  AND p.received_on BETWEEN (%s)::date AND (%s)::date
		  AND %s`, from, to, middleware.Wall("p", branch, grants))
}

// RefundedBetween is the FROM and WHERE of the refunds paid out from the
// business date from to the business date to inclusive: payment_refunds r,
// COMPLETE, of a payment that is not voided or of a credit memo, walled by the
// yard of the payment or the memo (a refund counts at the original payment's
// yard).
func RefundedBetween(from, to string, branch, grants int) string {
	return fmt.Sprintf(`payment_refunds r
		LEFT JOIN payments rp ON rp.id = r.payment_id
		LEFT JOIN credit_memos rm ON rm.id = r.credit_memo_id
		WHERE r.status = 'COMPLETE'
		  AND r.refunded_on BETWEEN (%s)::date AND (%s)::date
		  AND (rp.id IS NULL OR rp.status = 'POSTED')
		  AND (%s OR %s)`, from, to, middleware.Wall("rp", branch, grants), middleware.Wall("rm", branch, grants))
}

// ReceivedFrom is ReceivedBetween for the one business date day.
func ReceivedFrom(day string, branch, grants int) string {
	return ReceivedBetween(day, day, branch, grants)
}

// RefundedFrom is RefundedBetween for the one business date day.
func RefundedFrom(day string, branch, grants int) string {
	return RefundedBetween(day, day, branch, grants)
}

// NetCentsBetween is a scalar subquery: the net collections in cents from the
// business date from to the business date to inclusive.
func NetCentsBetween(from, to string, branch, grants int) string {
	return `(
		(SELECT COALESCE(SUM(ROUND(p.amount * 100)::bigint), 0) FROM ` + ReceivedBetween(from, to, branch, grants) + `)
		- (SELECT COALESCE(SUM(ROUND(r.amount * 100)::bigint), 0) FROM ` + RefundedBetween(from, to, branch, grants) + `)
	)`
}

// NetCentsAt is NetCentsBetween for the one business date day.
func NetCentsAt(day string, branch, grants int) string {
	return NetCentsBetween(day, day, branch, grants)
}

// NetByDay is a query of (date, cents): the net collections of each business
// date from from to to inclusive that has any, oldest first.
func NetByDay(from, to string, branch, grants int) string {
	return `
		WITH received AS (
			SELECT p.received_on AS day, SUM(ROUND(p.amount * 100)::bigint) AS cents
			FROM ` + ReceivedBetween(from, to, branch, grants) + `
			GROUP BY 1
		),
		refunded AS (
			SELECT r.refunded_on AS day, SUM(ROUND(r.amount * 100)::bigint) AS cents
			FROM ` + RefundedBetween(from, to, branch, grants) + `
			GROUP BY 1
		)
		SELECT COALESCE(rc.day, rf.day) AS date, (COALESCE(rc.cents, 0) - COALESCE(rf.cents, 0))::bigint AS cents
		FROM received rc FULL JOIN refunded rf ON rf.day = rc.day
		ORDER BY 1`
}

// NetCentsForBranchAt is a scalar subquery: the net collections in cents of
// one branch on the business date day, for a read that names its branch (the
// branch day summary) rather than reading the caller's reach. The branch is
// the parameter position branch. The definition is ReceivedBetween's and
// RefundedBetween's: payments by their received_on and own yard, never a
// voided one; refunds by their refunded_on, at the yard of the payment or the
// credit memo they refund. The caller holds the branch to its wall first.
func NetCentsForBranchAt(day string, branch int) string {
	return fmt.Sprintf(`(
		(SELECT COALESCE(SUM(ROUND(p.amount * 100)::bigint), 0) FROM payments p
			WHERE p.status = 'POSTED' AND p.received_on = (%[1]s)::date AND p.branch_id = $%[2]d)
		- (SELECT COALESCE(SUM(ROUND(r.amount * 100)::bigint), 0) FROM payment_refunds r
			LEFT JOIN payments rp ON rp.id = r.payment_id
			LEFT JOIN credit_memos rm ON rm.id = r.credit_memo_id
			WHERE r.status = 'COMPLETE' AND r.refunded_on = (%[1]s)::date
			  AND (rp.id IS NULL OR rp.status = 'POSTED')
			  AND (rp.branch_id = $%[2]d OR rm.branch_id = $%[2]d))
	)`, day, branch)
}
