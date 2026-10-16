// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck_test

// Fix round 1 of PR 145: the run's write deadline, the stored settings
// re-validated at run time, the dial guard, the id following a changed
// address, and the small ones.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/marketcheck"
)

func TestHandler_RunSurvivesAShortServerWriteTimeout(t *testing.T) {
	a := product("S1", 500, 100)
	stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
		time.Sleep(700 * time.Millisecond)
		matchAll(4)(w, r, body)
	})
	svc, _, _ := configured(t, stub, catalogOf(a), nil)
	svc.WithBatchTimeout(5 * time.Second)
	mux := http.NewServeMux()
	marketcheck.NewHandler(svc).RegisterRoutes(mux, nil, nil)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.WriteTimeout = 300 * time.Millisecond // serve's is 15 seconds; the run outlives it
	srv.Start()
	defer srv.Close()
	body := fmt.Sprintf(`{"product_ids":[%q]}`, a.ID.String())
	resp, err := http.Post(srv.URL+"/api/v1/pricing/market-check", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("the response was cut: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"status":"matched"`) {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
}

func TestHandler_SecondRunOfTheSameCallerIsRefusedWhileOneRuns(t *testing.T) {
	a := product("S1", 500, 100)
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
		started <- struct{}{}
		<-release
		matchAll(4)(w, r, body)
	})
	svc, _, _ := configured(t, stub, catalogOf(a), nil)
	mux := http.NewServeMux()
	marketcheck.NewHandler(svc).RegisterRoutes(mux, nil, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	body := fmt.Sprintf(`{"product_ids":[%q]}`, a.ID.String())
	first := make(chan int, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/api/v1/pricing/market-check", "application/json", strings.NewReader(body))
		if err != nil {
			first <- -1
			return
		}
		resp.Body.Close()
		first <- resp.StatusCode
	}()
	<-started
	resp, err := http.Post(srv.URL+"/api/v1/pricing/market-check", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 429 || !strings.Contains(string(raw), "already") {
		t.Fatalf("second run: %d %s", resp.StatusCode, raw)
	}
	close(release)
	if code := <-first; code != 200 {
		t.Fatalf("first run: %d", code)
	}
	// and once it is done a new run is accepted
	resp, err = http.Post(srv.URL+"/api/v1/pricing/market-check", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("after: %d", resp.StatusCode)
	}
}

// rawStore returns a service whose stored rows were written straight into
// system_settings (a path other than the PUT), sealed with the fake vault.
func rawStore(t *testing.T, rows map[string]string) *marketcheck.Service {
	t.Helper()
	st := newMemStore()
	for k, v := range rows {
		st.rows[k] = v
	}
	return marketcheck.NewService(st, catalogOf()).WithVault(fakeVault{present: true}).WithTxRunner(passTx{}).
		WithBatchTimeout(time.Second)
}

func sealed(v string) string { s, _ := fakeVault{}.Seal(v); return s }

func TestRun_StoredSettingsAreRevalidated(t *testing.T) {
	cases := map[string]map[string]string{
		"metadata address over http": {"market_check.connect_url": "http://169.254.169.254", "market_check.webhook_id": sealed(testHook)},
		"address with user info":     {"market_check.connect_url": "https://u:p@connect.example.test", "market_check.webhook_id": sealed(testHook)},
		"address with a path":        {"market_check.connect_url": "https://connect.example.test/x?", "market_check.webhook_id": sealed(testHook)},
		"id not sealed":              {"market_check.connect_url": "https://connect.example.test", "market_check.webhook_id": "x/../../other?"},
		"sealed id off pattern":      {"market_check.connect_url": "https://connect.example.test", "market_check.webhook_id": sealed("x/../../other?")},
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			svc := rawStore(t, rows)
			_, err := svc.Run(context.Background(), ids(product("S1", 500, 100)))
			if err != marketcheck.ErrNotConfigured {
				t.Fatalf("want ErrNotConfigured, got %v", err)
			}
			s, _ := svc.Settings(context.Background())
			if s.Configured {
				t.Fatalf("settings still read configured: %+v", s)
			}
		})
	}
}

