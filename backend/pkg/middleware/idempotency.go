// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/httputil"
)

// Header names. Idempotency-Key is canonical; X-Idempotency-Key (the name the
// in-memory implementation used) remains an alias addressing the same claim,
// so existing clients keep working. A request carrying both is claimed under
// the canonical one only.
const (
	IdempotencyHeader         = "Idempotency-Key"
	LegacyIdempotencyHeader   = "X-Idempotency-Key"
	IdempotencyReplayedHeader = "Idempotency-Replayed"
)

// How long a completed response stays replayable. Matches the 24h TTL of the
// in-memory store this file replaced.
const idempotencyRetention = 24 * time.Hour

// idempotencyResponseWriter wraps http.ResponseWriter to capture the response.
type idempotencyResponseWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
	wrote  bool
}

func (w *idempotencyResponseWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *idempotencyResponseWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *idempotencyResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// principalID derives the caller a claim is keyed on. The idempotency
// middleware runs inside the auth middleware, so the JWT subject (or the
// portal claims, or an integration key's tenant) is already in the context.
// Under AUTH_MODE=dev the caller is the fixed "dev" principal, mirroring the
// devActor convention in internal/pricing. An unidentifiable caller gets "":
// the request then passes through uncached rather than joining the shared
// anonymous namespace the in-memory store kept (which could replay one
// anonymous caller's response to another).
func principalID(r *http.Request) string {
	ctx := r.Context()
	if pc, ok := ctx.Value(PortalClaimsKey).(*PortalClaims); ok && pc != nil {
		return "portal:" + pc.CustomerID.String() + ":" + pc.CustomerUserID.String()
	}
	if claims := ClaimsFromContext(ctx); claims != nil && claims.Subject != "" {
		return "user:" + claims.Subject
	}
	if tenant := TenantIDFromContext(ctx); tenant != "" {
		return "tenant:" + tenant
	}
	if devAuthMode() {
		return "dev"
	}
	return ""
}

func devAuthMode() bool {
	return strings.EqualFold(os.Getenv("AUTH_MODE"), "dev")
}

// Idempotency returns middleware that makes POST/PUT requests carrying an
// Idempotency-Key (or the legacy X-Idempotency-Key) exactly-once per caller:
// the claim and the stored response live in the idempotency_keys table
// (migration 087), so a replay survives a restart and is shared across
// instances behind a load balancer.
//
// Scope rules, unchanged from the in-memory implementation: POST and PUT
// only; only 2xx responses are stored and replayed (a 4xx or 5xx releases
// the claim so the client can retry); requests without a key pass through.
//
// Wiring contract: inside the auth middleware (the principal comes from the
// context it populates) and inside MaxRequestSize (the fingerprint read
// honours the stack's request-size limit). A nil db disables the middleware
// entirely.
//
// Claim protocol: one autocommitted INSERT ... ON CONFLICT before the
// handler, never a transaction held across it (the handler uses the pool). A
// concurrent request with the same key gets 409 while the first is in
// progress; the same key with a different request fingerprint gets 422; a
// completed key replays its stored status and body with
// Idempotency-Replayed: true. If the database is unreachable the middleware
// fails open: the request is served uncached.
func Idempotency(db *database.DB) func(http.Handler) http.Handler {
	store := &idempotencyStore{db: db}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if db == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Only mutating methods participate, as before.
			if r.Method != http.MethodPost && r.Method != http.MethodPut {
				next.ServeHTTP(w, r)
				return
			}

			clientKey := r.Header.Get(IdempotencyHeader)
			if clientKey == "" {
				clientKey = r.Header.Get(LegacyIdempotencyHeader)
			}
			if clientKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			principal := principalID(r)
			if principal == "" {
				next.ServeHTTP(w, r)
				return
			}

			// The fingerprint binds the key to this request, so the body must
			// be read here and handed to the handler unchanged.
			body, err := readRequestBody(r)
			if err != nil {
				// The only read error reachable here is the size limit
				// MaxRequestSize enforces around this middleware.
				respondIdempotencyError(w, r, http.StatusRequestEntityTooLarge,
					"Request body exceeds the size limit")
				return
			}
			fingerprint := requestFingerprint(r.Method, r.URL.Path, body)

			claimed, holder, err := store.acquire(r.Context(), principal, clientKey, fingerprint,
				time.Now().Add(idempotencyClaimLease))
			if err != nil {
				// Fail open: idempotency is a safety layer, not an
				// availability gate.
				slog.Warn("idempotency: claim failed, serving uncached",
					"error", err, "principal", principal, "method", r.Method, "path", r.URL.Path)
				next.ServeHTTP(w, r)
				return
			}

			if !claimed {
				if holder.fingerprint != fingerprint {
					respondIdempotencyError(w, r, http.StatusUnprocessableEntity,
						"This idempotency key was already used with a different request (method, path or body differs)")
					return
				}
				if holder.state == idempotencyStateComplete {
					replayStoredResponse(w, holder)
					return
				}
				respondIdempotencyError(w, r, http.StatusConflict,
					"This idempotency key is already in progress; retry after the original request completes")
				return
			}

			// The claim is ours. Run the handler, then store or release.
			crw := &idempotencyResponseWriter{
				ResponseWriter: w,
				status:         http.StatusOK,
			}
			handlerReturned := false
			defer func() {
				if !handlerReturned {
					// The handler panicked; the Recovery middleware above us
					// answers 500. Release so a retry can run, using a
					// context the cancelled request cannot tear down.
					if rerr := store.release(context.WithoutCancel(r.Context()), principal, clientKey); rerr != nil {
						logIdempotencyError("release after panic", clientKey, rerr)
					}
				}
			}()
			next.ServeHTTP(crw, r)
			handlerReturned = true

			// Bookkeeping context survives a client disconnect right after
			// the response: without this, a disconnect between response and
			// complete would leave the claim 409ing until its lease lapses.
			bctx := context.WithoutCancel(r.Context())
			if crw.status >= 200 && crw.status < 300 {
				if cerr := store.complete(bctx, principal, clientKey, crw.status,
					crw.Header().Get("Content-Type"), crw.body.Bytes(),
					time.Now().Add(idempotencyRetention)); cerr != nil {
					logIdempotencyError("store response", clientKey, cerr)
				}
			} else {
				// A 4xx is validation the client must fix and a 5xx a server
				// fault: neither is a stored outcome (the in-memory store
				// cached 2xx only), and both leave the key claimable again.
				if rerr := store.release(bctx, principal, clientKey); rerr != nil {
					logIdempotencyError("release", clientKey, rerr)
				}
			}
		})
	}
}

