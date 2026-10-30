// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote_test

// The dealer quote link decided through the REAL quote lifecycle and the real
// purchase order service, the wiring serve runs (wireCustomerQuotes plus the
// quote service's auto purchase order adapter): the plain accept of a quote
// with a costed product line is one transaction with its audit row, raises no
// purchase order (a purchase order line links to an order line, and a plain
// accept creates no order), and three concurrent accepts of one link decide
// once.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// lifecyclePO adapts the real purchase order service to the quote module's
// auto purchase order seam, as serve's autoPOAdapter does for the plain line
// (no product lookup: the accept path carries none).
type lifecyclePO struct{ po *purchase_order.Service }

func (a lifecyclePO) CreatePOFromSpecialOrderLine(ctx context.Context, productID *uuid.UUID, vendorID *uuid.UUID, quantity float64, unitCost float64, linkedSOLineID uuid.UUID, branch uuid.UUID) error {
	return a.po.CreateFromSOLine(ctx, linkedSOLineID, branch, productID, vendorID, "special order", quantity, unitCost)
}

// failingAudit refuses every row, so the acceptance's audit write fails inside
// the acceptance's transaction.
type failingAudit struct{}

func (failingAudit) Log(context.Context, audit.Entry) error { return errors.New("the audit sink is down") }

// realLifecycle wires the quote module as serve does: one transaction per
// write with its outbox event, and the auto purchase order seam on the real
// purchase order service.
func realLifecycle(w *world) customerquote.DealerQuotes {
	svc := quote.NewService(quote.NewRepository(w.db)).
		WithOutbox(outbox.NewWriter(w.db, "")).WithTxRunner(w.db)
	svc.WithAutoPO(lifecyclePO{po: purchase_order.NewService(purchase_order.NewRepository(w.db), w.db, nil, nil, nil, nil)})
	return customerquote.QuoteLifecycle{Quotes: svc}
}

// costedSentDealerQuote seeds a sent dealer quote whose line is a costed
// product line (the product carries an average unit cost), issues its link
// and wires the real lifecycle, answering the quote id and the token.
func costedSentDealerQuote(t *testing.T, w *world, sink customerquote.AuditSink) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	exp := w.now.Add(24 * time.Hour)
	id, _, _ := w.dealerQuote("SENT", &exp)
	if _, err := w.db.Pool.Exec(ctx, `UPDATE products SET average_unit_cost = 3.25 WHERE id = $1`, w.stud); err != nil {
		t.Fatalf("cost the stud: %v", err)
	}
	if _, err := w.db.Pool.Exec(ctx, `INSERT INTO quote_lines (id, quote_id, product_id, sku, description, quantity, uom, unit_price, line_total, position)
		VALUES ($1, $2, $3, 'CQT-STUD', '2x4 stud', 100, 'EA', 7.5000, 750.00, 0)`, uuid.New(), id, w.stud); err != nil {
		t.Fatalf("seed the costed line: %v", err)
	}
	t.Cleanup(func() {
		_, _ = w.db.Pool.Exec(ctx, `DELETE FROM pro_portal_share_tokens WHERE dealer_quote_id = $1`, id)
		_, _ = w.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id = $1`, id)
		_, _ = w.db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type = 'quote' AND entity_id = $1`, id)
		_, _ = w.db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE linked_so_line_id IN (SELECT id FROM quote_lines WHERE quote_id = $1)`, id)
		_, _ = w.db.Pool.Exec(ctx, `DELETE FROM quote_lines WHERE quote_id = $1`, id)
		_, _ = w.db.Pool.Exec(ctx, `DELETE FROM quotes WHERE id = $1`, id)
	})
	res, err := w.svc.ShareDealerQuote(ctx, desk, id, "")
	if err != nil {
		t.Fatalf("issue the link: %v", err)
	}
	w.svc.WithDealerQuotes(realLifecycle(w), sink)
	return id, strings.TrimPrefix(res.Path, "/dq/")
}

// QE-01: the acceptance and its audit row are one transaction. When the audit
// row cannot be written, the acceptance rolls back with it: the quote stays
// SENT, no quote.accepted event survives and no purchase order line exists.
func TestQE01_DealerLinkAcceptAtomic(t *testing.T) {
	testutil.LockOutboxTables(t)
	w := newWorld(t)
	ctx := context.Background()
	id, token := costedSentDealerQuote(t, w, failingAudit{})

	if _, err := w.svc.DealerQuoteDecide(ctx, token, anon, true, "Pat Contractor", ""); err == nil {
		t.Fatal("an acceptance whose audit row cannot be written succeeded")
	}
	var state string
	if err := w.db.Pool.QueryRow(ctx, `SELECT state::text FROM quotes WHERE id = $1`, id).Scan(&state); err != nil || state != "SENT" {
		t.Fatalf("the quote is %s (%v), want SENT: the acceptance must roll back with its audit row", state, err)
	}
	var events, rows, linked int
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM events_outbox WHERE entity_type = 'quote' AND entity_id = $1 AND type = 'quote.accepted'`, id).Scan(&events); err != nil || events != 0 {
		t.Fatalf("%d quote.accepted events survived the rollback (%v)", events, err)
	}
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE entity_type = 'quote' AND entity_id = $1 AND action = 'quote.accepted_from_link'`, id).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("%d audit rows survived the rollback (%v)", rows, err)
	}
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM purchase_order_lines pol
		JOIN quote_lines ql ON pol.linked_so_line_id = ql.id WHERE ql.quote_id = $1`, id).Scan(&linked); err != nil || linked != 0 {
		t.Fatalf("%d purchase order line(s) point at a quote line (%v)", linked, err)
	}
}

// QE-01: three accepts of one link at pool size 4 decide once: one wins, the
// others get the already decided answer (the lifecycle's refusal), the quote
// is ACCEPTED with exactly one audit row, and no purchase order line points at
// a quote line.
func TestQE01_DealerLinkAcceptDecideOnceAtPoolFour(t *testing.T) {
	testutil.LockOutboxTables(t)
	w := newWorldPool(t, 4)
	ctx := context.Background()
	id, token := costedSentDealerQuote(t, w, audit.NewLogger(w.db))

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = w.svc.DealerQuoteDecide(ctx, token, customerquote.Caller{Address: "203.0.113.9"}, true, "Pat Contractor", "")
		}()
	}
	close(start)
	wg.Wait()

	won, decided := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, customerquote.ErrInvalidState):
			decided++
		}
	}
	if won != 1 || decided != 2 {
		t.Fatalf("of three accepts %d won and %d got the already decided answer (%v)", won, decided, errs)
	}
	var state string
	var rows int
	if err := w.db.Pool.QueryRow(ctx, `SELECT state::text FROM quotes WHERE id = $1`, id).Scan(&state); err != nil || state != "ACCEPTED" {
		t.Fatalf("the quote is %s (%v), want ACCEPTED", state, err)
	}
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE entity_type = 'quote' AND entity_id = $1 AND action = 'quote.accepted_from_link'`, id).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("%d acceptance audit rows (%v), want one", rows, err)
	}
	var linked int
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM purchase_order_lines pol
		JOIN quote_lines ql ON pol.linked_so_line_id = ql.id WHERE ql.quote_id = $1`, id).Scan(&linked); err != nil || linked != 0 {
		t.Fatalf("%d purchase order line(s) point at a quote line (%v)", linked, err)
	}
}
