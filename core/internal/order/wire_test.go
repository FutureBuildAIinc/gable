// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// The order module on the wire contract (ADR 0001) and the sales document
// line shape of ADR 0005 section 2, tested end to end: a real Postgres, the
// real handler on a real mux behind the real idempotency middleware,
// requests as JSON and responses read back as JSON. Nothing here touches an
// order type, so each test states a wire fact.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type fixture struct {
	t          *testing.T
	db         *database.DB
	srv        *httptest.Server
	customerID uuid.UUID
	productID  uuid.UUID
	sku        string
	branchRate string
}

// newFixture builds the module the way serve does (repository, service with
// the outbox, handler) behind the global idempotency layer, with a customer,
// a product and a branch tax rate of its own.
func newFixture(t *testing.T, db *database.DB) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	f := &fixture{t: t, db: db, customerID: uuid.New(), productID: uuid.New(), sku: "WIRE-" + uuid.NewString()[:8], branchRate: "0.088750"}
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Wire Order Co', $2, `+branch+`)`, f.customerID, "WORD-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5)`, f.productID, f.sku); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = $1
		WHERE id = `+branch, f.branchRate); err != nil {
		t.Fatalf("seed branch rate: %v", err)
	}

	repo := order.NewRepository(db)
	svc := order.NewService(repo).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAuditLog(audit.NewLogger(db))
	mux := http.NewServeMux()
	order.NewHandler(svc).RegisterRoutes(mux)
	// The branch middleware, as serve mounts it, so the wall is real.
	f.srv = httptest.NewServer(middleware.Idempotency(db)(middleware.NewBranchMiddleware(db).Handler(mux)))

	t.Cleanup(func() {
		f.srv.Close()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'order' AND entity_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, f.customerID)
	})
	return f
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *fixture) do(method, path string, body any, headers ...string) resp {
	f.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		f.t.Fatal(err)
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", "req-wire-test")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

func (f *fixture) line(qty string) map[string]any {
	return map[string]any{"product_id": f.productID.String(), "quantity": qty}
}

func (f *fixture) createBody(lines ...map[string]any) map[string]any {
	if len(lines) == 0 {
		lines = []map[string]any{f.line("10")}
	}
	return map[string]any{"customer_id": f.customerID.String(), "delivery_type": "pickup", "lines": lines}
}

func (f *fixture) create(lines ...map[string]any) resp {
	f.t.Helper()
	r := f.do("POST", "/api/v1/orders", f.createBody(lines...))
	if r.status != http.StatusCreated {
		f.t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	return r
}

func str(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	s, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s is %T (%v), want a string", key, m[key], m[key])
	}
	return s
}

func num(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	n, ok := m[key].(json.Number)
	if !ok {
		t.Fatalf("%s is %T (%v), want an integer", key, m[key], m[key])
	}
	v, err := n.Int64()
	if err != nil {
		t.Fatalf("%s = %s, want an integer: %v", key, n, err)
	}
	return v
}

func errorOf(t *testing.T, r resp) (code, message string, details []map[string]any) {
	t.Helper()
	e, ok := r.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope in %s", r.raw)
	}
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	if ds, ok := e["details"].([]any); ok {
		for _, d := range ds {
			details = append(details, d.(map[string]any))
		}
	}
	meta, _ := r.body["meta"].(map[string]any)
	if meta["request_id"] != "req-wire-test" {
		t.Errorf("meta.request_id = %v, want the request's id", meta["request_id"])
	}
	return code, message, details
}

func eventsFor(t *testing.T, db *database.DB, orderID string) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT type FROM events_outbox WHERE entity_type = 'order' AND entity_id = $1 ORDER BY position`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var ty string
		if err := rows.Scan(&ty); err != nil {
			t.Fatal(err)
		}
		types = append(types, ty)
	}
	return types
}

func revision(t *testing.T, r resp) int64 {
	t.Helper()
	if r.body == nil {
		t.Fatalf("no body in %s", r.raw)
	}
	return num(t, r.body, "revision")
}

