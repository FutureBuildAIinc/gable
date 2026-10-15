// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// RULE (item 130, L1 follow up): an account charge is not money in the
// drawer, so a count that names it is refused in words and the till stays
// open. Counted less expected summed over it would post the account's whole
// charge as a shortage.
func TestBlindCountRefusesTheAccountMethod(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	session := f.openTill(20000)
	r := f.do("POST", "/api/v1/pos/till/"+session+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 20000, "account": 0}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest {
		t.Fatalf("a count naming account = %d, want 400: %s", r.status, r.raw)
	}
	if !strings.Contains(string(r.raw), "counted_by_method.account") {
		t.Errorf("the refusal does not name the field: %s", r.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM till_sessions WHERE id = $1 AND status = 'OPEN'`, session); n != 1 {
		t.Errorf("the till was closed by a refused count")
	}
	ok := f.do("POST", "/api/v1/pos/till/"+session+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 20000}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if ok.status != http.StatusOK {
		t.Errorf("a cash count = %d, want 200: %s", ok.status, ok.raw)
	}
}
