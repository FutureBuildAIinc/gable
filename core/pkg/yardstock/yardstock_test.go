// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package yardstock

import "testing"

func TestAtOrBelowIsInclusive(t *testing.T) {
	if got := AtOrBelow("SUM(i.quantity)", "p.reorder_point"); got != "SUM(i.quantity) <= p.reorder_point" {
		t.Errorf("AtOrBelow = %q", got)
	}
}
