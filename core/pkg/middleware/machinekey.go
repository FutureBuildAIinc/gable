// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
)

// Machine keys. A Bearer token whose shape is a machine key (minted by
// internal/techadmin GenerateKey as "sk_live_" plus base64url; a JWT is three
// dot separated base64url segments and never carries this prefix) authenticates
// against the api_keys table through techadmin.ValidateKey and carries per
// module read and write scopes. The wire ADR's seam rules apply to it: it is a
// principal on the /api/v1 module routes only. See docs/adr/0002-machine-keys.md.

// machineKeyPrefix is the prefix every GenerateKey key carries. The shape
// check is on the prefix only; ValidateKey does the real work (length, hash
// comparison, revocation).
const machineKeyPrefix = "sk_live_"

// IsMachineKey reports whether a Bearer token is a machine key by its shape:
// the sk_live_ prefix GenerateKey mints. A JWT (three dot separated segments,
// typically beginning "eyJ") never matches, so the dispatch between the two
// Bearer credential kinds is exact.
func IsMachineKey(token string) bool {
	return len(token) > len(machineKeyPrefix) && strings.HasPrefix(token, machineKeyPrefix)
}

// Scope suffixes. A module's read scope admits GET and HEAD; its write scope
// admits every other method.
const (
	scopeReadSuffix  = ":read"
	scopeWriteSuffix = ":write"
)

// ModuleForPath returns the module a request path belongs to for scope
// purposes: the first path segment under /api/v1/, verbatim. The scope a key
// needs for a call is therefore derivable from the URL alone, with no lookup
// table: GET /api/v1/quotes/{id} needs quotes:read, POST /api/v1/quotes needs
// quotes:write. ok is false for paths that are not /api/v1 module routes
// (other seams, or a bare /api/v1 prefix), which the machine-key path refuses.
func ModuleForPath(path string) (module string, ok bool) {
	rest, found := strings.CutPrefix(path, "/api/v1/")
	if !found || rest == "" {
		return "", false
	}
	segment, _, _ := strings.Cut(rest, "/")
	if segment == "" {
		return "", false
	}
	return segment, true
}

// ModuleScopePolicy says what the machine-key system knows about a module
// segment.
type ModuleScopePolicy int

const (
	// ModuleScopeAllowed: the segment is a module whose routes a key holding
	// the module's scope may call.
	ModuleScopeAllowed ModuleScopePolicy = iota
	// ModuleScopeExcluded: the segment sits under /api/v1 but is not a module
	// route; it keeps its own authentication and a machine key is refused.
	ModuleScopeExcluded
	// ModuleScopeUnknown: the segment is declared neither in the vocabulary
	// nor in the exclusions. The census test keeps this unreachable for
	// registered routes; the middleware refuses it fail closed.
	ModuleScopeUnknown
)

// machineKeyExcludedSegments lists /api/v1 segments that are not module
// routes. a2a is the agent-to-agent JWS seam with its own published contract
// (ADR 0001): it is a public path the auth middleware never unpacks, and a
// machine key is not a principal there.
var machineKeyExcludedSegments = map[string]string{
	"a2a": "agent-to-agent JWS seam; keeps its own authentication",
}

// machineKeyModules is the scope vocabulary: every first path segment under
// /api/v1 that names a module a scoped key can address. It mirrors the route
// census (api/ROUTES.txt), and the census test fails when the two disagree in
// either direction, so a new route cannot appear without its module (and
// therefore its scope) being declared here.
var machineKeyModules = map[string]struct{}{
	"accounts":        {},
	"activities":      {},
	"admin":           {},
	"ap":              {},
	"apps":            {},
	"bankrecon":       {},
	"branches":        {},
	"configurator":    {},
	"contacts":        {},
	"credit-memos":    {},
	"customers":       {},
	"dashboard":       {},
	"delivery":        {},
	"deposits":        {},
	"documents":       {},
	"edi":             {},
	"events":          {},
	"gl":              {},
	"governance":      {},
	"inventory":       {},
	"invoices":        {},
	"locations":       {},
	"market-indices":  {},
	"matching":        {},
	"me":              {},
	"millwork":        {},
	"orders":          {},
	"parsing":         {},
	"payment-terms":   {},
	"payments":        {},
	"pos":             {},
	"price_levels":    {},
	"pricing":         {},
	"products":        {},
	"purchase-orders": {},
	"quotes":          {},
	"reports":         {},
	"reporting":       {},
	"sales-team":      {},
	"ship-tos":        {},
	"tax":             {},
	"users":           {},
	"vendors":         {},
	"vision":          {},
}

