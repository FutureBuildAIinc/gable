// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gablelbm/gable/pkg/database"
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

// idempotencyMaxStoredBody caps what a stored response body may weigh. A
// larger 2xx/3xx is served to its caller in full but not stored (the claim is
// released and the drop logged): the table is a replay cache, not a blob
// store, and a retry re-running the handler is safe, unlike a missing replay.
const idempotencyMaxStoredBody = 1 << 20 // 1 MiB

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

// The principal a claim is keyed on says whose retry a stored answer belongs
// to. Three extractors, one per surface, because the identity is established
// in three places:
//
//   - the ERP API identifies itself with the JWT subject, which the auth
//     middleware (outside which the global layer runs) puts in the context.
//     Under AUTH_MODE=dev there is no JWT layer: a caller with no identity is
//     the fixed dev principal, mirroring the devActor convention in
//     internal/pricing. Outside dev an unidentifiable caller gets "": the
//     request passes through uncached rather than joining a shared anonymous
//     namespace (which could replay one anonymous caller's response to
//     another).
//   - the portal API authenticates per customer and user inside its own
//     chain (portalMw), which runs after the global layer: the portal layer
//     wraps inside it and reads the claims it injects.
//   - the integration API authenticates a shared key and identifies the
//     caller by its tenant (the X-Tenant-ID header). A tenantless caller
//     behaves like an anonymous one: its own dev:integration principal under
//     AUTH_MODE=dev, so it never shares the global layer's dev namespace,
//     uncached outside it.
func globalIdempotencyPrincipal(r *http.Request) string {
	if claims := ClaimsFromContext(r.Context()); claims != nil && claims.Subject != "" {
		return "user:" + claims.Subject
	}
	if devAuthMode() {
		return "dev"
	}
	return ""
}

func portalIdempotencyPrincipal(r *http.Request) string {
	if pc, ok := r.Context().Value(PortalClaimsKey).(*PortalClaims); ok && pc != nil {
		return "portal:" + pc.CustomerID.String() + ":" + pc.CustomerUserID.String()
	}
	return ""
}

func integrationIdempotencyPrincipal(r *http.Request) string {
	if tenant := r.Header.Get("X-Tenant-ID"); tenant != "" {
		return "tenant:" + tenant
	}
	if devAuthMode() {
		// Its own principal, never the global layer's dev: the two layers
		// share the one claim table, and a shared namespace would let an
		// integration retry meet the ERP layer's stored answer (or collide
		// with it) under the same key.
		return "dev:integration"
	}
	return ""
}

func devAuthMode() bool {
	return strings.EqualFold(os.Getenv("AUTH_MODE"), "dev")
}

// idempotencyOwnedPrefixes are the surfaces that carry their own idempotency
// layer inside their auth chain, where the principal that scopes their claims
// is established. The global layer skips them so no request runs through
// both.
var idempotencyOwnedPrefixes = []string{"/api/portal/v1/", "/api/integration/"}

func skipIdempotencyOwnedPrefix(r *http.Request) bool {
	for _, p := range idempotencyOwnedPrefixes {
		if strings.HasPrefix(r.URL.Path, p) {
			return true
		}
	}
	return false
}

// Idempotency returns the global idempotency layer: it covers the ERP API's
// POST/PUT/PATCH routes, whose principal (the JWT subject) the auth middleware
// outside it establishes. Portal and integration routes are NOT covered here:
// their principals are established inside their own auth chains, so they
// carry IdempotencyForPortalAuth and IdempotencyForIntegrationAuth there, and
// this layer skips their prefixes so nothing runs twice.
//
// Scope rules: POST, PUT and PATCH only; 2xx and 3xx responses are stored and
// replayed (a 4xx or 5xx releases the claim so the client can retry);
// requests without a key pass through.
//
// Wiring contract: inside the auth middleware (the principal comes from the
// context it populates) and inside MaxRequestSize (the fingerprint read
// honours the stack's request-size limit). A nil db disables the middleware
// entirely.
//
// Claim protocol: one autocommitted INSERT ... ON CONFLICT before the
// handler, never a transaction held across it (the handler uses the pool). A
// concurrent request with the same key gets 409 idempotency_in_progress while
// the first is in progress; the same key with a different request fingerprint
// gets 422 idempotency_key_reused; a completed key replays its stored status
// and body with Idempotency-Replayed: true. Each claim carries a token
// (claim_id) that complete and release must match, so a holder whose lease
// lapsed cannot touch a successor's claim. If the database is unreachable
// the middleware fails open: the request is served uncached.
func Idempotency(db *database.DB) func(http.Handler) http.Handler {
	return idempotencyLayer(db, globalIdempotencyPrincipal, skipIdempotencyOwnedPrefix)
}

