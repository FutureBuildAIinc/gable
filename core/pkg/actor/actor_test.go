// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package actor_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/middleware"
)

func TestFromContext_UserSubjectFromClaims(t *testing.T) {
	claims := &middleware.UserClaims{}
	claims.Subject = "user-123"
	ctx := context.WithValue(context.Background(), middleware.UserContextKey, claims)

	got := actor.FromContext(ctx)
	if got.Kind != actor.KindUser {
		t.Fatalf("kind = %q, want %q", got.Kind, actor.KindUser)
	}
	if got.ID != "user-123" {
		t.Fatalf("id = %q, want the JWT subject user-123", got.ID)
	}
	if got.ActingAs != "" || got.Tool != "" {
		t.Fatalf("agent fields set on a plain user call: acting_as=%q tool=%q", got.ActingAs, got.Tool)
	}
}

func TestFromContext_KeyIDFromContextValue(t *testing.T) {
	ctx := actor.WithKeyID(context.Background(), "key-456")

	got := actor.FromContext(ctx)
	if got.Kind != actor.KindKey {
		t.Fatalf("kind = %q, want %q", got.Kind, actor.KindKey)
	}
	if got.ID != "key-456" {
		t.Fatalf("id = %q, want the key id key-456", got.ID)
	}
}

func TestFromContext_AgentRecordsUserMarkerAndTool(t *testing.T) {
	var reqCtx context.Context
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
	req.Header.Set(actor.HeaderActingAs, "agent")
	req.Header.Set(actor.HeaderAgentTool, "quote.create")
	handler := actor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCtx = r.Context()
	}))
	handler.ServeHTTP(rec, req)

	claims := &middleware.UserClaims{}
	claims.Subject = "user-789"
	ctx := context.WithValue(reqCtx, middleware.UserContextKey, claims)

	got := actor.FromContext(ctx)
	if got.Kind != actor.KindAgent {
		t.Fatalf("kind = %q, want %q", got.Kind, actor.KindAgent)
	}
	if got.ID != "user-789" {
		t.Fatalf("id = %q, want the acted-for user's subject user-789", got.ID)
	}
	if got.ActingAs != "agent" {
		t.Fatalf("acting_as = %q, want the marker %q", got.ActingAs, "agent")
	}
	if got.Tool != "quote.create" {
		t.Fatalf("tool = %q, want quote.create", got.Tool)
	}
}

func TestFromContext_UnattributedWhenNothingPresent(t *testing.T) {
	got := actor.FromContext(context.Background())
	if got.Kind != actor.KindAnonymous {
		t.Fatalf("kind = %q, want %q (no identity means no user to claim)", got.Kind, actor.KindAnonymous)
	}
	if got.ID != "" {
		t.Fatalf("id = %q, want empty", got.ID)
	}
}

// rejectedBody decodes the 400 body a bad identity header produces, in the
// wire ADR's error envelope shape.
type rejectedBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"details"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
}

func TestMiddleware_RejectsMarkerOver128Bytes(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
	req.Header.Set(actor.HeaderActingAs, strings.Repeat("a", 129))
	req.Header.Set("X-Request-ID", "req-marker-long")
	handler := actor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("an over-long marker must not reach the handler")
	}))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body rejectedBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != "validation_failed" {
		t.Errorf("error.code = %q, want validation_failed", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, actor.HeaderActingAs) {
		t.Errorf("error.message = %q, want it to name the header %s", body.Error.Message, actor.HeaderActingAs)
	}
	if len(body.Error.Details) != 1 || body.Error.Details[0].Field != actor.HeaderActingAs {
		t.Errorf("error.details = %+v, want one entry naming %s", body.Error.Details, actor.HeaderActingAs)
	}
	if body.Meta.RequestID != "req-marker-long" {
		t.Errorf("meta.request_id = %q, want the request's req-marker-long", body.Meta.RequestID)
	}
}

func TestMiddleware_RejectsNonPrintableTool(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
	req.Header.Set(actor.HeaderAgentTool, "quote.create\x01")
	handler := actor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a non-printable tool name must not reach the handler")
	}))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body rejectedBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != "validation_failed" {
		t.Errorf("error.code = %q, want validation_failed", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, actor.HeaderAgentTool) {
		t.Errorf("error.message = %q, want it to name the header %s", body.Error.Message, actor.HeaderAgentTool)
	}
}

func TestMiddleware_AcceptsHeaderValueAt128ByteCap(t *testing.T) {
	var reqCtx context.Context
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
	req.Header.Set(actor.HeaderActingAs, "agent")
	req.Header.Set(actor.HeaderAgentTool, strings.Repeat("t", 128))
	handler := actor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCtx = r.Context()
	}))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a value at the cap is valid", rec.Code)
	}
	got := actor.FromContext(reqCtx)
	if got.Tool != strings.Repeat("t", 128) {
		t.Errorf("tool = %d bytes, want the full 128-byte value accepted", len(got.Tool))
	}
}

func TestFromContext_MarkerRecordedLowercased(t *testing.T) {
	var reqCtx context.Context
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
	req.Header.Set(actor.HeaderActingAs, "Agent")
	req.Header.Set(actor.HeaderAgentTool, "Quote.Create")
	handler := actor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCtx = r.Context()
	}))
	handler.ServeHTTP(rec, req)

	got := actor.FromContext(reqCtx)
	if got.ActingAs != "agent" {
		t.Fatalf("acting_as = %q, want the marker normalized to lowercase", got.ActingAs)
	}
	if got.Tool != "Quote.Create" {
		t.Fatalf("tool = %q, want the tool name recorded as sent", got.Tool)
	}
}

func TestMiddleware_NoHeadersLeavesContextClean(t *testing.T) {
	var reqCtx context.Context
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders", nil)
	handler := actor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCtx = r.Context()
	}))
	handler.ServeHTTP(rec, req)

	if got := actor.FromContext(reqCtx); got.Kind != actor.KindAnonymous || got.ActingAs != "" || got.Tool != "" {
		t.Fatalf("actor = %+v, want an anonymous actor with no agent fields", got)
	}
}

func TestMiddleware_MarkerAloneRecordsAgentWithoutIdentity(t *testing.T) {
	var reqCtx context.Context
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
	req.Header.Set(actor.HeaderActingAs, "agent")
	handler := actor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCtx = r.Context()
	}))
	handler.ServeHTTP(rec, req)

	// No claims, no key: still an agent row, just unattributed. The marker
	// records; it never authenticates.
	got := actor.FromContext(reqCtx)
	if got.Kind != actor.KindAgent || got.ID != "" || got.ActingAs != "agent" {
		t.Fatalf("actor = %+v, want an unattributed agent with the marker", got)
	}
}
