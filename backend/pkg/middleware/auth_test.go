// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer   = "https://brain.futurebuild.test/"
	testAudience = "gable-erp"
	testKID      = "test-key-1"
)

// authTestKey is the signing key shared by the whole suite. Generating an RSA
// key is slow, so it is created once by newAuthFixture and reused.
type authTestFixture struct {
	priv    *rsa.PrivateKey
	jwksRaw json.RawMessage
}

var sharedFixture *authTestFixture

func newAuthFixture(t *testing.T) *authTestFixture {
	t.Helper()
	if sharedFixture != nil {
		return sharedFixture
	}
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	// A JWK Set holding only the PUBLIC half, exactly as an IdP would publish
	// it. Note: no "alg" member — plenty of real JWKS omit it (it is optional
	// in RFC 7517), and its absence is precisely when keyfunc's own algorithm
	// comparison goes quiet and our pin has to carry the weight.
	jwk := map[string]string{
		"kty": "RSA",
		"kid": testKID,
		"use": "sig",
		"n":   base64.RawURLEncoding.EncodeToString(priv.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes()),
	}
	set := map[string]any{"keys": []map[string]string{jwk}}
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal JWKS: %v", err)
	}
	sharedFixture = &authTestFixture{priv: priv, jwksRaw: raw}
	return sharedFixture
}

// publicKeyPEM returns the PEM encoding of the public half — i.e. bytes an
// attacker can fetch from the JWKS endpoint and would use as an HMAC secret
// in an algorithm-confusion attack.
func (f *authTestFixture) publicKeyPEM(t *testing.T) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&f.priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// newTestAuthMiddleware builds an AuthMiddleware against the in-memory JWKS.
// It bypasses NewAuthMiddleware only to avoid the network fetch; the parser —
// the thing under test — is built by the same production constructor.
func (f *authTestFixture) newTestAuthMiddleware(t *testing.T, issuer, audience string, algs []string) *AuthMiddleware {
	t.Helper()
	k, err := keyfunc.NewJWKSetJSON(f.jwksRaw)
	if err != nil {
		t.Fatalf("build keyfunc from JWKS: %v", err)
	}
	return &AuthMiddleware{
		jwks:     k,
		parser:   newTokenParser(issuer, audience, algs),
		issuer:   issuer,
		audience: audience,
		logger:   slog.New(slog.NewTextHandler(newDiscard(), &slog.HandlerOptions{Level: slog.LevelError + 1})),
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func newDiscard() discardWriter                   { return discardWriter{} }

// signRS256 mints a normal, well-formed token, then applies mutations.
func (f *authTestFixture) sign(t *testing.T, method jwt.SigningMethod, key any, claims jwt.Claims) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	tok.Header["kid"] = testKID
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign token (%s): %v", method.Alg(), err)
	}
	return s
}

// validClaims is the happy-path claim set: correct issuer, correct audience,
// and an expiry in the future.
func validClaims() *UserClaims {
	return &UserClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    testIssuer,
			Audience:  jwt.ClaimStrings{testAudience},
			Subject:   "user-123",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Email: "operator@example.test",
		Role:  "admin",
	}
}

// serve drives a request bearing token through the middleware and returns the
// status code plus whatever claims reached the protected handler.
func serve(m *AuthMiddleware, token string) (int, *UserClaims) {
	var got *UserClaims
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	m.Handler(next).ServeHTTP(rec, req)
	return rec.Code, got
}

// --- Positive control -------------------------------------------------------
//
// This must pass, or every 401 below proves nothing but a broken fixture.

func TestAuth_ValidTokenIsAccepted(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	token := f.sign(t, jwt.SigningMethodRS256, f.priv, validClaims())

	code, claims := serve(m, token)
	if code != http.StatusOK {
		t.Fatalf("valid token: got %d, want 200 (positive control failed — the refusal tests below are meaningless)", code)
	}
	if claims == nil {
		t.Fatal("valid token: no claims injected into context")
	}
	if claims.Subject != "user-123" || claims.Email != "operator@example.test" {
		t.Fatalf("valid token: claims not carried through: %+v", claims)
	}
}

// --- Attack 1: algorithm confusion against the JWKS public key -------------

func TestAuth_RejectsHS256SignedWithPublicKeyMaterial(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	// The attacker downloads the public key (it is published in the JWKS) and
	// uses its bytes as an HMAC secret, hoping the verifier picks the
	// algorithm from the attacker-controlled header.
	token := f.sign(t, jwt.SigningMethodHS256, f.publicKeyPEM(t), validClaims())

	if code, _ := serve(m, token); code != http.StatusUnauthorized {
		t.Fatalf("HS256-with-public-key token: got %d, want 401", code)
	}
}

