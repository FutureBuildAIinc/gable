// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// refusingStore is the store an ai.KeyStore becomes when no PAYMENT_VAULT_KEY
// is configured: Set refuses rather than writing the credential in plaintext.
type refusingStore struct {
	fakeStore
	err error
}

func (r *refusingStore) Set(context.Context, string) error { return r.err }

// The operator-visible claim this whole change rests on is that saving a key
// in Tech Admin FAILS when there is no vault key — the boot warning says so in
// as many words. Nothing asserted it at the layer the claim is about: making
// SaveAISettings / SaveRoutingSettings swallow Set()'s error and answer 200
// left the suite green, and the admin UI would have shown "saved" for a
// credential that was never written.
//
// This pins the refusal at the HTTP boundary: the status must be a 5xx and the
// body must not claim success.
func TestSaveSettings_SurfacesStoreRefusal(t *testing.T) {
	refusal := errors.New(`refusing to write secret setting "openrouter_api_key": no PAYMENT_VAULT_KEY configured, so it would be stored in plaintext`)

	for _, tc := range []struct {
		name    string
		body    string
		invoke  func(h *Handler, w http.ResponseWriter, r *http.Request)
		handler func(store *refusingStore) *Handler
	}{
		{
			name:   "AI settings",
			body:   `{"api_key":"sk-or-v1-live"}`,
			invoke: func(h *Handler, w http.ResponseWriter, r *http.Request) { h.SaveAISettings(w, r) },
			handler: func(store *refusingStore) *Handler {
				return &Handler{aiKeyStore: store, baseURLStore: &fakeStore{}}
			},
		},
		{
			name:   "AI settings with a base URL alongside",
			body:   `{"api_key":"sk-or-v1-live","base_url":"https://openrouter.ai/api/v1"}`,
			invoke: func(h *Handler, w http.ResponseWriter, r *http.Request) { h.SaveAISettings(w, r) },
			handler: func(store *refusingStore) *Handler {
				return &Handler{aiKeyStore: store, baseURLStore: &fakeStore{}}
			},
		},
		{
			name:   "routing settings",
			body:   `{"api_key":"ors-live-key"}`,
			invoke: func(h *Handler, w http.ResponseWriter, r *http.Request) { h.SaveRoutingSettings(w, r) },
			handler: func(store *refusingStore) *Handler {
				return &Handler{orsKeyStore: store}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &refusingStore{err: refusal}
			h := tc.handler(store)

			rec := httptest.NewRecorder()
			tc.invoke(h, rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(tc.body)))

			if rec.Code < 500 {
				t.Fatalf("status = %d; a refused credential write must not report success to the admin", rec.Code)
			}
			if strings.Contains(rec.Body.String(), `"saved"`) {
				t.Fatalf("body %q claims the key was saved; it was refused", rec.Body.String())
			}
		})
	}
}

// The base-URL half of the same handler. It is not a credential, but a failed
// write there must not be reported as success either — and the two writes sit
// in one handler, so a single swallowed error covers both.
func TestSaveAISettings_SurfacesBaseURLWriteFailure(t *testing.T) {
	h := &Handler{
		aiKeyStore:   &fakeStore{},
		baseURLStore: &refusingStore{err: errors.New("write failed")},
	}

	rec := httptest.NewRecorder()
	h.SaveAISettings(rec, httptest.NewRequest(http.MethodPut, "/",
		strings.NewReader(`{"api_key":"sk-or-v1-live","base_url":"https://openrouter.ai/api/v1"}`)))

	if rec.Code < 500 {
		t.Fatalf("status = %d, want 5xx when the base-URL write fails", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"saved"`) {
		t.Fatalf("body %q claims success after a failed write", rec.Body.String())
	}
}
