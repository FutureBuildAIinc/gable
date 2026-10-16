// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

import (
	"context"
	"math"
	"testing"

	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
)

// An order fulfilled through the real fulfilment writer at a yard west of UTC
// is dated by its own yard's day (BranchLocalDate), so the invoice it writes is
// in the Sales Summary's default window, Total Invoiced and count alike: the
// yard's evening is never the server's tomorrow.
func TestFulfilmentInvoiceIsInTheSalesSummaryOfTheYardsDay(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	// The default branch, the yard the fixture's orders sit at, is on a zone
	// that is on an earlier day than UTC at every hour.
	testutil.SetBranchDayZone(t, db, testutil.AlwaysBehindUTC)
	f := newFixture(t, db)
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "3.2500")

	ctx := branchctx.WithSystem(context.Background())
	svc := reporting.NewService(reporting.NewRepository(db))
	before, err := svc.GetSalesSummary(ctx, "", "")
	if err != nil {
		t.Fatalf("summary before: %v", err)
	}

	r := f.create() // 10 PCS pickup at 5.50 = 5500, tax 8.875% = 488
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer"})
	if r.status != 201 {
		t.Fatalf("fulfil = %d: %s", r.status, r.raw)
	}

	// The invoice's own date is the yard's day, not the server's.
	var date, day string
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT invoice_date::text, `+testutil.BranchDaySQL+`::text FROM invoices WHERE id = $1`, invoiceIDOf(t, r)).Scan(&date, &day); err != nil {
		t.Fatal(err)
	}
	if date != day {
		t.Fatalf("invoice_date = %s, want the yard's day %s", date, day)
	}

	// A fresh service: the summary is cached for a minute per reach.
	after, err := reporting.NewService(reporting.NewRepository(db)).GetSalesSummary(ctx, "", "")
	if err != nil {
		t.Fatalf("summary after: %v", err)
	}
	if after.InvoiceCount != before.InvoiceCount+1 || math.Round((after.TotalInvoiced-before.TotalInvoiced)*100) != 5988 {
		t.Errorf("summary moved by %d invoices and %.2f, want 1 and 59.88 (the fulfilled order in the yard's evening)",
			after.InvoiceCount-before.InvoiceCount, after.TotalInvoiced-before.TotalInvoiced)
	}
}
