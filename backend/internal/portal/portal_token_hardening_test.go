// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// loginRepo lets Login find exactly one user, so the positive control below is
// a token minted by the real Login path rather than one hand-assembled by the
// test.
type loginRepo struct {
	*fakePortalRepo
	user *CustomerUser
}

func (r loginRepo) GetCustomerUserByEmail(context.Context, string) (*CustomerUser, error) {
	return r.user, nil
}

const hardeningSecret = "portal-prod-secret-not-a-dev-default"

// newHardeningHarness returns the real portal handler mounted behind the real
// PortalAuthMiddleware — the same composition cmd/server installs in non-dev
// mode — plus the service, so a test can mint through the real Login.
func newHardeningHarness(t *testing.T) (*httptest.Server, *Service, uuid.UUID) {
	t.Helper()

	customerID, userID := uuid.New(), uuid.New()
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	repo := loginRepo{
		fakePortalRepo: newFakePortalRepo(),
		user: &CustomerUser{
			ID: userID, CustomerID: customerID,
			Email: "sam@kelbrook.ca", PasswordHash: string(hash),
			Name: "Sam Kelbrook", Role: "Admin", Status: "Active",
		},
	}
	repo.fakePortalRepo.orders = []PortalOrderDTO{{ID: uuid.New(), Status: "DRAFT", TotalAmount: 4210.75}}

	svc := NewService(repo, hardeningSecret, testLogger(), nil, nil, nil, nil, nil)
	mux := http.NewServeMux()
	NewHandler(svc).RegisterRoutes(mux, middleware.NewPortalAuthMiddleware([]byte(hardeningSecret), testLogger()).Handler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, svc, customerID
}

func fetchOrders(t *testing.T, srv *httptest.Server, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/portal/v1/orders", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// ACCEPTANCE II. A portal token with no exp, or a wrong/absent iss, or an
// absent aud, must be REFUSED by the real middleware — with a positive control
// that a token minted by the real Login still reaches real order data.
//
// The forged tokens here are signed with the CORRECT secret. That is the whole
// point: forging one needs PORTAL_JWT_SECRET, so this is not a no-secret
// bypass. What it proves is that a leaked or hand-minted token used to be
// immortal (no exp) and unattributable (any iss), and now is neither.
func TestPortalAuth_RefusesUnderConstrainedTokens(t *testing.T) {
	srv, svc, customerID := newHardeningHarness(t)
	now := time.Now()

	base := func() PortalClaims {
		return PortalClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   uuid.New().String(),
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
				Issuer:    middleware.PortalTokenIssuer,
				Audience:  jwt.ClaimStrings{middleware.PortalTokenAudience},
			},
			CustomerID:     customerID,
			CustomerUserID: uuid.New(),
			Email:          "mallory@example.invalid",
			Name:           "Mallory",
			Role:           "Admin",
		}
	}

	hostile := []struct {
		name   string
		mutate func(*PortalClaims)
	}{
		{"no exp — an immortal token", func(c *PortalClaims) { c.ExpiresAt = nil }},
		{"expired", func(c *PortalClaims) { c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute)) }},
		{"wrong issuer", func(c *PortalClaims) { c.Issuer = "totally-not-gable-portal" }},
		{"absent issuer", func(c *PortalClaims) { c.Issuer = "" }},
		{"wrong audience", func(c *PortalClaims) { c.Audience = jwt.ClaimStrings{"some-other-service"} }},
		{"absent audience", func(c *PortalClaims) { c.Audience = nil }},
		{"no exp AND wrong issuer", func(c *PortalClaims) {
			c.ExpiresAt = nil
			c.Issuer = "totally-not-gable-portal"
		}},
	}

	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			claims := base()
			tc.mutate(&claims)
			token := signHS256(t, hardeningSecret, claims)

			if got := fetchOrders(t, srv, token); got != http.StatusUnauthorized {
				t.Errorf("GET /orders with a %s token returned %d, want 401", tc.name, got)
			}
			// The service-side parse is the same token class through a second
			// door; it must refuse identically.
			if _, err := svc.ParseToken(token); err == nil {
				t.Errorf("ParseToken accepted a %s token", tc.name)
			}
		})
	}

	t.Run("no token", func(t *testing.T) {
		if got := fetchOrders(t, srv, ""); got != http.StatusUnauthorized {
			t.Errorf("GET /orders with no token returned %d, want 401", got)
		}
	})
	t.Run("garbage token", func(t *testing.T) {
		if got := fetchOrders(t, srv, "not.a.jwt"); got != http.StatusUnauthorized {
			t.Errorf("GET /orders with a garbage token returned %d, want 401", got)
		}
	})

	// POSITIVE CONTROL: the real Login mints a token the real middleware
	// accepts. Without this the test above would pass just as well if the
	// middleware rejected everything.
	t.Run("positive control — real Login token", func(t *testing.T) {
		res, err := svc.Login(context.Background(), LoginRequest{Email: "sam@kelbrook.ca", Password: "correct-horse"})
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		if got := fetchOrders(t, srv, res.Token); got != http.StatusOK {
			t.Fatalf("a freshly minted login token returned %d, want 200", got)
		}
		claims, err := svc.ParseToken(res.Token)
		if err != nil {
			t.Fatalf("ParseToken rejected a freshly minted login token: %v", err)
		}
		if claims.CustomerID != customerID {
			t.Errorf("CustomerID = %s, want %s", claims.CustomerID, customerID)
		}
	})
}

