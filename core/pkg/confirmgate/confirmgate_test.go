// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package confirmgate_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/confirmgate"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// recordingSink collects the entries the gate writes.
type recordingSink struct {
	entries []audit.Entry
}

func (s *recordingSink) Log(_ context.Context, e audit.Entry) error {
	s.entries = append(s.entries, e)
	return nil
}

// gateEnv builds the gate around a next handler that records the call, and
// a second server whose chain marks the request as keyed (the machine-key
// auth core's context value) before the gate runs.
func gateEnv(t *testing.T) (*recordingSink, *httptest.Server, *httptest.Server, *bool) {
	t.Helper()
	sink := &recordingSink{}
	reached := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/quotes", func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(200) })
	mux.HandleFunc("/api/v1/quotes/{id}/transitions", func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(200) })
	mux.HandleFunc("/api/v1/drafts/quotes/{id}/promote", func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(200) })
	mux.HandleFunc("/api/v1/drafts/quotes/{id}", func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(200) })
	// A catch-all stands in for every route the shapes resolve to that this
	// small mux does not spell out (a GET by id, another module's create).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(200) })
	// The chain as serve orders it: the actor middleware (which copies the
	// marker into the context) outside the gate.
	srv := httptest.NewServer(actor.Middleware(confirmgate.Middleware(sink)(mux)))
	t.Cleanup(srv.Close)

	setKeyID := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(middleware.WithKeyID(r.Context(), "44444444-4444-4444-4444-444444444444")))
		})
	}
	keyed := httptest.NewServer(actor.Middleware(setKeyID(confirmgate.Middleware(sink)(mux))))
	t.Cleanup(keyed.Close)
	return sink, srv, keyed, &reached
}

func doGate(t *testing.T, srv *httptest.Server, method, path string, headers ...string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(raw)
}

// TestGateRefusesAgentConfirmsOnAMarkedSession pins the gate's policy: an
// agent marked session (X-Acting-As of any non-empty value, with or without
// a tool name) is refused the promotion and every entity write of a gated
// module with 403 and the fixed message, and the agent.commit_refused row
// is written naming the method, the bounded path and the module. Reads and
// draft writes pass.
func TestGateRefusesAgentConfirmsOnAMarkedSession(t *testing.T) {
	sink, srv, _, reached := gateEnv(t)

	cases := []struct {
		method, path, marker string
		entityID             uuid.UUID
	}{
		{"POST", "/api/v1/quotes", "agent", uuid.Nil},
		{"POST", "/api/v1/quotes", "x", uuid.Nil},
		{"POST", "/api/v1/quotes/11111111-1111-1111-1111-111111111111/transitions", "agent", uuid.MustParse("11111111-1111-1111-1111-111111111111")},
		{"PUT", "/api/v1/quotes/11111111-1111-1111-1111-111111111111", "AGENT", uuid.MustParse("11111111-1111-1111-1111-111111111111")},
		{"POST", "/api/v1/drafts/quotes/22222222-2222-2222-2222-222222222222/promote", "agent", uuid.MustParse("22222222-2222-2222-2222-222222222222")},
	}
	for i, tc := range cases {
		*reached = false
		status, body := doGate(t, srv, tc.method, tc.path, actor.HeaderActingAs, tc.marker, actor.HeaderAgentTool, "quote-builder")
		if status != http.StatusForbidden {
			t.Errorf("case %d: %s %s with marker %q = %d, want 403", i, tc.method, tc.path, tc.marker, status)
		}
		if !strings.Contains(body, "an agent proposes through drafts; a person confirms") {
			t.Errorf("case %d: body %q lacks the fixed message", i, body)
		}
		if *reached {
			t.Errorf("case %d: the handler ran behind a refused gate", i)
		}
		if len(sink.entries) <= i {
			t.Fatalf("case %d: no audit row written", i)
		}
		e := sink.entries[i]
		if e.Action != "agent.commit_refused" {
			t.Errorf("case %d: audit action %q, want agent.commit_refused", i, e.Action)
		}
		if e.EntityID != tc.entityID {
			t.Errorf("case %d: audit entity id %v, want %v", i, e.EntityID, tc.entityID)
		}
		if tc.method == "POST" && strings.HasSuffix(tc.path, "/promote") && e.EntityType != "draft" {
			t.Errorf("case %d: audit entity type %q, want draft on the promotion route", i, e.EntityType)
		}
		if tc.entityID == uuid.Nil && e.EntityType != "quotes" {
			t.Errorf("case %d: audit entity type %q, want the module on a create", i, e.EntityType)
		}
		if changes, _ := e.Changes["tool"].(string); changes != "quote-builder" {
			t.Errorf("case %d: audit row lacks the tool name: %v", i, e.Changes)
		}
		if m, _ := e.Changes["method"].(string); m != tc.method {
			t.Errorf("case %d: audit row method %v", i, e.Changes["method"])
		}
	}
}

