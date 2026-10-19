// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Migration 126 (the counter's buyers, tenders, returns and receipts): it
// applies twice to the same effect, its down removes everything it added and
// puts the credit memo line shape back, and a sealed signature refuses an
// update.

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func mig126Files(t *testing.T) (before []string, target, down string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "126_"):
			target = f
		case base < "126_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 126 not found")
	}
	return before, target, "../../migrations/down/126_counter_buyers_returns_and_receipts_down.sql"
}

// the columns, tables and constraints the up adds, as the down must remove
const mig126Present = `
	SELECT
	  (SELECT count(*) FROM information_schema.columns WHERE
	     (table_name = 'customers' AND column_name IN ('named_buyer_required', 'check_limit'))
	  OR (table_name = 'customer_contacts' AND column_name IN ('authorized_buyer', 'buyer_sale_cap'))
	  OR (table_name = 'pos_tenders' AND column_name IN ('id_checked', 'id_type'))
	  OR (table_name = 'pos_transactions' AND column_name IN ('buyer_contact_id', 'print_count', 'exchange_return_id'))
	  OR (table_name = 'pos_returns' AND column_name IN ('restocking_fee', 'exchange_sale_id'))
	  OR (table_name = 'pos_return_lines' AND column_name IN ('reason_code', 'restocking_fee')))
	+ (SELECT count(*) FROM information_schema.tables WHERE table_name IN ('pos_account_overrides', 'pos_signatures'))`

