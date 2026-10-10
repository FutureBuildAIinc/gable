// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
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
// admits every other method; propose admits the draft routes of a confirm
// gated module and commit admits those plus the promotion (ADR 0007 5.1).
const (
	scopeReadSuffix    = ":read"
	scopeWriteSuffix   = ":write"
	scopeProposeSuffix = ":propose"
	scopeCommitSuffix  = ":commit"
)

// ModuleForPath returns the first path segment under /api/v1/, verbatim. It
// is the vocabulary key the census and the module policy check use; scope
// resolution is ScopeTarget's (ADR 0007 section 5.2), which refines it for
// the two delegating segments (drafts, links) and the admin areas.
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
	"ar":              {},
	"bankrecon":       {},
	"branches":        {},
	"configurator":    {},
	"contacts":        {},
	"credit-memos":    {},
	"customers":       {},
	"dashboard":       {},
	"delivery":        {},
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
	"charge-codes":    {},
	"ship-tos":        {},
	"tax":             {},
	"units":           {},
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
// module: reads are GET and HEAD, writes are everything else. This is the
// plain module level rule; RequiredScopeForPath refines it for the modules
// that declare finer scopes (ADR 0009) and is what the auth core serves.
func RequiredScope(module string, method string) string {
	if method == http.MethodGet || method == http.MethodHead {
		return module + scopeReadSuffix
	}
	return module + scopeWriteSuffix
}

// adminAreaScopes refines the admin module's scope by the second path segment
// under /api/v1/admin (ADR 0002's known limits, narrowed on ADR 0009): a key
// granted settings authority must not reach the staff roster or the module
// kill switches, and the scope stays derivable from the URL alone because the
// area is the second segment. One scope admits every method on its area's
// routes: an operator who may save the AI key may see its hint, and the
// read/write split is the module level rule, not the area's.
var adminAreaScopes = map[string]string{
	"settings": "admin:settings",
	"staff":    "admin:staff",
	"modules":  "admin:modules",
}

// writeScopeOverrides replaces a module's write scope name with a finer one
// (ADR 0009): the users module's only writes are the branch grant, revoke and
// home branch routes, so its write scope is named for what it grants. The
// module's read scope keeps the plain name.
var writeScopeOverrides = map[string]string{
	"users": "users:grants",
}

// RequiredScopeForPath returns the scope a machine key must hold for a method
// and path, refining the plain module rule where a module declares finer
// scopes: an admin area (the second segment under /api/v1/admin) needs its
// area scope for every method, and a module with a write override needs the
// override's name for every non read. ok is false wherever ModuleForPath
// refuses; the caller checks the module policy separately.
//
// An empty second segment under /api/v1/admin (the path is /api/v1/admin with
// nothing after, or // appears after it) is refused: the coarse admin:read or
// admin:write fallback would be too wide for a path the request does not
// name, and the "coarse scopes reach only routes no area declares" guarantee
// (ADR 0009 section 5) must not depend on the router cleaning the path.
//
// A "." or ".." segment under /api/v1/admin, or any path not equal to
// path.Clean(path), is refused for the same reason: the request did not name
// the clean area, so the area rule cannot be the answer. Without this check
// the router's 307 redirect would authorise the redirected request on the
// clean path, and the ADR 0009 guarantee would lean on the router cleaning
// the path for it.
func RequiredScopeForPath(method string, p string) (string, bool) {
	module, ok := ModuleForPath(p)
	if !ok {
		return "", false
	}
	if module == "admin" {
		rest, _ := strings.CutPrefix(p, "/api/v1/admin")
		segment, _, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/")
		if segment == "" || segment == "." || segment == ".." || p != path.Clean(p) {
			return "", false
		}
		if area, ok := adminAreaScopes[segment]; ok {
			return area, true
		}
	}
	if method != http.MethodGet && method != http.MethodHead {
		if finer, ok := writeScopeOverrides[module]; ok {
			return finer, true
		}
	}
	return RequiredScope(module, method), true
}