// ModuleScopePolicyFor returns the scope policy of a module segment.
func ModuleScopePolicyFor(module string) ModuleScopePolicy {
	if _, ok := machineKeyModules[module]; ok {
		return ModuleScopeAllowed
	}
	if _, ok := machineKeyExcludedSegments[module]; ok {
		return ModuleScopeExcluded
	}
	return ModuleScopeUnknown
}

// MachineKeyModules returns the scope vocabulary, sorted, for tests and
// tooling.
func MachineKeyModules() []string {
	out := make([]string, 0, len(machineKeyModules))
	for m := range machineKeyModules {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// RequiredScope returns the scope a machine key must hold for a method on a
// module: reads are GET and HEAD, writes are everything else.
func RequiredScope(module string, method string) string {
	if method == http.MethodGet || method == http.MethodHead {
		return module + scopeReadSuffix
	}
	return module + scopeWriteSuffix
}

// userOnlyRoute is a route prefix a machine key may never reach, with the
// refusal message that says why.
type userOnlyRoute struct {
	prefix  string
	message string
}

// machineKeyUserOnlyRoutes lists route prefixes a machine key may never
// reach, whatever scope it holds, because the route resolves its actor as a
// human user the JWT names. Key management: a key that could mint or revoke
// keys would be a key that could grant itself everything. The me routes:
// the caller there is the user, and a machine key has no user, so it has no
// "me" (before this rule a keyed caller's missing claims fell into the dev
// mode fallback and read every branch). The census test fails if a prefix
// matches no registered route.
var machineKeyUserOnlyRoutes = []userOnlyRoute{
	{"/api/v1/admin/keys", "key management requires a user"},
	{"/api/v1/me", "a machine key has no user"},
}

// MachineKeyUserOnlyPrefixes returns the user-only prefixes, for tests.
func MachineKeyUserOnlyPrefixes() []string {
	out := make([]string, len(machineKeyUserOnlyRoutes))
	for i, route := range machineKeyUserOnlyRoutes {
		out[i] = route.prefix
	}
	return out
}

// underUserOnlyPrefix reports whether a path is a user-only prefix itself or
// sits under it at a segment boundary, so /api/v1/me refuses keys without
// also swallowing a future /api/v1/metrics module.
func underUserOnlyPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// --- the machine-key auth core ----------------------------------------------

// KeyPrincipal is the identity a validated machine key carries.
type KeyPrincipal struct {
	// ID is the api_keys row's id, the value audit rows attribute to.
	ID string
	// Scopes are the key's stored scopes, verbatim.
	Scopes []string
}

// ErrInvalidMachineKey is the credential verdict: the presented key is
// unknown, revoked, or malformed. A KeyValidator returns it (wrapped or
// verbatim) for anything that is the caller's credential failing, as opposed
// to the key store being unreachable (any other error, answered 503).
var ErrInvalidMachineKey = errors.New("invalid machine key")

// KeyValidator checks a raw Bearer machine key. The techadmin service
// satisfies it through a small adapter at the wiring site (cmd/server).
type KeyValidator interface {
	ValidateKey(ctx context.Context, rawKey string) (KeyPrincipal, error)
}

// KeyRefusalAuditor records a refused machine-key request. pkg/audit
// implements it; it is an interface here because pkg/audit imports this
// package (request ids), so this package cannot import pkg/audit. A nil
// auditor refuses exactly the same and simply leaves no row.
type KeyRefusalAuditor interface {
	AuditKeyRefusal(ctx context.Context, keyID, action, scope, method, path string)
}

// Refusal actions written to the audit log.
const (
	// AuditActionKeyScopeRefused: the key is valid but holds neither the
	// module's read nor its write scope for this method.
	AuditActionKeyScopeRefused = "key.scope_refused"
	// AuditActionKeyUserRequired: the route is user-only (key management);
	// no scope would admit a key.
	AuditActionKeyUserRequired = "key.user_required"
	// AuditActionKeyPathRefused: the path is not a /api/v1 module route the
	// key system knows (another seam, or an undeclared module).
	AuditActionKeyPathRefused = "key.path_refused"
)

// keyIDContextKey and keyScopesContextKey carry the authenticated machine
// key through the request context. The key id's canonical home is this
// package because pkg/actor reads it while importing this package;
// actor.WithKeyID and actor.KeyIDFromContext delegate to these.
const (
	keyIDContextKey     contextKey = "machine_key_id"
	keyScopesContextKey contextKey = "machine_key_scopes"
)

// WithKeyID records a machine key's id in the context, for audit
// attribution. Set by the machine-key auth core once a key validates.
func WithKeyID(ctx context.Context, keyID string) context.Context {
	return context.WithValue(ctx, keyIDContextKey, keyID)
}

// KeyIDFromContext returns the machine key id the auth layer set, if any.
func KeyIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(keyIDContextKey).(string)
	return id, ok && id != ""
}

// WithKeyScopes records the key's scopes in the context, so handlers and
// guards can see what the caller holds.
func WithKeyScopes(ctx context.Context, scopes []string) context.Context {
	return context.WithValue(ctx, keyScopesContextKey, scopes)
}

// KeyScopesFromContext returns the authenticated machine key's scopes. The
// second return is false when the request did not authenticate with a
// machine key.
func KeyScopesFromContext(ctx context.Context) ([]string, bool) {
	scopes, ok := ctx.Value(keyScopesContextKey).([]string)
	return scopes, ok
}

// MachineKeyAuth is the machine-key half of the auth layer. The JWT
// middleware dispatches to it on a machine-key-shaped Bearer token
// (AuthConfig.MachineKeys); with the JWT layer off (AUTH_MODE=dev) it mounts
// standalone through Handler. Both mounts apply the same rules.
type MachineKeyAuth struct {
	keys        KeyValidator
	auditor     KeyRefusalAuditor
	publicPaths []string
	logger      *slog.Logger
}

// NewMachineKeyAuth builds the machine-key auth core. keys must be non-nil;
// auditor may be nil (refusals are then not audited); logger may be nil (a
// default logger is used).
func NewMachineKeyAuth(keys KeyValidator, auditor KeyRefusalAuditor, publicPaths []string, logger *slog.Logger) *MachineKeyAuth {
	if logger == nil {
		logger = slog.Default()
	}
	return &MachineKeyAuth{keys: keys, auditor: auditor, publicPaths: publicPaths, logger: logger}
}

// Handler is the standalone mount, used where the JWT layer is off
// (AUTH_MODE=dev): a request carrying no machine-key Bearer passes through
// untouched (dev serves it unauthenticated, as before), a machine-key Bearer
// is validated and scope checked exactly as behind the JWT middleware, and
// public paths (the integration and a2a seams, health, metrics) are skipped
// so those seams keep their own authentication in both modes.
func (a *MachineKeyAuth) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.isPublic(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		token, ok := bearerToken(r)
		if !ok || !IsMachineKey(token) {
			next.ServeHTTP(w, r)
			return
		}
		a.handle(w, r, token, next)
	})
}

