// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory_test

// Migration 098 on rows that exist (the recipe's migration step): the schema
// up to 097 is built in a scratch database of its own, legacy inventory rows
// are written in the shape the base commit left them (no created_at, a
// nullable updated_at), then 098 is applied and each backfill is read back.
// The down drops what the up added; the up applies again (idempotence on a
// fresh round trip).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/jackc/pgx/v5"
)

func scratchDB(t *testing.T) *pgx.Conn {
	t.Helper()
	db := testutil.RequireDB(t)
	base, err := url.Parse(db.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base.String())
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "gv1_c31bmig_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		admin.Close(ctx)
		t.Skipf("cannot create a scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name))
		admin.Close(context.Background())
	})

	scratch := *base
	scratch.Path = "/" + name
	conn, err := pgx.Connect(ctx, scratch.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func migration098Files(t *testing.T) (before []string, target, down string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "098_"):
			target = f
		case base < "098_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 098 not found")
	}
	down = "../../migrations/down/098_inventory_read_contract_down.sql"
	if _, err := os.Stat(down); err != nil {
		t.Fatal("migration 098 down not found")
	}
	return before, target, down
}

func applyFile(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func TestMigration098_BackfillsRowsThatExist(t *testing.T) {
	conn := scratchDB(t)
	before, target, downFile := migration098Files(t)
	for _, f := range before {
		applyFile(t, conn, f)
	}
	ctx := context.Background()

	// Legacy rows in the base commit's shape: no created_at exists, updated_at
	// nullable. One row names its updated_at, one leaves it NULL, one carries
	// the deprecated location text only.
	product := "3f0d6b6e-1111-4e6a-9b7d-000000000001"
	if _, err := conn.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, 'M098-1', 'migration 098', 'EA', 1)`, product); err != nil {
		t.Fatal(err)
	}
	var setAt, nullAt string
	if err := conn.QueryRow(ctx, `INSERT INTO inventory (product_id, location, quantity, allocated, updated_at)
		VALUES ($1, 'm098-set', 10, 2, '2026-01-06T10:00:00Z') RETURNING id::text, updated_at::text`, product).Scan(&setAt, &nullAt); err != nil {
		t.Fatal(err)
	}
	var unsetID string
	if err := conn.QueryRow(ctx, `INSERT INTO inventory (product_id, location, quantity, updated_at)
		VALUES ($1, 'm098-null', 5, NULL) RETURNING id::text`, product).Scan(&unsetID); err != nil {
		t.Fatal(err)
	}

	applyFile(t, conn, target)

	// created_at is NOT NULL and takes the row's own updated_at where it had
	// one; a NULL updated_at is filled (with NOW()) and created_at follows.
	var count int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM inventory WHERE created_at IS NULL OR updated_at IS NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d rows still hold a NULL created_at or updated_at", count)
	}
	var gotCreated string
	if err := conn.QueryRow(ctx, `SELECT created_at::text FROM inventory WHERE id = $1`, setAt).Scan(&gotCreated); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotCreated, "2026-01-06 10:00:00") {
		t.Fatalf("the row's created_at = %q, want its updated_at", gotCreated)
	}
	var filledUpdated time.Time
	if err := conn.QueryRow(ctx, `SELECT updated_at FROM inventory WHERE id = $1`, unsetID).Scan(&filledUpdated); err != nil {
		t.Fatal(err)
	}
	if filledUpdated.IsZero() || time.Since(filledUpdated) > time.Hour {
		t.Fatalf("the NULL updated_at was not filled: %v", filledUpdated)
	}
	// The keyset index exists.
	var idx int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM pg_indexes WHERE indexname = 'idx_inventory_created_at_id'`).Scan(&idx); err != nil {
		t.Fatal(err)
	}
	if idx != 1 {
		t.Fatal("the keyset index idx_inventory_created_at_id is missing")
	}

	// The down drops the column the up added and the index beside it; the up
	// applies again on a fresh round trip.
	applyFile(t, conn, downFile)
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_name = 'inventory' AND column_name = 'created_at'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("the down left the created_at column behind")
	}
	applyFile(t, conn, target)
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM inventory WHERE created_at IS NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("the second up left %d rows without created_at", count)
	}
}
