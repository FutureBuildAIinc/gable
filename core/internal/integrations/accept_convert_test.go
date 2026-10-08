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
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
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
