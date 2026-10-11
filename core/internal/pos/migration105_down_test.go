// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Migration 105's down and backfill on contract era rows (eighth review
// P3-3): the down deletes the kit, component, charge and text lines the old
// shape cannot hold, so it refuses (naming the count) while any belongs to a
// completed sale, and the up's invoice line backfill matches only product
// sale lines to product invoice lines.

import (
	"context"
	"os"
	"strings"
	"testing"
)

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	mig105DoneSale = "00000000-0000-0000-0000-0000000000d9"
	mig105KitSale  = "00000000-0000-0000-0000-0000000000d8"
)

// RULE (eighth review P3-3): the down refuses while a contract era kit,
// component, charge or text line belongs to a completed sale, naming the
// count and changing nothing; once no completed sale holds one, it runs.
func TestMigration105_DownRefusesWhileACompletedSaleHoldsAContractEraLine(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	before, target, downFile := mig105Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	mig105Legacy(t, c)
	mig105Apply(t, c, target)
	ctx := context.Background()
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency, status, completed_at)
		VALUES ('`+mig105DoneSale+`', 'REG-01', '00000000-0000-0000-0000-0000000000e1', `+mig105Branch+`, 'USD', 'COMPLETED', NOW());
		INSERT INTO pos_line_items (transaction_id, line_type, description, price_source)
		VALUES ('`+mig105DoneSale+`', 'TEXT', 'delivery note', 'NONE');`); err != nil {
		t.Fatalf("seed the completed sale with a text line: %v", err)
	}
	sql := mustRead(t, downFile)
	_, err := c.Exec(ctx, sql)
	if err == nil {
		t.Fatal("the down ran while a completed sale held a text line")
	}
	if !strings.Contains(err.Error(), "1 ") || !strings.Contains(err.Error(), "completed") {
		t.Errorf("the down's refusal = %v, want it to name the count of lines and the completed sales", err)
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'pos_line_items' AND column_name = 'line_type'`); n != 1 {
		t.Errorf("the refused down changed the schema: line_type columns = %d, want 1", n)
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM pos_line_items WHERE transaction_id = '`+mig105DoneSale+`'`); n != 1 {
		t.Errorf("the refused down deleted the completed sale's line: %d rows, want 1", n)
	}
	// the same line on a sale that is not completed does not hold the down
	if _, err := c.Exec(ctx, `UPDATE pos_transactions SET status = 'OPEN', completed_at = NULL WHERE id = '`+mig105DoneSale+`'`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, sql); err != nil {
		t.Fatalf("the down refused with no completed sale holding a contract era line: %v", err)
	}
}

// RULE (eighth review P3-3): the backfill pairs product sale lines with
// product invoice lines only. A completed sale whose first line is a kit of
// the product and whose second is the product itself, over an invoice that
// holds the product line alone, links the product line to it and leaves the
// kit line unlinked (the kit's own invoice line is not a product line).
func TestMigration105_BackfillLinksOnlyProductLines(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	before, target, _ := mig105Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	mig105Legacy(t, c)
	mig105Apply(t, c, target)
	ctx := context.Background()
	if _, err := c.Exec(ctx, `
		INSERT INTO invoices (id, customer_id, branch_id, number, status, invoice_date, currency,
		                      subtotal, tax_amount, total_amount, amount_open, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-0000000000d7', '00000000-0000-0000-0000-0000000000a1',
		        `+mig105Branch+`, 'INV-MIG-2', 'UNPAID', '2026-01-01', 'USD', 5.50, 0.49, 5.99, 5.99,
		        '2026-01-01T11:02:00Z', '2026-01-01T11:02:00Z');
		INSERT INTO invoice_lines (id, invoice_id, product_id, description, quantity, uom, price_uom,
		                           uom_qty, price_uom_qty, unit_price, line_total, unit_cost, position, created_at)
		VALUES ('00000000-0000-0000-0000-0000000000d6', '00000000-0000-0000-0000-0000000000d7',
		        '00000000-0000-0000-0000-0000000000f1', '2x4x8', 1, 'PCS', 'PCS', 1, 1, 5.50, 5.50, 3.25, 0, '2026-01-01T11:02:00Z');
		INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency, status, completed_at, invoice_id)
		VALUES ('`+mig105KitSale+`', 'REG-01', '00000000-0000-0000-0000-0000000000e1', `+mig105Branch+`, 'USD', 'COMPLETED', NOW(),
		        '00000000-0000-0000-0000-0000000000d7');
		INSERT INTO pos_line_items (id, transaction_id, line_type, product_id, description, quantity, uom, price_uom,
		                            uom_qty, price_uom_qty, unit_price, line_total, position, price_source)
		VALUES
			('00000000-0000-0000-0000-0000000000d5', '`+mig105KitSale+`', 'KIT', '00000000-0000-0000-0000-0000000000f1',
			 'a kit', 1, 'PCS', 'PCS', 1, 1, 5.50, 5.50, 0, 'PRICE_LIST'),
			('00000000-0000-0000-0000-0000000000d4', '`+mig105KitSale+`', 'PRODUCT', '00000000-0000-0000-0000-0000000000f1',
			 '2x4x8', 1, 'PCS', 'PCS', 1, 1, 5.50, 5.50, 1, 'PRICE_LIST');`); err != nil {
		t.Fatalf("seed the kit sale: %v", err)
	}
	// the backfill runs on every apply
	mig105Apply(t, c, target)
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM pos_line_items WHERE id = '00000000-0000-0000-0000-0000000000d5' AND invoice_line_id IS NULL`); n != 1 {
		t.Errorf("the kit line was linked to an invoice line: the backfill matched a non product line")
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM pos_line_items
		WHERE id = '00000000-0000-0000-0000-0000000000d4' AND invoice_line_id = '00000000-0000-0000-0000-0000000000d6'`); n != 1 {
		t.Errorf("the product line was not linked to the invoice's product line")
	}
}

// RULE (ninth review P3-2, the invoice side of the backfill): the pairing
// counts product invoice lines only on the invoice's side too. An invoice
// whose first line of the product is a component (a kit's member) and whose
// second is the product's own line links the sale's product line to the
// product line, never to the component that happens to sit before it.
func TestMigration105_BackfillPairsTheInvoiceSideProductLineOnly(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	before, target, _ := mig105Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	mig105Legacy(t, c)
	mig105Apply(t, c, target)
	ctx := context.Background()
	if _, err := c.Exec(ctx, `
		INSERT INTO invoices (id, customer_id, branch_id, number, status, invoice_date, currency,
		                      subtotal, tax_amount, total_amount, amount_open, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-0000000000e7', '00000000-0000-0000-0000-0000000000a1',
		        `+mig105Branch+`, 'INV-MIG-3', 'UNPAID', '2026-01-01', 'USD', 11.00, 0.98, 11.98, 11.98,
		        '2026-01-01T11:02:00Z', '2026-01-01T11:02:00Z');
		INSERT INTO invoice_lines (id, invoice_id, line_type, parent_line_id, product_id, description, quantity, uom, price_uom,
		                           uom_qty, price_uom_qty, unit_price, line_total, unit_cost, taxable, position, created_at)
		VALUES
			('00000000-0000-0000-0000-0000000000e8', '00000000-0000-0000-0000-0000000000e7', 'KIT', NULL,
			 '00000000-0000-0000-0000-0000000000f1', 'bundle', 1, 'PCS', 'PCS', 1, 1, 11.00, 11.00, 3.25, TRUE, 0, '2026-01-01T11:02:00Z'),
			('00000000-0000-0000-0000-0000000000e5', '00000000-0000-0000-0000-0000000000e7', 'COMPONENT', '00000000-0000-0000-0000-0000000000e8',
			 '00000000-0000-0000-0000-0000000000f1', 'kit member', 1, 'PCS', 'PCS', 1, 1, 0, 0, 3.25, FALSE, 1, '2026-01-01T11:02:00Z'),
			('00000000-0000-0000-0000-0000000000e6', '00000000-0000-0000-0000-0000000000e7', 'PRODUCT', NULL,
			 '00000000-0000-0000-0000-0000000000f1', '2x4x8', 1, 'PCS', 'PCS', 1, 1, 5.50, 5.50, 3.25, TRUE, 2, '2026-01-01T11:02:00Z');
		INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency, status, completed_at, invoice_id)
		VALUES ('`+mig105KitSale+`', 'REG-01', '00000000-0000-0000-0000-0000000000e1', `+mig105Branch+`, 'USD', 'COMPLETED', NOW(),
		        '00000000-0000-0000-0000-0000000000e7');
		INSERT INTO pos_line_items (id, transaction_id, line_type, product_id, description, quantity, uom, price_uom,
		                            uom_qty, price_uom_qty, unit_price, line_total, position, price_source)
		VALUES ('00000000-0000-0000-0000-0000000000e4', '`+mig105KitSale+`', 'PRODUCT', '00000000-0000-0000-0000-0000000000f1',
		 '2x4x8', 1, 'PCS', 'PCS', 1, 1, 5.50, 5.50, 0, 'PRICE_LIST');`); err != nil {
		t.Fatalf("seed the component first invoice: %v", err)
	}
	// the backfill runs on every apply
	mig105Apply(t, c, target)
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM pos_line_items
		WHERE id = '00000000-0000-0000-0000-0000000000e4' AND invoice_line_id = '00000000-0000-0000-0000-0000000000e6'`); n != 1 {
		t.Errorf("the sale's product line was not linked to the invoice's own product line")
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM pos_line_items
		WHERE id = '00000000-0000-0000-0000-0000000000e4' AND invoice_line_id IN ('00000000-0000-0000-0000-0000000000e5', '00000000-0000-0000-0000-0000000000e8')`); n != 0 {
		t.Errorf("the sale's product line was linked to the component or kit line that sits before it")
	}
}

// RULE (ninth review P3-3, tenth review P3-4): the down drops the refund
// intents table with its rows, and a PENDING claim dropped that way releases
// its tender (the next refund would pay the card twice), so the down refuses
// while any claim is still pending, naming the count and changing nothing.
func TestMigration105_DownRefusesWhileAClaimIsPending(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	before, target, downFile := mig105Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	mig105Legacy(t, c)
	mig105Apply(t, c, target)
	ctx := context.Background()
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_refund_intents (id, sale_id, tender_id, gateway_tx_id, amount, act, state)
		VALUES ('00000000-0000-0000-0000-0000000000f2', '00000000-0000-0000-0000-0000000000b1',
		        '00000000-0000-0000-0000-0000000000d2', 'gw-mig', 9.98, 'void', 'PENDING')`); err != nil {
		t.Fatalf("seed the pending claim: %v", err)
	}
	sql := mustRead(t, downFile)
	_, err := c.Exec(ctx, sql)
	if err == nil {
		t.Fatal("the down ran while a refund claim was still pending")
	}
	if !strings.Contains(err.Error(), "pending") || !strings.Contains(err.Error(), "1 ") {
		t.Errorf("the down's refusal = %v, want it to name the count of pending claims", err)
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM pos_refund_intents WHERE state = 'PENDING'`); n != 1 {
		t.Errorf("the refused down did not leave the claim standing: %d rows", n)
	}
	// the claim settled, the down runs and drops the table with it
	if _, err := c.Exec(ctx, `UPDATE pos_refund_intents SET state = 'DONE' WHERE state = 'PENDING'`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, sql); err != nil {
		t.Fatalf("the down refused with no pending claim: %v", err)
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'pos_refund_intents'`); n != 0 {
		t.Errorf("the intents table survived the down: %d rows", n)
	}
}

