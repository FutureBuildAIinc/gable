// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/pkg/httputil"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

type AuthConfig struct {
	JWKSURL string
	// Issuer is the exact `iss` every token must carry. Required.
	Issuer string
	// Audience is the value that must appear in every token's `aud`. Required.
	// Without it a token minted for a *sibling* service in the same IdP would
	// verify here, since it is signed by the same JWKS.
	Audience string
	// Algorithms is the signing-algorithm allowlist. Required; build it with
	// ParseAlgorithms so symmetric/none entries are rejected.
	Algorithms  []string
	PublicPaths []string
}

type AuthMiddleware struct {
	jwks        keyfunc.Keyfunc
	parser      *jwt.Parser
	issuer      string
	audience    string
	publicPaths []string
	logger      *slog.Logger
}

// UserClaims holds standard OIDC claims and FB Brain custom claims.
type UserClaims struct {
	jwt.RegisteredClaims
	Email string   `json:"email,omitempty"`
	Roles []string `json:"roles,omitempty"`

	// FutureBuild Brain custom claims — populated when tokens are issued by Brain's OIDC.
	OrgID    string `json:"org_id,omitempty"`    // Brain tenant/org UUID
	Role     string `json:"role,omitempty"`      // Brain role: owner, admin, member
	PlanTier string `json:"plan_tier,omitempty"` // Brain plan: free, pro, enterprise
}

// Key for Context
type contextKey string

const UserContextKey contextKey = "user"

// NewAuthMiddleware initializes the JWKS fetcher and returns the middleware.
//
// It refuses to build a weakened verifier: issuer, audience and an
// asymmetric-only algorithm allowlist are all mandatory. main.go gates these
// at boot too; the duplication is deliberate, because a security boundary
// should not be constructible in a state that verifies less than it claims to.
func NewAuthMiddleware(ctx context.Context, cfg AuthConfig, logger *slog.Logger) (*AuthMiddleware, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("auth middleware: Issuer is required (AUTH_ISSUER)")
	}
	if cfg.Audience == "" {
		return nil, fmt.Errorf("auth middleware: Audience is required (AUTH_AUDIENCE)")
	}
	if len(cfg.Algorithms) == 0 {
		return nil, fmt.Errorf("auth middleware: Algorithms is required (build it with ParseAlgorithms)")
	}
	for _, alg := range cfg.Algorithms {
		upper := strings.ToUpper(alg)
		if upper == "NONE" || strings.HasPrefix(upper, "HS") {
			return nil, fmt.Errorf("auth middleware: refusing algorithm %q — JWKS verification never accepts HMAC or none", alg)
		}
	}

	// Create the JWKS from the URL.
	// This will fetch the keys immediately and cache them.
	// It handles refresh automatically based on Cache-Control headers or errors.
	// NewDefaultCtx (not NewDefault) ties the background refresh goroutine to
	// the caller's context instead of context.Background().
	k, err := keyfunc.NewDefaultCtx(ctx, []string{cfg.JWKSURL})
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS from URL %s: %w", cfg.JWKSURL, err)
	}

	return &AuthMiddleware{
		jwks:        k,
		parser:      newTokenParser(cfg.Issuer, cfg.Audience, cfg.Algorithms),
		issuer:      cfg.Issuer,
		audience:    cfg.Audience,
		publicPaths: cfg.PublicPaths,
		logger:      logger,
	}, nil
}

// newTokenParser builds the strict parser used for every access token.
//
// Every constraint here is load-bearing:
//   - WithValidMethods pins the signing algorithm, closing algorithm
//     confusion (and cross-algorithm substitution within one key type, e.g.
//     an RS256 key being accepted for PS256).
//   - WithIssuer / WithAudience are enforced unconditionally — the jwt
//     validator treats both as *required*, so a token missing `aud` entirely
//     is rejected rather than passing an empty comparison.
//   - WithExpirationRequired closes the token-minted-without-exp hole: by
//     default `exp` is only checked when present, so a token without one
//     would never expire.
func newTokenParser(issuer, audience string, algorithms []string) *jwt.Parser {
	return jwt.NewParser(
		jwt.WithValidMethods(algorithms),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
	)
}

