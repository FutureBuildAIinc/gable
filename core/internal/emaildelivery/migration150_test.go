// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package emaildelivery_test

// Migration 150 down (CP-14): rolling the status vocabulary back keeps every
// delivery row. The old check holds only sent and failed, so a queued row (a
// send that never resolved) and a logged row (a send no mail server took)
// become failed with the reason in error; sent and failed rows are untouched;
// the forward migration then applies again on the result. Replacing the down
// file with the old DELETE fails the row count here.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const migrations150 = "../../migrations/"

func execFile(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), string(text)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(path), err)
	}
}

// scratch150 builds a scratch database with every migration through 150
// applied.
func scratch150(t *testing.T) *sql.DB {
	t.Helper()
	base, err := url.Parse(testutil.RequireDB(t).Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", base.String())
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "gv1_m150_" + hex.EncodeToString(suffix)
	if _, err := admin.ExecContext(context.Background(), fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		admin.Close()
		t.Skipf("cannot create a scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name))
		admin.Close()
	})
	scratch := *base
	scratch.Path = "/" + name
	db, err := sql.Open("pgx", scratch.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	files, err := filepath.Glob(migrations150 + "*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		b := filepath.Base(f)
		if strings.HasSuffix(b, "_down.sql") || strings.Compare(b, "151_") >= 0 {
			continue
		}
		execFile(t, db, f)
	}
	return db
}

func TestMigration150Down_KeepsEveryRowAndReapplies(t *testing.T) {
	db := scratch150(t)
	insert := func(status, recipient string, errText any) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO email_deliveries (recipient, subject, document_type, status, error)
			VALUES ($1, 'down', 'invoice', $2, $3)`, recipient, status, errText); err != nil {
			t.Fatalf("seed %s: %v", status, err)
		}
	}
	insert("queued", "queued@example.test", nil)
	insert("logged", "logged@example.test", nil)
	insert("sent", "sent@example.test", nil)
	insert("failed", "failed@example.test", "smtp dial: connection refused")

	execFile(t, db, migrations150+"down/150_email_delivery_status_down.sql")

	got := map[string][2]string{}
	rows, err := db.Query(`SELECT recipient, status, COALESCE(error, '') FROM email_deliveries`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r, s, e string
		if err := rows.Scan(&r, &s, &e); err != nil {
			t.Fatal(err)
		}
		got[r] = [2]string{s, e}
	}
	if len(got) != 4 {
		t.Fatalf("down kept %d of 4 rows: %v", len(got), got)
	}
	for _, r := range []string{"queued@example.test", "logged@example.test"} {
		if got[r][0] != "failed" || got[r][1] == "" {
			t.Errorf("%s = %v, want failed with a reason", r, got[r])
		}
	}
	if got["sent@example.test"] != [2]string{"sent", ""} {
		t.Errorf("sent row changed: %v", got["sent@example.test"])
	}
	if got["failed@example.test"] != [2]string{"failed", "smtp dial: connection refused"} {
		t.Errorf("failed row changed: %v", got["failed@example.test"])
	}
	// the old check is back
	if _, err := db.Exec(`INSERT INTO email_deliveries (recipient, subject, document_type, status) VALUES ('x@example.test', 's', 'invoice', 'queued')`); err == nil {
		t.Error("queued is accepted after the down migration")
	}

	execFile(t, db, migrations150+"150_email_delivery_status.sql")
	for _, s := range []string{"queued", "logged"} {
		insert(s, s+"-again@example.test", nil)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM email_deliveries`).Scan(&n); err != nil || n != 6 {
		t.Errorf("rows after reapply = %d (%v), want 6", n, err)
	}
}