// RULE (ADR 0001 sections 6, 7, 7a, 8, 11, 12; ADR 0005 5.1): a created
// order carries a document number, a lowercase status, integer cents money,
// a decimal string tax rate percent, a revision and its ETag, Location, and
// optional fields present as null.
func TestOrderCreateShape(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	r := f.create()
	if loc := r.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/orders/") {
		t.Errorf("Location = %q, want the new order's path", loc)
	}
	if etag := r.header.Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q, want the revision 1", etag)
	}
	if number := str(t, r.body, "number"); !strings.HasPrefix(number, "SO-") {
		t.Errorf("number = %q, want an SO- document number", number)
	}
	if status := str(t, r.body, "status"); status != "draft" {
		t.Errorf("status = %q, want draft", status)
	}
	if cur := str(t, r.body, "currency"); cur != "USD" {
		t.Errorf("currency = %q, want the customer's effective currency", cur)
	}
	if dt := str(t, r.body, "delivery_type"); dt != "pickup" {
		t.Errorf("delivery_type = %q, want pickup", dt)
	}
	if pct := str(t, r.body, "tax_rate_percent"); pct != "8.875" {
		t.Errorf("tax_rate_percent = %q, want 8.875 exactly (a 0.08875 rate)", pct)
	}
	if src := str(t, r.body, "tax_source"); src != "branch_rate" {
		t.Errorf("tax_source = %q, want branch_rate (a pickup takes the branch rate)", src)
	}
	// 10 at 5.50 is 5500 cents; 8.875 percent of 5500 is 488.125 -> 488.
	if sub, tax, total := num(t, r.body, "subtotal_cents"), num(t, r.body, "tax_cents"), num(t, r.body, "total_cents"); sub != 5500 || tax != 488 || total != 5988 {
		t.Errorf("subtotal=%d tax=%d total=%d, want 5500, 488, 5988", sub, tax, total)
	}
	if rev := revision(t, r); rev != 1 {
		t.Errorf("revision = %d, want 1", rev)
	}
	lines, ok := r.body["lines"].([]any)
	if !ok || len(lines) != 1 {
		t.Fatalf("lines = %v, want one", r.body["lines"])
	}
	line := lines[0].(map[string]any)
	if lt := str(t, line, "line_type"); lt != "product" {
		t.Errorf("line_type = %q, want product", lt)
	}
	if q := str(t, line, "quantity"); q != "10" {
		t.Errorf("quantity = %q, want the decimal string 10", q)
	}
	if p := num(t, line, "unit_price_ten_thousandths"); p != 55000 {
		t.Errorf("unit_price = %d, want the engine's 5.50 at scale 4", p)
	}
	if src := str(t, line, "price_source"); src != "price_list" {
		t.Errorf("price_source = %q, want price_list", src)
	}
	// Optional header fields are present as null (ADR 0001 section 12).
	for _, field := range []string{"quote_id", "job_id", "customer_po", "hold_reason", "confirmed_at", "ship_to"} {
		if v, ok := r.body[field]; !ok {
			t.Errorf("%s is absent, want present as null", field)
		} else if v != nil {
			t.Errorf("%s = %v, want null", field, v)
		}
	}
	if ev := eventsFor(t, db, str(t, r.body, "id")); len(ev) != 1 || ev[0] != "order.created" {
		t.Errorf("events = %v, want one order.created", ev)
	}
}

// RULE (ADR 0001 section 4): one 400 carrying every offending field with its
// full path, and an unknown body field is refused.
func TestOrderCreateValidation(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	r := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup",
		"lines": []map[string]any{
			{"product_id": f.productID.String(), "quantity": "0"},
			{"product_id": f.productID.String(), "quantity": "1", "unit_price_ten_thousandths": 100000},
		},
	})
	if r.status != 400 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "validation_failed" {
		t.Errorf("code = %q, want validation_failed", code)
	}
	fields := map[string]bool{}
	for _, d := range details {
		fields[d["field"].(string)] = true
	}
	for _, want := range []string{"lines[0].quantity", "lines[1].override_reason"} {
		if !fields[want] {
			t.Errorf("no error on %s (details: %v)", want, details)
		}
	}

	// An unknown body field is refused, never a silent no-op.
	r = f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup",
		"lines": []map[string]any{f.line("1")}, "currency": "EUR",
	})
	if r.status != 400 {
		t.Fatalf("a create that sends currency = %d: %s", r.status, r.raw)
	}

	// A body the route cannot consume is bad_request.
	r = f.do("POST", "/api/v1/orders", "not an object")
	if r.status != 400 {
		t.Errorf("a malformed body = %d, want 400", r.status)
	}
}

// RULE (the carried item of R1-15's review): when price_uom equals uom the
// conversion pair must be 1 and 1; any other pair on equal units is a 400 on
// uom_qty.
func TestOrderCreateEqualUnitsPairMustBeOneAndOne(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	for _, c := range []struct {
		name string
		line map[string]any
	}{
		{"equal units named, pair not 1 and 1", map[string]any{
			"quantity": "10", "uom": "PCS", "price_uom": "PCS",
			"uom_qty": "12", "price_uom_qty": "1", "unit_price_ten_thousandths": 55000, "override_reason": "match"}},
		// The server fills uom from the product; the rule runs on the
		// resolved unit (review P1-3, probe 1: a 201 at 550 cents where
		// 187.5 PCS at 5.50 is 103125).
		{"uom omitted, equal after the product fills it", map[string]any{
			"quantity": "187.5", "price_uom": "PCS", "uom_qty": "187.5", "price_uom_qty": "1"}},
		{"uom and price_uom omitted, pair not 1 and 1", map[string]any{
			"quantity": "10", "uom_qty": "12", "price_uom_qty": "1"}},
		// Probe 2: a different price unit with no pair (a 201 at 5500).
		{"uom omitted, price_uom differs, no pair", map[string]any{
			"quantity": "10", "price_uom": "MBF"}},
		{"uom named, price_uom differs, no pair", map[string]any{
			"quantity": "10", "uom": "PCS", "price_uom": "MBF"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			line := c.line
			line["product_id"] = f.productID.String()
			body := map[string]any{"customer_id": f.customerID.String(), "delivery_type": "pickup", "lines": []map[string]any{line}}
			r := f.do("POST", "/api/v1/orders", body)
			if r.status != 400 {
				t.Fatalf("create = %d: %s", r.status, r.raw)
			}
			code, _, details := errorOf(t, r)
			if code != "validation_failed" || len(details) == 0 || details[0]["field"] != "lines[0].uom_qty" {
				t.Errorf("code=%q details=%v, want a 400 on lines[0].uom_qty", code, details)
			}
			// The same line on a draft's PUT is held to the same rule.
			draft := f.create()
			put := map[string]any{"revision": revision(t, draft), "customer_id": f.customerID.String(), "delivery_type": "pickup", "lines": []map[string]any{line}}
			pr := f.do("PUT", "/api/v1/orders/"+str(t, draft.body, "id"), put)
			if pr.status != 400 {
				t.Fatalf("put = %d: %s", pr.status, pr.raw)
			}
		})
	}
	// A pair beside a different price unit still passes, with the unit
	// omitted: 187.5 PCS to 1 MBF.
	ok := f.do("POST", "/api/v1/orders", f.createBody(map[string]any{
		"product_id": f.productID.String(), "quantity": "187.5", "price_uom": "MBF",
		"uom_qty": "187.5", "price_uom_qty": "1", "unit_price_ten_thousandths": 5000000, "override_reason": "per MBF"}))
	if ok.status != 201 {
		t.Fatalf("a real pair with the unit omitted = %d: %s", ok.status, ok.raw)
	}
}

