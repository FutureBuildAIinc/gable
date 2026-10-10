// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The deny by default round's own tests (ADR 0007 section 5.5): the customer
// scoped reads that name a customer in a path, a query or a body are confined
// through that customer's customer_branches exactly like the writes; a pricing
// rule that names a job is confined through the job's customer; a rule or
// category rule that names neither a customer nor a job is a dealer wide write
// and is refused; and the stated limits stay reachable, with their branch
// directory and index reference data answering a bound key as they answer
// everyone else. All through the production chain the fixture builds.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// TestKeyBranchPin_CustomerConfinedReads holds the pin on the customer scoped
// reads (round 3 P1-2, round 4 P2): a read that names the foreign customer in
// its path or query is refused with the audit row and answers nothing, the
// pin's own customer passes, and the unbound key and the user keep the same
// answer as each other everywhere.
func TestKeyBranchPin_CustomerConfinedReads(t *testing.T) {
	f := newKeyPinFixture(t)
	A, B := f.custID.String(), f.custB.String()

	// The exemption read names the customer in its path.
	f.refuseForeign(t, "GET", "/api/v1/tax/exemptions/"+B, "", nil)
	f.allow(t, "bound", "GET", "/api/v1/tax/exemptions/"+A, "", nil, http.StatusOK)
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "GET", "/api/v1/tax/exemptions/"+B, "", nil, http.StatusOK)
	}

	// The tax preview names the customer in its body: confined when it names
	// one, and a preview that names none is a plain rate calculation.
	preview := func(customer string) string {
		return fmt.Sprintf(`{"customer_id":%q,"document_type":"SalesInvoice","lines":[{"amount":100}]}`, customer)
	}
	f.refuseForeign(t, "POST", "/api/v1/tax/preview", preview(B), nil)
	if status, _ := f.key(t, f.bound, "POST", "/api/v1/tax/preview", preview(A), nil); status == http.StatusForbidden {
		t.Errorf("tax preview of the pin's customer was refused: the wall must pass it to the module")
	}
	f.parity(t, "POST", "/api/v1/tax/preview", preview(B))

	// The category rule list is confined by its customer_id query; the
	// unfiltered list is refused (it carries every customer's rules).
	q := "/api/v1/pricing/category-rules?customer_id="
	f.refuseForeign(t, "GET", q+B, "", nil)
	f.allow(t, "bound", "GET", q+A, "", nil, http.StatusOK)
	f.refuseForeign(t, "GET", "/api/v1/pricing/category-rules", "", nil)
	for _, path := range []string{q + B, q + A, "/api/v1/pricing/category-rules"} {
		f.parity(t, "GET", path, "")
	}

	// The rule audit is confined by its row's customer.
	f.refuseForeign(t, "GET", "/api/v1/pricing/category-rules/"+f.catRuleB.String()+"/audit", "", nil)
	f.allow(t, "bound", "GET", "/api/v1/pricing/category-rules/"+f.catRuleA.String()+"/audit", "", nil, http.StatusOK)
	f.parity(t, "GET", "/api/v1/pricing/category-rules/"+f.catRuleB.String()+"/audit", "")

	// The calculation and the resolution name the customer in their query.
	// No product is seeded, so a read that passes the wall reaches the
	// module's own 404; a refused one never does.
	calc := "/api/v1/pricing/calculate?product_id=" + uuid.NewString() + "&customer_id="
	f.refuseForeign(t, "GET", calc+B+"&quantity=1", "", nil)
	f.allow(t, "bound", "GET", calc+A+"&quantity=1", "", nil, http.StatusNotFound)
	f.parity(t, "GET", calc+B+"&quantity=1", "")
	// The resolve preview answers a bare product with no match rather than a
	// 404; what matters is that the pin's customer reaches that answer and
	// the foreign one never reaches the module.
	resolve := "/api/v1/pricing/resolve?product_id=" + uuid.NewString() + "&customer_id="
	f.refuseForeign(t, "GET", resolve+B, "", nil)
	f.allow(t, "bound", "GET", resolve+A, "", nil, http.StatusOK)
	f.parity(t, "GET", resolve+B, "")
}

