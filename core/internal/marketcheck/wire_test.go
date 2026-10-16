// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck_test

// The Market check on the wire, against a real Postgres: the real handler on a
// real mux behind the role guards, the real settings store and the real vault.
// The flow is a stub of FB Connect on a loopback port.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/marketcheck"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// A made up vault key: 32 bytes of an obvious repeating placeholder.
var testVaultKey = strings.Repeat("ab", 32)

type wireFixture struct {
	t    *testing.T
	db   *database.DB
	srv  *httptest.Server
	svc  *marketcheck.Service
	logs *logCapture
}

func claimsAs(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role != "" {
			c := &middleware.UserClaims{Role: role}
			r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, c))
		}
		next.ServeHTTP(w, r)
	})
}

func cleanMarketRows(db *database.DB) {
	_, _ = db.Pool.Exec(context.Background(), `DELETE FROM system_settings WHERE key LIKE 'market_check.%'`)
}

func newWire(t *testing.T, vaultKey string, maxConns int32) *wireFixture {
	t.Helper()
	db := testutil.RequireDBMaxConns(t, maxConns)
	vault, err := payment.NewVault(vaultKey)
	if err != nil {
		t.Fatal(err)
	}
	lg, cap := newLogger()
	svc := marketcheck.NewService(marketcheck.NewPostgresStore(db), marketcheck.NewPostgresCatalog(db)).
		WithVault(vault).WithTxRunner(db).WithLogger(lg).WithBatchTimeout(2 * time.Second)
	mux := http.NewServeMux()
	guard := middleware.RequireRole("admin", "owner")
	marketcheck.NewHandler(svc).RegisterRoutes(mux, guard, guard)
	f := &wireFixture{t: t, db: db, svc: svc, logs: cap}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claimsAs(r.Header.Get("X-Test-Role"), mux).ServeHTTP(w, r)
	}))
	cleanMarketRows(db)
	t.Cleanup(func() { f.srv.Close(); cleanMarketRows(db) })
	return f
}

func (f *wireFixture) do(role, method, path string, body any) (int, string) {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Role", role)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (f *wireFixture) seedProduct(sku string, price, cost float64) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost, category)
		VALUES ($1, $2, 'market check test product', 'PCS', $3, $4, 'Lumber')`, id, sku, price, cost); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM products WHERE id = $1`, id) })
	return id
}

