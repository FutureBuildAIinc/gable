// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer_test

// Migration 091 on rows that exist (recipe step 3 and ADR 0005 section 13):
// the schema up to 090 is built in a scratch database of its own, legacy rows
// are written in the shape the base commit left them, then 091 is applied and
// each backfill is read back. The jobs merge is the one that moves data
// between tables: a quote's job reads the same job after the migration, and a
// job with no customer is reported and not copied.

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
	name := "gv1_c21mig_" + hex.EncodeToString(suffix)
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
		case strings.HasPrefix(base, "091_"):
			target = f
		case base < "091_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 091 not found")
	}
	return before, target
}

func apply(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func scalar[T any](t *testing.T, conn *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func TestMigration091_BackfillsRowsThatExist(t *testing.T) {
	conn, notices := scratchDB(t)
	before, target := migrationFiles(t)
	for _, f := range before {
		apply(t, conn, f)
	}
	ctx := context.Background()

	const (
		a1, a2, a3         = "00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2", "00000000-0000-0000-0000-0000000000a3"
		b1, b2, b3, b4, b5 = "00000000-0000-0000-0000-0000000000b1", "00000000-0000-0000-0000-0000000000b2", "00000000-0000-0000-0000-0000000000b3", "00000000-0000-0000-0000-0000000000b4", "00000000-0000-0000-0000-0000000000b5"
	)
	legacy := fmt.Sprintf(`
		INSERT INTO customers (id, name, account_number, address, credit_limit, balance_due, payment_terms, primary_branch_id, created_at) VALUES
		 ('%[1]s','Alpha Co','A1','1 Main St',0,NULL,'NET30',(SELECT id FROM locations LIMIT 1),NULL),
		 ('%[2]s','Beta Co','A2','',5000,10,'Net 45',(SELECT id FROM locations LIMIT 1),now()),
		 ('%[3]s','Gamma Co','A3',NULL,0,0,NULL,(SELECT id FROM locations LIMIT 1),now());
		INSERT INTO customer_contacts (customer_id, first_name, last_name, created_at) VALUES ('%[1]s','Pat','Lee',NULL);
		INSERT INTO invoices (order_id, customer_id, status, total_amount, subtotal, tax_amount, payment_terms, branch_id)
		 SELECT NULL, '%[1]s', 'UNPAID', 1, 1, 0, 'Net 15', (SELECT id FROM locations LIMIT 1) WHERE false;
		INSERT INTO customer_jobs (id, customer_id, name, is_active) VALUES
		 ('%[4]s','%[1]s','Job one',true),
		 ('%[5]s','%[1]s','Job two',false),
		 ('%[6]s',NULL,'Orphan with one quote customer',true),
		 ('%[7]s',NULL,'Orphan with no customer',true),
		 ('%[8]s',NULL,'Orphan with two quote customers',true);
		INSERT INTO quotes (id, customer_id, job_id, state, total_amount, branch_id, created_at) VALUES
		 ('00000000-0000-0000-0000-0000000000c1','%[1]s','%[4]s','DRAFT',1,(SELECT id FROM locations LIMIT 1),now()),
		 ('00000000-0000-0000-0000-0000000000c2','%[2]s','%[6]s','DRAFT',1,(SELECT id FROM locations LIMIT 1),now()),
		 ('00000000-0000-0000-0000-0000000000c3','%[1]s','%[8]s','DRAFT',1,(SELECT id FROM locations LIMIT 1),now()),
		 ('00000000-0000-0000-0000-0000000000c4','%[2]s','%[8]s','DRAFT',1,(SELECT id FROM locations LIMIT 1),now()),
		 ('00000000-0000-0000-0000-0000000000c5','%[3]s',NULL,'DRAFT',1,(SELECT id FROM locations LIMIT 1),now());
		INSERT INTO pricing_rules (name, rule_type, job_id) VALUES
		 ('rule on the orphan','JOB_OVERRIDE','%[7]s'), ('rule on a copied job','JOB_OVERRIDE','%[4]s');
	`, a1, a2, a3, b1, b2, b3, b4, b5)
	if _, err := conn.Exec(ctx, legacy); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}
	// An invoice naming terms nothing else names.
	if _, err := conn.Exec(ctx, `INSERT INTO orders (customer_id, status, total_amount, branch_id) VALUES ($1, 'FULFILLED', 1, (SELECT id FROM locations LIMIT 1))`, a1); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO invoices (order_id, customer_id, status, total_amount, subtotal, tax_amount, payment_terms, branch_id)
		VALUES ((SELECT id FROM orders WHERE customer_id = $1), $1, 'UNPAID', 1, 1, 0, 'Net 15', (SELECT id FROM locations LIMIT 1))`, a1); err != nil {
		t.Fatal(err)
	}

	apply(t, conn, target)

	// 1. created_at, revision, balance.
	if n := scalar[int](t, conn, `SELECT count(*) FROM customers WHERE created_at IS NULL OR revision <> 1 OR balance_due IS NULL`); n != 0 {
		t.Errorf("%d customers with a NULL created_at, a NULL balance or a revision other than 1", n)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM customer_contacts WHERE created_at IS NULL OR revision <> 1 OR can_place_orders IS NOT TRUE OR order_limit IS NOT NULL`); n != 0 {
		t.Errorf("%d contacts with a NULL created_at or authority other than the defaults", n)
	}

	// 2. Credit limit: 0 (no limit) became NULL; a real limit stayed.
	if v := scalar[*string](t, conn, `SELECT credit_limit::text FROM customers WHERE account_number = 'A1'`); v != nil {
		t.Errorf("A1 credit limit = %v, want NULL (0 meant no limit)", *v)
	}
	if v := scalar[*string](t, conn, `SELECT credit_limit::text FROM customers WHERE account_number = 'A2'`); v == nil || *v != "5000.00" {
		t.Errorf("A2 credit limit = %v, want 5000.00", v)
	}
	if v := scalar[bool](t, conn, `SELECT po_required FROM customers WHERE account_number = 'A1'`); v {
		t.Error("po_required must default to false")
	}

	// 3. Terms: the seed, the legacy text value, the default for none.
	for _, code := range []string{"NET30", "NET60", "NET90", "DUE_ON_RECEIPT", "COD"} {
		if n := scalar[int](t, conn, `SELECT count(*) FROM payment_terms WHERE code = $1`, code); n != 1 {
			t.Errorf("seeded terms %s: %d rows", code, n)
		}
	}
	code := func(acct string) string {
		return scalar[string](t, conn, `SELECT pt.code FROM customers c JOIN payment_terms pt ON pt.id = c.payment_terms_id WHERE c.account_number = $1`, acct)
	}
	if code("A1") != "NET30" || code("A2") != "NET45" || code("A3") != "NET30" {
		t.Errorf("customer terms = %s, %s, %s; want NET30, NET45 ('Net 45' keeps its 45 days), NET30 (none)", code("A1"), code("A2"), code("A3"))
	}
	if k := scalar[string](t, conn, `SELECT kind || ':' || net_days FROM payment_terms WHERE code = 'NET45'`); k != "NET_DAYS:45" {
		t.Errorf("a legacy text value became %s, want NET_DAYS:45", k)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM payment_terms WHERE code = 'NET15'`); n != 1 {
		t.Errorf("the terms text of an invoice: %d rows, want 1", n)
	}
	// A raw writer that names no terms still gets the default.
	if _, err := conn.Exec(ctx, `INSERT INTO customers (name, account_number, primary_branch_id) VALUES ('Raw', 'RAW-1', (SELECT id FROM locations LIMIT 1))`); err != nil {
		t.Fatalf("a raw customer insert without terms: %v", err)
	}
	if code("RAW-1") != "NET30" {
		t.Errorf("a raw insert got terms %s, want the NET30 default", code("RAW-1"))
	}

	// 4. Ship-tos: one default MAIN for each customer with an address.
	if n := scalar[int](t, conn, `SELECT count(*) FROM customer_ship_tos WHERE is_default AND code = 'MAIN' AND line1 = '1 Main St'
		AND customer_id = $1`, a1); n != 1 {
		t.Errorf("A1 MAIN ship-tos = %d, want 1", n)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM customer_ship_tos WHERE customer_id IN ($1, $2)`, a2, a3); n != 0 {
		t.Errorf("%d ship-tos for customers with an empty or null address, want 0", n)
	}

	// 5. Currency settings.
	if got := scalar[string](t, conn, `SELECT value FROM system_settings WHERE key = 'currency.default'`); got != "USD" {
		t.Errorf("currency.default = %s", got)
	}
	if got := scalar[string](t, conn, `SELECT value FROM system_settings WHERE key = 'currency.enabled'`); got != "USD" {
		t.Errorf("currency.enabled = %s", got)
	}

	// 6. The jobs merge.
	if to := scalar[*string](t, conn, `SELECT to_regclass('customer_jobs')::text`); to != nil {
		t.Errorf("customer_jobs still exists: %s", *to)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'quotes' AND column_name = 'job_id'`); n != 0 {
		t.Error("quotes.job_id still exists")
	}
	job := func(id string) string {
		return scalar[string](t, conn, `SELECT customer_id::text || '/' || status FROM projects WHERE id = $1`, id)
	}
	if job(b1) != a1+"/Active" || job(b2) != a1+"/Inactive" {
		t.Errorf("copied jobs = %s, %s; want ids kept with Active and Inactive by is_active", job(b1), job(b2))
	}
	if job(b3) != a2+"/Active" {
		t.Errorf("the orphan whose quotes name one customer = %s, want %s/Active", job(b3), a2)
	}
	for _, id := range []string{b4, b5} {
		if n := scalar[int](t, conn, `SELECT count(*) FROM projects WHERE id = $1`, id); n != 0 {
			t.Errorf("job %s was copied though no single customer owns it", id)
		}
	}
	project := func(quote string) *string {
		return scalar[*string](t, conn, `SELECT project_id::text FROM quotes WHERE id = $1`, quote)
	}
	if p := project("00000000-0000-0000-0000-0000000000c1"); p == nil || *p != b1 {
		t.Errorf("quote c1 project = %v, want its job %s unchanged", p, b1)
	}
	if p := project("00000000-0000-0000-0000-0000000000c2"); p == nil || *p != b3 {
		t.Errorf("quote c2 project = %v, want %s", p, b3)
	}
	for _, q := range []string{"c3", "c4", "c5"} {
		if p := project("00000000-0000-0000-0000-0000000000" + q); p != nil {
			t.Errorf("quote %s project = %v, want none (its job was not copied, or it had none)", q, *p)
		}
	}
	reported := false
	for _, n := range *notices {
		if strings.Contains(n, "2 row(s) with no customer were not copied") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("notices = %q, want the count of uncopied jobs reported", *notices)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM pricing_rules WHERE name = 'rule on the orphan'`); n != 0 {
		t.Error("a pricing rule scoped to an uncopied job must leave pricing_rules, not be widened to every job")
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM pricing_rules_unmigrated WHERE name = 'rule on the orphan' AND job_id = $1`, b4); n != 1 {
		t.Error("that rule must be kept whole in pricing_rules_unmigrated")
	}
	if j := scalar[*string](t, conn, `SELECT job_id::text FROM pricing_rules WHERE name = 'rule on a copied job'`); j == nil || *j != b1 {
		t.Errorf("a pricing rule on a copied job = %v, want %s", j, b1)
	}
	// The rule's job now points at projects.
	if n := scalar[int](t, conn, `SELECT count(*) FROM pg_constraint WHERE conname = 'pricing_rules_job_id_fkey' AND confrelid = 'projects'::regclass`); n != 1 {
		t.Error("pricing_rules.job_id must reference projects")
	}

	// 7. Applying it again changes nothing and fails nothing.
	apply(t, conn, target)
	if n := scalar[int](t, conn, `SELECT count(*) FROM customer_ship_tos`); n != 1 {
		t.Errorf("a second run left %d ship-tos, want 1", n)
	}
}

// legacyScratch is a scratch database at schema 090 with one branch in it.
func legacyScratch(t *testing.T) (*pgx.Conn, *[]string, string) {
	t.Helper()
	conn, notices := scratchDB(t)
	before, target := migrationFiles(t)
	for _, f := range before {
		apply(t, conn, f)
	}
	return conn, notices, target
}

func exec(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// RULE (review P2-2): no customer's terms may change meaning. Spellings are
// matched case blind with spacing, dots, hyphens and underscores ignored;
// `NET <n>` is a NET n term (made when absent); COD variants are COD; text
// that matches nothing gets a term of its own (a code in the API's pattern, the
// original text as the name), never the NET30 default.
func TestMigration091_LegacyTermsKeepTheirMeaning(t *testing.T) {
	conn, notices, target := legacyScratch(t)
	cases := []struct {
		text     any    // customers.payment_terms
		wantCode string // the terms code the customer ends on
		wantKind string
		wantDays string // net_days as text, "" for none
		wantName string // "" to skip
	}{
		{"NET30", "NET30", "NET_DAYS", "30", ""},
		{"net 30", "NET30", "NET_DAYS", "30", ""},
		{"  Net-60 ", "NET60", "NET_DAYS", "60", ""},
		{"NET 15", "NET15", "NET_DAYS", "15", "Net 15"},
		{"Net 45 days", "NET45", "NET_DAYS", "45", "Net 45"},
		{"n10", "NET10", "NET_DAYS", "10", ""},
		{"NET_90", "NET90", "NET_DAYS", "90", ""},
		{"COD", "COD", "DUE_ON_RECEIPT", "", ""},
		{"cod", "COD", "DUE_ON_RECEIPT", "", ""},
		{"C.O.D.", "COD", "DUE_ON_RECEIPT", "", ""},
		{"Cash on delivery", "COD", "DUE_ON_RECEIPT", "", ""},
		{"Due on receipt", "DUE_ON_RECEIPT", "DUE_ON_RECEIPT", "", ""},
		{"DUE_ON_RECEIPT", "DUE_ON_RECEIPT", "DUE_ON_RECEIPT", "", ""},
		{"2/10 Net 30", "2-10-NET-30", "NET_DAYS", "30", "2/10 Net 30"},
		{"EOM", "EOM", "NET_DAYS", "30", "EOM"},
		{"Net 30 EOM", "NET-30-EOM", "NET_DAYS", "30", "Net 30 EOM"},
		{"2/10 net.30", "2-10-NET-30-2", "NET_DAYS", "30", "2/10 net.30"}, // collides with the code above: a suffix, never a merge
		{nil, "NET30", "NET_DAYS", "30", ""},
		{"", "NET30", "NET_DAYS", "30", ""},
	}
	for i, c := range cases {
		exec(t, conn, `INSERT INTO customers (name, account_number, payment_terms, primary_branch_id) VALUES ($1, $2, $3, (SELECT id FROM locations LIMIT 1))`,
			fmt.Sprintf("T%d", i), fmt.Sprintf("T-%02d", i), c.text)
	}
	apply(t, conn, target)

	for i, c := range cases {
		var code, kind, days, name string
		if err := conn.QueryRow(context.Background(), `SELECT pt.code, pt.kind, COALESCE(pt.net_days::text, ''), pt.name
			FROM customers cu JOIN payment_terms pt ON pt.id = cu.payment_terms_id WHERE cu.account_number = $1`, fmt.Sprintf("T-%02d", i)).
			Scan(&code, &kind, &days, &name); err != nil {
			t.Fatal(err)
		}
		if code != c.wantCode || kind != c.wantKind || days != c.wantDays || (c.wantName != "" && name != c.wantName) {
			t.Errorf("%q became %s %s %s %q, want %s %s %s %q", c.text, code, kind, days, name, c.wantCode, c.wantKind, c.wantDays, c.wantName)
		}
	}
	// Every code made is in the pattern the API enforces on a terms code.
	if n := scalar[int](t, conn, `SELECT count(*) FROM payment_terms WHERE code !~ '^[A-Z0-9][A-Z0-9_-]{0,39}$'`); n != 0 {
		t.Errorf("%d terms codes are outside the API's pattern", n)
	}
	// The unmatched texts are reported.
	reported := false
	for _, n := range *notices {
		if strings.Contains(n, "own payment terms") && strings.Contains(n, "EOM") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("notices = %q, want the texts that got terms of their own reported", *notices)
	}
	// Re-applying changes nothing.
	before := scalar[int](t, conn, `SELECT count(*) FROM payment_terms`)
	apply(t, conn, target)
	if after := scalar[int](t, conn, `SELECT count(*) FROM payment_terms`); after != before {
		t.Errorf("a second run made %d terms", after-before)
	}
}

// RULE (review P2-1 and the lossless jobs merge): 091 completes whatever the
// pricing rules scoped to uncopied jobs look like, including two rules the
// scope key would collide once the job is taken away, and nothing is dropped:
// the uncopied jobs, the quote links and the rules are kept in tables of their
// own.
func TestMigration091_UncopiedJobsLoseNothingAndNeverAbort(t *testing.T) {
	conn, _, target := legacyScratch(t)
	exec(t, conn, `
		INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES
		 ('00000000-0000-0000-0000-0000000000a1','A','A1',(SELECT id FROM locations LIMIT 1)),
		 ('00000000-0000-0000-0000-0000000000a2','B','A2',(SELECT id FROM locations LIMIT 1));
		INSERT INTO customer_jobs (id, customer_id, name, is_active) VALUES
		 ('00000000-0000-0000-0000-0000000000b1','00000000-0000-0000-0000-0000000000a1','copied',true),
		 ('00000000-0000-0000-0000-0000000000b2','00000000-0000-0000-0000-0000000000a1','copied, quote has another project',true),
		 ('00000000-0000-0000-0000-0000000000b3',NULL,'customerless one',true),
		 ('00000000-0000-0000-0000-0000000000b4',NULL,'customerless two',true),
		 ('00000000-0000-0000-0000-0000000000b5',NULL,'customerless, two customers quote it',true);
		INSERT INTO projects (id, customer_id, name) VALUES ('00000000-0000-0000-0000-0000000000d1','00000000-0000-0000-0000-0000000000a1','an existing project');
		INSERT INTO quotes (id, customer_id, job_id, project_id, state, total_amount, branch_id, created_at) VALUES
		 ('00000000-0000-0000-0000-0000000000c1','00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-0000000000b2','00000000-0000-0000-0000-0000000000d1','DRAFT',1,(SELECT id FROM locations LIMIT 1),now()),
		 ('00000000-0000-0000-0000-0000000000c2','00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-0000000000b5',NULL,'DRAFT',1,(SELECT id FROM locations LIMIT 1),now()),
		 ('00000000-0000-0000-0000-0000000000c3','00000000-0000-0000-0000-0000000000a2','00000000-0000-0000-0000-0000000000b5',NULL,'DRAFT',1,(SELECT id FROM locations LIMIT 1),now()),
		 ('00000000-0000-0000-0000-0000000000c4','00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-0000000000b1',NULL,'DRAFT',1,(SELECT id FROM locations LIMIT 1),now());
		-- Two rules the scope key would make identical once their jobs are gone, and one
		-- identical to an unscoped rule that already exists.
		INSERT INTO pricing_rules (name, rule_type, job_id, fixed_price) VALUES
		 ('r1','JOB_OVERRIDE','00000000-0000-0000-0000-0000000000b3', 1.5),
		 ('r1','JOB_OVERRIDE','00000000-0000-0000-0000-0000000000b4', 2.5),
		 ('r2','JOB_OVERRIDE','00000000-0000-0000-0000-0000000000b3', 3.5),
		 ('r2','JOB_OVERRIDE',NULL, 4.5),
		 ('kept','JOB_OVERRIDE','00000000-0000-0000-0000-0000000000b1', 5.5);`)

	apply(t, conn, target) // the base of the review: this aborted on the scope key

	if n := scalar[int](t, conn, `SELECT count(*) FROM pricing_rules`); n != 2 {
		t.Errorf("%d pricing rules left, want the unscoped r2 and the one on a copied job", n)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM pricing_rules_unmigrated`); n != 3 {
		t.Errorf("%d rules kept in pricing_rules_unmigrated, want the 3 scoped to uncopied jobs", n)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM pricing_rules_unmigrated WHERE name = 'r1' AND job_id IS NOT NULL`); n != 2 {
		t.Errorf("the two r1 rules keep their own job ids: got %d", n)
	}
	if got := scalar[string](t, conn, `SELECT job_id::text FROM pricing_rules WHERE name = 'kept'`); got != "00000000-0000-0000-0000-0000000000b1" {
		t.Errorf("the rule on a copied job points at %s", got)
	}

	// No job row is dropped: copied ones are projects, the rest are kept whole.
	if n := scalar[int](t, conn, `SELECT count(*) FROM customer_jobs_unmigrated WHERE id IN
		('00000000-0000-0000-0000-0000000000b3','00000000-0000-0000-0000-0000000000b4','00000000-0000-0000-0000-0000000000b5')`); n != 3 {
		t.Errorf("%d customerless jobs kept in customer_jobs_unmigrated, want 3 (one named by two customers' quotes)", n)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM projects WHERE id IN
		('00000000-0000-0000-0000-0000000000b1','00000000-0000-0000-0000-0000000000b2')`); n != 2 {
		t.Errorf("%d copied jobs in projects, want 2", n)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM customer_jobs_unmigrated WHERE id IN
		('00000000-0000-0000-0000-0000000000b1','00000000-0000-0000-0000-0000000000b2')`); n != 0 {
		t.Errorf("a job that was copied is also in the unmigrated table")
	}

	// A quote's job link survives somewhere recoverable when project_id cannot carry it.
	link := func(quote string) string {
		return scalar[string](t, conn, `SELECT COALESCE((SELECT job_id::text || ':' || reason FROM quote_jobs_unmigrated WHERE quote_id = $1), '')`, quote)
	}
	if got := link("00000000-0000-0000-0000-0000000000c1"); got != "00000000-0000-0000-0000-0000000000b2:quote_has_other_project" {
		t.Errorf("quote c1 link = %q", got)
	}
	if got := link("00000000-0000-0000-0000-0000000000c2"); got != "00000000-0000-0000-0000-0000000000b5:job_not_copied" {
		t.Errorf("quote c2 link = %q", got)
	}
	if got := link("00000000-0000-0000-0000-0000000000c3"); got != "00000000-0000-0000-0000-0000000000b5:job_not_copied" {
		t.Errorf("quote c3 link = %q", got)
	}
	if got := link("00000000-0000-0000-0000-0000000000c4"); got != "" {
		t.Errorf("quote c4 (cleanly carried onto project_id) has a link row: %q", got)
	}
	if p := scalar[*string](t, conn, `SELECT project_id::text FROM quotes WHERE id = '00000000-0000-0000-0000-0000000000c1'`); p == nil || *p != "00000000-0000-0000-0000-0000000000d1" {
		t.Errorf("quote c1 keeps its own project, got %v", p)
	}
	if p := scalar[*string](t, conn, `SELECT project_id::text FROM quotes WHERE id = '00000000-0000-0000-0000-0000000000c4'`); p == nil || *p != "00000000-0000-0000-0000-0000000000b1" {
		t.Errorf("quote c4 project = %v, want its job", p)
	}

	// Applying it again and rolling it back both work and lose nothing.
	apply(t, conn, target)
	down, err := os.ReadFile("../../migrations/down/091_customers_wire_contract_down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM customer_jobs`); n != 5 {
		t.Errorf("%d jobs after the rollback, want all 5 back", n)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM pricing_rules`); n != 5 {
		t.Errorf("%d pricing rules after the rollback, want all 5 back", n)
	}
	for q, want := range map[string]string{
		"c1": "00000000-0000-0000-0000-0000000000b2", "c2": "00000000-0000-0000-0000-0000000000b5",
		"c3": "00000000-0000-0000-0000-0000000000b5", "c4": "00000000-0000-0000-0000-0000000000b1",
	} {
		if got := scalar[*string](t, conn, `SELECT job_id::text FROM quotes WHERE id = $1`, "00000000-0000-0000-0000-0000000000"+q); got == nil || *got != want {
			t.Errorf("quote %s job after the rollback = %v, want %s", q, got, want)
		}
	}
}
