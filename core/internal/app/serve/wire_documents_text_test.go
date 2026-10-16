// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// The documents' routes carry the text a person reads (G1 notes N14, N15):
// the emailed statement names its period in the dealer's words, and the
// printed purchase order names its vendor.

// A statement email's subject reads "Jun 1, 2026 to Jun 30, 2026", never the
// ISO days; the route's recorded delivery holds the subject.
func TestEmailedStatementNamesItsPeriodInWords(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	ctx := context.Background()
	t.Cleanup(func() {
		f.docHandler.Wait()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM email_deliveries WHERE document_id = $1`, f.docCust)
	})

	status, body := f.callBody(t, "POST", "/api/v1/ar/customers/"+f.docCust.String()+"/statement/email?from=2026-06-01&to=2026-06-30",
		"", "finance", "u-a", f.branchA.String())
	if status != http.StatusAccepted {
		t.Fatalf("email statement: %d %s", status, body)
	}
	f.docHandler.Wait()
	var subject string
	if err := db.Pool.QueryRow(ctx, `SELECT subject FROM email_deliveries WHERE document_id = $1 ORDER BY created_at DESC LIMIT 1`,
		f.docCust).Scan(&subject); err != nil {
		t.Fatalf("the recorded delivery: %v", err)
	}
	if !strings.HasPrefix(subject, "Statement Jun 1, 2026 to Jun 30, 2026") {
		t.Errorf("subject = %q, want it to open \"Statement Jun 1, 2026 to Jun 30, 2026\"", subject)
	}
	if strings.Contains(subject, "2026-06") {
		t.Errorf("subject = %q, want no year first day", subject)
	}
}

// The printed purchase order carries its vendor's name: the print route reads
// the record with the vendor, not the bare record.
func TestPrintedPurchaseOrderNamesItsVendor(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	var name string
	if err := db.Pool.QueryRow(context.Background(), `SELECT name FROM vendors WHERE id = $1`, f.vendorID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	status, pdf := f.callBody(t, "GET", "/api/v1/documents/print/purchase-order/"+f.poA.String(), "", "purchasing", "u-a", f.branchA.String())
	if status != http.StatusOK {
		t.Fatalf("print purchase order: %d", status)
	}
	if !strings.Contains(string(pdf), "Vendor: "+name) {
		t.Errorf("the printed purchase order does not carry \"Vendor: %s\"", name)
	}
}