// bearerToken extracts an Authorization: Bearer token, reporting whether the
// header carries one in the exact shape the auth layer accepts.
func bearerToken(r *http.Request) (string, bool) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", false
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return "", false
	}
	return parts[1], true
}

// isPublic mirrors the JWT middleware's public path semantics: a trailing
// slash means prefix, anything else exact match.
func (a *MachineKeyAuth) isPublic(path string) bool {
	return isPublicPath(a.publicPaths, path)
}

func isPublicPath(publicPaths []string, path string) bool {
	for _, p := range publicPaths {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(path, p) {
				return true
			}
		} else if path == p {
			return true
		}
	}
	return false
}

// handle authorizes one request whose Bearer token is a machine key: it
// validates the key, refuses it anywhere a key is not a principal, checks
// the module scope, and on success carries the key's id and scopes into the
// request context (the id through the value pkg/actor reads, so audit rows
// attribute to the key).
func (a *MachineKeyAuth) handle(w http.ResponseWriter, r *http.Request, rawKey string, next http.Handler) {
	principal, err := a.keys.ValidateKey(r.Context(), rawKey)
	if err != nil {
		if errors.Is(err, ErrInvalidMachineKey) {
			a.logger.Warn("machine key rejected", "method", r.Method, "path", r.URL.Path, "request_id", GetRequestID(r.Context()))
			respondAuthError(w, r, http.StatusUnauthorized, "unauthorized", "invalid machine key")
			return
		}
		a.logger.Error("machine key validation failed", "error", err, "method", r.Method, "path", r.URL.Path, "request_id", GetRequestID(r.Context()))
		respondAuthError(w, r, http.StatusServiceUnavailable, "unavailable", "machine key validation is unavailable")
		return
	}

	// The context carries the key id from here on, so both the refusal
	// audit rows and any downstream writes attribute to it.
	ctx := WithKeyID(r.Context(), principal.ID)

	module, isModuleRoute := ModuleForPath(r.URL.Path)
	if !isModuleRoute || ModuleScopePolicyFor(module) != ModuleScopeAllowed {
		// Fail closed: a machine key is a principal on declared /api/v1
		// module routes only. Public seams never reach here (the caller's
		// public path check runs first); anything else is refused.
		a.auditRefusal(ctx, principal.ID, AuditActionKeyPathRefused, "", r)
		respondAuthError(w, r, http.StatusForbidden, "forbidden", "machine keys are accepted on module routes under /api/v1 only")
		return
	}

	for _, route := range machineKeyUserOnlyRoutes {
		if underUserOnlyPrefix(r.URL.Path, route.prefix) {
			a.auditRefusal(ctx, principal.ID, AuditActionKeyUserRequired, "", r)
			respondAuthError(w, r, http.StatusForbidden, "forbidden", route.message)
			return
		}
	}

	scope := RequiredScope(module, r.Method)
	if !scopeHeld(principal.Scopes, scope) {
		a.auditRefusal(ctx, principal.ID, AuditActionKeyScopeRefused, scope, r)
		respondAuthError(w, r, http.StatusForbidden, "forbidden", "machine key lacks required scope "+scope)
		return
	}

	ctx = WithKeyScopes(ctx, principal.Scopes)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// scopeHeld is an exact match. There are no wildcard scopes: no existing key
// data needs them (the table is empty at base and nothing seeds scopes), and
// an exact vocabulary keeps a granted scope auditable as written.
func scopeHeld(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

func (a *MachineKeyAuth) auditRefusal(ctx context.Context, keyID, action, scope string, r *http.Request) {
	if a.auditor == nil {
		return
	}
	a.auditor.AuditKeyRefusal(ctx, keyID, action, scope, r.Method, r.URL.Path)
}

// --- the ADR error envelope, for auth-layer refusals -------------------------

// authErrorBody is the wire ADR's error envelope (section 3). The shared
// writer lands with the platform packages; the auth layer writes the same
// shape here, as pkg/actor already does for its one rejection.
type authErrorBody struct {
	Error struct {
		Code    string   `json:"code"`
		Message string   `json:"message"`
		Details []string `json:"details,omitempty"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
}

// respondAuthError writes an auth-layer refusal in the ADR envelope: a
// lowercase snake_case code from the ADR table, the specific message kept
// verbatim (4xx only; this layer writes no 500 with detail), and the request
// id so a client can quote one identifier for any response.
func respondAuthError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	reqID := w.Header().Get("X-Request-ID")
	if reqID == "" {
		reqID = r.Header.Get("X-Request-ID")
	}

	var body authErrorBody
	body.Error.Code = code
	body.Error.Message = message
	body.Meta.RequestID = reqID

	slog.Warn("auth refusal",
		"code", code,
		"status", status,
		"method", r.Method,
		"path", r.URL.Path,
		"request_id", reqID,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
