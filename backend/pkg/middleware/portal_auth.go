// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// portalContextKey is the key used to store portal claims in context.
type portalContextKeyType string

const PortalClaimsKey portalContextKeyType = "portal_claims"

// PortalClaims holds JWT claims for portal auth.
type PortalClaims struct {
	jwt.RegisteredClaims
	CustomerID     uuid.UUID `json:"customer_id"`
	CustomerUserID uuid.UUID `json:"customer_user_id"`
	Email          string    `json:"email"`
	Name           string    `json:"name"`
	Role           string    `json:"role"`
}

// PortalTokenIssuer and PortalTokenAudience are the iss/aud every portal token
// must carry. They live here, next to the verifier, and internal/portal imports
// them for minting — one definition, so the two sides cannot drift.
const (
	PortalTokenIssuer   = "gable-portal"
	PortalTokenAudience = "gable-portal-api"
)

// NewPortalTokenParser builds the strict parser used for every portal token,
// by the middleware and by portal.Service.ParseToken alike.
//
// Every constraint is load-bearing:
//   - WithValidMethods pins HS256. The portal secret is symmetric
//     (PORTAL_JWT_SECRET), so HMAC is CORRECT here — this is not a JWKS
//     boundary and must not be "upgraded" to an asymmetric allowlist. Pinning
//     the exact algorithm still closes cross-HMAC substitution.
//   - WithExpirationRequired closes the hole this replaced: jwt/v5 only checks
//     `exp` when it is PRESENT, so a token minted without one never expires.
//     A leaked portal token was immortal.
//   - WithIssuer / WithAudience are enforced unconditionally; the validator
//     treats both as required, so a token missing `aud` entirely is rejected
//     rather than passing an empty comparison.
func NewPortalTokenParser() *jwt.Parser {
	return jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(PortalTokenIssuer),
		jwt.WithAudience(PortalTokenAudience),
		jwt.WithExpirationRequired(),
	)
}

// PortalAuthMiddleware validates portal JWTs and injects customer context.
type PortalAuthMiddleware struct {
	jwtSecret []byte
	parser    *jwt.Parser
	logger    *slog.Logger
}

// NewPortalAuthMiddleware creates a new portal auth middleware.
func NewPortalAuthMiddleware(jwtSecret []byte, logger *slog.Logger) *PortalAuthMiddleware {
	return &PortalAuthMiddleware{
		jwtSecret: jwtSecret,
		parser:    NewPortalTokenParser(),
		logger:    logger,
	}
}

// Handler returns the middleware handler function.
func (m *PortalAuthMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Extract token — prefer httpOnly cookie, fall back to Authorization header
		var rawToken string

		if cookie, err := r.Cookie("portal_token"); err == nil && cookie.Value != "" {
			rawToken = cookie.Value
		} else {
			authHeader := r.Header.Get("Authorization")
			parts := strings.Split(authHeader, " ")
			if len(parts) == 2 && parts[0] == "Bearer" {
				rawToken = parts[1]
			}
		}

		if rawToken == "" {
			m.logger.Warn("PortalAuth: No token found in cookie or Authorization header", "path", r.URL.Path)
			httputil.RespondError(w, r, "Unauthorized", http.StatusUnauthorized, nil)
			return
		}

		// 2. Parse and validate JWT. m.parser pins HS256 and enforces
		//    iss/aud/exp — see NewPortalTokenParser.
		//
		//    The comment that used to sit here claimed "jwt/v5 validates exp by
		//    default — expired tokens are rejected automatically". That is true
		//    only if `exp` is PRESENT. A token minted without one sailed
		//    through, forever, from any issuer. The comment is why nobody
		//    looked; do not restore it.
		token, err := m.parser.ParseWithClaims(rawToken, &PortalClaims{}, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return m.jwtSecret, nil
		})
		if err != nil {
			m.logger.Warn("PortalAuth: Token validation failed", "error", err, "path", r.URL.Path)
			httputil.RespondError(w, r, "Unauthorized", http.StatusUnauthorized, nil)
			return
		}

		claims, ok := token.Claims.(*PortalClaims)
		if !ok || !token.Valid {
			m.logger.Warn("PortalAuth: Invalid token claims", "path", r.URL.Path)
			httputil.RespondError(w, r, "Unauthorized", http.StatusUnauthorized, nil)
			return
		}

		// 3. Verify essential claims
		if claims.CustomerID == uuid.Nil {
			m.logger.Warn("PortalAuth: Missing customer_id in claims", "path", r.URL.Path)
			httputil.RespondError(w, r, "Unauthorized", http.StatusUnauthorized, nil)
			return
		}

		// 4. Inject claims into context
		ctx := context.WithValue(r.Context(), PortalClaimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