// readRequestBody buffers the request body (for the fingerprint) and hands it
// back to the handler unchanged. It reads through whatever reader the
// middleware chain outside installed, so MaxRequestSize's limit applies to
// this read.
func readRequestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// replayStoredResponse answers with the stored response and marks the replay.
func replayStoredResponse(w http.ResponseWriter, h idempotencyHolder) {
	if h.contentType != "" {
		w.Header().Set("Content-Type", h.contentType)
	}
	w.Header().Set(IdempotencyReplayedHeader, "true")
	w.WriteHeader(h.statusCode)
	_, _ = w.Write(h.body)
}

// respondIdempotencyError answers with the repository's error envelope
// ({error: {code, message}, meta: {request_id}}), keeping the specific
// message (httputil.RespondError would replace it with a generic one; these
// errors only help if the client can tell what to do next).
func respondIdempotencyError(w http.ResponseWriter, r *http.Request, code int, message string) {
	reqID := w.Header().Get("X-Request-ID")
	if reqID == "" {
		reqID = r.Header.Get("X-Request-ID")
	}
	slog.Warn(message,
		"status", code,
		"method", r.Method,
		"path", r.URL.Path,
		"request_id", reqID,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(httputil.ErrorResponse{
		Error: httputil.ErrorDetail{Code: idempotencyErrorCode(code), Message: message},
		Meta:  httputil.ErrorMeta{RequestID: reqID},
	})
}

// idempotencyErrorCode mirrors httputil's status-to-code mapping for the
// statuses this middleware answers with.
func idempotencyErrorCode(status int) string {
	switch status {
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusUnprocessableEntity:
		return "UNPROCESSABLE_ENTITY"
	default:
		if status >= 400 && status < 500 {
			return "BAD_REQUEST"
		}
		return "INTERNAL_ERROR"
	}
}
