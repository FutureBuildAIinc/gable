// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

// QE-01-a: the portal's decide and convert agree with the quote module's one
// expiry refusal: a quote past its Valid until is refused with the ruling's
// words in the portal's own refusal shape, on both paths.

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

var qe01WordsRe = regexp.MustCompile(
	`^This quote expired on [A-Z][a-z]{2} \d{1,2}, \d{4}: revise it with a new valid until date to send or convert it$`)

func TestQE01_ExpiredPortalDecideAndConvertRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()

	customerID, productID := uuid.New(), uuid.New()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'QE Portal Co', $2, `+branch+`)`, customerID, "QEPORT-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5)`, productID, "QEPORT-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
	})

	orderSvc := order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	quoteSvc := quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithOrderCreator(orderSvc)
	portalSvc := NewService(NewRepository(db), "qe-expiry-test-secret", slog.Default(), nil, nil, nil, nil, nil).
		WithQuoteService(quoteSvc)

	// One quote sent while its date is good, then aged past it, as time alone
	// moves a sent quote; the decide and the convert each try it.
	mkSentPast := func() uuid.UUID {
		good := time.Now().UTC().Add(24 * time.Hour)
		q, err := quoteSvc.Create(ctx, &quote.Draft{
			CustomerID: customerID, DeliveryType: quote.DeliveryPickup, Source: "manual",
			ExpiresAt: httpx.PtrTimestamp(&good),
			Lines: []quote.DraftLine{{ProductID: &productID, SKU: "S", Description: "D",
				Quantity: 10000, UOM: "PCS", PriceUOM: "PCS", UOMQty: 10000, PriceUOMQty: 10000,
				UnitPrice: 55000}},
		})
		if err != nil {
			t.Fatalf("create quote: %v", err)
		}
		if err := quoteSvc.UpdateState(branchctx.WithSystem(ctx), q.ID, quote.QuoteStateSent); err != nil {
			t.Fatalf("send quote: %v", err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE quotes SET expires_at = $1 WHERE id = $2`,
			time.Now().UTC().Add(-time.Hour), q.ID); err != nil {
			t.Fatalf("age the quote: %v", err)
		}
		return q.ID
	}

	// Decide: the accept is refused with the ruling's words.
	decideID := mkSentPast()
	_, err := portalSvc.AcceptQuote(ctx, decideID, customerID)
	if err == nil {
		t.Fatal("the portal accepted a quote past its Valid until")
	}
	if !errors.Is(err, ErrQuoteNotDecidable) {
		t.Errorf("the decide refusal = %v, want ErrQuoteNotDecidable", err)
	}
	if !qe01WordsRe.MatchString(err.Error()) {
		t.Errorf("the decide refusal = %q, want the ruling's words", err.Error())
	}

	// Convert: refused with the same words, and no order was made.
	convertID := mkSentPast()
	_, err = portalSvc.ConvertPortalQuote(ctx, convertID, customerID, CapabilitiesFor(RoleOwner))
	if err == nil {
		t.Fatal("the portal converted a quote past its Valid until")
	}
	if !errors.Is(err, ErrQuoteNotConvertible) {
		t.Errorf("the convert refusal = %v, want ErrQuoteNotConvertible", err)
	}
	if !qe01WordsRe.MatchString(err.Error()) {
		t.Errorf("the convert refusal = %q, want the ruling's words", err.Error())
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE quote_id = $1`, convertID).Scan(&n); err != nil || n != 0 {
		t.Errorf("orders from the refused convert = %d (err %v), want 0", n, err)
	}
}
