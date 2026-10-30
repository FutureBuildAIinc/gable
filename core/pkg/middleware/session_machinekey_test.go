// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/golang-jwt/jwt/v5"
)

type oneSession struct{}

func (oneSession) Resolve(ctx context.Context, id string) (*middleware.UserClaims, error) {
	if id != "good" {
		return nil, middleware.ErrSessionInvalid
	}
	return &middleware.UserClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: "sub-1"}, Roles: []string{"sales"}}, nil
}

func sessionsAndKeys(t *testing.T) (http.Handler, *okHandler) {
	t.Helper()
	h := &okHandler{}
	mka := middleware.NewMachineKeyAuth(stubValidator{principal: middleware.KeyPrincipal{ID: "key-1", Scopes: []string{"quotes:write"}}}, &stubAuditor{}, nil, slog.Default())
	am, err := middleware.NewAuthMiddleware(context.Background(), middleware.AuthConfig{Sessions: oneSession{}, MachineKeys: mka}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return am.Handler(h), h
}

// A machine key is not a cookie request: it is not asked for the CSRF header.
func TestMachineKeyIsNotAskedForTheCSRFHeader(t *testing.T) {
	h, ok := sessionsAndKeys(t)
	req := bearerRequest(t, http.MethodPost, "/api/v1/quotes", "sk_live_"+"teststubteststubteststub")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !ok.reached {
		t.Fatalf("keyed POST = %d, reached %v; want 200", w.Code, ok.reached)
	}
}

// A machine key beside a cookie that does not resolve is refused: the failed
// credential is never passed over for the key.
func TestMachineKeyBesideABadCookieIs401(t *testing.T) {
	h, ok := sessionsAndKeys(t)
	req := bearerRequest(t, http.MethodPost, "/api/v1/quotes", "sk_live_"+"teststubteststubteststub")
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "stale"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || ok.reached {
		t.Fatalf("status %d reached %v; want 401", w.Code, ok.reached)
	}
}

// A staff session carries no email, so it never passes the partner chain.
func TestStaffSessionNeverPassesPartnerAuth(t *testing.T) {
	partner := middleware.NewPartnerAuthMiddleware(nil, slog.Default())
	reached := false
	chain := partner.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	am, _ := middleware.NewAuthMiddleware(context.Background(), middleware.AuthConfig{Sessions: oneSession{}}, slog.Default())
	h := am.Handler(chain)
	req := httptest.NewRequest(http.MethodGet, "/api/partner/v1/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "good"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || reached {
		t.Fatalf("status %d reached %v; want 401", w.Code, reached)
	}
}
