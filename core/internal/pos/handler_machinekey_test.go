// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The machine key contract (R1-13, ADR 0002 section 4, second review P2-4):
// the POS routes that resolve their cashier from the request identity
// (starting a sale, opening a till, recording a return) are user-only, and
// a key is refused 403 "a cashier must be a user"; the routes that carry no
// cashier identity of their own (item changes, completion, void, the sync,
// the close, the reports) stay reachable with pos scopes.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// keyedServer serves the fixture's real handler stack with a machine key in
// the request context, where the auth core would put it once a key validates.
func keyedServer(f *fixture, keyID string) *httptest.Server {
	mux := http.NewServeMux()
	pos.NewHandler(f.service).RegisterRoutes(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(middleware.WithKeyID(r.Context(), keyID)))
	}))
	f.t.Cleanup(srv.Close)
	return srv
}

// keyRefusalBody decodes the refusal envelope the handler writes.
type keyRefusalBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
}

func TestMachineKeyNeverGetsFabricatedCashier(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	keyID := uuid.NewString()
	keyed := keyedServer(f, keyID)

	// The three user-only routes: 403 with the envelope and the message.
	for _, route := range []string{"/api/v1/pos/transactions", "/api/v1/pos/till/open", "/api/v1/pos/returns"} {
		req := httptest.NewRequest(http.MethodPost, route,
			strings.NewReader(`{"register_id":"`+f.register+`","reason":"k","lines":[{"product_id":"`+f.productID.String()+`","quantity":"1","unit_price_ten_thousandths":1000}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "req-pos-key-test")
		rec := httptest.NewRecorder()
		keyed.Config.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("machine key on %s: status = %d, want 403; body: %s", route, rec.Code, rec.Body.String())
			continue
		}
		var body keyRefusalBody
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Errorf("decode refusal envelope: %v (body: %s)", err, rec.Body.String())
			continue
		}
		if body.Error.Code != "forbidden" || !strings.Contains(body.Error.Message, "a cashier must be a user") {
			t.Errorf("refusal on %s = (%s, %s), want forbidden and a cashier must be a user", route, body.Error.Code, body.Error.Message)
		}
	}

	// The routes that carry no cashier identity of their own stay reachable:
	// a key holding pos scopes works them (ADR 0002 section 4). A sale is
	// built through the unkeyed fixture (a key cannot start one).
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	revision := num(t, f.getSale(t, saleID), "revision")
	keyedDo := func(method, path, body string) int {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "req-pos-key-test")
		rec := httptest.NewRecorder()
		keyed.Config.Handler.ServeHTTP(rec, req)
		return rec.Code
	}
	// item changes
	if code := keyedDo("POST", "/api/v1/pos/transactions/"+saleID+"/items",
		`{"line":{"product_id":"`+f.productID.String()+`","quantity":"1"}}`); code == http.StatusForbidden {
		t.Errorf("add item with a machine key: 403, want reachable (ADR 0002 section 4: item changes)")
	}
	// completion
	if code := keyedDo("POST", "/api/v1/pos/transactions/"+saleID+"/complete",
		`{"tenders":[{"method":"cash","amount_cents":1198}],"revision":`+jsonNumberOf(revision)+`}`); code == http.StatusForbidden {
		t.Errorf("complete with a machine key: 403, want reachable")
	}
	// the void (its own revision: the completion moved it)
	revision = num(t, f.getSale(t, saleID), "revision")
	if code := keyedDo("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		`{"reason":"keyed","revision":`+jsonNumberOf(revision)+`}`); code == http.StatusForbidden {
		t.Errorf("void with a machine key: 403, want reachable")
	}
	// the offline sync
	if code := keyedDo("POST", "/api/v1/pos/sync",
		`{"batch_id":"key-sync","register_id":"`+f.register+`","items":[{"client_id":"`+uuid.NewString()+`","cashier_id":"`+uuid.NewString()+`","items":[{"product_id":"`+f.productID.String()+`","quantity":"1"}],"tenders":[{"method":"cash","amount_cents":599}]}]}`); code == http.StatusForbidden {
		t.Errorf("sync with a machine key: 403, want reachable")
	}
	// the till close (a drawer the unkeyed fixture opened)
	f.openTill(0)
	sessionID := f.currentTillID(t)
	if code := keyedDo("POST", "/api/v1/pos/till/"+sessionID+"/close",
		`{"counted_by_method":{"cash":0}}`); code == http.StatusForbidden {
		t.Errorf("till close with a machine key: 403, want reachable")
	}
	f.assertARInvariants(t)
}

func jsonNumberOf(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