// TestGatePassesReadsDraftWritesAndUnmarkedSessions pins what the gate does
// NOT fire on: reads of a gated module, draft reads and writes (an agent
// proposes), writes of an ungated module, and every unmarked session and
// keyed request (a key is governed by its scopes alone, marker or not).
func TestGatePassesReadsDraftWritesAndUnmarkedSessions(t *testing.T) {
	sink, srv, keyed, reached := gateEnv(t)

	pass := []struct{ method, path string }{
		{"GET", "/api/v1/quotes"},                                             // entity read
		{"GET", "/api/v1/quotes/11111111-1111-1111-1111-111111111111"},        // entity read
		{"PUT", "/api/v1/drafts/quotes/33333333-3333-3333-3333-333333333333"}, // draft write, marker or not
		{"POST", "/api/v1/drafts/quotes"},                                     // draft create
		{"POST", "/api/v1/customers"},                                         // ungated module write
	}
	for i, tc := range pass {
		*reached = false
		status, _ := doGate(t, srv, tc.method, tc.path, actor.HeaderActingAs, "agent")
		if status != http.StatusOK {
			t.Errorf("case %d: %s %s = %d, want to pass the gate", i, tc.method, tc.path, status)
		}
		if !*reached {
			t.Errorf("case %d: the handler did not run", i)
		}
	}
	if len(sink.entries) != 0 {
		t.Errorf("the gate wrote %d rows for requests it admits", len(sink.entries))
	}

	// An unmarked session is that person: the gate stays out.
	*reached = false
	if status, _ := doGate(t, srv, "POST", "/api/v1/quotes"); status != http.StatusOK || !*reached {
		t.Errorf("an unmarked session's create was refused (%d) or lost (%v)", status, *reached)
	}

	// A keyed request is governed by its scopes alone: the marker is
	// recorded and changes nothing. The key id in the context is what the
	// machine-key auth core sets once a key validates.
	*reached = false
	if status, _ := doGate(t, keyed, "POST", "/api/v1/quotes", actor.HeaderActingAs, "agent"); status != http.StatusOK || !*reached {
		t.Errorf("a keyed request was refused (%d) or lost (%v)", status, *reached)
	}
	if len(sink.entries) != 0 {
		t.Errorf("the gate wrote rows for requests it admits: %d", len(sink.entries))
	}
}

// TestGateBoundedPath pins the audit row's stored path bound: a path past
// the audit bound is stored truncated, never whole.
func TestGateBoundedPath(t *testing.T) {
	sink, srv, _, _ := gateEnv(t)
	long := "/api/v1/quotes/" + strings.Repeat("a", 4096)
	if status, _ := doGate(t, srv, "POST", long, actor.HeaderActingAs, "agent"); status != http.StatusForbidden {
		t.Fatalf("long path create = %d, want 403", status)
	}
	if len(sink.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(sink.entries))
	}
	p, _ := sink.entries[0].Changes["path"].(string)
	if len(p) > 513 {
		t.Errorf("stored path is %d bytes, want the bounded copy", len(p))
	}
}
