// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package appwrite

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// The membership read asks Appwrite for the one user, so a team larger than
// one page still finds a later member.
func TestClientListMembershipsFiltersByUser(t *testing.T) {
	var queries []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/teams/{teamId}/memberships", func(w http.ResponseWriter, r *http.Request) {
		queries = r.URL.Query()["queries[]"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total":1,"memberships":[{"$id":"m9","userId":"u9","roles":["gable-sales"],"confirm":true}]}`))
	})
	c := newTestServer(t, mux)
	m, found, err := c.ListMemberships(t.Context(), "team1", "u9")
	if err != nil || !found || m.ID != "m9" {
		t.Fatalf("m=%+v found=%v err=%v", m, found, err)
	}
	if len(queries) != 2 {
		t.Fatalf("queries = %q, want two", queries)
	}
	var byUser struct {
		Method    string   `json:"method"`
		Attribute string   `json:"attribute"`
		Values    []string `json:"values"`
	}
	if err := json.Unmarshal([]byte(queries[0]), &byUser); err != nil {
		t.Fatalf("first query is not JSON: %q: %v", queries[0], err)
	}
	if byUser.Method != "equal" || byUser.Attribute != "userId" || len(byUser.Values) != 1 || byUser.Values[0] != "u9" {
		t.Fatalf("request did not filter by user id: %+v", byUser)
	}
	var page struct {
		Method string `json:"method"`
		Values []int  `json:"values"`
	}
	if err := json.Unmarshal([]byte(queries[1]), &page); err != nil {
		t.Fatalf("second query is not JSON: %q: %v", queries[1], err)
	}
	if page.Method != "limit" || len(page.Values) != 1 || page.Values[0] != 25 {
		t.Fatalf("request did not set the page size: %+v", page)
	}
}

// A page that is not the whole answer (more members than it holds, user not
// on it) is not a definite "not a member": it is an error.
func TestClientListMembershipsPartialReadIsAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/teams/{teamId}/memberships", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total":60,"memberships":[{"$id":"m1","userId":"u1","roles":[],"confirm":true}]}`))
	})
	c := newTestServer(t, mux)
	_, found, err := c.ListMemberships(t.Context(), "team1", "u9")
	if err == nil || found {
		t.Fatalf("partial read gave found=%v err=%v, want an error", found, err)
	}
}

func TestOAuth2TokenURLCarriesNoKey(t *testing.T) {
	c := &Client{Endpoint: "https://aw.example.com/", ProjectID: "proj", APIKey: "test-key"}
	got := c.OAuth2TokenURL("google", "https://gable.example.com/api/v1/auth/callback?state=s", "https://gable.example.com/api/v1/auth/callback?state=s&failure=1")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "aw.example.com" || u.Path != "/v1/account/tokens/oauth2/google" {
		t.Fatalf("url = %s, want the oauth2 token route on the endpoint", got)
	}
	if u.Query().Get("project") != "proj" || u.Query().Get("success") == "" || u.Query().Get("failure") == "" {
		t.Fatalf("query = %v, want project, success and failure", u.Query())
	}
	if strings.Contains(got, "test-key") {
		t.Fatal("the navigation URL must never carry the API key")
	}
}

func TestClientCreateSessionFromToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/account/sessions/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Appwrite-Key") != "test-key" {
			t.Error("the exchange must carry the key")
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["userId"] != "u1" || body["secret"] != "tok" {
			t.Errorf("body = %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"$id":"sess1","userId":"u1","secret":"sekret"}`))
	})
	c := newTestServer(t, mux)
	s, err := c.CreateSessionFromToken(t.Context(), "u1", "tok")
	if err != nil || s.ID != "sess1" || s.Secret != "sekret" || s.UserID != "u1" {
		t.Fatalf("session = %+v, err = %v", s, err)
	}
}

func TestClientFindUserByEmail(t *testing.T) {
	answer := `{"users":[{"$id":"u1","email":"a@example.com","status":true,"emailVerification":true}]}`
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/users", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "equal") {
			t.Errorf("query = %s, want an equal filter on email", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(answer))
	})
	c := newTestServer(t, mux)
	u, found, err := c.FindUserByEmail(t.Context(), "A@example.com")
	if err != nil || !found || u.ID != "u1" {
		t.Fatalf("user = %+v found = %v err = %v", u, found, err)
	}
	answer = `{"users":[]}`
	if _, found, err = c.FindUserByEmail(t.Context(), "none@example.com"); err != nil || found {
		t.Fatalf("found = %v err = %v, want a clean miss", found, err)
	}
	answer = `{"users":[{"$id":"u1","email":"a@example.com"},{"$id":"u2","email":"A@example.com"}]}`
	if _, _, err = c.FindUserByEmail(t.Context(), "a@example.com"); err == nil {
		t.Fatal("two users on one email must be an error, never a pick")
	}
}

func TestClientCreateMagicURLToken(t *testing.T) {
	var got map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/account/tokens/magic-url", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})
	c := newTestServer(t, mux)
	if err := c.CreateMagicURLToken(t.Context(), "u1", "a@example.com", "https://gable.example.com/api/v1/auth/callback?state=s"); err != nil {
		t.Fatal(err)
	}
	if got["userId"] != "u1" || got["email"] != "a@example.com" || got["url"] == "" {
		t.Fatalf("body = %v", got)
	}
}
