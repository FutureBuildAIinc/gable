// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The dealer's own quote link through serve's own wiring: the quote service
// the link decides through carries the auto purchase order adapter and the
// order creator, exactly as serve wires them, so the public accept of a quote
// with a costed product line runs the path the desk and the link share.

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// newDQStack stands the dealer quote link on the wiring serve really runs: the
// stud is costed, so the seeded dealer quote's line is a costed product line
// (the line an auto purchase order would target), the quote service carries
// the auto purchase order adapter and the order creator, and the desk's quotes
// routes are mounted so an accepted quote can also be converted from the desk.
func newDQStack(t *testing.T) *cqStack {
	t.Helper()
	s := newCQStack(t)
	ctx := context.Background()
	if _, err := s.db.Pool.Exec(ctx, `UPDATE products SET average_unit_cost = 3.25 WHERE id = $1`, s.stud); err != nil {
		t.Fatalf("cost the stud: %v", err)
	}
	poSvc := purchase_order.NewService(purchase_order.NewRepository(s.db), s.db, nil, nil, nil, nil)
	productSvc := product.NewService(product.NewRepository(s.db))
	s.quoteSvc.WithAutoPO(&autoPOAdapter{poSvc: poSvc, productSvc: productSvc})
	s.quoteSvc.WithOrderCreator(order.NewService(order.NewRepository(s.db)).
		WithOutbox(outbox.NewWriter(s.db, "")).WithTxRunner(s.db))
	s.wall.quotes(s.mux, s.quoteSvc)
	return s
}

// sharedDealerLink seeds a sent dealer quote with a costed line and issues its
// link, answering the quote id and the public base path of the link.
func sharedDealerLink(t *testing.T, s *cqStack) (uuid.UUID, string) {
	t.Helper()
	qid := s.dealerQuote("SENT")
	r := s.do("POST", "/api/v1/quotes/"+qid.String()+"/share-link", asDesk, nil)
	if r.status != 200 {
		t.Fatalf("issue the link: %d %s", r.status, r.body)
	}
	token := strings.TrimPrefix(jsonOf(t, r.body)["path"].(string), "/dq/")
	return qid, "/api/public/v1/dealer-quotes/" + token
}

// QE-01: the public accept of a dealer quote whose product line carries a cost
// answers 200 (it answered 500: the accept's auto purchase order linked a
// purchase order line to the QUOTE line, whose foreign key targets
// order_lines, aborting the transaction the audit row rides), the quote is
// ACCEPTED, the audit row is written, and no purchase order line points at a
// quote line: a plain accept with no order raises no purchase order at all.
func TestQE01_DealerLinkAcceptCostedLine(t *testing.T) {
	s := newDQStack(t)
	ctx := context.Background()
	qid, pub := sharedDealerLink(t, s)

	r := s.do("POST", pub+"/accept", nil, map[string]any{"full_name": "Pat Contractor"})
	if r.status != 200 || jsonOf(t, r.body)["status"] != "accepted" {
		t.Fatalf("accept a costed line: %d %s", r.status, r.body)
	}
	var state string
	if err := s.db.Pool.QueryRow(ctx, `SELECT state::text FROM quotes WHERE id = $1`, qid).Scan(&state); err != nil || state != "ACCEPTED" {
		t.Fatalf("the quote is %s (%v), want ACCEPTED", state, err)
	}
	var who string
	if err := s.db.Pool.QueryRow(ctx, `SELECT changes->>'decided_by' FROM audit_log
		WHERE entity_type = 'quote' AND entity_id = $1 AND action = 'quote.accepted_from_link'`, qid).Scan(&who); err != nil {
		t.Fatalf("no audit row rode the acceptance: %v", err)
	}
	if who != "Pat Contractor" {
		t.Errorf("the audit row holds %q", who)
	}
	var linked int
	if err := s.db.Pool.QueryRow(ctx, `SELECT count(*) FROM purchase_order_lines pol
		JOIN quote_lines ql ON pol.linked_so_line_id = ql.id WHERE ql.quote_id = $1`, qid).Scan(&linked); err != nil || linked != 0 {
		t.Fatalf("%d purchase order line(s) point at a quote line (%v)", linked, err)
	}
	var raised int
	if err := s.db.Pool.QueryRow(ctx, `SELECT count(*) FROM purchase_order_lines pol
		JOIN purchase_orders po ON po.id = pol.po_id
		WHERE po.source = 'SPECIAL_ORDER' AND po.branch_id = $1`, s.branch).Scan(&raised); err != nil || raised != 0 {
		t.Fatalf("the plain accept raised %d purchase order line(s) (%v), want none: with no order there is no order line to serve", raised, err)
	}
}

