// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// TestSeedLeavesTheDealersYardsOnly seeds a fresh scratch database (migrate,
// then the demo seed, each through its command) and checks the branches are
// the dealer's three yards: the migrations' placeholder branch (MAIN, Main
// Branch) must not survive into the yard switcher.
func TestSeedLeavesTheDealersYardsOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a scratch database of its own; skipped under -short")
	}
	db, dbURL := scratchSeedDB(t)
	runSeedCommand(t, dbURL, "./cmd/migrate")
	runSeedCommand(t, dbURL, "./cmd/seed")

	rows, err := db.Query(`SELECT code FROM locations WHERE type = 'BRANCH' ORDER BY code`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var codes []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			t.Fatal(err)
		}
		codes = append(codes, code)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(codes, ","); got != "KEL-MAIN,LK,WK" {
		t.Fatalf("branches after the seed = %s, want KEL-MAIN,LK,WK", got)
	}
}

// scratchSeedDB creates a scratch database beside the shared test database and
// drops it when the test ends; it answers a handle and the URL to seed.
func scratchSeedDB(t *testing.T) (*sql.DB, string) {
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
	name := "seed_branches_" + hex.EncodeToString(suffix)
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
	return db, scratch.String()
}

// runSeedCommand runs one of the repository's commands from core/ (so migrate
// finds migrations/) against the scratch database with the demo seed asked for.
func runSeedCommand(t *testing.T, dbURL, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "run", pkg)
	cmd.Dir = "../../.."
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dbURL, demoSeedEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go run %s: %v\n%s", pkg, err, out)
	}
}
