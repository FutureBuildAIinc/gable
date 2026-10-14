// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pim

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/google/uuid"
)

func aiOffMux(t *testing.T, client *ai.Client) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(newPIMTestService(&stubPIMRepo{}, client)).RegisterRoutes(mux)
	return mux
}

type offBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func postJSON(mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// With no key every PIM generate route says plainly that AI is off: the
// stable code and the sentence, never a 500 or a bare status.
func TestWire_PIMGenerateRoutesSayAIOffWithNoKey(t *testing.T) {
	id := uuid.New().String()
	for _, route := range []string{"descriptions", "seo", "collateral", "image"} {
		t.Run(route, func(t *testing.T) {
			mux := aiOffMux(t, ai.NewClient(""))
			rec := postJSON(mux, "/api/v1/products/"+id+"/pim/generate/"+route, `{}`)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body)
			}
			var b offBody
			if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
				t.Fatal(err)
			}
			if b.Error.Code != ai.CodeAIOff {
				t.Errorf("code = %q, want %q", b.Error.Code, ai.CodeAIOff)
			}
			if !strings.Contains(b.Error.Message, "AI is off") || !strings.Contains(b.Error.Message, "Tech Admin") {
				t.Errorf("message = %q", b.Error.Message)
			}
		})
	}
}

// chanRepo reports each media finalize on a channel, so a test of the route
// (which generates on a detached goroutine) waits without a data race.
type chanRepo struct {
	stubPIMRepo
	done chan mediaUpdate
}

func (r *chanRepo) UpdateMediaResult(_ context.Context, id uuid.UUID, url, genModel, _, status string) error {
	r.done <- mediaUpdate{id: id, url: url, model: genModel, status: status}
	return nil
}

func newChanRepo() *chanRepo { return &chanRepo{done: make(chan mediaUpdate, 1)} }

func (r *chanRepo) wait(t *testing.T) mediaUpdate {
	t.Helper()
	select {
	case u := <-r.done:
		return u
	case <-time.After(5 * time.Second):
		t.Fatal("the media row was never finalized")
		return mediaUpdate{}
	}
}