// TestKeyBranchPin_PricingRuleJobConfinement closes round 4 P1 and its dealer
// wide sibling P2: a pricing rule that names a job is confined through that
// job's customer (projects.customer_id), a rule that names a customer and a
// foreign job is refused (the resolver checks both), and a rule that names
// neither a customer nor a job prices every branch's customers and is refused.
func TestKeyBranchPin_PricingRuleJobConfinement(t *testing.T) {
	f := newKeyPinFixture(t)
	rulesOnJob := func(job uuid.UUID) (n int) {
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM pricing_rules WHERE job_id = $1`, job).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	jobRule := func(label string, job uuid.UUID) string {
		return fmt.Sprintf(`{"name":"pin job %s","rule_type":"job_override","job_id":%q,"fixed_price_ten_thousandths":10000}`,
			label, job.String())
	}

	// The foreign customer's job, with no customer named: refused, no rule.
	f.refuseForeign(t, "POST", "/api/v1/pricing/rules", jobRule("refused", f.jobB), nil)
	if n := rulesOnJob(f.jobB); n != 0 {
		t.Errorf("refused job rule wrote %d rows on the foreign customer's job", n)
	}
	// A rule naming the pin's customer AND the foreign customer's job: the
	// resolver checks both, so it is refused too.
	both := fmt.Sprintf(`{"name":"pin job both","rule_type":"job_override","customer_id":%q,"job_id":%q,"fixed_price_ten_thousandths":10000}`,
		f.custID.String(), f.jobB.String())
	f.refuseForeign(t, "POST", "/api/v1/pricing/rules", both, nil)
	if n := rulesOnJob(f.jobB); n != 0 {
		t.Errorf("refused customer plus foreign job rule wrote %d rows", n)
	}
	// The pin's own customer's job passes.
	f.allow(t, "bound", "POST", "/api/v1/pricing/rules", jobRule("own", f.jobA), nil, http.StatusCreated)
	// The unbound key and the user keep the foreign job and the dealer wide
	// rule.
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/rules", jobRule(who, f.jobB), nil, http.StatusCreated)
	}

	// A rule that names neither a customer nor a job is dealer wide: refused
	// for the bound key with its audit row, open to everyone else (each on
	// its own name: a rule's name and scope are unique).
	wide := func(label string) string {
		return fmt.Sprintf(`{"name":"pin wide %s","rule_type":"promotional","discount_pct":"50"}`, label)
	}
	f.refuseForeign(t, "POST", "/api/v1/pricing/rules", wide("refused"), nil)
	var wideRows int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM pricing_rules WHERE name LIKE 'pin wide %'`).Scan(&wideRows); err != nil {
		t.Fatal(err)
	}
	if wideRows != 0 {
		t.Errorf("refused dealer wide rule wrote %d rows", wideRows)
	}
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/rules", wide(who), nil, http.StatusCreated)
	}
}

