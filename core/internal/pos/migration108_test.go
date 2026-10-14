// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Migration 108 (r15 P2-1): the till session, Z report and return reads filter
// on their branch_id, which hid every row written before the column was
// stamped. The migration backfills each row from its own register (the round 6
// rule, COALESCE(the register's location's branch, pos_registers.branch_id)).
// The schema up to 107 is built in a scratch database of its own, legacy rows
// are planted the way refactor/v1 wrote them (branch NULL, on registers whose
// location names no branch), and 108 is applied: it must set each row from its
// register, count what it set per table in a NOTICE, report by count and first
// ids the rows whose register names no branch and leave them NULL, never touch
// a row whose branch is already set, move no money, and change nothing at all
// on a second run. Replacing the file with SELECT 1 fails every assertion here.

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

func scratch108(t *testing.T) (*pgx.Conn, *[]string) {
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
	name := "gv1_m108_" + hex.EncodeToString(suffix)
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

func applyFile108(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func scalar108[T any](t *testing.T, conn *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// digest108 hashes a table's planted rows without their branch_id, so a
// migration that touched anything but the branch fails the comparison.
func digest108(t *testing.T, conn *pgx.Conn, table string, ids []uuid.UUID) string {
	t.Helper()
	return scalar108[string](t, conn, fmt.Sprintf(
		`SELECT md5(string_agg((row_to_json(x)::jsonb - 'branch_id')::text, ',' ORDER BY x.id))
		   FROM %s x WHERE x.id = ANY($1)`, table), ids)
}

func branch108(t *testing.T, conn *pgx.Conn, table string, id uuid.UUID) *uuid.UUID {
	t.Helper()
	var branch *uuid.UUID
	if err := conn.QueryRow(context.Background(),
		fmt.Sprintf(`SELECT branch_id FROM %s WHERE id = $1`, table), id).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	return branch
}

// notice108 finds the 108 NOTICE holding the fragment.
func notice108(notices *[]string, fragment string) string {
	for _, n := range *notices {
		if strings.HasPrefix(n, "108:") && strings.Contains(n, fragment) {
			return n
		}
	}
	return ""
}

func TestMigration108_BackfillsTheBranchFromTheRegister(t *testing.T) {
	conn, notices := scratch108(t)
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
		case strings.HasPrefix(base, "108_"):
			target = f
		case base < "108_":
			applyFile108(t, conn, f)
		}
	}
	if target == "" {
		t.Fatal("migration 108 not found")
	}
	ctx := context.Background()

	// Two branches, a yard of the first, and the registers: one whose
	// location_id names no location (resolved through its own
	// pos_registers.branch_id), one on the yard (the location's branch wins
	// over its own). A row whose register was never there (the foreign keys
	// would refuse one today, so they come off in this scratch database) is
	// the shape the stuck NOTICE reports.
	branchX, branchY, yardX := uuid.New(), uuid.New(), uuid.New()
	if _, err := conn.Exec(ctx, `INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'M108X', 'm108 X'), ($2, 'BRANCH', 'M108Y', 'm108 Y')`,
		branchX, branchY); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO locations (id, type, code, name, parent_id) VALUES ($1, 'YARD', 'M108XY', 'm108 yard', $2)`,
		yardX, branchX); err != nil {
		t.Fatal(err)
	}
	regNoLoc, regYard, gone := "M108-NOLOC", "M108-YARD", "M108-GONE"
	if _, err := conn.Exec(ctx, `INSERT INTO pos_registers (id, location_id, branch_id, name) VALUES
		($1, $2, $3, 'm108 no location'), ($4, $5, $6, 'm108 on the yard')`,
		regNoLoc, uuid.New(), branchX, regYard, yardX, branchY); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `ALTER TABLE till_sessions DROP CONSTRAINT till_sessions_register_id_fkey`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `ALTER TABLE pos_returns DROP CONSTRAINT pos_returns_register_id_fkey`); err != nil {
		t.Fatal(err)
	}

	// The legacy rows: branch NULL on both registers, a row whose branch is
	// already set (never to be touched), and a row whose register was never
	// there (left NULL and reported). Money columns are planted once and
	// hashed: the migration may not move any of it.
	cashier := uuid.New()
	openSess, closedSess, setSess, stuckSess := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	zrLegacy, zrSet, zrStuck := uuid.New(), uuid.New(), uuid.New()
	retNoLoc, retYard, retSet, retStuck := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	plant := fmt.Sprintf(`
		INSERT INTO till_sessions (id, register_id, branch_id, cashier_id, status, opening_float) VALUES
			('%[1]s', '%[10]s', NULL, '%[14]s', 'OPEN', 50.25),
			('%[2]s', '%[11]s', NULL, '%[14]s', 'CLOSED', 10.10),
			('%[3]s', '%[10]s', '%[13]s', '%[14]s', 'CLOSED', 77.77),
			('%[4]s', '%[12]s', NULL, '%[14]s', 'OPEN', 1.00);
		INSERT INTO till_z_reports (id, till_session_id, register_id, branch_id, over_short, payload) VALUES
			('%[5]s', '%[2]s', '%[11]s', NULL, -1.25, '{"legacy":true}'),
			('%[6]s', '%[3]s', '%[10]s', '%[13]s', 0, '{"set":true}'),
			('%[7]s', '%[1]s', '%[12]s', NULL, 2.50, '{"stuck":true}');
		INSERT INTO pos_returns (id, register_id, branch_id, cashier_id, subtotal, tax_amount, total, refund_method, status, currency, number) VALUES
			('%[8]s', '%[10]s', NULL, '%[14]s', 10.11, 0.99, 11.10, 'CASH', 'COMPLETED', 'USD', 'RTNM108-1'),
			('%[9]s', '%[11]s', NULL, '%[14]s', 20.22, 1.98, 22.20, 'CARD', 'COMPLETED', 'USD', 'RTNM108-2'),
			('%[15]s', '%[10]s', '%[13]s', '%[14]s', 5.00, 0.45, 5.45, 'CASH', 'COMPLETED', 'USD', 'RTNM108-3'),
			('%[16]s', '%[12]s', NULL, '%[14]s', 1.00, 0.09, 1.09, 'CASH', 'COMPLETED', 'USD', 'RTNM108-4');
	`, openSess, closedSess, setSess, stuckSess, zrLegacy, zrSet, zrStuck,
		retNoLoc, retYard, regNoLoc, regYard, gone, branchY, cashier, retSet, retStuck)
	if _, err := conn.Exec(ctx, plant); err != nil {
		t.Fatalf("plant the legacy rows: %v", err)
	}
	tills := []uuid.UUID{openSess, closedSess, setSess, stuckSess}
	zreports := []uuid.UUID{zrLegacy, zrSet, zrStuck}
	returns := []uuid.UUID{retNoLoc, retYard, retSet, retStuck}
	digests := map[string]string{
		"till_sessions":  digest108(t, conn, "till_sessions", tills),
		"till_z_reports": digest108(t, conn, "till_z_reports", zreports),
		"pos_returns":    digest108(t, conn, "pos_returns", returns),
	}

	// 1. The first run: one NOTICE per table counts what it set, one NOTICE
	// per table with a stuck row reports the count and its first ids, the
	// register's location's branch wins over the register's own, an already
	// set row never moves, a stuck row stays NULL, and no money column moves.
	*notices = nil
	applyFile108(t, conn, target)
	for table, want := range map[string]string{
		"till_sessions":  "till_sessions: set the branch from its register on 2 row(s)",
		"till_z_reports": "till_z_reports: set the branch from its register on 1 row(s)",
		"pos_returns":    "pos_returns: set the branch from its register on 2 row(s)",
	} {
		if n := notice108(notices, want); n == "" {
			t.Errorf("the NOTICE does not count what it set (%q): %v", want, *notices)
		}
		stuck := notice108(notices, table+": 1 row(s) name a register with no branch")
		if stuck == "" || !strings.Contains(stuck, "first ids") {
			t.Errorf("the %s stuck NOTICE does not report the count and the first ids: %v", table, *notices)
			continue
		}
		var id uuid.UUID
		switch table {
		case "till_sessions":
			id = stuckSess
		case "till_z_reports":
			id = zrStuck
		case "pos_returns":
			id = retStuck
		}
		if !strings.Contains(stuck, id.String()) {
			t.Errorf("the %s stuck NOTICE does not name %s: %q", table, id, stuck)
		}
	}
	if got := branch108(t, conn, "till_sessions", openSess); got == nil || *got != branchX {
		t.Errorf("the open session's branch is %v, want the register's own %s", got, branchX)
	}
	if got := branch108(t, conn, "till_sessions", closedSess); got == nil || *got != branchX {
		t.Errorf("the closed session's branch is %v, want its yard's branch %s (the location's branch wins over the register's own)", got, branchX)
	}
	if got := branch108(t, conn, "till_z_reports", zrLegacy); got == nil || *got != branchX {
		t.Errorf("the legacy Z report's branch is %v, want its register's yard's %s", got, branchX)
	}
	if got := branch108(t, conn, "pos_returns", retNoLoc); got == nil || *got != branchX {
		t.Errorf("the return on the locationless register has branch %v, want %s", got, branchX)
	}
	if got := branch108(t, conn, "pos_returns", retYard); got == nil || *got != branchX {
		t.Errorf("the return on the yard register has branch %v, want the yard's %s (the location's branch wins over the register's own)", got, branchX)
	}
	if got := branch108(t, conn, "till_sessions", setSess); got == nil || *got != branchY {
		t.Errorf("the already set session's branch is %v, want it left at %s", got, branchY)
	}
	if got := branch108(t, conn, "till_z_reports", zrSet); got == nil || *got != branchY {
		t.Errorf("the already set Z report's branch is %v, want it left at %s", got, branchY)
	}
	if got := branch108(t, conn, "pos_returns", retSet); got == nil || *got != branchY {
		t.Errorf("the already set return's branch is %v, want it left at %s", got, branchY)
	}
	for _, s := range []struct {
		table string
		id    uuid.UUID
	}{{"till_sessions", stuckSess}, {"till_z_reports", zrStuck}, {"pos_returns", retStuck}} {
		if got := branch108(t, conn, s.table, s.id); got != nil {
			t.Errorf("the stuck %s row %s was set to %v, want it left NULL", s.table, s.id, got)
		}
	}
	for table, ids := range map[string][]uuid.UUID{"till_sessions": tills, "till_z_reports": zreports, "pos_returns": returns} {
		if after := digest108(t, conn, table, ids); after != digests[table] {
			t.Errorf("migration 108 moved more than the branch on %s", table)
		}
	}

	// 2. The second run changes nothing: every table sets 0 rows, the stuck
	// rows are still NULL and still reported, and no digest moved.
	*notices = nil
	applyFile108(t, conn, target)
	for _, table := range []string{"till_sessions", "till_z_reports", "pos_returns"} {
		if n := notice108(notices, table+": set the branch from its register on 0 row(s)"); n == "" {
			t.Errorf("the second run's %s NOTICE does not count 0: %v", table, *notices)
		}
		if notice108(notices, table+": 1 row(s) name a register with no branch") == "" {
			t.Errorf("the second run no longer reports the stuck %s row: %v", table, *notices)
		}
	}
	for table, ids := range map[string][]uuid.UUID{"till_sessions": tills, "till_z_reports": zreports, "pos_returns": returns} {
		if after := digest108(t, conn, table, ids); after != digests[table] {
			t.Errorf("the second run moved more than the branch on %s", table)
		}
	}
	if got := branch108(t, conn, "till_sessions", openSess); got == nil || *got != branchX {
		t.Errorf("the second run moved the open session's branch to %v", got)
	}

	// 3. The down file leaves the data: every branch the backfill set is
	// still there, the stuck rows are still NULL, and no digest moved.
	down, err := os.ReadFile("../../migrations/down/108_pos_branch_backfill_down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(down)); err != nil {
		t.Fatalf("applying the down file: %v", err)
	}
	if got := branch108(t, conn, "till_sessions", openSess); got == nil || *got != branchX {
		t.Errorf("the down file nulled the open session's branch to %v, want it kept", got)
	}
	if got := branch108(t, conn, "till_sessions", stuckSess); got != nil {
		t.Errorf("the down file set the stuck session's branch to %v", got)
	}
	for table, ids := range map[string][]uuid.UUID{"till_sessions": tills, "till_z_reports": zreports, "pos_returns": returns} {
		if after := digest108(t, conn, table, ids); after != digests[table] {
			t.Errorf("the down file moved more than the branch on %s", table)
		}
	}
}