// The minting side must set every claim the verifier requires. If Login ever
// stops setting aud or iss, every portal user is locked out at the next deploy
// — a failure worth catching here rather than in production.
func TestLogin_MintsExpIssuerAndAudience(t *testing.T) {
	_, svc, _ := newHardeningHarness(t)

	res, err := svc.Login(context.Background(), LoginRequest{Email: "sam@kelbrook.ca", Password: "correct-horse"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// Parse with NO validation so the raw minted claims are visible.
	var claims PortalClaims
	if _, _, err := jwt.NewParser().ParseUnverified(res.Token, &claims); err != nil {
		t.Fatalf("ParseUnverified: %v", err)
	}
	if claims.ExpiresAt == nil {
		t.Error("Login minted a token with no exp — it would never expire")
	}
	if claims.Issuer != middleware.PortalTokenIssuer {
		t.Errorf("iss = %q, want %q", claims.Issuer, middleware.PortalTokenIssuer)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != middleware.PortalTokenAudience {
		t.Errorf("aud = %v, want [%s]", claims.Audience, middleware.PortalTokenAudience)
	}
}

// The portal secret is symmetric. Pinning HS256 must not be mistaken for a
// reason to accept an asymmetric header — the classic algorithm-confusion
// substitution stays refused.
func TestPortalAuth_RejectsAlgorithmSubstitution(t *testing.T) {
	srv, _, customerID := newHardeningHarness(t)
	now := time.Now()

	signed := signHS256(t, hardeningSecret, PortalClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uuid.New().String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			Issuer:    middleware.PortalTokenIssuer,
			Audience:  jwt.ClaimStrings{middleware.PortalTokenAudience},
		},
		CustomerID: customerID,
	})
	parts := splitJWT(t, signed)

	for name, header := range map[string]string{
		// {"alg":"RS256","typ":"JWT"} and {"alg":"none","typ":"JWT"}, base64url.
		"RS256 header over an HMAC signature": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9",
		"alg=none":                            "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0",
	} {
		if got := fetchOrders(t, srv, header+"."+parts[1]+"."+parts[2]); got != http.StatusUnauthorized {
			t.Errorf("%s returned %d, want 401", name, got)
		}
	}
}

func splitJWT(t *testing.T, tok string) []string {
	t.Helper()
	parts := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(tok); i++ {
		if tok[i] == '.' {
			parts = append(parts, tok[start:i])
			start = i + 1
		}
	}
	parts = append(parts, tok[start:])
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", tok)
	}
	return parts
}

// signWith mints a token with an explicit HMAC signing method, so a test can
// present HS384/HS512 where HS256 is expected.
func signWith(t *testing.T, method *jwt.SigningMethodHMAC, secret string, claims PortalClaims) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(method, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign %s: %v", method.Alg(), err)
	}
	return tok
}

