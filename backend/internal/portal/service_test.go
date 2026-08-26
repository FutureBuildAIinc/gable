// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// The B2B portal is the only externally-authenticated surface in the ERP, and
// it is the float64-dollars side of the dollars/cents boundary described in
// CLAUDE.md. Two things are pinned here:
//
//  1. Token handling — ParseToken is the gate on every protected portal route.
//  2. Wire format — portal DTOs carry float64 DOLLARS, and the password hash on
//     CustomerUser must never reach a client.
//
// portal.Service takes the Repository interface declared in repository.go, so
// the data-access methods are unit-testable against a fake store; that suite
// lives in service_repo_test.go, and the DB-backed proof that the SQL really
// filters on customer_id lives in tenancy_pg_test.go. This file covers token
// handling, the wire format and the HTTP layer.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

const testSecret = "test-portal-secret-value"

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTokenService(secret string) *Service {
	return NewService(nil, secret, testLogger(), nil, nil, nil, nil, nil)
}

// signHS256 mints a token the way Service.Login does, so ParseToken is being
// tested against the real format rather than a test-only shape.
func signHS256(t *testing.T, secret string, claims PortalClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	return s
}

func validClaims(customerID, userID uuid.UUID) PortalClaims {
	now := time.Now()
	return PortalClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
			Issuer:    "gable-portal",
		},
		CustomerID:     customerID,
		CustomerUserID: userID,
		Email:          "buyer@acme.example",
		Name:           "Jo Buyer",
		Role:           "Buyer",
	}
}

// --- ParseToken: the happy path -----------------------------------------

// CORRECTNESS: a token minted by this service must round-trip with every claim
// intact. CustomerID in particular is the multi-tenant scope for every portal
// query; losing or corrupting it would either lock a contractor out or show
// them someone else's data.
func TestParseToken_RoundTripsAllClaims(t *testing.T) {
	svc := newTokenService(testSecret)
	custID, userID := uuid.New(), uuid.New()
	want := validClaims(custID, userID)

	got, err := svc.ParseToken(signHS256(t, testSecret, want))
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if got.CustomerID != custID {
		t.Errorf("CustomerID = %s, want %s", got.CustomerID, custID)
	}
	if got.CustomerUserID != userID {
		t.Errorf("CustomerUserID = %s, want %s", got.CustomerUserID, userID)
	}
	if got.Subject != userID.String() {
		t.Errorf("Subject = %q, want %q", got.Subject, userID.String())
	}
	if got.Email != want.Email || got.Name != want.Name || got.Role != want.Role {
		t.Errorf("identity claims changed: %+v", got)
	}
	if got.Issuer != "gable-portal" {
		t.Errorf("Issuer = %q, want gable-portal", got.Issuer)
	}
}

// --- ParseToken: rejection ----------------------------------------------

// CORRECTNESS (security): every one of these must be refused. Each row is a
// real attack or a real failure mode, not a syntax variation.
func TestParseToken_Rejections(t *testing.T) {
	svc := newTokenService(testSecret)
	custID, userID := uuid.New(), uuid.New()

	// A token signed with a different secret: the classic "attacker mints
	// their own" case.
	forged := signHS256(t, "a-completely-different-secret", validClaims(custID, userID))

	// An expired token.
	expiredClaims := validClaims(custID, userID)
	expiredClaims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	expiredClaims.IssuedAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
	expired := signHS256(t, testSecret, expiredClaims)

	// A token that is not valid yet.
	futureClaims := validClaims(custID, userID)
	futureClaims.NotBefore = jwt.NewNumericDate(time.Now().Add(time.Hour))
	notYet := signHS256(t, testSecret, futureClaims)

	// alg=none: the signature-stripping attack.
	noneTok := jwt.NewWithClaims(jwt.SigningMethodNone, validClaims(custID, userID))
	noneStr, err := noneTok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("minting an alg=none token: %v", err)
	}

	// A valid token with its signature truncated.
	good := signHS256(t, testSecret, validClaims(custID, userID))
	tampered := good[:len(good)-4] + "AAAA"

	// A valid token with a mutated payload (signature no longer matches).
	parts := strings.Split(good, ".")
	swapped := parts[0] + "." + parts[1][:len(parts[1])-2] + "AA." + parts[2]

	tests := []struct {
		name  string
		token string
	}{
		{"empty string", ""},
		{"not a token at all", "hello"},
		{"only two segments", "a.b"},
		{"four segments", "a.b.c.d"},
		{"signed with the wrong secret", forged},
		{"expired", expired},
		{"not valid yet", notYet},
		{"alg=none", noneStr},
		{"truncated signature", tampered},
		{"mutated payload", swapped},
		{"base64 garbage", "eyJ.eyJ.eyJ"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := svc.ParseToken(tc.token)
			if err == nil {
				t.Fatalf("token was accepted, returning claims %+v", claims)
			}
			if claims != nil {
				t.Errorf("claims %+v were returned alongside the error, want nil", claims)
			}
		})
	}
}

