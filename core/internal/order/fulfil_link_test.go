// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// RULE (ruling A, ADR 0007 section 5.8): the invoice a fulfilment writes for a
// customer not linked to its yard adds the link in the fulfilment's
// transaction, with one audit row naming the invoice and the yard.
func TestFulfilmentInvoiceLinksTheCustomerToItsYard(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	f.stock(f.productID, "10")
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'customer.branch_linked' AND entity_id = $1`, f.customerID)
	})

	r := f.create()
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	// The order's own create linked the yard; take it away so only the
	// fulfilment's invoice can put it back.
	if _, err := db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'customer.branch_linked' AND entity_id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer"})
	if r.status != 201 {
		t.Fatalf("fulfil = %d: %s", r.status, r.raw)
	}
	inv := invoiceIDOf(t, r)
	var links, audits int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM customer_branches cb JOIN invoices i ON i.customer_id = cb.customer_id AND i.branch_id = cb.branch_id WHERE i.id = $1`, inv).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'customer.branch_linked' AND entity_id = $1
		AND changes->>'document_id' = $2 AND changes->>'document_type' = 'invoice'`, f.customerID, inv).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if links != 1 || audits != 1 {
		t.Errorf("the fulfilment invoice left %d link(s) and %d audit row(s), want 1 and 1", links, audits)
	}
}