// QE-01: the decline of a link records the contractor's reason beside the
// decision, through the same transaction, and the quote is REJECTED.
func TestQE01_DealerLinkDeclineReason(t *testing.T) {
	s := newDQStack(t)
	ctx := context.Background()
	qid, pub := sharedDealerLink(t, s)

	r := s.do("POST", pub+"/decline", nil, map[string]any{"full_name": "Pat Contractor", "reason": "found it cheaper elsewhere"})
	if r.status != 200 || jsonOf(t, r.body)["status"] != "declined" {
		t.Fatalf("decline: %d %s", r.status, r.body)
	}
	var state, reason string
	if err := s.db.Pool.QueryRow(ctx, `SELECT state::text, changes->>'reason' FROM quotes q
		JOIN audit_log a ON a.entity_id = q.id AND a.action = 'quote.declined_from_link' WHERE q.id = $1`, qid).Scan(&state, &reason); err != nil {
		t.Fatalf("the decline and its audit row: %v", err)
	}
	if state != "REJECTED" || reason != "found it cheaper elsewhere" {
		t.Errorf("state %s, reason %q", state, reason)
	}
}

// QE-01: a quote the contractor accepted from its link converts from the desk:
// the desk's Convert (the detail page shows it on an accepted quote) answers
// 201 with the order, the quote keeps its ACCEPTED state, and no second
// acceptance is recorded.
func TestQE01_ConvertAccepted(t *testing.T) {
	s := newDQStack(t)
	ctx := context.Background()
	qid, pub := sharedDealerLink(t, s)

	if r := s.do("POST", pub+"/accept", nil, map[string]any{"full_name": "Pat Contractor"}); r.status != 200 {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	got := s.do("GET", "/api/v1/quotes/"+qid.String(), asDesk, nil)
	if got.status != 200 {
		t.Fatalf("read the quote: %d %s", got.status, got.body)
	}
	rev := jsonOf(t, got.body)["revision"].(float64)

	r := s.do("POST", "/api/v1/quotes/"+qid.String()+"/convert", map[string]string{"X-Test-Role": "sales", "X-Test-Sub": "desk-user-1", "X-Test-Email": deskEmail, "If-Match": `"` + strconv.Itoa(int(rev)) + `"`}, nil)
	if r.status != 201 {
		t.Fatalf("convert an accepted quote: %d %s", r.status, r.body)
	}
	o := jsonOf(t, r.body)
	if o["status"] != "draft" {
		t.Errorf("the converted order is %v", o["status"])
	}
	var state string
	var orders, acceptedEvents int
	if err := s.db.Pool.QueryRow(ctx, `SELECT state::text FROM quotes WHERE id = $1`, qid).Scan(&state); err != nil || state != "ACCEPTED" {
		t.Fatalf("the quote after convert is %s (%v)", state, err)
	}
	if err := s.db.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE quote_id = $1`, qid).Scan(&orders); err != nil || orders != 1 {
		t.Fatalf("%d orders for the quote (%v)", orders, err)
	}
	if err := s.db.Pool.QueryRow(ctx, `SELECT count(*) FROM events_outbox WHERE entity_type = 'quote' AND entity_id = $1 AND type = 'quote.accepted'`, qid).Scan(&acceptedEvents); err != nil || acceptedEvents != 1 {
		t.Fatalf("%d quote.accepted events (%v), want the link accept's one", acceptedEvents, err)
	}
}
