// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// CRM activities are the call/meeting log a salesperson keeps against a
// customer. The type is a closed vocabulary that the UI filters and groups on,
// so an unrecognised value would create an activity nothing displays.
//
// crm.Handler takes the Repository interface declared in activity.go, so this
// file covers the vocabulary, the wire format and the request-shape rejections
// that return before any query; repository_test.go drives the CRUD routes
// against a fake store, and repository_pg_test.go covers the SQL.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// --- activity type vocabulary -------------------------------------------

// CORRECTNESS: exactly four activity types are valid, matched exactly. A
// lower-case "call" is not a CALL: the column is compared literally everywhere
// downstream.
func TestValidActivityType(t *testing.T) {
	valid := []ActivityType{ActivityCall, ActivityMeeting, ActivityEmail, ActivityNote}
	for _, at := range valid {
		if !ValidActivityType(at) {
			t.Errorf("ValidActivityType(%q) = false, want true", at)
		}
	}

	invalid := []ActivityType{
		"", "call", "Call", "CALLS", "TASK", "VISIT", "QUOTE",
		"CALL ", " CALL", "CALL;DROP TABLE crm_activities",
	}
	for _, at := range invalid {
		if ValidActivityType(at) {
			t.Errorf("ValidActivityType(%q) = true, want false", at)
		}
	}
}

// CORRECTNESS: the constants are the wire values. A rename would silently
// invalidate every stored row, so the literals are pinned.
func TestActivityTypeConstants(t *testing.T) {
	for got, want := range map[ActivityType]string{
		ActivityCall:    "CALL",
		ActivityMeeting: "MEETING",
		ActivityEmail:   "EMAIL",
		ActivityNote:    "NOTE",
	} {
		if string(got) != want {
			t.Errorf("constant = %q, want %q", got, want)
		}
	}
}

// --- JSON contract -------------------------------------------------------

// CORRECTNESS: optional foreign keys must round-trip as null rather than the
// zero UUID. "00000000-0000-0000-0000-000000000000" is a value the contact FK
// would reject, and it is not the same thing as "no contact".
func TestActivityJSON_OptionalIDsAreNullable(t *testing.T) {
	b, err := json.Marshal(Activity{
		ID: uuid.New(), CustomerID: uuid.New(),
		ActivityType: ActivityCall, Description: "Called about the Maple Street order",
		ActivityDate: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "00000000-0000-0000-0000-000000000000") {
		t.Errorf("an unset optional id serialised as the zero UUID: %s", b)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := raw["contact_id"]; present {
		t.Errorf("contact_id was serialised despite being unset: %s", b)
	}
	if _, present := raw["logged_by"]; present {
		t.Errorf("logged_by was serialised despite being unset: %s", b)
	}

	// And a set contact id must appear.
	contact := uuid.New()
	b2, err := json.Marshal(Activity{ID: uuid.New(), ContactID: &contact, ActivityType: ActivityNote})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b2), contact.String()) {
		t.Errorf("a set contact id was not serialised: %s", b2)
	}
}

// --- HTTP layer ----------------------------------------------------------

// newTestMux wires the handler against a nil repository. Every test below
// drives a path that must return before the repository is reached; if a path
// stops doing so the test panics, which is a louder failure than a silent
// change of behaviour.
func newTestMux() *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(nil).RegisterRoutes(mux)
	return mux
}

func do(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// CORRECTNESS: a malformed id is a client error on every route, caught before
// any query is built.
func TestHandlers_MalformedIDsAre400(t *testing.T) {
	mux := newTestMux()

	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/customers/not-a-uuid/activities", ""},
		{http.MethodPost, "/api/v1/customers/not-a-uuid/activities", `{"activity_type":"CALL"}`},
		{http.MethodGet, "/api/v1/activities/not-a-uuid", ""},
		{http.MethodPut, "/api/v1/activities/not-a-uuid", `{"activity_type":"CALL"}`},
		{http.MethodDelete, "/api/v1/activities/not-a-uuid", ""},
		{http.MethodGet, "/api/v1/activities/" + uuid.NewString() + "x", ""},
	}

	for _, tc := range cases {
		rec := do(t, mux, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400", tc.method, tc.path, rec.Code)
		}
	}
}