// fakeAnthropic answers every Messages call with the given drawing and counts
// the calls and the model each carried.
func fakeAnthropic(t *testing.T, svg string, hits *int32, models chan<- string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if models != nil {
			models <- body.Model
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   body.Model,
			"content": []any{map[string]any{"type": "text", "text": svg}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func anthropicClient(url string) *ai.Client {
	return ai.NewClient("or-test-key-not-real").WithProvider(ai.ProviderAnthropic).
		WithAnthropicKey("test-" + "key-not-real").WithAnthropicBaseURL(url)
}

const illustrationWithExtras = `<?xml version="1.0"?>
<svg xmlns="http://www.w3.org/2000/svg" width="400" height="400" viewBox="0 0 256 256">
  <title>A stud</title>
  <rect x="20" y="100" width="216" height="56" fill="#c9a36b" stroke="#7a5a30" stroke-width="2"/>
</svg>`

// With Anthropic chosen the route draws an illustration: Claude is asked,
// the drawing is sanitised, and the row finalizes ready with the SVG data URI
// of what was KEPT. No OpenRouter server receives a thing.
func TestWire_PIMImageUnderAnthropicStoresSanitisedIllustration(t *testing.T) {
	var orHits, anHits int32
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&orHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer or.Close()
	models := make(chan string, 4)
	an := fakeAnthropic(t, illustrationWithExtras, &anHits, models)
	client := anthropicClient(an.URL).WithBaseURL(or.URL)
	repo := newChanRepo()
	mux := http.NewServeMux()
	NewHandler(newPIMTestService(repo, client)).RegisterRoutes(mux)

	rec := postJSON(mux, "/api/v1/products/"+uuid.New().String()+"/pim/generate/image", `{}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	u := repo.wait(t)
	if u.status != "ready" {
		t.Fatalf("status = %q, want ready", u.status)
	}
	const prefix = "data:image/svg+xml;base64,"
	if !strings.HasPrefix(u.url, prefix) {
		t.Fatalf("url = %.40q, want an SVG data URI", u.url)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(u.url, prefix))
	if err != nil {
		t.Fatal(err)
	}
	stored := string(raw)
	if strings.Contains(stored, "<title") || strings.Contains(stored, "<?xml") || strings.Contains(stored, `width="400"`) {
		t.Errorf("the model's text was passed through: %s", stored)
	}
	if !strings.Contains(stored, `viewBox="0 0 256 256"`) || !strings.Contains(stored, "<rect") {
		t.Errorf("the drawing was not kept: %s", stored)
	}
	if got := <-models; got != ai.DefaultAnthropicModel {
		t.Errorf("model sent to Anthropic = %q", got)
	}
	if n := atomic.LoadInt32(&orHits); n != 0 {
		t.Errorf("OpenRouter received %d requests, want 0", n)
	}
	if n := atomic.LoadInt32(&anHits); n != 1 {
		t.Errorf("Anthropic received %d requests, want 1", n)
	}
}

// A drawing that carries an attack is never stored: the row settles failed
// with no URL.
func TestWire_PIMImageUnderAnthropicRefusesAnAttackDrawing(t *testing.T) {
	var hits int32
	bad := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256"><script>alert(1)</script><rect width="9" height="9"/></svg>`
	an := fakeAnthropic(t, bad, &hits, nil)
	repo := newChanRepo()
	mux := http.NewServeMux()
	NewHandler(newPIMTestService(repo, anthropicClient(an.URL))).RegisterRoutes(mux)

	rec := postJSON(mux, "/api/v1/products/"+uuid.New().String()+"/pim/generate/image", `{}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	u := repo.wait(t)
	if u.status != "failed" || u.url != "" {
		t.Errorf("finalize = %+v, want failed with no url", u)
	}
}

// With no Anthropic key saved the route says AI is off: the chosen provider's
// state, even with an OpenRouter key present.
func TestWire_PIMImageUnderAnthropicWithNoKeyIsOff(t *testing.T) {
	client := ai.NewClient("or-test-key-not-real").WithProvider(ai.ProviderAnthropic)
	rec := postJSON(aiOffMux(t, client), "/api/v1/products/"+uuid.New().String()+"/pim/generate/image", `{}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body)
	}
}

// With OpenRouter chosen the photo model is called, as before, and Anthropic
// receives nothing even when a key for it is saved.
func TestWire_PIMImageUnderOpenRouterCallsThePhotoModel(t *testing.T) {
	var modalities int32
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["modalities"]; ok {
			atomic.AddInt32(&modalities, 1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "photo-model-sentinel",
			"choices": []any{map[string]any{"message": map[string]any{
				"images": []any{map[string]any{"image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}},
			}}},
		})
	}))
	defer or.Close()
	var anHits int32
	an := fakeAnthropic(t, illustrationWithExtras, &anHits, nil)
	client := ai.NewClient("or-test-key-not-real").WithBaseURL(or.URL).
		WithAnthropicKey("test-" + "key-not-real").WithAnthropicBaseURL(an.URL)
	repo := newChanRepo()
	mux := http.NewServeMux()
	NewHandler(newPIMTestService(repo, client)).RegisterRoutes(mux)

	rec := postJSON(mux, "/api/v1/products/"+uuid.New().String()+"/pim/generate/image", `{}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	u := repo.wait(t)
	if u.status != "ready" || u.model != "photo-model-sentinel" || !strings.HasPrefix(u.url, "data:image/png") {
		t.Errorf("finalize = %+v", u)
	}
	if atomic.LoadInt32(&modalities) != 1 {
		t.Errorf("the photo model was not called once")
	}
	if n := atomic.LoadInt32(&anHits); n != 0 {
		t.Errorf("Anthropic received %d requests, want 0", n)
	}
}

func svgDataURI(svg string) string {
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg))
}

// photoModelAnswering runs a fake OpenRouter whose photo model answers with
// the given image URL, and returns the finalize the route produced.
func photoModelAnswering(t *testing.T, url string) mediaUpdate {
	t.Helper()
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "photo-model-sentinel",
			"choices": []any{map[string]any{"message": map[string]any{
				"images": []any{map[string]any{"image_url": map[string]any{"url": url}}},
			}}},
		})
	}))
	defer or.Close()
	repo := newChanRepo()
	mux := http.NewServeMux()
	NewHandler(newPIMTestService(repo, ai.NewClient("or-test-key-not-real").WithBaseURL(or.URL))).RegisterRoutes(mux)
	rec := postJSON(mux, "/api/v1/products/"+uuid.New().String()+"/pim/generate/image", `{}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	return repo.wait(t)
}

// The photo model is not trusted with SVG: a script in its SVG answer is
// never stored, and a clean one is stored only as re-serialised bytes.
func TestWire_PIMPhotoPathNeverStoresUnsanitisedSVG(t *testing.T) {
	bad := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256"><script>alert(1)</script><rect width="9" height="9"/></svg>`
	u := photoModelAnswering(t, svgDataURI(bad))
	if u.status != "failed" || u.url != "" {
		t.Errorf("script svg: finalize = %+v, want failed with no url", u)
	}

	u = photoModelAnswering(t, svgDataURI(illustrationWithExtras))
	if u.status != "ready" {
		t.Fatalf("clean svg: status = %q", u.status)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(u.url, "data:image/svg+xml;base64,"))
	if err != nil || strings.Contains(string(raw), "<title") || strings.Contains(string(raw), `width="400"`) {
		t.Errorf("clean svg stored as the model sent it: %v %s", err, raw)
	}
}

// Only the raster types are accepted from the photo model as they are.
func TestWire_PIMPhotoPathAcceptsOnlyRasterOrSanitisedSVG(t *testing.T) {
	for _, ok := range []string{"png", "jpeg", "webp", "gif"} {
		u := photoModelAnswering(t, "data:image/"+ok+";base64,AAAA")
		if u.status != "ready" || u.url != "data:image/"+ok+";base64,AAAA" {
			t.Errorf("%s: finalize = %+v", ok, u)
		}
	}
	for _, bad := range []string{"data:image/svg+xml;utf8,<svg/>", "data:image/x-icon;base64,AAAA", "data:image/svg+xml;base64,!!!", "data:text/html;base64,AAAA"} {
		u := photoModelAnswering(t, bad)
		if u.status != "failed" || u.url != "" {
			t.Errorf("%q: finalize = %+v, want failed", bad, u)
		}
	}
}