// RULE (ADR 0005 5.1 and section 1): a stocked line is sold in its product's
// stocking unit; another unit is refused with unit_not_stock_unit until
// cycle 3.
func TestOrderCreateStockingUnitRule(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	r := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup",
		"lines": []map[string]any{{"product_id": f.productID.String(), "quantity": "10", "uom": "MBF"}},
	})
	if r.status != 409 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "conflict" || len(details) != 1 || details[0]["code"] != "unit_not_stock_unit" {
		t.Errorf("code=%q details=%v, want conflict with unit_not_stock_unit", code, details)
	}
}

// RULE (ADR 0005 2.3): an override is stored with the engine's price beside
// it, its reason kept, its actor on the line, and one audit row written in
// the same transaction; a price equal to the engine's answer is stored as
// price_list with the reason dropped.
func TestOrderCreateOverrideAndDiscountAudit(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	r := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup",
		"lines": []map[string]any{{
			"product_id": f.productID.String(), "quantity": "10",
			"unit_price_ten_thousandths": 40000, "override_reason": "match the walk-in ask",
		}},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	line := r.body["lines"].([]any)[0].(map[string]any)
	if src := str(t, line, "price_source"); src != "override" {
		t.Errorf("price_source = %q, want override", src)
	}
	if p := num(t, line, "priced_unit_price_ten_thousandths"); p != 55000 {
		t.Errorf("priced_unit_price = %d, want the engine's 55000 beside the override", p)
	}
	if reason := str(t, line, "override_reason"); reason != "match the walk-in ask" {
		t.Errorf("override_reason = %q", reason)
	}
	orderID := str(t, r.body, "id")

	// One audit row for the override, inside the create's transaction.
	var n int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE entity_type = 'order' AND entity_id = $1 AND action = 'order.line_price_overridden'`,
		orderID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d override audit rows, want 1", n)
	}

	// A percent discount: the extension through ExtendDiscounted, its reason
	// kept, its audit row written.
	r = f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup",
		"lines": []map[string]any{{
			"product_id": f.productID.String(), "quantity": "10",
			"discount_percent": "10", "discount_reason": "good customer",
		}},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("discounted create = %d: %s", r.status, r.raw)
	}
	line = r.body["lines"].([]any)[0].(map[string]any)
	// 10 at 5.50 less 10 percent is 4950.
	if tot := num(t, line, "line_total_cents"); tot != 4950 {
		t.Errorf("discounted line_total = %d, want 4950", tot)
	}
	orderID = str(t, r.body, "id")
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE entity_type = 'order' AND entity_id = $1 AND action = 'order.line_discounted'`,
		orderID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d discount audit rows, want 1", n)
	}

	// An amount discount past the extension is refused naming the line.
	r = f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup",
		"lines": []map[string]any{{
			"product_id": f.productID.String(), "quantity": "1",
			"discount_cents": 99999, "discount_reason": "too much",
		}},
	})
	if r.status != 400 {
		t.Fatalf("a discount past the extension = %d: %s", r.status, r.raw)
	}
	_, _, details := errorOf(t, r)
	if len(details) == 0 || details[0]["field"] != "lines[0].discount_cents" {
		t.Errorf("details = %v, want lines[0].discount_cents", details)
	}
}

