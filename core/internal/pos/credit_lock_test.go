// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The credit check is serialized (ADR 0005 section 11 step 1a, second
// review P2-1): the completion takes the customer's credit advisory lock
// before it reads the open receivable, so two ACCOUNT sales for one customer
// cannot both read an exposure that does not yet count the other and end
// over the limit together.

import (
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestTwoAccountSalesCannotBothPassTheCreditCheck(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 30.00 WHERE id = $1`, f.customerID)
	t.Cleanup(func() {
		mustExec(t, f.db, `UPDATE customers SET credit_limit = NULL WHERE id = $1`, f.customerID)
	})
	// Two sales that each fit the limit alone (2395 of a 3000 limit) but not
	// together; completed at the same time at pool size 4.
	sales := make([]string, 2)
	for i := range sales {
		sales[i] = f.startSale(&f.customerID)
		if r := f.addLine(sales[i], f.productLine("4")); r.status != 200 {
			t.Fatalf("add line = %d: %s", r.status, r.raw)
		}
	}
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i := range sales {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.completeSale(sales[i], tender("account", 2395)).status
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, st := range statuses {
		if st == 200 {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("statuses = %v, want exactly one 200 (the other refused credit_limit)", statuses)
	}
	if got := f.balance(); got > 3000 {
		t.Errorf("balance_due = %d, want at most the 3000 limit", got)
	}
	f.assertARInvariants(t)
}
