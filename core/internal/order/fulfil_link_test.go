// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

import (
	"context"
	"strings"
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

// RULE (ruling A, brief item 3): a fulfilment whose invoice link fails rolls
// back whole: no invoice, no customer_branches row, no audit row, and the
// order stays fulfillable. A control fulfilment with no failure forced shows
// the same call reaches the link.
func TestFulfilmentInvoiceRollsBackWithItsLink(t *testing.T) {
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
	unlink := func() {
		if _, err := db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = $1`, f.customerID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'customer.branch_linked' AND entity_id = $1`, f.customerID); err != nil {
			t.Fatal(err)
		}
	}
	confirmed := func() (id string, rev int64) {
		r := f.create()
		id = str(t, r.body, "id")
		r = f.transition(id, 1, "confirmed")
		unlink()
		return id, revision(t, r)
	}
	linkRows := func() (links, audits int) {
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM customer_branches WHERE customer_id = $1`, f.customerID).Scan(&links); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'customer.branch_linked' AND entity_id = $1`, f.customerID).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		return links, audits
	}
	invoices := func() (n int) {
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE customer_id = $1`, f.customerID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The control.
	id, rev := confirmed()
	if r := f.fulfil(id, rev, map[string]any{"picked_up_by": "Counter customer"}); r.status != 201 {
		t.Fatalf("control fulfil = %d: %s", r.status, r.raw)
	}
	if links, audits := linkRows(); links != 1 || audits != 1 {
		t.Fatalf("control left %d link(s) and %d audit row(s), want 1 and 1", links, audits)
	}

	// The forced failure: the link's audit row cannot be written.
	id, rev = confirmed()
	before := invoices()
	trg := "fail_fulfil_link_" + strings.ReplaceAll(f.customerID.String()[:8], "-", "")
	for _, stmt := range []string{
		`CREATE OR REPLACE FUNCTION ` + trg + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced link failure'; END $$`,
		`CREATE TRIGGER ` + trg + ` BEFORE INSERT ON audit_log FOR EACH ROW
		   WHEN (NEW.action = 'customer.branch_linked' AND NEW.entity_id = '` + f.customerID.String() + `')
		   EXECUTE FUNCTION ` + trg + `()`,
	} {
		if _, err := db.Pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DROP TRIGGER IF EXISTS `+trg+` ON audit_log`)
		_, _ = db.Pool.Exec(ctx, `DROP FUNCTION IF EXISTS `+trg+`()`)
	})
	if r := f.fulfil(id, rev, map[string]any{"picked_up_by": "Counter customer"}); r.status < 400 {
		t.Fatalf("fulfil with the link failing = %d, want an error", r.status)
	}
	if links, audits := linkRows(); links != 0 || audits != 0 {
		t.Errorf("a rolled back fulfilment left %d link(s) and %d audit row(s), want none", links, audits)
	}
	if after := invoices(); after != before {
		t.Errorf("a rolled back fulfilment left %d invoice(s), want %d", after, before)
	}
}
