// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// The convert and the branch (ADR 0007 section 2.3, ADR 0005 section 5.8):
// the convert holds the quote to the caller's wall inside its transaction,
// failing closed, and the order it creates takes the QUOTE's branch (and so
// that branch's tax rate), never the caller's context or the deployment
// default.

import (
	"context"
	"errors"
	"testing"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// convertWorld is a second branch with its own tax rate beside the default
// branch (10 percent), a customer and a quote in the second branch.
type convertWorld struct {
	f        *fixture
	svc      *quote.Service
	orderSvc *order.Service
	other    uuid.UUID
}

const defaultBranchSQL = `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`

func newConvertWorld(t *testing.T, db *database.DB) *convertWorld {
	t.Helper()
	testutil.LockOutboxTables(t)
	ctx := context.Background()
	f := newFixture(t, db)
	w := &convertWorld{f: f, other: uuid.New()}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, default_tax_rate) VALUES ($1, 'BRANCH', $2, 0.05)`,
		w.other, "CB-"+w.other.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	var oldRate *string
	if err := db.Pool.QueryRow(ctx, `SELECT default_tax_rate::text FROM locations WHERE id = `+defaultBranchSQL).Scan(&oldRate); err != nil {
		t.Fatalf("read default branch rate: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.10 WHERE id = `+defaultBranchSQL); err != nil {
		t.Fatalf("seed default branch rate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'order' AND entity_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, w.other)
		_, _ = db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = $1 WHERE id = `+defaultBranchSQL, oldRate)
	})
	w.orderSvc = order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	w.svc = quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithBranchGuard(middleware.NewBranchGuard(db)).WithOrderCreator(w.orderSvc)
	return w
}

// otherBranchQuote creates a 10 PCS quote at 5.50 in the second branch.
func (w *convertWorld) otherBranchQuote(t *testing.T) *quote.Quote {
	t.Helper()
	pid := w.f.productID
	q, err := w.svc.Create(branchctx.WithSystem(context.Background()), &quote.Draft{
		CustomerID: w.f.customerID, DeliveryType: quote.DeliveryPickup, Source: "manual", BranchID: &w.other,
		Lines: []quote.DraftLine{{
			ProductID: &pid, SKU: w.f.sku, Description: "2x4x8 SPF",
			Quantity: 10 * 10000, UOM: "PCS", PriceUOM: "PCS", UOMQty: 10000, PriceUOMQty: 10000, UnitPrice: 55000,
		}},
	})
	if err != nil {
		t.Fatalf("create the second branch quote: %v", err)
	}
	if q.BranchID != w.other {
		t.Fatalf("quote branch = %s, want the second branch %s", q.BranchID, w.other)
	}
	return q
}

func refusedOnID(t *testing.T, what string, err error) {
	t.Helper()
	var herr *httpx.Error
	if !errors.As(err, &herr) || herr.Status != 403 {
		t.Fatalf("%s: %v, want the 403 forbidden error", what, err)
	}
	if len(herr.Details) == 0 || herr.Details[0].Field != "id" {
		t.Fatalf("%s: %v, want the error naming id", what, herr.Details)
	}
}

// RULE (ADR 0007 2.3): the convert fails closed on the record's branch, as
// the transition it replaced did. A caller with no branch context and no
// system mark, or held to another branch, converts nothing, and the refusal
// leaves the quote unaccepted with no order.
func TestConvert_RecordBranchFailsClosed(t *testing.T) {
	db := testutil.RequireDB(t)
	w := newConvertWorld(t, db)
	ctx := context.Background()
	q := w.otherBranchQuote(t)

	_, err := w.svc.Convert(ctx, q.ID, quote.Precondition{Revision: &q.Revision})
	refusedOnID(t, "Convert with no branch context and no system mark", err)
	_, err = w.svc.ConvertInProcess(ctx, q.ID)
	refusedOnID(t, "ConvertInProcess with no branch context and no system mark", err)

	home := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, home, "CBH-"+home.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, home) })
	bound := branchctx.With(ctx, &branchctx.Context{UserSub: "u-home", BranchID: &home})
	// A context branch is held by the repository's own filter first: the
	// foreign quote is not found at all.
	var herr *httpx.Error
	if _, err = w.svc.Convert(bound, q.ID, quote.Precondition{Revision: &q.Revision}); !errors.As(err, &herr) || herr.Status != 404 {
		t.Fatalf("Convert held to another branch: %v, want 404", err)
	}

	got, err := w.svc.GetQuote(branchctx.WithSystem(ctx), q.ID)
	if err != nil || got.Status != quote.QuoteStateDraft {
		t.Fatalf("quote after the refusals = %v (%v), want still draft", got.Status, err)
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE quote_id = $1`, q.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d orders exist for the refused quote (%v)", n, err)
	}

	if _, err := w.svc.Convert(branchctx.WithSystem(ctx), q.ID, quote.Precondition{Revision: &q.Revision}); err != nil {
		t.Fatalf("Convert as a marked system caller: %v", err)
	}
}