func TestAuth_RejectsAlgNone(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	// jwt/v5 refuses to *sign* "none" without a magic constant, so build the
	// token by hand the way an attacker would: valid claims, alg none, empty
	// signature.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT","kid":"` + testKID + `"}`))
	body, err := json.Marshal(validClaims())
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	token := header + "." + payload + "."

	if code, _ := serve(m, token); code != http.StatusUnauthorized {
		t.Fatalf("alg=none token: got %d, want 401", code)
	}
}

// TestAuth_RejectsCrossAlgorithmSubstitution is the case that fails against
// the unpinned parser: PS256 and RS256 use the same RSA key, so without
// WithValidMethods the signature verifies and the token is accepted.
func TestAuth_RejectsCrossAlgorithmSubstitution(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	token := f.sign(t, jwt.SigningMethodPS256, f.priv, validClaims())

	if code, _ := serve(m, token); code != http.StatusUnauthorized {
		t.Fatalf("PS256 token under an RS256-only pin: got %d, want 401", code)
	}
}

// --- Attack 2: audience — a token minted for a sibling service -------------

func TestAuth_RejectsTokenForDifferentAudience(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	// Same IdP, same signing key, same issuer — a perfectly valid token, just
	// minted for a different service. This is the headline attack: without an
	// audience check it is indistinguishable from one of ours.
	claims := validClaims()
	claims.Audience = jwt.ClaimStrings{"futurebuild-takeoff"}

	token := f.sign(t, jwt.SigningMethodRS256, f.priv, claims)

	if code, _ := serve(m, token); code != http.StatusUnauthorized {
		t.Fatalf("sibling-service audience: got %d, want 401", code)
	}
}

func TestAuth_RejectsTokenWithNoAudienceClaim(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	// The "sibling IdP that omits aud" variant. An audience check that only
	// fires when the claim is present is not an audience check.
	claims := validClaims()
	claims.Audience = nil

	token := f.sign(t, jwt.SigningMethodRS256, f.priv, claims)

	if code, _ := serve(m, token); code != http.StatusUnauthorized {
		t.Fatalf("missing aud claim: got %d, want 401", code)
	}
}

func TestAuth_AcceptsAudienceAsStringOrArray(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	// RFC 7519 allows aud as a bare string or an array. Both must pass, so a
	// future claims refactor cannot quietly break real IdPs.
	t.Run("array containing the audience", func(t *testing.T) {
		claims := validClaims()
		claims.Audience = jwt.ClaimStrings{"some-other-service", testAudience}
		token := f.sign(t, jwt.SigningMethodRS256, f.priv, claims)
		if code, _ := serve(m, token); code != http.StatusOK {
			t.Fatalf("aud array: got %d, want 200", code)
		}
	})

	t.Run("bare string audience", func(t *testing.T) {
		// jwt.ClaimStrings always marshals as an array, so build the bare
		// string form by hand — it is what many real IdPs emit.
		token := f.sign(t, jwt.SigningMethodRS256, f.priv, jwt.MapClaims{
			"iss": testIssuer,
			"aud": testAudience,
			"sub": "user-123",
			"exp": time.Now().Add(time.Hour).Unix(),
		})

		// Confirm the wire form really is a JSON string, not an array — else
		// this test is a duplicate of the one above.
		payload := strings.Split(token, ".")[1]
		body, err := base64.RawURLEncoding.DecodeString(payload)
		if err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if !strings.Contains(string(body), `"aud":"`+testAudience+`"`) {
			t.Fatalf("expected a bare-string aud on the wire, got: %s", body)
		}

		if code, _ := serve(m, token); code != http.StatusOK {
			t.Fatalf("aud string: got %d, want 200", code)
		}
	})
}

// --- Attack 3: issuer -------------------------------------------------------

func TestAuth_RejectsWrongIssuer(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	claims := validClaims()
	claims.Issuer = "https://evil.example.test/"

	token := f.sign(t, jwt.SigningMethodRS256, f.priv, claims)

	if code, _ := serve(m, token); code != http.StatusUnauthorized {
		t.Fatalf("wrong issuer: got %d, want 401", code)
	}
}

func TestAuth_RejectsMissingIssuer(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	claims := validClaims()
	claims.Issuer = ""

	token := f.sign(t, jwt.SigningMethodRS256, f.priv, claims)

	if code, _ := serve(m, token); code != http.StatusUnauthorized {
		t.Fatalf("missing issuer: got %d, want 401", code)
	}
}

// --- Attack 4: expiry -------------------------------------------------------

func TestAuth_RejectsTokenWithNoExpiry(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	// exp is only validated when present unless expiration is required, so a
	// token minted without one would otherwise be valid forever.
	claims := validClaims()
	claims.ExpiresAt = nil

	token := f.sign(t, jwt.SigningMethodRS256, f.priv, claims)

	if code, _ := serve(m, token); code != http.StatusUnauthorized {
		t.Fatalf("token with no exp: got %d, want 401", code)
	}
}

func TestAuth_RejectsExpiredToken(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	claims := validClaims()
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-1 * time.Minute))

	token := f.sign(t, jwt.SigningMethodRS256, f.priv, claims)

	if code, _ := serve(m, token); code != http.StatusUnauthorized {
		t.Fatalf("expired token: got %d, want 401", code)
	}
}

