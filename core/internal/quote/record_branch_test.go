// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// The record branch rule (ADR 0007 section 2.3) on a quote a path id
// addresses, at the service boundary the portal and the integration seam
// call through: the check fails closed, so a caller with no branch context
// and no system mark is refused, a marked system caller passes, and an
// ordinary branch context is held to the quote's branch.

import (
	"context"
	"errors"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// seedQuote creates one draft quote through the service itself, marked a
// system caller because the fixture's context carries no branch context.
func seedQuote(t *testing.T, svc *quote.Service, customerID, productID uuid.UUID, sku string) *quote.Quote {
	t.Helper()
	q, err := svc.Create(branchctx.WithSystem(context.Background()), &quote.Draft{
		CustomerID:   customerID,
		DeliveryType: quote.DeliveryPickup,
		Source:       "manual",
		Lines: []quote.DraftLine{{
			ProductID: &productID, SKU: sku, Description: "2x4x8 SPF",
			Quantity: 10 * 10000, UOM: "PCS", PriceUOM: "PCS", UOMQty: 10000, PriceUOMQty: 10000,
			UnitPrice: 55000,
		}},
	})
	if err != nil {
		t.Fatalf("create quote as system caller: %v", err)
	}
	return q
}

func TestGetQuote_RecordBranchFailsClosed(t *testing.T) {
	// seedQuote below writes a quote.created row into events_outbox, which
	// the events and outbox packages' tests truncate under this lock; take it
	// so this test's writes and their truncates serialise across packages.
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()

	svc := quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithBranchGuard(middleware.NewBranchGuard(db))
	q := seedQuote(t, svc, f.customerID, f.productID, f.sku)

	if _, err := svc.GetQuote(ctx, q.ID); err == nil {
		t.Fatalf("GetQuote with no branch context and no system mark: nil error, want refused")
	} else {
		var herr *httpx.Error
		if !errors.As(err, &herr) || herr.Status != 403 {
			t.Fatalf("GetQuote with no branch context: %v, want the 403 forbidden error", err)
		}
		if len(herr.Details) == 0 || herr.Details[0].Field != "id" {
			t.Fatalf("GetQuote with no branch context: %v, want the error naming id", herr.Details)
		}
	}

	other := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, other, "RB-"+other.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, other)
	})
	bound := &branchctx.Context{UserSub: "u-b", BranchID: &other}
	if _, err := svc.GetQuote(branchctx.With(ctx, bound), q.ID); err == nil {
		t.Fatalf("GetQuote from a caller held to another branch: nil error, want refused")
	}

	if _, err := svc.GetQuote(branchctx.WithSystem(ctx), q.ID); err != nil {
		t.Fatalf("GetQuote as a marked system caller: %v, want the quote", err)
	}
	admin := &branchctx.Context{UserSub: "boss", IsAdmin: true}
	if _, err := svc.GetQuote(branchctx.With(ctx, admin), q.ID); err != nil {
		t.Fatalf("GetQuote as an administrator across branches: %v, want the quote", err)
	}
}

func TestUpdateState_RecordBranchFailsClosed(t *testing.T) {
	// Same outbox lock as TestGetQuote_RecordBranchFailsClosed above: the
	// seeded quote's events share events_outbox with the truncating tests of
	// the events and outbox packages.
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()

	svc := quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithBranchGuard(middleware.NewBranchGuard(db))
	q := seedQuote(t, svc, f.customerID, f.productID, f.sku)

	if err := svc.UpdateState(ctx, q.ID, quote.QuoteStateSent); err == nil {
		t.Fatalf("UpdateState with no branch context and no system mark: nil error, want refused")
	}
	if err := svc.UpdateState(branchctx.WithSystem(ctx), q.ID, quote.QuoteStateSent); err != nil {
		t.Fatalf("UpdateState as a marked system caller: %v, want the transition", err)
	}
	got, err := svc.GetQuote(branchctx.WithSystem(ctx), q.ID)
	if err != nil || got.Status != quote.QuoteStateSent {
		t.Fatalf("quote after the system transition = %v (%v), want sent", got.Status, err)
	}
}
