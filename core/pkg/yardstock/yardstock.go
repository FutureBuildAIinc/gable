// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package yardstock holds the one definition of a yard's stock and of the
// reorder comparison that the low stock alert (the dashboard) and the reorder
// list read (FROM-PRINCIPAL item 135 b): an inventory row sits at one
// location, a yard's stock is the sum of every row whose location's branch_id
// is that yard (the yard's own row, its zones and its bins), a row at no yard
// is in no yard's sum, and a yard is at or below its reorder point when that
// sum is at or below it.
package yardstock

// Rows is the FROM fragment of a yard's stock: inventory rows as i joined to
// the locations they sit at as l, where the location names its yard. A row with
// no location, or at a location with no branch, is in no yard and is dropped.
// The yard of a row is l.branch_id.
const Rows = `inventory i JOIN locations l ON l.id = i.location_id AND l.branch_id IS NOT NULL`

// AtOrBelow is the reorder comparison for a yard's summed on hand (sum) and a
// product's reorder point: at or below, never strictly below.
func AtOrBelow(sum, point string) string {
	return sum + ` <= ` + point
}
