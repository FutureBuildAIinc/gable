// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// The kit on a quote (row QE-05, slice QE-05-a): a kit line explodes into
// its components through the one shared explosion (ADR 0005 section 2.6),
// the quote's margin uses the components' cost, a draft edit re-explodes,
// and the converted order carries the same kit and component lines the
// order path makes, so the quote's margin and the order's agree to the cent.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// kitFixture is the wire fixture plus a kit product of two components. The
// components carry an average cost each, so a kit priced below their sum
// shows a negative margin: the dealer's warning that a flat kit line hid.
type kitFixture struct {
	*fixture
	kitID, compA, compB uuid.UUID
	kitSKU              string
}

// newKitFixture seeds a kit priced as a whole and two costed components:
// four of A at 4.00 and one of B at 6.00, so one kit costs 22.00.
func newKitFixture(t *testing.T) *kitFixture {
	t.Helper()
	f := newFixture(t, testutil.RequireDB(t))
	ctx := context.Background()
	k := &kitFixture{fixture: f, kitID: uuid.New(), compA: uuid.New(), compB: uuid.New(),
		kitSKU: "KIT-" + uuid.NewString()[:8]}
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO products (id, sku, description, uom_primary, base_price, is_kit, taxable, average_unit_cost)
			VALUES ($1, $2, 'A deck bracket kit', 'EA', 10.00, TRUE, TRUE, 0)`, []any{k.kitID, k.kitSKU}},
		{`INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost)
			VALUES ($1, $2, 'A bracket', 'EA', 4.00, 4.00)`, []any{k.compA, "KITCOMP-A-" + uuid.NewString()[:6]}},
		{`INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost)
			VALUES ($1, $2, 'A post anchor', 'EA', 6.00, 6.00)`, []any{k.compB, "KITCOMP-B-" + uuid.NewString()[:6]}},
		{`INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position)
			VALUES ($1, $2, 4, 0)`, []any{k.kitID, k.compA}},
		{`INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position)
			VALUES ($1, $2, 1, 1)`, []any{k.kitID, k.compB}},
	}
	for _, s := range stmts {
		if _, err := f.db.Pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed kit fixture: %v", err)
		}
	}
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM product_kit_components WHERE kit_product_id = $1`, k.kitID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM products WHERE id IN ($1, $2, $3)`, k.kitID, k.compA, k.compB)
	})
	return k
}

// kitLine is one kit line on a create body: `qty` kits at the given unit
// price (ten thousandths), priced as a whole.
func (k *kitFixture) kitLine(qty string, unitPrice int64) map[string]any {
	return map[string]any{
		"product_id": k.kitID.String(), "sku": k.kitSKU, "description": "A deck bracket kit",
		"quantity": qty, "uom": "EA", "unit_price_ten_thousandths": unitPrice,
	}
}

// RULE (ADR 0005 section 2.6): a quote line that names a kit product stores
// the kit line, priced as a whole, followed by one component line per
// component at quantity kit x per kit, unit price zero, extension zero.
func TestQE05_QuoteExplodesKit(t *testing.T) {
	testutil.LockOutboxTables(t)
	k := newKitFixture(t)

	created := k.do("POST", "/api/v1/quotes", k.createBody(k.kitLine("2", 250000)))
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	lines := created.body["lines"].([]any)
	if len(lines) != 3 {
		t.Fatalf("%d lines, want the kit and its two components: %s", len(lines), created.raw)
	}
	kitLine := lines[0].(map[string]any)
	if got := str(t, kitLine, "line_type"); got != "kit" {
		t.Errorf("kit line_type = %q, want kit", got)
	}
	if kitLine["parent_line_id"] != nil {
		t.Errorf("kit parent_line_id = %v, want null", kitLine["parent_line_id"])
	}
	if p := num(t, kitLine, "unit_price_ten_thousandths"); p != 250000 {
		t.Errorf("kit price = %d, want 250000 kept (the kit prices as a whole)", p)
	}
	if tot := num(t, kitLine, "line_total_cents"); tot != 5000 {
		t.Errorf("kit extension = %d, want 5000 (2 kits at 25.00)", tot)
	}
	kitID := str(t, kitLine, "id")
	wantQty := map[string]string{k.compA.String(): "8", k.compB.String(): "2"}
	for _, l := range lines[1:] {
		comp := l.(map[string]any)
		if got := str(t, comp, "line_type"); got != "component" {
			t.Fatalf("component line_type = %q, want component", got)
		}
		if parent := str(t, comp, "parent_line_id"); parent != kitID {
			t.Errorf("component parent = %q, want the kit line %q", parent, kitID)
		}
		product := str(t, comp, "product_id")
		if q := str(t, comp, "quantity"); q != wantQty[product] {
			t.Errorf("component %s quantity = %q, want %q", product, q, wantQty[product])
		}
		if p := num(t, comp, "unit_price_ten_thousandths"); p != 0 {
			t.Errorf("component price = %d, want 0", p)
		}
		if tot := num(t, comp, "line_total_cents"); tot != 0 {
			t.Errorf("component extension = %d, want 0", tot)
		}
		if c := num(t, comp, "unit_cost_ten_thousandths"); c == 0 {
			t.Errorf("component %s carries no cost: the margin needs it", product)
		}
	}
	// The components ride at zero, so the quote's total is the kit alone.
	if tot := num(t, created.body, "total_cents"); tot != 5000 {
		t.Errorf("total = %d, want 5000 (the components are free on the face)", tot)
	}
}

// RULE (QE-05): a quote's margin uses the kit's components' cost, never the
// kit row's own average cost, so a kit quoted below its component cost shows
// a negative margin instead of a healthy one.
func TestQE05_QuoteKitMarginUsesComponentCost(t *testing.T) {
	testutil.LockOutboxTables(t)
	k := newKitFixture(t)
	branchID := k.ownBranch("0")

	// One kit at 10.00 against components worth 22.00 (4 x 4.00 + 1 x 6.00):
	// the margin must read -1200 cents, not the positive figure the kit row's
	// own zero cost would print.
	body := k.createBody(k.kitLine("1", 100000))
	body["branch_id"] = branchID.String()
	created := k.do("POST", "/api/v1/quotes", body)
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	id := str(t, created.body, "id")
	if m := num(t, created.body, "margin_total_cents"); m != -1200 {
		t.Errorf("create margin = %d, want -1200 (10.00 revenue against 22.00 of components)", m)
	}
	got := k.do("GET", "/api/v1/quotes/"+id, nil)
	if m := num(t, got.body, "margin_total_cents"); m != -1200 {
		t.Errorf("read margin = %d, want -1200", m)
	}
}

// RULE (QE-05): converting the quote makes the same kit and component order
// lines the order path makes today, so the quote's margin and the order's
// agree to the cent.
func TestQE05_ConvertedKitMarginMatchesQuote(t *testing.T) {
	testutil.LockOutboxTables(t)
	k := newKitFixture(t)
	branchID := k.ownBranch("0.05")

	// Two kits at 10.00 against components worth 44.00 (2 x (4 x 4.00 + 6.00)):
	// both documents must read the same -2400 cent margin.
	body := k.createBody(k.kitLine("2", 100000))
	body["branch_id"] = branchID.String()
	created := k.do("POST", "/api/v1/quotes", body)
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	id := str(t, created.body, "id")
	want := num(t, created.body, "margin_total_cents")

	converted := k.do("POST", "/api/v1/quotes/"+id+"/convert", nil, "If-Match", `"1"`)
	if converted.status != http.StatusCreated {
		t.Fatalf("convert = %d: %s", converted.status, converted.raw)
	}
	orderID := str(t, converted.body, "id")
	// The order carries the kit line and both components, the same explosion
	// a direct order create makes.
	lines := converted.body["lines"].([]any)
	if len(lines) != 3 {
		t.Fatalf("order carries %d lines, want the kit and its two components", len(lines))
	}
	if got := str(t, lines[0].(map[string]any), "line_type"); got != "kit" {
		t.Errorf("order kit line_type = %q, want kit", got)
	}
	for _, l := range lines[1:] {
		if got := str(t, l.(map[string]any), "line_type"); got != "component" {
			t.Errorf("order component line_type = %q, want component", got)
		}
	}
	order := k.getOrder(t, orderID)
	if m := num(t, order, "total_margin_cents"); m != want {
		t.Errorf("order margin = %d, want the quote's %d", m, want)
	}
	if m := num(t, order, "total_margin_cents"); m != -2400 {
		t.Errorf("order margin = %d, want -2400 (20.00 revenue against 44.00 of components)", m)
	}
}

// getOrder reads the converted order's margin rollup through the order
// module's own read route, on a mux the test mounts for it.
func (k *kitFixture) getOrder(t *testing.T, orderID string) map[string]any {
	t.Helper()
	svc := order.NewService(order.NewRepository(k.db)).
		WithOutbox(outbox.NewWriter(k.db, "")).WithTxRunner(k.db)
	mux := http.NewServeMux()
	order.NewHandler(svc).RegisterRoutes(mux)
	srv := httptest.NewServer(middleware.Idempotency(k.db)(mux))
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/api/v1/orders/" + orderID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("order read = %d: %s", res.StatusCode, raw)
	}
	body := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

// RULE (ADR 0005 section 2.6): the explosion is done at the line's create or
// edit, so editing the kit's quantity on a draft replaces the components the
// old quantity carried, leaving one set, at the new quantities.
func TestQE05_QuoteKitRevisionUpdateReexplodes(t *testing.T) {
	testutil.LockOutboxTables(t)
	k := newKitFixture(t)

	created := k.do("POST", "/api/v1/quotes", k.createBody(k.kitLine("2", 250000)))
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	id := str(t, created.body, "id")
	kitLineID := str(t, created.body["lines"].([]any)[0].(map[string]any), "id")

	updated := k.do("PUT", "/api/v1/quotes/"+id, map[string]any{
		"customer_id": k.customerID.String(), "delivery_type": "pickup", "revision": 1,
		"lines": []map[string]any{{
			"id": kitLineID, "product_id": k.kitID.String(), "sku": k.kitSKU,
			"description": "A deck bracket kit", "quantity": "3", "uom": "EA",
			"unit_price_ten_thousandths": 250000,
		}},
	})
	if updated.status != http.StatusOK {
		t.Fatalf("update = %d: %s", updated.status, updated.raw)
	}
	lines := updated.body["lines"].([]any)
	if len(lines) != 3 {
		t.Fatalf("%d lines after the edit, want the kit and two replaced components", len(lines))
	}
	wantQty := map[string]string{k.compA.String(): "12", k.compB.String(): "3"}
	for _, l := range lines[1:] {
		comp := l.(map[string]any)
		if parent := str(t, comp, "parent_line_id"); parent != kitLineID {
			t.Errorf("replaced component parent = %q, want the kit line kept", parent)
		}
		product := str(t, comp, "product_id")
		if q := str(t, comp, "quantity"); q != wantQty[product] {
			t.Errorf("component %s quantity = %q, want %q (3 kits x per kit)", product, q, wantQty[product])
		}
	}
	if tot := num(t, updated.body, "total_cents"); tot != 7500 {
		t.Errorf("total after edit = %d, want 7500 (3 kits at 25.00)", tot)
	}
	// The stored rows were replaced, not appended: one kit, two components.
	var n int
	if err := k.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM quote_lines WHERE quote_id = $1 AND line_type = 'COMPONENT'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d stored component rows, want 2", n)
	}
}

// The kit path rides the create's transaction at pool size 4 with three
// contenders (QE-COMMON section 3): every concurrent kit quote explodes
// whole, with its own distinct number.
func TestQE05_KitQuoteConcurrency_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	ctx := context.Background()
	kitID, compID := uuid.New(), uuid.New()
	kitSKU := "KIT-" + uuid.NewString()[:8]
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO products (id, sku, description, uom_primary, base_price, is_kit, taxable)
			VALUES ($1, $2, 'A fence section kit', 'EA', 100.00, TRUE, TRUE)`, []any{kitID, kitSKU}},
		{`INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost)
			VALUES ($1, $2, 'A fence post', 'EA', 12.50, 12.50)`, []any{compID, "KITPOST-" + uuid.NewString()[:6]}},
		{`INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position)
			VALUES ($1, $2, 4, 0)`, []any{kitID, compID}},
	} {
		if _, err := db.Pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_kit_components WHERE kit_product_id = $1`, kitID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id IN ($1, $2)`, kitID, compID)
	})

	svc := quote.NewService(quote.NewRepository(db)).WithTxRunner(db)
	kit := kitID
	const contenders, each = 3, 3
	var mu sync.Mutex
	var numbers = map[string]bool{}
	var wg sync.WaitGroup
	errs := make(chan error, contenders*each)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				// The kit line alone: the kit product's own stocking row (EA,
				// from the migration's constraint trigger) resolves its units.
				d := &quote.Draft{
					CustomerID: f.customerID, DeliveryType: quote.DeliveryPickup, Source: "manual",
					Lines: []quote.DraftLine{{
						ProductID: &kit, SKU: kitSKU, Description: "A fence section kit",
						Quantity: httpx.Quantity(40000), UOM: product.UOM_EA,
						PriceUOM: "EA", UOMQty: httpx.Quantity(10000), PriceUOMQty: httpx.Quantity(10000),
						UnitPrice: 1000000,
					}},
				}
				q, err := svc.Create(ctx, d)
				if err != nil {
					var he *httpx.Error
					if errors.As(err, &he) {
						errs <- fmt.Errorf("create: %s: %v", he.Message, he.Details)
					} else {
						errs <- err
					}
					return
				}
				kitLines, comps := 0, 0
				for _, l := range q.Lines {
					switch l.LineType {
					case salesdoc.LineKit:
						kitLines++
					case salesdoc.LineComponent:
						comps++
					}
				}
				mu.Lock()
				numbers[q.Number] = true
				mu.Unlock()
				if kitLines != 1 || comps != 1 {
					errs <- fmt.Errorf("quote %s exploded into %d kit and %d component lines, want 1 and 1", q.Number, kitLines, comps)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(numbers) != contenders*each {
		t.Fatalf("%d quotes created, want %d distinct numbers", len(numbers), contenders*each)
	}
	// Every stored kit quote still holds exactly its two lines: the kit and
	// its component.
	var bad int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT ql.quote_id FROM quote_lines ql
			JOIN quotes q ON q.id = ql.quote_id
			WHERE q.customer_id = $1
			  AND ql.quote_id IN (SELECT quote_id FROM quote_lines WHERE product_id = $2)
			GROUP BY ql.quote_id HAVING count(*) <> 2
		) s`, f.customerID, kitID).Scan(&bad); err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Errorf("%d kit quotes hold a wrong line count, want 0", bad)
	}
}
