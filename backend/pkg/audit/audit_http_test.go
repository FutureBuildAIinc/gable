// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package audit_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/metrics"
	"github.com/google/uuid"
)

// TestHTTPStackRecordsAgentRowWithMarkerAndTool drives one request through
// the real middleware stack in cmd/server's order (metrics, request id,
// recovery, rate limit, idempotency, CORS, actor, auth, handler) with auth
// replaced by a stand-in that injects the same claims the JWT middleware
// would, standing exactly where auth sits. A mutation performed under that
// request must land as an agent row carrying the marker (lowercased) and the
// tool name.
func TestHTTPStackRecordsAgentRowWithMarkerAndTool(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)
	entityID := uuid.New()

	// The mutation the handler performs on the request's behalf.
	var handlerErr error
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerErr = logger.Log(r.Context(), audit.Entry{
			Action:     "order.created",
			EntityType: "http_stack_test",
			EntityID:   entityID,
		})
		w.WriteHeader(http.StatusOK)
		if handlerErr != nil {
			t.Errorf("audit write under the HTTP stack: %v", handlerErr)
		}
	})

	// Stands exactly where auth sits in cmd/server: inside the actor
	// middleware, outside the handler, injecting the claims a verified JWT
	// would. The real auth middleware needs a JWKS; its one job here is
	// putting claims in the context, and the stand-in does the same thing.
	authStandIn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := &middleware.UserClaims{}
			claims.Subject = "user-789"
			ctx := context.WithValue(r.Context(), middleware.UserContextKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	// cmd/server's order, outermost first.
	var h http.Handler = handler
	h = authStandIn(h)
	h = actor.Middleware(h)
	h = middleware.CORSMiddleware(h)
	h = middleware.Idempotency()(h)
	h = middleware.RateLimit(120)(h)
	h = middleware.Recovery(slog.Default())(h)
	h = middleware.RequestID(h)
	h = metrics.HTTPMetrics(h)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
	req.Header.Set(actor.HeaderActingAs, "Agent")
	req.Header.Set(actor.HeaderAgentTool, "quote.create")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		body, _ := json.Marshal(rec.Body.String())
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, body)
	}

	rows := fetchRows(t, db, entityID)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.ActorKind != actor.KindAgent {
		t.Errorf("actor_kind = %q, want %q (the marker must mark the row through the real stack)", r.ActorKind, actor.KindAgent)
	}
	if r.ActorID == nil || *r.ActorID != "user-789" {
		t.Errorf("actor_id = %v, want the acted-for user user-789", r.ActorID)
	}
	if r.ActingAs == nil || *r.ActingAs != "agent" {
		t.Errorf("acting_as = %v, want the marker lowercased to agent", r.ActingAs)
	}
	if r.Tool == nil || *r.Tool != "quote.create" {
		t.Errorf("tool = %v, want quote.create", r.Tool)
	}
}
