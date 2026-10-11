// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// Migration 107 on rows that exist (round 6 P1-1 and round 7 P2-2): a category
// pricing rule targets exactly one customer scope. The schema up to 106 is built
// in a scratch database of its own, a rule that breaks the invariant is planted
// the way an earlier version could write it, and 107 is applied: it must report
// the row in a NOTICE without rewriting it and put the check on NOT VALID, then
// on a database with no such row it must validate on add. Either way the check
// refuses a new rule that names both. Replacing the file with SELECT 1 fails
// every assertion here.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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

const constraint107 = "category_pricing_rules_single_target_check"

func scratch107(t *testing.T) (*pgx.Conn, *[]string) {
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
	name := "gv1_m107_" + hex.EncodeToString(suffix)
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

func scalar107[T any](t *testing.T, conn *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func TestMigration107_SingleTargetCheck(t *testing.T) {
	conn, notices := scratch107(t)
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	var target string
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasSuffix(base, "_down.sql"):
		case strings.HasPrefix(base, "107_"):
			target = f
		case base < "107_":
			applyFile(t, conn, f)
		}
	}
	if target == "" {
		t.Fatal("migration 107 not found")
	}
	ctx := context.Background()
	const (
		tierRule    = "00000000-0000-0000-0000-0000000001a1"
		accountRule = "00000000-0000-0000-0000-0000000001a2"
		goodTier    = "00000000-0000-0000-0000-0000000001a3"
		customer    = "00000000-0000-0000-0000-0000000001c1"
		category    = "00000000-0000-0000-0000-0000000001d1"
		category2   = "00000000-0000-0000-0000-0000000001d2"
	)
	setup := fmt.Sprintf(`
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ('%[1]s', 'm107 customer', 'M107-1', (SELECT id FROM locations LIMIT 1));
		INSERT INTO product_categories (id, name, slug, path) VALUES ('%[2]s', 'm107 category', 'm107-cat', 'm107'),
		 ('00000000-0000-0000-0000-0000000001d2', 'm107 other category', 'm107-cat2', 'm107b');
		INSERT INTO category_pricing_rules (id, target_type, tier, customer_id, category_id, rule_type, rule_value, created_by) VALUES
		 ('%[3]s', 'TIER', 'M107', '%[1]s', '%[2]s', 'MARKUP', 10, 'test'),
		 ('%[4]s', 'ACCOUNT', 'M107', '%[1]s', '%[2]s', 'MARKUP', 10, 'test'),
		 ('%[5]s', 'TIER', 'M107-OK', NULL, '%[2]s', 'MARKUP', 10, 'test');
	`, customer, category, tierRule, accountRule, goodTier)
	if _, err := conn.Exec(ctx, setup); err != nil {
		t.Fatalf("plant the legacy rows: %v", err)
	}
	rowsDigest := func() string {
		return scalar107[string](t, conn, `SELECT md5(string_agg(x::text, ',' ORDER BY x::text)) FROM (SELECT * FROM category_pricing_rules) x`)
	}
	before := rowsDigest()

	// 1. On a database that holds rows breaking the invariant: a NOTICE names
	// the count and both ids, no row is rewritten, and the check goes on NOT
	// VALID so the report and the schema can be reconciled by hand.
	*notices = nil
	applyFile(t, conn, target)
	var noticed string
	for _, n := range *notices {
		if strings.HasPrefix(n, "107:") {
			noticed = n
		}
	}
	if !strings.Contains(noticed, "2 category pricing rule row(s) name both") || !strings.Contains(noticed, tierRule) || !strings.Contains(noticed, accountRule) {
		t.Errorf("the NOTICE does not report the two rows and their ids: %q", noticed)
	}
	if strings.Contains(noticed, goodTier) {
		t.Errorf("the NOTICE names a row that breaks nothing: %q", noticed)
	}
	if after := rowsDigest(); after != before {
		t.Error("migration 107 rewrote a category pricing rule row")
	}
	if validated := scalar107[bool](t, conn, `SELECT convalidated FROM pg_constraint WHERE conname = $1`, constraint107); validated {
		t.Error("the check is validated although two rows break it: it must be NOT VALID")
	}

	// 2. The check refuses a new rule that names both, and a rule edited into
	// that shape, however the table got there.
	insertRule := func(targetType, tier, customerArg string) error {
		_, err := conn.Exec(ctx, `INSERT INTO category_pricing_rules (target_type, tier, customer_id, category_id, rule_type, rule_value, created_by)
			VALUES ($1, NULLIF($2, ''), NULLIF($3, '')::uuid, $4, 'MARKUP', 5, 'test')`, targetType, tier, customerArg, category2)
		return err
	}
	for _, c := range []struct{ targetType, tier, customer string }{
		{"TIER", "M107-NEW", customer},
		{"ACCOUNT", "M107-NEW", customer},
	} {
		var pgErr *pgconn.PgError
		if err := insertRule(c.targetType, c.tier, c.customer); !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != constraint107 {
			t.Errorf("a new %s rule naming a tier and a customer: %v, want the check violation of %s", c.targetType, err, constraint107)
		}
	}
	if err := insertRule("ACCOUNT", "", customer); err != nil {
		t.Errorf("a plain account rule was refused: %v", err)
	}
	if err := insertRule("TIER", "M107-NEW", ""); err != nil {
		t.Errorf("a plain tier rule was refused: %v", err)
	}

	// 3. Once the legacy rows are reconciled the check validates.
	if _, err := conn.Exec(ctx, `DELETE FROM category_pricing_rules WHERE id IN ($1, $2)`, tierRule, accountRule); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `ALTER TABLE category_pricing_rules VALIDATE CONSTRAINT `+constraint107); err != nil {
		t.Errorf("the check does not validate once the legacy rows are gone: %v", err)
	}

	// 4. On a database with no such row the migration validates on add: drop
	// the check and run the file again over clean rows.
	if _, err := conn.Exec(ctx, `ALTER TABLE category_pricing_rules DROP CONSTRAINT `+constraint107); err != nil {
		t.Fatal(err)
	}
	*notices = nil
	applyFile(t, conn, target)
	noticed = ""
	for _, n := range *notices {
		if strings.HasPrefix(n, "107:") {
			noticed = n
		}
	}
	if !strings.Contains(noticed, "no category pricing rule row names both") {
		t.Errorf("the clean database NOTICE is wrong: %q", noticed)
	}
	if validated := scalar107[bool](t, conn, `SELECT convalidated FROM pg_constraint WHERE conname = $1`, constraint107); !validated {
		t.Error("the check is NOT VALID on a database with no row that breaks it: it must validate on add")
	}
}
