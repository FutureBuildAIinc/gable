// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
)

// TestPutSettingsRefusesAMachineKey: the dealer's large cash settings turn
// the Canadian threshold on or off and name the audit actor, so the write is
// a person's, as the counter rules write is. A key holding admin:settings is
// refused 403 and nothing is saved or audited.
func TestPutSettingsRefusesAMachineKey(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := largecash.NewService(db, nil, "")
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'large_cash.settings.changed' AND changes->'after'->>'ca_threshold_cents' = '777700'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key LIKE 'largecash.%' OR key = 'country.default'`)
	})
	mux := http.NewServeMux()
	allow := func(next http.Handler) http.Handler { return next }
	largecash.NewHandler(svc).RegisterRoutes(mux, allow, allow, allow)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/large-cash/settings",
		strings.NewReader(`{"ca_enabled":false,"ca_threshold_cents":777700}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithKeyID(req.Context(), "key-settings-test"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a machine key writing the large cash settings = %d, want 403: %s", w.Code, w.Body.String())
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'large_cash.settings.changed'
		AND changes->'after'->>'ca_threshold_cents' = '777700'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the refused write left %d audit rows, want 0", n)
	}
}
