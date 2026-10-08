// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package actor resolves which kind of principal is performing a write: a
// user (the JWT subject), a scoped machine key, or an agent acting for a
// user. pkg/audit records the resolved actor on every audit_log row.
//
// The agent marker never grants anything: it only records. Nothing in this
// package consults roles, scopes or permissions.
package actor

import (
	"context"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/pkg/middleware"
)

// Actor kinds recorded on audit_log.actor_kind.
const (
	KindUser  = "user"
	KindKey   = "key"
	KindAgent = "agent"
)

// Agent identity headers. An agentic UI performing a mutation on a user's
// behalf sends the user's own token plus these headers; the refactor inputs
// document name the marker ("acting-as: agent") and the tool/action name but
// fix no header spelling, so these are the product's choice.
const (
	HeaderActingAs  = "X-Acting-As"
	HeaderAgentTool = "X-Agent-Tool"
)

// Actor is the principal resolved from a request context.
type Actor struct {
	// Kind is one of KindUser, KindKey, KindAgent.
	Kind string
	// ID is the principal's id: the user's JWT subject, or the machine key's
	// id. On an agent row it is the user the agent acted for (falling back to
	// the key id when an agent drives a keyed integration).
	ID string
	// ActingAs is the X-Acting-As marker, set only on agent rows.
	ActingAs string
	// Tool is the X-Agent-Tool tool name, set only on agent rows.
	Tool string
}

type ctxKey int

const (
	keyIDKey ctxKey = iota + 1
	actingAsKey
	agentToolKey
)

// WithKeyID records a machine key's id in the context. The scoped-key
// middleware (R1-13) calls this once it has validated a Bearer machine key
// against the api_keys table, so downstream audit rows can attribute the
// write to that key.
func WithKeyID(ctx context.Context, keyID string) context.Context {
	return context.WithValue(ctx, keyIDKey, keyID)
}

// KeyIDFromContext returns the machine key id set by WithKeyID, if any.
func KeyIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(keyIDKey).(string)
	return id, ok && id != ""
}

// Middleware copies the agent identity headers into the request context so
// pkg/audit can record them. It grants nothing and rejects nothing: a request
// carrying the marker is authenticated by the auth middleware exactly like
// any other, and the marker only ends up on the audit row.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if v := strings.TrimSpace(r.Header.Get(HeaderActingAs)); v != "" {
			ctx = context.WithValue(ctx, actingAsKey, v)
		}
		if v := strings.TrimSpace(r.Header.Get(HeaderAgentTool)); v != "" {
			ctx = context.WithValue(ctx, agentToolKey, v)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// FromContext resolves the actor for audit purposes. Precedence: an agent
// marker wins (the row records the user it acts for, plus the marker and the
// tool name); then a machine key; then the JWT subject. When nothing is
// present the actor is unattributed (kind user, empty id) — the audit log's
// job is to record the principal, not to authenticate the request.
func FromContext(ctx context.Context) Actor {
	a := Actor{Kind: KindUser}
	if claims := middleware.ClaimsFromContext(ctx); claims != nil {
		a.ID = claims.Subject
	}
	if keyID, ok := KeyIDFromContext(ctx); ok {
		a.Kind = KindKey
		a.ID = keyID
	}
	if marker, ok := ctx.Value(actingAsKey).(string); ok && marker != "" {
		a.Kind = KindAgent
		a.ActingAs = marker
		a.Tool, _ = ctx.Value(agentToolKey).(string)
		// An agent row records the user it acted for. The subject already
		// sits in a.ID from the claims above; when the call came through a
		// keyed integration instead, a.ID holds the key id and stays.
	}
	return a
}