// --- Signature + transport basics ------------------------------------------

func TestAuth_RejectsTokenSignedByAnotherKey(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate second key: %v", err)
	}
	token := f.sign(t, jwt.SigningMethodRS256, other, validClaims())

	if code, _ := serve(m, token); code != http.StatusUnauthorized {
		t.Fatalf("token signed by a foreign key: got %d, want 401", code)
	}
}

func TestAuth_RejectsMissingAndMalformedHeaders(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	cases := []struct{ name, header string }{
		{"no header", ""},
		{"no bearer prefix", "abc.def.ghi"},
		{"wrong scheme", "Basic abc.def.ghi"},
		{"bearer with no token", "Bearer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			m.Handler(next).ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s: got %d, want 401", tc.name, rec.Code)
			}
		})
	}
}

func TestAuth_PublicPathsBypassAuth(t *testing.T) {
	f := newAuthFixture(t)
	m := f.newTestAuthMiddleware(t, testIssuer, testAudience, []string{"RS256"})
	m.publicPaths = []string{"/health", "/api/portal/v1/"}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, path := range []string{"/health", "/api/portal/v1/login"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		m.Handler(next).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("public path %s: got %d, want 200", path, rec.Code)
		}
	}

	// An exact-match entry must not act as a prefix.
	req := httptest.NewRequest(http.MethodGet, "/healthz-not-public", nil)
	rec := httptest.NewRecorder()
	m.Handler(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/healthz-not-public: got %d, want 401", rec.Code)
	}
}

// --- Constructor refuses to build a weakened verifier -----------------------

func TestNewAuthMiddleware_RefusesWeakConfig(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(newDiscard(), nil))

	cases := []struct {
		name string
		cfg  AuthConfig
	}{
		{"no issuer", AuthConfig{JWKSURL: "http://127.0.0.1:1/jwks", Audience: testAudience, Algorithms: []string{"RS256"}}},
		{"no audience", AuthConfig{JWKSURL: "http://127.0.0.1:1/jwks", Issuer: testIssuer, Algorithms: []string{"RS256"}}},
		{"no algorithms", AuthConfig{JWKSURL: "http://127.0.0.1:1/jwks", Issuer: testIssuer, Audience: testAudience}},
		{"hmac algorithm", AuthConfig{JWKSURL: "http://127.0.0.1:1/jwks", Issuer: testIssuer, Audience: testAudience, Algorithms: []string{"HS256"}}},
		{"none algorithm", AuthConfig{JWKSURL: "http://127.0.0.1:1/jwks", Issuer: testIssuer, Audience: testAudience, Algorithms: []string{"none"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// These must fail on config validation, before any JWKS fetch is
			// attempted — the URL above is deliberately unreachable.
			if _, err := NewAuthMiddleware(t.Context(), tc.cfg, logger); err == nil {
				t.Fatalf("%s: NewAuthMiddleware returned no error; a weakened verifier must not be constructible", tc.name)
			}
		})
	}
}

// --- End-to-end through the production constructor --------------------------
//
// Every test above builds the middleware struct directly to skip the network.
// This one goes through NewAuthMiddleware against a real HTTP JWKS endpoint,
// so the wiring that production actually uses — keyfunc.NewDefaultCtx plus the
// parser built inside the constructor — is exercised at least once.
func TestNewAuthMiddleware_EndToEndAgainstLiveJWKS(t *testing.T) {
	f := newAuthFixture(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(f.jwksRaw)
	}))
	defer srv.Close()

	m, err := NewAuthMiddleware(t.Context(), AuthConfig{
		JWKSURL:    srv.URL,
		Issuer:     testIssuer,
		Audience:   testAudience,
		Algorithms: []string{"RS256"},
	}, slog.New(slog.NewTextHandler(newDiscard(), &slog.HandlerOptions{Level: slog.LevelError + 1})))
	if err != nil {
		t.Fatalf("NewAuthMiddleware: %v", err)
	}

	t.Run("valid token accepted", func(t *testing.T) {
		token := f.sign(t, jwt.SigningMethodRS256, f.priv, validClaims())
		if code, claims := serve(m, token); code != http.StatusOK || claims == nil {
			t.Fatalf("got %d (claims=%v), want 200 with claims", code, claims)
		}
	})

	t.Run("sibling audience refused", func(t *testing.T) {
		claims := validClaims()
		claims.Audience = jwt.ClaimStrings{"futurebuild-takeoff"}
		token := f.sign(t, jwt.SigningMethodRS256, f.priv, claims)
		if code, _ := serve(m, token); code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", code)
		}
	})

	t.Run("cross-algorithm substitution refused", func(t *testing.T) {
		token := f.sign(t, jwt.SigningMethodPS256, f.priv, validClaims())
		if code, _ := serve(m, token); code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", code)
		}
	})
}
