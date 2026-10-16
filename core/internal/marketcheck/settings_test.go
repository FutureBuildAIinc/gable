// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/marketcheck"
)

func newSvc(vault bool) (*marketcheck.Service, *memStore) {
	st := newMemStore()
	return marketcheck.NewService(st, catalogOf()).WithVault(fakeVault{present: vault}).WithTxRunner(passTx{}), st
}

func TestSettings_DefaultsWhenUnset(t *testing.T) {
	svc, _ := newSvc(true)
	got, err := svc.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Configured || got.WebhookIDSaved || got.FlowName != "FB Connect pricing flow" ||
		got.StoreLabel != "" || got.BandPct != "5" || got.MarginFloorPct != "15" {
		t.Fatalf("defaults: %+v", got)
	}
}

func TestSettings_RoundTripNeverAnswersTheID(t *testing.T) {
	svc, st := newSvc(true)
	ctx := context.Background()
	in := marketcheck.SettingsInput{ConnectURL: ptr("https://connect.example.test"), WebhookID: ptr(testHook),
		FlowName: ptr("Pricing flow"), StoreLabel: ptr("Home Depot, Spokane WA"), StoreID: ptr("1234"),
		StoreZip: ptr("99201"), BandPct: ptr("7.5"), MarginFloorPct: ptr("20")}
	got, err := svc.SaveSettings(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Configured || !got.WebhookIDSaved || got.ConnectURL != "https://connect.example.test" ||
		got.FlowName != "Pricing flow" || got.StoreID != "1234" || got.StoreZip != "99201" ||
		got.BandPct != "7.5" || got.MarginFloorPct != "20" {
		t.Fatalf("saved: %+v", got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), testHook) || strings.Contains(string(b), "webhook_id\"") {
		t.Fatalf("the answer carries the id: %s", b)
	}
	if !strings.Contains(string(b), `"webhook_id_saved":true`) {
		t.Fatalf("answer lacks the saved flag: %s", b)
	}
	// sealed at rest
	raw := st.rows["market_check.webhook_id"]
	if !strings.HasPrefix(raw, "enc:v1:") || strings.Contains(raw, testHook) {
		t.Fatalf("stored value is not sealed: %q", raw)
	}
	// a PUT with webhook_id absent keeps the id; an empty string clears it
	got, err = svc.SaveSettings(ctx, marketcheck.SettingsInput{FlowName: ptr("Renamed")})
	if err != nil || !got.WebhookIDSaved || got.FlowName != "Renamed" || got.ConnectURL == "" {
		t.Fatalf("keep: %+v %v", got, err)
	}
	got, err = svc.SaveSettings(ctx, marketcheck.SettingsInput{WebhookID: ptr("")})
	if err != nil || got.WebhookIDSaved || got.Configured {
		t.Fatalf("clear: %+v %v", got, err)
	}
	if _, ok := st.rows["market_check.webhook_id"]; ok {
		t.Fatal("the cleared id is still stored")
	}
}

func TestSettings_RefusedWithNoVaultKey(t *testing.T) {
	svc, st := newSvc(false)
	_, err := svc.SaveSettings(context.Background(), marketcheck.SettingsInput{
		ConnectURL: ptr("https://connect.example.test"), WebhookID: ptr(testHook)})
	if err == nil || !strings.Contains(err.Error(), "vault") {
		t.Fatalf("want a refusal naming the vault, got %v", err)
	}
	for k, v := range st.rows {
		if strings.Contains(v, testHook) {
			t.Fatalf("id stored plain under %s", k)
		}
	}
	if len(st.rows) != 0 {
		t.Fatalf("a refused save wrote rows: %v", st.rows)
	}
}

func TestSettings_Validation(t *testing.T) {
	bad := map[string]marketcheck.SettingsInput{
		"connect_url":      {ConnectURL: ptr("http://connect.example.test")},
		"connect_url ":     {ConnectURL: ptr("https://user:pw@connect.example.test")},
		"connect_url  ":    {ConnectURL: ptr("https://connect.example.test/api")},
		"connect_url   ":   {ConnectURL: ptr("https://connect.example.test/?a=b")},
		"connect_url    ":  {ConnectURL: ptr("https://connect.example.test/#frag")},
		"connect_url     ": {ConnectURL: ptr("ftp://connect.example.test")},
		"webhook_id":       {WebhookID: ptr("short")},
		"webhook_id ":      {WebhookID: ptr("has space in it")},
		"webhook_id  ":     {WebhookID: ptr(strings.Repeat("a", 65))},
		"store_id":         {StoreID: ptr("123456789")},
		"store_id ":        {StoreID: ptr("12ab")},
		"store_zip":        {StoreZip: ptr("9920")},
		"store_zip ":       {StoreZip: ptr("99a01")},
		"band_pct":         {BandPct: ptr("51")},
		"band_pct ":        {BandPct: ptr("-1")},
		"band_pct  ":       {BandPct: ptr("five")},
		"margin_floor_pct": {MarginFloorPct: ptr("91")},
		"flow_name":        {FlowName: ptr(strings.Repeat("x", 121))},
		"store_label":      {StoreLabel: ptr("line\nbreak")},
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			svc, st := newSvc(true)
			_, err := svc.SaveSettings(context.Background(), in)
			if err == nil {
				t.Fatal("accepted")
			}
			want := strings.TrimSpace(name)
			if !strings.Contains(err.Error(), want) && !strings.Contains(marketcheck.FieldOf(err), want) {
				t.Fatalf("error does not name %s: %v (field %q)", want, err, marketcheck.FieldOf(err))
			}
			if len(st.rows) != 0 {
				t.Fatalf("a refused save wrote rows: %v", st.rows)
			}
		})
	}
	ok := []marketcheck.SettingsInput{
		{ConnectURL: ptr("http://127.0.0.1:8123")},
		{ConnectURL: ptr("http://localhost:8123/")},
		{ConnectURL: ptr("http://[::1]:8123")},
		{ConnectURL: ptr("https://connect.example.test:8443")},
		{StoreID: ptr("")}, {StoreZip: ptr("")}, {BandPct: ptr("0")}, {BandPct: ptr("50")}, {MarginFloorPct: ptr("90")},
	}
	for _, in := range ok {
		svc, _ := newSvc(true)
		if _, err := svc.SaveSettings(context.Background(), in); err != nil {
			t.Fatalf("%+v refused: %v", in, err)
		}
	}
}