// Handler is the actual middleware function
func (m *AuthMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 0. Check Public Paths
		// Paths ending with "/" are treated as prefixes (e.g. "/api/portal/v1/").
		// All other paths require an exact match.
		for _, path := range m.publicPaths {
			if strings.HasSuffix(path, "/") {
				if strings.HasPrefix(r.URL.Path, path) {
					next.ServeHTTP(w, r)
					return
				}
			} else if r.URL.Path == path {
				next.ServeHTTP(w, r)
				return
			}
		}

		// 1. Extract Token
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			m.logger.Warn("Missing Authorization header", "path", r.URL.Path)
			httputil.RespondError(w, r, "Unauthorized: No token provided", http.StatusUnauthorized, nil)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			m.logger.Warn("Invalid Authorization header format", "path", r.URL.Path)
			httputil.RespondError(w, r, "Unauthorized: Invalid token format", http.StatusUnauthorized, nil)
			return
		}
		tokenString := parts[1]

		// 2. Parse and Validate Token. m.parser pins the algorithm and
		// enforces iss/aud/exp; see newTokenParser.
		token, err := m.parser.ParseWithClaims(tokenString, &UserClaims{}, m.jwks.Keyfunc)
		if err != nil {
			// Log what we expected alongside the failure: the overwhelmingly
			// common cause of a 401 here is an AUTH_ISSUER/AUTH_AUDIENCE
			// mismatch with the IdP, and without these an operator is guessing.
			m.logger.Warn("Token validation failed",
				"error", err,
				"path", r.URL.Path,
				"expected_issuer", m.issuer,
				"expected_audience", m.audience)
			httputil.RespondError(w, r, "Unauthorized: Invalid token", http.StatusUnauthorized, nil)
			return
		}

		// 3. Verify the token parsed clean. Issuer, audience, algorithm and
		// expiry were all enforced by m.parser above — do not re-add a
		// conditional issuer check here; "verify only if configured" is how
		// the check silently disappears when config is missing.
		if !token.Valid {
			m.logger.Warn("Token is invalid", "path", r.URL.Path)
			httputil.RespondError(w, r, "Unauthorized: Invalid token", http.StatusUnauthorized, nil)
			return
		}

		claims, ok := token.Claims.(*UserClaims)
		if !ok {
			m.logger.Error("Failed to cast claims", "path", r.URL.Path)
			httputil.RespondError(w, r, "Internal Server Error", http.StatusInternalServerError, nil)
			return
		}

		// 4. Inject into Context
		ctx := context.WithValue(r.Context(), UserContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole returns middleware that restricts access to users with one of the allowed roles.
// In dev mode (no auth configured, claims == nil), requests pass through.
func RequireRole(allowedRoles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedRoles))
	for _, r := range allowedRoles {
		allowed[r] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := ClaimsFromContext(r.Context())
			if claims == nil {
				// Dev mode: no auth configured, pass through
				next.ServeHTTP(w, r)
				return
			}

			// Check Brain role (single role field)
			if claims.Role != "" && allowed[claims.Role] {
				next.ServeHTTP(w, r)
				return
			}

			// Check OIDC roles (array field)
			for _, role := range claims.Roles {
				if allowed[role] {
					next.ServeHTTP(w, r)
					return
				}
			}

			httputil.RespondError(w, r, "Forbidden: insufficient role", http.StatusForbidden, nil)
		})
	}
}

// --- FutureBuild Brain context helpers ---

// ClaimsFromContext retrieves UserClaims from the request context.
// Returns nil if no claims are present (unauthenticated or integration-key request).
func ClaimsFromContext(ctx context.Context) *UserClaims {
	claims, _ := ctx.Value(UserContextKey).(*UserClaims)
	return claims
}

// BrainOrgIDFromContext extracts the FB Brain org_id from JWT claims.
// Returns empty string if not present.
func BrainOrgIDFromContext(ctx context.Context) string {
	if claims := ClaimsFromContext(ctx); claims != nil {
		return claims.OrgID
	}
	return ""
}

// BrainRoleFromContext extracts the FB Brain role from JWT claims.
func BrainRoleFromContext(ctx context.Context) string {
	if claims := ClaimsFromContext(ctx); claims != nil {
		return claims.Role
	}
	return ""
}

// BrainPlanTierFromContext extracts the FB Brain plan tier from JWT claims.
func BrainPlanTierFromContext(ctx context.Context) string {
	if claims := ClaimsFromContext(ctx); claims != nil {
		return claims.PlanTier
	}
	return ""
}