// RULE (ADR 0005 2.1 and 2.5): a charge line names a charge code, takes its
// default price when it sends none, its taxable flag from the code, and
// snapshots the code's revenue account; a text line carries only its
// description, its priced fields null but present.
func TestOrderCreateChargeAndTextLines(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	r := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup",
		"lines": []map[string]any{
			f.line("10"),
			{"charge_code": "FREIGHT", "description": "Delivery to site", "quantity": "1", "unit_price_ten_thousandths": 15000},
			{"line_type": "text", "description": "Leave at the side gate"},
		},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	lines := r.body["lines"].([]any)
	if len(lines) != 3 {
		t.Fatalf("%d lines, want 3", len(lines))
	}
	charge := lines[1].(map[string]any)
	if lt := str(t, charge, "line_type"); lt != "charge" {
		t.Errorf("charge line_type = %q", lt)
	}
	if code := str(t, charge, "charge_code"); code != "FREIGHT" {
		t.Errorf("charge_code = %q, want FREIGHT", code)
	}
	if acct := str(t, charge, "revenue_account_code"); acct != "4020" {
		t.Errorf("revenue_account_code = %q, want the code's 4020 snapshotted", acct)
	}
	if src := str(t, charge, "price_source"); src != "manual" {
		t.Errorf("charge price_source = %q, want manual", src)
	}
	if taxable := charge["taxable"].(bool); taxable {
		t.Error("FREIGHT is not taxable in the seed")
	}
	text := lines[2].(map[string]any)
	for _, field := range []string{"quantity", "uom", "price_uom", "uom_qty", "price_uom_qty",
		"unit_price_ten_thousandths", "line_total_cents", "product_id", "charge_code_id"} {
		if v, ok := text[field]; !ok || v != nil {
			t.Errorf("text line %s = %v, want present and null", field, v)
		}
	}
	// The totals: the charge counts in the subtotal, the text line does not.
	if sub := num(t, r.body, "subtotal_cents"); sub != 5650 {
		t.Errorf("subtotal = %d, want 5650 (5500 goods + 150 freight)", sub)
	}
	// Only the taxable goods line is taxed: 8.875 percent of 5500.
	if tax := num(t, r.body, "tax_cents"); tax != 488 {
		t.Errorf("tax = %d, want 488 on the taxable base alone", tax)
	}
}

