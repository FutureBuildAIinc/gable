// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// CP-10: the order path reads the same exemption window the counter reads
// (tax.ExemptionCovering): a certificate that has expired, or whose window has not
// begun, does not make an order's tax exempt.

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
)

// orderExemption writes the fixture customer a certificate window and
// removes it at the test's end.
func orderExemption(t *testing.T, f *fixture, effective, expiry *time.Time) {
	t.Helper()
	ctx := context.Background()
	e, x := any(nil), any(nil)
	if effective != nil {
		e = effective.Format("2006-01-02")
	}
	if expiry != nil {
		x = expiry.Format("2006-01-02")
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO tax_exemptions (customer_id, exempt_reason, certificate_number, effective_date, expiry_date)
		VALUES ($1, 'RESALE', 'CERT-CP10-ORD', $2::date, $3::date)`, f.customerID, e, x); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM tax_exemptions WHERE customer_id = $1`, f.customerID)
	})
}

// RULE: an expired certificate does not exempt an order: the tax is the
// branch rate's and the source is the rate, the same window the counter
// reads (tax.ExemptionCovering).
func TestCP10_ExpiredCertificateDoesNotExemptOnOrders(t *testing.T) {
	testutil.LockBranchDay(t)
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	day := testutil.BranchDay(t, f.db)
	orderExemption(t, f, &[]time.Time{day.AddDate(0, 0, -30)}[0], &[]time.Time{day.AddDate(0, 0, -1)}[0])

	r := f.create(f.line("10")) // subtotal 5500, branch tax 488
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	if exempt, _ := r.body["tax_exempt"].(bool); exempt {
		t.Error("tax_exempt = true, want false (the window has closed)")
	}
	if src := str(t, r.body, "tax_source"); src != "branch_rate" {
		t.Errorf("tax_source = %q, want branch_rate", src)
	}
	if tax := num(t, r.body, "tax_cents"); tax != 488 {
		t.Errorf("tax_cents = %d, want 488 (the branch rate on 5500)", tax)
	}
}

// RULE: a certificate whose window has not begun does not exempt an order
// yet.
func TestCP10_FutureCertificateDoesNotExemptOnOrders(t *testing.T) {
	testutil.LockBranchDay(t)
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	day := testutil.BranchDay(t, f.db)
	orderExemption(t, f, &[]time.Time{day.AddDate(0, 0, 1)}[0], nil)

	r := f.create(f.line("10"))
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	if exempt, _ := r.body["tax_exempt"].(bool); exempt {
		t.Error("tax_exempt = true, want false (the window has not begun)")
	}
	if tax := num(t, r.body, "tax_cents"); tax != 488 {
		t.Errorf("tax_cents = %d, want 488", tax)
	}
}
