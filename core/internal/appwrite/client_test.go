// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package appwrite

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Client{Endpoint: srv.URL, ProjectID: "proj", APIKey: "test-key"}
}

func TestClientVersion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"2.1.0"}`))
	})
	c := newTestServer(t, mux)
	v, err := c.Version(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if v != "2.1.0" {
		t.Fatalf("Version() = %q, want 2.1.0", v)
	}
}

func TestClientCreateEmailPasswordSessionSendsKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/account/sessions/email", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Appwrite-Key"); got != "test-key" {
			t.Errorf("X-Appwrite-Key = %q, want test-key", got)
		}
		if got := r.Header.Get("X-Appwrite-Project"); got != "proj" {
			t.Errorf("X-Appwrite-Project = %q, want proj", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"$id":"sess1","userId":"u1","secret":"sekret"}`))
	})
	c := newTestServer(t, mux)
	s, err := c.CreateEmailPasswordSession(t.Context(), "sales@s9proof.example", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "sess1" || s.Secret != "sekret" || s.UserID != "u1" {
		t.Fatalf("unexpected session: %+v", s)
	}
}

func TestClientGetUser(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"$id":"u1","email":"a@s9proof.example","status":true,"emailVerification":true,"mfa":false}`))
	})
	c := newTestServer(t, mux)
	u, err := c.GetUser(t.Context(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !u.Status || !u.EmailVerification || u.MFA {
		t.Fatalf("unexpected user: %+v", u)
	}
}

func TestClientListMemberships(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/teams/{teamId}/memberships", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total":2,"memberships":[{"$id":"m1","userId":"u1","roles":["gable-owner"],"confirm":true},{"$id":"m2","userId":"u2","roles":["gable-sales"],"confirm":false}]}`))
	})
	c := newTestServer(t, mux)
	m, found, err := c.ListMemberships(t.Context(), "team1", "u2")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("membership not found")
	}
	if m.Confirmed {
		t.Fatal("expected unconfirmed membership")
	}
	if len(m.Roles) != 1 || m.Roles[0] != "gable-sales" {
		t.Fatalf("unexpected roles: %v", m.Roles)
	}

	_, found, err = c.ListMemberships(t.Context(), "team1", "nobody")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected membership absent")
	}
}

func TestClientDeleteSessionAsUser(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/account/sessions/{sessionId}", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Appwrite-Session"); got != "sekret" {
			t.Errorf("X-Appwrite-Session = %q, want sekret", got)
		}
		if got := r.Header.Get("X-Appwrite-Key"); got != "" {
			t.Errorf("user call must not carry the key, got %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c := newTestServer(t, mux)
	if err := c.DeleteSessionAsUser(t.Context(), "sess1", "sekret"); err != nil {
		t.Fatal(err)
	}
}

func TestClientErrorSurfacesStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom","type":"general_unknown"}`))
	})
	c := newTestServer(t, mux)
	_, err := c.Version(t.Context())
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, want *Error", err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", apiErr.StatusCode)
	}
}

func TestAdmitWithClientShape(t *testing.T) {
	// The client JSON tags must line up with what Admit consumes: this is a
	// guard against a silent field rename drifting the two apart.
	var u User
	if err := json.Unmarshal([]byte(`{"$id":"u1","status":true,"emailVerification":true,"mfa":false}`), &u); err != nil {
		t.Fatal(err)
	}
	m := Membership{Roles: []string{"gable-driver"}, Confirmed: true}
	if err := Admit(u, m, true); err != nil {
		t.Fatal(err)
	}
	if got := MapRoles(m.Roles); len(got) != 1 || got[0] != "driver" {
		t.Fatalf("MapRoles = %v, want [driver]", got)
	}
}