// TestPortalAuth_PinsHS256 covers what TestPortalAuth_RejectsAlgorithmSubstitution
// cannot.
//
// That test's RS256 and alg=none cases are refused by the keyfunc's
// `t.Method.(*jwt.SigningMethodHMAC)` assertion, so they stay refused with
// WithValidMethods deleted — dropping the HS256 pin from NewPortalTokenParser
// left the entire suite green. HS384 and HS512 ARE SigningMethodHMAC, so the
// pin is the only thing that refuses them.
//
// d1c9c20 pinned WithValidMethods for the staff JWKS path (see
// pkg/middleware/algorithms_test.go). This is the portal path's equivalent: one
// declared algorithm, verified as declared, on the transport that actually
// carries the secret.
func TestPortalAuth_PinsHS256(t *testing.T) {
	srv, svc, customerID := newHardeningHarness(t)
	now := time.Now()

	claims := PortalClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uuid.New().String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			Issuer:    middleware.PortalTokenIssuer,
			Audience:  jwt.ClaimStrings{middleware.PortalTokenAudience},
		},
		CustomerID:     customerID,
		CustomerUserID: uuid.New(),
		Email:          "mallory@example.invalid",
		Role:           "Admin",
	}

	for _, method := range []*jwt.SigningMethodHMAC{jwt.SigningMethodHS384, jwt.SigningMethodHS512} {
		t.Run(method.Alg(), func(t *testing.T) {
			// Signed with the CORRECT secret and every claim in order. Only the
			// algorithm differs from what the portal declares.
			token := signWith(t, method, hardeningSecret, claims)

			if got := fetchOrders(t, srv, token); got != http.StatusUnauthorized {
				t.Errorf("GET /orders with an %s token returned %d, want 401 — the parser must pin HS256", method.Alg(), got)
			}
			if _, err := svc.ParseToken(token); err == nil {
				t.Errorf("ParseToken accepted an %s token", method.Alg())
			}
		})
	}

	// Positive control: the same claims under HS256 are accepted, so the four
	// rejections above are the algorithm pin and not a broken harness.
	if got := fetchOrders(t, srv, signWith(t, jwt.SigningMethodHS256, hardeningSecret, claims)); got != http.StatusOK {
		t.Fatalf("positive control: an HS256 token with the same claims returned %d, want 200", got)
	}
}

// A token can be perfectly signed, unexpired, correctly issued and correctly
// addressed, and still name no customer. The middleware's
// `claims.CustomerID == uuid.Nil` check is what stops that request; deleting it
// survived the whole suite.
//
// A nil customer_id is not harmless: every portal handler scopes its query by
// the customer in the claims, so admitting a zero UUID sends a tenancy filter
// of all-zeroes into the data layer instead of refusing the request at the
// door. Fail closed at the boundary.
func TestPortalAuth_RefusesTokenWithNoCustomerID(t *testing.T) {
	srv, _, customerID := newHardeningHarness(t)
	now := time.Now()

	base := PortalClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uuid.New().String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			Issuer:    middleware.PortalTokenIssuer,
			Audience:  jwt.ClaimStrings{middleware.PortalTokenAudience},
		},
		CustomerUserID: uuid.New(),
		Email:          "nobody@example.invalid",
		Role:           "Admin",
	}

	// customer_id absent from the JSON entirely, and explicitly the zero UUID:
	// both decode to uuid.Nil, and both must be refused.
	nilClaims := base
	nilClaims.CustomerID = uuid.Nil
	if got := fetchOrders(t, srv, signHS256(t, hardeningSecret, nilClaims)); got != http.StatusUnauthorized {
		t.Errorf("GET /orders with a nil customer_id returned %d, want 401", got)
	}

	// Positive control: the identical token with a real customer_id is admitted,
	// so the rejection above is the claim check and not something else.
	ok := base
	ok.CustomerID = customerID
	if got := fetchOrders(t, srv, signHS256(t, hardeningSecret, ok)); got != http.StatusOK {
		t.Fatalf("positive control: the same token with a real customer_id returned %d, want 200", got)
	}
}