// RULE (ADR 0005 2.6): a kit explodes into its components, is priced as a
// whole, and its components carry no price and no tax.
func TestOrderCreateKitExplodes(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()
	kit := uuid.New()
	comp := uuid.New()
	kitSKU := "WIRE-KIT-" + uuid.NewString()[:6]
	compSKU := "WIRE-POST-" + uuid.NewString()[:6]
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price, is_kit, taxable)
		VALUES ($1, $2, 'A fence section kit', 'EA', 100.00, TRUE, TRUE)`, kit, kitSKU); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, 'A fence post', 'EA', 12.50)`, comp, compSKU); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position)
		VALUES ($1, $2, 4, 0)`, kit, comp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_kit_components WHERE kit_product_id = $1`, kit)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id IN ($1, $2)`, kit, comp)
	})

	r := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup",
		"lines": []map[string]any{{"product_id": kit.String(), "quantity": "2"}},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	lines := r.body["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("%d lines, want the kit and its component", len(lines))
	}
	kitLine := lines[0].(map[string]any)
	compLine := lines[1].(map[string]any)
	if str(t, kitLine, "line_type") != "kit" || str(t, compLine, "line_type") != "component" {
		t.Fatalf("line types = %q, %q, want kit, component",
			str(t, kitLine, "line_type"), str(t, compLine, "line_type"))
	}
	if parent := str(t, compLine, "parent_line_id"); parent != str(t, kitLine, "id") {
		t.Errorf("component parent = %q, want the kit line", parent)
	}
	if q := str(t, compLine, "quantity"); q != "8" {
		t.Errorf("component quantity = %q, want 8 (2 kits x 4 posts)", q)
	}
	if p := num(t, compLine, "unit_price_ten_thousandths"); p != 0 {
		t.Errorf("component price = %d, want 0 (the kit prices as a whole)", p)
	}
	if tot := num(t, compLine, "line_total_cents"); tot != 0 {
		t.Errorf("component total = %d, want 0", tot)
	}
	if src := str(t, compLine, "price_source"); src != "none" {
		t.Errorf("component price_source = %q, want none", src)
	}
	if sub := num(t, r.body, "subtotal_cents"); sub != 20000 {
		t.Errorf("subtotal = %d, want 20000 (2 kits at 100.00, components free)", sub)
	}
}

// RULE (ADR 0005 5.2): the edit replaces header and lines in draft only,
// with the revision precondition; a line keeps its id across edits.
func TestOrderUpdate(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	created := f.create()
	id := str(t, created.body, "id")
	lineID := str(t, created.body["lines"].([]any)[0].(map[string]any), "id")

	// Without a precondition it is 428.
	r := f.do("PUT", "/api/v1/orders/"+id, map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup", "lines": []map[string]any{f.line("3")},
	})
	if r.status != 428 {
		t.Fatalf("edit without a precondition = %d, want 428", r.status)
	}
	// A stale revision is 409.
	r = f.do("PUT", "/api/v1/orders/"+id, map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup", "revision": 99, "lines": []map[string]any{f.line("3")},
	})
	if r.status != 409 {
		t.Fatalf("edit on a stale revision = %d, want 409", r.status)
	}
	// The edit keeps the line id it is sent.
	r = f.do("PUT", "/api/v1/orders/"+id, map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup", "revision": 1,
		"lines": []map[string]any{{"id": lineID, "product_id": f.productID.String(), "quantity": "3"}},
	})
	if r.status != 200 {
		t.Fatalf("edit = %d: %s", r.status, r.raw)
	}
	if rev := revision(t, r); rev != 2 {
		t.Errorf("revision after edit = %d, want 2", rev)
	}
	if newID := str(t, r.body["lines"].([]any)[0].(map[string]any), "id"); newID != lineID {
		t.Errorf("line id moved from %s to %s", lineID, newID)
	}
	if sub := num(t, r.body, "subtotal_cents"); sub != 1650 {
		t.Errorf("subtotal after edit = %d, want 1650", sub)
	}
	if ev := eventsFor(t, db, id); len(ev) != 2 || ev[1] != "order.updated" {
		t.Errorf("events = %v, want order.created then order.updated", ev)
	}

	// Once confirmed, the edit is a 409 with order_not_draft.
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 2})
	if r.status != 200 {
		t.Fatalf("confirm = %d: %s", r.status, r.raw)
	}
	r = f.do("PUT", "/api/v1/orders/"+id, map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup", "revision": 3, "lines": []map[string]any{f.line("3")},
	})
	if r.status != 409 {
		t.Fatalf("edit of a confirmed order = %d, want 409", r.status)
	}
	code, _, details := errorOf(t, r)
	if code != "conflict" || len(details) != 1 || details[0]["code"] != "order_not_draft" {
		t.Errorf("code=%q details=%v, want conflict with order_not_draft", code, details)
	}
}

// RULE (ADR 0001 sections 1, 2 and 5): the list envelope, the cursor that
// walks every row once, the filters that filter, the unknown parameter
// refused, include=total.
func TestOrderList(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ids := []string{}
	for i := 0; i < 3; i++ {
		r := f.create()
		ids = append(ids, str(t, r.body, "id"))
	}
	// An empty page is [] in the bytes, for another customer.
	other := f.do("GET", "/api/v1/orders?customer_id="+uuid.New().String(), nil)
	if !strings.Contains(string(other.raw), `"items":[]`) {
		t.Errorf("an empty page = %s, want items []", other.raw)
	}

	r := f.do("GET", "/api/v1/orders?customer_id="+f.customerID.String()+"&limit=2", nil)
	if r.status != 200 {
		t.Fatalf("list = %d: %s", r.status, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("%d items, want the limit 2", len(items))
	}
	cursor, _ := r.body["next_cursor"].(string)
	if cursor == "" {
		t.Fatal("next_cursor missing with more pages")
	}
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.(map[string]any)["id"].(string)] = true
	}
	next := cursor
	pages := 0
	for next != "" && pages < 5 {
		r := f.do("GET", "/api/v1/orders?customer_id="+f.customerID.String()+"&limit=2&cursor="+next, nil)
		for _, it := range r.body["items"].([]any) {
			id := it.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("order %s appeared twice", id)
			}
			seen[id] = true
		}
		next, _ = r.body["next_cursor"].(string)
		pages++
	}
	if len(seen) != 3 {
		t.Errorf("the cursor walked %d orders, want 3", len(seen))
	}

	// The status filter filters; an unknown value is refused; an unknown
	// parameter is refused.
	r = f.do("GET", "/api/v1/orders?customer_id="+f.customerID.String()+"&status=draft", nil)
	if r.status != 200 || len(r.body["items"].([]any)) != 3 {
		t.Errorf("status=draft = %d with %v", r.status, r.body["items"])
	}
	r = f.do("GET", "/api/v1/orders?customer_id="+f.customerID.String()+"&status=sent", nil)
	if r.status != 400 {
		t.Errorf("status=sent = %d, want 400", r.status)
	}
	r = f.do("GET", "/api/v1/orders?customer_id="+f.customerID.String()+"&client=none", nil)
	if r.status != 400 {
		t.Errorf("an unknown parameter = %d, want 400", r.status)
	}
	r = f.do("GET", "/api/v1/orders?customer_id="+f.customerID.String()+"&include=total", nil)
	if r.status != 200 || num(t, r.body, "total") != 3 {
		t.Errorf("include=total = %d %v", r.status, r.body["total"])
	}
	// The delivery_type filter.
	r = f.do("GET", "/api/v1/orders?customer_id="+f.customerID.String()+"&delivery_type=delivery", nil)
	if r.status != 200 || len(r.body["items"].([]any)) != 0 {
		t.Errorf("delivery_type=delivery = %d with %d items, want none (all pickups)", r.status, len(r.body["items"].([]any)))
	}
}

// RULE (ADR 0005 5.2 and 5.3): the confirm runs the guards and the credit
// check; over the limit the order lands on_hold in the same transaction,
// order.hold is written and the answer is 200, never an error; the release
// skips the credit check. The credit check reads documents, not
// customers.balance_due.
func TestOrderConfirmCreditHoldAndRelease(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()
	// A credit limit of 5000 cents; the order's total is 5988 with tax.
	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET credit_limit = 50.00, balance_due = 999999.00 WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	r := f.create() // 5500 subtotal + 488 tax = 5988 total
	id := str(t, r.body, "id")
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != 200 {
		t.Fatalf("confirm = %d: %s", r.status, r.raw)
	}
	if status := str(t, r.body, "status"); status != "on_hold" {
		t.Fatalf("status after an over limit confirm = %q, want on_hold (the hold is a committed state, not an error)", status)
	}
	if reason := str(t, r.body, "hold_reason"); reason != "credit_limit" {
		t.Errorf("hold_reason = %q, want credit_limit", reason)
	}
	if ev := eventsFor(t, db, id); len(ev) != 2 || ev[1] != "order.hold" {
		t.Errorf("events = %v, want order.created then order.hold", ev)
	}

	// The release needs the finance roles; dev's admin passes.
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 2})
	if r.status != 200 {
		t.Fatalf("release = %d: %s", r.status, r.raw)
	}
	if status := str(t, r.body, "status"); status != "confirmed" {
		t.Errorf("status after release = %q, want confirmed", status)
	}
	if confirmed := str(t, r.body, "confirmed_at"); confirmed == "" {
		t.Error("confirmed_at missing after the release's confirm")
	}
	if ev := eventsFor(t, db, id); len(ev) != 4 || ev[2] != "order.hold_released" || ev[3] != "order.confirmed" {
		t.Errorf("events = %v, want hold, hold_released, confirmed", ev)
	}

	// A stale balance_due did not hold an order whose documents are within
	// the limit: the check read the documents.
	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET credit_limit = NULL WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	r = f.create()
	r = f.do("POST", "/api/v1/orders/"+str(t, r.body, "id")+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != 200 || str(t, r.body, "status") != "confirmed" {
		t.Errorf("confirm with a stale balance_due = %d %q, want 200 confirmed", r.status, r.body["status"])
	}
}

// eventsWithFrom reads an order's events in order as "type" or
// "type<from_status".
func eventsWithFrom(t *testing.T, db *database.DB, orderID string) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT type, COALESCE(data->>'from_status', '') FROM events_outbox WHERE entity_type = 'order' AND entity_id = $1 ORDER BY position`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ty, from string
		if err := rows.Scan(&ty, &from); err != nil {
			t.Fatal(err)
		}
		if from != "" {
			ty += "<" + from
		}
		out = append(out, ty)
	}
	return out
}

