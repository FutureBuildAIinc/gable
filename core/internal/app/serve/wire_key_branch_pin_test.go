// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The branch bound machine key's pin (ADR 0007 section 5.5) through serve's
// real mount methods and the real machine key core: one key minted bound to
// branch A, one unbound key, and a user, against every route the sweep found
// writing or reading another branch's rows without the branch middleware.
// Only authentication is a stand-in: the machine key core is the real one
// over a real minted key, and the user arrives as the claims a JWT would
// carry. A route that loses its hold on the pin fails here.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ap"
	"github.com/gablelbm/gable/internal/app/orderwire"
	"github.com/gablelbm/gable/internal/bankrecon"
	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/events"
	"github.com/gablelbm/gable/internal/gl"
	glint "github.com/gablelbm/gable/internal/integrations/gl"
	"github.com/gablelbm/gable/internal/location"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/internal/salesteam"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// keyPinFixture is two branches, one yard per branch for the location rows,
// two spare branches for the archive arms, a quote per branch for the
// exposure routes, and the serve mounts the pin must hold on: the location
// routes through branchWall.locations and the exposure routes through
// registerExposureRoutes, both behind the real machine key core.
type keyPinFixture struct {
	t         *testing.T
	db        *database.DB
	srv       *httptest.Server
	branchA   uuid.UUID
	branchB   uuid.UUID
	branchC   uuid.UUID
	branchC2  uuid.UUID
	yardA     uuid.UUID
	yardB     uuid.UUID
	quoteA    uuid.UUID
	quoteB    uuid.UUID
	custID    uuid.UUID
	custB     uuid.UUID // a customer whose branches hold branch B only
	category  uuid.UUID // one product category the priced rules hang on
	catRuleA  uuid.UUID // a category rule scoped to the pin's customer
	catRuleB  uuid.UUID // a category rule scoped to the foreign customer
	taxExempt uuid.UUID // a tax exemption row of the foreign customer
	bound     string    // the key pinned to branch A
	unbound   string    // the same scopes, no pin
}

// pinKeyScopes opens every route under test: the location writes, the branch
// directory, the user grants, the exposure surface and the dealer wide reads.
var pinKeyScopes = []string{
	"locations:write", "branches:read", "branches:write",
	"users:read", "users:grants", "quotes:read", "quotes:write", "admin:write",
	"reports:read", "reporting:read", "reporting:write", "events:read",
	"gl:read", "gl:write", "ap:read", "bankrecon:read", "sales-team:read",
	"market-indices:write", "tax:write", "pricing:read", "pricing:write",
}

