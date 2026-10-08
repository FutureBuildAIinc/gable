// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

type AuthConfig struct {
	JWKSURL     string
	Issuer      string
	PublicPaths []string
	// MachineKeys, when set, validates Bearer machine keys (the sk_live_
	// shape). When nil a machine-key Bearer is refused 401 fail closed
	// rather than reaching the JWT parser.
	MachineKeys *MachineKeyAuth
}

type AuthMiddleware struct {
	jwks        keyfunc.Keyfunc
	issuer      string
	publicPaths []string
	logger      *slog.Logger
	machineKeys *MachineKeyAuth
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

// NewAuthMiddleware initializes the JWKS fetcher and returns the middleware
func NewAuthMiddleware(ctx context.Context, cfg AuthConfig, logger *slog.Logger) (*AuthMiddleware, error) {
	// Create the JWKS from the URL.
	// This will fetch the keys immediately and cache them.
	// It handles refresh automatically based on Cache-Control headers or errors.
	k, err := keyfunc.NewDefault([]string{cfg.JWKSURL})
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS from URL %s: %w", cfg.JWKSURL, err)
	}

	return &AuthMiddleware{
		jwks:        k,
		issuer:      cfg.Issuer,
		publicPaths: cfg.PublicPaths,
		logger:      logger,
		machineKeys: cfg.MachineKeys,
	}, nil
}

// Handler is the actual middleware function
func (m *AuthMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 0. Check Public Paths
		// Paths ending with "/" are treated as prefixes (e.g. "/api/portal/v1/").
		// All other paths require an exact match.
		if isPublicPath(m.publicPaths, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// 1. Extract Token
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			m.logger.Warn("Missing Authorization header", "path", r.URL.Path)
			respondAuthError(w, r, http.StatusUnauthorized, "unauthorized", "no token provided")
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			m.logger.Warn("Invalid Authorization header format", "path", r.URL.Path)
			respondAuthError(w, r, http.StatusUnauthorized, "unauthorized", "invalid Authorization header format")
			return
		}
		tokenString := parts[1]

		// 1b. A Bearer machine key is not a JWT: dispatch by shape before
		// the JWT parser sees it. With no machine-key core wired the token
		// is refused fail closed; it must never fall into JWT parsing, which
		// would only produce a misleading parse error.
		if IsMachineKey(tokenString) {
			if m.machineKeys == nil {
				respondAuthError(w, r, http.StatusUnauthorized, "unauthorized", "machine keys are not accepted")
				return
			}
			m.machineKeys.handle(w, r, tokenString, next)
			return
		}

		// 2. Parse and Validate Token
		token, err := jwt.ParseWithClaims(tokenString, &UserClaims{}, m.jwks.Keyfunc)
		if err != nil {
			m.logger.Warn("Token validation failed", "error", err, "path", r.URL.Path)
			respondAuthError(w, r, http.StatusUnauthorized, "unauthorized", "invalid token")
			return
		}

		// 3. Verify Claims (Issuer)
		if !token.Valid {
			m.logger.Warn("Token is invalid", "path", r.URL.Path)
			respondAuthError(w, r, http.StatusUnauthorized, "unauthorized", "invalid token")
			return
		}

		claims, ok := token.Claims.(*UserClaims)
		if !ok {
			m.logger.Error("Failed to cast claims", "path", r.URL.Path)
			respondAuthError(w, r, http.StatusInternalServerError, "internal_error", "internal error")
			return
		}

		// Optional: Verify Issuer strictly if configured
		// Note: keyfunc handles signature, but we check business logic claims here
		if m.issuer != "" && claims.Issuer != m.issuer {
			m.logger.Warn("Token issuer mismatch", "expected", m.issuer, "got", claims.Issuer)
			respondAuthError(w, r, http.StatusUnauthorized, "unauthorized", "invalid issuer")
			return
		}

		// 4. Inject into Context
		ctx := context.WithValue(r.Context(), UserContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole returns middleware that restricts access to users with one of the allowed roles.
// A machine key has no roles: when the request authenticated with one, the
// role check is skipped, because the auth layer's scope check has already
// authorized the module (and key management routes, where a key must never
// pass, are refused before this guard runs). In dev mode (no auth configured,
// no key, claims == nil), requests pass through.
func RequireRole(allowedRoles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedRoles))
	for _, r := range allowedRoles {
		allowed[r] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := ClaimsFromContext(r.Context())
			if claims == nil {
				if _, ok := KeyScopesFromContext(r.Context()); ok {
					// A machine key: scope check already ran in the auth layer.
					next.ServeHTTP(w, r)
					return
				}
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

			respondAuthError(w, r, http.StatusForbidden, "forbidden", "insufficient role")
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
