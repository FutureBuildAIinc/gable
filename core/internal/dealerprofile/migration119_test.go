// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dealerprofile_test

// Migration 119 on rows that exist (the recipe's migration step): the dealer
// profile gains its locale, the tag every date the app shows is formatted
// for. An existing row takes the en-US default (the demo seed lifts the demo
// dealer to en-CA), the shape constraint refuses a tag that is no language
// tag, the down drops the column, and a re-apply works.

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

func scratchDB119(t *testing.T) *pgx.Conn {
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
	name := "gv1_g1mig119_" + hex.EncodeToString(suffix)
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

func migration119Files(t *testing.T) (before []string, target, downFile string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "119_"):
			target = f
		case base < "119_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 119 not found")
	}
	downFile = "../../migrations/down/119_dealer_profile_locale_down.sql"
	if _, err := os.Stat(downFile); err != nil {
		t.Fatal("migration 119 down not found")
	}
	return before, target, downFile
}

func apply119(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func TestMigration119_DealerProfileLocale(t *testing.T) {
	conn := scratchDB119(t)
	before, target, downFile := migration119Files(t)
	for _, f := range before {
		apply119(t, conn, f)
	}
	ctx := context.Background()

	// The single profile row as an existing database holds it: filled in, no
	// locale column yet.
	if _, err := conn.Exec(ctx, `UPDATE dealer_profile SET legal_name = 'Mig Yard Ltd', trading_name = 'Mig Yard' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	apply119(t, conn, target)

	var locale string
	if err := conn.QueryRow(ctx, `SELECT locale FROM dealer_profile WHERE id = 1`).Scan(&locale); err != nil {
		t.Fatalf("the existing row reads no locale: %v", err)
	}
	if locale != "en-US" {
		t.Errorf("the existing row's locale = %q, want the en-US default", locale)
	}
	if _, err := conn.Exec(ctx, `UPDATE dealer_profile SET locale = 'en-CA' WHERE id = 1`); err != nil {
		t.Errorf("a good tag is refused: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE dealer_profile SET locale = 'not a locale' WHERE id = 1`); err == nil {
		t.Error("a tag that is no language tag passed the shape check")
	}

	apply119(t, conn, downFile)
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'dealer_profile' AND column_name = 'locale'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the down left the locale column behind")
	}

	apply119(t, conn, target)
	if err := conn.QueryRow(ctx, `SELECT locale FROM dealer_profile WHERE id = 1`).Scan(&locale); err != nil || locale != "en-US" {
		t.Errorf("a re-apply: locale = %q, %v", locale, err)
	}
}
