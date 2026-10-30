// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Migration 146 (CP-13 slice O1, ADR 0011): pos_offline_policy,
// pos_register_devices, pos_offline_snapshots, pos_offline_exceptions with its
// partial unique indexes on sale_id and (register_id, device_epoch, device_seq)
// excluding sequence_reused, bad_signature and replay_mismatch, and the
// warning_alert_id column that holds the sold offline past an open warning
// exception (CP-13 OW rides this table); the offline columns and unique index
// on pos_transactions; ABANDONED in the sale status CHECK and wire enum;
// OFFLINE_RATE in the tax_source CHECK on orders and invoices and in
// salesdoc.TaxSource.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/salesdoc"
)

func mig146Files(t *testing.T) (before []string, target, down string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "146_"):
			target = f
		case base < "146_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 146 not found")
	}
	return before, target, "../../migrations/down/146_pos_offline_counter_down.sql"
}

const mig146Present = `
	SELECT
	  (SELECT count(*) FROM information_schema.tables
	   WHERE table_schema = 'public'
	     AND table_name IN ('pos_offline_policy', 'pos_register_devices', 'pos_offline_snapshots', 'pos_offline_exceptions'))
	+ (SELECT count(*) FROM information_schema.columns
	   WHERE table_schema = 'public'
	     AND table_name = 'pos_transactions'
	     AND column_name IN (
	       'device_epoch', 'device_seq', 'offline_ref', 'snapshot_id',
	       'signature_digest', 'offline_session_id', 'replaces_sale_id',
	       'different_customer', 'unknown_cart_id', 'queue_count',
	       'queue_total_cents', 'confirmed_mark'
	     ))
	+ (SELECT count(*) FROM pg_indexes
	   WHERE schemaname = 'public'
	     AND indexname IN (
	       'uq_pos_offline_exceptions_sale_id',
	       'uq_pos_offline_exceptions_register_epoch_seq',
	       'uq_pos_transactions_device_seq',
	       'idx_pos_transactions_replaces_sale',
	       'uq_pos_register_devices_active',
	       'idx_pos_offline_exceptions_warning_alert'
	     ))`

