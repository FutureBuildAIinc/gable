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
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
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

	// A preview that names only a ship to is confined through that ship to's
	// customer (round 6 P3): the foreign branch's ship to is refused, the
	// pin's own customer's ship to passes, and a preview naming neither
	// stays a plain rate calculation.
	shipTo := func(id uuid.UUID) string {
		return fmt.Sprintf(`{"ship_to_id":%q,"document_type":"SalesInvoice","lines":[{"amount":100}]}`, id.String())
	}
	shipA, shipB := uuid.New(), uuid.New()
	for _, s := range []struct{ id, customer uuid.UUID }{{shipA, f.custID}, {shipB, f.custB}} {
		if _, err := f.db.Pool.Exec(context.Background(),
			`INSERT INTO customer_ship_tos (id, customer_id, code, name, line1) VALUES ($1, $2, 'pin', 'pin ship to', 'pin')`,
			s.id, s.customer); err != nil {
			t.Fatalf("seed ship to: %v", err)
		}
	}
	f.refuseForeign(t, "POST", "/api/v1/tax/preview", shipTo(shipB), nil)
	if status, buf := f.key(t, f.bound, "POST", "/api/v1/tax/preview", shipTo(shipA), nil); status == http.StatusForbidden {
		t.Errorf("tax preview of the pin's customer's ship to was refused: %s", buf)
	}
	f.parity(t, "POST", "/api/v1/tax/preview", shipTo(shipB))

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

	// The calculation also resolves job_id through its job's customer, the
	// same resolution the write carries (round 5 F-3): the pin's customer
	// with the foreign customer's job is refused, not answered with the job
	// rule that prices the foreign customer.
	jobRule := func(job uuid.UUID) string {
		return fmt.Sprintf(`{"name":"pin calc job %s","rule_type":"job_override","job_id":%q,"fixed_price_ten_thousandths":10000}`,
			job.String()[:8], job.String())
	}
	f.allow(t, "unbound", "POST", "/api/v1/pricing/rules", jobRule(f.jobB), nil, http.StatusCreated)
	calcJob := "/api/v1/pricing/calculate?product_id=" + f.posProd.String() + "&quantity=1&customer_id="
	f.refuseForeign(t, "GET", calcJob+A+"&job_id="+f.jobB.String(), "", nil)
	f.parity(t, "GET", calcJob+A+"&job_id="+f.jobB.String(), "")
	status, buf := f.key(t, f.bound, "GET", calcJob+A+"&job_id="+f.jobA.String(), "", nil)
	if status == http.StatusForbidden {
		t.Errorf("calculate with the pin's own job was refused: %s", buf)
	}
	if status == http.StatusOK && !strings.Contains(string(buf), `"price_basis":"retail"`) {
		t.Errorf("calculate with the pin's own job answered %s, want the module's own answer", buf)
	}
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

	// The index history read stays dealer wide reference data; the index
	// metadata write is refused with every other stated limit write (the
	// stated limit split: it feeds every branch's escalators).
	f.refuseForeign(t, "PUT", "/api/v1/market-indices/"+idx, `{"name":"pin"}`, nil)
	for _, path := range []string{
		"/api/v1/market-indices/" + idx + "/history",
	} {
		sb, bb := f.key(t, f.bound, "GET", path, "", nil)
		sk, _ := f.key(t, f.unbound, "GET", path, "", nil)
		if sb == http.StatusForbidden {
			t.Errorf("bound key GET %s: refused %s, want the module's own answer", path, bb)
		}
		if sb != sk {
			t.Errorf("GET %s: bound %d, unbound %d, want the same answer", path, sb, sk)
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
		raw, key, err := keys.GenerateKey(ctx, name, middleware.ValidScopeGrammar(), branch)
		if err != nil {
			t.Fatalf("mint %s: %v", name, err)
		}
		// The bound key references the branch, so it goes before the branch.
		t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, key.ID) })
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

