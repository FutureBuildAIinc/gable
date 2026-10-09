// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// TestFulfilmentStampsTheBranchLocalInvoiceDate proves the act calls the
// branch's local date: at one instant inside the gap between UTC midnight and
// a branch behind UTC, the invoice of a branch in that zone carries the
// previous day, a UTC branch carries the UTC day, and the NET30 due date is
// thirty days after whichever invoice date was stamped.
func TestFulfilmentStampsTheBranchLocalInvoiceDate(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	instant := time.Date(2031, 1, 15, 3, 30, 0, 0, time.UTC) // 22:30 on the 14th in New York, 19:30 in Etc/GMT+8

	cases := []struct {
		zone, invoiceDate, dueDate string
	}{
		{"America/New_York", "2031-01-14", "2031-02-13"},
		{"Etc/GMT+8", "2031-01-14", "2031-02-13"},
		{"Etc/UTC", "2031-01-15", "2031-02-14"},
	}
	for _, c := range cases {
		t.Run(c.zone, func(t *testing.T) {
			f := newFixture(t, db)
			branch := uuid.New()
			if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, timezone, default_tax_rate) VALUES ($1, 'BRANCH', $2, $3, 0.05)`,
				branch, "IDT-"+branch.String()[:8], c.zone); err != nil {
				t.Fatalf("insert branch: %v", err)
			}
			yard := uuid.New()
			if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`,
				yard, "IDY-"+yard.String()[:8], branch); err != nil {
				t.Fatalf("insert yard: %v", err)
			}
			if _, err := db.Pool.Exec(ctx, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 10, 0)`,
				f.productID, yard); err != nil {
				t.Fatalf("seed inventory: %v", err)
			}
			f.serveWith(f.withStock(), f.withMoney(), func(s *order.Service) *order.Service {
				return s.WithClockForTest(func() time.Time { return instant })
			})
			f.cleanMoney()
			t.Cleanup(func() {
				_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE location_id = $1`, yard)
				_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1`, f.customerID)
				_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, yard)
				_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, branch)
			})

			body := f.createBody()
			body["branch_id"] = branch.String()
			r := f.do("POST", "/api/v1/orders", body)
			if r.status != 201 {
				t.Fatalf("create = %d: %s", r.status, r.raw)
			}
			id := str(t, r.body, "id")
			r = f.transition(id, 1, "confirmed")
			if r.status != 200 {
				t.Fatalf("confirm = %d: %s", r.status, r.raw)
			}
			r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer"})
			if r.status != 201 {
				t.Fatalf("fulfil = %d: %s", r.status, r.raw)
			}
			var invDate, due string
			if err := db.Pool.QueryRow(ctx, `SELECT invoice_date::text, (due_date AT TIME ZONE 'UTC')::date::text FROM invoices WHERE id = $1`,
				invoiceIDOf(t, r)).Scan(&invDate, &due); err != nil {
				t.Fatal(err)
			}
			if invDate != c.invoiceDate || due != c.dueDate {
				t.Errorf("%s: invoice_date %s due_date %s, want %s and %s", c.zone, invDate, due, c.invoiceDate, c.dueDate)
			}
		})
	}
}