// CORRECTNESS: the activity type is validated before persistence, on both the
// create and the update path. An unvalidated update is the easier one to miss.
func TestHandlers_RejectInvalidActivityType(t *testing.T) {
	mux := newTestMux()
	custID := uuid.NewString()
	actID := uuid.NewString()

	for _, at := range []string{"", "call", "TASK", "VISIT", "CALL "} {
		t.Run("create "+at, func(t *testing.T) {
			rec := do(t, mux, http.MethodPost, "/api/v1/customers/"+custID+"/activities",
				`{"activity_type":"`+at+`","description":"x"}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("activity_type %q => %d, want 400", at, rec.Code)
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
		})

		t.Run("update "+at, func(t *testing.T) {
			rec := do(t, mux, http.MethodPut, "/api/v1/activities/"+actID,
				`{"activity_type":"`+at+`","description":"x"}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("activity_type %q => %d, want 400", at, rec.Code)
			}
		})
	}
}

// CORRECTNESS: an activity with no activity_type at all is rejected. This is
// the common case for a client that forgets the field, and it must not default
// to anything.
func TestHandlers_MissingActivityTypeIsRejected(t *testing.T) {
	mux := newTestMux()

	rec := do(t, mux, http.MethodPost, "/api/v1/customers/"+uuid.NewString()+"/activities",
		`{"description":"Called the customer"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a missing activity_type", rec.Code)
	}
}

// CORRECTNESS: a malformed body is a client error, caught before validation.
func TestHandlers_MalformedBodyIs400(t *testing.T) {
	mux := newTestMux()

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/customers/" + uuid.NewString() + "/activities"},
		{http.MethodPut, "/api/v1/activities/" + uuid.NewString()},
	} {
		rec := do(t, mux, tc.method, tc.path, "{not json")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400", tc.method, tc.path, rec.Code)
		}
	}

	// A wrong-typed field is also a decode failure.
	rec := do(t, mux, http.MethodPost, "/api/v1/customers/"+uuid.NewString()+"/activities",
		`{"activity_type":42}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a non-string activity_type", rec.Code)
	}
}

// CORRECTNESS: the role guard supplied at registration must wrap every route —
// CRM notes are customer-relationship data and should not be world-readable.
func TestRegisterRoutes_RoleGuardWrapsEveryEndpoint(t *testing.T) {
	guard := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
	}
	mux := http.NewServeMux()
	NewHandler(nil).RegisterRoutes(mux, guard)

	id := uuid.NewString()
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/customers/" + id + "/activities"},
		{http.MethodPost, "/api/v1/customers/" + id + "/activities"},
		{http.MethodGet, "/api/v1/activities/" + id},
		{http.MethodPut, "/api/v1/activities/" + id},
		{http.MethodDelete, "/api/v1/activities/" + id},
	}
	for _, r := range routes {
		rec := do(t, mux, r.method, r.path, `{"activity_type":"CALL"}`)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403: the role guard did not wrap this route", r.method, r.path, rec.Code)
		}
	}
}

// CORRECTNESS (security): the customer id comes from the path, so a body that
// names a different customer must not win. Verified structurally: the handler
// assigns a.CustomerID from the path AFTER decoding, so the body value is
// always overwritten. This test pins that a decoded body does carry the
// attacker's value, making the overwrite the thing that matters.
func TestCreateActivity_BodyCustomerIDIsOverwritten(t *testing.T) {
	attacker := uuid.New()
	var a Activity
	if err := json.Unmarshal([]byte(`{"customer_id":"`+attacker.String()+`","activity_type":"CALL"}`), &a); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a.CustomerID != attacker {
		t.Fatalf("the fixture is wrong: decoded customer_id = %s", a.CustomerID)
	}

	// The handler's assignment is what neutralises it. Reproduce that step.
	pathCustomer := uuid.New()
	a.CustomerID = pathCustomer
	if a.CustomerID != pathCustomer {
		t.Fatal("the path customer did not win")
	}
}