// CORRECTNESS (security): the keyfunc must reject any non-HMAC algorithm, not
// just alg=none. An RSA-signed token whose "key" is the HMAC secret is the
// algorithm-confusion attack; the signing-method check is what stops it.
func TestParseToken_RejectsNonHMACAlgorithms(t *testing.T) {
	svc := newTokenService(testSecret)

	// Hand-craft a header claiming RS256 over a payload the service would
	// otherwise like, signed with the HMAC secret.
	claims := validClaims(uuid.New(), uuid.New())
	hmacSigned := signHS256(t, testSecret, claims)
	parts := strings.Split(hmacSigned, ".")

	// {"alg":"RS256","typ":"JWT"} base64url, no padding.
	rsHeader := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9"
	confused := rsHeader + "." + parts[1] + "." + parts[2]

	if got, err := svc.ParseToken(confused); err == nil {
		t.Fatalf("an RS256-header token was accepted with the HMAC secret: %+v", got)
	}
}

// CORRECTNESS (security): tokens do not cross tenants. A token minted for one
// deployment's secret must not validate against another's — this is what keeps
// the staging and production portals separate.
func TestParseToken_SecretIsolation(t *testing.T) {
	a := newTokenService("secret-A")
	b := newTokenService("secret-B")
	claims := validClaims(uuid.New(), uuid.New())

	tok := signHS256(t, "secret-A", claims)
	if _, err := a.ParseToken(tok); err != nil {
		t.Fatalf("service A rejected its own token: %v", err)
	}
	if got, err := b.ParseToken(tok); err == nil {
		t.Fatalf("service B accepted service A's token: %+v", got)
	}
}

// CHARACTERIZATION: NewService accepts an empty jwtSecret. Its own doc comment
// says callers "should fail startup if not configured", but the constructor
// does not enforce it — so a misconfigured deployment produces a service that
// happily mints and validates tokens signed with the empty key, which any
// attacker can reproduce. The guard lives in cmd/server/main.go, out of this
// package's reach.
func TestNewService_DoesNotRejectAnEmptySecret(t *testing.T) {
	svc := newTokenService("")
	claims := validClaims(uuid.New(), uuid.New())

	tok := signHS256(t, "", claims)
	if _, err := svc.ParseToken(tok); err != nil {
		t.Fatalf("a service built with an empty secret rejected an empty-key token (%v); if the constructor now validates the secret, this characterization test should become a construction-failure test", err)
	}
}

// --- user management validation -----------------------------------------

// CORRECTNESS: the portal role vocabulary is closed. An unrecognised role must
// be refused before it reaches the database, because the handler's admin gate
// compares against the literal string "Admin" — a stored typo silently
// downgrades a user, and a stored "admin" is not an admin.
func TestInviteUser_RoleWhitelist(t *testing.T) {
	svc := newTokenService(testSecret) // repo is nil: valid roles must not be reached
	ctx := context.Background()
	custID := uuid.New()

	rejected := []string{
		"admin", "ADMIN", "Owner", "buyer", "Superuser", "",
		"Admin ", " Admin", "Admin,Buyer", "View Only",
	}
	for _, role := range rejected {
		t.Run("rejects "+role, func(t *testing.T) {
			if _, err := svc.InviteUser(ctx, custID, InviteUserRequest{Email: "a@b.c", Role: role}); err == nil {
				t.Fatalf("role %q was accepted", role)
			}
		})
	}
}