func newKeyPinFixture(t *testing.T) *keyPinFixture {
	t.Helper()
	testutil.LockOutboxTables(t) // the exposure writes record outbox events
	db := testutil.RequireDB(t)
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	f := &keyPinFixture{t: t, db: db,
		branchA: uuid.New(), branchB: uuid.New(), branchC: uuid.New(), branchC2: uuid.New(),
		yardA: uuid.New(), yardB: uuid.New(), quoteA: uuid.New(), quoteB: uuid.New(), custID: uuid.New(),
		custB: uuid.New(), category: uuid.New(), catRuleA: uuid.New(), catRuleB: uuid.New(), taxExempt: uuid.New(),
	}
	for _, r := range []struct {
		id     uuid.UUID
		typ    string
		parent any
	}{{f.branchA, "BRANCH", nil}, {f.branchB, "BRANCH", nil}, {f.branchC, "BRANCH", nil}, {f.branchC2, "BRANCH", nil},
		{f.yardA, "YARD", f.branchA}, {f.yardB, "YARD", f.branchB}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, name, parent_id) VALUES ($1, $2, $3, $4, $5)`,
			r.id, r.typ, "pin-"+r.id.String()[:8], "pin row "+r.id.String()[:8], r.parent); err != nil {
			t.Fatalf("seed location: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'pin cust', $2, $3)`, f.custID, "PIN-"+f.custID.String()[:8], f.branchA); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	for _, q := range []struct{ id, branch uuid.UUID }{{f.quoteA, f.branchA}, {f.quoteB, f.branchB}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO quotes (id, number, customer_id, state, branch_id, total_amount)
			VALUES ($1, $2, $3, 'DRAFT', $4, 10)`, q.id, "PINQ-"+q.id.String()[:8], f.custID, q.branch); err != nil {
			t.Fatalf("seed quote: %v", err)
		}
	}
	// A user granted both branches: the read narrowing keeps the pin's branch
	// only for the bound key and both for everyone else.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by)
		VALUES ('pin-both', $1, TRUE, 'test'), ('pin-both', $2, FALSE, 'test')`, f.branchA, f.branchB); err != nil {
		t.Fatalf("seed grants: %v", err)
	}
	// The customer confinement rows: a customer per branch with its
	// customer_branches row, a product category, one priced category rule per
	// customer, and a tax exemption of the foreign customer.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'pin cust B', $2, $3)`, f.custB, "PINB-"+f.custB.String()[:8], f.branchB); err != nil {
		t.Fatalf("seed customer B: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2), ($3, $4)`,
		f.custID, f.branchA, f.custB, f.branchB); err != nil {
		t.Fatalf("seed customer branches: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO product_categories (id, name, slug, path)
		VALUES ($1, 'pin cat', $2, $3)`, f.category, "pin-cat-"+f.category.String()[:8], "pin."+f.category.String()[:8]); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	for _, r := range []struct{ id, customer uuid.UUID }{{f.catRuleA, f.custID}, {f.catRuleB, f.custB}} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO category_pricing_rules (id, target_type, customer_id, category_id, rule_type, rule_value, created_by)
			VALUES ($1, 'ACCOUNT', $2, $3, 'MARKUP', 10, 'test')`, r.id, r.customer, f.category); err != nil {
			t.Fatalf("seed category rule: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO tax_exemptions (id, customer_id, exempt_reason) VALUES ($1, $2, 'pin')`, f.taxExempt, f.custB); err != nil {
		t.Fatalf("seed tax exemption: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote_exposure_event'
			AND entity_id IN (SELECT id::text FROM quote_exposure_events WHERE quote_id IN ($1, $2))`, f.quoteA, f.quoteB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN ($1, $2)`, f.quoteA, f.quoteB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quote_exposure_events WHERE quote_id IN ($1, $2)`, f.quoteA, f.quoteB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE id IN ($1, $2)`, f.quoteA, f.quoteB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM tax_exemptions WHERE customer_id IN ($1, $2) OR id = $3`, f.custID, f.custB, f.taxExempt)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'category_rule'
			AND entity_id IN (SELECT id::text FROM category_pricing_rules WHERE customer_id IN ($1, $2))`, f.custID, f.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'pricing_rule'
			AND entity_id IN (SELECT id::text FROM pricing_rules WHERE customer_id IN ($1, $2))`, f.custID, f.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM category_pricing_audit WHERE rule_id IN (SELECT id FROM category_pricing_rules WHERE customer_id IN ($1, $2))`, f.custID, f.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM category_pricing_rules WHERE customer_id IN ($1, $2)`, f.custID, f.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pricing_rules WHERE customer_id IN ($1, $2)`, f.custID, f.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_categories WHERE id = $1`, f.category)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id IN ($1, $2)`, f.custID, f.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id IN ($1, $2)`, f.custID, f.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub LIKE 'pin-%' OR user_sub = 'pin-both'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE code LIKE 'pin-new-%'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE parent_id IN ($1, $2, $3, $4)`, f.branchA, f.branchB, f.branchC, f.branchC2)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2, $3, $4, $5, $6)`,
			f.branchA, f.branchB, f.branchC, f.branchC2, f.yardA, f.yardB)
	})

	setSetting(t, db, "multi_branch_enabled", "true")
	setSetting(t, db, "default_branch_required", "false")
	wall := newBranchWall(db)
	mux := http.NewServeMux()
	wall.locations(mux, location.NewHandler(location.NewService(location.NewRepository(db)),
		location.NewUserRepository(db), middleware.RequireRole("admin", "owner")))

	// The exposure surface exactly as wireExposure registers it (the index
	// admin surface with the market index refresh comes with it).
	escRepo := pricing.NewEscalatorRepository(db)
	exposureRepo := pricing.NewExposureRepository(db)
	quoteRepo := quote.NewRepository(db)
	exposureAudit := &exposureAuditAdapter{auditLog: audit.NewLogger(db)}
	registerExposureRoutes(mux, exposureRoutes{
		Scanner: pricing.NewExposureScanner(exposureRepo, escRepo, quoteRepo, exposureAudit, db, slog.Default()).
			WithOutbox(outbox.NewWriter(db, "")),
		Checker:  pricing.NewExposureChecker(db),
		Exposure: exposureRepo,
		Service: pricing.NewExposureService(exposureRepo, escRepo, quoteRepo, exposureAudit, pricing.NewExposureChecker(db), slog.Default()).
			WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db),
		Escalators: escRepo,
		DB:         db,
		Logger:     slog.Default(),
		AuditLog:   audit.NewLogger(db),
	})

	// The dealer wide modules exactly as serve registers them: the reporting
	// surface in its three registrations, the events feed, the GL, AP and bank
	// reconciliation books and the sales team reads.
	reportingHandler := reporting.NewHandler(reporting.NewService(reporting.NewRepository(db)))
	wall.reporting(mux, reportingHandler)
	wall.reportingSchedules(mux, reportingHandler, nil)
	wall.reportingBIIntegration(mux, reportingHandler)
	wall.eventsFeed(mux, events.NewHandler(db))
	glSvc := gl.NewService(gl.NewRepository(db), glint.NewMockGLAdapter(), slog.Default())
	gl.NewHandler(glSvc).RegisterRoutes(mux, wall.dealerWideReads("the general ledger spans every branch", "admin", "owner"))
	ap.NewHandler(ap.NewService(db, ap.NewRepository(db), glSvc, slog.Default())).
		RegisterRoutes(mux, wall.dealerWideReads("payables span every branch", "admin", "owner", "finance"))
	bankrecon.NewHandler(bankrecon.NewService(db, bankrecon.NewRepository(db), glSvc, slog.Default())).
		RegisterRoutes(mux, wall.dealerWideReads("bank reconciliation spans every branch", "admin", "owner", "finance"))
	salesteam.NewHandler(salesteam.NewRepository(db)).
		RegisterRoutes(mux, wall.dealerWideReads("the sales team roster spans every branch", "admin", "owner", "sales"))

	// The customer confined modules exactly as serve registers them: the tax
	// exemption writes and the customer priced rules.
	customerSvc := customer.NewService(customer.NewRepository(db))
	productSvc := product.NewService(product.NewRepository(db))
	tax.NewHandler(orderwire.NewTaxService(db, &config.Config{}, slog.Default())).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "finance"))
	pricing.NewHandler(pricing.NewService(pricing.NewRepository(db)).WithTxRunner(db), customerSvc, productSvc).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	pricing.NewCategoryHandler(
		pricing.NewCategoryPricingService(pricing.NewCategoryRepository(db)).WithTxRunner(db).
			WithOutbox(outbox.NewWriter(db, "")).
			WithAudit(pricingAuditAdapter{l: audit.NewLogger(db)}),
		customerSvc).RegisterCategoryRoutes(mux, middleware.RequireRole("admin", "owner"))

	keys := techadmin.NewService(techadmin.NewRepository(db)).WithTxRunner(db)
	mint := func(name string, branch *uuid.UUID) string {
		t.Helper()
		raw, _, err := keys.GenerateKey(ctx, name, pinKeyScopes, branch)
		if err != nil {
			t.Fatalf("mint %s: %v", name, err)
		}
		return raw
	}
	f.bound = mint("pin bound to A", &f.branchA)
	f.unbound = mint("pin unbound", nil)
	mkAuth := middleware.NewMachineKeyAuth(pinKeyValidator{svc: keys}, audit.NewLogger(db), []string{"/api/integration/"}, nil)
	f.srv = httptest.NewServer(mkAuth.Handler(pinClaims(mux)))
	t.Cleanup(f.srv.Close)
	return f
}

// pinClaims injects the claims a JWT would carry only for a request that
// names a role, so a keyed request reaches the role guards with no claims
// (the machine key core's context is what passes it there), exactly as the
// real auth layer arranges.
func pinClaims(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role := r.Header.Get("X-Test-Role"); role != "" {
			claims := &middleware.UserClaims{
				RegisteredClaims: jwt.RegisteredClaims{Subject: r.Header.Get("X-Test-Sub")},
				Role:             role, Roles: []string{role},
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, claims)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// pinKeyValidator adapts the techadmin service to the machine key core, the
// same adapter serve uses.
type pinKeyValidator struct {
	svc *techadmin.Service
}

func (v pinKeyValidator) ValidateKey(ctx context.Context, rawKey string) (middleware.KeyPrincipal, error) {
	k, err := v.svc.ValidateKey(ctx, rawKey)
	if err != nil {
		if err == techadmin.ErrInvalidKey {
			return middleware.KeyPrincipal{}, middleware.ErrInvalidMachineKey
		}
		return middleware.KeyPrincipal{}, err
	}
	return middleware.KeyPrincipal{ID: k.ID.String(), Scopes: k.Scopes, BranchID: k.BranchID}, nil
}

// key sends one authenticated machine key request.
func (f *keyPinFixture) key(t *testing.T, raw, method, path, body string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+raw)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, buf
}

// user sends one request as the claims a JWT would carry.
func (f *keyPinFixture) user(t *testing.T, method, path, body string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Role", "admin")
	req.Header.Set("X-Test-Sub", "pin-boss")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, buf
}

func (f *keyPinFixture) auditRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action = 'key.branch_refused'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// refuseForeign runs one bound key call that must be refused with its audit
// row: 403 forbidden, the pin refusal message, one fresh key.branch_refused.
func (f *keyPinFixture) refuseForeign(t *testing.T, method, path, body string, hdr map[string]string) {
	t.Helper()
	before := f.auditRows(t)
	status, buf := f.key(t, f.bound, method, path, body, hdr)
	if status != http.StatusForbidden {
		t.Errorf("bound key %s %s: %d %s, want 403", method, path, status, buf)
		return
	}
	if !strings.Contains(string(buf), "a branch bound key may act only on its own branch") {
		t.Errorf("bound key %s %s: refusal body does not name the pin rule: %s", method, path, buf)
	}
	if after := f.auditRows(t); after != before+1 {
		t.Errorf("bound key %s %s: key.branch_refused rows %d then %d, want one new row", method, path, before, after)
	}
}

// allow runs one call that must pass (the bound key's own branch, or an
// unbound key, or a user) and returns its status with the body.
func (f *keyPinFixture) allow(t *testing.T, who, method, path, body string, hdr map[string]string, want int) []byte {
	t.Helper()
	var (
		status int
		buf    []byte
	)
	switch who {
	case "bound":
		status, buf = f.key(t, f.bound, method, path, body, hdr)
	case "unbound":
		status, buf = f.key(t, f.unbound, method, path, body, hdr)
	default:
		status, buf = f.user(t, method, path, body, hdr)
	}
	if status != want {
		t.Errorf("%s %s %s: %d %s, want %d", who, method, path, status, buf, want)
	}
	return buf
}

// parity asserts the unbound key and the user keep the same answer on a
// route: the reach they hold wherever the wall refuses the bound key.
func (f *keyPinFixture) parity(t *testing.T, method, path, body string) int {
	t.Helper()
	sk, _ := f.key(t, f.unbound, method, path, body, nil)
	su, _ := f.user(t, method, path, body, nil)
	if sk != su {
		t.Errorf("unbound key %s %s: %d, user: %d, want the same answer", method, path, sk, su)
	}
	return sk
}

// locState reads a location row's revision, name and active flag.
func (f *keyPinFixture) locState(t *testing.T, id uuid.UUID) (revision int64, name string, active bool) {
	t.Helper()
	err := f.db.Pool.QueryRow(context.Background(),
		`SELECT revision, COALESCE(name, ''), active FROM locations WHERE id = $1`, id).
		Scan(&revision, &name, &active)
	if err != nil {
		t.Fatal(err)
	}
	return revision, name, active
}

func (f *keyPinFixture) hasGrant(t *testing.T, sub string, branch uuid.UUID) bool {
	t.Helper()
	var ok bool
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM user_locations WHERE user_sub = $1 AND branch_id = $2)`, sub, branch).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// homeBranch reads the branch a user's is_home grant row names.
func (f *keyPinFixture) homeBranch(t *testing.T, sub string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT branch_id FROM user_locations WHERE user_sub = $1 AND is_home`, sub).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *keyPinFixture) exposureEvents(t *testing.T, quote uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM quote_exposure_events WHERE quote_id = $1`, quote).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// putLocationBody builds a PUT body that keeps the row's code and changes its
// name, with the If-Match the row's current revision demands.
func (f *keyPinFixture) putLocation(t *testing.T, who string, id uuid.UUID, name string, want int) {
	t.Helper()
	rev, code, _ := f.locState(t, id)
	body := fmt.Sprintf(`{"code":%q,"path":"pin","name":%q}`, code, name)
	f.allow(t, who, "PUT", "/api/v1/locations/"+id.String(), body,
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, want)
}

// TestKeyBranchPin_LocationWrites walks the location module's unwalled writes:
// every branch B row the bound key names is a refused 403 with its audit row
// and no write, the key's own branch passes, and the unbound key and the user
// keep their reach.
func TestKeyBranchPin_LocationWrites(t *testing.T) {
	f := newKeyPinFixture(t)
	A, B := f.branchA.String(), f.branchB.String()

	// PUT /locations/{id}: the foreign yard is refused and unchanged, the own
	// yard, the unbound key and the user pass.
	f.refuseForeign(t, "PUT", "/api/v1/locations/"+f.yardB.String(),
		fmt.Sprintf(`{"code":"pin-%s","path":"pin","name":"no"}`, f.yardB.String()[:8]),
		map[string]string{"If-Match": `"1"`})
	if rev, _, active := f.locState(t, f.yardB); rev != 1 || !active {
		t.Errorf("refused PUT changed the foreign yard: revision %d active %v", rev, active)
	}
	f.putLocation(t, "bound", f.yardA, "pin own edit", http.StatusOK)
	f.putLocation(t, "unbound", f.yardB, "pin free edit", http.StatusOK)
	f.putLocation(t, "user", f.yardB, "pin user edit", http.StatusOK)

	// DELETE /locations/{id}: the foreign yard is refused and stays active.
	f.refuseForeign(t, "DELETE", "/api/v1/locations/"+f.yardB.String(), "", map[string]string{"If-Match": `"2"`})
	if _, _, active := f.locState(t, f.yardB); !active {
		t.Errorf("refused DELETE archived the foreign yard")
	}
	rev, _, _ := f.locState(t, f.yardA)
	f.allow(t, "bound", "DELETE", "/api/v1/locations/"+f.yardA.String(), "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, http.StatusNoContent)
	rev, _, _ = f.locState(t, f.yardB)
	f.allow(t, "unbound", "DELETE", "/api/v1/locations/"+f.yardB.String(), "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, http.StatusNoContent)

	// POST /branches: the directory create is refused for a bound key
	// outright (a new branch is outside every pin), and stays open to the
	// unbound key and the user.
	newBranch := fmt.Sprintf(`{"type":"branch","code":"pin-new-%s","name":"pin new"}`, uuid.NewString()[:8])
	f.refuseForeign(t, "POST", "/api/v1/branches", newBranch, nil)
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM locations WHERE code LIKE 'pin-new-%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("refused POST /branches created rows")
	}
	f.allow(t, "unbound", "POST", "/api/v1/branches", newBranch, nil, http.StatusCreated)
	f.allow(t, "user", "POST", "/api/v1/branches",
		fmt.Sprintf(`{"type":"branch","code":"pin-new-%s","name":"pin new"}`, uuid.NewString()[:8]), nil, http.StatusCreated)

	// PUT /branches/{id}: the foreign branch is refused and unchanged, the
	// key's own branch passes, and the unbound key and the user keep theirs.
	f.refuseForeign(t, "PUT", "/api/v1/branches/"+B,
		fmt.Sprintf(`{"code":"pin-%s","path":"pin","name":"no"}`, B[:8]), map[string]string{"If-Match": `"1"`})
	if _, name, _ := f.locState(t, f.branchB); name != "pin row "+B[:8] {
		t.Errorf("refused PUT /branches renamed the foreign branch to %q", name)
	}
	putBranch := func(who string, id uuid.UUID, name string, want int) {
		rev, code, _ := f.locState(t, id)
		f.allow(t, who, "PUT", "/api/v1/branches/"+id.String(),
			fmt.Sprintf(`{"code":%q,"path":"pin","name":%q}`, code, name),
			map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, want)
	}
	putBranch("bound", f.branchA, "pin own branch", http.StatusOK)
	putBranch("unbound", f.branchB, "pin free branch", http.StatusOK)
	putBranch("user", f.branchB, "pin user branch", http.StatusOK)

	// DELETE /branches/{id}: the foreign branch is refused and stays active.
	f.refuseForeign(t, "DELETE", "/api/v1/branches/"+B, "", map[string]string{"If-Match": `"2"`})
	if _, _, active := f.locState(t, f.branchB); !active {
		t.Errorf("refused DELETE /branches archived the foreign branch")
	}
	rev, _, _ = f.locState(t, f.branchC)
	f.allow(t, "unbound", "DELETE", "/api/v1/branches/"+f.branchC.String(), "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, http.StatusNoContent)
	rev, _, _ = f.locState(t, f.branchC2)
	f.allow(t, "user", "DELETE", "/api/v1/branches/"+f.branchC2.String(), "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, http.StatusNoContent)
	rev, _, _ = f.locState(t, f.branchA)
	f.allow(t, "bound", "DELETE", "/api/v1/branches/"+A, "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, http.StatusNoContent)
}

// TestKeyBranchPin_UserGrants walks the grant, revoke and home branch routes:
// the bound key names branch B in the body or the path and is refused with
// its audit row and no grant write, its own branch passes, and the unbound
// key and the user keep their reach.
func TestKeyBranchPin_UserGrants(t *testing.T) {
	f := newKeyPinFixture(t)
	A, B := f.branchA.String(), f.branchB.String()

	// Grant: body branch_id.
	f.refuseForeign(t, "POST", "/api/v1/users/pin-bound-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, B), nil)
	if f.hasGrant(t, "pin-bound-grant", f.branchB) {
		t.Errorf("refused grant wrote the foreign grant row")
	}
	f.allow(t, "bound", "POST", "/api/v1/users/pin-bound-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, A), nil, http.StatusNoContent)
	if !f.hasGrant(t, "pin-bound-grant", f.branchA) {
		t.Errorf("own branch grant did not land")
	}
	f.allow(t, "unbound", "POST", "/api/v1/users/pin-free-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, B), nil, http.StatusNoContent)
	f.allow(t, "user", "POST", "/api/v1/users/pin-user-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, B), nil, http.StatusNoContent)

	// Home branch: body branch_id.
	f.refuseForeign(t, "PUT", "/api/v1/users/pin-bound-grant/home-branch", fmt.Sprintf(`{"branch_id":%q}`, B), nil)
	f.allow(t, "bound", "PUT", "/api/v1/users/pin-bound-grant/home-branch", fmt.Sprintf(`{"branch_id":%q}`, A), nil, http.StatusNoContent)
	f.allow(t, "unbound", "PUT", "/api/v1/users/pin-free-grant/home-branch", fmt.Sprintf(`{"branch_id":%q}`, B), nil, http.StatusNoContent)
	f.allow(t, "user", "PUT", "/api/v1/users/pin-user-grant/home-branch", fmt.Sprintf(`{"branch_id":%q}`, B), nil, http.StatusNoContent)

	// A body the handler would read only in part never reaches it: the wall
	// parses the whole body exactly as the handler's own decoder would have to,
	// and a valid object followed by anything at all is refused for the bound
	// key with its audit row and no write. Each body below names the foreign
	// branch in its first value, which is all the handler's json.Decoder reads.
	for _, body := range []string{
		fmt.Sprintf(`{"branch_id":%q} {}`, B),
		fmt.Sprintf(`{"branch_id":%q}xyz`, B),
		fmt.Sprintf(`{"branch_id":%q} 1`, B),
	} {
		f.refuseForeign(t, "POST", "/api/v1/users/pin-trail-grant/branches", body, nil)
		if f.hasGrant(t, "pin-trail-grant", f.branchB) {
			t.Errorf("grant body with trailing data %q wrote the foreign grant row", body)
		}
	}
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by)
		VALUES ('pin-trail-home', $1, TRUE, 'test'), ('pin-trail-home', $2, FALSE, 'test')`, f.branchA, f.branchB); err != nil {
		t.Fatalf("seed trail home grants: %v", err)
	}
	for _, body := range []string{
		fmt.Sprintf(`{"branch_id":%q} {}`, B),
		fmt.Sprintf(`{"branch_id":%q}]`, B),
	} {
		f.refuseForeign(t, "PUT", "/api/v1/users/pin-trail-home/home-branch", body, nil)
		if home := f.homeBranch(t, "pin-trail-home"); home != f.branchA {
			t.Errorf("home branch body with trailing data %q moved the home to %s", body, home)
		}
	}

	// Revoke: path branch_id.
	f.refuseForeign(t, "DELETE", "/api/v1/users/pin-both/branches/"+B, "", nil)
	if !f.hasGrant(t, "pin-both", f.branchB) {
		t.Errorf("refused revoke removed the foreign grant row")
	}
	f.allow(t, "bound", "DELETE", "/api/v1/users/pin-bound-grant/branches/"+A, "", nil, http.StatusNoContent)
	if f.hasGrant(t, "pin-bound-grant", f.branchA) {
		t.Errorf("own branch revoke did not land")
	}
	f.allow(t, "unbound", "DELETE", "/api/v1/users/pin-free-grant/branches/"+B, "", nil, http.StatusNoContent)
	f.allow(t, "user", "DELETE", "/api/v1/users/pin-user-grant/branches/"+B, "", nil, http.StatusNoContent)
}

// TestKeyBranchPin_LocationReads narrows the unwalled reads: the bound key
// reads only its own branch's grant rows, and the unbound key and the user
// keep the unfiltered answers.
func TestKeyBranchPin_LocationReads(t *testing.T) {
	f := newKeyPinFixture(t)
	A, B := f.branchA.String(), f.branchB.String()

	// The branch users read of the foreign branch is refused like a write.
	f.refuseForeign(t, "GET", "/api/v1/branches/"+B+"/users", "", nil)
	f.allow(t, "bound", "GET", "/api/v1/branches/"+A+"/users", "", nil, http.StatusOK)
	f.allow(t, "unbound", "GET", "/api/v1/branches/"+B+"/users", "", nil, http.StatusOK)
	f.allow(t, "user", "GET", "/api/v1/branches/"+B+"/users", "", nil, http.StatusOK)

	// A user's branch list carries only the pin's branch for the bound key,
	// and every granted branch for everyone else.
	read := func(who string) map[string]bool {
		t.Helper()
		buf := f.allow(t, who, "GET", "/api/v1/users/pin-both/branches", "", nil, http.StatusOK)
		var out []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(buf, &out); err != nil {
			t.Fatalf("%s branch list body: %v %s", who, err, buf)
		}
		got := map[string]bool{}
		for _, it := range out {
			got[it.ID] = true
		}
		return got
	}
	if got := read("bound"); got[A] != true || got[B] != false {
		t.Errorf("bound key branch list = %v, want only its own branch", got)
	}
	if got := read("unbound"); !got[A] || !got[B] {
		t.Errorf("unbound key branch list = %v, want both branches", got)
	}
	if got := read("user"); !got[A] || !got[B] {
		t.Errorf("user branch list = %v, want both branches", got)
	}
}

// TestKeyBranchPin_ExposureRoutes holds the pin on the exposure surface: the
// quote of the other branch is refused on every by id route with its audit
// row and no write, the key's own branch's quote passes to the module, the
// dealer wide scan is refused for a bound key, and the unbound key and the
// user keep their reach. AUTH_MODE=dev is the mode where the actor fallback
// lets a keyless caller act, so it is the mode that must hold.
func TestKeyBranchPin_ExposureRoutes(t *testing.T) {
	f := newKeyPinFixture(t)
	qA, qB := f.quoteA.String(), f.quoteB.String()
	ack := `{"method":"VERBAL","notes":"customer acknowledged by phone"}`
	override := `{"notes":"emergency override, owner spoken to"}`

	// Every by id route on the foreign quote: refused, audited, no write.
	for _, c := range []struct{ method, suffix, body string }{
		{"GET", "/exposure", ""},
		{"POST", "/exposure/acknowledge", ack},
		{"POST", "/exposure/request-ack", ""},
		{"POST", "/exposure/override", override},
		{"POST", "/exposure/escalate-now", ""},
	} {
		f.refuseForeign(t, c.method, "/api/v1/quotes/"+qB+c.suffix, c.body, nil)
	}
	if n := f.exposureEvents(t, f.quoteB); n != 0 {
		t.Errorf("refused exposure routes wrote %d events for the foreign quote", n)
	}

	// The own branch's quote passes to the module: the read and the preview
	// answer 200, request ack writes its event, and the two state gated
	// actions reach the module's own refusal (409, exposure already clear),
	// not the wall.
	f.allow(t, "bound", "GET", "/api/v1/quotes/"+qA+"/exposure", "", nil, http.StatusOK)
	f.allow(t, "bound", "POST", "/api/v1/quotes/"+qA+"/exposure/escalate-now", "", nil, http.StatusOK)
	f.allow(t, "bound", "POST", "/api/v1/quotes/"+qA+"/exposure/request-ack", "", nil, http.StatusOK)
	if n := f.exposureEvents(t, f.quoteA); n != 1 {
		t.Errorf("own quote request ack wrote %d events, want 1", n)
	}
	f.allow(t, "bound", "POST", "/api/v1/quotes/"+qA+"/exposure/acknowledge", ack, nil, http.StatusConflict)
	f.allow(t, "bound", "POST", "/api/v1/quotes/"+qA+"/exposure/override", override, nil, http.StatusConflict)

	// The dealer wide scan is refused for a bound key; the unbound key and
	// the user keep it.
	f.refuseForeign(t, "POST", "/api/v1/admin/exposure-scan", "", nil)
	f.allow(t, "unbound", "POST", "/api/v1/admin/exposure-scan", "", nil, http.StatusOK)
	f.allow(t, "user", "POST", "/api/v1/admin/exposure-scan", "", nil, http.StatusOK)

	// The unbound key and the user keep the foreign quote's surface.
	f.allow(t, "unbound", "GET", "/api/v1/quotes/"+qB+"/exposure", "", nil, http.StatusOK)
	f.allow(t, "user", "GET", "/api/v1/quotes/"+qB+"/exposure", "", nil, http.StatusOK)
	f.allow(t, "unbound", "POST", "/api/v1/quotes/"+qB+"/exposure/request-ack", "", nil, http.StatusOK)
	f.allow(t, "user", "POST", "/api/v1/quotes/"+qB+"/exposure/request-ack", "", nil, http.StatusOK)

	// The auth core's own header rule stands in front of all of it: a bound
	// key naming the other branch in X-Branch-Id is refused before the router.
	status, buf := f.key(t, f.bound, "GET", "/api/v1/quotes/"+qA+"/exposure", "",
		map[string]string{"X-Branch-Id": f.branchB.String()})
	if status != http.StatusForbidden {
		t.Errorf("bound key with foreign X-Branch-Id: %d %s, want 403", status, buf)
	}
}

// TestKeyBranchPin_DealerWideReads holds the pin on the dealer wide surface:
// every reporting route, the events feed, the two exposure lists, the GL, AP,
// bank reconciliation and sales team reads, the known users list and the
// market index refresh refuse a branch bound key outright with their audit
// row, while the unbound key and the user keep the same answer as each other
// on every route, and the GL writes (no branch fact, outside the ruling) keep
// their reach for every principal alike.
func TestKeyBranchPin_DealerWideReads(t *testing.T) {
	f := newKeyPinFixture(t)
	preview := `{"entity_type":"invoices","definition":{}}`
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/reports/sales-summary", ""},
		{"GET", "/api/v1/reports/daily-till", ""},
		{"GET", "/api/v1/reports/ar-aging", ""},
		{"GET", "/api/v1/reports/customer-statement/" + f.custID.String(), ""},
		{"GET", "/api/v1/reporting/export/invoices", ""},
		{"POST", "/api/v1/reporting/builder/preview", preview},
		{"POST", "/api/v1/reporting/builder/export", preview},
		{"GET", "/api/v1/reporting/saved", ""},
		{"POST", "/api/v1/reporting/saved/" + uuid.NewString() + "/run", ""},
		{"GET", "/api/v1/reporting/schedules", ""},
		{"POST", "/api/v1/reporting/schedules", `{"name":"pin"}`},
		{"GET", "/api/v1/events", ""},
		{"GET", "/api/v1/quotes/exposure", ""},
		{"GET", "/api/v1/reports/exposure", ""},
		{"GET", "/api/v1/gl/accounts", ""},
		{"GET", "/api/v1/gl/journal-entries", ""},
		{"GET", "/api/v1/gl/journal-entries/" + uuid.NewString(), ""},
		{"GET", "/api/v1/gl/trial-balance", ""},
		{"GET", "/api/v1/gl/profit-and-loss", ""},
		{"GET", "/api/v1/gl/balance-sheet", ""},
		{"GET", "/api/v1/gl/fiscal-periods", ""},
		{"GET", "/api/v1/ap/invoices", ""},
		{"GET", "/api/v1/ap/payments", ""},
		{"GET", "/api/v1/ap/aging", ""},
		{"GET", "/api/v1/bankrecon/accounts", ""},
		{"GET", "/api/v1/bankrecon/sessions", ""},
		{"GET", "/api/v1/sales-team", ""},
		{"GET", "/api/v1/sales-team/" + uuid.NewString(), ""},
		{"GET", "/api/v1/users", ""},
		{"POST", "/api/v1/market-indices/" + uuid.NewString() + "/refresh", ""},
	} {
		f.refuseForeign(t, c.method, c.path, c.body, nil)
		f.parity(t, c.method, c.path, c.body)
	}

	// The reads that answer data keep answering it to the unbound key: the
	// summaries and lists the second review read branch B rows through.
	for _, path := range []string{
		"/api/v1/reports/sales-summary", "/api/v1/reports/ar-aging",
		"/api/v1/reports/customer-statement/" + f.custID.String(),
		"/api/v1/events", "/api/v1/quotes/exposure", "/api/v1/gl/accounts",
		"/api/v1/ap/invoices", "/api/v1/sales-team", "/api/v1/users",
	} {
		if sk := f.parity(t, "GET", path, ""); sk != http.StatusOK {
			t.Errorf("unbound key GET %s: %d, want 200", path, sk)
		}
	}

	// The GL writes carry no branch fact and are outside the ruling: every
	// principal meets the same module answer, none a wall refusal.
	body := `{"name":"pin wall probe"}`
	sk, _ := f.key(t, f.unbound, "POST", "/api/v1/gl/accounts", body, nil)
	su, _ := f.user(t, "POST", "/api/v1/gl/accounts", body, nil)
	sb, bb := f.key(t, f.bound, "POST", "/api/v1/gl/accounts", body, nil)
	if sk != su || sb != sk {
		t.Errorf("POST /api/v1/gl/accounts: unbound %d, user %d, bound %d (%s), want one answer", sk, su, sb, bb)
	}
	if sb == http.StatusForbidden && strings.Contains(string(bb), "branch bound key") {
		t.Errorf("POST /api/v1/gl/accounts: the GL writes are outside the ruling and must not hit the wall: %s", bb)
	}
}

// pricingRuleBody builds a plain pricing rule create body for a customer.
// label tells the principals' rules apart: a rule's name and scope is unique.
func pricingRuleBody(label string, customer uuid.UUID) string {
	return fmt.Sprintf(`{"name":"pin rule %s","rule_type":"job_override","customer_id":%q,"discount_pct":"5"}`,
		label, customer.String())
}

// categoryRuleBody builds a category rule create body for a customer on a
// fresh category.
func categoryRuleBody(customer, category uuid.UUID) string {
	return fmt.Sprintf(`{"target_type":"account","customer_id":%q,"category_id":%q,"rule_type":"markup","value_pct":"5"}`,
		customer.String(), category.String())
}

// freshCategory inserts a product category no rule hangs on yet, so a create
// or a bulk element never meets the duplicate the seeded rules hold.
func (f *keyPinFixture) freshCategory(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO product_categories (id, name, slug, path) VALUES ($1, 'pin cat', $2, $3)`,
		id, "pin-cat-"+id.String()[:8], "pin."+id.String()[:8]); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	return id
}

// seedCatRule inserts a category rule row for a customer on a category.
func (f *keyPinFixture) seedCatRule(t *testing.T, id, customer, category uuid.UUID) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO category_pricing_rules (id, target_type, customer_id, category_id, rule_type, rule_value, created_by)
		VALUES ($1, 'ACCOUNT', $2, $3, 'MARKUP', 10, 'test')`, id, customer, category); err != nil {
		t.Fatalf("seed category rule: %v", err)
	}
}

// seedTaxExempt inserts a tax exemption row for a customer.
func (f *keyPinFixture) seedTaxExempt(t *testing.T, id, customer uuid.UUID) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO tax_exemptions (id, customer_id, exempt_reason) VALUES ($1, $2, 'pin')`, id, customer); err != nil {
		t.Fatalf("seed tax exemption: %v", err)
	}
}

// exemptionsOf counts a customer's tax exemption rows.
func (f *keyPinFixture) exemptionsOf(t *testing.T, customer uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM tax_exemptions WHERE customer_id = $1`, customer).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// rulesOf counts a customer's priced rules, plain and category.
func (f *keyPinFixture) rulesOf(t *testing.T, customer uuid.UUID) (plain, category int) {
	t.Helper()
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT (SELECT COUNT(*) FROM pricing_rules WHERE customer_id = $1),
		(SELECT COUNT(*) FROM category_pricing_rules WHERE customer_id = $1)`, customer).Scan(&plain, &category); err != nil {
		t.Fatal(err)
	}
	return plain, category
}

// catRuleRev reads a category rule row's revision for an If-Match.
func (f *keyPinFixture) catRuleRev(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var rev int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT revision FROM category_pricing_rules WHERE id = $1`, id).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`"%d"`, rev)
}

// TestKeyBranchPin_CustomerConfinedWrites holds the pin on the customer
// confined writes (the lead's ruling): the tax exemption writes and the
// customer priced rules (plain and category, single and bulk) reach a branch
// bound key only while its pin is among the customer's branches
// (customer_branches); a customer outside the pin is refused with the audit
// row and no write, the pin's own customer passes, and the unbound key and
// the user keep the same answer as each other on every route, each against
// its own fresh target where the write would otherwise consume it.
func TestKeyBranchPin_CustomerConfinedWrites(t *testing.T) {
	f := newKeyPinFixture(t)
	A, B := f.custID.String(), f.custB.String()

	// Tax exemptions: the create's body names the customer, the delete's row
	// belongs to one.
	f.refuseForeign(t, "POST", "/api/v1/tax/exemptions",
		fmt.Sprintf(`{"customer_id":%q,"exempt_reason":"pin"}`, B), nil)
	if n := f.exemptionsOf(t, f.custB); n != 1 {
		t.Errorf("refused exemption create wrote the foreign customer's rows: %d", n)
	}
	exemptA := uuid.New()
	f.seedTaxExempt(t, exemptA, f.custID)
	f.allow(t, "bound", "DELETE", "/api/v1/tax/exemptions/"+exemptA.String(), "", nil, http.StatusNoContent)
	f.refuseForeign(t, "DELETE", "/api/v1/tax/exemptions/"+f.taxExempt.String(), "", nil)
	if n := f.exemptionsOf(t, f.custB); n != 1 {
		t.Errorf("refused exemption delete changed the foreign customer's rows: %d", n)
	}
	f.allow(t, "bound", "POST", "/api/v1/tax/exemptions",
		fmt.Sprintf(`{"customer_id":%q,"exempt_reason":"pin"}`, A), nil, http.StatusCreated)
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/tax/exemptions",
			fmt.Sprintf(`{"customer_id":%q,"exempt_reason":"pin"}`, B), nil, http.StatusCreated)
	}
	for _, who := range []string{"unbound", "user"} {
		id := uuid.New()
		f.seedTaxExempt(t, id, f.custB)
		f.allow(t, who, "DELETE", "/api/v1/tax/exemptions/"+id.String(), "", nil, http.StatusNoContent)
	}

	// Plain pricing rules: the create's body names the customer.
	f.refuseForeign(t, "POST", "/api/v1/pricing/rules", pricingRuleBody("refused", f.custB), nil)
	if _, n := f.rulesOf(t, f.custB); n != 1 {
		t.Errorf("refused pricing rule create wrote the foreign customer's rows: %d", n)
	}
	f.allow(t, "bound", "POST", "/api/v1/pricing/rules", pricingRuleBody("own", f.custID), nil, http.StatusCreated)
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/rules", pricingRuleBody(who, f.custB), nil, http.StatusCreated)
	}

	// Category rules: the create's body names the customer, the update and
	// the delete act on a row that belongs to one.
	f.refuseForeign(t, "POST", "/api/v1/pricing/category-rules", categoryRuleBody(f.custB, f.freshCategory(t)), nil)
	if _, n := f.rulesOf(t, f.custB); n != 1 {
		t.Errorf("refused category rule create wrote the foreign customer's rows: %d", n)
	}
	f.allow(t, "bound", "POST", "/api/v1/pricing/category-rules",
		categoryRuleBody(f.custID, f.freshCategory(t)), nil, http.StatusCreated)
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/category-rules",
			categoryRuleBody(f.custB, f.freshCategory(t)), nil, http.StatusCreated)
	}

	putBody := `{"rule_type":"markdown","value_pct":"2"}`
	putHdr := func(id uuid.UUID) map[string]string {
		return map[string]string{"If-Match": f.catRuleRev(t, id)}
	}
	f.refuseForeign(t, "PUT", "/api/v1/pricing/category-rules/"+f.catRuleB.String(), putBody, putHdr(f.catRuleB))
	f.allow(t, "bound", "PUT", "/api/v1/pricing/category-rules/"+f.catRuleA.String(), putBody, putHdr(f.catRuleA), http.StatusOK)
	ruleFor := func(customer uuid.UUID) uuid.UUID {
		id := uuid.New()
		f.seedCatRule(t, id, customer, f.freshCategory(t))
		return id
	}
	for _, who := range []string{"unbound", "user"} {
		id := ruleFor(f.custB)
		f.allow(t, who, "PUT", "/api/v1/pricing/category-rules/"+id.String(), putBody, putHdr(id), http.StatusOK)
	}

	f.refuseForeign(t, "DELETE", "/api/v1/pricing/category-rules/"+f.catRuleB.String(), "", putHdr(f.catRuleB))
	if _, n := f.rulesOf(t, f.custB); n != 1 {
		t.Errorf("refused category rule delete changed the foreign customer's rows: %d", n)
	}
	deadA := ruleFor(f.custID)
	f.allow(t, "bound", "DELETE", "/api/v1/pricing/category-rules/"+deadA.String(), "", putHdr(deadA), http.StatusNoContent)
	for _, who := range []string{"unbound", "user"} {
		id := ruleFor(f.custB)
		f.allow(t, who, "DELETE", "/api/v1/pricing/category-rules/"+id.String(), "", putHdr(id), http.StatusNoContent)
	}

	// Bulk: every element's customer must hold the pin, and a bulk delete's
	// rows belong to their customers.
	f.refuseForeign(t, "POST", "/api/v1/pricing/category-rules/bulk",
		"["+categoryRuleBody(f.custID, f.freshCategory(t))+","+categoryRuleBody(f.custB, f.freshCategory(t))+"]", nil)
	if _, n := f.rulesOf(t, f.custB); n != 1 {
		t.Errorf("refused bulk upsert wrote the foreign customer's rows: %d", n)
	}
	f.allow(t, "bound", "POST", "/api/v1/pricing/category-rules/bulk",
		"["+categoryRuleBody(f.custID, f.freshCategory(t))+"]", nil, http.StatusOK)
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/category-rules/bulk",
			"["+categoryRuleBody(f.custB, f.freshCategory(t))+"]", nil, http.StatusOK)
	}
	f.refuseForeign(t, "DELETE", "/api/v1/pricing/category-rules/bulk",
		fmt.Sprintf(`{"ids":[%q]}`, f.catRuleB.String()), nil)
	if _, n := f.rulesOf(t, f.custB); n != 1 {
		t.Errorf("refused bulk delete changed the foreign customer's rows: %d", n)
	}
	for _, who := range []string{"unbound", "user"} {
		id := ruleFor(f.custB)
		f.allow(t, who, "DELETE", "/api/v1/pricing/category-rules/bulk",
			fmt.Sprintf(`{"ids":[%q]}`, id.String()), nil, http.StatusNoContent)
	}
}
