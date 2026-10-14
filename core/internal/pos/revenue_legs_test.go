// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// A returned charge line credits its own revenue account (ADR 0005 section
// 8.3, second review P3-5): a FREIGHT charge posts to 4020 on the sale, and
// its credit memo debits 4020 back, not 4010.

import (
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestAReturnedChargeLineCreditsItsOwnRevenueAccount(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, map[string]any{"line_type": "charge", "charge_code": "FREIGHT",
		"description": "delivery", "quantity": "1", "unit_price_ten_thousandths": 250000}); r.status != 200 {
		t.Fatalf("charge line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 2500)); r.status != 200 {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.returnOn(t, saleID, lineID, "1", map[string]any{"restock": false})
	if r.status != 201 {
		t.Fatalf("return = %d: %s", r.status, r.raw)
	}
	memoID := str(t, r.body, "credit_memo_id")
	_, legs := f.legsFor(memoID)
	if legs["4020"].debit != 2500 {
		t.Errorf("4020 debit = %+v, want 2500 (the freight charge debits its own revenue account back)", legs["4020"])
	}
	if legs["4010"].debit != 0 {
		t.Errorf("4010 debit = %+v, want 0 (the freight line posts no product revenue)", legs["4010"])
	}
	f.assertARInvariants(t)
}