func TestMigration126_UpIsIdempotentAndDownReversesIt(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	before, target, down := mig126Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	ctx := context.Background()
	if n := mig105Scalar[int](t, c, mig126Present); n != 0 {
		t.Fatalf("before the up, %d of the migration's objects already exist", n)
	}
	mig105Apply(t, c, target)
	const want = 13 + 2
	if n := mig105Scalar[int](t, c, mig126Present); n != want {
		t.Fatalf("after the up, %d of the migration's objects exist, want %d", n, want)
	}
	// a second apply changes nothing
	mig105Apply(t, c, target)
	if n := mig105Scalar[int](t, c, mig126Present); n != want {
		t.Fatalf("after a second up, %d objects, want %d", n, want)
	}

	// a return credit memo with a product line (-10.00), tax (-0.80), and a
	// restocking fee CHARGE line (+1.50), which only the new shape admits
	if _, err := c.Exec(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ('00000000-0000-0000-0000-000000001260', 'Mig Co', 'MIG126', (SELECT id FROM locations LIMIT 1));
		INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost)
		VALUES ('00000000-0000-0000-0000-000000001269', 'MIG-SKU-126', 'Mig Item', 'EA', 10.00, 5.00);
		INSERT INTO credit_memos (id, number, customer_id, branch_id, currency, reason_code, reason, status, amount, subtotal,
			tax_amount, total_amount, amount_open, memo_date)
		VALUES ('00000000-0000-0000-0000-000000001261', 'CM-001261', '00000000-0000-0000-0000-000000001260',
			(SELECT id FROM locations LIMIT 1), 'USD', 'RETURN', 'x', 'OPEN', 9.30, -8.50, -0.80, -9.30, -9.30, CURRENT_DATE);
		INSERT INTO credit_memo_lines (credit_memo_id, position, line_type, product_id, description, quantity, uom,
			price_uom, uom_qty, price_uom_qty, unit_price, price_source, line_total, taxable)
		VALUES ('00000000-0000-0000-0000-000000001261', 0, 'PRODUCT', '00000000-0000-0000-0000-000000001269',
			'Mig Item', -1, 'EA', 'EA', 1, 1, 10.00, 'PRICE_LIST', -10.00, TRUE);
		INSERT INTO credit_memo_lines (credit_memo_id, position, line_type, charge_code_id, description, quantity, uom,
			price_uom, uom_qty, price_uom_qty, unit_price, price_source, line_total, taxable)
		SELECT '00000000-0000-0000-0000-000000001261', 1, 'CHARGE', cc.id, 'Restocking fee', 1, 'EA', 'EA', 1, 1, 1.50,
			'MANUAL', 1.50, FALSE FROM charge_codes cc WHERE cc.code = 'RESTOCK';`); err != nil {
		t.Fatalf("a restocking fee line was refused by the new shape: %v", err)
	}
	// a taxable fee line is not a shape
	if _, err := c.Exec(ctx, `
		INSERT INTO credit_memo_lines (credit_memo_id, position, line_type, charge_code_id, description, quantity, uom,
			price_uom, uom_qty, price_uom_qty, unit_price, price_source, line_total, taxable)
		SELECT '00000000-0000-0000-0000-000000001261', 2, 'CHARGE', cc.id, 'Restocking fee', 1, 'EA', 'EA', 1, 1, 1.50,
			'MANUAL', 1.50, TRUE FROM charge_codes cc WHERE cc.code = 'RESTOCK'`); err == nil {
		t.Error("a taxable positive charge line was admitted")
	}

	mig105Apply(t, c, down)
	if n := mig105Scalar[int](t, c, mig126Present); n != 0 {
		t.Errorf("after the down, %d of the migration's objects remain", n)
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM credit_memo_lines WHERE line_type = 'CHARGE' AND quantity > 0`); n != 0 {
		t.Errorf("the down left %d restocking fee lines the old shape cannot hold", n)
	}
	if inv := mig105Scalar[string](t, c, `
		SELECT cm.subtotal::text || ':' || COALESCE(SUM(l.line_total), 0)::text || ':' ||
		       cm.total_amount::text || ':' || (cm.subtotal + cm.tax_amount)::text || ':' ||
		       cm.amount::text || ':' || cm.amount_open::text
		FROM credit_memos cm
		LEFT JOIN credit_memo_lines l ON l.credit_memo_id = cm.id
		WHERE cm.id = '00000000-0000-0000-0000-000000001261'
		GROUP BY cm.id`); inv != "-10.00:-10.00:-10.80:-10.80:10.80:-10.80" {
		t.Errorf("credit_memo invariants after down = %s, want -10.00:-10.00:-10.80:-10.80:10.80:-10.80", inv)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO credit_memo_lines (credit_memo_id, position, line_type, charge_code_id, description, quantity, uom,
			price_uom, uom_qty, price_uom_qty, unit_price, price_source, line_total, taxable)
		SELECT '00000000-0000-0000-0000-000000001261', 2, 'CHARGE', cc.id, 'Restocking fee', 1, 'EA', 'EA', 1, 1, 2.00,
			'MANUAL', 2.00, FALSE FROM charge_codes cc WHERE cc.code = 'RESTOCK'`); err == nil {
		t.Error("the old shape admitted a positive charge line after the down")
	}
	// the up goes back on cleanly after its down
	mig105Apply(t, c, target)
	if n := mig105Scalar[int](t, c, mig126Present); n != want {
		t.Errorf("after the up again, %d objects, want %d", n, want)
	}
}

func TestMigration126_ASealedSignatureRefusesAnUpdate(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	before, target, _ := mig126Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	mig105Apply(t, c, target)
	ctx := context.Background()
	hash := strings.Repeat("a", 64)
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency, status)
		VALUES ('00000000-0000-0000-0000-000000001262', 'REG-01', '00000000-0000-0000-0000-0000000000e1',
			(SELECT id FROM locations LIMIT 1), 'USD', 'COMPLETED')`); err != nil {
		t.Fatalf("seed the sale: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_signatures (sale_id, signer_name, image, image_sha256, total_cents, signed_at, seal)
		VALUES ('00000000-0000-0000-0000-000000001262', 'A Signer', '\x89504e47', $1, 100, NOW(), $1)`, hash); err != nil {
		t.Fatalf("seal a signature: %v", err)
	}
	if _, err := c.Exec(ctx, `UPDATE pos_signatures SET signer_name = 'Someone Else' WHERE sale_id = '00000000-0000-0000-0000-000000001262'`); err == nil {
		t.Error("a sealed signature accepted an update")
	}
	if _, err := c.Exec(ctx, `UPDATE pos_signatures SET image = '\x00'`); err == nil {
		t.Error("a sealed signature's image accepted an update")
	}
	if s := mig105Scalar[string](t, c, `SELECT signer_name FROM pos_signatures`); s != "A Signer" {
		t.Errorf("signer = %q after the refused updates, want unchanged", s)
	}
	// the image is capped
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency, status)
		VALUES ('00000000-0000-0000-0000-000000001263', 'REG-01', '00000000-0000-0000-0000-0000000000e1',
			(SELECT id FROM locations LIMIT 1), 'USD', 'COMPLETED')`); err != nil {
		t.Fatalf("seed the second sale: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_signatures (sale_id, signer_name, image, image_sha256, total_cents, signed_at, seal)
		VALUES ('00000000-0000-0000-0000-000000001263', 'A Signer', repeat('x', 131073)::bytea, $1, 100, NOW(), $1)`, hash); err == nil {
		t.Error("an image past the cap was admitted")
	}
}

// mig132File answers the 132 up and down files, or fails the test when the
// upgrade migration is not there.
func mig132Files(t *testing.T) (up, down string) {
	t.Helper()
	ups, err := filepath.Glob("../../migrations/132_*.sql")
	if err != nil || len(ups) != 1 {
		t.Fatalf("migration 132 not found: %v", err)
	}
	downs, err := filepath.Glob("../../migrations/down/132_*_down.sql")
	if err != nil || len(downs) != 1 {
		t.Fatalf("migration 132 down not found: %v", err)
	}
	return ups[0], downs[0]
}

// RULE (R2 P1-1): a database that already applied 126 at its original
// definition (the live demos and CI's upgrade check hold that shape) reaches
// the widened override kind and the sealed signature's delete wall only by
// applying 132 on top, and 132's down puts both back.
func TestMigration132_UpgradeFromOriginal126SealsDeletesAndWidensOverrideKind(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	before, target, _ := mig126Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	mig105Apply(t, c, target) // 126 as origin/refactor/v1 holds it
	up, down := mig132Files(t)
	mig105Apply(t, c, up)
	ctx := context.Background()
	hash := strings.Repeat("a", 64)

	// the sale, its sealed signature and a refund_method override row
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency, status)
		VALUES ('00000000-0000-0000-0000-000000001264', 'REG-01', '00000000-0000-0000-0000-0000000000e1',
			(SELECT id FROM locations LIMIT 1), 'USD', 'COMPLETED')`); err != nil {
		t.Fatalf("seed the sale: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_signatures (sale_id, signer_name, image, image_sha256, total_cents, signed_at, seal)
		VALUES ('00000000-0000-0000-0000-000000001264', 'A Signer', '\x89504e47', $1, 100, NOW(), $1)`, hash); err != nil {
		t.Fatalf("seal a signature: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_account_overrides (sale_id, kind, approved_by, reason, cap)
		VALUES ('00000000-0000-0000-0000-000000001264', 'refund_method', 'a-manager', 'cash payout approved', 59.99)`); err != nil {
		t.Fatalf("a refund_method override row was refused after 132: %v", err)
	}

	// a direct delete of the seal is refused while the sale stands
	if _, err := c.Exec(ctx, `DELETE FROM pos_signatures WHERE sale_id = '00000000-0000-0000-0000-000000001264'`); err == nil {
		t.Error("a sealed signature accepted a direct delete after 132")
	}
	// the sale's own cascade cleanup goes through
	if _, err := c.Exec(ctx, `DELETE FROM pos_transactions WHERE id = '00000000-0000-0000-0000-000000001264'`); err != nil {
		t.Fatalf("the parent sale's cascade cleanup was refused after 132: %v", err)
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM pos_signatures`); n != 0 {
		t.Errorf("%d signatures remain after the sale's cascade, want 0", n)
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM pos_account_overrides`); n != 0 {
		t.Errorf("%d overrides remain after the sale's cascade, want 0", n)
	}

	// a second sale and seal for the down's shape
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency, status)
		VALUES ('00000000-0000-0000-0000-000000001265', 'REG-01', '00000000-0000-0000-0000-0000000000e1',
			(SELECT id FROM locations LIMIT 1), 'USD', 'COMPLETED')`); err != nil {
		t.Fatalf("seed the second sale: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_signatures (sale_id, signer_name, image, image_sha256, total_cents, signed_at, seal)
		VALUES ('00000000-0000-0000-0000-000000001265', 'A Signer', '\x89504e47', $1, 100, NOW(), $1)`, hash); err != nil {
		t.Fatalf("seal the second signature: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_account_overrides (sale_id, kind, approved_by, reason, cap)
		VALUES ('00000000-0000-0000-0000-000000001265', 'refund_method', 'a-manager', 'cash payout approved', 59.99)`); err != nil {
		t.Fatalf("a refund_method override row was refused before the down: %v", err)
	}

	// the down puts the kind and the trigger back
	mig105Apply(t, c, down)
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_account_overrides (sale_id, kind, approved_by, reason, cap)
		VALUES ('00000000-0000-0000-0000-000000001265', 'refund_method', 'a-manager', 'refused', 59.99)`); err == nil {
		t.Error("a refund_method override row was admitted after the 132 down")
	}
	if _, err := c.Exec(ctx, `DELETE FROM pos_signatures WHERE sale_id = '00000000-0000-0000-0000-000000001265'`); err != nil {
		t.Errorf("a direct delete was refused after the 132 down (the wall was update only): %v", err)
	}
	// the update wall 126 originally set still stands after the down
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency, status)
		VALUES ('00000000-0000-0000-0000-000000001266', 'REG-01', '00000000-0000-0000-0000-0000000000e1',
			(SELECT id FROM locations LIMIT 1), 'USD', 'COMPLETED')`); err != nil {
		t.Fatalf("seed the third sale: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_signatures (sale_id, signer_name, image, image_sha256, total_cents, signed_at, seal)
		VALUES ('00000000-0000-0000-0000-000000001266', 'A Signer', '\x89504e47', $1, 100, NOW(), $1)`, hash); err != nil {
		t.Fatalf("seal the third signature: %v", err)
	}
	if _, err := c.Exec(ctx, `UPDATE pos_signatures SET signer_name = 'Someone Else' WHERE sale_id = '00000000-0000-0000-0000-000000001266'`); err == nil {
		t.Error("a sealed signature accepted an update after the 132 down")
	}
}
