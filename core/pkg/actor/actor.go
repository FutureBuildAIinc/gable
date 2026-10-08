// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package actor resolves which kind of principal is performing a write: a
// user (the JWT subject), a scoped machine key, an agent acting for a user,
// or nobody at all (anonymous — dev mode, background jobs). pkg/audit
// records the resolved actor on every audit_log row.
//
// The agent marker never grants anything: it only records. Nothing in this
// package consults roles, scopes or permissions.
package actor

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/pkg/middleware"
)

// Actor kinds recorded on audit_log.actor_kind.
const (
	KindUser      = "user"
	KindKey       = "key"
	KindAgent     = "agent"
	KindAnonymous = "anonymous"
)

// Agent identity headers. An agentic UI performing a mutation on a user's
// behalf sends the user's own token plus these headers; the refactor inputs
// document name the marker ("acting-as: agent") and the tool/action name but
// fix no header spelling, so these are the product's choice.
//
// Both values are self-asserted free text that lands verbatim on audit rows,
// so the middleware bounds them: at most 128 bytes of printable ASCII.
// Anything longer, or any byte outside printable ASCII, is refused before
// the request runs, with a 400 naming the header.
const (
	HeaderActingAs  = "X-Acting-As"
	HeaderAgentTool = "X-Agent-Tool"

	// maxValueBytes caps each identity header's value.
	maxValueBytes = 128
)

// Actor is the principal resolved from a request context.
type Actor struct {
	// Kind is one of KindUser, KindKey, KindAgent, KindAnonymous.
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

// WithKeyID records a machine key's id in the context. The canonical home of
// the value is pkg/middleware (the scoped-key auth core, R1-13, sets it once
// it has validated a Bearer machine key against the api_keys table); this
// package reads it from there, and these helpers delegate so callers keep one
// import.
func WithKeyID(ctx context.Context, keyID string) context.Context {
	return middleware.WithKeyID(ctx, keyID)
}

// KeyIDFromContext returns the machine key id set by WithKeyID, if any.
func KeyIDFromContext(ctx context.Context) (string, bool) {
	return middleware.KeyIDFromContext(ctx)
}

// validateHeaderValue reports whether a trimmed identity header value is
// acceptable: at most maxValueBytes of printable ASCII. The bound keeps a
// caller from writing arbitrary payloads into the audit trail through two
// headers that cannot be authenticated.
func validateHeaderValue(value string) bool {
	if len(value) > maxValueBytes {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7E {
			return false
		}
	}
	return true
}

// Middleware copies the agent identity headers into the request context so
// pkg/audit can record them. It grants nothing: a request carrying the
// marker is authenticated by the auth middleware exactly like any other, and
// the marker only ends up on the audit row. A marker or tool name over the
// cap, or carrying a non-printable byte, is refused with a 400 naming the
// header — the value is free text the caller chose, so a bad one is the
// caller's malformed request, not something to silently truncate.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marker := strings.TrimSpace(r.Header.Get(HeaderActingAs))
		tool := strings.TrimSpace(r.Header.Get(HeaderAgentTool))
		if !validateHeaderValue(marker) {
			respondHeaderRejected(w, r, HeaderActingAs)
			return
		}
		if !validateHeaderValue(tool) {
			respondHeaderRejected(w, r, HeaderAgentTool)
			return
		}
		ctx := r.Context()
		if marker != "" {
			// The marker is recorded lowercased: it is a fixed vocabulary
			// ("agent"), and normalizing it keeps AuditLog filters from
			// having to case-fold. The tool name stays as sent.
			ctx = context.WithValue(ctx, actingAsKey, strings.ToLower(marker))
		}
		if tool != "" {
			ctx = context.WithValue(ctx, agentToolKey, tool)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// rejectionDetail is one entry of the error envelope's details array.
type rejectionDetail struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// rejectionBody is the error envelope the wire ADR fixes for every error
// response: `{error: {code, message, details}, meta: {request_id}}`. The
// ADR's shared writer lands with the platform packages (R1-6); until then
// this writes the same shape for the one error this package can produce.
type rejectionBody struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Details []rejectionDetail `json:"details"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
}

// respondHeaderRejected writes the 400 for an identity header that failed
// validation, naming the header in the message and in details.
func respondHeaderRejected(w http.ResponseWriter, r *http.Request, header string) {
	const problem = "must be at most 128 bytes of printable ASCII"

	reqID := w.Header().Get("X-Request-ID")
	if reqID == "" {
		reqID = r.Header.Get("X-Request-ID")
	}

	var body rejectionBody
	body.Error.Code = "validation_failed"
	body.Error.Message = header + " header " + problem
	body.Error.Details = []rejectionDetail{{Field: header, Message: problem}}
	body.Meta.RequestID = reqID

	slog.Warn("rejected agent identity header",
		"header", header,
		"status", http.StatusBadRequest,
		"method", r.Method,
		"path", r.URL.Path,
		"request_id", reqID,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(body)
}

// FromContext resolves the actor for audit purposes. Precedence: an agent
// marker wins (the row records the user it acts for, plus the marker and the
// tool name); then a machine key; then the JWT subject. When nothing is
// present the actor is anonymous (kind anonymous, null actor_id) — a row
// with no identity behind it must not claim a user — and a marker alone is
// an unattributed agent row. The audit log's job is to record the principal,
// not to authenticate the request.
func FromContext(ctx context.Context) Actor {
	a := Actor{Kind: KindAnonymous}
	if claims := middleware.ClaimsFromContext(ctx); claims != nil && claims.Subject != "" {
		a.Kind = KindUser
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