// RULE (ADR 0005 5.8's table): the order takes the quote's branch_id, and so
// the quote's branch tax rate, whoever converts and whatever branch the
// caller's context holds. Probe on edbc55c: a quote in a 5 percent branch,
// the default branch at 10 percent, an administrator's convert created the
// order in the default branch with 550 cents of tax; the answer is 275.
func TestConvert_OrderTakesTheQuotesBranch(t *testing.T) {
	db := testutil.RequireDB(t)
	w := newConvertWorld(t, db)
	ctx := context.Background()
	admin := branchctx.With(ctx, &branchctx.Context{UserSub: "boss", IsAdmin: true})

	check := func(name string, o *order.Order) {
		t.Helper()
		if o.BranchID != w.other {
			t.Errorf("%s: order branch = %s, want the quote's %s", name, o.BranchID, w.other)
		}
		if o.TaxRatePercent == nil || *o.TaxRatePercent != "5" {
			rate := "null"
			if o.TaxRatePercent != nil {
				rate = *o.TaxRatePercent
			}
			t.Errorf("%s: tax rate = %s, want the quote branch's 5", name, rate)
		}
		if o.SubtotalCents != 5500 || o.TaxCents != 275 {
			t.Errorf("%s: subtotal %d tax %d, want 5500 and 275", name, o.SubtotalCents, o.TaxCents)
		}
	}

	q := w.otherBranchQuote(t)
	o, err := w.svc.Convert(admin, q.ID, quote.Precondition{Revision: &q.Revision})
	if err != nil {
		t.Fatalf("Convert as an administrator: %v", err)
	}
	check("Convert, administrator", o)

	// The integration seam's convert runs with no context branch: it must not
	// fall to the default branch either.
	q2 := w.otherBranchQuote(t)
	o2, err := w.svc.ConvertInProcess(branchctx.WithSystem(ctx), q2.ID)
	if err != nil {
		t.Fatalf("ConvertInProcess: %v", err)
	}
	check("ConvertInProcess", o2)
}

type poRecorder struct {
	calls []uuid.UUID // the quote line ids it was asked about
	qty   []float64
	cost  []float64
	err   error
}

func (p *poRecorder) CreatePOFromSpecialOrderLine(_ context.Context, _ uuid.UUID, _ *uuid.UUID, qty, unitCost float64, lineID uuid.UUID) error {
	p.calls = append(p.calls, lineID)
	p.qty = append(p.qty, qty)
	p.cost = append(p.cost, unitCost)
	return p.err
}

// RULE: the convert creates the automatic purchase orders for the quote's
// special order lines, as the accept it replaced did (best effort, after the
// transaction commits: ADR 0005 5.8 is silent, and a purchase order the
// convert's own rollback could not take back would be orphaned). A failure
// is logged and never blocks the convert; a refused convert creates none.
func TestConvert_CreatesTheAutomaticPurchaseOrders(t *testing.T) {
	db := testutil.RequireDB(t)
	w := newConvertWorld(t, db)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `UPDATE products SET average_unit_cost = 3.25 WHERE id = $1`, w.f.productID); err != nil {
		t.Fatal(err)
	}
	po := &poRecorder{}
	w.svc.WithAutoPO(po)

	// A refused convert (no branch context) creates none.
	q := w.otherBranchQuote(t)
	if _, err := w.svc.Convert(ctx, q.ID, quote.Precondition{Revision: &q.Revision}); err == nil {
		t.Fatal("convert with no branch context succeeded")
	}
	if len(po.calls) != 0 {
		t.Fatalf("a refused convert made %d purchase orders", len(po.calls))
	}

	o, err := w.svc.Convert(branchctx.WithSystem(ctx), q.ID, quote.Precondition{Revision: &q.Revision})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(po.calls) != 1 || po.calls[0] != *o.Lines[0].QuoteLineID || po.qty[0] != 10 || po.cost[0] != 3.25 {
		t.Fatalf("purchase orders asked for = lines %v qty %v cost %v, want the quote line once at 10 and 3.25", po.calls, po.qty, po.cost)
	}

	// The seam's convert does the same, and a failing purchase order never
	// blocks it.
	po.err = errors.New("vendor unavailable")
	q2 := w.otherBranchQuote(t)
	if _, err := w.svc.ConvertInProcess(branchctx.WithSystem(ctx), q2.ID); err != nil {
		t.Fatalf("convert with a failing purchase order: %v", err)
	}
	if len(po.calls) != 2 {
		t.Fatalf("the seam's convert asked %d times, want 2 in all", len(po.calls))
	}
}

// RULE (ADR 0001 section 12): quote.accepted from the convert carries the
// revision AFTER the accept moved it, as the transition's event does, and
// from_status.
func TestConvert_AcceptedEventCarriesTheNewRevision(t *testing.T) {
	db := testutil.RequireDB(t)
	w := newConvertWorld(t, db)
	ctx := branchctx.WithSystem(context.Background())
	q := w.otherBranchQuote(t)
	if _, err := w.svc.Convert(ctx, q.ID, quote.Precondition{Revision: &q.Revision}); err != nil {
		t.Fatalf("convert: %v", err)
	}
	after, err := w.svc.GetQuote(ctx, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rev int64
	var from string
	if err := db.Pool.QueryRow(ctx, `SELECT (data->>'revision')::bigint, data->>'from_status' FROM events_outbox
		WHERE entity_type = 'quote' AND entity_id = $1 AND type = 'quote.accepted'`, q.ID).Scan(&rev, &from); err != nil {
		t.Fatalf("read the event: %v", err)
	}
	if rev != after.Revision || rev <= q.Revision {
		t.Errorf("quote.accepted revision = %d, want the accepted quote's %d (above %d)", rev, after.Revision, q.Revision)
	}
	if from != "draft" {
		t.Errorf("from_status = %q, want draft", from)
	}
}
