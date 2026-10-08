// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package actor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	if got.Kind != actor.KindUser {
		t.Fatalf("kind = %q, want %q (unattributed rows record the user kind)", got.Kind, actor.KindUser)
	}
	if got.ID != "" {
		t.Fatalf("id = %q, want empty", got.ID)
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

	if got := actor.FromContext(reqCtx); got.Kind != actor.KindUser || got.ActingAs != "" || got.Tool != "" {
		t.Fatalf("actor = %+v, want an unattributed user with no agent fields", got)
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
