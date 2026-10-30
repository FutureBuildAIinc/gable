// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/internal/testutil"
)

// TestSaveSettingsWritesAnAuditRow: turning the dealer threshold off or
// moving it changes what the counter records, so the change is audited with
// the old and the new figures and who made it.
func TestSaveSettingsWritesAnAuditRow(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := largecash.NewService(db, nil, "")
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'large_cash.settings.changed' AND user_id = 'settings-audit-test'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key LIKE 'largecash.%' OR key = 'country.default'`)
	})
	if err := svc.SaveSettings(ctx, &largecash.Settings{CountryDefault: "US", CAEnabled: true, CAThresholdCents: 1234500}, "settings-audit-test"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'large_cash.settings.changed'
		AND user_id = 'settings-audit-test' AND changes->'after'->>'ca_enabled' = 'true'
		AND changes->'after'->>'ca_threshold_cents' = '1234500'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("settings audit rows = %d, want 1", n)
	}
}