// TestKeyBranchPin_StatedLimitWritesRefused holds the stated limit split (the
// lead's ruling on item 37, with item 38 and PD-3): a branch bound key reads
// the dealer wide reference data and is refused every write on it, each
// family named, with its audit row, while the unbound key keeps the write
// (never the gate's 403; the module's own answer). The driver routes are
// refused reads included (PD-3: personal data, no branch column); the vehicle
// reads stay stated limits.
func TestKeyBranchPin_StatedLimitWritesRefused(t *testing.T) {
	f := newKeyPinFixture(t)
	idx, prod := uuid.NewString(), f.posProd.String()
	drv, veh, partner, rfc, cat := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/admin/settings/ai", `{"provider":"avalara"}`},
		{"DELETE", "/api/v1/admin/settings/ai", ""},
		{"PUT", "/api/v1/admin/settings/routing", `{"provider":"avalara"}`},
		{"DELETE", "/api/v1/admin/settings/routing", ""},
		{"PUT", "/api/v1/admin/modules/" + uuid.NewString(), `{"is_enabled":false}`},
		{"POST", "/api/v1/apps/millwork/disable", ""},
		{"POST", "/api/v1/apps/millwork/enable", ""},
		{"PUT", "/api/v1/matching/config", `{"price_tolerance_pct":"1"}`},
		{"POST", "/api/v1/edi/partners", `{"partner_id":"PIN-EDI","name":"pin"}`},
		{"PUT", "/api/v1/edi/partners/" + partner, `{"name":"pin"}`},
		{"DELETE", "/api/v1/edi/partners/" + partner, ""},
		{"POST", "/api/v1/edi/partners/" + partner + "/import-catalog", `{}`},
		{"POST", "/api/v1/vendors", `{"name":"pin vendor","email":"pin@example.com"}`},
		{"POST", "/api/v1/units", `{"code":"PIN","name":"pin unit"}`},
		{"PUT", "/api/v1/units/PIN", `{"name":"pin unit"}`},
		{"POST", "/api/v1/pricing/categories", `{"name":"pin cat","slug":"pin-cat"}`},
		{"PUT", "/api/v1/pricing/categories/" + cat, `{"name":"pin cat"}`},
		{"POST", "/api/v1/pricing/rebates/programs", `{"name":"pin rebate"}`},
		{"POST", "/api/v1/millwork/options", `{"name":"pin option"}`},
		{"POST", "/api/v1/governance/rfcs", `{"title":"pin rfc"}`},
		{"PUT", "/api/v1/governance/rfcs/" + rfc, `{"title":"pin rfc"}`},
		{"POST", "/api/v1/governance/rfcs/" + rfc + "/transitions", `{"transition":"review"}`},
		{"PUT", "/api/v1/market-indices/" + idx, `{"name":"pin index"}`},
		{"POST", "/api/v1/delivery/drivers", `{"name":"pin driver","license_number":"PIN-1"}`},
		{"GET", "/api/v1/delivery/drivers", ""},
		{"GET", "/api/v1/delivery/drivers/" + drv, ""},
		{"PUT", "/api/v1/delivery/drivers/" + drv, `{"name":"pin driver"}`},
		{"DELETE", "/api/v1/delivery/drivers/" + drv, ""},
		{"POST", "/api/v1/delivery/drivers/" + drv + "/photo", ``},
		{"POST", "/api/v1/delivery/vehicles", `{"name":"pin truck","license_plate":"PIN-1"}`},
		{"PUT", "/api/v1/delivery/vehicles/" + veh, `{"name":"pin truck"}`},
		{"POST", "/api/v1/delivery/vehicles/" + veh + "/photo", ``},
		{"DELETE", "/api/v1/delivery/vehicles/" + veh, ""},
		{"POST", "/api/v1/products", `{"sku":"PIN-PROD-1","description":"pin"}`},
		{"PATCH", "/api/v1/products/" + prod + "/dimensions", `{}`},
		{"PATCH", "/api/v1/products/" + prod + "/lead-time", `{"lead_time_days":2,"revision":1}`},
		{"PUT", "/api/v1/products/" + prod + "/kit-components", `[]`},
		{"PUT", "/api/v1/products/" + prod + "/units", `[]`},
		{"PUT", "/api/v1/products/" + prod + "/pim/content", `{"description":"pin"}`},
		{"DELETE", "/api/v1/products/" + prod + "/pim/collateral/" + uuid.NewString(), ""},
		{"DELETE", "/api/v1/products/" + prod + "/pim/media/" + uuid.NewString(), ""},
		{"PATCH", "/api/v1/products/" + prod + "/pim/media/" + uuid.NewString() + "/primary", ``},
		{"POST", "/api/v1/products/" + prod + "/pim/generate/collateral", `{}`},
		{"POST", "/api/v1/products/" + prod + "/pim/generate/descriptions", `{}`},
		{"POST", "/api/v1/products/" + prod + "/pim/generate/image", `{}`},
		{"POST", "/api/v1/products/" + prod + "/pim/generate/seo", `{}`},
		{"POST", "/api/v1/charge-codes", `{"code":"PIN-CC","description":"pin"}`},
		{"PUT", "/api/v1/charge-codes/" + uuid.NewString(), `{"code":"PIN-CC"}`},
		{"POST", "/api/v1/payment-terms", `{"kind":"net_days","days":30}`},
		{"PUT", "/api/v1/payment-terms/" + uuid.NewString(), `{"kind":"net_days","days":30}`},
		{"POST", "/api/v1/purchase-orders/refresh-reorder-targets", `{"dry_run":false}`},
	} {
		f.refuseForeign(t, c.method, c.path, c.body, nil)
		if sk, _ := f.key(t, f.unbound, c.method, c.path, c.body, nil); sk == http.StatusForbidden {
			t.Errorf("unbound key %s %s: the gate's 403, want the module's own answer", c.method, c.path)
		}
	}

	// The written families prove nothing landed from the refused leg: the
	// bound key's margin attempt leaves the product's target margin where it
	// was, and the unbound key's same write sets it (the round 5 probe, both
	// ways, with the revision the write demands).
	var margin float64
	var revision int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(target_margin, 0), revision FROM products WHERE id = $1`, f.posProd).
		Scan(&margin, &revision); err != nil {
		t.Fatal(err)
	}
	f.refuseForeign(t, "PATCH", "/api/v1/products/"+prod+"/margins",
		fmt.Sprintf(`{"target_margin":0.5,"revision":%d}`, revision), nil)
	var after float64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(target_margin, 0) FROM products WHERE id = $1`, f.posProd).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != margin {
		t.Errorf("refused margins write moved the dealer wide target margin %v to %v", margin, after)
	}
	if sk, buf := f.key(t, f.unbound, "PATCH", "/api/v1/products/"+prod+"/margins",
		fmt.Sprintf(`{"target_margin":0.5,"revision":%d}`, revision), nil); sk != http.StatusOK {
		t.Errorf("unbound margins write: %d %s, want 200", sk, buf)
	}
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(target_margin, 0) FROM products WHERE id = $1`, f.posProd).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0.5 {
		t.Errorf("unbound margins write left target margin %v, want 0.5", after)
	}

	// The reads that stay stated limits answer the bound key as everyone
	// else: the catalogue, the charge codes, the payment terms and the
	// vehicle list.
	for _, path := range []string{
		"/api/v1/products", "/api/v1/products/" + prod,
		"/api/v1/charge-codes", "/api/v1/payment-terms", "/api/v1/delivery/vehicles",
	} {
		sb, bb := f.key(t, f.bound, "GET", path, "", nil)
		sk, _ := f.key(t, f.unbound, "GET", path, "", nil)
		if sb == http.StatusForbidden {
			t.Errorf("bound key GET %s: refused %s, want the stated limit read", path, bb)
		}
		if sb != sk {
			t.Errorf("GET %s: bound %d, unbound %d, want the same answer", path, sb, sk)
		}
	}
}

// TestKeyBranchPin_SharedCustomerWrites holds the stricter line of the
// principal's PD-4 ruling: a branch bound key may READ a customer shared
// with another branch, but may not add or change a customer scoped pricing
// rule or tax exemption for it, because the write prices the other branch's
// sales to that customer too. The refusal names the other branch; a customer
// the pin holds alone passes; the unbound key and the user keep both.
func TestKeyBranchPin_SharedCustomerWrites(t *testing.T) {
	f := newKeyPinFixture(t)
	// A customer the pin shares with branch B, and the pin's own alone.
	shared := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'pin shared', $2, $3)`,
		shared, "PINSH-"+shared.String()[:8], f.branchA); err != nil {
		t.Fatalf("seed shared customer: %v", err)
	}
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2), ($1, $3)`,
		shared, f.branchA, f.branchB); err != nil {
		t.Fatalf("seed shared branches: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM tax_exemptions WHERE customer_id = $1`, shared)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type IN ('category_rule', 'pricing_rule')
			AND entity_id IN (SELECT id::text FROM category_pricing_rules WHERE customer_id = $1 UNION SELECT id::text FROM pricing_rules WHERE customer_id = $1)`, shared)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM category_pricing_audit WHERE rule_id IN (SELECT id FROM category_pricing_rules WHERE customer_id = $1)`, shared)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM category_pricing_rules WHERE customer_id = $1`, shared)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pricing_rules WHERE customer_id = $1`, shared)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM customer_branches WHERE customer_id = $1`, shared)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM customers WHERE id = $1`, shared)
	})
	sh := shared.String()

	// The read passes: the exemption read of the shared customer answers.
	f.allow(t, "bound", "GET", "/api/v1/tax/exemptions/"+sh, "", nil, http.StatusOK)

	// Every customer scoped write on the shared customer is refused with its
	// audit row, the message naming branch B, and nothing written.
	before := struct{ rules, catRules, exemptions int }{}
	counts := func() (int, int, int) {
		var r, c, e int
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT (SELECT COUNT(*) FROM pricing_rules WHERE customer_id = $1),
			(SELECT COUNT(*) FROM category_pricing_rules WHERE customer_id = $1),
			(SELECT COUNT(*) FROM tax_exemptions WHERE customer_id = $1)`, shared).Scan(&r, &c, &e); err != nil {
			t.Fatal(err)
		}
		return r, c, e
	}
	refusedNaming := func(method, path, body string) {
		t.Helper()
		b := f.auditRows(t)
		status, buf := f.key(t, f.bound, method, path, body, nil)
		if status != http.StatusForbidden {
			t.Errorf("bound key %s %s: %d %s, want 403", method, path, status, buf)
			return
		}
		if !strings.Contains(string(buf), "the customer is also held by branch "+f.branchB.String()) {
			t.Errorf("bound key %s %s: the refusal does not name the other branch: %s", method, path, buf)
		}
		if after := f.auditRows(t); after != b+1 {
			t.Errorf("bound key %s %s: key.branch_refused rows %d then %d, want one new row", method, path, b, after)
		}
	}
	refusedNaming("POST", "/api/v1/pricing/rules", pricingRuleBody("shared", shared))
	refusedNaming("POST", "/api/v1/pricing/category-rules", categoryRuleBody(shared, f.freshCategory(t)))
	refusedNaming("POST", "/api/v1/tax/exemptions",
		fmt.Sprintf(`{"customer_id":%q,"exempt_reason":"pin"}`, sh))
	exemptShared := uuid.New()
	f.seedTaxExempt(t, exemptShared, shared)
	refusedNaming("DELETE", "/api/v1/tax/exemptions/"+exemptShared.String(), "")
	catShared := uuid.New()
	f.seedCatRule(t, catShared, shared, f.freshCategory(t))
	before.rules, before.catRules, before.exemptions = counts()
	refusedNaming("PUT", "/api/v1/pricing/category-rules/"+catShared.String(),
		`{"rule_type":"markdown","value_pct":"2"}`)
	refusedNaming("DELETE", "/api/v1/pricing/category-rules/"+catShared.String(), "")
	r, c, e := counts()
	if r != before.rules || c != before.catRules || e != before.exemptions {
		t.Errorf("refused shared customer writes changed rows: rules %d->%d, category %d->%d, exemptions %d->%d",
			before.rules, r, before.catRules, c, before.exemptions, e)
	}

	// A customer the pin holds alone passes every one of the same writes.
	f.allow(t, "bound", "POST", "/api/v1/pricing/rules", pricingRuleBody("own", f.custID), nil, http.StatusCreated)
	f.allow(t, "bound", "POST", "/api/v1/pricing/category-rules",
		categoryRuleBody(f.custID, f.freshCategory(t)), nil, http.StatusCreated)
	f.allow(t, "bound", "POST", "/api/v1/tax/exemptions",
		fmt.Sprintf(`{"customer_id":%q,"exempt_reason":"pin"}`, f.custID.String()), nil, http.StatusCreated)

	// The unbound key and the user keep the shared customer's writes (each on
	// its own fresh target).
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/rules", pricingRuleBody(who, shared), nil, http.StatusCreated)
		f.allow(t, who, "POST", "/api/v1/tax/exemptions",
			fmt.Sprintf(`{"customer_id":%q,"exempt_reason":"pin"}`, sh), nil, http.StatusCreated)
	}
}

// TestKeyBranchPin_GrantWritesAreAudited holds the PD-5 ruling's leg: a
// shared user's grant, revoke and home branch for the pin branch is accepted
// as confined and audited, the audit row naming the machine key that acted.
func TestKeyBranchPin_GrantWritesAreAudited(t *testing.T) {
	f := newKeyPinFixture(t)
	A := f.branchA.String()
	grantRows := func(action string) (n int) {
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM audit_log WHERE action = $1 AND actor_kind = 'key'`, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := grantRows("user.branch.granted")
	f.allow(t, "bound", "POST", "/api/v1/users/pin-audit-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, A), nil, http.StatusNoContent)
	if after := grantRows("user.branch.granted"); after != before+1 {
		t.Errorf("grant audit rows: %d then %d, want one new row naming the key", before, after)
	}
	before = grantRows("user.home_branch.set")
	f.allow(t, "bound", "PUT", "/api/v1/users/pin-audit-grant/home-branch", fmt.Sprintf(`{"branch_id":%q}`, A), nil, http.StatusNoContent)
	if after := grantRows("user.home_branch.set"); after != before+1 {
		t.Errorf("home branch audit rows: %d then %d, want one new row naming the key", before, after)
	}
	before = grantRows("user.branch.revoked")
	f.allow(t, "bound", "DELETE", "/api/v1/users/pin-audit-grant/branches/"+A, "", nil, http.StatusNoContent)
	if after := grantRows("user.branch.revoked"); after != before+1 {
		t.Errorf("revoke audit rows: %d then %d, want one new row naming the key", before, after)
	}
}

// TestKeyBranchPin_AISpendsWithItsKey holds the PD-6 ruling: the parsing
// upload and the vision scan stay stated limits, and each call is audited
// with the key that spent the dealer's AI credit.
func TestKeyBranchPin_AISpendsWithItsKey(t *testing.T) {
	f := newKeyPinFixture(t)
	spendRows := func() (n int) {
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM audit_log WHERE action IN ('ai.parsing.upload', 'ai.vision.scan') AND actor_kind = 'key'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The upload: a small multipart body; the AI client is unconfigured, so
	// the answer is the module's own (synthetic), never the gate's refusal.
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "pin-material.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("sku,qty\nPIN-1,2\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", f.srv.URL+"/api/v1/parsing/upload", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+f.bound)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	uploadBody, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode == http.StatusForbidden {
		t.Errorf("bound key parsing upload refused: %s", uploadBody)
	}
	if n := spendRows(); n < 1 {
		t.Errorf("parsing upload audit rows naming the key: %d, want at least one", n)
	}

	before := spendRows()
	status, buf := f.key(t, f.bound, "POST", "/api/v1/vision/scan", `{"blueprint_text":"pin wall probe"}`, nil)
	if status == http.StatusForbidden {
		t.Errorf("bound key vision scan refused: %s", buf)
	}
	if after := spendRows(); after != before+1 {
		t.Errorf("vision scan audit rows naming the key: %d then %d, want one new row", before, after)
	}
}