// RULE (ADR 0005 5.2 and section 12): a release writes order.hold_released,
// and order.confirmed only if the order was never confirmed before; a
// manual hold of a confirmed order and its release must not count the
// confirm twice. Every status change event carries from_status.
func TestOrderManualHoldReleaseEvents(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	r := f.create()
	id := str(t, r.body, "id")
	step := func(body map[string]any) {
		t.Helper()
		if r = f.do("POST", "/api/v1/orders/"+id+"/transitions", body); r.status != 200 {
			t.Fatalf("%v = %d: %s", body, r.status, r.raw)
		}
	}
	step(map[string]any{"to": "confirmed", "revision": 1})
	step(map[string]any{"to": "on_hold", "revision": 2, "hold_note": "customer asked to wait"})
	step(map[string]any{"to": "confirmed", "revision": 3})
	want := "[order.created order.confirmed<draft order.hold<confirmed order.hold_released<on_hold]"
	if got := fmt.Sprint(eventsWithFrom(t, db, id)); got != want {
		t.Errorf("events = %s, want %s", got, want)
	}
	step(map[string]any{"to": "draft", "revision": 4})
	if got := eventsWithFrom(t, db, id); got[len(got)-1] != "order.reopened<confirmed" {
		t.Errorf("last event = %s, want order.reopened<confirmed", got[len(got)-1])
	}
}

