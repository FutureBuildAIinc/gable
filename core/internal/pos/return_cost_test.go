// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// A return reverses COGS at the cost the sale relieved (ADR 0005 section
// 8.4, second review P1-6): a credit memo line that restocks takes its
// source invoice line's unit_cost, not today's average, so the inventory
// account never overstates a returned unit after the average moved.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestAReturnReversesCOGSAtTheSalesCost(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID, body := f.saleOf("2", tender("cash", 1198))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	// the average moves after the sale: the returned unit still comes back
	// at the 3.25 the sale relieved, not today's 9.00
	mustExec(t, f.db, `UPDATE products SET average_unit_cost = 9.00 WHERE id = $1`, f.productID)
	t.Cleanup(func() {
		mustExec(t, f.db, `UPDATE products SET average_unit_cost = 3.25 WHERE id = $1`, f.productID)
	})
	r := f.returnOn(t, saleID, lineID, "1")
	if r.status != http.StatusCreated {
		t.Fatalf("return = %d: %s", r.status, r.raw)
	}
	memoID := str(t, r.body, "credit_memo_id")
	// the memo line names the invoice line it reverses
	invoiceID := str(t, f.getSale(t, saleID), "invoice_id")
	invLine := f.scalar(`SELECT l.id::text FROM invoice_lines l WHERE l.invoice_id = $1 AND l.position = 0`, invoiceID).(string)
	var memoInvLine *string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT invoice_line_id::text FROM credit_memo_lines WHERE credit_memo_id = $1`, memoID).Scan(&memoInvLine); err != nil {
		t.Fatal(err)
	}
	if memoInvLine == nil || *memoInvLine != invLine {
		t.Errorf("the memo line names invoice line %v, want the sale's own %s", memoInvLine, invLine)
	}
	// the entry relieves at 3.25, not 9.00
	_, legs := f.legsFor(memoID)
	if legs["1030"].debit != 325 || legs["5010"].credit != 325 {
		t.Errorf("restock legs = 1030 %+v, 5010 %+v, want 325 each (the cost the sale relieved)", legs["1030"], legs["5010"])
	}
	if got := f.stock(); got != "99.0000/0.0000" {
		t.Errorf("stock = %s, want 99/0", got)
	}
	f.assertARInvariants(t)
}
