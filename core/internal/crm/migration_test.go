// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm_test

// Migration 096 on rows that exist (recipe step 3): the schema up to 095 is
// built in a scratch database of its own, legacy rows are written in the
// shape the base commit left them, then 096 is applied and each backfill is
// read back; the down file and a re-apply both work.

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
	name := "gv1_c51bmig_" + hex.EncodeToString(suffix)
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

func migration096Files(t *testing.T) (before []string, target string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "096_"):
			target = f
		case base < "096_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 096 not found")
	}
	return before, target
}

func apply096(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func scalar096[T any](t *testing.T, conn *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func TestMigration096_BackfillsRowsThatExist(t *testing.T) {
	conn := scratchDB(t)
	before, target := migration096Files(t)
	for _, f := range before {
		apply096(t, conn, f)
	}
	ctx := context.Background()

	// Legacy rows in the shape the base commit left them: NULL created_at
	// where the column allowed it, a NULL description, no revision column.
	legacy := `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ('00000000-0000-0000-0000-00000000e001', 'Mig Crm', 'MIG-CRM-1',
			(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'));
		INSERT INTO crm_activities (id, customer_id, contact_id, activity_type, description, logged_by, activity_date, created_at, updated_at)
		VALUES
		 ('00000000-0000-0000-0000-00000000a001','00000000-0000-0000-0000-00000000e001',NULL,'CALL','a call',NULL,now() - interval '2 days',NULL,NULL),
		 ('00000000-0000-0000-0000-00000000a002','00000000-0000-0000-0000-00000000e001',NULL,'NOTE',NULL,NULL,NULL,now() - interval '1 day',NULL);
		INSERT INTO projects (id, customer_id, name, status, created_at, updated_at)
		VALUES
		 ('00000000-0000-0000-0000-00000000b001','00000000-0000-0000-0000-00000000e001','Mig Job','Active',NULL,now()),
		 ('00000000-0000-0000-0000-00000000b002','00000000-0000-0000-0000-00000000e001','Done Job','Completed',now(),now());
		INSERT INTO millwork_options (id, category, name, price_adjustment, attributes, created_at, updated_at)
		VALUES
		 ('00000000-0000-0000-0000-00000000c001','door_type','Mig Door',12.50,'{}'::jsonb,NULL,now());`
	if _, err := conn.Exec(ctx, legacy); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}

	apply096(t, conn, target)

	// crm_activities: created_at filled and NOT NULL, descriptions filled,
	// revision 1, the keyset index.
	if n := scalar096[int](t, conn, `SELECT count(*) FROM crm_activities WHERE created_at IS NULL OR revision <> 1 OR description IS NULL`); n != 0 {
		t.Errorf("%d activities with a NULL created_at, a NULL description or a revision other than 1", n)
	}
	if null := scalar096[*string](t, conn, `SELECT description FROM crm_activities WHERE id = '00000000-0000-0000-0000-00000000a002'`); null == nil || *null != "" {
		t.Errorf("a legacy NULL description = %v, want the empty string", null)
	}
	if n := scalar096[int](t, conn, `SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_crm_activities_customer_created_at_id_desc'`); n != 1 {
		t.Error("the crm_activities keyset index is missing")
	}
	if def := scalar096[string](t, conn, `SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_crm_activities_customer_created_at_id_desc'`); !strings.Contains(def, "(customer_id, created_at DESC, id DESC)") {
		t.Errorf("the crm_activities keyset index does not lead with the scope column: %s", def)
	}

	// projects: the data 091 moved stays intact (ids, statuses, counts).
	if n := scalar096[int](t, conn, `SELECT count(*) FROM projects WHERE created_at IS NULL OR revision <> 1`); n != 0 {
		t.Errorf("%d projects with a NULL created_at or a revision other than 1", n)
	}
	if got := scalar096[string](t, conn, `SELECT status FROM projects WHERE id = '00000000-0000-0000-0000-00000000b001'`); got != "Active" {
		t.Errorf("the migrated job's status = %s, want Active (091's data stays as it is)", got)
	}
	if n := scalar096[int](t, conn, `SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_projects_customer_created_at_id_desc'`); n != 1 {
		t.Error("the projects keyset index is missing")
	}
	if def := scalar096[string](t, conn, `SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_projects_customer_created_at_id_desc'`); !strings.Contains(def, "(customer_id, created_at DESC, id DESC)") {
		t.Errorf("the projects keyset index does not lead with the scope column: %s", def)
	}

	// millwork_options: same three rules.
	if n := scalar096[int](t, conn, `SELECT count(*) FROM millwork_options WHERE created_at IS NULL OR revision <> 1`); n != 0 {
		t.Errorf("%d millwork options with a NULL created_at or a revision other than 1", n)
	}
	if n := scalar096[int](t, conn, `SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_millwork_options_category_created_at_id_desc'`); n != 1 {
		t.Error("the millwork_options keyset index is missing")
	}
	if def := scalar096[string](t, conn, `SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_millwork_options_category_created_at_id_desc'`); !strings.Contains(def, "(category, created_at DESC, id DESC)") {
		t.Errorf("the millwork_options keyset index does not lead with the scope column: %s", def)
	}

	// A NULL created_at was filled from a stated fallback, never the epoch.
	if epoch := scalar096[*string](t, conn, `SELECT created_at::text FROM millwork_options WHERE id = '00000000-0000-0000-0000-00000000c001'`); epoch == nil || strings.HasPrefix(*epoch, "0001-") {
		t.Errorf("a NULL created_at was left at the epoch: %v", epoch)
	}

	// Applying it again changes nothing and fails nothing.
	apply096(t, conn, target)
	if n := scalar096[int](t, conn, `SELECT count(*) FROM crm_activities`); n != 2 {
		t.Errorf("a second run left %d activities, want 2", n)
	}

	// The down file applies, and up again is clean.
	down, err := os.ReadFile("../../migrations/down/096_crm_projects_millwork_wire_contract_down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := scalar096[int](t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'crm_activities' AND column_name = 'revision'`); n != 0 {
		t.Error("the down file left crm_activities.revision behind")
	}
	apply096(t, conn, target)
}

// Migration 096 normalises activity_type: 033 left the column unconstrained
// TEXT, so a legacy import or a direct insert could hold any spelling, and
// the wire read (a closed vocabulary) fails on a value outside the four.
// Every stored value is uppercased and trimmed, whatever is still outside
// CALL, MEETING, EMAIL and NOTE maps to NOTE, and a CHECK then holds the
// column to the four. The NULL row needs the column's own NOT NULL dropped
// first (033 declares NOT NULL, so the test simulates a drifted legacy
// schema to prove the rewrite covers that arm too).
func TestMigration096_NormalisesActivityType(t *testing.T) {
	conn := scratchDB(t)
	before, target := migration096Files(t)
	for _, f := range before {
		apply096(t, conn, f)
	}
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `ALTER TABLE crm_activities ALTER COLUMN activity_type DROP NOT NULL`); err != nil {
		t.Fatalf("drift the column nullable: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ('00000000-0000-0000-0000-00000000e002', 'Mig Odd Types', 'MIG-CRM-2',
			(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'));
		INSERT INTO crm_activities (id, customer_id, activity_type, description, created_at)
		VALUES
		 ('00000000-0000-0000-0000-00000000a101','00000000-0000-0000-0000-00000000e002','call','lowercase',now()),
		 ('00000000-0000-0000-0000-00000000a102','00000000-0000-0000-0000-00000000e002','WeIrD','mixed case',now()),
		 ('00000000-0000-0000-0000-00000000a103','00000000-0000-0000-0000-00000000e002','','empty',now()),
		 ('00000000-0000-0000-0000-00000000a104','00000000-0000-0000-0000-00000000e002',NULL,'null type',now()),
		 ('00000000-0000-0000-0000-00000000a105','00000000-0000-0000-0000-00000000e002','  meeting  ','padded',now()),
		 ('00000000-0000-0000-0000-00000000a106','00000000-0000-0000-0000-00000000e002','EMAIL','already fine',now())`); err != nil {
		t.Fatalf("legacy odd rows: %v", err)
	}

	apply096(t, conn, target)

	// Both rewrites: upper(btrim(...)) first, then everything still outside
	// the four values (a NULL included) maps to NOTE.
	for id, want := range map[string]string{
		"00000000-0000-0000-0000-00000000a101": "CALL",
		"00000000-0000-0000-0000-00000000a102": "NOTE",
		"00000000-0000-0000-0000-00000000a103": "NOTE",
		"00000000-0000-0000-0000-00000000a104": "NOTE",
		"00000000-0000-0000-0000-00000000a105": "MEETING",
		"00000000-0000-0000-0000-00000000a106": "EMAIL",
	} {
		if got := scalar096[string](t, conn, `SELECT activity_type FROM crm_activities WHERE id = $1`, id); got != want {
			t.Errorf("activity %s normalised to %q, want %q", id[len(id)-4:], got, want)
		}
	}
	// The CHECK holds the column to the four storage values.
	if _, err := conn.Exec(ctx, `UPDATE crm_activities SET activity_type = 'WeIrD' WHERE id = $1`,
		"00000000-0000-0000-0000-00000000a101"); err == nil {
		t.Error("the activity_type CHECK accepts a value outside the four")
	}
	if _, err := conn.Exec(ctx, `UPDATE crm_activities SET activity_type = 'call' WHERE id = $1`,
		"00000000-0000-0000-0000-00000000a101"); err == nil {
		t.Error("the activity_type CHECK accepts a lowercase spelling")
	}
}
