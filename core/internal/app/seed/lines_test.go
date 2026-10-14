// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import "testing"

// No order or invoice the demo seed writes is without lines, and the lines of
// the Ridgeline Formwork order and invoice add up to their subtotal (a
// visitor who opens the open balance on AR Aging must see what it is for).
func TestSeedLeavesNoOrderOrInvoiceWithoutLines(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a scratch database of its own; skipped under -short")
	}
	db, dbURL := scratchSeedDB(t)
	runSeedCommand(t, dbURL, "./cmd/migrate")
	runSeedCommand(t, dbURL, "./cmd/seed")

	for _, q := range []struct{ what, sql string }{
		{"orders", `SELECT COALESCE(string_agg(o.id::text, ', '), '') FROM orders o
			WHERE NOT EXISTS (SELECT 1 FROM order_lines l WHERE l.order_id = o.id)`},
		{"invoices", `SELECT COALESCE(string_agg(i.id::text, ', '), '') FROM invoices i
			WHERE NOT EXISTS (SELECT 1 FROM invoice_lines l WHERE l.invoice_id = i.id)`},
	} {
		var ids string
		if err := db.QueryRow(q.sql).Scan(&ids); err != nil {
			t.Fatal(err)
		}
		if ids != "" {
			t.Errorf("seeded %s with no lines: %s", q.what, ids)
		}
	}

	for _, q := range []struct{ what, sql string }{
		{"order", `SELECT o.subtotal, COALESCE(SUM(l.line_total), 0) FROM orders o
			JOIN customers c ON c.id = o.customer_id AND c.account_number = 'RFC-001'
			LEFT JOIN order_lines l ON l.order_id = o.id GROUP BY o.id`},
		{"invoice", `SELECT i.subtotal, COALESCE(SUM(l.line_total), 0) FROM invoices i
			JOIN customers c ON c.id = i.customer_id AND c.account_number = 'RFC-001'
			LEFT JOIN invoice_lines l ON l.invoice_id = i.id GROUP BY i.id`},
	} {
		var subtotal, lines float64
		if err := db.QueryRow(q.sql).Scan(&subtotal, &lines); err != nil {
			t.Fatalf("Ridgeline %s: %v", q.what, err)
		}
		if subtotal != 16800 || lines != subtotal {
			t.Errorf("Ridgeline %s: subtotal %.2f, lines add up to %.2f, want 16800.00 both", q.what, subtotal, lines)
		}
	}
}
