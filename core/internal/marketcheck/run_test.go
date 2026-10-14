// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/marketcheck"
	"github.com/google/uuid"
)

func rowOf(t *testing.T, res *marketcheck.Result, sku string) marketcheck.Row {
	t.Helper()
	for _, r := range res.Rows {
		if r.SKU == sku {
			return r
		}
	}
	t.Fatalf("no row for %s in %+v", sku, res.Rows)
	return marketcheck.Row{}
}

func TestRun_NotConfigured(t *testing.T) {
	svc, _ := newSvc(true)
	_, err := svc.Run(context.Background(), []uuid.UUID{uuid.New()})
	if err != marketcheck.ErrNotConfigured {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}

func TestRun_RequestShapeHoldsNoCostAndTheRowsAreOursRecomputed(t *testing.T) {
	a := product("DL_SPF_2X4_L08_S", 542, 300)
	b := product("DL_SPF_2X6_L08_S", 900, 700)
	// the flow lies about the difference: our figures are recomputed
	stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
		_, _ = fmt.Fprint(w, `{"store":{"id":"1234","name":"  Spokane Store "},"rows":[
		 {"sku":"DL_SPF_2X4_L08_S","status":"matched","diffPercent":-99,"position":"below","match":{"title":"HD stud","theirPrice":4.00,"listedPrice":40.00,"link":"https://www.homedepot.com/p/1","confidence":150,"normNote":"Sold as a pack of 10; price divided by 10"},"note":""},
		 {"sku":"DL_SPF_2X6_L08_S","status":"unmatched","note":"No equivalent found"},
		 {"sku":"NOT_SENT","status":"matched","match":{"title":"x","theirPrice":1,"listedPrice":1,"link":"","confidence":1,"normNote":""}}]}`)
	})
	cat := catalogOf(a, b)
	svc, _, _ := configured(t, stub, cat, func(in *marketcheck.SettingsInput) {
		in.StoreID, in.StoreZip, in.FlowName = ptr("1234"), ptr("99201"), ptr("Pricing flow")
	})
	res, err := svc.Run(context.Background(), ids(a, b))
	if err != nil {
		t.Fatal(err)
	}
	// the request: exactly the documented shape
	if len(stub.bodies) != 1 {
		t.Fatalf("batches: %d", len(stub.bodies))
	}
	want := `{"source":"gable","yard":{"name":"Kelowna Main Yard","town":"Kelowna, BC"},` +
		`"store":{"id":"1234","zip":"99201","label":"Home Depot, Spokane WA: nearest store with live prices"},` +
		`"bandPercent":5,"items":[` +
		`{"sku":"DL_SPF_2X4_L08_S","name":"Desc DL_SPF_2X4_L08_S","category":"Lumber","unit":"PCS","price":5.42},` +
		`{"sku":"DL_SPF_2X6_L08_S","name":"Desc DL_SPF_2X6_L08_S","category":"Lumber","unit":"PCS","price":9}]}`
	var gotJSON, wantJSON any
	_ = json.Unmarshal([]byte(stub.bodies[0]), &gotJSON)
	_ = json.Unmarshal([]byte(want), &wantJSON)
	if fmt.Sprint(gotJSON) != fmt.Sprint(wantJSON) {
		t.Fatalf("body\n got %s\nwant %s", stub.bodies[0], want)
	}
	if strings.Contains(stub.bodies[0], "cost") || strings.Contains(stub.bodies[0], "300") || strings.Contains(stub.bodies[0], "700") {
		t.Fatalf("the request leaks a cost: %s", stub.bodies[0])
	}
	if stub.paths[0] != "POST /api/v1/webhooks/"+testHook+"/sync application/json" {
		t.Fatalf("call: %q", stub.paths[0])
	}
	// the answer
	if res.FlowName != "Pricing flow" || res.StoreSeen == nil || *res.StoreSeen != "Spokane Store" ||
		res.BandPct != "5" || res.MarginFloorPct != "15" {
		t.Fatalf("header: %+v", res)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows (the unknown sku must be ignored): %+v", res.Rows)
	}
	r := rowOf(t, res, "DL_SPF_2X4_L08_S")
	if r.Status != "matched" || r.Competitor == nil || r.Competitor.PriceCents != 400 ||
		r.Competitor.ListedPriceCents != 4000 || r.Competitor.Confidence != 100 ||
		r.Competitor.Link != "https://www.homedepot.com/p/1" ||
		r.Competitor.UnitNote != "Sold as a pack of 10; price divided by 10" {
		t.Fatalf("matched row: %+v %+v", r, r.Competitor)
	}
	// ours 542 vs 400: +35.5 percent, above; suggestion max(floor(400*1.05)=420, ceil(300/.85)=353) = 420
	if r.OurPriceCents != 542 || r.DiffPct == nil || *r.DiffPct != 35.5 || r.Position != "above" ||
		r.SuggestedPriceCents == nil || *r.SuggestedPriceCents != 420 || r.Reason != "lower_to_band" || !r.CostKnown {
		t.Fatalf("our figures: %+v", r)
	}
	u := rowOf(t, res, "DL_SPF_2X6_L08_S")
	if u.Status != "unmatched" || u.Competitor != nil || u.Note != "No equivalent found" || u.OurPriceCents != 900 {
		t.Fatalf("unmatched row: %+v", u)
	}
	out, _ := json.Marshal(res)
	if strings.Contains(string(out), testHook) || strings.Contains(string(out), stub.URL) {
		t.Fatalf("the answer leaks the id or the address: %s", out)
	}
}

