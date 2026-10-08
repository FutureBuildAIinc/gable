// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// The category tree, the category rules and the rules' value bounds on the
// wire contract (ADR 0001, ADR 0006 7.3): the create shape with its revision
// and ETag, one 400 naming every field, the revision preconditions with the
// target fields fixed at create, the keyset list with its filters, the bulk
// replace by id, the audit trail written with the rule, and the foreign key
// and unique refusals as 400 and 409 where the base commit answered 500.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// newCategoryFixture is the pricing fixture with the category routes mounted
// beside the rules routes, over a category of its own.
func newCategoryFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	f.srv.Close()
	customerSvc := customer.NewService(customer.NewRepository(f.db))
	productSvc := product.NewService(product.NewRepository(f.db))
	svc := pricing.NewService(pricing.NewRepository(f.db))
	catSvc := pricing.NewCategoryPricingService(pricing.NewCategoryRepository(f.db)).WithTxRunner(f.db)
	mux := http.NewServeMux()
	pricing.NewHandler(svc, customerSvc, productSvc).RegisterRoutes(mux)
	pricing.NewCategoryHandler(catSvc, customerSvc).RegisterCategoryRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(f.db)(mux))

	slug := "cw-" + uuid.NewString()[:8]
	res := f.do("POST", "/api/v1/pricing/categories", map[string]any{
		"name": "Wire " + slug, "slug": slug, "path": "cw_" + slug[3:],
	}, "Idempotency-Key", uuid.NewString())
	if res.status != http.StatusCreated {
		t.Fatalf("seed category: %d %s", res.status, res.raw)
	}
	catID := res.body["id"].(string)
	t.Cleanup(func() {
		ctx := t.Context()
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM category_pricing_audit WHERE category_id = $1`, catID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM category_pricing_rules WHERE category_id = $1`, catID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM product_categories WHERE id = $1`, catID)
	})
	return f, catID
}

func rawField(t *testing.T, raw []byte, name string) json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m[name]
}

func detailFields(res resp) map[string]bool {
	out := map[string]bool{}
	errBody, _ := res.body["error"].(map[string]any)
	details, _ := errBody["details"].([]any)
	for _, d := range details {
		out[d.(map[string]any)["field"].(string)] = true
	}
	return out
}

func errCode(res resp) string {
	errBody, _ := res.body["error"].(map[string]any)
	code, _ := errBody["code"].(string)
	return code
}

func (f *fixture) tierRule(catID, tier string, extra map[string]any) resp {
	body := map[string]any{
		"target_type": "tier", "tier": tier, "category_id": catID,
		"rule_type": "markup", "value_pct": "12.5",
	}
	for k, v := range extra {
		body[k] = v
	}
	return f.do("POST", "/api/v1/pricing/category-rules", body, "Idempotency-Key", uuid.NewString())
}

// TestCategoryRuleCreateShape: a created rule answers revision 1 with its
// ETag and Location, lowercase vocabularies, the value in the one reading its
// type gives it (null for the other) and every optional field as null.
func TestCategoryRuleCreateShape(t *testing.T) {
	f, cat := newCategoryFixture(t)

	pct := f.tierRule(cat, "WIRETIER", map[string]any{"margin_floor_pct": "5"})
	if pct.status != http.StatusCreated {
		t.Fatalf("create: %d %s", pct.status, pct.raw)
	}
	if pct.body["target_type"] != "tier" || pct.body["rule_type"] != "markup" {
		t.Fatalf("vocabularies = %v/%v, want lowercase", pct.body["target_type"], pct.body["rule_type"])
	}
	if pct.body["value_pct"] != "12.5" || pct.body["value_ten_thousandths"] != nil {
		t.Fatalf("a percent rule reads value_pct=%v value_ten_thousandths=%v", pct.body["value_pct"], pct.body["value_ten_thousandths"])
	}
	if pct.body["revision"] != json.Number("1") || pct.header.Get("ETag") != `"1"` {
		t.Fatalf("revision/ETag = %v/%q, want 1", pct.body["revision"], pct.header.Get("ETag"))
	}
	if pct.header.Get("Location") != "/api/v1/pricing/category-rules/"+pct.body["id"].(string) {
		t.Fatalf("Location = %q", pct.header.Get("Location"))
	}
	for _, field := range []string{"customer_id", "starts_at", "expires_at"} {
		if v, ok := pct.body[field]; !ok || v != nil {
			t.Errorf("%s = %v (present=%v), want present as null", field, v, ok)
		}
	}
	if pct.body["rule_value"] != nil {
		t.Fatalf("the legacy rule_value survives: %s", pct.raw)
	}

	fixed := f.tierRule(cat, "WIREFIXED", map[string]any{"rule_type": "fixed", "value_pct": nil, "value_ten_thousandths": 99900})
	if fixed.status != http.StatusCreated {
		t.Fatalf("fixed create: %d %s", fixed.status, fixed.raw)
	}
	if fixed.body["value_ten_thousandths"] != json.Number("99900") || fixed.body["value_pct"] != nil {
		t.Fatalf("a fixed rule reads value_ten_thousandths=%v value_pct=%v", fixed.body["value_ten_thousandths"], fixed.body["value_pct"])
	}

	// A second active rule for the same target and category is a 409 duplicate.
	dup := f.tierRule(cat, "WIRETIER", nil)
	if dup.status != http.StatusConflict || errCode(dup) != "duplicate" {
		t.Fatalf("duplicate: %d %s, want 409 duplicate", dup.status, dup.raw)
	}
	// The audit trail was written with the rule.
	audit := f.do("GET", "/api/v1/pricing/category-rules/"+pct.body["id"].(string)+"/audit", nil)
	items, _ := audit.body["items"].([]any)
	if audit.status != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["action"] != "CREATE" {
		t.Fatalf("audit after create: %d %s", audit.status, audit.raw)
	}
}

// TestCategoryRuleValidation: every offending field in one 400 with its path;
// a body field the route does not declare, a percentage past its bound and a
// reference that does not exist are 400s, never a database fault.
func TestCategoryRuleValidation(t *testing.T) {
	f, cat := newCategoryFixture(t)

	bad := f.do("POST", "/api/v1/pricing/category-rules", map[string]any{
		"target_type": "nope", "rule_type": "nope", "category_id": "not-a-uuid",
	}, "Idempotency-Key", uuid.NewString())
	if bad.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", bad.status)
	}
	got := detailFields(bad)
	for _, want := range []string{"target_type", "rule_type", "category_id"} {
		if !got[want] {
			t.Errorf("details miss %q: %s", want, bad.raw)
		}
	}

	for name, extra := range map[string]map[string]any{
		"a markdown past 100":     {"rule_type": "markdown", "value_pct": "101"},
		"a negative percent":      {"value_pct": "-1"},
		"a floor past 100":        {"margin_floor_pct": "100.5"},
		"a price past the bound":  {"rule_type": "fixed", "value_pct": nil, "value_ten_thousandths": 1000000000000},
		"both readings":           {"value_ten_thousandths": 5},
		"a percent as a number":   {"value_pct": 12.5},
		"a fixed rule with a pct": {"rule_type": "fixed"},
	} {
		if res := f.tierRule(cat, "WIREBAD", extra); res.status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, res.status, res.raw)
		}
	}
	if res := f.tierRule(cat, "WIREBAD", map[string]any{"rule_value": 5}); res.status != http.StatusBadRequest {
		t.Errorf("the legacy rule_value: status = %d, want 400 (an unknown field)", res.status)
	}

	ghost := f.do("POST", "/api/v1/pricing/category-rules", map[string]any{
		"target_type": "tier", "tier": "WIREGHOST", "category_id": uuid.NewString(),
		"rule_type": "markup", "value_pct": "1",
	}, "Idempotency-Key", uuid.NewString())
	if ghost.status != http.StatusBadRequest || !detailFields(ghost)["category_id"] {
		t.Fatalf("a category that does not exist: %d %s, want 400 naming category_id", ghost.status, ghost.raw)
	}
	acct := f.do("POST", "/api/v1/pricing/category-rules", map[string]any{
		"target_type": "account", "customer_id": uuid.NewString(), "category_id": cat,
		"rule_type": "markup", "value_pct": "1",
	}, "Idempotency-Key", uuid.NewString())
	if acct.status != http.StatusBadRequest || !detailFields(acct)["customer_id"] {
		t.Fatalf("a customer that does not exist: %d %s, want 400 naming customer_id", acct.status, acct.raw)
	}
}

// TestCategoryRuleRevision: the update and the delete name the revision
// (428 without, 409 stale, If-Match strong and weak), the target is fixed at
// create, and an update writes its audit entry with the old and new values.
func TestCategoryRuleRevision(t *testing.T) {
	f, cat := newCategoryFixture(t)
	rule := f.tierRule(cat, "WIREREV", nil)
	id := rule.body["id"].(string)
	path := "/api/v1/pricing/category-rules/" + id
	body := map[string]any{"rule_type": "markup", "value_pct": "15"}

	if res := f.do("PUT", path, body, "Idempotency-Key", uuid.NewString()); res.status != http.StatusPreconditionRequired {
		t.Fatalf("no revision: %d, want 428", res.status)
	}
	withTarget := map[string]any{"rule_type": "markup", "value_pct": "15", "tier": "OTHER"}
	if res := f.do("PUT", path, withTarget, "If-Match", `"1"`, "Idempotency-Key", uuid.NewString()); res.status != http.StatusBadRequest {
		t.Fatalf("a target field in the body: %d, want 400", res.status)
	}
	ok := f.do("PUT", path, body, "If-Match", `W/"1"`, "Idempotency-Key", uuid.NewString())
	if ok.status != http.StatusOK || ok.body["revision"] != json.Number("2") || ok.header.Get("ETag") != `"2"` {
		t.Fatalf("weak If-Match update: %d %s", ok.status, ok.raw)
	}
	if ok.body["value_pct"] != "15" || ok.body["tier"] != "WIREREV" || ok.body["category_id"] != cat {
		t.Fatalf("the update lost the target or the value: %s", ok.raw)
	}
	stale := f.do("PUT", path, body, "If-Match", `"1"`, "Idempotency-Key", uuid.NewString())
	if stale.status != http.StatusConflict || errCode(stale) != "stale_revision" {
		t.Fatalf("stale: %d %s, want 409 stale_revision", stale.status, stale.raw)
	}
	byBody := f.do("PUT", path, map[string]any{"rule_type": "markup", "value_pct": "16", "revision": 2}, "Idempotency-Key", uuid.NewString())
	if byBody.status != http.StatusOK || byBody.body["revision"] != json.Number("3") {
		t.Fatalf("body revision update: %d %s", byBody.status, byBody.raw)
	}
	if res := f.do("PUT", "/api/v1/pricing/category-rules/"+uuid.NewString(), body, "If-Match", `"1"`, "Idempotency-Key", uuid.NewString()); res.status != http.StatusNotFound {
		t.Fatalf("unknown rule: %d, want 404", res.status)
	}

	audit := f.do("GET", path+"/audit", nil)
	items, _ := audit.body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("audit holds %d entries, want 3 (create and two updates): %s", len(items), audit.raw)
	}

	if res := f.do("DELETE", path, nil); res.status != http.StatusPreconditionRequired {
		t.Fatalf("delete with no revision: %d, want 428", res.status)
	}
	if res := f.do("DELETE", path, nil, "If-Match", `"2"`); res.status != http.StatusConflict {
		t.Fatalf("delete stale: %d, want 409", res.status)
	}
	if res := f.do("DELETE", path, nil, "If-Match", `"3"`); res.status != http.StatusNoContent {
		t.Fatalf("delete: %d, want 204", res.status)
	}
	if res := f.do("DELETE", path, nil, "If-Match", `"3"`); res.status != http.StatusNotFound {
		t.Fatalf("delete again: %d, want 404", res.status)
	}
}

// TestCategoryRuleList: the list is the keyset envelope, newest first, the
// filters filter, an unknown value or parameter is a 400, and the cursor
// walks every row once.
func TestCategoryRuleList(t *testing.T) {
	f, cat := newCategoryFixture(t)
	empty := f.do("GET", "/api/v1/pricing/category-rules?category_id="+cat, nil)
	if empty.status != http.StatusOK || string(rawField(t, empty.raw, "items")) != "[]" {
		t.Fatalf("an empty page must carry items as [] in the bytes: %s", empty.raw)
	}
	for i := 0; i < 3; i++ {
		if res := f.tierRule(cat, "WIRELIST"+strconv.Itoa(i), nil); res.status != http.StatusCreated {
			t.Fatalf("create %d: %d %s", i, res.status, res.raw)
		}
	}
	f.tierRule(cat, "WIRELIST3", map[string]any{"is_active": false})

	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 5; page++ {
		url := "/api/v1/pricing/category-rules?category_id=" + cat + "&limit=2&include=total"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		res := f.do("GET", url, nil)
		if res.status != http.StatusOK || res.body["total"] != json.Number("4") {
			t.Fatalf("page %d: %d %s (total must be 4)", page, res.status, res.raw)
		}
		for _, it := range res.body["items"].([]any) {
			id := it.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("row %s served twice", id)
			}
			seen[id] = true
		}
		next, _ := res.body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 4 {
		t.Fatalf("the cursor walk served %d rows, want 4", len(seen))
	}

	active := f.do("GET", "/api/v1/pricing/category-rules?category_id="+cat+"&is_active=false", nil)
	if items := active.body["items"].([]any); len(items) != 1 {
		t.Fatalf("is_active=false filter served %d rows, want 1", len(items))
	}
	for _, q := range []string{"?target_type=TIER", "?offset=1", "?is_active=maybe", "?limit=0", "?cursor=garbage", "?customer_id=nope"} {
		if res := f.do("GET", "/api/v1/pricing/category-rules"+q, nil); res.status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, res.status)
		}
	}
}

// TestCategoryRuleBulk: a bulk element that carries an id replaces the rule
// with that id (the base upsert did), the batch is written with its audit
// entries or not at all, and the single create refuses an id.
func TestCategoryRuleBulk(t *testing.T) {
	f, cat := newCategoryFixture(t)
	rule := f.tierRule(cat, "WIREBULK", nil)
	id := rule.body["id"].(string)

	res := f.do("POST", "/api/v1/pricing/category-rules/bulk", []map[string]any{
		{"id": id, "target_type": "tier", "tier": "WIREBULK", "category_id": cat, "rule_type": "markup", "value_pct": "20"},
		{"target_type": "tier", "tier": "WIREBULK2", "category_id": cat, "rule_type": "margin", "value_pct": "22"},
	}, "Idempotency-Key", uuid.NewString())
	if res.status != http.StatusOK || res.body["count"] != json.Number("2") {
		t.Fatalf("bulk: %d %s", res.status, res.raw)
	}
	got := f.do("GET", "/api/v1/pricing/category-rules?category_id="+cat+"&tier=WIREBULK", nil)
	items := got.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["value_pct"] != "20" || items[0].(map[string]any)["revision"] != json.Number("2") {
		t.Fatalf("the element with an id did not replace its rule at the next revision: %s", got.raw)
	}
	if res := f.do("POST", "/api/v1/pricing/category-rules/bulk", []map[string]any{}, "Idempotency-Key", uuid.NewString()); res.status != http.StatusBadRequest {
		t.Fatalf("empty batch: %d, want 400", res.status)
	}
	if res := f.tierRule(cat, "WIREID", map[string]any{"id": uuid.NewString()}); res.status != http.StatusBadRequest {
		t.Fatalf("an id on a single create: %d, want 400", res.status)
	}
	del := f.do("DELETE", "/api/v1/pricing/category-rules/bulk", map[string]any{"ids": []string{id}})
	if del.status != http.StatusNoContent {
		t.Fatalf("bulk delete: %d %s", del.status, del.raw)
	}
}

// TestRuleBounds: a 100 percent discount and a 150 percent markup fit (the
// base columns overflowed to a 500, migration 093 widens them) and an
// impossible percentage or price is a 400.
func TestRuleBounds(t *testing.T) {
	f := newFixture(t)
	n := 0
	mk := func(extra map[string]any) resp {
		n++
		body := map[string]any{"name": f.sku + " rule " + strconv.Itoa(n), "rule_type": "promotional", "product_id": f.productID.String()}
		for k, v := range extra {
			body[k] = v
		}
		return f.do("POST", "/api/v1/pricing/rules", body, "Idempotency-Key", uuid.NewString())
	}
	if res := mk(map[string]any{"discount_pct": "100"}); res.status != http.StatusCreated {
		t.Fatalf("a 100 percent discount: %d %s", res.status, res.raw)
	}
	if res := mk(map[string]any{"markup_pct": "150"}); res.status != http.StatusCreated {
		t.Fatalf("a 150 percent markup: %d %s", res.status, res.raw)
	}
	for name, extra := range map[string]map[string]any{
		"a discount past 100":    {"discount_pct": "100.01"},
		"a negative markup":      {"markup_pct": "-5"},
		"a price past the bound": {"fixed_price_ten_thousandths": 1000000000000},
		"a negative price":       {"fixed_price_ten_thousandths": -1},
		"an unknown field":       {"discount_pct": "5", "tier": "GOLD"},
	} {
		if res := mk(extra); res.status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, res.status, res.raw)
		}
	}
	if res := mk(map[string]any{"discount_pct": "5", "product_id": uuid.NewString()}); res.status != http.StatusBadRequest || !detailFields(res)["product_id"] {
		t.Errorf("a product that does not exist: %d %s, want 400 naming product_id", res.status, res.raw)
	}
	// The same name and scope twice is a 409, not a database fault.
	first := mk(map[string]any{"discount_pct": "1"})
	body := map[string]any{"name": f.sku + " rule " + strconv.Itoa(n), "rule_type": "promotional", "product_id": f.productID.String(), "discount_pct": "2"}
	if again := f.do("POST", "/api/v1/pricing/rules", body, "Idempotency-Key", uuid.NewString()); first.status != http.StatusCreated || again.status != http.StatusConflict {
		t.Errorf("the same name and scope twice: %d then %d, want 201 then 409 (%s)", first.status, again.status, again.raw)
	}
	_, _ = f.db.Pool.Exec(t.Context(), `DELETE FROM pricing_rules WHERE name LIKE $1`, f.sku+" rule%")
}

// TestCategoryWrites: a category update on an unknown id is a 404 (the base
// echoed the body with a 200) and a repeated slug is a 409.
func TestCategoryWrites(t *testing.T) {
	f, cat := newCategoryFixture(t)
	missing := f.do("PUT", "/api/v1/pricing/categories/"+uuid.NewString(), map[string]any{
		"name": "Ghost", "slug": "cw-ghost", "path": "cw_ghost",
	}, "Idempotency-Key", uuid.NewString())
	if missing.status != http.StatusNotFound {
		t.Fatalf("unknown category: %d %s, want 404", missing.status, missing.raw)
	}
	list := f.do("GET", "/api/v1/pricing/categories?view=flat", nil)
	if list.status != http.StatusOK || list.body["items"] == nil {
		t.Fatalf("categories list is not the envelope: %d %s", list.status, list.raw)
	}
	if res := f.do("GET", "/api/v1/pricing/categories?view=grid", nil); res.status != http.StatusBadRequest {
		t.Fatalf("view=grid: %d, want 400", res.status)
	}
	slug := "cw-dup-" + uuid.NewString()[:6]
	first := f.do("POST", "/api/v1/pricing/categories", map[string]any{"name": "Dup", "slug": slug, "path": "cwdup_" + slug[7:]}, "Idempotency-Key", uuid.NewString())
	if first.status != http.StatusCreated {
		t.Fatalf("category create: %d %s", first.status, first.raw)
	}
	defer f.db.Pool.Exec(t.Context(), `DELETE FROM product_categories WHERE id = $1`, first.body["id"])
	again := f.do("POST", "/api/v1/pricing/categories", map[string]any{"name": "Dup", "slug": slug, "path": "cwdup2_" + slug[7:]}, "Idempotency-Key", uuid.NewString())
	if again.status != http.StatusConflict || errCode(again) != "duplicate" {
		t.Fatalf("repeated slug: %d %s, want 409 duplicate", again.status, again.raw)
	}
	_ = cat
}
