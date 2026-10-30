// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Slice O1 (CP-13, ADR 0011 section 4 and section 8): GET and PUT
// /api/v1/pos/offline/policy with branch -> default -> none resolution,
// enrolled register count and worst case per account exposure, role and
// machine key and branch wall guards, synchronous audit, racing writes at
// pool size 4, and the abandoned status filter on GET /api/v1/pos/transactions.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func TestOfflinePolicy_BranchDefaultNoneAndExposure(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ctx := context.Background()
	mustExec(t, f.db, `DELETE FROM pos_offline_policy WHERE branch_id = $1`, f.branchID)
	mustExec(t, f.db, `DELETE FROM pos_register_devices WHERE branch_id = $1`, f.branchID)
	mustExec(t, f.db, `DELETE FROM system_settings WHERE key LIKE 'pos.offline.%'`)
	mustExec(t, f.db, `DELETE FROM audit_log WHERE action = 'pos.offline.policy' AND entity_id = $1::uuid`, f.branchID.String())
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM audit_log WHERE action = 'pos.offline.policy' AND entity_id = $1::uuid`, f.branchID.String())
		mustExec(t, f.db, `DELETE FROM pos_offline_policy WHERE branch_id = $1`, f.branchID)
		mustExec(t, f.db, `DELETE FROM pos_register_devices WHERE branch_id = $1`, f.branchID)
		mustExec(t, f.db, `DELETE FROM system_settings WHERE key LIKE 'pos.offline.%'`)
	})

	fin := mustUUID(t)
	finHdr := []string{"X-Test-Role", "finance", "X-Test-Sub", fin}
	admHdr := []string{"X-Test-Role", "admin", "X-Test-Sub", fin}

	// 1. Neither branch row nor system_settings set: source "none", enabled false.
	r := f.do("GET", "/api/v1/pos/offline/policy?branch_id="+f.branchID.String(), nil, finHdr...)
	if r.status != http.StatusOK {
		t.Fatalf("GET none policy = %d: %s", r.status, r.raw)
	}
	if got := str(t, r.body, "source"); got != "none" {
		t.Errorf("source = %s, want none", got)
	}
	if r.body["enabled"] != false {
		t.Errorf("enabled = %v, want false", r.body["enabled"])
	}
	if tenders, ok := r.body["tenders"].([]any); !ok || len(tenders) != 0 {
		t.Errorf("tenders = %v, want []", r.body["tenders"])
	}

	// 2. Database wide defaults in system_settings: source "default".
	for k, v := range map[string]string{
		"pos.offline.enabled":               "true",
		"pos.offline.tenders":               "cash,check",
		"pos.offline.sale_cap_cents":        "150000",
		"pos.offline.check_cap_cents":       "75000",
		"pos.offline.account_cap_cents":     "200000",
		"pos.offline.queue_total_cap_cents": "600000",
		"pos.offline.max_queued":            "15",
	} {
		setPosSetting(t, f.db, k, v)
	}
	// Enrol two registers on the branch (one active on REG-01, one active on a second register, plus one revoked).
	reg2 := "REG-OFF-" + uuid.New().String()[:6]
	mustExec(t, f.db, `INSERT INTO pos_registers (id, name, location_id, branch_id, is_active) VALUES ($1, 'Offline Reg 2', $2, $2, TRUE)`, reg2, f.branchID)
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM pos_register_devices WHERE register_id = $1`, reg2)
		mustExec(t, f.db, `DELETE FROM pos_registers WHERE id = $1`, reg2)
	})
	mustExec(t, f.db, `
		INSERT INTO pos_register_devices (register_id, branch_id, epoch, public_key, enrolled_by, revoked_at)
		VALUES
			($1, $3, 1, 'pub-old', 'mgr', NOW()),
			($1, $3, 2, 'pub-active-1', 'mgr', NULL),
			($2, $3, 1, 'pub-active-2', 'mgr', NULL)`,
		f.register, reg2, f.branchID)

	r = f.do("GET", "/api/v1/pos/offline/policy?branch_id="+f.branchID.String(), nil, finHdr...)
	if r.status != http.StatusOK {
		t.Fatalf("GET default policy = %d: %s", r.status, r.raw)
	}
	if str(t, r.body, "source") != "default" || r.body["enabled"] != true ||
		num(t, r.body, "sale_cap_cents") != 150000 ||
		num(t, r.body, "check_cap_cents") != 75000 ||
		num(t, r.body, "account_cap_cents") != 200000 ||
		num(t, r.body, "queue_total_cap_cents") != 600000 ||
		num(t, r.body, "max_queued") != 15 ||
		num(t, r.body, "enrolled_register_count") != 2 ||
		num(t, r.body, "max_account_exposure_cents") != 400000 {
		t.Errorf("default policy = %s", r.raw)
	}

	// 3. Branch override via PUT /api/v1/pos/offline/policy: source "branch", revision 1 -> 2, audited.
	putBody := map[string]any{
		"branch_id":             f.branchID.String(),
		"enabled":               true,
		"tenders":               []string{"cash", "check", "account"},
		"sale_cap_cents":        250000,
		"check_cap_cents":       100000,
		"account_cap_cents":     300000,
		"queue_total_cap_cents": 900000,
		"max_queued":            20,
	}
	r = f.do("PUT", "/api/v1/pos/offline/policy", putBody, admHdr...)
	if r.status != http.StatusOK {
		t.Fatalf("PUT branch policy = %d: %s", r.status, r.raw)
	}
	if str(t, r.body, "source") != "branch" ||
		num(t, r.body, "revision") != 1 ||
		str(t, r.body, "updated_by") != fin ||
		num(t, r.body, "account_cap_cents") != 300000 ||
		num(t, r.body, "enrolled_register_count") != 2 ||
		num(t, r.body, "max_account_exposure_cents") != 600000 {
		t.Errorf("PUT branch policy body = %s", r.raw)
	}

	putBody["account_cap_cents"] = int64(350000)
	r = f.do("PUT", "/api/v1/pos/offline/policy", putBody, admHdr...)
	if r.status != http.StatusOK || num(t, r.body, "revision") != 2 || num(t, r.body, "max_account_exposure_cents") != 700000 {
		t.Errorf("second PUT branch policy = %d: %s", r.status, r.raw)
	}

	// Clear branch override: falls back to "default".
	r = f.do("PUT", "/api/v1/pos/offline/policy", map[string]any{
		"branch_id": f.branchID.String(),
		"clear":     true,
	}, admHdr...)
	if r.status != http.StatusOK || str(t, r.body, "source") != "default" || num(t, r.body, "account_cap_cents") != 200000 {
		t.Errorf("clear branch policy = %d: %s", r.status, r.raw)
	}

	var audits int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action = 'pos.offline.policy' AND entity_id = $1::uuid`,
		f.branchID.String()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 3 {
		t.Errorf("pos.offline.policy audit rows = %d, want 3", audits)
	}

	// 4. Validation errors, role/machine-key refusals, and unknown branch 404.
	for _, bad := range []any{
		map[string]any{"branch_id": "not-a-uuid", "enabled": true, "tenders": []string{"cash"}},
		map[string]any{"branch_id": f.branchID.String(), "enabled": true, "tenders": []string{"card"}},
		map[string]any{"branch_id": f.branchID.String(), "enabled": true, "tenders": []string{"store_credit"}},
		map[string]any{"branch_id": f.branchID.String(), "enabled": true, "tenders": []string{"CASH"}},
		map[string]any{"branch_id": f.branchID.String(), "enabled": true, "tenders": []string{"cash"}, "sale_cap_cents": -1},
		map[string]any{"branch_id": f.branchID.String(), "enabled": true, "tenders": []string{"cash"}, "max_queued": -1},
		map[string]any{"branch_id": f.branchID.String(), "clear": false},
	} {
		if r := f.do("PUT", "/api/v1/pos/offline/policy", bad, admHdr...); r.status != http.StatusBadRequest {
			t.Errorf("bad PUT %v = %d, want 400: %s", bad, r.status, r.raw)
		}
	}
	if r := f.do("GET", "/api/v1/pos/offline/policy?branch_id=bad", nil, finHdr...); r.status != http.StatusBadRequest {
		t.Errorf("GET bad branch_id = %d, want 400", r.status)
	}
	ghost := uuid.New().String()
	if r := f.do("GET", "/api/v1/pos/offline/policy?branch_id="+ghost, nil, finHdr...); r.status != http.StatusNotFound {
		t.Errorf("GET unknown branch = %d, want 404: %s", r.status, r.raw)
	}
	if r := f.do("PUT", "/api/v1/pos/offline/policy", map[string]any{"branch_id": ghost, "enabled": false, "tenders": []string{}}, admHdr...); r.status != http.StatusNotFound {
		t.Errorf("PUT unknown branch = %d, want 404: %s", r.status, r.raw)
	}
	if r := f.do("PUT", "/api/v1/pos/offline/policy", putBody, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusForbidden {
		t.Errorf("cashier PUT = %d, want 403", r.status)
	}
	// The write is an admin act (ADR 0011 section 4): finance may read it
	// but not write it; admin and owner write; a cashier does neither.
	if r := f.do("PUT", "/api/v1/pos/offline/policy", putBody, finHdr...); r.status != http.StatusForbidden {
		t.Errorf("finance PUT = %d, want 403: %s", r.status, r.raw)
	}
	if r := f.do("PUT", "/api/v1/pos/offline/policy", putBody, "X-Test-Role", "owner", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Errorf("owner PUT = %d, want 200: %s", r.status, r.raw)
	}
	for _, role := range []string{"admin", "owner", "finance"} {
		if r := f.do("GET", "/api/v1/pos/offline/policy?branch_id="+f.branchID.String(), nil, "X-Test-Role", role, "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
			t.Errorf("%s GET = %d, want 200: %s", role, r.status, r.raw)
		}
	}
	if r := f.do("GET", "/api/v1/pos/offline/policy?branch_id="+f.branchID.String(), nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusForbidden {
		t.Errorf("cashier GET = %d, want 403: %s", r.status, r.raw)
	}
	keyed := keyedServer(f, uuid.NewString())
	keyReq, err := http.NewRequest(http.MethodPut, keyed.URL+"/api/v1/pos/offline/policy",
		strings.NewReader(`{"branch_id":"`+f.branchID.String()+`","enabled":false}`))
	if err != nil {
		t.Fatal(err)
	}
	keyReq.Header.Set("Content-Type", "application/json")
	keyResp, err := http.DefaultClient.Do(keyReq)
	if err != nil {
		t.Fatal(err)
	}
	defer keyResp.Body.Close()
	if keyResp.StatusCode != http.StatusForbidden {
		t.Errorf("machine key PUT = %d, want 403", keyResp.StatusCode)
	}
}

func TestOfflinePolicy_CarriesTheBranchWall(t *testing.T) {
	w := newWallFixture(t)
	scoped := append([]string{}, w.scoped...)
	scoped[1] = "admin"

	if r := w.do("GET", "/api/v1/pos/offline/policy?branch_id="+w.branchID.String(), nil, scoped...); r.status != http.StatusForbidden {
		t.Errorf("branch A offline policy read by branch B caller = %d, want 403: %s", r.status, r.raw)
	}
	if r := w.do("PUT", "/api/v1/pos/offline/policy", map[string]any{
		"branch_id": w.branchID.String(),
		"enabled":   true,
		"tenders":   []string{"cash"},
	}, scoped...); r.status != http.StatusForbidden {
		t.Errorf("branch A offline policy write by branch B caller = %d, want 403: %s", r.status, r.raw)
	}
	if got := countOf(t, w.db, `SELECT count(*) FROM pos_offline_policy WHERE branch_id = $1`, w.branchID); got != 0 {
		t.Errorf("%d policy rows written through the branch wall, want 0", got)
	}
}

func TestRacingOfflinePolicyWritesProduceMonotonicRevisionsAndMatchingAuditRows(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	mustExec(t, db, `DELETE FROM pos_offline_policy WHERE branch_id = $1`, f.branchID)
	mustExec(t, db, `DELETE FROM audit_log WHERE action = 'pos.offline.policy' AND entity_id = $1::uuid`, f.branchID.String())
	t.Cleanup(func() {
		mustExec(t, db, `DELETE FROM pos_offline_policy WHERE branch_id = $1`, f.branchID)
		mustExec(t, db, `DELETE FROM audit_log WHERE action = 'pos.offline.policy' AND entity_id = $1::uuid`, f.branchID.String())
	})

	const contenders = 3
	start := make(chan struct{})
	var wg sync.WaitGroup
	statuses := make([]int, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sub := uuid.New().String()
			<-start
			r := f.do("PUT", "/api/v1/pos/offline/policy", map[string]any{
				"branch_id":             f.branchID.String(),
				"enabled":               true,
				"tenders":               []string{"cash", "check"},
				"sale_cap_cents":        int64(100000 + idx*10000),
				"check_cap_cents":       50000,
				"account_cap_cents":     0,
				"queue_total_cap_cents": 500000,
				"max_queued":            10 + idx,
			}, "X-Test-Role", "admin", "X-Test-Sub", sub)
			statuses[idx] = r.status
		}(i)
	}
	close(start)
	wg.Wait()

	for i, st := range statuses {
		if st != http.StatusOK {
			t.Fatalf("contender %d status = %d, want 200", i, st)
		}
	}
	if rev := countOf(t, db, `SELECT revision::int FROM pos_offline_policy WHERE branch_id = $1`, f.branchID); rev != contenders {
		t.Errorf("final revision = %d, want %d", rev, contenders)
	}
	if audits := countOf(t, db, `SELECT count(*) FROM audit_log WHERE action = 'pos.offline.policy' AND entity_id = $1::uuid`, f.branchID.String()); audits != contenders {
		t.Errorf("audit rows = %d, want %d", audits, contenders)
	}
}

func TestListTransactions_FiltersAbandonedStatus(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	abandonedID := uuid.New()
	mustExec(t, f.db, `
		INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency, status)
		VALUES ($1, $2, $3, $4, 'USD', 'ABANDONED')`,
		abandonedID, f.register, uuid.New(), f.branchID)
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM pos_transactions WHERE id = $1`, abandonedID)
	})

	r := f.do("GET", "/api/v1/pos/transactions?status=abandoned&register_id="+f.register, nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET transactions?status=abandoned = %d: %s", r.status, r.raw)
	}
	items, ok := r.body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v, want 1 abandoned sale", r.body["items"])
	}
	first := items[0].(map[string]any)
	if str(t, first, "id") != abandonedID.String() || str(t, first, "status") != "abandoned" {
		t.Errorf("abandoned item = %v", first)
	}
}
