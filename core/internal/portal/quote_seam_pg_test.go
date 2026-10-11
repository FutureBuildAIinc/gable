// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

// The portal's quote decision against the branch wall as quote.Service now
// draws it: the record check fails closed on a branch-free context, and the
// portal is a context-free caller. It has proved the customer owns the quote
// before it calls the lifecycle, so it calls as a marked system caller
// (branchctx.WithSystem) and accept still works end to end with no branch
// context at all.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

func TestAcceptQuote_DecidesAsAMarkedSystemCaller(t *testing.T) {
	// The quote service below writes quote events into events_outbox, which
	// the events and outbox packages' tests truncate under this lock; take it
	// so this test's writes and their truncates serialise across packages.
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()

	customerID, productID := uuid.New(), uuid.New()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Seam Co', $2, `+branch+`)`, customerID, "PSEAM-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5)`, productID, "PSEAM-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
	})

	quoteSvc := quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithBranchGuard(middleware.NewBranchGuard(db))
	q, err := quoteSvc.Create(ctx, &quote.Draft{
		CustomerID:   customerID,
		DeliveryType: quote.DeliveryPickup,
		Source:       "manual",
		Lines: []quote.DraftLine{{
			ProductID: &productID, SKU: "S", Description: "D",
			Quantity: 10 * 10000, UOM: "PCS", PriceUOM: "PCS", UOMQty: 10000, PriceUOMQty: 10000,
			UnitPrice: 55000,
		}},
	})
	if err != nil {
		t.Fatalf("create quote: %v", err)
	}
	// The dealer prices and sends it, as the ERP does.
	if err := quoteSvc.UpdateState(branchctx.WithSystem(ctx), q.ID, quote.QuoteStateSent); err != nil {
		t.Fatalf("send quote: %v", err)
	}

	portalSvc := NewService(NewRepository(db), "seam-test-secret", slog.Default(), nil, nil, nil, nil, nil).
		WithQuoteService(quoteSvc)
	out, err := portalSvc.AcceptQuote(ctx, q.ID, customerID)
	if err != nil {
		t.Fatalf("AcceptQuote with no branch context: %v, want the accepted quote", err)
	}
	if out.Status != QuoteStatusAccepted {
		t.Fatalf("AcceptQuote status = %s, want %s", out.Status, QuoteStatusAccepted)
	}
}
