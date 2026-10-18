// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package signinmethods_test

// Migration 129 on rows that exist (the recipe's migration step): the
// dealer's allowed sign in methods table arrives with the safe default row
// (email and password, the one method the server implements today), so a
// tenant created before the migration keeps its sign in exactly as it was.
// The column check refuses a method outside the six, the guard trigger
// refuses the delete or truncate that would empty the table, the down drops
// everything the up created, and a re-apply works.

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

func scratchDB129(t *testing.T) *pgx.Conn {
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
	name := "gv1_s9mig129_" + hex.EncodeToString(suffix)
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

func migration129Files(t *testing.T) (before []string, target, downFile string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "129_"):
			target = f
		case base < "129_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 129 not found")
	}
	downFile = "../../migrations/down/129_dealer_signin_methods_down.sql"
	if _, err := os.Stat(downFile); err != nil {
		t.Fatal("migration 129 down not found")
	}
	return before, target, downFile
}

func apply129(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func TestMigration129_DealerSigninMethods(t *testing.T) {
	conn := scratchDB129(t)
	before, target, downFile := migration129Files(t)
	for _, f := range before {
		apply129(t, conn, f)
	}
	ctx := context.Background()

	apply129(t, conn, target)

	// A tenant that existed before the migration keeps its sign in: the one
	// method the server implements, email and password, is allowed by the
	// default row the migration seeds.
	var method string
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM dealer_signin_methods`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the seeded rows = %d, %v; want the single default", n, err)
	}
	if err := conn.QueryRow(ctx, `SELECT method FROM dealer_signin_methods`).Scan(&method); err != nil || method != "email_password" {
		t.Fatalf("the default method = %q, %v; want email_password", method, err)
	}

	// The closed vocabulary is a database check.
	if _, err := conn.Exec(ctx, `INSERT INTO dealer_signin_methods (method, sort_order) VALUES ('sms', 1)`); err == nil {
		t.Error("a method outside the six passed the check")
	}

	// At least one method always allowed: deleting the last row, and
	// truncating, are both refused.
	if _, err := conn.Exec(ctx, `DELETE FROM dealer_signin_methods`); err == nil {
		t.Error("the delete that would empty the allowlist was allowed")
	}
	if _, err := conn.Exec(ctx, `TRUNCATE dealer_signin_methods`); err == nil {
		t.Error("the truncate that would empty the allowlist was allowed")
	}

	// A second method may be added, and then removed again.
	if _, err := conn.Exec(ctx, `INSERT INTO dealer_signin_methods (method, sort_order) VALUES ('google', 2)`); err != nil {
		t.Fatalf("adding a second method: %v", err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM dealer_signin_methods WHERE method = 'google'`); err != nil {
		t.Fatalf("removing the second method: %v", err)
	}

	apply129(t, conn, downFile)
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'dealer_signin_methods'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the down left the table behind: %d, %v", n, err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_proc WHERE proname = 'dealer_signin_methods_keep_one'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the down left the guard function behind: %d, %v", n, err)
	}

	apply129(t, conn, target)
	if err := conn.QueryRow(ctx, `SELECT method FROM dealer_signin_methods`).Scan(&method); err != nil || method != "email_password" {
		t.Errorf("a re-apply: method = %q, %v", method, err)
	}
}