func TestRun_NoCostGivesNoSuggestion(t *testing.T) {
	a := product("SKU_A", 1000, 0)
	stub := newFlow(t, matchAll(8.00))
	svc, _, _ := configured(t, stub, catalogOf(a), nil)
	res, err := svc.Run(context.Background(), ids(a))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Rows[0]
	if r.CostKnown || r.SuggestedPriceCents != nil || r.Note != "No cost on file, so no price is suggested" {
		t.Fatalf("%+v", r)
	}
}

func TestRun_BadRowsAreErrors(t *testing.T) {
	ps := []marketcheck.Product{product("S1", 500, 100), product("S2", 500, 100), product("S3", 500, 100)}
	stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
		_, _ = fmt.Fprint(w, `{"store":null,"rows":[
		 {"sku":"S1","status":"matched","match":{"title":"neg","theirPrice":-3,"listedPrice":1,"link":"","confidence":5,"normNote":""}},
		 {"sku":"S2","status":"matched","match":{"title":"huge","theirPrice":100000,"listedPrice":1,"link":"","confidence":5,"normNote":""}},
		 {"sku":"S3","status":"matched","match":{"title":"ok\u0007bell","theirPrice":4.5,"listedPrice":4.5,"link":"https://evil.example/p","confidence":-4,"normNote":""}}]}`)
	})
	svc, _, _ := configured(t, stub, catalogOf(ps...), nil)
	res, err := svc.Run(context.Background(), ids(ps...))
	if err != nil {
		t.Fatal(err)
	}
	if res.StoreSeen != nil {
		t.Fatalf("store_seen should be null: %v", *res.StoreSeen)
	}
	if rowOf(t, res, "S1").Status != "error" || rowOf(t, res, "S2").Status != "error" {
		t.Fatalf("out of range prices must be error rows: %+v", res.Rows)
	}
	s3 := rowOf(t, res, "S3")
	if s3.Status != "matched" || s3.Competitor.Link != "" || s3.Competitor.Confidence != 0 ||
		strings.ContainsRune(s3.Competitor.Title, 7) {
		t.Fatalf("sanitised row: %+v %+v", s3, s3.Competitor)
	}
}

func TestRun_ItemWithNoRowIsAnError(t *testing.T) {
	a, b := product("S1", 500, 100), product("S2", 500, 100)
	stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
		_, _ = fmt.Fprint(w, `{"store":null,"rows":[{"sku":"S1","status":"matched","match":{"title":"t","theirPrice":5,"listedPrice":5,"link":"","confidence":50,"normNote":""}}]}`)
	})
	svc, _, _ := configured(t, stub, catalogOf(a, b), nil)
	res, err := svc.Run(context.Background(), ids(a, b))
	if err != nil {
		t.Fatal(err)
	}
	r := rowOf(t, res, "S2")
	if r.Status != "error" || r.Note != "The flow did not return this item" {
		t.Fatalf("%+v", r)
	}
}

func TestRun_OneBatchFailingOthersStand(t *testing.T) {
	var ps []marketcheck.Product
	for i := 0; i < 7; i++ {
		ps = append(ps, product(fmt.Sprintf("SKU_%d", i), 1000, 100))
	}
	stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
		if strings.Contains(body, "SKU_3") { // the second batch
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		matchAll(9)(w, r, body)
	})
	svc, _, _ := configured(t, stub, catalogOf(ps...), nil)
	res, err := svc.Run(context.Background(), ids(ps...))
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.bodies) != 3 {
		t.Fatalf("batches of up to 3: got %d calls", len(stub.bodies))
	}
	for i, r := range res.Rows {
		inBad := i >= 3 && i <= 5
		if inBad && (r.Status != "error" || r.Note != "FB Connect did not answer") {
			t.Fatalf("row %d: %+v", i, r)
		}
		if !inBad && r.Status != "matched" {
			t.Fatalf("row %d should stand: %+v", i, r)
		}
	}
}