// RULE (tenth review P3-4): a voided sale's contract era lines are history
// the same way a completed sale's are, so the down refuses over them too.
func TestMigration105_DownRefusesWhileAVoidedSaleHoldsAContractEraLine(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	before, target, downFile := mig105Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	mig105Legacy(t, c)
	mig105Apply(t, c, target)
	ctx := context.Background()
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency, status, completed_at)
		VALUES ('`+mig105DoneSale+`', 'REG-01', '00000000-0000-0000-0000-0000000000e1', `+mig105Branch+`, 'USD', 'VOIDED', NOW());
		INSERT INTO pos_line_items (transaction_id, line_type, description, price_source)
		VALUES ('`+mig105DoneSale+`', 'TEXT', 'delivery note', 'NONE');`); err != nil {
		t.Fatalf("seed the voided sale with a text line: %v", err)
	}
	sql := mustRead(t, downFile)
	_, err := c.Exec(ctx, sql)
	if err == nil {
		t.Fatal("the down ran while a voided sale held a text line")
	}
	if !strings.Contains(err.Error(), "1 ") || !strings.Contains(err.Error(), "voided") {
		t.Errorf("the down's refusal = %v, want it to name the count of lines and the voided sales", err)
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM pos_line_items WHERE transaction_id = '`+mig105DoneSale+`'`); n != 1 {
		t.Errorf("the refused down deleted the voided sale's line: %d rows, want 1", n)
	}
}
