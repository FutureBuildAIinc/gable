// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesteam

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The sales team roster is what the customer record's salesperson assignment
// points at, and what commission attribution keys on.
//
// salesteam.NewHandler takes the Repository interface declared in
// repository.go, so this file covers routing and the request-shape rejections
// that return before any query; roster_test.go drives the handler against a
// fake repository, and repository_pg_test.go covers the SQL.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

func newTestMux() *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(nil).RegisterRoutes(mux)
	return mux
}

func do(t *testing.T, mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// CORRECTNESS: a malformed salesperson id is a client error and must be
// rejected before any query is built.
func TestHandleGet_MalformedIDIs400(t *testing.T) {
	mux := newTestMux()

	for _, id := range []string{"not-a-uuid", "123", uuid.NewString() + "x", "%20"} {
		rec := do(t, mux, http.MethodGet, "/api/v1/sales-team/"+id)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/v1/sales-team/%s = %d, want 400", id, rec.Code)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Error.Code != "BAD_REQUEST" {
			t.Errorf("error code = %q, want BAD_REQUEST", body.Error.Code)
		}
	}
}

// CORRECTNESS: the error envelope must not echo the parse error, which
// contains the caller's raw input.
func TestHandleGet_ErrorDoesNotEchoInput(t *testing.T) {
	const marker = "REFLECTED-INPUT-MARKER"
	rec := do(t, newTestMux(), http.MethodGet, "/api/v1/sales-team/"+marker)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), marker) {
		t.Errorf("the caller's input was reflected into the response: %s", rec.Body.String())
	}
}

// CORRECTNESS: the role guard supplied at registration must wrap both routes.
func TestRegisterRoutes_RoleGuardWrapsEveryEndpoint(t *testing.T) {
	guard := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
	}
	mux := http.NewServeMux()
	NewHandler(nil).RegisterRoutes(mux, guard)

	for _, path := range []string{"/api/v1/sales-team", "/api/v1/sales-team/" + uuid.NewString()} {
		rec := do(t, mux, http.MethodGet, path)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", path, rec.Code)
		}
	}
}

// CORRECTNESS: only GET is routed. A POST to the collection must not fall
// through to the list handler.
func TestRegisterRoutes_OnlyGETIsRouted(t *testing.T) {
	mux := newTestMux()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := do(t, mux, method, "/api/v1/sales-team")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/v1/sales-team = %d, want 405", method, rec.Code)
		}
	}
}

// --- JSON contract -------------------------------------------------------

// CORRECTNESS: the salesperson payload is the shape the customer-assignment UI
// and the commission report both read. The field names are the contract.
func TestSalesPersonJSON_FieldNames(t *testing.T) {
	id := uuid.New()
	b, err := json.Marshal(SalesPerson{
		ID: id, Name: "Dana Reyes", Email: "dana@dealer.example", Phone: "+15555550101",
		Role: "OUTSIDE", IsActive: true,
		CreatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, field := range []string{"id", "name", "email", "phone", "role", "is_active", "created_at", "updated_at"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("the payload is missing %q: %s", field, b)
		}
	}
	if got := string(raw["is_active"]); got != "true" {
		t.Errorf("is_active = %s, want true", got)
	}
	if !strings.Contains(string(raw["id"]), id.String()) {
		t.Errorf("id = %s, want %s", raw["id"], id)
	}
}

// CORRECTNESS: an inactive salesperson still serialises is_active rather than
// omitting it — the roster endpoint filters on is_active = true in SQL, so a
// client that receives a record with the field missing would have no way to
// tell an inactive rep from an unset one.
func TestSalesPersonJSON_InactiveIsExplicit(t *testing.T) {
	b, err := json.Marshal(SalesPerson{ID: uuid.New(), Name: "Ex Employee", IsActive: false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"is_active":false`) {
		t.Errorf("is_active was omitted for an inactive rep: %s", b)
	}
}

// CORRECTNESS: the seam is a consumer-defined interface, and the Postgres
// implementation satisfies it. If someone re-couples NewHandler to the concrete
// type, this stops compiling.
func TestSalesteamSeam_IsAnInterface(t *testing.T) {
	var _ Repository = (*PostgresRepository)(nil)

	// NewHandler must accept anything that satisfies Repository, not just the
	// Postgres one — that is what makes roster_test.go possible.
	if NewHandler(&fakeRoster{}) == nil {
		t.Fatal("NewHandler returned nil for a fake repository")
	}
}