func TestRun_FailureWords(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		note   string
	}{
		"404":   {404, "{}", "No flow answers at that webhook id: check it in Tech Admin"},
		"400":   {400, "{}", "No flow answers at that webhook id: check it in Tech Admin"},
		"408":   {408, "{}", "FB Connect timed out waiting for the run"},
		"500":   {500, "{}", "FB Connect did not answer"},
		"badjs": {200, "{not json", "FB Connect did not answer"},
		"302":   {302, "", "FB Connect did not answer"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			a := product("S1", 500, 100)
			redirected := false
			target := newFlow(t, func(w http.ResponseWriter, r *http.Request, b string) { redirected = true })
			stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
				if c.status == 302 {
					http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
					return
				}
				w.WriteHeader(c.status)
				_, _ = fmt.Fprint(w, c.body)
			})
			svc, _, _ := configured(t, stub, catalogOf(a), nil)
			res, err := svc.Run(context.Background(), ids(a))
			if err != nil {
				t.Fatal(err)
			}
			if r := res.Rows[0]; r.Status != "error" || r.Note != c.note {
				t.Fatalf("%+v", r)
			}
			if redirected {
				t.Fatal("a redirect was followed")
			}
		})
	}
}

func TestRun_TimeoutIsTimedOutWords(t *testing.T) {
	a := product("S1", 500, 100)
	stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
		time.Sleep(400 * time.Millisecond)
	})
	st := newMemStore()
	svc := marketcheck.NewService(st, catalogOf(a)).WithVault(fakeVault{present: true}).WithTxRunner(passTx{}).
		WithBatchTimeout(50 * time.Millisecond)
	if _, err := svc.SaveSettings(context.Background(), marketcheck.SettingsInput{ConnectURL: ptr(stub.URL), WebhookID: ptr(testHook)}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Run(context.Background(), ids(a))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Rows[0]; r.Status != "error" || r.Note != "FB Connect timed out waiting for the run" {
		t.Fatalf("%+v", r)
	}
}

func TestRun_OversizedAnswerRefused(t *testing.T) {
	a := product("S1", 500, 100)
	stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
		_, _ = fmt.Fprintf(w, `{"store":null,"rows":[],"pad":"%s"}`, strings.Repeat("x", 1<<20+10))
	})
	svc, _, _ := configured(t, stub, catalogOf(a), nil)
	res, err := svc.Run(context.Background(), ids(a))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Rows[0]; r.Status != "error" || r.Note != "FB Connect did not answer" {
		t.Fatalf("%+v", r)
	}
}

func TestRun_PickBounds(t *testing.T) {
	a := product("S1", 500, 100)
	stub := newFlow(t, matchAll(5))
	svc, _, _ := configured(t, stub, catalogOf(a), nil)
	if _, err := svc.Run(context.Background(), nil); err == nil {
		t.Fatal("empty pick accepted")
	}
	if _, err := svc.Run(context.Background(), []uuid.UUID{a.ID, a.ID}); err == nil {
		t.Fatal("duplicate pick accepted")
	}
	var many []uuid.UUID
	for i := 0; i < 13; i++ {
		many = append(many, uuid.New())
	}
	if _, err := svc.Run(context.Background(), many); err == nil {
		t.Fatal("13 accepted")
	}
}

func TestRun_UnknownProductIsAnErrorRow(t *testing.T) {
	stub := newFlow(t, matchAll(5))
	svc, _, _ := configured(t, stub, catalogOf(), nil)
	res, err := svc.Run(context.Background(), []uuid.UUID{uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Rows[0]; r.Status != "error" || len(stub.bodies) != 0 {
		t.Fatalf("%+v calls %d", r, len(stub.bodies))
	}
}

func TestRun_LogsCarryNeitherTheIDNorTheAddress(t *testing.T) {
	a := product("S1", 500, 100)
	for _, status := range []int{200, 500, 404} {
		stub := newFlow(t, func(w http.ResponseWriter, r *http.Request, body string) {
			if status != 200 {
				w.WriteHeader(status)
				return
			}
			matchAll(5)(w, r, body)
		})
		svc, _, cap := configured(t, stub, catalogOf(a), nil)
		res, err := svc.Run(context.Background(), ids(a))
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(res)
		all := cap.String() + string(out)
		if strings.Contains(all, testHook) || strings.Contains(all, "/api/v1/webhooks") || strings.Contains(all, stub.URL) {
			t.Fatalf("status %d leaks:\n%s", status, all)
		}
		if !strings.Contains(cap.String(), "127.0.0.1") {
			t.Fatalf("the batch outcome log lacks the host: %q", cap.String())
		}
	}
	// a dial failure's error text carries the URL; it must not be logged or answered
	svc := marketcheck.NewService(newMemStore(), catalogOf(a)).WithVault(fakeVault{present: true}).WithTxRunner(passTx{})
	lg, cap := newLogger()
	svc.WithLogger(lg)
	if _, err := svc.SaveSettings(context.Background(), marketcheck.SettingsInput{
		ConnectURL: ptr("http://127.0.0.1:1"), WebhookID: ptr(testHook)}); err != nil {
		t.Fatal(err)
	}
	res, _ := svc.Run(context.Background(), ids(a))
	out, _ := json.Marshal(res)
	if strings.Contains(cap.String()+string(out), testHook) {
		t.Fatalf("dial failure leaks the id: %s %s", cap.String(), out)
	}
}
