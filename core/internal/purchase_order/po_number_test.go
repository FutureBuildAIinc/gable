// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

// Purchase order document numbers (PO-NUMBER): PO-000001 from a sequence, the
// quote and order migrations' shape. A created order carries the number from
// the column default, the list and the by id read return it, and migration
// 116 numbers rows that already exist in (created_at, id) order.

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var poNumberShape = regexp.MustCompile(`^PO-[0-9]{6,}$`)

// RULE: a created purchase order answers with PO- and at least six digits,
// two creates answer increasing numbers, and the by id and list reads carry
// the same number.
func TestCreatePO_CarriesAnIncreasingNumber(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := branchctx.WithSystem(context.Background())

	vendor := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, "po-num-"+vendor.String()[:6]); err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, id)
		}
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
	})

	svc := purchase_order.NewService(purchase_order.NewRepository(db), db, nil, nil, nil, nil)
	first, err := svc.CreateManualPOFromHandler(ctx, vendor, nil, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ids = append(ids, first.ID)
	second, err := svc.CreateManualPOFromHandler(ctx, vendor, nil, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ids = append(ids, second.ID)

	if !poNumberShape.MatchString(first.Number) {
		t.Errorf("created number = %q, want PO- and at least six digits", first.Number)
	}
	if !(second.Number > first.Number) {
		t.Errorf("numbers %q then %q, want increasing", first.Number, second.Number)
	}

	got, err := svc.GetPO(ctx, first.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Number != first.Number {
		t.Errorf("by id number = %q, want %q", got.Number, first.Number)
	}
	list, err := svc.ListPOs(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, po := range list {
		if po.ID == second.ID {
			found = true
			if po.Number != second.Number {
				t.Errorf("list number = %q, want %q", po.Number, second.Number)
			}
		}
	}
	if !found {
		t.Errorf("created order missing from the list")
	}
}

// RULE: migration 116 numbers existing rows in (created_at, id) order, sets
// the sequence past the highest, and a second apply changes nothing. Run in a
// scratch schema holding only the one table the file touches.
func TestMigration116_NumbersExistingRowsInCreationOrder(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	sqlBytes, err := os.ReadFile("../../migrations/116_purchase_order_numbers.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	conn, err := pgx.Connect(ctx, db.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	schema := "po116_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := conn.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`) }()
	if _, err := conn.Exec(ctx, `SET search_path TO `+schema); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE purchase_orders (id UUID PRIMARY KEY, created_at TIMESTAMPTZ NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	older, newer := uuid.MustParse("ffffffff-0000-0000-0000-000000000001"), uuid.MustParse("00000000-0000-0000-0000-000000000002")
	base := time.Now().Add(-48 * time.Hour)
	// The newer id sorts first by id, the older created_at must still win.
	for id, at := range map[uuid.UUID]time.Time{newer: base.Add(time.Hour), older: base} {
		if _, err := conn.Exec(ctx, `INSERT INTO purchase_orders (id, created_at) VALUES ($1, $2)`, id, at); err != nil {
			t.Fatal(err)
		}
	}

	apply := func() {
		t.Helper()
		if _, err := conn.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply 116: %v", err)
		}
	}
	apply()
	apply() // idempotent

	number := func(id uuid.UUID) string {
		var n string
		if err := conn.QueryRow(ctx, `SELECT number FROM purchase_orders WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := number(older); n != "PO-000001" {
		t.Errorf("older row number = %q, want PO-000001", n)
	}
	if n := number(newer); n != "PO-000002" {
		t.Errorf("newer row number = %q, want PO-000002", n)
	}
	var next string
	if err := conn.QueryRow(ctx, `SELECT purchase_order_next_number()`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != "PO-000003" {
		t.Errorf("next number = %q, want PO-000003", next)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO purchase_orders (id, created_at) VALUES ($1, NOW())`, uuid.New()); err != nil {
		t.Errorf("insert relying on the column default: %v", err)
	}
}