// FinerAdminScopes returns the admin module's area scopes, sorted, so tests
// and tooling can hold them against the route census.
func FinerAdminScopes() []string {
	out := make([]string, 0, len(adminAreaScopes))
	for _, scope := range adminAreaScopes {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

// --- route classes and the delegating segments (ADR 0007 section 5) ---------

// ScopeClass is the class of route a method and path resolve to. The class,
// not the method alone, decides which scopes admit a keyed request: the
// seven draft shapes and the two link shapes each have their own row in the
// policy table (ADR 0007 section 5.1).
type ScopeClass int

const (
	// ScopeEntityRead: GET and HEAD under /api/v1/m/..., admitted by m:read.
	ScopeEntityRead ScopeClass = iota
	// ScopeEntityWrite: every other method under /api/v1/m/..., admitted by
	// m:write (or the module's finer write name).
	ScopeEntityWrite
	// ScopeDraftRead: the draft list, the draft read and the feed of a
	// confirm gated module, admitted by m:propose or m:commit.
	ScopeDraftRead
	// ScopeDraftWrite: draft create, PUT and transitions, admitted by
	// m:propose or m:commit.
	ScopeDraftWrite
	// ScopePromotion: POST /api/v1/drafts/m/{id}/promote, admitted by
	// m:commit only.
	ScopePromotion
	// ScopeLink: GET /api/v1/links/m/{id}, admitted by m:read.
	ScopeLink
	// ScopeDraftLink: GET /api/v1/links/drafts/m/{id}, admitted by
	// m:propose or m:commit.
	ScopeDraftLink
)

// String names the class for tests and refusal messages.
func (c ScopeClass) String() string {
	switch c {
	case ScopeEntityRead:
		return "entity_read"
	case ScopeEntityWrite:
		return "entity_write"
	case ScopeDraftRead:
		return "draft_read"
	case ScopeDraftWrite:
		return "draft_write"
	case ScopePromotion:
		return "promotion"
	case ScopeLink:
		return "link"
	case ScopeDraftLink:
		return "draft_link"
	}
	return "unknown"
}

// confirmGatedModules lists the modules with a registered draft kind (the
// kind registry lives in internal/drafts; a test there holds this set
// against it). propose and commit are grantable only on these modules, and
// the census test holds every draft route's module against this set.
// "drafts" and "links" never appear here: they are delegating segments, not
// modules, and cannot be granted.
var confirmGatedModules = map[string]struct{}{
	"quotes": {},
	"orders": {},
}

// ConfirmGatedModules returns the confirm gated module set, sorted, for
// tests, the mint's grammar check and the census test.
func ConfirmGatedModules() []string {
	out := make([]string, 0, len(confirmGatedModules))
	for m := range confirmGatedModules {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// IsConfirmGated reports whether the module has a registered draft kind.
func IsConfirmGated(module string) bool {
	_, ok := confirmGatedModules[module]
	return ok
}

// ScopeTarget resolves a method and path to the scope module and route class
// a keyed request is judged against (ADR 0007 section 5.2). Two segments are
// delegating: under /api/v1/drafts/ and /api/v1/links/ the module is the next
// segment, and the class is decided by matching the whole method and path
// against the seven draft shapes and the two link shapes, never by the method
// or the last segment alone. For every other path the module is the first
// segment (with the admin areas' finer module names, ADR 0009) and the class
// is the entity read/write split.
//
// It fails closed: ok is false for anything that is not /api/v1, and for any
// method and shape under /api/v1/drafts/m/ or /api/v1/links/ that the policy
// table does not name, so a later draft route cannot quietly fall into the
// draft write class. Doubled slashes and dot segments never widen anything:
// they fail the prefix or name a module outside the vocabulary, and the
// router's own cleaning redirects them for every caller anyway.
func ScopeTarget(method, path string) (module string, class ScopeClass, ok bool) {
	rest, found := strings.CutPrefix(path, "/api/v1/")
	if !found || rest == "" {
		return "", 0, false
	}
	segments := strings.Split(rest, "/")
	// Doubled slashes (an empty segment), a trailing slash and dot segments
	// never resolve: the router redirects their cleaned spelling for every
	// caller, and admitting one here would judge a request the router never
	// serves.
	for _, s := range segments {
		if s == "" || s == "." || s == ".." {
			return "", 0, false
		}
	}
	read := method == http.MethodGet || method == http.MethodHead

	switch segments[0] {
	case "drafts":
		if len(segments) < 2 {
			return "", 0, false
		}
		m := segments[1]
		switch {
		case len(segments) == 2:
			if read {
				return m, ScopeDraftRead, true // the list
			}
			if method == http.MethodPost {
				return m, ScopeDraftWrite, true // create
			}
			return "", 0, false
		case len(segments) == 3:
			if read {
				return m, ScopeDraftRead, true // the read and the feed
			}
			if method == http.MethodPut {
				return m, ScopeDraftWrite, true // payload replace
			}
			return "", 0, false
		case len(segments) == 4:
			switch segments[3] {
			case "transitions":
				if method == http.MethodPost {
					return m, ScopeDraftWrite, true
				}
			case "promote":
				if method == http.MethodPost {
					return m, ScopePromotion, true
				}
			}
			return "", 0, false
		default:
			return "", 0, false
		}

	case "links":
		switch {
		case len(segments) == 3 && read && segments[1] != "drafts":
			return segments[1], ScopeLink, true
		case len(segments) == 4 && read && segments[1] == "drafts":
			return segments[2], ScopeDraftLink, true
		default:
			return "", 0, false
		}

	case "admin":
		if len(segments) < 2 || segments[1] == "" {
			// /api/v1/admin with nothing after: no area is named, and the
			// coarse fallback would be wider than the path deserves.
			return "", 0, false
		}
		if _, isArea := adminAreaScopes[segments[1]]; isArea {
			// The scope module carries the area ("admin/settings") so
			// AdmittedScopes names the finer scope for every method (ADR
			// 0009); the vocabulary check the auth core runs stays on the
			// path's first segment.
			module = "admin/" + segments[1]
		} else {
			module = "admin"
		}
		if read {
			return module, ScopeEntityRead, true
		}
		return module, ScopeEntityWrite, true

	default:
		if read {
			return segments[0], ScopeEntityRead, true
		}
		return segments[0], ScopeEntityWrite, true
	}
}

// PolicyModuleForPath names the module whose scope policy judges a path: the
// first segment, except under the delegating segments (drafts and links),
// where the module is the one ScopeTarget delegates to, because the segments
// themselves are not modules and never appear in the vocabulary (ADR 0007
// section 5.2). The auth core and the census test share it, so a keyed
// request and the drift gate cannot disagree.
func PolicyModuleForPath(method, path string) (string, bool) {
	first, ok := ModuleForPath(path)
	if !ok {
		return "", false
	}
	if first == "drafts" || first == "links" {
		module, _, ok := ScopeTarget(method, path)
		return module, ok
	}
	return first, true
}

// AdmittedScopes returns the scopes that admit a keyed request on a module's
// route class, the whole policy table of ADR 0007 section 5.1: matching is
// exact, with no wildcard and no implication between verbs, and a grant
// reads back as written. The first entry is the refused scope the audit row
// records when none is held. drafts and links never appear as a module. An
// admin area module ("admin/settings", from ScopeTarget) resolves to its one
// area scope for every method (ADR 0009).
func AdmittedScopes(module string, class ScopeClass) []string {
	if area, isArea := strings.CutPrefix(module, "admin/"); isArea {
		if scope, ok := adminAreaScopes[area]; ok {
			return []string{scope}
		}
	}
	switch class {
	case ScopeEntityRead:
		return []string{module + scopeReadSuffix}
	case ScopeEntityWrite:
		if finer, ok := writeScopeOverrides[module]; ok {
			return []string{finer}
		}
		return []string{module + scopeWriteSuffix}
	case ScopeDraftRead, ScopeDraftWrite, ScopeDraftLink:
		return []string{module + scopeProposeSuffix, module + scopeCommitSuffix}
	case ScopePromotion:
		return []string{module + scopeCommitSuffix}
	case ScopeLink:
		return []string{module + scopeReadSuffix}
	}
	return nil
}

// ValidScopeGrammar returns every scope a registered /api/v1 route can
// require: the module read and write scopes, with the finer names (admin
// areas, users:grants) in place of the coarse ones they replace, and the
// propose and commit verbs of the confirm gated modules (ADR 0007 section
// 5.1). The mint's validation (section 5.3) checks granted scopes against
// this set; it is the one source, so the mint and the auth core cannot
// disagree.
func ValidScopeGrammar() []string {
	var out []string
	for module := range machineKeyModules {
		out = append(out, module+scopeReadSuffix)
		if finer, ok := writeScopeOverrides[module]; ok {
			out = append(out, finer)
		} else {
			out = append(out, module+scopeWriteSuffix)
		}
	}
	for module := range confirmGatedModules {
		out = append(out, module+scopeProposeSuffix, module+scopeCommitSuffix)
	}
	out = append(out, FinerAdminScopes()...)
	sort.Strings(out)
	return out
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
	// BranchID is the branch bound key's pin (ADR 0007 section 5.5): nil is
	// today's unbound behaviour, a branch pins the request's branch context
	// to it.
	BranchID *uuid.UUID
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

// BranchRefusalAuditor records a branch bound key's refused X-Branch-Id
// (ADR 0007 section 5.5, the key.branch_refused row). Optional: when the
// wired auditor does not implement it the refusal is still served, with no
// row.
type BranchRefusalAuditor interface {
	AuditKeyBranchRefusal(ctx context.Context, keyID string, branch uuid.UUID, method, path string)
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
	// key system knows (another seam, or an undeclared module), or not a
	// shape the drafts and links policy table names.
	AuditActionKeyPathRefused = "key.path_refused"
	// AuditActionKeyBranchRefused: a branch bound key named another branch
	// in X-Branch-Id (ADR 0007 section 5.5).
	AuditActionKeyBranchRefused = "key.branch_refused"
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

	// The route class the whole method and path resolve to (ADR 0007 section
	// 5.2): drafts and links delegate to their second segment, and any shape
	// under them the policy table does not name fails closed here, so a
	// later draft route cannot quietly fall into the draft write class.
	scopeModule, class, isRoute := ScopeTarget(r.Method, r.URL.Path)
	policyModule, _ := PolicyModuleForPath(r.Method, r.URL.Path)
	if !isRoute || ModuleScopePolicyFor(policyModule) != ModuleScopeAllowed {
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

	admitted := AdmittedScopes(scopeModule, class)
	if !anyScopeHeld(principal.Scopes, admitted) {
		// The refusal audit row records the first admitted scope as the
		// refused one (ADR 0007 section 5.2).
		refused := ""
		if len(admitted) > 0 {
			refused = admitted[0]
		}
		a.auditRefusal(ctx, principal.ID, AuditActionKeyScopeRefused, refused, r)
		respondAuthError(w, r, http.StatusForbidden, "forbidden", "machine key lacks required scope "+refused)
		return
	}

	// A branch bound key is pinned to its branch (ADR 0007 section 5.5): a
	// request naming another branch in X-Branch-Id is refused with its audit
	// row; every other request carries the pin, which the branch middleware
	// (and, on routes without it, the context set here) turns into the
	// request's branch context, so lists, reads, drafts, the feed and links
	// see that branch only and a payload branch_id is held to it.
	if principal.BranchID != nil {
		if hdr := strings.TrimSpace(r.Header.Get("X-Branch-Id")); hdr != "" {
			if named, err := uuid.Parse(hdr); err != nil || named != *principal.BranchID {
				if ba, ok := a.auditor.(BranchRefusalAuditor); ok && ba != nil {
					ba.AuditKeyBranchRefusal(ctx, principal.ID, *principal.BranchID, r.Method, r.URL.Path)
				}
				respondAuthError(w, r, http.StatusForbidden, "forbidden", "a branch bound key may name only its own branch in X-Branch-Id")
				return
			}
		}
		ctx = branchctx.WithKeyBranch(ctx, *principal.BranchID)
		ctx = branchctx.With(ctx, &branchctx.Context{BranchID: principal.BranchID})
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

// anyScopeHeld is the class form of scopeHeld: a key reaches a route when it
// holds any one of the scopes its class admits (ADR 0007 section 5.1).
func anyScopeHeld(scopes, admitted []string) bool {
	for _, want := range admitted {
		if scopeHeld(scopes, want) {
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