// IdempotencyForPortalAuth is the portal surface's idempotency layer. Wire it
// INSIDE the portal auth middleware (portalMw(portalIdem(handler))): the
// claim is scoped on the customer and user the portal auth chain
// establishes, which the global layer cannot see. A request without portal
// claims passes through uncached.
func IdempotencyForPortalAuth(db *database.DB) func(http.Handler) http.Handler {
	return idempotencyLayer(db, portalIdempotencyPrincipal, nil)
}

// IdempotencyForIntegrationAuth is the integration surface's idempotency
// layer. Wire it INSIDE the integration auth chain (after the
// X-Integration-Key check): the claim is scoped on the caller's tenant (the
// X-Tenant-ID header). Under AUTH_MODE=dev a tenantless caller gets its own
// dev:integration principal, never the global layer's dev; outside dev it
// passes through uncached.
func IdempotencyForIntegrationAuth(db *database.DB) func(http.Handler) http.Handler {
	return idempotencyLayer(db, integrationIdempotencyPrincipal, nil)
}

// idempotencyLayer builds one idempotency middleware: principalOf derives the
// caller a claim is scoped on ("" means pass through uncached), and skip, when
// set, names requests this layer must leave to another one.
func idempotencyLayer(db *database.DB, principalOf func(*http.Request) string, skip func(*http.Request) bool) func(http.Handler) http.Handler {
	store := &idempotencyStore{db: db}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if db == nil {
				next.ServeHTTP(w, r)
				return
			}

			if skip != nil && skip(r) {
				next.ServeHTTP(w, r)
				return
			}

			// Only mutating methods participate, as before.
			if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch {
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
			if !validIdempotencyKey(clientKey) {
				respondIdempotencyError(w, r, http.StatusBadRequest, codeValidationFailed,
					IdempotencyHeader+" must be 1 to 255 printable ASCII characters",
					idempotencyErrorDetail{Field: IdempotencyHeader, Message: "must be 1 to 255 printable ASCII characters"})
				return
			}

			principal := principalOf(r)
			if principal == "" {
				next.ServeHTTP(w, r)
				return
			}

			// The fingerprint binds the key to this request, so the body must
			// be read here and handed to the handler unchanged.
			body, err := readRequestBody(r)
			if err != nil {
				// The request size limit (MaxBytesReader) is the 413; any
				// other read failure is the client's (a dropped connection,
				// a malformed body) and answers 400.
				var maxBytes *http.MaxBytesError
				if errors.As(err, &maxBytes) {
					respondIdempotencyError(w, r, http.StatusRequestEntityTooLarge, codePayloadTooLarge,
						"Request body exceeds the size limit")
				} else {
					respondIdempotencyError(w, r, http.StatusBadRequest, codeBadRequest,
						"The request body could not be read")
				}
				return
			}
			fingerprint := requestFingerprint(r.Method, r.URL.Path, canonicalQuery(r.URL.Query()), body)

			claimed, claimID, holder, err := store.acquire(r.Context(), principal, clientKey, fingerprint,
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
					respondIdempotencyError(w, r, http.StatusUnprocessableEntity, codeIdempotencyKeyReused,
						"This idempotency key was already used with a different request (method, path, query or body differs)")
					return
				}
				if holder.state == idempotencyStateComplete {
					replayStoredResponse(w, holder)
					return
				}
				respondIdempotencyError(w, r, http.StatusConflict, codeIdempotencyInProgress,
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
					if rerr := store.release(context.WithoutCancel(r.Context()), principal, clientKey, claimID); rerr != nil {
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
			if crw.status >= 200 && crw.status < 400 {
				if crw.body.Len() > idempotencyMaxStoredBody {
					// Served in full to this caller, but never stored: the
					// retry re-runs the handler rather than the table holding
					// an unbounded body.
					slog.Warn("idempotency: response body over the stored cap; served, released, not stored",
						"key", clientKey, "principal", principal, "status", crw.status,
						"bytes", crw.body.Len(), "cap", idempotencyMaxStoredBody)
					if rerr := store.release(bctx, principal, clientKey, claimID); rerr != nil {
						logIdempotencyError("release after oversized response", clientKey, rerr)
					}
				} else if cerr := store.complete(bctx, principal, clientKey, claimID, crw.status,
					crw.Header().Get("Content-Type"), crw.Header().Get("Location"), crw.body.Bytes(),
					time.Now().Add(idempotencyRetention)); cerr != nil {
					logIdempotencyError("store response", clientKey, cerr)
				}
			} else {
				// A 4xx is validation the client must fix and a 5xx a server
				// fault: neither is a stored outcome, and both leave the key
				// claimable again so the client can retry.
				if rerr := store.release(bctx, principal, clientKey, claimID); rerr != nil {
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

// validIdempotencyKey accepts 1 to 255 printable ASCII characters (0x20
// through 0x7e). The key is an opaque client-chosen token, but it becomes a
// table value and a log field, so it must be bounded and printable.
func validIdempotencyKey(key string) bool {
	if len(key) < 1 || len(key) > 255 {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

// replayStoredResponse answers with the stored response and marks the replay.
// The stored Location (a 201's or a 3xx's) is replayed with it; a session
// cookie is never stored, so none is ever replayed.
func replayStoredResponse(w http.ResponseWriter, h idempotencyHolder) {
	if h.contentType != "" {
		w.Header().Set("Content-Type", h.contentType)
	}
	if h.location != "" {
		w.Header().Set("Location", h.location)
	}
	w.Header().Set(IdempotencyReplayedHeader, "true")
	w.WriteHeader(h.statusCode)
	_, _ = w.Write(h.body)
}

// idempotencyErrorDetail is one entry of the wire envelope's details array
// (the error shape of docs/adr/0001-wire-contract.md): the offending field's
// path and what is wrong with it.
type idempotencyErrorDetail struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// idempotencyErrorEnvelope is the standard error envelope this layer answers
// with: a machine code, the message in full, a details array (always an
// array, never null) and the request id in meta. Held locally rather than
// shared with httputil because the details field has not landed there yet;
// the shapes are the wire contract's.
type idempotencyErrorEnvelope struct {
	Error struct {
		Code    string                   `json:"code"`
		Message string                   `json:"message"`
		Details []idempotencyErrorDetail `json:"details"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
}

// Machine codes this layer answers with: the wire contract's table, plus the
// two idempotency outcomes reserved for this middleware.
const (
	codeIdempotencyInProgress = "idempotency_in_progress"
	codeIdempotencyKeyReused  = "idempotency_key_reused"
	codeValidationFailed      = "validation_failed"
	codeBadRequest            = "bad_request"
	codePayloadTooLarge       = "payload_too_large"
)

// respondIdempotencyError answers with the wire contract's error envelope,
// keeping the specific message (httputil.RespondError would replace it with a
// generic one; these errors only help if the client can tell what to do next).
func respondIdempotencyError(w http.ResponseWriter, r *http.Request, status int, code, message string, details ...idempotencyErrorDetail) {
	reqID := w.Header().Get("X-Request-ID")
	if reqID == "" {
		reqID = r.Header.Get("X-Request-ID")
	}
	slog.Warn(message,
		"status", status,
		"code", code,
		"method", r.Method,
		"path", r.URL.Path,
		"request_id", reqID,
	)
	var env idempotencyErrorEnvelope
	env.Error.Code = code
	env.Error.Message = message
	if details == nil {
		details = []idempotencyErrorDetail{}
	}
	env.Error.Details = details
	env.Meta.RequestID = reqID
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}