func TestDialGuard(t *testing.T) {
	refused := []string{
		"169.254.169.254:80", "169.254.10.1:443", "[fe80::1]:443", "0.0.0.0:80", "[::]:80",
		"224.0.0.1:80", "[ff02::1]:80", "[fd00:ec2::254]:80",
	}
	for _, a := range refused {
		for _, allowPrivate := range []bool{false, true} {
			if err := marketcheck.RefusedDial(a, allowPrivate); err == nil {
				t.Errorf("%s allowed (allowPrivate %v)", a, allowPrivate)
			}
		}
	}
	private := []string{"127.0.0.1:80", "[::1]:80", "10.1.2.3:443", "192.168.1.5:443", "172.16.0.9:443", "100.64.1.1:443", "[fd12::1]:443"}
	for _, a := range private {
		if err := marketcheck.RefusedDial(a, false); err == nil {
			t.Errorf("%s allowed to a public name's address", a)
		}
		if err := marketcheck.RefusedDial(a, true); err != nil {
			t.Errorf("%s refused though the stored address is itself private: %v", a, err)
		}
	}
	if err := marketcheck.RefusedDial("93.184.216.34:443", false); err != nil {
		t.Errorf("a public address refused: %v", err)
	}
	for host, want := range map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true, "10.0.0.5": true,
		"100.64.3.3": true, "connect.example.test": false, "93.184.216.34": false} {
		if got := marketcheck.AllowPrivateFor(host); got != want {
			t.Errorf("AllowPrivateFor(%s) = %v", host, got)
		}
	}
}

func TestRun_LinkLocalAddressIsNeverDialled(t *testing.T) {
	a := product("S1", 500, 100)
	lg, cap := newLogger()
	st := newMemStore()
	svc := marketcheck.NewService(st, catalogOf(a)).WithVault(fakeVault{present: true}).WithTxRunner(passTx{}).
		WithLogger(lg).WithBatchTimeout(3 * time.Second)
	// https passes the address validation; the address is link local.
	if _, err := svc.SaveSettings(context.Background(), marketcheck.SettingsInput{
		ConnectURL: ptr("https://169.254.169.254"), WebhookID: ptr(testHook)}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := svc.Run(context.Background(), ids(a))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Rows[0]; r.Status != "error" || r.Note != "FB Connect did not answer" {
		t.Fatalf("%+v", r)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the refusal took %v: it was dialled until timeout", time.Since(start))
	}
	if !strings.Contains(cap.String(), "blocked_address") || strings.Contains(cap.String(), testHook) {
		t.Fatalf("log: %s", cap.String())
	}
}

func TestSettings_ChangedAddressClearsTheSavedID(t *testing.T) {
	svc, st := newSvc(true)
	ctx := context.Background()
	if _, err := svc.SaveSettings(ctx, marketcheck.SettingsInput{ConnectURL: ptr("https://a.example.test"), WebhookID: ptr(testHook)}); err != nil {
		t.Fatal(err)
	}
	// the same address again keeps the id
	got, err := svc.SaveSettings(ctx, marketcheck.SettingsInput{ConnectURL: ptr("https://a.example.test/"), FlowName: ptr("x")})
	if err != nil || !got.WebhookIDSaved {
		t.Fatalf("same address: %+v %v", got, err)
	}
	// a new address alone clears it, in words
	got, err = svc.SaveSettings(ctx, marketcheck.SettingsInput{ConnectURL: ptr("https://b.example.test")})
	if err != nil {
		t.Fatal(err)
	}
	if got.WebhookIDSaved || got.Configured || got.ConnectURL != "https://b.example.test" ||
		got.Notice != "Enter the webhook id again for the new address" {
		t.Fatalf("changed address: %+v", got)
	}
	if _, ok := st.rows["market_check.webhook_id"]; ok {
		t.Fatal("the old id is still stored")
	}
	// a new address with a new id keeps the new id
	if _, err := svc.SaveSettings(ctx, marketcheck.SettingsInput{ConnectURL: ptr("https://c.example.test"), WebhookID: ptr("test-hook-0002")}); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Settings(ctx)
	if !got.WebhookIDSaved || !got.Configured || got.Notice != "" {
		t.Fatalf("new address and id: %+v", got)
	}
}

func TestSettings_UnreadableIDIsNotConfiguredInWords(t *testing.T) {
	st := newMemStore()
	good := marketcheck.NewService(st, catalogOf()).WithVault(fakeVault{present: true}).WithTxRunner(passTx{})
	if _, err := good.SaveSettings(context.Background(), marketcheck.SettingsInput{
		ConnectURL: ptr("https://a.example.test"), WebhookID: ptr(testHook)}); err != nil {
		t.Fatal(err)
	}
	noKey := marketcheck.NewService(st, catalogOf()).WithVault(fakeVault{present: false})
	got, err := noKey.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Configured || !got.WebhookIDSaved || got.Notice != "The saved webhook id cannot be read; enter it again" {
		t.Fatalf("%+v", got)
	}
}

func TestRun_LongLinkIsDropped(t *testing.T) {
	a := product("S1", 500, 100)
	long := "https://www.homedepot.com/p/" + strings.Repeat("a", 2100)
	stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
		_, _ = fmt.Fprintf(w, `{"store":null,"rows":[{"sku":"S1","status":"matched","match":{"title":"t","theirPrice":4,"listedPrice":4,"link":%q,"confidence":50,"normNote":""}}]}`, long)
	})
	svc, _, _ := configured(t, stub, catalogOf(a), nil)
	res, err := svc.Run(context.Background(), ids(a))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Rows[0]; r.Status != "matched" || r.Competitor.Link != "" {
		t.Fatalf("%+v", r.Competitor)
	}
}

