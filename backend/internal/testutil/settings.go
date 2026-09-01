// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package testutil

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/pkg/database"
)

// SnapshotSetting records the current value of a system_settings row and
// restores it (or deletes it, if it did not exist) when the test ends.
//
// The credential-sealing tests write canary values into the same table a
// developer's live dev database uses for real keys. Without this they would
// clobber whatever OpenRouter key the developer had configured, and the
// failure would show up much later as "AI stopped working".
func SnapshotSetting(t *testing.T, db *database.DB, key string) {
	t.Helper()
	ctx := context.Background()

	var prev string
	had := db.Pool.QueryRow(ctx, "SELECT value FROM system_settings WHERE key = $1", key).Scan(&prev) == nil

	t.Cleanup(func() {
		if had {
			_, _ = db.Pool.Exec(ctx, "UPDATE system_settings SET value = $2 WHERE key = $1", key, prev)
			return
		}
		_, _ = db.Pool.Exec(ctx, "DELETE FROM system_settings WHERE key = $1", key)
	})
}