// TestKeyBranchPin_CategoryTierRefusals closes the tier half of round 4 P2: a
// tier category rule prices every branch's customers, so a bound key is
// refused it on every verb, and a bulk delete that names a tier rule row is
// refused like one that names a foreign customer's row.
func TestKeyBranchPin_CategoryTierRefusals(t *testing.T) {
	f := newKeyPinFixture(t)
	// PIN-TIER is not any tier the migrations seed, so the rows this test
	// creates are exactly its own and the cleanup can name them (each on its
	// own category: a target and category pair is unique).
	tier := func() string {
		return fmt.Sprintf(`{"target_type":"tier","tier":"PIN-TIER","category_id":%q,"rule_type":"markup","value_pct":"5"}`,
			f.freshCategory(t).String())
	}

	f.refuseForeign(t, "POST", "/api/v1/pricing/category-rules", tier(), nil)
	var tierRows int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM category_pricing_rules WHERE tier = 'PIN-TIER'`).Scan(&tierRows); err != nil {
		t.Fatal(err)
	}
	if tierRows != 0 {
		t.Errorf("refused tier rule create wrote %d rows", tierRows)
	}
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/category-rules", tier(), nil, http.StatusCreated)
	}

	// A tier rule that also names the pin's own customer is still a tier rule:
	// it prices every branch's customers of that tier, so the wall refuses the
	// element however many branches of the named customer hold the pin (round
	// 6 P1-1). The handler and the migration keep the same invariant: a tier
	// rule carries no customer, an account rule no tier.
	tierNamingCustomer := func(customer uuid.UUID) string {
		return fmt.Sprintf(`{"target_type":"tier","tier":"PIN-TIER","customer_id":%q,"category_id":%q,"rule_type":"markup","value_pct":"5"}`,
			customer.String(), f.freshCategory(t).String())
	}
	for _, customer := range []uuid.UUID{f.custID, f.custB} {
		f.refuseForeign(t, "POST", "/api/v1/pricing/category-rules", tierNamingCustomer(customer), nil)
	}
	f.refuseForeign(t, "POST", "/api/v1/pricing/category-rules/bulk",
		"["+tierNamingCustomer(f.custID)+","+categoryRuleBody(f.custID, f.freshCategory(t))+"]", nil)
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM category_pricing_rules WHERE tier = 'PIN-TIER' AND customer_id IS NOT NULL`).Scan(&tierRows); err != nil {
		t.Fatal(err)
	}
	if tierRows != 0 {
		t.Errorf("refused tier rule naming a customer wrote %d rows", tierRows)
	}
	// A target that names no customer and no tier is neither an account rule
	// nor a tier rule: refused for the bound key, and the handler's own 400
	// answers everyone else.
	f.refuseForeign(t, "POST", "/api/v1/pricing/category-rules",
		fmt.Sprintf(`{"target_type":"account","category_id":%q,"rule_type":"markup","value_pct":"5"}`, f.freshCategory(t).String()), nil)
	// The unbound key and the user pass the wall on the same body and meet the
	// handler's own invariant answer: a tier rule that names a customer is a
	// 400 naming customer_id, never a write.
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/category-rules", tierNamingCustomer(f.custID), nil, http.StatusBadRequest)
	}

	putBody := `{"rule_type":"markdown","value_pct":"2"}`
	f.refuseForeign(t, "PUT", "/api/v1/pricing/category-rules/"+f.catRuleT.String(), putBody,
		map[string]string{"If-Match": f.catRuleRev(t, f.catRuleT)})
	f.refuseForeign(t, "DELETE", "/api/v1/pricing/category-rules/"+f.catRuleT.String(), "",
		map[string]string{"If-Match": f.catRuleRev(t, f.catRuleT)})
	var tierRow int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM category_pricing_rules WHERE id = $1`, f.catRuleT).Scan(&tierRow); err != nil {
		t.Fatal(err)
	}
	if tierRow != 1 {
		t.Errorf("tier rule refusals disturbed the seeded tier rule: %d rows", tierRow)
	}
	f.refuseForeign(t, "GET", "/api/v1/pricing/category-rules/"+f.catRuleT.String()+"/audit", "", nil)
	f.refuseForeign(t, "DELETE", "/api/v1/pricing/category-rules/bulk",
		fmt.Sprintf(`{"ids":[%q]}`, f.catRuleT.String()), nil)
	f.parity(t, "GET", "/api/v1/pricing/category-rules/"+f.catRuleT.String()+"/audit", "")
}

// TestKeyBranchPin_StatedLimits holds the stated limits open: the branch
// directory reads and the market index reference data answer a bound key as
// they answer everyone else, the reach the allowlist grants by design.
func TestKeyBranchPin_StatedLimits(t *testing.T) {
	f := newKeyPinFixture(t)
	B := f.branchB.String()
	idx := uuid.NewString()

	// The branch directory reads stay reference data.
	f.allow(t, "bound", "GET", "/api/v1/branches", "", nil, http.StatusOK)
	f.allow(t, "bound", "GET", "/api/v1/branches/"+B, "", nil, http.StatusOK)
	f.parity(t, "GET", "/api/v1/branches", "")
	f.parity(t, "GET", "/api/v1/branches/"+B, "")

	// The index metadata write and the history read are dealer wide reference
	// data: the bound key meets the module's own answer, never a refusal.
	for _, c := range []struct{ method, path string }{
		{"PUT", "/api/v1/market-indices/" + idx},
		{"GET", "/api/v1/market-indices/" + idx + "/history"},
	} {
		sb, bb := f.key(t, f.bound, c.method, c.path, "", nil)
		sk, _ := f.key(t, f.unbound, c.method, c.path, "", nil)
		if sb == http.StatusForbidden {
			t.Errorf("bound key %s %s: refused %s, want the module's own answer", c.method, c.path, bb)
		}
		if sb != sk {
			t.Errorf("%s %s: bound %d, unbound %d, want the same answer", c.method, c.path, sb, sk)
		}
	}
}

// allowlistGateFixture is the allowlist gate over the whole census: a stub
// handler registered for every machine key reachable census route, behind the
// production chain (buildChain) and the real machine key core, with one key
// bound to branch A and one unbound, each holding every scope the grammar can
// grant. A stub answers 200, so the only 403 a bound key can meet is the
// gate's, and the test says exactly which routes the allowlist refuses.
type allowlistGateFixture struct {
	t       *testing.T
	srv     *httptest.Server
	bound   string
	unbound string
}

func newAllowlistGateFixture(t *testing.T) *allowlistGateFixture {
	t.Helper()
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	branchA := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'pin gate branch')`,
		branchA, "pin-gate-"+branchA.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, branchA)
	})

	// One stub per machine key reachable census route: the pattern the gate
	// resolves is the registered one, exactly as the census names it.
	mux := http.NewServeMux()
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"stub":true}`))
	})
	for _, line := range allowlistCensusLines(t) {
		fields := strings.Split(line, "\t")
		mux.HandleFunc(fields[0]+" "+fields[1], stub)
	}

	keys := techadmin.NewService(techadmin.NewRepository(db)).WithTxRunner(db)
	mint := func(name string, branch *uuid.UUID) string {
		t.Helper()
		raw, _, err := keys.GenerateKey(ctx, name, middleware.ValidScopeGrammar(), branch)
		if err != nil {
			t.Fatalf("mint %s: %v", name, err)
		}
		return raw
	}
	f := &allowlistGateFixture{t: t,
		bound:   mint("pin gate bound", &branchA),
		unbound: mint("pin gate unbound", nil),
	}
	mkAuth := middleware.NewMachineKeyAuth(pinKeyValidator{svc: keys}, audit.NewLogger(db), []string{"/api/integration/"}, nil)
	chain := buildChain(ChainDeps{Mux: mux, DB: db, AuditLog: audit.NewLogger(db), MachineKeyAuth: mkAuth})
	f.srv = httptest.NewServer(pinClaims(chain))
	t.Cleanup(f.srv.Close)
	return f
}

// allowlistCensusLines reads the committed census from the serve package.
func allowlistCensusLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "ROUTES.txt"))
	if err != nil {
		t.Fatalf("read api/ROUTES.txt: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || !middleware.KeyBranchRouteReachable(fields[0], fields[1]) {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		t.Fatal("the census carries no machine key reachable routes")
	}
	return out
}

func (f *allowlistGateFixture) call(t *testing.T, key, method, path string) int {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

// TestKeyBranchPin_AllowlistGateCoversCensus drives every machine key
// reachable census route through the production chain: the bound key is
// refused exactly the REFUSED entries and passes everything else, the
// unbound key and the user pass everything. This is the one test that says
// the gate and the table agree with the census on every route at once, so a
// route reclassified, unclassified or misclassified fails here by name.
func TestKeyBranchPin_AllowlistGateCoversCensus(t *testing.T) {
	f := newAllowlistGateFixture(t)
	refused, admitted := 0, 0
	for _, line := range allowlistCensusLines(t) {
		fields := strings.Split(line, "\t")
		method, pattern := fields[0], fields[1]
		// A census pattern is the registered shape; drive one instance of it.
		path := allowlistInstantiate(pattern)
		want403 := false
		if class, ok := middleware.KeyBranchRouteClassFor(method, pattern); ok {
			want403 = class == middleware.KeyBranchClassRefused
		}
		if got := f.call(t, f.bound, method, path); (got == http.StatusForbidden) != want403 {
			t.Errorf("bound key %s %s: %d, want %s", method, pattern, got, allowlistWant(want403))
		}
		if got := f.call(t, f.unbound, method, path); got != http.StatusOK {
			t.Errorf("unbound key %s %s: %d, want 200 (the stub answers)", method, pattern, got)
		}
		if want403 {
			refused++
		} else {
			admitted++
		}
	}
	if refused == 0 || admitted == 0 {
		t.Fatalf("the census drove %d refused and %d admitted routes; both halves must be exercised", refused, admitted)
	}
	t.Logf("allowlist gate over the census: %d routes, %d refused to the bound key, %d admitted", refused+admitted, refused, admitted)
}

// allowlistInstantiate replaces a census pattern's wildcards with instance
// values the router accepts.
func allowlistInstantiate(pattern string) string {
	out := pattern
	out = strings.ReplaceAll(out, "{id}", "11111111-1111-1111-1111-111111111111")
	out = strings.ReplaceAll(out, "{customerId}", "22222222-2222-2222-2222-222222222222")
	out = strings.ReplaceAll(out, "{customerID}", "22222222-2222-2222-2222-222222222222")
	out = strings.ReplaceAll(out, "{sub}", "pin-sub")
	out = strings.ReplaceAll(out, "{branch_id}", "33333333-3333-3333-3333-333333333333")
	out = strings.ReplaceAll(out, "{module_id}", "m")
	out = strings.ReplaceAll(out, "{code}", "EA")
	out = strings.ReplaceAll(out, "{key}", "millwork")
	out = strings.ReplaceAll(out, "{entity}", "customers")
	out = strings.ReplaceAll(out, "{po_id}", "44444444-4444-4444-4444-444444444444")
	out = strings.ReplaceAll(out, "{delivery_id}", "44444444-4444-4444-4444-444444444444")
	out = strings.ReplaceAll(out, "{itemId}", "1")
	out = strings.ReplaceAll(out, "{freightId}", "55555555-5555-5555-5555-555555555555")
	out = strings.ReplaceAll(out, "{collateralId}", "66666666-6666-6666-6666-666666666666")
	out = strings.ReplaceAll(out, "{mediaId}", "77777777-7777-7777-7777-777777777777")
	return out
}

func allowlistWant(refused bool) string {
	if refused {
		return "403 (the allowlist refuses it)"
	}
	return "200 (the stub answers)"
}
