// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
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
)

// Migration 130, up down up, with the stated role mapping proven on real
// rows: the legacy trio maps in place, the five role constraint accepts the
// new vocabulary and refuses the old, and the scheduling tables and the
// orders column come and go with the migration.

func scratch130(t *testing.T) *pgx.Conn {
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
	name := "gv1_m130_" + uuid.New().String()[:8]
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

func apply130(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func scalar130(t *testing.T, conn *pgx.Conn, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return s
}

func TestMigration130_UpDownUpWithRoleMapping(t *testing.T) {
	conn := scratch130(t)
	ctx := context.Background()

	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)

	var up130, down130 string
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "130_"):
			up130 = f
		}
	}
	down130 = "../../migrations/down/130_pro_portal_schedules_and_boards_down.sql"
	if up130 == "" {
		t.Fatal("migration 130 not found")
	}

	// Apply everything before 130 (the down siblings are not migrations).
	for _, f := range all {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_down.sql") || strings.HasPrefix(base, "130_") || base > "130_" {
			continue
		}
		apply130(t, conn, f)
	}

	// A yard and two customers with users in the legacy trio.
	yard := uuid.New()
	if _, err := conn.Exec(ctx,
		`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'M130', 'm130 yard')`, yard); err != nil {
		t.Fatal(err)
	}
	legacy := []struct {
		id   uuid.UUID
		role string
	}{
		{uuid.New(), "Admin"},
		{uuid.New(), "Buyer"},
		{uuid.New(), "View-Only"},
		{uuid.New(), "owner"},
		{uuid.New(), "mystery_role"},
	}
	for _, u := range legacy {
		if _, err := conn.Exec(ctx, `
			INSERT INTO customers (id, name, account_number, primary_branch_id)
			VALUES ($1, $2, $3, $4)`, uuid.New(), "M130 "+u.role, "M130-"+u.id.String()[:8], yard); err != nil {
			t.Fatal(err)
		}
	}
	var cust string
	if err := conn.QueryRow(ctx, `SELECT id FROM customers LIMIT 1`).Scan(&cust); err != nil {
		t.Fatal(err)
	}
	for _, u := range legacy {
		if _, err := conn.Exec(ctx, `
			INSERT INTO customer_users (id, customer_id, email, password_hash, name, role, status)
			VALUES ($1, $2, $3, 'x', $4, $5, 'Active')`,
			u.id, cust, "m130-"+u.role+"@example.test", u.role, u.role); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `
			INSERT INTO portal_invites (id, customer_id, email, role, token, expires_at)
			VALUES ($1, $2, $3, $4, $5, NOW() + INTERVAL '7 days')`,
			uuid.New(), cust, "m130-inv-"+u.role+"@example.test", u.role, "tok-"+u.role); err != nil {
			t.Fatal(err)
		}
	}

	// UP: the mapping lands.
	apply130(t, conn, up130)
	want := map[string]string{
		"m130-Admin@example.test":        "owner",
		"m130-Buyer@example.test":        "project_manager",
		"m130-View-Only@example.test":    "estimator",
		"m130-owner@example.test":        "owner",
		"m130-mystery_role@example.test": "estimator",
	}
	for email, role := range want {
		if got := scalar130(t, conn, `SELECT role FROM customer_users WHERE email = $1`, email); got != role {
			t.Errorf("%s mapped to %s, want %s", email, got, role)
		}
	}
	wantInv := map[string]string{
		"m130-inv-Admin@example.test":        "owner",
		"m130-inv-Buyer@example.test":        "project_manager",
		"m130-inv-View-Only@example.test":    "estimator",
		"m130-inv-owner@example.test":        "owner",
		"m130-inv-mystery_role@example.test": "estimator",
	}
	for email, role := range wantInv {
		if got := scalar130(t, conn, `SELECT role FROM portal_invites WHERE email = $1`, email); got != role {
			t.Errorf("invite %s mapped to %s, want %s", email, got, role)
		}
	}
	// Default role is estimator.
	if _, err := conn.Exec(ctx,
		`INSERT INTO customer_users (customer_id, email, password_hash, name, status)
		 VALUES ($1, 'm130-default@example.test', 'x', 'Default', 'Active')`, cust); err != nil {
		t.Fatalf("insert with default role: %v", err)
	}
	if got := scalar130(t, conn, `SELECT role FROM customer_users WHERE email = 'm130-default@example.test'`); got != "estimator" {
		t.Errorf("default role = %s, want estimator", got)
	}
	// The constraint refuses the legacy spelling.
	if _, err := conn.Exec(ctx,
		`INSERT INTO customer_users (customer_id, email, password_hash, name, role, status)
		 VALUES ($1, 'm130-bad@example.test', 'x', 'Bad', 'Admin', 'Active')`, cust); err == nil {
		t.Error("the legacy Admin spelling was accepted after 130")
	}
	// The scheduling tables and the orders column exist.
	if _, err := conn.Exec(ctx, `
		INSERT INTO yard_schedule_windows (yard_id, kind, weekday, start_time, end_time, capacity, cutoff_minutes)
		VALUES ($1, 'delivery', 3, '08:00', '12:00', 2, 60)`, yard); err != nil {
		t.Fatalf("insert a window: %v", err)
	}
	if got := scalar130(t, conn, `SELECT count(*)::text FROM information_schema.columns WHERE table_name = 'orders' AND column_name = 'site_delivery_instructions'`); got != "1" {
		t.Errorf("orders.site_delivery_instructions columns = %s, want 1", got)
	}
	if got := scalar130(t, conn, `SELECT count(*)::text FROM information_schema.tables WHERE table_name IN ('procurement_projects', 'procurement_project_lines', 'portal_payment_methods', 'portal_payment_intents')`); got != "4" {
		t.Errorf("phase 2 portal tables = %s, want 4", got)
	}

	// DOWN: the inverse mapping and the gone tables.
	apply130(t, conn, down130)
	if got := scalar130(t, conn, `SELECT role FROM customer_users WHERE email = $1`, "m130-Admin@example.test"); got != "Admin" {
		t.Errorf("down mapped owner to %s, want Admin", got)
	}
	if got := scalar130(t, conn, `SELECT count(*)::text FROM information_schema.tables WHERE table_name IN ('yard_schedule_windows', 'portal_schedule_holds', 'procurement_projects', 'procurement_project_lines', 'portal_payment_methods', 'portal_payment_intents')`); got != "0" {
		t.Errorf("portal tables still present after down: %s", got)
	}

	// UP again: the mapping is idempotent on the already mapped rows.
	apply130(t, conn, up130)
	if got := scalar130(t, conn, `SELECT role FROM customer_users WHERE email = $1`, "m130-Admin@example.test"); got != "owner" {
		t.Errorf("second up left the role %s, want owner", got)
	}
	if got := scalar130(t, conn, `SELECT count(*)::text FROM information_schema.tables WHERE table_name IN ('procurement_projects', 'procurement_project_lines', 'portal_payment_methods', 'portal_payment_intents')`); got != "4" {
		t.Errorf("phase 2 portal tables after second up = %s, want 4", got)
	}
}