// CORRECTNESS: the three valid roles must pass validation. With a nil
// repository the call panics once it reaches persistence, which is exactly the
// signal that validation let it through — so the test recovers and asserts that
// it got that far.
func TestInviteUser_ValidRolesPassValidation(t *testing.T) {
	for _, role := range []string{"Admin", "Buyer", "View-Only"} {
		t.Run(role, func(t *testing.T) {
			if !reachedPersistence(func() {
				svc := newTokenService(testSecret)
				_, _ = svc.InviteUser(context.Background(), uuid.New(), InviteUserRequest{Email: "a@b.c", Role: role})
			}) {
				t.Fatalf("role %q did not get past validation", role)
			}
		})
	}
}

// CORRECTNESS: the same closed vocabulary governs a role change on an existing
// user, and the status vocabulary is closed too.
func TestUpdateUserRoleAndStatus_Whitelists(t *testing.T) {
	svc := newTokenService(testSecret)
	ctx := context.Background()

	for _, role := range []string{"admin", "Root", "", "Admin;--"} {
		if err := svc.UpdateUserRole(ctx, uuid.New(), uuid.New(), role); err == nil {
			t.Errorf("UpdateUserRole accepted %q", role)
		}
	}
	for _, status := range []string{"active", "INACTIVE", "Deleted", "", "Suspended"} {
		if err := svc.UpdateUserStatus(ctx, uuid.New(), uuid.New(), status); err == nil {
			t.Errorf("UpdateUserStatus accepted %q", status)
		}
	}

	for _, role := range []string{"Admin", "Buyer", "View-Only"} {
		if !reachedPersistence(func() {
			_ = svc.UpdateUserRole(ctx, uuid.New(), uuid.New(), role)
		}) {
			t.Errorf("UpdateUserRole rejected the valid role %q", role)
		}
	}
	for _, status := range []string{"Active", "Inactive"} {
		if !reachedPersistence(func() {
			_ = svc.UpdateUserStatus(ctx, uuid.New(), uuid.New(), status)
		}) {
			t.Errorf("UpdateUserStatus rejected the valid status %q", status)
		}
	}
}

// reachedPersistence runs fn and reports whether it got as far as touching the
// nil repository (which panics). A validation rejection returns cleanly
// instead, so a false result means the input was refused.
func reachedPersistence(fn func()) (reached bool) {
	defer func() {
		if recover() != nil {
			reached = true
		}
	}()
	fn()
	return false
}

// --- wire format: money is dollars --------------------------------------

// CORRECTNESS (contract): portal DTOs carry float64 DOLLARS. The ERP side of
// the same data is int64 cents (order/invoice), so this boundary is exactly
// where a $73.88 invoice would render as $7,388.00 if the two were confused.
// Every money field in the portal's customer-facing DTOs is pinned here.
func TestPortalDTOs_MoneyIsDollarsNotCents(t *testing.T) {
	fields := func(t *testing.T, v any) map[string]json.RawMessage {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return raw
	}

	t.Run("dashboard", func(t *testing.T) {
		raw := fields(t, PortalDashboardDTO{BalanceDue: 4873.19, CreditLimit: 25000, PastDue: 73.88})
		for field, want := range map[string]string{
			"balance_due":  "4873.19",
			"credit_limit": "25000",
			"past_due":     "73.88",
		} {
			if got := string(raw[field]); got != want {
				t.Errorf("%s = %s, want %s dollars", field, got, want)
			}
		}
	})

	t.Run("invoice", func(t *testing.T) {
		raw := fields(t, PortalInvoiceDTO{TotalAmount: 108.25, Subtotal: 100, TaxAmount: 8.25})
		for field, want := range map[string]string{
			"total_amount": "108.25",
			"subtotal":     "100",
			"tax_amount":   "8.25",
		} {
			if got := string(raw[field]); got != want {
				t.Errorf("%s = %s, want %s dollars", field, got, want)
			}
		}
	})

	t.Run("order and line", func(t *testing.T) {
		raw := fields(t, PortalOrderDTO{TotalAmount: 4200.5})
		if got := string(raw["total_amount"]); got != "4200.5" {
			t.Errorf("total_amount = %s, want 4200.5 dollars", got)
		}
		lineRaw := fields(t, PortalLineDTO{Quantity: 2.5, PriceEach: 4.75})
		if got := string(lineRaw["price_each"]); got != "4.75" {
			t.Errorf("price_each = %s, want 4.75 dollars", got)
		}
		if got := string(lineRaw["quantity"]); got != "2.5" {
			t.Errorf("quantity = %s, want a fractional quantity to survive", got)
		}
	})

	t.Run("cart", func(t *testing.T) {
		raw := fields(t, CartDTO{Subtotal: 1234.56})
		if got := string(raw["subtotal"]); got != "1234.56" {
			t.Errorf("subtotal = %s, want 1234.56 dollars", got)
		}
		itemRaw := fields(t, CartItemDTO{UnitPrice: 4.75, LineTotal: 11.875, Quantity: 2.5})
		if got := string(itemRaw["unit_price"]); got != "4.75" {
			t.Errorf("unit_price = %s, want dollars", got)
		}
		if got := string(itemRaw["line_total"]); got != "11.875" {
			t.Errorf("line_total = %s, want the exact extended value", got)
		}
	})

	t.Run("catalog", func(t *testing.T) {
		raw := fields(t, CatalogProductDTO{BasePrice: 5.49, CustomerPrice: 4.75})
		if got := string(raw["base_price"]); got != "5.49" {
			t.Errorf("base_price = %s, want dollars", got)
		}
		if got := string(raw["customer_price"]); got != "4.75" {
			t.Errorf("customer_price = %s, want dollars", got)
		}
	})
}