func TestWire_SettingsSealedAtRestAndNeverAnswered(t *testing.T) {
	f := newWire(t, testVaultKey, 10)
	code, body := f.do("admin", "PUT", "/api/v1/admin/market-check/settings", map[string]any{
		"connect_url": "https://connect.example.test", "webhook_id": testHook, "band_pct": "6", "flow_name": "Pricing flow"})
	if code != 200 || strings.Contains(body, testHook) || !strings.Contains(body, `"configured":true`) {
		t.Fatalf("put: %d %s", code, body)
	}
	code, body = f.do("owner", "GET", "/api/v1/admin/market-check/settings", nil)
	if code != 200 || strings.Contains(body, testHook) || !strings.Contains(body, `"webhook_id_saved":true`) ||
		!strings.Contains(body, `"band_pct":"6"`) {
		t.Fatalf("get: %d %s", code, body)
	}
	var stored string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT value FROM system_settings WHERE key = 'market_check.webhook_id'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "enc:v1:") || strings.Contains(stored, testHook) {
		t.Fatalf("stored value: %q", stored)
	}
	var plain int
	_ = f.db.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM system_settings WHERE value LIKE '%' || $1 || '%'`, testHook).Scan(&plain)
	if plain != 0 {
		t.Fatalf("the id is stored in plain text in %d rows", plain)
	}
	// unknown fields are refused
	if code, _ = f.do("admin", "PUT", "/api/v1/admin/market-check/settings", map[string]any{"cost": 1}); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
}

func TestWire_NoVaultKeyRefusesInWords(t *testing.T) {
	f := newWire(t, "", 10)
	code, body := f.do("admin", "PUT", "/api/v1/admin/market-check/settings", map[string]any{
		"connect_url": "https://connect.example.test", "webhook_id": testHook})
	if code != 400 || !strings.Contains(body, "vault") || strings.Contains(body, testHook) {
		t.Fatalf("%d %s", code, body)
	}
	var n int
	_ = f.db.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM system_settings WHERE key LIKE 'market_check.%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("a refused save wrote %d rows", n)
	}
}

func TestWire_RolesAreWalled(t *testing.T) {
	f := newWire(t, testVaultKey, 10)
	for _, role := range []string{"cashier", "sales", "driver"} {
		for _, rt := range []struct{ m, p string }{
			{"GET", "/api/v1/admin/market-check/settings"},
			{"PUT", "/api/v1/admin/market-check/settings"},
			{"POST", "/api/v1/pricing/market-check"},
		} {
			code, _ := f.do(role, rt.m, rt.p, map[string]any{"product_ids": []string{uuid.NewString()}})
			if code != http.StatusForbidden {
				t.Fatalf("%s %s %s: %d", role, rt.m, rt.p, code)
			}
		}
	}
}

func TestWire_RunEndToEnd(t *testing.T) {
	f := newWire(t, testVaultKey, 10)
	p := f.seedProduct("MC_TEST_SKU_1", 5.42, 3.00)
	var calls int
	var mu sync.Mutex
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		if r.URL.Path != "/api/v1/webhooks/"+testHook+"/sync" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"store":{"id":"1","name":"Spokane"},"rows":[{"sku":"MC_TEST_SKU_1","status":"matched","match":{"title":"Stud","theirPrice":4.00,"listedPrice":4.00,"link":"https://www.homedepot.com/p/1","confidence":88,"normNote":""}}]}`))
	}))
	defer stub.Close()
	// not configured first
	if code, body := f.do("admin", "POST", "/api/v1/pricing/market-check", map[string]any{"product_ids": []string{p.String()}}); code != 409 || !strings.Contains(body, "not_configured") {
		t.Fatalf("not configured: %d %s", code, body)
	}
	if code, body := f.do("admin", "PUT", "/api/v1/admin/market-check/settings", map[string]any{
		"connect_url": stub.URL, "webhook_id": testHook}); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	code, body := f.do("admin", "POST", "/api/v1/pricing/market-check", map[string]any{"product_ids": []string{p.String()}})
	if code != 200 {
		t.Fatalf("run: %d %s", code, body)
	}
	var res marketcheck.Result
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	r := res.Rows[0]
	if r.Status != "matched" || r.OurPriceCents != 542 || r.Position != "above" || r.DiffPct == nil || *r.DiffPct != 35.5 ||
		r.SuggestedPriceCents == nil || *r.SuggestedPriceCents != 420 {
		t.Fatalf("row: %+v", r)
	}
	if strings.Contains(body, testHook) || strings.Contains(body, "average_unit_cost") || strings.Contains(f.logs.String(), testHook) {
		t.Fatalf("a leak:\n%s\n%s", body, f.logs.String())
	}
	if code, _ = f.do("admin", "POST", "/api/v1/pricing/market-check", map[string]any{"product_ids": []string{}}); code != 400 {
		t.Fatalf("empty pick: %d", code)
	}
}

// The settings write opens a transaction: at pool size 4 with three
// contenders every save must finish (a pool read inside the transaction would
// deadlock here).
func TestWire_SettingsSaveConcurrencyPoolOfFour(t *testing.T) {
	f := newWire(t, testVaultKey, 4)
	var wg sync.WaitGroup
	errs := make(chan error, 3*8)
	for c := 0; c < 3; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			for i := 0; i < 8; i++ {
				_, err := f.svc.SaveSettings(ctx, marketcheck.SettingsInput{
					ConnectURL: ptr("https://connect.example.test"), WebhookID: ptr(testHook),
					FlowName: ptr("flow"), StoreZip: ptr("99201")})
				errs <- err
			}
		}(c)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a contender failed: %v", err)
		}
	}
	got, err := f.svc.Settings(context.Background())
	if err != nil || !got.Configured {
		t.Fatalf("%+v %v", got, err)
	}
}
