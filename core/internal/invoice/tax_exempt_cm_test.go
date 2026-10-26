// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

// CP-10 fix round: a credit memo with no invoice reads the customer's tax
// exemption through the one window rule (tax.ExemptionCovering on the
// customer's branch day), so an expired certificate exempts nothing and the
// memo carries the branch rate's tax.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// RULE: an expired certificate does not exempt a credit memo with no
// invoice: the tax is the branch rate's, as it is at the counter.
func TestCP10_ExpiredCertificateDoesNotExemptACreditMemo(t *testing.T) {
	testutil.LockBranchDay(t)
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	day := testutil.BranchDay(t, db)
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO tax_exemptions (customer_id, exempt_reason, certificate_number, effective_date, expiry_date)
		VALUES ($1, 'RESALE', 'CERT-EXPIRED', $2::date, $3::date)`,
		f.customerID, day.AddDate(0, 0, -30).Format("2006-01-02"), day.AddDate(0, 0, -1).Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(context.Background(), `DELETE FROM tax_exemptions WHERE customer_id = $1`, f.customerID) })

	body := map[string]any{"customer_id": f.customerID.String(), "reason_code": "price_adjustment", "reason": "goodwill",
		"lines": []map[string]any{
			{"line_type": "product", "description": "Freight allowance", "quantity": "-1", "uom": "EA", "unit_price_ten_thousandths": 100000}}}
	r := f.do("POST", "/api/v1/credit-memos", body)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	// 10.00 credited at the branch rate 8.875% = 88.75 -> 89: the expired
	// window exempts nothing
	if got := num(t, r.body, "tax_cents"); got != -89 {
		t.Errorf("tax_cents = %d, want -89 (the branch rate, not the expired exemption)", got)
	}
}