func TestRun_BandPercentIsFromTheParsedValue(t *testing.T) {
	a := product("S1", 500, 100)
	stub := newFlow(t, matchAll(4))
	svc, st, _ := configured(t, stub, catalogOf(a), nil)
	st.rows["market_check.band_pct"] = "05" // accepted by the parser, invalid as a JSON number
	st.rows["market_check.margin_floor_pct"] = "15.50"
	res, err := svc.Run(context.Background(), ids(a))
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0].Status != "matched" {
		t.Fatalf("%+v", res.Rows[0])
	}
	var sent struct {
		BandPercent json.Number `json:"bandPercent"`
	}
	if err := json.Unmarshal([]byte(stub.bodies[0]), &sent); err != nil || sent.BandPercent.String() != "5" {
		t.Fatalf("sent %q: %v", stub.bodies[0], err)
	}
}

func TestRun_NeverMoreThanFourBatchesAtOnce(t *testing.T) {
	var ps []marketcheck.Product
	for i := 0; i < 12; i++ {
		ps = append(ps, product(fmt.Sprintf("SKU_%d", i), 500, 100))
	}
	var cur, peak int32
	var mu sync.Mutex
	stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
		n := atomic.AddInt32(&cur, 1)
		mu.Lock()
		if n > peak {
			peak = n
		}
		mu.Unlock()
		time.Sleep(150 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		matchAll(4)(w, r, body)
	})
	svc, _, _ := configured(t, stub, catalogOf(ps...), nil)
	res, err := svc.Run(context.Background(), ids(ps...))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Rows {
		if r.Status != "matched" {
			t.Fatalf("%+v", r)
		}
	}
	if len(stub.bodies) != 4 || peak > 4 || peak < 2 {
		t.Fatalf("batches %d, peak concurrent %d", len(stub.bodies), peak)
	}
}

func TestRun_BelowRowHeldAtTheFloorSaysSo(t *testing.T) {
	a := product("S1", 520, 500) // floor 589; theirs 560, ours 520 is below the band
	stub := newFlow(t, matchAll(5.60))
	svc, _, _ := configured(t, stub, catalogOf(a), nil)
	res, err := svc.Run(context.Background(), ids(a))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Rows[0]
	if r.Position != "below" || r.SuggestedPriceCents == nil || *r.SuggestedPriceCents != 589 || r.Reason != "held_at_floor" {
		t.Fatalf("%+v", r)
	}
}