// RULE (ADR 0005 5.2): the PO guard, the forbidden edges of the transition
// table, the cancel's reason, and the events each transition writes.
func TestOrderTransitionsTable(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()

	// The PO guard.
	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET po_required = TRUE WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	r := f.create()
	id := str(t, r.body, "id")
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != 409 {
		t.Fatalf("confirm without a PO = %d: %s", r.status, r.raw)
	}
	_, _, details := errorOf(t, r)
	if len(details) != 1 || details[0]["code"] != "po_required" {
		t.Errorf("details = %v, want po_required", details)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET po_required = FALSE WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}

	// A manual hold carries its note; the hold keeps the order out of the
	// confirm's way until released.
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != 200 || str(t, r.body, "status") != "confirmed" {
		t.Fatalf("confirm = %d %q: %s", r.status, r.body["status"], r.raw)
	}
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "on_hold", "revision": 2, "hold_note": "customer asked to wait"})
	if r.status != 200 || str(t, r.body, "status") != "on_hold" {
		t.Fatalf("manual hold = %d %q: %s", r.status, r.body["status"], r.raw)
	}
	if reason := str(t, r.body, "hold_reason"); reason != "manual" {
		t.Errorf("hold_reason = %q, want manual", reason)
	}
	// A manual hold without its note is a 400.
	other := f.create()
	otherID := str(t, other.body, "id")
	// 5.2: a manual hold is allowed from confirmed or backordered only. A
	// draft was never credit checked, and its release would confirm it
	// unchecked.
	r = f.do("POST", "/api/v1/orders/"+otherID+"/transitions", map[string]any{"to": "on_hold", "revision": 1, "hold_note": "x"})
	if r.status != 409 {
		t.Fatalf("a manual hold on a draft = %d, want 409: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "invalid_state_transition" {
		t.Errorf("code = %q, want invalid_state_transition", code)
	}
	if g := f.do("GET", "/api/v1/orders/"+otherID, nil); str(t, g.body, "status") != "draft" || revision(t, g) != 1 {
		t.Errorf("the refused hold moved the draft: %s", g.raw)
	}
	r = f.do("POST", "/api/v1/orders/"+otherID+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != 200 {
		t.Fatalf("confirm = %d: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/orders/"+otherID+"/transitions", map[string]any{"to": "on_hold", "revision": 2})
	if r.status != 400 {
		t.Errorf("a manual hold without a note = %d, want 400", r.status)
	}

	// The cancel requires its reason.
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "cancelled", "revision": 3})
	if r.status != 400 {
		t.Errorf("a cancel without a reason = %d, want 400", r.status)
	}
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "cancelled", "revision": 3, "reason": "customer moved away"})
	if r.status != 200 || str(t, r.body, "status") != "cancelled" {
		t.Fatalf("cancel = %d %q: %s", r.status, r.body["status"], r.raw)
	}
	// A cancelled order is terminal.
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 4, "reason": "no"})
	if r.status != 409 {
		t.Errorf("a transition out of cancelled = %d, want 409 invalid_state_transition", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "invalid_state_transition" {
		t.Errorf("code = %q, want invalid_state_transition", code)
	}
	// The reopen is refused once the order is billed: confirm it, bill it,
	// then try to walk it back to draft.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO invoices (id, order_id, customer_id, branch_id, status, total_amount, subtotal, tax_amount, due_date, payment_terms)
		VALUES (gen_random_uuid(), $1, $2, (SELECT branch_id FROM orders WHERE id = $1), 'UNPAID', 100, 100, 0, CURRENT_DATE + 30, 'NET30')`,
		str(t, other.body, "id"), f.customerID); err != nil {
		t.Fatal(err)
	}
	r = f.do("POST", "/api/v1/orders/"+str(t, other.body, "id")+"/transitions", map[string]any{"to": "draft", "revision": 2})
	if r.status != 409 {
		t.Errorf("reopen of a billed order = %d, want 409 has_fulfilments", r.status)
	}
	_, _, details = errorOf(t, r)
	if len(details) != 1 || details[0]["code"] != "has_fulfilments" {
		t.Errorf("details = %v, want has_fulfilments", details)
	}
}

// RULE (ADR 0005 3): a delivery order takes its ship-to's rate when set, an
// exempt customer pays nothing, and a branch with no configured rate refuses
// the act.
func TestOrderTaxResolution(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()

	// A ship-to with its own rate, set as the customer's default.
	shipTo := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, tax_rate, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Golden Way', 'Kelbrook', 'BC', 'V0A 1A0', 0.120000, TRUE, TRUE)`,
		shipTo, f.customerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM customer_ship_tos WHERE id = $1`, shipTo) })

	r := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "delivery", "lines": []map[string]any{f.line("10")},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("delivery create = %d: %s", r.status, r.raw)
	}
	if pct := str(t, r.body, "tax_rate_percent"); pct != "12" {
		t.Errorf("tax_rate_percent = %q, want the ship-to's 12", pct)
	}
	if src := str(t, r.body, "tax_source"); src != "ship_to_rate" {
		t.Errorf("tax_source = %q, want ship_to_rate", src)
	}
	if shipToID := str(t, r.body, "ship_to_id"); shipToID != shipTo.String() {
		t.Errorf("ship_to_id = %q, want the customer's default taken", shipToID)
	}

	// An exempt customer pays nothing.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tax_exemptions (id, customer_id, exempt_reason, is_active)
		VALUES (gen_random_uuid(), $1, 'Wholesale', TRUE)`, f.customerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM tax_exemptions WHERE customer_id = $1`, f.customerID) })
	r = f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "delivery", "lines": []map[string]any{f.line("10")},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("exempt create = %d: %s", r.status, r.raw)
	}
	if src := str(t, r.body, "tax_source"); src != "exempt" {
		t.Errorf("tax_source = %q, want exempt (the exemption outranks the rates)", src)
	}
	if exempt := r.body["tax_exempt"].(bool); !exempt {
		t.Error("tax_exempt = false, want true")
	}
	if tax := num(t, r.body, "tax_cents"); tax != 0 {
		t.Errorf("tax = %d, want 0", tax)
	}
	_, _ = db.Pool.Exec(ctx, `DELETE FROM tax_exemptions WHERE customer_id = $1`, f.customerID)

	// A branch with no configured rate refuses the act.
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = NULL WHERE id = `+branch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = $1 WHERE id = `+branch, f.branchRate)
	})
	r = f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup", "lines": []map[string]any{f.line("10")},
	})
	if r.status != 409 {
		t.Fatalf("create with no configured rate = %d: %s", r.status, r.raw)
	}
	_, _, details := errorOf(t, r)
	if len(details) != 1 || details[0]["code"] != "tax_rate_not_configured" {
		t.Errorf("details = %v, want tax_rate_not_configured", details)
	}
}

// RULE (ADR 0001 section 9): the same create twice with one idempotency key
// returns the first response and makes one row and one event; the same key
// with another body is 422.
func TestOrderIdempotency(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	body := f.createBody(f.line("4"))
	key := "wire-order-" + uuid.NewString()[:8]
	r1 := f.do("POST", "/api/v1/orders", body, "Idempotency-Key", key)
	if r1.status != 201 {
		t.Fatalf("first create = %d: %s", r1.status, r1.raw)
	}
	r2 := f.do("POST", "/api/v1/orders", body, "Idempotency-Key", key)
	if r2.status != 201 || r2.header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay = %d (replayed %q), want the stored 201 marked replayed", r2.status, r2.header.Get("Idempotency-Replayed"))
	}
	if id1, id2 := str(t, r1.body, "id"), str(t, r2.body, "id"); id1 != id2 {
		t.Errorf("replay created a second order: %s then %s", id1, id2)
	}
	var n int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM orders WHERE customer_id = $1`, f.customerID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d orders after an idempotent replay, want 1", n)
	}
	if ev := eventsFor(t, db, str(t, r1.body, "id")); len(ev) != 1 {
		t.Errorf("events = %v, want one order.created", ev)
	}
	other := f.createBody(f.line("5"))
	r3 := f.do("POST", "/api/v1/orders", other, "Idempotency-Key", key)
	if r3.status != 422 {
		t.Errorf("key reuse with another body = %d, want 422", r3.status)
	}
}

