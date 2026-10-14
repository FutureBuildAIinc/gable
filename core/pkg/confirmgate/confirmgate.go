// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package confirmgate holds the agent half of the confirm gate (ADR 0007
// section 5.4): an agent acting on a person's behalf with the person's JWT
// and the actor marker may propose through drafts but never confirm. The
// gate refuses the promotion route and every entity write route of a
// confirm gated module for such a request, server side, and writes the
// agent.commit_refused audit row. Reads and draft writes pass.
//
// A session request is any request not authenticated by a machine key, so
// AUTH_MODE=dev with a marker is gated too. The marker is any non-empty
// X-Acting-As value: the actor seam records any marker as an agent, so the
// gate must not key on the value "agent" alone, or X-Acting-As: x would be
// recorded as an agent and escape it. Keyed requests are governed by their
// scopes alone, with or without a marker: the marker is recorded and changes
// nothing.
//
// The honest limit (ADR 0007's Known limits): the marker is self asserted,
// so an agent holding a person's token that leaves the marker off is, to the
// server, that person. The gate stops an honest agent and every agent
// framework that sets the marker by construction; the enforceable gate is a
// key (an agent that must never commit is given a propose key, not a
// session).
package confirmgate

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// refusalMessage is the one message the gate writes, in the ADR 0001 error
// envelope: it states the product rule, not a permission detail.
const refusalMessage = "an agent proposes through drafts; a person confirms"

// maxRefusalPathBytes bounds the caller controlled path stored on the audit
// row, the same bound the machine-key refusals use.
const maxRefusalPathBytes = 512

// Middleware builds the gate. A nil sink refuses exactly the same and
// writes no row (tests); serve wires the platform audit logger.
func Middleware(sink AuditSink) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			module, class, ok := middleware.ScopeTarget(r.Method, r.URL.Path)
			if ok && gated(class) && middleware.IsConfirmGated(module) && isAgentSession(ctx) {
				refuse(sink, ctx, w, r, module, class)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// gated reports whether the class is one the gate fires on: the promotion
// route and the entity writes. Draft reads and writes and every read pass.
func gated(class middleware.ScopeClass) bool {
	return class == middleware.ScopePromotion || class == middleware.ScopeEntityWrite
}

// isAgentSession reports whether the request is an agent acting with a
// person's session: the actor resolved kind agent (any non-empty marker)
// and no machine key authenticated the call. A keyed request is governed by
// its scopes alone.
func isAgentSession(ctx context.Context) bool {
	if _, keyed := middleware.KeyIDFromContext(ctx); keyed {
		return false
	}
	return actor.FromContext(ctx).Kind == actor.KindAgent
}

// AuditSink writes the refusal's audit row. *audit.Logger satisfies it; it
// is an interface so the package's tests need no database.
type AuditSink interface {
	Log(ctx context.Context, entry audit.Entry) error
}

// refuse answers the 403 and writes the agent.commit_refused row: the
// method, the bounded path and the module; the entity is the draft (on the
// promotion route) or the entity the path names, or the module with the nil
// UUID on a create (no id on the path).
func refuse(sink AuditSink, ctx context.Context, w http.ResponseWriter, r *http.Request, module string, class middleware.ScopeClass) {
	// The entity is the draft on the promotion route, the entity the path
	// names on an entity write, or the module with the nil UUID on a create.
	// PathValue is empty here (the gate runs outside the router), so the id
	// is read from the path itself.
	entityType, entityID := module, uuid.Nil
	if class == middleware.ScopePromotion {
		entityType = "draft"
	}
	if id := lastPathUUID(r.URL.Path); id != uuid.Nil {
		entityID = id
	}

	act := actor.FromContext(ctx)
	changes := map[string]any{
		"method": r.Method,
		"path":   boundedPath(r.URL.Path),
		"module": module,
	}
	if act.Tool != "" {
		changes["tool"] = act.Tool
	}
	if sink != nil {
		if err := sink.Log(ctx, audit.Entry{
			Action:     "agent.commit_refused",
			EntityType: entityType,
			EntityID:   entityID,
			Changes:    changes,
		}); err != nil {
			slog.Error("confirm gate: failed to write the refusal's audit row", "error", err)
		}
	}
	slog.Warn("confirm gate refused an agent's confirm",
		"method", r.Method, "path", r.URL.Path, "module", module, "request_id", middleware.GetRequestID(ctx))
	respondForbidden(w, r)
}

// lastPathUUID returns the first path segment (from the right) that parses
// as a UUID, so /api/v1/quotes/{id}/transitions names its quote and
// /api/v1/quotes (a create) names none.
func lastPathUUID(path string) uuid.UUID {
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if id, err := uuid.Parse(seg); err == nil {
			return id
		}
	}
	return uuid.Nil
}

// boundedPath cuts the stored path on a rune boundary at the audit bound.
func boundedPath(path string) string {
	if len(path) <= maxRefusalPathBytes {
		return path
	}
	cut := maxRefusalPathBytes
	for cut > 0 && !utf8.RuneStart(path[cut]) {
		cut--
	}
	return path[:cut]
}

// respondForbidden writes the refusal in the ADR 0001 error envelope.
func respondForbidden(w http.ResponseWriter, r *http.Request) {
	reqID := w.Header().Get("X-Request-ID")
	if reqID == "" {
		reqID = r.Header.Get("X-Request-ID")
	}
	body := struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Meta struct {
			RequestID string `json:"request_id"`
		} `json:"meta"`
	}{}
	body.Error.Code = "forbidden"
	body.Error.Message = refusalMessage
	body.Meta.RequestID = reqID
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(body)
}
