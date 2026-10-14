// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware_test

import (
	"context"
	"crypto/rsa"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/golang-jwt/jwt/v5"
)

// subjectJWT signs a user token with the subject and roles given.
func subjectJWT(t *testing.T, key *rsa.PrivateKey, subject, role string, roles []string) string {
	t.Helper()
	claims := &middleware.UserClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "test-issuer",
			Subject:   subject,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		Role:  role,
		Roles: roles,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	s, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return s
}

// CORRECTNESS (r5 P3-7): a user token that bears a role but no subject is
// refused 401 in the auth layer. The branch rule reads an empty subject as
// "no user" and gives such a caller the every branch reach, and no grant can
// ever be keyed to an empty subject, so a role bearing token without one is
// never a caller the rest of the system can hold to its grants. A token with
// a subject passes as it always did.
func TestAuthRefusesARoleBearingTokenWithNoSubject(t *testing.T) {
	jwks, key := jwksServer(t)
	auth, err := middleware.NewAuthMiddleware(context.Background(), middleware.AuthConfig{
		JWKSURL: jwks.URL,
		Issuer:  "test-issuer",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewAuthMiddleware: %v", err)
	}
	for _, c := range []struct {
		name    string
		subject string
		role    string
		roles   []string
		want    int
	}{
		{"roles array, no subject", "", "", []string{"finance"}, http.StatusUnauthorized},
		{"role claim, no subject", "", "admin", nil, http.StatusUnauthorized},
		{"both, no subject", "", "owner", []string{"owner"}, http.StatusUnauthorized},
		{"roles array with a subject", "user-1", "", []string{"finance"}, http.StatusOK},
		{"role claim with a subject", "user-1", "admin", nil, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := &okHandler{}
			rec := httptest.NewRecorder()
			auth.Handler(h).ServeHTTP(rec, bearerRequest(t, "GET", "/api/v1/quotes", subjectJWT(t, key, c.subject, c.role, c.roles)))
			if rec.Code != c.want || h.reached != (c.want == http.StatusOK) {
				t.Errorf("status = %d (handler reached %t), want %d", rec.Code, h.reached, c.want)
			}
		})
	}
}
