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
	"testing"

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
	resolve := "/api/v1/pricing/resolve?product_id=" + uuid.NewString() + "&customer_id="
	f.refuseForeign(t, "GET", resolve+B, "", nil)
	f.allow(t, "bound", "GET", resolve+A, "", nil, http.StatusNotFound)
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
	// for the bound key with its audit row, open to everyone else.
	wide := `{"name":"pin wide","rule_type":"promotional","discount_pct":"50"}`
	f.refuseForeign(t, "POST", "/api/v1/pricing/rules", wide, nil)
	var wideRows int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM pricing_rules WHERE name = 'pin wide'`).Scan(&wideRows); err != nil {
		t.Fatal(err)
	}
	if wideRows != 0 {
		t.Errorf("refused dealer wide rule wrote %d rows", wideRows)
	}
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/rules", wide, nil, http.StatusCreated)
	}
}

// TestKeyBranchPin_CategoryTierRefusals closes the tier half of round 4 P2: a
// tier category rule prices every branch's customers, so a bound key is
// refused it on every verb, and a bulk delete that names a tier rule row is
// refused like one that names a foreign customer's row.
func TestKeyBranchPin_CategoryTierRefusals(t *testing.T) {
	f := newKeyPinFixture(t)
	// PIN-TIER is not any tier the migrations seed, so the rows this test
	// creates are exactly its own and the cleanup can name them.
	tier := fmt.Sprintf(`{"target_type":"tier","tier":"PIN-TIER","category_id":%q,"rule_type":"markup","value_pct":"5"}`,
		f.freshCategory(t).String())

	f.refuseForeign(t, "POST", "/api/v1/pricing/category-rules", tier, nil)
	var tierRows int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM category_pricing_rules WHERE tier = 'PIN-TIER'`).Scan(&tierRows); err != nil {
		t.Fatal(err)
	}
	if tierRows != 0 {
		t.Errorf("refused tier rule create wrote %d rows", tierRows)
	}
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/category-rules", tier, nil, http.StatusCreated)
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
		{"GET", "/api/v1/market-indices"},
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
