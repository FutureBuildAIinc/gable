// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The order routes and the quote convert behind the branch wall, through
// serve's own mount methods and the real BranchMiddleware (ADR 0007 section
// 2.3): the convert holds the quote it addresses to the caller's wall, the
// order it creates takes the quote's branch and that branch's tax rate, and
// the order create holds the body's branch_id to the caller's wall.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type ordersWall struct {
	*wallFixture
	quoteSvc         *quote.Service
	orderSvc         *order.Service
	customer         uuid.UUID
	quoteA, quoteB   uuid.UUID
	rateA, rateB     string
	defaultBranchSQL string
}

func newOrdersWall(t *testing.T, db *database.DB) *ordersWall {
	t.Helper()
	testutil.LockOutboxTables(t)
	ctx := context.Background()
	f := newWallFixture(t, db, true)
	w := &ordersWall{wallFixture: f, customer: uuid.New()}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'wall order cust', $2, $3)`,
		w.customer, "WLORD-"+w.customer.String()[:8], f.branchA); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2), ($1, $3)`,
		w.customer, f.branchA, f.branchB); err != nil {
		t.Fatalf("seed customer branches: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.07 WHERE id = $1`, f.branchA); err != nil {
		t.Fatalf("seed branch A rate: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.05 WHERE id = $1`, f.branchB); err != nil {
		t.Fatalf("seed branch B rate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id IN (SELECT id FROM orders WHERE customer_id = $1)`, w.customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, w.customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1 AND id NOT IN ($2, $3)`, w.customer, f.orderA, f.orderB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, w.customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quote_lines WHERE quote_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, w.customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, w.customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = $1`, w.customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, w.customer)
	})

	w.orderSvc = order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	w.quoteSvc = quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithOrderCreator(w.orderSvc)
	mux := http.NewServeMux()
	wall := newBranchWall(db)
	wall.quotes(mux, w.quoteSvc)
	wall.orders(mux, w.orderSvc)
	srv := httptest.NewServer(asRole(mux))
	t.Cleanup(srv.Close)
	w.wallFixture.srv = srv

	w.quoteA = w.seedQuote(t, f.branchA)
	w.quoteB = w.seedQuote(t, f.branchB)
	return w
}

func (w *ordersWall) seedQuote(t *testing.T, branch uuid.UUID) uuid.UUID {
	t.Helper()
	pid := w.productID
	q, err := w.quoteSvc.Create(branchctx.WithSystem(context.Background()), &quote.Draft{
		CustomerID: w.customer, DeliveryType: quote.DeliveryPickup, Source: "manual", BranchID: &branch,
		Lines: []quote.DraftLine{{
			ProductID: &pid, SKU: "wall", Description: "x", Quantity: 10 * 10000, UOM: "PCS",
			PriceUOM: "PCS", UOMQty: 10000, PriceUOMQty: 10000, UnitPrice: 55000,
		}},
	})
	if err != nil {
		t.Fatalf("seed quote: %v", err)
	}
	return q.ID
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

func errorField(t *testing.T, body []byte) string {
	t.Helper()
	e, _ := decode(t, body)["error"].(map[string]any)
	ds, _ := e["details"].([]any)
	if len(ds) == 0 {
		return ""
	}
	field, _ := ds[0].(map[string]any)["field"].(string)
	return field
}

// RULE (ADR 0007 2.3): a branch A user converting a branch B quote is
// refused 403 naming id, with a header for A or with none; the quote stays
// draft with no order. The same user converts the branch A quote, and an
// administrator converts the branch B quote into an order in branch B taxed
// at B's rate.
func TestOrdersWall_ConvertHoldsTheQuoteToTheCallersWall(t *testing.T) {
	db := testutil.RequireDB(t)
	w := newOrdersWall(t, db)
	A := w.branchA.String()
	ctx := context.Background()

	convert := func(quoteID uuid.UUID, role, sub, header string) (int, []byte) {
		req, err := http.NewRequest("POST", w.srv.URL+"/api/v1/quotes/"+quoteID.String()+"/convert", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("If-Match", `"1"`)
		req.Header.Set("X-Test-Role", role)
		req.Header.Set("X-Test-Sub", sub)
		if header != "" {
			req.Header.Set("X-Branch-Id", header)
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

	// No header: the caller's grants hold it. This is the hole: the repository
	// filter only fires for a caller that carries a context branch.
	status, body := convert(w.quoteB, "sales", "u-a", "")
	if status != http.StatusForbidden || errorField(t, body) != "id" {
		t.Fatalf("foreign quote, no header: %d %s, want 403 naming id", status, body)
	}
	// A header for the caller's own branch: the foreign quote is not visible.
	if status, body = convert(w.quoteB, "sales", "u-a", A); status != http.StatusNotFound {
		t.Fatalf("foreign quote, header A: %d %s, want 404", status, body)
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE quote_id = $1`, w.quoteB).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d orders exist for the refused quote (%v)", n, err)
	}
	var st string
	if err := db.Pool.QueryRow(ctx, `SELECT state::text FROM quotes WHERE id = $1`, w.quoteB).Scan(&st); err != nil || st != "DRAFT" {
		t.Fatalf("foreign quote status = %q (%v), want still DRAFT", st, err)
	}

	// Own branch passes, and the order sits in A at A's rate.
	status, body = convert(w.quoteA, "sales", "u-a", A)
	if status != http.StatusCreated {
		t.Fatalf("own quote: %d %s, want 201", status, body)
	}
	o := decode(t, body)
	if o["branch_id"] != A || o["tax_rate_percent"] != "7" {
		t.Errorf("own order: branch %v rate %v, want %s at 7", o["branch_id"], o["tax_rate_percent"], A)
	}

	// An administrator across branches converts B's quote: B's branch, B's rate.
	status, body = convert(w.quoteB, "admin", "boss", "")
	if status != http.StatusCreated {
		t.Fatalf("admin, foreign quote: %d %s, want 201", status, body)
	}
	o = decode(t, body)
	if o["branch_id"] != w.branchB.String() || o["tax_rate_percent"] != "5" || o["tax_cents"] != float64(275) {
		t.Errorf("admin order: branch %v rate %v tax %v, want %s at 5 percent and 275 cents", o["branch_id"], o["tax_rate_percent"], o["tax_cents"], w.branchB)
	}
}

// RULE (ADR 0007 2.3): POST /api/v1/orders holds the body's branch_id to the
// caller's wall: a bound user, and a user with grants but no header, are
// refused 403 naming branch_id for a branch they may not target.
func TestOrdersWall_CreatePayloadBranchRule(t *testing.T) {
	db := testutil.RequireDB(t)
	w := newOrdersWall(t, db)
	A, B := w.branchA.String(), w.branchB.String()

	body := func(branch string) string {
		return fmt.Sprintf(`{"branch_id":%q,"customer_id":%q,"delivery_type":"pickup","lines":[{"product_id":%q,"quantity":"1"}]}`, branch, w.customer, w.productID)
	}
	for _, c := range []struct {
		name, branch, role, sub, header string
		want                            int
	}{
		{"bound user, foreign payload", B, "sales", "u-a", A, http.StatusForbidden},
		{"granted user, foreign payload, no header", B, "sales", "u-a", "", http.StatusForbidden},
		{"bound user, own payload", A, "sales", "u-a", A, http.StatusCreated},
		{"granted user, own payload, no header", A, "sales", "u-a", "", http.StatusCreated},
		{"admin, foreign payload", B, "admin", "boss", "", http.StatusCreated},
	} {
		status, resp := w.callBody(t, "POST", "/api/v1/orders", body(c.branch), c.role, c.sub, c.header)
		if status != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, status, resp, c.want)
			continue
		}
		if c.want == http.StatusForbidden && errorField(t, resp) != "branch_id" {
			t.Errorf("%s: %s, want the error naming branch_id", c.name, resp)
		}
		if c.want == http.StatusCreated && !strings.Contains(string(resp), `"branch_id":"`+c.branch+`"`) {
			t.Errorf("%s: order not at branch %s: %s", c.name, c.branch, resp)
		}
	}
}
