// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm_test

// Migration 102 on rows that exist (the recipe's migration step, C5-1b P3-1
// follow up): updated_at is read into a non-nullable Go time.Time by every
// repository in crm, project and millwork, but the column is nullable on all
// three tables, so a single row with updated_at = NULL takes the whole list
// down. 102 backfills a NULL updated_at to created_at (created_at is already
// NOT NULL by 096) and sets the column NOT NULL, so a later raw insert cannot
// reintroduce the 500. The down restores the nullable shape; a re-apply works.

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

func scratchDB102(t *testing.T) *pgx.Conn {
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
	name := "gv1_c51bmig102_" + hex.EncodeToString(suffix)
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

func migration102Files(t *testing.T) (before []string, target, downFile string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "102_"):
			target = f
		case base < "102_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 102 not found")
	}
	downFile = "../../migrations/down/102_crm_projects_millwork_updated_at_down.sql"
	if _, err := os.Stat(downFile); err != nil {
		t.Fatal("migration 102 down not found")
	}
	return before, target, downFile
}

func apply102(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func scalar102[T any](t *testing.T, conn *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func TestMigration102_BackfillsUpdatedAt(t *testing.T) {
	conn := scratchDB102(t)
	before, target, downFile := migration102Files(t)
	for _, f := range before {
		apply102(t, conn, f)
	}
	ctx := context.Background()

	// Legacy rows: one row per table with updated_at = NULL, the read's
	// failure shape (a NULL would be scanned into a non-null time.Time and
	// 500 the whole list).
	if _, err := conn.Exec(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ('00000000-0000-0000-0000-00000000e102', 'Mig UA', 'MIG-UA-1',
			(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'));
		INSERT INTO crm_activities (id, customer_id, contact_id, activity_type, description, logged_by, activity_date, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-00000000a301','00000000-0000-0000-0000-00000000e102',NULL,'CALL','null updated_at',NULL, now() - interval '2 days', now() - interval '2 days', NULL);
		INSERT INTO projects (id, customer_id, name, status, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-00000000b301','00000000-0000-0000-0000-00000000e102','Null Update Job','Active', now() - interval '2 days', NULL);
		INSERT INTO millwork_options (id, category, name, price_adjustment, attributes, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-00000000c301','door_type','Null Update',0,'{}'::jsonb, now() - interval '2 days', NULL)`); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}

	// Before 102: each updated_at column is nullable, and scanning a NULL into
	// a non-null time.Time is the read's failure (a single row with NULL
	// updated_at would 500 the whole list, the class P3-1 names).
	cases := []struct{ table, id string }{
		{"crm_activities", "00000000-0000-0000-0000-00000000a301"},
		{"projects", "00000000-0000-0000-0000-00000000b301"},
		{"millwork_options", "00000000-0000-0000-0000-00000000c301"},
	}
	for _, c := range cases {
		if v := scalar102[string](t, conn, `SELECT is_nullable FROM information_schema.columns WHERE table_name = $1 AND column_name = 'updated_at'`, c.table); v != "YES" {
			t.Errorf("%s.updated_at is_nullable = %s before 102, want YES", c.table, v)
		}
		var updated time.Time
		if err := conn.QueryRow(ctx, fmt.Sprintf(`SELECT updated_at FROM %s WHERE id = $1`, c.table), c.id).Scan(&updated); err == nil {
			t.Errorf("%s id %s scanned a NULL updated_at into a time.Time, want the read to fail", c.table, c.id)
		}
	}

	// Apply 102.
	apply102(t, conn, target)

	// After 102: each row's updated_at is its created_at (the column's last
	// non-null value, the anchor the row itself carries), and the column is
	// NOT NULL, so a later raw insert cannot reintroduce the 500.
	for _, c := range cases {
		if !scalar102[bool](t, conn, `SELECT updated_at = created_at FROM `+c.table+` WHERE id = $1`, c.id) {
			t.Errorf("%s id %s: updated_at does not equal created_at after 102", c.table, c.id)
		}
		if v := scalar102[string](t, conn, `SELECT is_nullable FROM information_schema.columns WHERE table_name = $1 AND column_name = 'updated_at'`, c.table); v != "NO" {
			t.Errorf("%s.updated_at is_nullable = %s after 102, want NO", c.table, v)
		}
	}
	if _, err := conn.Exec(ctx, `UPDATE crm_activities SET updated_at = NULL WHERE id = '00000000-0000-0000-0000-00000000a301'`); err == nil {
		t.Error("crm_activities.updated_at still accepts a NULL")
	}
	if _, err := conn.Exec(ctx, `UPDATE projects SET updated_at = NULL WHERE id = '00000000-0000-0000-0000-00000000b301'`); err == nil {
		t.Error("projects.updated_at still accepts a NULL")
	}
	if _, err := conn.Exec(ctx, `UPDATE millwork_options SET updated_at = NULL WHERE id = '00000000-0000-0000-0000-00000000c301'`); err == nil {
		t.Error("millwork_options.updated_at still accepts a NULL")
	}

	// Idempotent: a second apply changes nothing.
	apply102(t, conn, target)
	if n := scalar102[int](t, conn, `SELECT count(*) FROM crm_activities`); n != 1 {
		t.Errorf("a second run left %d activities, want 1", n)
	}

	// The down file applies, the column is nullable again, and a re-apply works.
	down, err := os.ReadFile(downFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	for _, tbl := range []string{"crm_activities", "projects", "millwork_options"} {
		if v := scalar102[string](t, conn, `SELECT is_nullable FROM information_schema.columns WHERE table_name = $1 AND column_name = 'updated_at'`, tbl); v != "YES" {
			t.Errorf("%s.updated_at is_nullable = %s after down, want YES", tbl, v)
		}
	}
	apply102(t, conn, target)
}
