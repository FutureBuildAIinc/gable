// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"encoding/json"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// RULE (ruling on R1-15): accept-and-convert refuses a quote with a line an
// order cannot carry (a price per another unit) before it creates anything,
// and leaves the quote as it was. The order service is nil here: reaching it
// would panic, which is the proof nothing was created.
func TestAcceptAndConvert_ConvertsThePairAndConfirms(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	customerID, productID := uuid.New(), uuid.New()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Seam Test Co', $2, `+branch+`)`, customerID, "SEAM-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5)`, productID, "SEAM-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	// The 2x4x8's MBF row (1, 187.5): the product prices per MBF (ADR 0006
	// section 3.2's worked set; the stocking row came with the insert).
	if _, err := db.Pool.Exec(ctx, `INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
		VALUES ($1, 'MBF', 1, 187.5, TRUE, TRUE, TRUE)`, productID); err != nil {
		t.Fatalf("seed unit set: %v", err)
	}
	// A migrated, unseeded database has no branch tax rate; the convert's
	// order needs one.
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.088750
		WHERE id = (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`); err != nil {
		t.Fatalf("seed branch rate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id IN (SELECT id FROM orders WHERE customer_id = $1)`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
	})

	orderSvc := order.NewService(order.NewRepository(db)).WithTxRunner(db)
	quoteSvc := quote.NewService(quote.NewRepository(db)).WithTxRunner(db).WithOrderCreator(orderSvc)
	// 187.5 PCS at 500.00 per MBF: the line the seam refused before cycle 2.
	q, err := quoteSvc.Create(ctx, &quote.Draft{
		CustomerID: customerID, DeliveryType: quote.DeliveryPickup, Source: "manual",
		Lines: []quote.DraftLine{{
			ProductID: &productID, SKU: "S", Description: "D", Quantity: 1875000, UOM: "PCS",
			PriceUOM: "MBF", UOMQty: httpx.Quantity(1875000), PriceUOMQty: 10000, UnitPrice: 5000000,
		}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	h := NewHandler(db, nil, quoteSvc, orderSvc, nil, nil, "k")
	req := httptest.NewRequest("POST", "/api/integration/quotes/"+q.ID.String()+"/accept-and-convert", nil)
	req.SetPathValue("id", q.ID.String())
	rec := httptest.NewRecorder()
	h.AcceptAndConvertQuote(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("accept-and-convert = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		ID      string `json:"id"`
		QuoteID string `json:"quote_id"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "CONFIRMED" {
		t.Errorf("seam status = %q, want CONFIRMED (the seam's own vocabulary)", body.Status)
	}
	// The order carried the pair without loss.
	o, err := orderSvc.GetOrder(ctx, uuid.MustParse(body.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Lines) != 1 {
		t.Fatalf("the order carries %d lines", len(o.Lines))
	}
	l := o.Lines[0]
	if l.UOM == nil || l.PriceUOM == nil || *l.PriceUOM != "MBF" || *l.UOMQty != 1875000 || *l.UnitPrice != 5000000 {
		t.Errorf("the MBF line crossed as %+v", l)
	}
	if l.LineTotal == nil || *l.LineTotal != 50000 {
		t.Errorf("the MBF line's extension = %v, want 50000 cents", l.LineTotal)
	}
}

// A stocked line sold in another unit than its product's stocking unit is
// still refused (unit_not_stock_unit, until cycle 3), and the refusal leaves
// the quote as it was with no order.
func TestAcceptAndConvert_RefusesANonStockUnit(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	customerID, productID := uuid.New(), uuid.New()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Seam Test Co', $2, `+branch+`)`, customerID, "SEAM-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5)`, productID, "SEAM-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	// The 2x4x8's MBF row (1, 187.5): the product prices per MBF (ADR 0006
	// section 3.2's worked set; the stocking row came with the insert).
	if _, err := db.Pool.Exec(ctx, `INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
		VALUES ($1, 'MBF', 1, 187.5, TRUE, TRUE, TRUE)`, productID); err != nil {
		t.Fatalf("seed unit set: %v", err)
	}
	// A migrated, unseeded database has no branch tax rate; the convert's
	// order needs one.
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.088750
		WHERE id = (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`); err != nil {
		t.Fatalf("seed branch rate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
	})

	orderSvc := order.NewService(order.NewRepository(db)).WithTxRunner(db)
	quoteSvc := quote.NewService(quote.NewRepository(db)).WithTxRunner(db).WithOrderCreator(orderSvc)
	q, err := quoteSvc.Create(ctx, &quote.Draft{
		CustomerID: customerID, DeliveryType: quote.DeliveryPickup, Source: "manual",
		Lines: []quote.DraftLine{{
			ProductID: &productID, SKU: "S", Description: "D", Quantity: 10000, UOM: "MBF",
			PriceUOM: "MBF", UOMQty: 10000, PriceUOMQty: 10000, UnitPrice: 5000000,
		}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	h := NewHandler(db, nil, quoteSvc, orderSvc, nil, nil, "k")
	req := httptest.NewRequest("POST", "/api/integration/quotes/"+q.ID.String()+"/accept-and-convert", nil)
	req.SetPathValue("id", q.ID.String())
	rec := httptest.NewRecorder()
	h.AcceptAndConvertQuote(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("accept-and-convert = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "stocking unit") {
		t.Errorf("the refusal does not name the rule: %s", rec.Body.String())
	}
	got, err := quoteSvc.GetQuote(ctx, q.ID)
	if err != nil || got.Status != quote.QuoteStateDraft || got.Revision != 1 {
		t.Errorf("quote after the refusal = %v rev %d (%v), want draft rev 1", got.Status, got.Revision, err)
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE quote_id = $1`, q.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d orders exist for the refused quote", n)
	}
}

// seamWorld seeds a customer, a product and a second branch at a 5 percent
// tax rate beside the default branch at 10 percent, and answers the
// services the seam wires.
type seamWorld struct {
	db         *database.DB
	orderSvc   *order.Service
	quoteSvc   *quote.Service
	customerID uuid.UUID
	productID  uuid.UUID
	other      uuid.UUID
}

func newSeamWorld(t *testing.T) *seamWorld {
	t.Helper()
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	w := &seamWorld{db: db, customerID: uuid.New(), productID: uuid.New(), other: uuid.New()}
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Seam Test Co', $2, `+branch+`)`, w.customerID, "SEAM-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5)`, w.productID, "SEAM-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, default_tax_rate) VALUES ($1, 'BRANCH', $2, 0.05)`,
		w.other, "SM-"+w.other.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	var oldRate *string
	if err := db.Pool.QueryRow(ctx, `SELECT default_tax_rate::text FROM locations WHERE id = `+branch).Scan(&oldRate); err != nil {
		t.Fatalf("read default branch rate: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.10 WHERE id = `+branch); err != nil {
		t.Fatalf("seed default branch rate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id IN (SELECT id FROM orders WHERE customer_id = $1)`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, w.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, w.other)
		_, _ = db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = $1 WHERE id = `+branch, oldRate)
	})
	w.orderSvc = order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	w.quoteSvc = quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithOrderCreator(w.orderSvc)
	return w
}

// quoteAt creates a quote of qty PCS at 5.50 in the branch.
func (w *seamWorld) quoteAt(t *testing.T, branch *uuid.UUID, qty httpx.Quantity) *quote.Quote {
	t.Helper()
	q, err := w.quoteSvc.Create(branchctx.WithSystem(context.Background()), &quote.Draft{
		CustomerID: w.customerID, DeliveryType: quote.DeliveryPickup, Source: "manual", BranchID: branch,
		Lines: []quote.DraftLine{{
			ProductID: &w.productID, SKU: "S", Description: "D", Quantity: qty, UOM: "PCS",
			PriceUOM: "PCS", UOMQty: 10000, PriceUOMQty: 10000, UnitPrice: 55000,
		}},
	})
	if err != nil {
		t.Fatalf("create quote: %v", err)
	}
	return q
}

func (w *seamWorld) acceptAndConvert(q *quote.Quote) *httptest.ResponseRecorder {
	h := NewHandler(w.db, nil, w.quoteSvc, w.orderSvc, nil, nil, "k")
	req := httptest.NewRequest("POST", "/api/integration/quotes/"+q.ID.String()+"/accept-and-convert", nil)
	req.SetPathValue("id", q.ID.String())
	rec := httptest.NewRecorder()
	h.AcceptAndConvertQuote(rec, req)
	return rec
}

// RULE (ADR 0005 5.8's table): the seam's order takes the QUOTE's branch and
// its tax rate. The seam has no context branch, so before the fix every
// seam convert landed in the default branch at the default rate.
func TestAcceptAndConvert_OrderTakesTheQuotesBranch(t *testing.T) {
	w := newSeamWorld(t)
	q := w.quoteAt(t, &w.other, 10*10000)
	rec := w.acceptAndConvert(q)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept-and-convert = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	o, err := w.orderSvc.GetOrder(branchctx.WithSystem(context.Background()), uuid.MustParse(body.ID))
	if err != nil {
		t.Fatal(err)
	}
	if o.BranchID != w.other {
		t.Errorf("order branch = %s, want the quote's %s", o.BranchID, w.other)
	}
	if o.TaxRatePercent == nil || *o.TaxRatePercent != "5" || o.TaxCents != 275 {
		t.Errorf("order tax = %v at %v, want 275 cents at 5 percent", o.TaxCents, o.TaxRatePercent)
	}
}

// RULE (ADR 0005 5.8): a confirm that lands on_hold answers the seam's
// frozen 409, with today's body byte for byte, while the order stays held
// with its order.hold event. The seam keeps its 409; the product keeps the
// hold.
func TestAcceptAndConvert_OverLimitKeepsTheSeam409AndTheHold(t *testing.T) {
	w := newSeamWorld(t)
	ctx := context.Background()
	// 10 PCS at 5.50 is over a 10.00 limit.
	if _, err := w.db.Pool.Exec(ctx, `UPDATE customers SET credit_limit = 10.00 WHERE id = $1`, w.customerID); err != nil {
		t.Fatal(err)
	}
	q := w.quoteAt(t, nil, 10*10000)
	rec := w.acceptAndConvert(q)
	var orderID uuid.UUID
	if err := w.db.Pool.QueryRow(ctx, `SELECT id FROM orders WHERE quote_id = $1`, q.ID).Scan(&orderID); err != nil {
		t.Fatalf("the held order was not kept: %v", err)
	}
	want := `{"error":"order ` + orderID.String() + ` created from quote but could not be confirmed: credit limit exceeded: order placed ON HOLD"}` + "\n"
	if rec.Code != http.StatusConflict || rec.Body.String() != want {
		t.Fatalf("over limit = %d %q, want 409 %q", rec.Code, rec.Body.String(), want)
	}
	var status string
	if err := w.db.Pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, orderID).Scan(&status); err != nil || status != "ON_HOLD" {
		t.Errorf("order status = %q (%v), want ON_HOLD", status, err)
	}
	var holds int
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM events_outbox WHERE entity_type = 'order' AND entity_id = $1 AND type = 'order.hold'`, orderID).Scan(&holds); err != nil || holds != 1 {
		t.Errorf("order.hold events = %d (%v), want 1", holds, err)
	}
}

// RULE: an unknown quote id answers 404 (the base answered 500 "failed to
// get quote"; CONTRACT-CHANGES).
func TestAcceptAndConvert_UnknownQuoteIs404(t *testing.T) {
	w := newSeamWorld(t)
	h := NewHandler(w.db, nil, w.quoteSvc, w.orderSvc, nil, nil, "k")
	id := uuid.New()
	req := httptest.NewRequest("POST", "/api/integration/quotes/"+id.String()+"/accept-and-convert", nil)
	req.SetPathValue("id", id.String())
	rec := httptest.NewRecorder()
	h.AcceptAndConvertQuote(rec, req)
	if rec.Code != http.StatusNotFound || rec.Body.String() != `{"error":"quote not found"}`+"\n" {
		t.Fatalf("unknown quote = %d %q, want 404 quote not found", rec.Code, rec.Body.String())
	}
}

// RULE (ADR 0005 5.8 and 14.2): an order that lands backordered reports
// CONFIRMED in the seam's status, the vocabulary its callers know; the order
// itself is backordered with the rest on back order.
func TestAcceptAndConvert_ShortOrderReportsConfirmed(t *testing.T) {
	w := newSeamWorld(t)
	ctx := context.Background()
	w.orderSvc.WithInventory(inventory.NewService(inventory.NewRepository(w.db)))
	yard := uuid.New()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := w.db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, `+branch+`, `+branch+`)`, yard, "SH-"+yard.String()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Pool.Exec(ctx, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 4, 0)`, w.productID, yard); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.db.Pool.Exec(context.Background(), `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, w.customerID)
		_, _ = w.db.Pool.Exec(context.Background(), `DELETE FROM inventory WHERE location_id = $1`, yard)
		_, _ = w.db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, yard)
	})
	// 10 PCS ordered, 4 on hand.
	q := w.quoteAt(t, nil, 10*10000)
	rec := w.acceptAndConvert(q)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept-and-convert of a short order = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "CONFIRMED" {
		t.Errorf("seam status = %q, want CONFIRMED for a back ordered order", body.Status)
	}
	var status, qty string
	if err := w.db.Pool.QueryRow(ctx, `SELECT o.status, l.quantity_allocated::text || '/' || l.quantity_backordered::text FROM orders o JOIN order_lines l ON l.order_id = o.id WHERE o.id = $1`, body.ID).Scan(&status, &qty); err != nil {
		t.Fatal(err)
	}
	if status != "BACKORDERED" || qty != "4.0000/6.0000" {
		t.Errorf("order = %s %s, want BACKORDERED 4.0000/6.0000", status, qty)
	}
}
