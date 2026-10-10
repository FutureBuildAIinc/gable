// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts_test

// Migration 103 (ADR 0007 section 9): applies on an empty database and on
// one that carries rows, is idempotent, the down file reverses it without
// losing a row the base owned, and the read only report raises a NOTICE for
// a key whose scopes fall outside the grant grammar (section 5.3).

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

func scratchDB103(t *testing.T) (*pgx.Conn, *[]string) {
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
		t.Fatal(err)
	}
	name := "gv1_c52amig_" + hex.EncodeToString(suffix)
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

func migrationFiles103(t *testing.T) (before []string, target, down string) {
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
		case strings.HasPrefix(base, "103_"):
			target = f
		case base < "103_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 103 not found")
	}
	down = filepath.Join(filepath.Dir(target), "down", strings.TrimSuffix(filepath.Base(target), ".sql")+"_down.sql")
	if _, err := os.Stat(down); err != nil {
		t.Fatalf("down file: %v", err)
	}
	return before, target, down
}

func applySQL103(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func scalar103(t *testing.T, conn *pgx.Conn, sql string, args ...any) any {
	t.Helper()
	var v any
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// TestMigration103_AppliesOnEmptyDatabaseAndIsIdempotent builds the schema
// from scratch through 102, applies 103 twice (every step is guarded), and
// reads the shapes back: the drafts constraints, the purge row, and the
// api_keys column with its NULL default.
func TestMigration103_AppliesOnEmptyDatabaseAndIsIdempotent(t *testing.T) {
	conn, _ := scratchDB103(t)
	before, target, _ := migrationFiles103(t)
	for _, f := range before {
		applySQL103(t, conn, f)
	}
	applySQL103(t, conn, target)
	applySQL103(t, conn, target) // idempotent

	if got := scalar103(t, conn, `SELECT through_position FROM draft_events_purged WHERE id`); got != int64(0) {
		t.Fatalf("draft_events_purged through_position = %v, want 0", got)
	}
	if got := scalar103(t, conn, `SELECT count(*) FROM drafts`); got != int64(0) {
		t.Fatalf("drafts is not empty: %v", got)
	}
	if got := scalar103(t, conn, `SELECT is_nullable FROM information_schema.columns WHERE table_name='api_keys' AND column_name='branch_id'`); got != "YES" {
		t.Fatalf("api_keys.branch_id is_nullable = %v, want YES (a null branch is today's unbound behaviour)", got)
	}

	// The status shape constraints bite: a PROMOTED row without provenance
	// and a subject pair that halves are both refused.
	branch := scalar103(t, conn, `SELECT id::text FROM locations WHERE type='BRANCH' LIMIT 1`).(string)
	if _, err := conn.Exec(context.Background(),
		`INSERT INTO drafts (module, branch_id, payload, created_by_kind, updated_by_kind)
		 VALUES ('quotes', $1, '{}', 'user', 'user')`, branch); err != nil {
		t.Fatalf("insert minimal draft: %v", err)
	}
	if _, err := conn.Exec(context.Background(),
		`UPDATE drafts SET status='PROMOTED'`); err == nil {
		t.Fatal("PROMOTED without promoted_entity_id was accepted")
	}
	if _, err := conn.Exec(context.Background(),
		`UPDATE drafts SET subject_id = $1, subject_revision = NULL`, branch); err == nil {
		t.Fatal("a subject_id with a NULL subject_revision was accepted")
	}
}

// TestMigration103_AppliesOnASeededDatabaseAndReportsOffGrammarKeys applies
// 103 to a database with three unrevoked keys: one whose scopes fall
// outside the grant grammar (an off-grammar key is named in a NOTICE
// naming the typo and the prefix), one whose scopes are inside the
// grammar with a propose or commit verb (a gaining-reach key is named in
// its own NOTICE because the verbs are new and the operator should
// review such a key explicitly), and one whose scopes are inside the
// grammar with only plain verbs (no NOTICE named it). Nothing is
// changed; each row keeps its stored scopes.
func TestMigration103_AppliesOnASeededDatabaseAndReportsOffGrammarKeys(t *testing.T) {
	conn, notices := scratchDB103(t)
	before, target, _ := migrationFiles103(t)
	for _, f := range before {
		applySQL103(t, conn, f)
	}
	branch, err := conn.Exec(context.Background(),
		`INSERT INTO api_keys (id, name, key_hash, key_prefix, scopes)
		 VALUES ('11111111-1111-1111-1111-111111111111', 'typo key', 'x$y', 'off_grammar_prefix', '{"quotes:writ"}'),
		        ('22222222-2222-2222-2222-222222222222', 'gaining key', 'x$y', 'gaining_reach_prefix', '{"quotes:read","quotes:propose"}'),
		        ('33333333-3333-3333-3333-333333333333', 'plain key', 'x$y', 'plain_in_grammar_prefix', '{"quotes:read"}')`)
	if err != nil {
		t.Fatalf("seed keys: %v", err)
	}
	if branch.RowsAffected() != 3 {
		t.Fatalf("seed keys wrote %d rows", branch.RowsAffected())
	}
	applySQL103(t, conn, target)

	var reportedOff, reportedGaining bool
	for _, n := range *notices {
		switch {
		case strings.Contains(n, "11111111-1111-1111-1111-111111111111") &&
			strings.Contains(n, "off_grammar_prefix") && strings.Contains(n, "outside the grant grammar"):
			reportedOff = true
		case strings.Contains(n, "22222222-2222-2222-2222-222222222222") &&
			strings.Contains(n, "gaining_reach_prefix") && strings.Contains(n, "gains reach"):
			reportedGaining = true
		case strings.Contains(n, "33333333"):
			t.Errorf("the plain in-grammar key was reported: %s", n)
		}
	}
	if !reportedOff {
		t.Errorf("the off-grammar key was not named in a NOTICE; notices: %v", *notices)
	}
	if !reportedGaining {
		t.Errorf("the gaining-reach key was not named in a NOTICE; notices: %v", *notices)
	}
	// The report changed nothing: each key keeps its stored scopes.
	if got := scalar103(t, conn,
		`SELECT scopes::text FROM api_keys WHERE id = '11111111-1111-1111-1111-111111111111'`); got != "{quotes:writ}" {
		t.Fatalf("the reported key's scopes were changed: %v", got)
	}
}

// TestMigration103_DownThenUpLosesNoRow: with 103 applied and rows in it,
// the down file drops only what 103 created and a second up restores the
// shapes; the rows the base owned (locations, api_keys) are counted across
// the cycle.
func TestMigration103_DownThenUpLosesNoRow(t *testing.T) {
	conn, _ := scratchDB103(t)
	before, target, down := migrationFiles103(t)
	for _, f := range before {
		applySQL103(t, conn, f)
	}
	applySQL103(t, conn, target)

	branch := scalar103(t, conn, `SELECT id::text FROM locations WHERE type='BRANCH' LIMIT 1`).(string)
	ctx := context.Background()
	if _, err := conn.Exec(ctx,
		`INSERT INTO api_keys (id, name, key_hash, key_prefix, scopes, branch_id)
		 VALUES ('33333333-3333-3333-3333-333333333333', 'bound', 'x$y', 'sk_live_cc', '{"quotes:read"}', $1)`, branch); err != nil {
		t.Fatalf("seed bound key: %v", err)
	}
	keysBefore := scalar103(t, conn, `SELECT count(*) FROM api_keys`)
	locationsBefore := scalar103(t, conn, `SELECT count(*) FROM locations`)

	applySQL103(t, conn, down)
	var probe int
	if err := conn.QueryRow(ctx, `SELECT 1 FROM drafts LIMIT 1`).Scan(&probe); err == nil {
		t.Fatal("drafts still exists after the down file")
	}
	if got := scalar103(t, conn, `SELECT count(*) FROM api_keys`); got != keysBefore {
		t.Fatalf("api_keys rows after down = %v, want %v (a bound branch_id column is dropped, not the key)", got, keysBefore)
	}
	if got := scalar103(t, conn,
		`SELECT count(*) FROM information_schema.columns WHERE table_name='api_keys' AND column_name='branch_id'`); got != int64(0) {
		t.Fatalf("api_keys.branch_id still exists after the down file: %v", got)
	}

	applySQL103(t, conn, target)
	if got := scalar103(t, conn, `SELECT count(*) FROM locations`); got != locationsBefore {
		t.Fatalf("locations rows after down/up = %v, want %v", got, locationsBefore)
	}
	if got := scalar103(t, conn, `SELECT count(*) FROM api_keys`); got != keysBefore {
		t.Fatalf("api_keys rows after down/up = %v, want %v", got, keysBefore)
	}
}