func TestMigration146_OfflineCounterSchemaAndConstraints(t *testing.T) {
	src, ok := salesdoc.ParseTaxSource("offline_rate")
	if !ok || src != salesdoc.TaxSourceOfflineRate {
		t.Fatalf("ParseTaxSource(offline_rate) = (%q, %v), want (%q, true)", src, ok, salesdoc.TaxSourceOfflineRate)
	}
	if b, err := src.MarshalText(); err != nil || string(b) != "offline_rate" {
		t.Fatalf("TaxSourceOfflineRate.MarshalText() = (%q, %v), want (offline_rate, nil)", string(b), err)
	}
	if pos.StatusAbandoned.Status() != "abandoned" {
		t.Fatalf("StatusAbandoned.Status() = %q, want abandoned", pos.StatusAbandoned.Status())
	}

	c, _ := mig105ScratchDB(t)
	before, target, down := mig146Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	ctx := context.Background()

	if n := mig105Scalar[int](t, c, mig146Present); n != 0 {
		t.Fatalf("before 146, %d of its objects already exist", n)
	}
	mig105Apply(t, c, target)
	const wantObjects = 4 + 12 + 6
	if n := mig105Scalar[int](t, c, mig146Present); n != wantObjects {
		t.Fatalf("after 146, %d objects exist, want %d", n, wantObjects)
	}
	// Idempotent second apply.
	mig105Apply(t, c, target)
	if n := mig105Scalar[int](t, c, mig146Present); n != wantObjects {
		t.Fatalf("after second 146 apply, %d objects exist, want %d", n, wantObjects)
	}

	branchID := mig105Scalar[string](t, c, `SELECT id::text FROM locations WHERE type = 'BRANCH' ORDER BY created_at, id LIMIT 1`)
	if _, err := c.Exec(ctx, `UPDATE pos_registers SET branch_id = $1::uuid WHERE id = 'REG-01'`, branchID); err != nil {
		t.Fatal(err)
	}

	// 1. pos_offline_policy checks: only CASH, CHECK, ACCOUNT tenders and non-negative caps.
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_offline_policy (
			branch_id, enabled, tenders, sale_cap_cents, check_cap_cents,
			account_cap_cents, queue_total_cap_cents, max_queued, updated_by
		) VALUES ($1::uuid, TRUE, ARRAY['CASH', 'CHECK', 'ACCOUNT'], 250000, 100000, 500000, 1000000, 25, 'tester')`, branchID); err != nil {
		t.Fatalf("valid pos_offline_policy insert failed: %v", err)
	}
	for _, badSQL := range []string{
		`INSERT INTO pos_offline_policy (branch_id, enabled, tenders, updated_by) VALUES ($1::uuid, TRUE, ARRAY['CARD'], 'tester')`,
		`INSERT INTO pos_offline_policy (branch_id, enabled, tenders, sale_cap_cents, updated_by) VALUES ($1::uuid, TRUE, ARRAY['CASH'], -1, 'tester')`,
		`INSERT INTO pos_offline_policy (branch_id, enabled, tenders, max_queued, updated_by) VALUES ($1::uuid, TRUE, ARRAY['CASH'], -1, 'tester')`,
	} {
		if _, err := c.Exec(ctx, `DELETE FROM pos_offline_policy WHERE branch_id = $1::uuid`, branchID); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(ctx, badSQL, branchID); err == nil {
			t.Errorf("expected CHECK refusal for %s", badSQL)
		}
	}

	// 2. pos_register_devices: one active (unrevoked) device per register, incremented epoch on re-enrol.
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_register_devices (register_id, branch_id, epoch, public_key, enrolled_by)
		VALUES ('REG-01', $1::uuid, 1, 'ed25519-pub-epoch-1', 'mgr-1')`, branchID); err != nil {
		t.Fatalf("enrol epoch 1 failed: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_register_devices (register_id, branch_id, epoch, public_key, enrolled_by)
		VALUES ('REG-01', $1::uuid, 2, 'ed25519-pub-epoch-2', 'mgr-1')`, branchID); err == nil {
		t.Errorf("second active device on REG-01 was admitted while epoch 1 was unrevoked")
	}
	if _, err := c.Exec(ctx, `
		UPDATE pos_register_devices SET revoked_at = NOW(), revoked_by = 'mgr-1'
		WHERE register_id = 'REG-01' AND epoch = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_register_devices (register_id, branch_id, epoch, public_key, enrolled_by)
		VALUES ('REG-01', $1::uuid, 2, 'ed25519-pub-epoch-2', 'mgr-1')`, branchID); err != nil {
		t.Fatalf("enrol epoch 2 after revoking epoch 1 failed: %v", err)
	}

	// 3. pos_offline_snapshots.
	snapID := "00000000-0000-0000-0000-000000000146"
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_offline_snapshots (id, register_id, branch_id, device_epoch, business_date, content, digest, etag)
		VALUES ($1::uuid, 'REG-01', $2::uuid, 2, CURRENT_DATE, '{"snapshot_id":"`+snapID+`"}'::jsonb, 'deadbeef', '"deadbeef"')`,
		snapID, branchID); err != nil {
		t.Fatalf("insert pos_offline_snapshots failed: %v", err)
	}

	// 4. pos_offline_exceptions partial unique indexes:
	//    - unique sale_id and unique (register_id, device_epoch, device_seq) for normal reason codes
	//    - duplicate sale_id and (register_id, device_epoch, device_seq) allowed for
	//      sequence_reused, bad_signature, and replay_mismatch.
	excSale1 := "00000000-0000-0000-0000-00000000e101"
	excSale2 := "00000000-0000-0000-0000-00000000e102"
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_offline_exceptions (
			sale_id, register_id, branch_id, device_epoch, device_seq,
			offline_ref, business_date, snapshot_id, reason_code
		) VALUES ($1::uuid, 'REG-01', $2::uuid, 2, 1, 'OFF-REG-01-2-000001', CURRENT_DATE, $3::uuid, 'credit_over_limit')`,
		excSale1, branchID, snapID); err != nil {
		t.Fatalf("insert first exception failed: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_offline_exceptions (
			sale_id, register_id, branch_id, device_epoch, device_seq,
			offline_ref, business_date, snapshot_id, reason_code
		) VALUES ($1::uuid, 'REG-01', $2::uuid, 2, 2, 'OFF-REG-01-2-000002', CURRENT_DATE, $3::uuid, 'insufficient_stock')`,
		excSale1, branchID, snapID); err == nil {
		t.Errorf("duplicate sale_id with insufficient_stock was admitted")
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_offline_exceptions (
			sale_id, register_id, branch_id, device_epoch, device_seq,
			offline_ref, business_date, snapshot_id, reason_code
		) VALUES ($1::uuid, 'REG-01', $2::uuid, 2, 1, 'OFF-REG-01-2-000001', CURRENT_DATE, $3::uuid, 'insufficient_stock')`,
		excSale2, branchID, snapID); err == nil {
		t.Errorf("duplicate (register_id, device_epoch, device_seq) with insufficient_stock was admitted")
	}
	for _, excludedReason := range []string{"sequence_reused", "bad_signature", "replay_mismatch"} {
		if _, err := c.Exec(ctx, `
			INSERT INTO pos_offline_exceptions (
				sale_id, register_id, branch_id, device_epoch, device_seq,
				offline_ref, business_date, snapshot_id, reason_code
			) VALUES ($1::uuid, 'REG-01', $2::uuid, 2, 1, 'OFF-REG-01-2-000001', CURRENT_DATE, $3::uuid, $4)`,
			excSale1, branchID, snapID, excludedReason); err != nil {
			t.Errorf("partial unique index refused %s on existing sale_id and sequence: %v", excludedReason, err)
		}
	}

	// 4b. The CP-13 OW exception rides this table: a sale sold offline past an
	//     open warning carries the warning's id, and a manager reviews it
	//     (status reviewed) with no second table.
	custID := "00000000-0000-0000-0000-00000000a146"
	if _, err := c.Exec(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1::uuid, 'Offline Warning Co', 'OFFWARN146', $2::uuid)`, custID, branchID); err != nil {
		t.Fatal(err)
	}
	alertID := "00000000-0000-0000-0000-00000000a246"
	if _, err := c.Exec(ctx, `
		INSERT INTO customer_alerts (id, customer_id, kind, text, acknowledge_required, created_by)
		VALUES ($1::uuid, $2::uuid, 'warning', 'account needs a manager''s word before charging', TRUE, 'office-1')`,
		alertID, custID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_offline_exceptions (
			sale_id, register_id, branch_id, device_epoch, device_seq, customer_id,
			warning_alert_id, till_session_id, business_date, reason_code, status, resolved_by, resolved_at
		) VALUES (
			$1::uuid, 'REG-01', $2::uuid, 2, 3, $3::uuid,
			$4::uuid, NULL, CURRENT_DATE, 'sold_past_warning', 'reviewed', 'office-2', NOW())`,
		"00000000-0000-0000-0000-00000000e103", branchID, custID, alertID); err != nil {
		t.Fatalf("insert sold_past_warning exception with warning_alert_id failed: %v", err)
	}
	// A sale can be sold past several open warnings: every id is kept on the row.
	if _, err := c.Exec(ctx, `
		UPDATE pos_offline_exceptions SET warning_alert_ids = jsonb_build_array($2::text, $3::text)
		WHERE sale_id = $1::uuid`,
		"00000000-0000-0000-0000-00000000e103", alertID, "00000000-0000-0000-0000-00000000a247"); err != nil {
		t.Fatalf("warning_alert_ids column missing or not JSONB: %v", err)
	}
	if n := mig105Scalar[int](t, c, `SELECT jsonb_array_length(warning_alert_ids) FROM pos_offline_exceptions WHERE sale_id = '00000000-0000-0000-0000-00000000e103'`); n != 2 {
		t.Errorf("warning_alert_ids holds %d ids, want 2", n)
	}
	if _, err := c.Exec(ctx, `
		UPDATE pos_offline_exceptions SET warning_alert_ids = '{"a":1}'::jsonb WHERE sale_id = $1::uuid`,
		"00000000-0000-0000-0000-00000000e103"); err == nil {
		t.Errorf("warning_alert_ids accepted a JSON object, want an array only")
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_offline_exceptions (
			sale_id, register_id, branch_id, device_epoch, device_seq, business_date, reason_code, warning_alert_id
		) VALUES (
			$1::uuid, 'REG-01', $2::uuid, 2, 4, CURRENT_DATE, 'sold_past_warning', $2::uuid)`,
		"00000000-0000-0000-0000-00000000e104", branchID); err == nil {
		t.Errorf("warning_alert_id accepted a branch id that is no customer alert")
	}

	// 5. pos_transactions: ABANDONED status, offline columns, and unique (register_id, device_epoch, device_seq).
	cartID := "00000000-0000-0000-0000-00000000c101"
	offlineSaleID := "00000000-0000-0000-0000-00000000c102"
	dupSeqSaleID := "00000000-0000-0000-0000-00000000c103"
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency, status)
		VALUES ($1::uuid, 'REG-01', '00000000-0000-0000-0000-0000000000e1', $2::uuid, 'USD', 'ABANDONED')`,
		cartID, branchID); err != nil {
		t.Fatalf("insert ABANDONED sale failed: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_transactions (
			id, register_id, cashier_id, branch_id, currency, status,
			device_epoch, device_seq, offline_ref, snapshot_id, signature_digest,
			replaces_sale_id, different_customer, unknown_cart_id,
			queue_count, queue_total_cents, confirmed_mark
		) VALUES (
			$1::uuid, 'REG-01', '00000000-0000-0000-0000-0000000000e1', $2::uuid, 'USD', 'COMPLETED',
			2, 10, 'OFF-REG-01-2-000010', $3::uuid, 'cafebabe',
			$4::uuid, FALSE, $4::uuid,
			1, 1500, 9
		)`, offlineSaleID, branchID, snapID, cartID); err != nil {
		t.Fatalf("insert offline sale with 146 columns failed: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO pos_transactions (
			id, register_id, cashier_id, branch_id, currency, status,
			device_epoch, device_seq, offline_ref
		) VALUES (
			$1::uuid, 'REG-01', '00000000-0000-0000-0000-0000000000e1', $2::uuid, 'USD', 'COMPLETED',
			2, 10, 'OFF-REG-01-2-000010-DUP'
		)`, dupSeqSaleID, branchID); err == nil {
		t.Errorf("duplicate (register_id, device_epoch, device_seq) on pos_transactions was admitted")
	}

	// 6. OFFLINE_RATE in orders and invoices tax_source CHECK constraints,
	//    and the widening keeps every value 141 allowed (ZONE_RATE).
	orderID := "00000000-0000-0000-0000-00000000b146"
	invID := "00000000-0000-0000-0000-00000000d146"
	zoneOrderID := "00000000-0000-0000-0000-00000000b246"
	if _, err := c.Exec(ctx, `
		INSERT INTO orders (id, customer_id, branch_id, currency, status, delivery_type, tax_source, subtotal, tax_amount, total_amount)
		VALUES ($1::uuid, $3::uuid, $2::uuid, 'USD', 'CONFIRMED', 'PICKUP', 'ZONE_RATE', 10.00, 0.80, 10.80)`,
		zoneOrderID, branchID, custID); err != nil {
		t.Fatalf("insert order with ZONE_RATE tax_source after 146 failed: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO orders (id, customer_id, branch_id, currency, status, delivery_type, tax_source, subtotal, tax_amount, total_amount)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'USD', 'CONFIRMED', 'PICKUP', 'OFFLINE_RATE', 10.00, 0.80, 10.80)`,
		orderID, custID, branchID); err != nil {
		t.Fatalf("insert order with OFFLINE_RATE tax_source failed: %v", err)
	}
	if _, err := c.Exec(ctx, `
		INSERT INTO invoices (id, customer_id, branch_id, currency, status, origin, tax_source, subtotal, tax_amount, total_amount, amount_open, invoice_date)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'USD', 'PAID', 'POS', 'OFFLINE_RATE', 10.00, 0.80, 10.80, 0.00, CURRENT_DATE)`,
		invID, custID, branchID); err != nil {
		t.Fatalf("insert invoice with OFFLINE_RATE tax_source failed: %v", err)
	}

	// 7. Down migration refuses while open exceptions, abandoned sales, or OFFLINE_RATE documents exist,
	//    then succeeds cleanly once cleared and re-applies cleanly.
	downBytes, err := os.ReadFile(down)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, string(downBytes)); err == nil {
		t.Errorf("146 down succeeded while pos_offline_exceptions and OFFLINE_RATE rows existed")
	}
	if _, err := c.Exec(ctx, `DELETE FROM pos_offline_exceptions`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `DELETE FROM pos_transactions WHERE id IN ($1::uuid, $2::uuid)`, offlineSaleID, cartID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `DELETE FROM invoices WHERE id = $1::uuid`, invID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `DELETE FROM orders WHERE id IN ($1::uuid, $2::uuid)`, orderID, zoneOrderID); err != nil {
		t.Fatal(err)
	}
	mig105Apply(t, c, down)
	if n := mig105Scalar[int](t, c, mig146Present); n != 0 {
		t.Fatalf("after 146 down, %d objects remain, want 0", n)
	}
	mig105Apply(t, c, target)
	if n := mig105Scalar[int](t, c, mig146Present); n != wantObjects {
		t.Fatalf("after re-applying 146 up, %d objects exist, want %d", n, wantObjects)
	}
}
