// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// TestCP03_ReceiptNamesTheCardAndTheCashChange: a card and cash sale prints
// its tenders on both layouts from the stored rows: the card by brand and last
// four (no number is stored), the cash by its amount and the change given.
func TestCP03_ReceiptNamesTheCardAndTheCashChange(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimApprove, 0), withRealThermalReceipts())
	sale, total := f.cartOf("2", &f.customerID)
	const cardPart = int64(500)
	cash := total - cardPart + 100
	if r := f.completeSale(sale, tender("card", cardPart), tender("cash", cash)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	for _, layout := range []string{"layout=thermal80", "layout=invoice"} {
		r := f.printReceipt(sale, layout)
		if r.status != http.StatusOK {
			t.Fatalf("%s print = %d: %s", layout, r.status, r.raw)
		}
		for _, want := range []string{"VISA", "****4242", "CASH", "Change: $1.00"} {
			if !strings.Contains(string(r.raw), want) {
				t.Errorf("%s PDF missing %q", layout, want)
			}
		}
	}
}