// CORRECTNESS: checkout converts the cart's float64 dollars into the order
// module's int64 cents with math.Round. This is the exact expression used at
// cart.go:110; the test pins the rounding rule it has to satisfy, including the
// binary-float traps that a truncating conversion loses a cent on.
func TestCartDollarsToOrderCents_RoundingRule(t *testing.T) {
	tests := []struct {
		dollars float64
		want    int64
	}{
		{0, 0},
		{0.01, 1},
		{4.75, 475},
		{8.20, 820},   // 8.20*100 == 819.9999999999999 in binary
		{0.29, 29},    // 0.29*100 == 28.999999999999996
		{1.15, 115},   // 1.15*100 == 114.99999999999999
		{10.99, 1099}, //
		{1234.56, 123456},
		{-4.75, -475}, // a credit line must round symmetrically
		{-8.20, -820},
	}

	for _, tc := range tests {
		got := int64(math.Round(tc.dollars * 100))
		if got != tc.want {
			t.Errorf("int64(math.Round(%v*100)) = %d cents, want %d", tc.dollars, got, tc.want)
		}
	}
}

// --- security: the password hash must never be serialised ---------------

// CORRECTNESS (security): CustomerUser is embedded in LoginResponse, which is
// written straight to the client on every successful login, and it is also
// returned by the user-list endpoint. The bcrypt hash must be excluded by the
// `json:"-"` tag; losing that tag would ship every portal user's hash to the
// browser.
func TestCustomerUser_PasswordHashIsNeverSerialised(t *testing.T) {
	const hash = "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123"

	user := CustomerUser{
		ID:           uuid.New(),
		CustomerID:   uuid.New(),
		Email:        "buyer@acme.example",
		PasswordHash: hash,
		Name:         "Jo Buyer",
		Role:         "Buyer",
		Status:       "Active",
	}

	for _, tc := range []struct {
		name string
		v    any
	}{
		{"CustomerUser alone", user},
		{"inside a LoginResponse", LoginResponse{User: user, Config: PortalConfig{DealerName: "Gable"}}},
		{"inside a slice, as the user-list endpoint returns", []CustomerUser{user}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.Contains(string(b), hash) {
				t.Fatalf("the bcrypt hash was serialised to the client: %s", b)
			}
			if strings.Contains(strings.ToLower(string(b)), "password") {
				t.Fatalf("a password-shaped field name appears in the payload: %s", b)
			}
		})
	}
}

// CORRECTNESS (security): the invite token is a bearer credential. It must not
// appear in the JSON the API returns from the invite-list endpoint.
func TestPortalInvite_TokenIsNeverSerialised(t *testing.T) {
	const token = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	b, err := json.Marshal(PortalInvite{
		ID: uuid.New(), CustomerID: uuid.New(), Email: "new@acme.example",
		Role: "Buyer", Token: token, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), token) {
		t.Fatalf("the invite token was serialised: %s", b)
	}
}

// --- cookie handling -----------------------------------------------------

