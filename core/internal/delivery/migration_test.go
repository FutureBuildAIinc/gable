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

// RULE (PR 70 review round 5 P2-1, lead decision on method): the up
// migration normalises a legacy stored status to the UPPERCASE vocabulary
// the model and scans carry (UPPER(BTRIM(status))) and maps a stored value
// outside the vocabulary to a TERMINAL non billing state of each table:
// a route becomes CANCELLED and a stop becomes FAILED (the values
// TransitionRoute and TransitionStop can never dispatch, deliver, fulfil
// or bill from). The known synonyms are mapped explicitly so a value the
// legacy spellings used (a lowercase or spaced form of a known value, a
// stored COMPLETE route, a stored IN_PROGRESS route, a stored CANCELED
// route, a stored DELIVERED with stray whitespace, a stored CANCELLED
// stop) lands on the right value. Each row the migration rewrites is
// named in a RAISE NOTICE (the table, the id, the old value, the new
// value). The mapping never sends a row to DRAFT or PENDING; DRAFT and
// PENDING are live states and a legacy row of unknown spelling could
// have been a finished route or a delivered stop, so the migration
// refuses to revive them. The test inserts one of each shape the legacy
// schema allows, applies migration 104, reads back, and asserts the
// known synonyms land on their canonical values, the unknown values
// land on the terminal non billing values of their tables, the notices
// name every rewritten row, and no rewritten row ended on DRAFT or
// PENDING (the unsafe mapping the prior round shipped).
func TestMigration104_NormalisesLowercaseAndUnknownStatuses(t *testing.T) {
	conn, notices := scratchDB104(t)
	before, target := migration104Files(t)
	for _, f := range before {
		apply104(t, conn, f)
	}
	branchSQL := `(SELECT id FROM locations WHERE code = 'NORM-104' LIMIT 1)`
	seedBase := fmt.Sprintf(`
		INSERT INTO locations (id, type, code, name)
		VALUES ('00000000-0000-4000-8000-000000000104', 'BRANCH', 'NORM-104', 'Normalisation branch');
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ('00000000-0000-4000-8000-0000000104cc', 'Norm co', 'NORM-104', %[1]s);
		INSERT INTO customer_branches (customer_id, branch_id)
		VALUES ('00000000-0000-4000-8000-0000000104cc', %[1]s);
		INSERT INTO orders (id, customer_id, status, total_amount, branch_id, currency, delivery_type, number)
		VALUES ('00000000-0000-4000-8000-00000001040a',
			'00000000-0000-4000-8000-0000000104cc', 'CONFIRMED', 10, %[1]s, 'USD', 'DELIVERY', 'SO-NORM-A');
		INSERT INTO vehicles (id, name, vehicle_type, license_plate)
		VALUES ('00000000-0000-4000-8000-00000001040b', 'Norm van', 'VAN', 'NORM-V');
		INSERT INTO drivers (id, name, license_number)
		VALUES ('00000000-0000-4000-8000-00000001040c', 'Norm driver', 'NORM-D');
	`, branchSQL)
	if _, err := conn.Exec(context.Background(), seedBase); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	// Lowercase known forms, foreign spellings the legacy vocabulary used,
	// whitespace-padded values and nonsense values the migration must
	// classify safely. Routes: lowercase 'in_transit', foreign 'IN_PROGRESS',
	// stored 'COMPLETE' (no D), foreign 'CANCELED' (one L), whitespace
	// ' DRAFT ', nonsense 'weird_legacy'. Stops: lowercase 'delivered',
	// whitespace 'DELIVERED ' (trailing space), nonsense 'finished?',
	// foreign 'CANCELLED' (has no home in the stop vocabulary). Every
	// row is rewritten by the migration.
	legacyRows := `
		INSERT INTO delivery_routes (id, vehicle_id, driver_id, scheduled_date, status) VALUES
			('00000000-0000-4000-8000-0000000104dd', '00000000-0000-4000-8000-00000001040b', '00000000-0000-4000-8000-00000001040c', CURRENT_DATE, 'in_transit'),
			('00000000-0000-4000-8000-0000000104de', '00000000-0000-4000-8000-00000001040b', '00000000-0000-4000-8000-00000001040c', CURRENT_DATE, 'IN_PROGRESS'),
			('00000000-0000-4000-8000-0000000104d1', '00000000-0000-4000-8000-00000001040b', '00000000-0000-4000-8000-00000001040c', CURRENT_DATE, 'COMPLETE'),
			('00000000-0000-4000-8000-0000000104d2', '00000000-0000-4000-8000-00000001040b', '00000000-0000-4000-8000-00000001040c', CURRENT_DATE, 'CANCELED'),
			('00000000-0000-4000-8000-0000000104d3', '00000000-0000-4000-8000-00000001040b', '00000000-0000-4000-8000-00000001040c', CURRENT_DATE, ' DRAFT '),
			('00000000-0000-4000-8000-0000000104d4', '00000000-0000-4000-8000-00000001040b', '00000000-0000-4000-8000-00000001040c', CURRENT_DATE, 'weird_legacy');
		INSERT INTO deliveries (id, route_id, order_id, stop_sequence, status) VALUES
			('00000000-0000-4000-8000-0000000104df', '00000000-0000-4000-8000-0000000104dd', '00000000-0000-4000-8000-00000001040a', 1, 'delivered'),
			('00000000-0000-4000-8000-0000000104e1', '00000000-0000-4000-8000-0000000104dd', '00000000-0000-4000-8000-00000001040a', 2, 'DELIVERED '),
			('00000000-0000-4000-8000-0000000104e2', '00000000-0000-4000-8000-0000000104dd', '00000000-0000-4000-8000-00000001040a', 3, 'finished?'),
			('00000000-0000-4000-8000-0000000104e3', '00000000-0000-4000-8000-0000000104dd', '00000000-0000-4000-8000-00000001040a', 4, 'CANCELLED');
	`
	if _, err := conn.Exec(context.Background(), legacyRows); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}

	apply104(t, conn, target)

	// Route: known synonyms land on their canonical values.
	if got := scalar104[string](t, conn, `SELECT status FROM delivery_routes WHERE id = $1`, "00000000-0000-4000-8000-0000000104dd"); got != "IN_TRANSIT" {
		t.Errorf("lowercase route status after migration = %s, want IN_TRANSIT", got)
	}
	if got := scalar104[string](t, conn, `SELECT status FROM delivery_routes WHERE id = $1`, "00000000-0000-4000-8000-0000000104de"); got != "IN_TRANSIT" {
		t.Errorf("IN_PROGRESS route status after migration = %s, want IN_TRANSIT", got)
	}
	if got := scalar104[string](t, conn, `SELECT status FROM delivery_routes WHERE id = $1`, "00000000-0000-4000-8000-0000000104d1"); got != "COMPLETED" {
		t.Errorf("COMPLETE route status after migration = %s, want COMPLETED (not DRAFT)", got)
	}
	if got := scalar104[string](t, conn, `SELECT status FROM delivery_routes WHERE id = $1`, "00000000-0000-4000-8000-0000000104d2"); got != "CANCELLED" {
		t.Errorf("CANCELED route status after migration = %s, want CANCELLED (not DRAFT)", got)
	}
	if got := scalar104[string](t, conn, `SELECT status FROM delivery_routes WHERE id = $1`, "00000000-0000-4000-8000-0000000104d3"); got != "DRAFT" {
		t.Errorf("whitespace-padded route status after migration = %s, want DRAFT (trim then known)", got)
	}
	// Route: nonsense value lands on the terminal non billing value
	// CANCELLED, never DRAFT (a DRAFT route is dispatchable).
	if got := scalar104[string](t, conn, `SELECT status FROM delivery_routes WHERE id = $1`, "00000000-0000-4000-8000-0000000104d4"); got != "CANCELLED" {
		t.Errorf("nonsense route status after migration = %s, want CANCELLED (not DRAFT)", got)
	}
	// Stop: known synonyms land on their canonical values.
	if got := scalar104[string](t, conn, `SELECT status FROM deliveries WHERE id = $1`, "00000000-0000-4000-8000-0000000104df"); got != "DELIVERED" {
		t.Errorf("lowercase stop status after migration = %s, want DELIVERED", got)
	}
	if got := scalar104[string](t, conn, `SELECT status FROM deliveries WHERE id = $1`, "00000000-0000-4000-8000-0000000104e1"); got != "DELIVERED" {
		t.Errorf("whitespace-padded stop status after migration = %s, want DELIVERED", got)
	}
	// Stop: nonsense and foreign values land on the terminal non billing
	// value FAILED, never PENDING (a PENDING stop can be delivered again
	// and re queue fulfilment and re bill).
	if got := scalar104[string](t, conn, `SELECT status FROM deliveries WHERE id = $1`, "00000000-0000-4000-8000-0000000104e2"); got != "FAILED" {
		t.Errorf("nonsense stop status after migration = %s, want FAILED (not PENDING)", got)
	}
	if got := scalar104[string](t, conn, `SELECT status FROM deliveries WHERE id = $1`, "00000000-0000-4000-8000-0000000104e3"); got != "FAILED" {
		t.Errorf("CANCELLED stop status after migration = %s, want FAILED (not PENDING)", got)
	}

	// No rewritten row ended on DRAFT or PENDING (the unsafe mapping the
	// prior round shipped; the brief: a route or a stop of unknown
	// spelling could have been a finished route or a delivered stop, so
	// the migration refuses to revive them onto the two most live
	// states). The unknown-status rows above are exactly the rows the
	// unsafe mapping would have revived.
	var draft, pending int
	if err := conn.QueryRow(context.Background(),
		`SELECT count(*) FROM delivery_routes WHERE id IN ('00000000-0000-4000-8000-0000000104d1','00000000-0000-4000-8000-0000000104d2','00000000-0000-4000-8000-0000000104d4') AND status = 'DRAFT'`).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	if draft != 0 {
		t.Errorf("%d rewritten route rows landed on DRAFT, want 0 (the unsafe mapping)", draft)
	}
	if err := conn.QueryRow(context.Background(),
		`SELECT count(*) FROM deliveries WHERE id IN ('00000000-0000-4000-8000-0000000104e2','00000000-0000-4000-8000-0000000104e3') AND status = 'PENDING'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Errorf("%d rewritten stop rows landed on PENDING, want 0 (the unsafe mapping)", pending)
	}

	// Every rewritten row is named in a RAISE NOTICE: the table, the id,
	// the old value and the new value. The notice text the migration
	// emits is the operator's audit trail for the rewrite.
	wantNotices := []struct{ table, id, old, neu string }{
		{"delivery_routes", "00000000-0000-4000-8000-0000000104de", "IN_PROGRESS", "IN_TRANSIT"},
		{"delivery_routes", "00000000-0000-4000-8000-0000000104d1", "COMPLETE", "COMPLETED"},
		{"delivery_routes", "00000000-0000-4000-8000-0000000104d2", "CANCELED", "CANCELLED"},
		{"delivery_routes", "00000000-0000-4000-8000-0000000104d4", "weird_legacy", "CANCELLED"},
		{"deliveries", "00000000-0000-4000-8000-0000000104e2", "finished?", "FAILED"},
		{"deliveries", "00000000-0000-4000-8000-0000000104e3", "CANCELLED", "FAILED"},
	}
	for _, w := range wantNotices {
		var found bool
		for _, line := range *notices {
			if strings.Contains(line, w.table) && strings.Contains(line, w.id) &&
				strings.Contains(line, w.old) && strings.Contains(line, w.neu) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no NOTICE named %s id=%s old=%s new=%s; got %d notices", w.table, w.id, w.old, w.neu, len(*notices))
		}
	}
}
