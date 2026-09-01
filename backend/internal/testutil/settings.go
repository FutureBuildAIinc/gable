// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/gablelbm/gable/pkg/database"
)

// SettingsSandbox returns a database handle whose `system_settings` table is
// PRIVATE to the calling test, while every other table still resolves to the
// shared schema.
//
// It replaces a snapshot-and-restore helper that could not work. The
// credential-sealing tests live in two different packages
// (cmd/server/settings_plaintext_test.go and internal/ai/keystore_vault_test.go)
// and both write the SAME two rows — openrouter_api_key and
// openrouteservice_api_key. `go test ./...` runs packages as concurrent
// PROCESSES against one database, so a per-process snapshot guard restores or
// deletes a row out from under the other package mid-test. On a database where
// those rows do not already exist the cleanup is a DELETE, and the failure is
// routine; once the rows exist the cleanup becomes an UPDATE and the whole
// thing passes forever after. That is why it was green locally and red on
// every fresh CI database.
//
// Isolation rather than serialisation, and rather than per-test key names,
// because the invariant under test is about the REAL production key names and
// about the whole table: cmd/server scans every row for a plaintext canary, and
// internal/ai deliberately writes a plaintext canary into openrouter_api_key to
// prove legacy rows still read. Renaming the keys would weaken the first test;
// a lock would not stop the second from being visible to the first.
//
// Mechanism: a uniquely-named schema containing a clone of the real
// system_settings definition, and a pool whose search_path puts that schema
// ahead of public. Unqualified `system_settings` therefore resolves to the
// clone; `customers`, `orders` and everything else still resolve to public.
// The schema is dropped when the test ends.
func SettingsSandbox(t *testing.T) *database.DB {
	t.Helper()

	// admin is a normal (non-sandboxed) handle, used to create and drop the
	// schema. RequireDB also gives us the skip-or-fail behaviour for free.
	admin := RequireDB(t)
	ctx := context.Background()

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("sandbox name: %v", err)
	}
	// Identifiers are lowercase and unique per test; the pid is not enough
	// because one process runs many tests.
	schema := "gable_settings_sbx_" + hex.EncodeToString(suffix[:])

	if _, err := admin.Pool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", schema)); err != nil {
		t.Fatalf("create sandbox schema: %v", err)
	}
	// INCLUDING ALL copies the primary key on `key`, which ON CONFLICT (key)
	// needs, and the updated_at default, which payment.SetSecret relies on by
	// not supplying the column.
	if _, err := admin.Pool.Exec(ctx, fmt.Sprintf(
		"CREATE TABLE %s.system_settings (LIKE public.system_settings INCLUDING ALL)", schema)); err != nil {
		t.Fatalf("clone system_settings into sandbox: %v", err)
	}

	sandbox := requireDBWithSearchPath(t, schema+",public")

	// Registered AFTER the sandbox pool's own close cleanup, so it runs BEFORE
	// it (t.Cleanup is LIFO) — hence the explicit Close here, so the DROP is
	// not blocked by the pool's idle connections. Closing twice is safe.
	t.Cleanup(func() {
		sandbox.Close()
		if _, err := admin.Pool.Exec(context.Background(),
			fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema)); err != nil {
			t.Errorf("drop sandbox schema %s: %v", schema, err)
		}
	})

	// Prove the redirection actually took effect. Without this the tests would
	// silently fall back to writing the shared table — which is the exact
	// failure mode this helper exists to end.
	var resolved string
	if err := sandbox.Pool.QueryRow(ctx,
		"SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = 'system_settings'::regclass").
		Scan(&resolved); err != nil {
		t.Fatalf("resolve system_settings in sandbox: %v", err)
	}
	if resolved != schema {
		t.Fatalf("sandbox search_path did not take: system_settings resolves to %q, want %q", resolved, schema)
	}

	return sandbox
}
