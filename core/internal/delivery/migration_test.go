// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery_test

// Migration 104 on rows that exist: a legacy row whose route or stop status
// was never set has a NULL on that column, and every reader the new wire
// contract carries treats the status as the start state (model.go: DRAFT for
// a route, PENDING for a stop) and reads it into a non-null value. The
// backfill the migration does is the only thing that keeps such a row from
// taking the read path down with a NULL-to-string scan and a 500. The cycle 5
// migration case: the up file rewrites the NULL, the read returns a status,
// and the down file drops the NOT NULL only (the start-state fill stays).

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

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func scratchDB104(t *testing.T) (*pgx.Conn, *[]string) {
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
		admin.Close(ctx)
		t.Skipf("cannot create a scratch database: %v", err)
	}
	name := "gv1_c51d104_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		admin.Close(ctx)
		t.Skipf("cannot create a scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name))
		admin.Close(ctx)
	})

	scratch := *base
	scratch.Path = "/" + name
	cfg, err := pgx.ParseConfig(scratch.String())
	if err != nil {
		t.Fatal(err)
	}
	var notices []string
	cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) { notices = append(notices, n.Message) }
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn, &notices
}

func migration104Files(t *testing.T) (before []string, target string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasSuffix(base, "_down.sql"):
		case strings.HasPrefix(base, "104_"):
			target = f
		case base < "104_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 104 not found")
	}
	return before, target
}

func apply104(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func scalar104[T any](t *testing.T, conn *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func seed104LegacyRows(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	branch := `(SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1)`
	sql := fmt.Sprintf(`
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ('00000000-0000-0000-0000-0000000104c1', 'Delivery mig co', 'M104-DLV', %[1]s);
		INSERT INTO customer_branches (customer_id, branch_id)
		VALUES ('00000000-0000-0000-0000-0000000104c1', %[1]s);
		INSERT INTO orders (id, customer_id, status, total_amount, branch_id, currency, delivery_type, number)
		VALUES ('00000000-0000-0000-0000-0000000104a1',
			'00000000-0000-0000-0000-0000000104c1', 'CONFIRMED', 10, %[1]s, 'USD', 'DELIVERY', 'SO-M104-A1');
		INSERT INTO vehicles (id, name, vehicle_type, license_plate)
		VALUES ('00000000-0000-0000-0000-0000000104b1', 'Migration van', 'VAN', 'M104-V1');
		INSERT INTO drivers (id, name, license_number)
		VALUES ('00000000-0000-0000-0000-0000000104c2', 'Migration driver', 'M104-D1');
		INSERT INTO delivery_routes (id, vehicle_id, driver_id, scheduled_date, status)
		VALUES ('00000000-0000-0000-0000-0000000104d1',
			'00000000-0000-0000-0000-0000000104b1',
			'00000000-0000-0000-0000-0000000104c2',
			CURRENT_DATE, NULL);
		INSERT INTO deliveries (id, route_id, order_id, stop_sequence, status)
		VALUES ('00000000-0000-0000-0000-0000000104e1',
			'00000000-0000-0000-0000-0000000104d1',
			'00000000-0000-0000-0000-0000000104a1',
			1, NULL);
	`, branch)
	if _, err := conn.Exec(context.Background(), sql); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}
}

// RULE (PR 70 review round 2 P2-2): migration 104 backfills a NULL route
// status and a NULL stop status with the values the model and the scans
// treat as the start state, then sets both NOT NULL. The test inserts the
// legacy shape the schema before 104 allows, applies 104, and reads each
// row back: the route carries DRAFT, the stop carries PENDING, the two
// columns are NOT NULL in the catalog, and a forced NULL on insert is
// rejected. The down file drops the NOT NULL only; the start-state fill
// stays.
func TestMigration104_BackfillsNullStatusAndSetsNotNull(t *testing.T) {
	conn, _ := scratchDB104(t)
	before, target := migration104Files(t)
	for _, f := range before {
		apply104(t, conn, f)
	}
	seed104LegacyRows(t, conn)

	if got := scalar104[string](t, conn, `SELECT COALESCE(status, '<null>') FROM delivery_routes WHERE id = $1`, "00000000-0000-0000-0000-0000000104d1"); got != "<null>" {
		t.Fatalf("pre-condition: legacy route status = %s, want NULL", got)
	}
	if got := scalar104[string](t, conn, `SELECT COALESCE(status, '<null>') FROM deliveries WHERE id = $1`, "00000000-0000-0000-0000-0000000104e1"); got != "<null>" {
		t.Fatalf("pre-condition: legacy stop status = %s, want NULL", got)
	}

	apply104(t, conn, target)

	if got := scalar104[string](t, conn, `SELECT status FROM delivery_routes WHERE id = $1`, "00000000-0000-0000-0000-0000000104d1"); got != "DRAFT" {
		t.Errorf("route status after migration = %s, want DRAFT", got)
	}
	if got := scalar104[string](t, conn, `SELECT status FROM deliveries WHERE id = $1`, "00000000-0000-0000-0000-0000000104e1"); got != "PENDING" {
		t.Errorf("stop status after migration = %s, want PENDING", got)
	}

	// The column is NOT NULL: a forced NULL on insert is rejected.
	if _, err := conn.Exec(context.Background(),
		`INSERT INTO delivery_routes (id, vehicle_id, driver_id, scheduled_date, status) VALUES ('00000000-0000-0000-0000-0000000104d2', '00000000-0000-0000-0000-0000000104b1', '00000000-0000-0000-0000-0000000104c2', CURRENT_DATE, NULL)`); err == nil {
		t.Errorf("delivery_routes accepted a NULL status; NOT NULL was not set")
	}
	if _, err := conn.Exec(context.Background(),
		`INSERT INTO deliveries (id, route_id, order_id, stop_sequence, status) VALUES ('00000000-0000-0000-0000-0000000104e2', '00000000-0000-0000-0000-0000000104d1', '00000000-0000-0000-0000-0000000104a1', 2, NULL)`); err == nil {
		t.Errorf("deliveries accepted a NULL status; NOT NULL was not set")
	}

	down, err := os.ReadFile("../../migrations/down/104_delivery_wire_contract_down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	if got := scalar104[string](t, conn, `SELECT status FROM delivery_routes WHERE id = $1`, "00000000-0000-0000-0000-0000000104d1"); got != "DRAFT" {
		t.Errorf("down rolled the backfill back to %s; the filled value must stay", got)
	}
	if _, err := conn.Exec(context.Background(),
		`INSERT INTO delivery_routes (id, vehicle_id, driver_id, scheduled_date, status) VALUES ('00000000-0000-0000-0000-0000000104d3', '00000000-0000-0000-0000-0000000104b1', '00000000-0000-0000-0000-0000000104c2', CURRENT_DATE, NULL)`); err != nil {
		t.Errorf("after down a NULL status must be accepted again: %v", err)
	}
}