// CORRECTNESS (security): logout must actively clear the cookie, and the
// cleared cookie must keep the same protective attributes — a Logout that
// dropped HttpOnly or SameSite would leave a window in which the (now empty)
// cookie name is scriptable.
func TestHandleLogout_ClearsTheCookieSecurely(t *testing.T) {
	t.Setenv("INSECURE_COOKIES", "")

	h := NewHandler(newTokenService(testSecret))
	rec := httptest.NewRecorder()
	h.HandleLogout(rec, httptest.NewRequest(http.MethodPost, "/api/portal/v1/logout", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("set %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != "portal_token" {
		t.Errorf("cookie name = %q, want portal_token", c.Name)
	}
	if c.Value != "" {
		t.Errorf("cookie value = %q, want empty", c.Value)
	}
	if c.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want a negative value so the browser deletes it", c.MaxAge)
	}
	if !c.HttpOnly {
		t.Error("HttpOnly is not set")
	}
	if !c.Secure {
		t.Error("Secure is not set by default")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}
	if c.Path != "/api/portal" {
		t.Errorf("Path = %q, want /api/portal so the right cookie is cleared", c.Path)
	}
}

// CORRECTNESS: the Secure flag is on unless a developer explicitly opts out
// with INSECURE_COOKIES=true. Any other value — including "1", "yes", or
// "TRUE" — must leave it on, so a typo cannot silently disable it.
func TestPortalCookie_SecureIsOptOutOnlyForTheExactValue(t *testing.T) {
	tests := []struct {
		env        string
		wantSecure bool
	}{
		{"", true},
		{"true", false},
		{"TRUE", true},
		{"True", true},
		{"1", true},
		{"yes", true},
		{"false", true},
	}

	for _, tc := range tests {
		t.Run("INSECURE_COOKIES="+tc.env, func(t *testing.T) {
			t.Setenv("INSECURE_COOKIES", tc.env)
			rec := httptest.NewRecorder()
			NewHandler(newTokenService(testSecret)).
				HandleLogout(rec, httptest.NewRequest(http.MethodPost, "/api/portal/v1/logout", nil))

			c := rec.Result().Cookies()[0]
			if c.Secure != tc.wantSecure {
				t.Errorf("Secure = %v, want %v", c.Secure, tc.wantSecure)
			}
		})
	}
}

// --- login handler validation -------------------------------------------

// CORRECTNESS: the login endpoint must reject an empty email or password
// before touching the database, and must not set a cookie on failure.
func TestHandleLogin_RejectsEmptyCredentialsWithoutTouchingTheRepository(t *testing.T) {
	h := NewHandler(newTokenService(testSecret)) // nil repo: reaching it would panic

	cases := []string{
		`{"email":"","password":"x"}`,
		`{"email":"a@b.c","password":""}`,
		`{}`,
	}
	for _, body := range cases {
		rec := httptest.NewRecorder()
		h.HandleLogin(rec, httptest.NewRequest(http.MethodPost, "/api/portal/v1/login", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s => status %d, want 400", body, rec.Code)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Errorf("body %s set a cookie on a rejected login", body)
		}
	}
}

func TestHandleLogin_MalformedBodyIs400(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHandler(newTokenService(testSecret)).
		HandleLogin(rec, httptest.NewRequest(http.MethodPost, "/api/portal/v1/login", strings.NewReader("{not json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// --- route registration --------------------------------------------------

// CORRECTNESS (security): login, logout and config are the only public portal
// routes. Everything else must go through the auth middleware — a protected
// route registered without it would expose one contractor's data to anyone.
func TestRegisterRoutes_OnlyThreeRoutesArePublic(t *testing.T) {
	var wrapped []string
	authMw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wrapped = append(wrapped, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
		})
	}

	mux := http.NewServeMux()
	NewHandler(newTokenService(testSecret)).RegisterRoutes(mux, authMw)

	id := uuid.NewString()
	protected := []struct{ method, path string }{
		{http.MethodGet, "/api/portal/v1/dashboard"},
		{http.MethodGet, "/api/portal/v1/orders"},
		{http.MethodGet, "/api/portal/v1/orders/" + id},
		{http.MethodPost, "/api/portal/v1/orders/reorder"},
		{http.MethodGet, "/api/portal/v1/invoices"},
		{http.MethodGet, "/api/portal/v1/invoices/" + id},
		{http.MethodGet, "/api/portal/v1/deliveries"},
		{http.MethodGet, "/api/portal/v1/deliveries/" + id},
		{http.MethodGet, "/api/portal/v1/catalog"},
		{http.MethodGet, "/api/portal/v1/catalog/" + id},
		// Capabilities added on top of migration 084. Every one of them reads
		// or writes customer-scoped data, so every one of them must be behind
		// the auth middleware — including the category tree, which is not
		// customer-scoped but must not be a way to enumerate a dealer's
		// merchandising from outside.
		{http.MethodPost, "/api/portal/v1/orders/" + id + "/cancel"},
		{http.MethodPut, "/api/portal/v1/orders/" + id + "/project"},
		{http.MethodPost, "/api/portal/v1/deliveries/" + id + "/reschedule"},
		{http.MethodGet, "/api/portal/v1/deliveries/" + id + "/reschedule"},
		{http.MethodGet, "/api/portal/v1/catalog/categories"},
		{http.MethodGet, "/api/portal/v1/catalog/" + id + "/volume-breaks"},
		{http.MethodGet, "/api/portal/v1/quotes"},
		{http.MethodPost, "/api/portal/v1/quotes"},
		{http.MethodGet, "/api/portal/v1/quotes/" + id},
		{http.MethodPost, "/api/portal/v1/quotes/" + id + "/accept"},
		{http.MethodPost, "/api/portal/v1/quotes/" + id + "/decline"},
		{http.MethodGet, "/api/portal/v1/cart"},
		{http.MethodPost, "/api/portal/v1/cart/items"},
		{http.MethodPut, "/api/portal/v1/cart/items/" + id},
		{http.MethodDelete, "/api/portal/v1/cart/items/" + id},
		{http.MethodPost, "/api/portal/v1/checkout"},
		{http.MethodGet, "/api/portal/v1/users"},
		{http.MethodGet, "/api/portal/v1/invites"},
		{http.MethodPost, "/api/portal/v1/invites"},
		{http.MethodPut, "/api/portal/v1/users/" + id + "/role"},
		{http.MethodPut, "/api/portal/v1/users/" + id + "/status"},
	}

	for _, r := range protected {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(r.method, r.path, strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401: this route is not behind the auth middleware", r.method, r.path, rec.Code)
		}
	}
	if len(wrapped) != len(protected) {
		t.Errorf("auth middleware ran %d times, want %d", len(wrapped), len(protected))
	}

	// Logout is public and must work with no auth at all.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/portal/v1/logout", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("POST /logout = %d, want 200 without auth", rec.Code)
	}
}

// CORRECTNESS: an optional login rate limiter must actually be applied to the
// login route when supplied — that limiter is the brute-force defence on the
// portal's only credential-checking endpoint.
func TestRegisterRoutes_LoginLimiterIsApplied(t *testing.T) {
	var limiterHits int
	limiter := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			limiterHits++
			w.WriteHeader(http.StatusTooManyRequests)
		})
	}
	passthrough := func(next http.Handler) http.Handler { return next }

	mux := http.NewServeMux()
	NewHandler(newTokenService(testSecret)).RegisterRoutes(mux, passthrough, limiter)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/portal/v1/login", strings.NewReader(`{"email":"a@b.c","password":"x"}`)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: the login limiter was not applied", rec.Code)
	}
	if limiterHits != 1 {
		t.Errorf("limiter ran %d times, want 1", limiterHits)
	}
}

// TestLogin_RejectsAnUnknownEmailWithoutSayingSo is the one Login path that is
// worth pinning here rather than in service_repo_test.go: the error text.
//
// CORRECTNESS (security): a login failure must be indistinguishable between
// "no such user" and "wrong password", or the endpoint enumerates which
// contractors have portal accounts.
func TestLogin_FailuresAreIndistinguishable(t *testing.T) {
	rig := newPortalRig(t)

	_, err := rig.svc.Login(context.Background(), LoginRequest{Email: "nobody@acme.example", Password: "hunter2"})
	if err == nil {
		t.Fatal("an unknown user logged in")
	}
	if err.Error() != "invalid credentials" {
		t.Errorf("error = %q, want the generic \"invalid credentials\"", err)
	}
	if strings.Contains(err.Error(), "nobody@acme.example") {
		t.Errorf("the submitted email was reflected into the error: %q", err)
	}
}
