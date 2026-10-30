// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import "fmt"

// The branch's own calendar day is the day a thing is due, which is the house
// rule for money: an invoice due on the branch's day is not yet overdue, and it
// is overdue from the branch's next day on. The day is the branch's, not the
// database session's: a session in UTC and a branch in Vancouver disagree
// about the date every evening, and the desk and the portal must answer the
// same about the same invoice.
//
// The expressions below are the one place that rule is written. The desk's
// list projection and filter read OverdueExpr, and every portal reader reads
// the same expression, so the two surfaces cannot drift apart.

// branchTodayExpr returns the SQL expression for the branch's own calendar
// day: the current instant read in the branch's time zone, falling back to
// UTC for a branch with no zone. The alias is the invoices table's alias in
// the query that reads it.
func branchTodayExpr(alias string) string {
	return fmt.Sprintf(`((NOW() AT TIME ZONE COALESCE((SELECT lc.timezone FROM locations lc WHERE lc.id = %[1]s.branch_id), 'UTC'))::date)`, alias)
}

// OverdueExpr returns the SQL predicate for the is_overdue flag: the invoice
// is open and due before the branch's own calendar day.
func OverdueExpr(alias string) string {
	return fmt.Sprintf(`(%[1]s.status IN ('UNPAID', 'PARTIAL') AND %[1]s.due_date IS NOT NULL AND %[1]s.due_date < %[2]s)`,
		alias, branchTodayExpr(alias))
}

// DueByBranchTodayExpr returns the SQL predicate for the Today page's
// inclusion: the invoice is open and due on or before the branch's own
// calendar day, so the page's list and the flag beside it agree.
func DueByBranchTodayExpr(alias string) string {
	return fmt.Sprintf(`(%[1]s.status IN ('UNPAID', 'PARTIAL') AND %[1]s.due_date IS NOT NULL AND %[1]s.due_date <= %[2]s)`,
		alias, branchTodayExpr(alias))
}
