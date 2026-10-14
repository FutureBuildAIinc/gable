// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer_test

// Migration 113 (SEC-NOHEADER-1 round 4): the customer wall reads
// customer_branches, and the demo seed wrote customers with a primary branch
// and no row there, so those customers are hidden from every header and grants
// caller. The migration inserts the row for a customer's own primary branch
// where it has none. The schema up to 112 is built in a scratch database of its
// own, customers are planted the old seeded way, and 113 is applied: it must
// add the row for each rowless customer's primary branch and nothing else
// (a customer shared with another branch keeps that row and gains its primary
// one), report by NOTICE how many it set and how many customers have no
// primary branch (left alone), never touch a customer row or a balance, and
// change nothing on a second run or after its down file. Replacing the file
// with SELECT 1 fails every assertion here.

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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func scratch113(t *testing.T) (*pgx.Conn, *[]string) {
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
	name := "gv1_m113_" + hex.EncodeToString(suffix)
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

func applyFile113(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func scalar113[T any](t *testing.T, conn *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// notice113 finds the 113 NOTICE holding the fragment, or "".
func notice113(notices []string, fragment string) string {
	for _, n := range notices {
		if strings.HasPrefix(n, "113:") && strings.Contains(n, fragment) {
			return n
		}
	}
	return ""
}

func TestMigration113_BackfillsThePrimaryBranchRow(t *testing.T) {
	conn, notices := scratch113(t)
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
		case strings.HasPrefix(base, "113_"):
			target = f
		case base < "113_":
			applyFile113(t, conn, f)
		}
	}
	if target == "" {
		t.Fatal("migration 113 not found")
	}
	ctx := context.Background()

	branchX, branchY := uuid.New(), uuid.New()
	if _, err := conn.Exec(ctx, `INSERT INTO locations (id, type, code, name) VALUES
		($1, 'BRANCH', 'M113X', 'm113 X'), ($2, 'BRANCH', 'M113Y', 'm113 Y')`, branchX, branchY); err != nil {
		t.Fatal(err)
	}
	// The migrated schema forbids a customer with no primary branch; the
	// legacy shape the migration reports is planted by lifting that rule here.
	if _, err := conn.Exec(ctx, `ALTER TABLE customers ALTER COLUMN primary_branch_id DROP NOT NULL`); err != nil {
		t.Fatal(err)
	}
	rowless, held, shared, rowlessY, noBranch := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for i, c := range []struct {
		id      uuid.UUID
		primary any
	}{{rowless, branchX}, {held, branchX}, {shared, branchX}, {rowlessY, branchY}, {noBranch, nil}} {
		if _, err := conn.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id, balance_due)
			VALUES ($1, $2, $3, $4, $5)`, c.id, fmt.Sprintf("m113 customer %d", i), fmt.Sprintf("M113-%d", i), c.primary, 100+i); err != nil {
			t.Fatal(err)
		}
	}
	// The product's own create wrote the held customer's row; the shared
	// customer is held by the other branch only. Migration 067 and the
	// seed's old insert left the rest with none, so clear whatever the
	// migrated schema mirrored.
	if _, err := conn.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id IN ($1, $2, $3, $4, $5)`,
		rowless, held, shared, rowlessY, noBranch); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2), ($3, $4)`,
		held, branchX, shared, branchY); err != nil {
		t.Fatal(err)
	}
	customersDigest := func() string {
		return scalar113[string](t, conn, `SELECT md5(string_agg(row_to_json(c)::text, ',' ORDER BY c.id)) FROM customers c`)
	}
	rowsOf := func(id uuid.UUID) []string {
		t.Helper()
		r, err := conn.Query(ctx, `SELECT branch_id::text FROM customer_branches WHERE customer_id = $1 ORDER BY 1`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var out []string
		for r.Next() {
			var b string
			if err := r.Scan(&b); err != nil {
				t.Fatal(err)
			}
			out = append(out, b)
		}
		return out
	}
	has := func(id, branch uuid.UUID) bool {
		for _, b := range rowsOf(id) {
			if b == branch.String() {
				return true
			}
		}
		return false
	}
	balances := scalar113[int64](t, conn, `SELECT SUM(ROUND(balance_due * 100))::bigint FROM customers`)
	digest := customersDigest()
	rowTotal := scalar113[int64](t, conn, `SELECT COUNT(*) FROM customer_branches`)

	// 1. The first run sets the row on the rowless customers and the one
	// shared with the other branch, and reports the customer with no branch.
	*notices = nil
	applyFile113(t, conn, target)
	if !has(rowless, branchX) || !has(rowlessY, branchY) || !has(shared, branchX) {
		t.Errorf("a rowless customer was left without its primary branch row: %v %v %v", rowsOf(rowless), rowsOf(rowlessY), rowsOf(shared))
	}
	if !has(shared, branchY) || len(rowsOf(shared)) != 2 {
		t.Errorf("the shared customer's other branch row did not survive: %v", rowsOf(shared))
	}
	if len(rowsOf(held)) != 1 || len(rowsOf(noBranch)) != 0 {
		t.Errorf("a customer that needed nothing changed: held %v, no branch %v", rowsOf(held), rowsOf(noBranch))
	}
	if n := notice113(*notices, "set the customer_branches row for its primary branch on 3 customer(s)"); n == "" {
		t.Errorf("no NOTICE counts the three customers set; notices: %v", *notices)
	}
	if n := notice113(*notices, "1 customer(s) have no primary branch and were left as they are"); n == "" {
		t.Errorf("no NOTICE reports the one customer with no primary branch; notices: %v", *notices)
	}
	if got := scalar113[int64](t, conn, `SELECT COUNT(*) FROM customer_branches`); got != rowTotal+3 {
		t.Errorf("customer_branches holds %d rows, want %d (three added, none deleted)", got, rowTotal+3)
	}
	if customersDigest() != digest {
		t.Errorf("the migration changed a customers row")
	}
	if got := scalar113[int64](t, conn, `SELECT SUM(ROUND(balance_due * 100))::bigint FROM customers`); got != balances {
		t.Errorf("customer balances moved from %d to %d", balances, got)
	}

	// 2. A second run changes nothing and says so.
	*notices = nil
	applyFile113(t, conn, target)
	if got := scalar113[int64](t, conn, `SELECT COUNT(*) FROM customer_branches`); got != rowTotal+3 {
		t.Errorf("a second run changed the row count to %d, want %d", got, rowTotal+3)
	}
	if n := notice113(*notices, "on 0 customer(s)"); n == "" {
		t.Errorf("the second run does not report zero customers set; notices: %v", *notices)
	}

	// 3. The down file removes nothing, and up again after it changes nothing.
	down, err := filepath.Glob("../../migrations/down/113_*_down.sql")
	if err != nil || len(down) != 1 {
		t.Fatalf("migration 113 down file: %v %v", down, err)
	}
	applyFile113(t, conn, down[0])
	if got := scalar113[int64](t, conn, `SELECT COUNT(*) FROM customer_branches`); got != rowTotal+3 {
		t.Errorf("the down file changed the row count to %d, want %d (it removes nothing)", got, rowTotal+3)
	}
	applyFile113(t, conn, target)
	if got := scalar113[int64](t, conn, `SELECT COUNT(*) FROM customer_branches`); got != rowTotal+3 {
		t.Errorf("up after down changed the row count to %d, want %d", got, rowTotal+3)
	}
}
