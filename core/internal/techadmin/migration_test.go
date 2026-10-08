// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin_test

// Migration 095 on rows that exist (recipe step 3): the schema up to 094 is
// built in a scratch database of its own, legacy rows are written in the
// shape the base commit left them, then 095 is applied and each backfill is
// read back. The down file reverses it and a second up changes nothing.

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

func scratchDB(t *testing.T) (*pgx.Conn, *[]string) {
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
	name := "gv1_c51amig_" + hex.EncodeToString(suffix)
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

func migrationFiles(t *testing.T) (before []string, target string) {
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
		case strings.HasPrefix(base, "095_"):
			target = f
		case base < "095_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 095 not found")
	}
	return before, target
}

func applyMigration(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func scalar095[T any](t *testing.T, conn *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func TestMigration095_BackfillsRowsThatExist(t *testing.T) {
	conn, _ := scratchDB(t)
	before, target := migrationFiles(t)
	for _, f := range before {
		applyMigration(t, conn, f)
	}
	ctx := context.Background()

	// Legacy rows in the base commit's shape: rfcs with no created_at (the
	// column defaulted but a raw insert could skip it), staff, and an api key
	// with no created_at.
	if _, err := conn.Exec(ctx, `
		INSERT INTO rfcs (id, title, status, problem_statement, proposed_solution, content, created_at, updated_at) VALUES
		 ('00000000-0000-0000-0000-0000000000a1','First RFC','draft','p1','s1','c1', now() - interval '2 hours', now() - interval '2 hours'),
		 ('00000000-0000-0000-0000-0000000000a2','Second RFC','approved','p2','s2', NULL, NULL, NULL);
		INSERT INTO staff (id, email, full_name, staff_no, role, active) VALUES
		 ('00000000-0000-0000-0000-0000000000b1','dana@gable.com','Dana Ramirez','STF-101','dispatcher',TRUE);
		INSERT INTO api_keys (id, name, key_hash, key_prefix, scopes, created_at) VALUES
		 ('00000000-0000-0000-0000-0000000000c1','legacy','h','sk_live_xx','{quotes:read}', NULL);`); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}

	applyMigration(t, conn, target)

	// 1. Timestamps filled and NOT NULL, revisions at 1.
	if n := scalar095[int](t, conn, `SELECT count(*) FROM rfcs WHERE created_at IS NULL OR updated_at IS NULL OR revision <> 1`); n != 0 {
		t.Errorf("%d rfcs with a NULL timestamp or a revision other than 1", n)
	}
	if n := scalar095[int](t, conn, `SELECT count(*) FROM staff WHERE revision <> 1`); n != 0 {
		t.Errorf("%d staff rows with a revision other than 1", n)
		if n := scalar095[int](t, conn, `SELECT count(*) FROM api_keys WHERE created_at IS NULL`); n != 0 {
			t.Errorf("%d api keys with a NULL created_at", n)
		}
	}

	// 2. Numbers: the older RFC carries the lower number, the newer the next;
	// both unique, both NOT NULL, both RFC- padded.
	first := scalar095[string](t, conn, `SELECT number FROM rfcs WHERE id = '00000000-0000-0000-0000-0000000000a1'`)
	second := scalar095[string](t, conn, `SELECT number FROM rfcs WHERE id = '00000000-0000-0000-0000-0000000000a2'`)
	if first != "RFC-000001" || second != "RFC-000002" {
		t.Errorf("numbers = %s, %s; want RFC-000001 and RFC-000002 in (created_at, id) order", first, second)
	}
	// A raw insert (the seed's shape) numbers itself through the DEFAULT.
	if _, err := conn.Exec(ctx, `INSERT INTO rfcs (title, status, problem_statement, proposed_solution) VALUES ('Raw RFC','draft','p','s')`); err != nil {
		t.Fatalf("a raw rfc insert: %v", err)
	}
	if got := scalar095[string](t, conn, `SELECT number FROM rfcs WHERE title = 'Raw RFC'`); got != "RFC-000003" {
		t.Errorf("a raw insert numbered itself %s, want RFC-000003 (the sequence past the backfill)", got)
	}

	// 5. The revision anchors table exists and starts empty (revision 1 is
	// the missing row).
	if n := scalar095[int](t, conn, `SELECT count(*) FROM admin_revisions`); n != 0 {
		t.Errorf("%d anchor rows before any settings write", n)
	}

	// 6. The keyset indexes exist.
	for _, idx := range []string{"idx_rfcs_created_id", "idx_staff_created_id", "idx_api_keys_created_id"} {
		if n := scalar095[int](t, conn, `SELECT count(*) FROM pg_indexes WHERE indexname = $1`, idx); n != 1 {
			t.Errorf("index %s: %d rows in pg_indexes", idx, n)
		}
	}

	// Idempotence: a second apply changes nothing.
	numbers := scalar095[int](t, conn, `SELECT count(*) FROM rfcs`)
	applyMigration(t, conn, target)
	if again := scalar095[int](t, conn, `SELECT count(*) FROM rfcs WHERE number IS NULL OR number NOT LIKE 'RFC-%'`); again != 0 {
		t.Errorf("a second apply renumbered or dropped numbers on %d rows", again)
	}
	if again := scalar095[int](t, conn, `SELECT count(*) FROM rfcs`); again != numbers {
		t.Errorf("a second apply changed the row count: %d then %d", numbers, again)
	}

	// The down file reverses it, and a fresh up after the down is clean.
	down, err := os.ReadFile("../../migrations/down/095_admin_wire_contract_down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := scalar095[int](t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'rfcs' AND column_name IN ('number', 'revision')`); n != 0 {
		t.Errorf("%d of the rfcs columns survived the down file", n)
	}
	if n := scalar095[int](t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'staff' AND column_name = 'revision'`); n != 0 {
		t.Error("staff.revision survived the down file")
	}
	if n := scalar095[int](t, conn, `SELECT count(*) FROM pg_tables WHERE tablename = 'admin_revisions'`); n != 0 {
		t.Error("admin_revisions survived the down file")
	}
	applyMigration(t, conn, target)
	if got := scalar095[string](t, conn, `SELECT number FROM rfcs WHERE id = '00000000-0000-0000-0000-0000000000a1'`); got != "RFC-000001" {
		t.Errorf("after down then up the first RFC is %s, want RFC-000001 again", got)
	}
}
