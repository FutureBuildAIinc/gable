// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// RULE (ruling on R1-15): accept-and-convert refuses a quote with a line an
// order cannot carry (a price per another unit) before it creates anything,
// and leaves the quote as it was. The order service is nil here: reaching it
// would panic, which is the proof nothing was created.
func TestAcceptAndConvert_RefusesALineOrdersCannotCarry(t *testing.T) {
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

	quoteSvc := quote.NewService(quote.NewRepository(db)).WithTxRunner(db)
	q, err := quoteSvc.Create(ctx, &quote.Draft{
		CustomerID: customerID, DeliveryType: quote.DeliveryPickup, Source: "manual",
		Lines: []quote.DraftLine{{
			ProductID: &productID, SKU: "S", Description: "D", Quantity: 10 * 10000, UOM: "PCS",
			PriceUOM: "MBF", UOMQty: httpx.Quantity(1875000), PriceUOMQty: 10000, UnitPrice: 5000000,
		}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	h := NewHandler(db, nil, quoteSvc, nil, nil, nil, "k")
	req := httptest.NewRequest("POST", "/api/integration/quotes/"+q.ID.String()+"/accept-and-convert", nil)
	req.SetPathValue("id", q.ID.String())
	rec := httptest.NewRecorder()
	h.AcceptAndConvertQuote(rec, req)

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "lines[0]") {
		t.Fatalf("accept-and-convert = %d %s, want 409 naming lines[0]", rec.Code, rec.Body.String())
	}
	got, err := quoteSvc.GetQuote(ctx, q.ID)
	if err != nil || got.Status != quote.QuoteStateDraft || got.Revision != 1 {
		t.Errorf("quote after the refusal = %v rev %d (%v), want draft rev 1", got.Status, got.Revision, err)
	}
}