var _ = fmt.Sprintf
var _ = httpx.ParseInclude

// RULE (review P3-13): quote_id is not accepted on POST /orders. A quote's
// order is created by POST /quotes/{id}/convert; accepting any quote_id on
// create left a manual order linked to a quote, which then blocked the real
// convert with already_converted.
func TestOrderCreateRefusesAQuoteID(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	body := f.createBody()
	body["quote_id"] = uuid.NewString()
	r := f.do("POST", "/api/v1/orders", body)
	if r.status != 400 {
		t.Fatalf("create with a quote_id = %d, want 400: %s", r.status, r.raw)
	}
	if _, _, details := errorOf(t, r); len(details) != 1 || details[0]["field"] != "quote_id" {
		t.Errorf("details = %v, want a single 400 on quote_id", details)
	}
	var n int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE customer_id = $1`, f.customerID).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d orders written by the refused create (%v)", n, err)
	}
}

// RULE (review P3-9): a discount 400 names the line the REQUEST sent, not
// the post-explosion position: a kit that precedes the line adds component
// lines in front of it.
func TestOrderDiscountErrorNamesTheRequestLine(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()
	kit, comp := uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price, is_kit, taxable)
		VALUES ($1, $2, 'A fence section kit', 'EA', 100.00, TRUE, TRUE)`, kit, "WIRE-KIT-"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, 'A fence post', 'EA', 12.50)`, comp, "WIRE-POST-"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position)
		VALUES ($1, $2, 4, 0)`, kit, comp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_kit_components WHERE kit_product_id = $1`, kit)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id IN ($1, $2)`, kit, comp)
	})
	r := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup",
		"lines": []map[string]any{
			{"product_id": kit.String(), "quantity": "1"},
			{"product_id": f.productID.String(), "quantity": "1", "discount_cents": 99999, "discount_reason": "too much"},
		},
	})
	if r.status != 400 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	if _, _, details := errorOf(t, r); len(details) == 0 || details[0]["field"] != "lines[1].discount_cents" {
		t.Errorf("details = %v, want lines[1].discount_cents (the request's index, not the exploded position 2)", details)
	}
}

// roleClaims stands in for authentication: X-Test-Role becomes the claims a
// JWT would carry.
func roleClaims(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role := r.Header.Get("X-Test-Role"); role != "" {
			claims := &middleware.UserClaims{Role: role, Roles: []string{role}}
			claims.Subject = r.Header.Get("X-Test-Sub")
			r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, claims))
		}
		next.ServeHTTP(w, r)
	})
}

// RULE (ADR 0005 5.2): releasing a hold is the admin, owner and finance
// roles' act. The check runs under the order's lock inside the transition
// (review P3-12), not in the handler before it: a sales caller is refused
// 403 and the order stays held on its revision with no release event.
func TestOrderReleaseNeedsAFinanceRole(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	if _, err := db.Pool.Exec(context.Background(), `UPDATE customers SET credit_limit = 50.00 WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	svc := order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	mux := http.NewServeMux()
	order.NewHandler(svc).RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(roleClaims(middleware.NewBranchMiddleware(db).Handler(mux))))
	t.Cleanup(f.srv.Close)

	r := f.create()
	id := str(t, r.body, "id")
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != 200 || str(t, r.body, "status") != "on_hold" {
		t.Fatalf("credit hold = %d %q: %s", r.status, r.body["status"], r.raw)
	}
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 2}, "X-Test-Role", "sales")
	if r.status != http.StatusForbidden {
		t.Fatalf("release by a sales role = %d, want 403: %s", r.status, r.raw)
	}
	g := f.do("GET", "/api/v1/orders/"+id, nil)
	if str(t, g.body, "status") != "on_hold" || revision(t, g) != 2 {
		t.Errorf("the refused release moved the order: %s", g.raw)
	}
	if ev := eventsFor(t, db, id); len(ev) != 2 {
		t.Errorf("events = %v, want only created and hold", ev)
	}
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 2}, "X-Test-Role", "finance")
	if r.status != 200 || str(t, r.body, "status") != "confirmed" {
		t.Errorf("release by finance = %d %q: %s", r.status, r.body["status"], r.raw)
	}
}
