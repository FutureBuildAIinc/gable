// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote_test

// Migration 131 on a scratch database that holds every earlier migration: it
// applies, rolls back (the down file drops every table it made), applies
// again, the approval log refuses UPDATE and DELETE, and the demo seed's
// TRUNCATE ... CASCADE still clears it.

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

func scratchDB131(t *testing.T) *pgx.Conn {
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
	name := "gv1_t3mig131_" + hex.EncodeToString(suffix)
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

func migration131Files(t *testing.T) (before []string, target, downFile string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "131_"):
			target = f
		case base < "131_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 131 not found")
	}
	downFile = "../../migrations/down/131_pro_customer_quotes_and_selections_down.sql"
	if _, err := os.Stat(downFile); err != nil {
		t.Fatal("migration 131 down not found")
	}
	return before, target, downFile
}

func apply131(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

var migration131Tables = []string{
	"pro_contractor_settings", "pro_customer_quotes", "pro_customer_quote_option_groups",
	"pro_customer_quote_lines", "pro_customer_quote_category_rules", "pro_customer_quote_labour_lines",
	"pro_customer_quote_overhead_lines", "pro_customer_quote_selections", "pro_portal_share_tokens",
	"pro_portal_verification_codes", "pro_customer_quote_approval_log",
}

func tableCount131(t *testing.T, conn *pgx.Conn) int {
	t.Helper()
	var n int
	err := conn.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = ANY($1)`,
		migration131Tables).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigration131_UpDownUp(t *testing.T) {
	conn := scratchDB131(t)
	before, target, downFile := migration131Files(t)
	for _, f := range before {
		apply131(t, conn, f)
	}
	if n := tableCount131(t, conn); n != 0 {
		t.Fatalf("%d of the tables exist before the migration", n)
	}
	apply131(t, conn, target)
	if n := tableCount131(t, conn); n != len(migration131Tables) {
		t.Fatalf("after up: %d of %d tables", n, len(migration131Tables))
	}
	apply131(t, conn, downFile)
	if n := tableCount131(t, conn); n != 0 {
		t.Fatalf("after down: %d tables remain", n)
	}
	apply131(t, conn, target)
	if n := tableCount131(t, conn); n != len(migration131Tables) {
		t.Fatalf("after the second up: %d of %d tables", n, len(migration131Tables))
	}
	// A second apply of the up file is a no-op (IF NOT EXISTS throughout).
	apply131(t, conn, target)
}

// seedQuote131 inserts the smallest customer and quote the log can hang from.
func seedQuote131(t *testing.T, conn *pgx.Conn) (quoteID string) {
	t.Helper()
	ctx := context.Background()
	var customerID string
	if err := conn.QueryRow(ctx,
		`INSERT INTO customers (name, account_number, primary_branch_id)
		 VALUES ('Migration Contractor', 'MIG131-001', (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')) RETURNING id`).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if err := conn.QueryRow(ctx,
		`INSERT INTO pro_customer_quotes (customer_id, reference, title, end_customer_name, valid_until)
		 VALUES ($1, 'CQ-MIG-1', 'Deck', 'A Homeowner', now() + interval '1 day') RETURNING id`, customerID).Scan(&quoteID); err != nil {
		t.Fatalf("seed quote: %v", err)
	}
	return quoteID
}

func TestMigration131_ApprovalLogIsAppendOnly(t *testing.T) {
	conn := scratchDB131(t)
	before, target, _ := migration131Files(t)
	for _, f := range before {
		apply131(t, conn, f)
	}
	apply131(t, conn, target)
	ctx := context.Background()
	quoteID := seedQuote131(t, conn)

	var logID string
	if err := conn.QueryRow(ctx,
		`INSERT INTO pro_customer_quote_approval_log (quote_id, quote_revision, step, actor_kind, actor_name)
		 VALUES ($1, 1, 'customer_approved', 'customer', 'A Homeowner') RETURNING id`, quoteID).Scan(&logID); err != nil {
		t.Fatalf("append: %v", err)
	}

	for name, stmt := range map[string]string{
		"update": `UPDATE pro_customer_quote_approval_log SET actor_name = 'Someone Else' WHERE id = $1`,
		"delete": `DELETE FROM pro_customer_quote_approval_log WHERE id = $1`,
	} {
		_, err := conn.Exec(ctx, stmt, logID)
		if err == nil {
			t.Errorf("%s of the approval log was accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "append only") {
			t.Errorf("%s refused for another reason: %v", name, err)
		}
	}

	var name string
	if err := conn.QueryRow(ctx, `SELECT actor_name FROM pro_customer_quote_approval_log WHERE id = $1`, logID).Scan(&name); err != nil || name != "A Homeowner" {
		t.Fatalf("the row changed or vanished: %q, %v", name, err)
	}

	// A quote with a log row cannot be deleted either (ON DELETE RESTRICT).
	if _, err := conn.Exec(ctx, `DELETE FROM pro_customer_quotes WHERE id = $1`, quoteID); err == nil {
		t.Error("a quote that has an approval log was deleted")
	}

	// The demo seed clears the transactional tables with TRUNCATE ... CASCADE, which
	// reaches the log through the quote's foreign keys; it must keep working.
	if _, err := conn.Exec(ctx, `TRUNCATE TABLE projects, orders, quotes CASCADE`); err != nil {
		t.Errorf("the seed's truncate cascade was refused: %v", err)
	}
	var left int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pro_customer_quote_approval_log`).Scan(&left); err != nil || left != 0 {
		t.Errorf("the cascade left %d log rows: %v", left, err)
	}
}

func TestMigration131_OneDecisionPerRevision(t *testing.T) {
	conn := scratchDB131(t)
	before, target, _ := migration131Files(t)
	for _, f := range before {
		apply131(t, conn, f)
	}
	apply131(t, conn, target)
	ctx := context.Background()
	quoteID := seedQuote131(t, conn)
	if _, err := conn.Exec(ctx,
		`INSERT INTO pro_customer_quote_approval_log (quote_id, quote_revision, step, actor_kind, actor_name)
		 VALUES ($1, 1, 'customer_approved', 'customer', 'A Homeowner')`, quoteID); err != nil {
		t.Fatal(err)
	}
	// One decision per revision: a second customer_approved for revision 1 is refused,
	// the next revision is allowed.
	if _, err := conn.Exec(ctx,
		`INSERT INTO pro_customer_quote_approval_log (quote_id, quote_revision, step, actor_kind, actor_name)
		 VALUES ($1, 1, 'customer_approved', 'customer', 'Again')`, quoteID); err == nil {
		t.Error("two customer approvals of one revision were accepted")
	}
	if _, err := conn.Exec(ctx,
		`INSERT INTO pro_customer_quote_approval_log (quote_id, quote_revision, step, actor_kind, actor_name)
		 VALUES ($1, 2, 'customer_approved', 'customer', 'Next Revision')`, quoteID); err != nil {
		t.Errorf("an approval of the next revision was refused: %v", err)
	}
}

func TestMigration131_Constraints(t *testing.T) {
	conn := scratchDB131(t)
	before, target, _ := migration131Files(t)
	for _, f := range before {
		apply131(t, conn, f)
	}
	apply131(t, conn, target)
	ctx := context.Background()
	quoteID := seedQuote131(t, conn)

	// A target margin of 100 percent or more is refused at every tier.
	if _, err := conn.Exec(ctx, `UPDATE pro_customer_quotes SET markup_kind = 'margin', markup_bps = 10000 WHERE id = $1`, quoteID); err == nil {
		t.Error("a quote default margin of 100 percent was accepted")
	}
	if _, err := conn.Exec(ctx, `UPDATE pro_customer_quotes SET markup_kind = 'margin', markup_bps = 9999 WHERE id = $1`, quoteID); err != nil {
		t.Errorf("a margin of 99.99 percent was refused: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO pro_contractor_settings (customer_id, markup_kind, markup_bps)
		SELECT customer_id, 'margin', 12000 FROM pro_customer_quotes WHERE id = $1`, quoteID); err == nil {
		t.Error("contractor default margin of 120 percent was accepted")
	}

	// A token hash must be 64 lowercase hex characters; one live link per quote.
	if _, err := conn.Exec(ctx, `INSERT INTO pro_portal_share_tokens (quote_id, token_hash, expires_at) VALUES ($1, 'not-a-hash', now() + interval '1 day')`, quoteID); err == nil {
		t.Error("a token hash that is not a sha256 hex was accepted")
	}
	good := strings.Repeat("ab", 32)
	if _, err := conn.Exec(ctx, `INSERT INTO pro_portal_share_tokens (quote_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 day')`, quoteID, good); err != nil {
		t.Fatalf("a good token hash was refused: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO pro_portal_share_tokens (quote_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 day')`, quoteID, strings.Repeat("cd", 32)); err == nil {
		t.Error("two live links for one quote were accepted")
	}
}
