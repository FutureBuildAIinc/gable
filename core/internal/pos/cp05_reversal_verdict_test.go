// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-05 round 3 P1-A: a reversal reads the gateway's verdict, not only its
// error. A gateway may answer a definite decline with no error (the Run
// terminal answers a C so), and a reversal that read only the error booked
// that declined void as voided while the card stayed charged. VOIDED or
// REFUNDED is the reversal; a definite DECLINED on the void goes to the
// refund; any other answer is unknown and books no reversal.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/google/uuid"
)

// refusedCardSale rings a card sale the simulated terminal approves and the
// completion then refuses (no stock), so the counter reverses the leg.
func refusedCardSale(t *testing.T, gw *fakeGateway) (*fixture, string, resp) {
	t.Helper()
	f := terminalFixture(t, payment.SimApprove, gw)
	saleID := f.openSale(t, nil)
	mustExec(t, f.db, `UPDATE inventory SET quantity = 0 WHERE product_id = $1`, f.productID)
	return f, saleID, f.completeSale(saleID, f.cardAll(t, saleID))
}

// RULE: a compensation void the gateway declines with no error goes to the
// refund; the leg is spent only once the refund put the money back.
func TestCP05R3_ADeclinedCompensationVoidGoesToTheRefund(t *testing.T) {
	gw := &fakeGateway{voidStatuses: []payment.GatewayStatus{payment.GatewayStatusDeclined}}
	f, saleID, r := refusedCardSale(t, gw)
	if r.status != http.StatusConflict {
		t.Fatalf("the refused completion = %d, wanted the refusal's 409: %s", r.status, r.raw)
	}
	if len(gw.voids) != 1 || len(gw.refunds) != 1 || gw.refundCents[0] != f.totalOf(t, saleID) {
		t.Fatalf("voids %v refunds %v cents %v, wanted the declined void then one refund of the whole charge",
			gw.voids, gw.refunds, gw.refundCents)
	}
	saleLegID, _, _ := f.terminalIntentOfKind(t, saleID, "sale")
	if state, spent, _ := f.intentFacts(t, saleLegID); state != "authorized" || !spent {
		t.Fatalf("the sale leg is %s spent=%t, wanted authorized and spent (refunded)", state, spent)
	}
}

// RULE: a compensation void and refund both declined with no error put
// nothing back: the completion answers the 502, the sale leg stays unspent
// and the compensation leg takes no authorized verdict, so the sweep files
// the manager item.
func TestCP05R3_ADeclinedCompensationVoidAndRefundBookNoReversal(t *testing.T) {
	gw := &fakeGateway{voidStatuses: []payment.GatewayStatus{payment.GatewayStatusDeclined},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusDeclined}}
	f, saleID, r := refusedCardSale(t, gw)
	if r.status != http.StatusBadGateway {
		t.Fatalf("the completion = %d, wanted 502 (the charge was not reversed): %s", r.status, r.raw)
	}
	saleLegID, _, _ := f.terminalIntentOfKind(t, saleID, "sale")
	if state, spent, _ := f.intentFacts(t, saleLegID); state != "authorized" || spent {
		t.Fatalf("the sale leg is %s spent=%t, wanted authorized and unspent (the card still holds it)", state, spent)
	}
	compID, _ := f.refundLegOf(t, saleID, "void")
	if state, _, _ := f.intentFacts(t, compID); state == "authorized" {
		t.Fatalf("the compensation void took an authorized verdict for a reversal the gateway declined")
	}
}

// RULE: a compensation void answering neither the reversal nor a decline is
// unknown: no refund is sent on top of a void that may have gone through,
// and the leg stays unspent for the sweep.
func TestCP05R3_AnUnknownCompensationVoidSendsNoRefund(t *testing.T) {
	gw := &fakeGateway{voidStatuses: []payment.GatewayStatus{payment.GatewayStatusPending}}
	f, saleID, r := refusedCardSale(t, gw)
	if r.status != http.StatusBadGateway {
		t.Fatalf("the completion = %d, wanted 502 (the reversal is unknown): %s", r.status, r.raw)
	}
	if len(gw.refunds) != 0 {
		t.Fatalf("refunds %v sent after an unknown void", gw.refunds)
	}
	saleLegID, _, _ := f.terminalIntentOfKind(t, saleID, "sale")
	if _, spent, _ := f.intentFacts(t, saleLegID); spent {
		t.Fatalf("the sale leg is spent after an unknown reversal")
	}
}

// resolvePaidInFull rings a lost card leg on a sale then paid in cash, so the
// manager's authorized resolution reverses the whole charge.
func resolvePaidInFull(t *testing.T, gw *fakeGateway) (f *fixture, saleID string, intentID, itemID uuid.UUID, r resp) {
	t.Helper()
	f = terminalFixture(t, payment.SimNoAnswer, gw)
	saleID = f.openSale(t, nil)
	intentID, itemID = f.lostLegOf(t, saleID)
	if done := f.completeSale(saleID, f.cashAll(t, saleID)); done.status != http.StatusOK {
		t.Fatalf("the cash completion = %d: %s", done.status, done.raw)
	}
	return f, saleID, intentID, itemID, f.resolveItem(itemID, "authorized", f.recordedRefOf(t, intentID, itemID))
}

// RULE: the manager's whole charge reversal goes to the refund when the void
// is declined with no error.
func TestCP05R3_AResolvedWholeChargeGoesToTheRefundOnADeclinedVoid(t *testing.T) {
	gw := &fakeGateway{voidStatuses: []payment.GatewayStatus{payment.GatewayStatusDeclined}}
	f, saleID, _, _, r := resolvePaidInFull(t, gw)
	if r.status != http.StatusOK {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	if len(gw.voids) != 1 || len(gw.refunds) != 1 || gw.refundCents[0] != f.totalOf(t, saleID) {
		t.Fatalf("voids %v refunds %v cents %v, wanted the declined void then the whole refund", gw.voids, gw.refunds, gw.refundCents)
	}
}

// RULE: the manager's whole charge reversal with the void and the refund
// both declined books nothing: 502, the item is open again, the leg unspent.
func TestCP05R3_AResolvedWholeChargeBothDeclinedBooksNothing(t *testing.T) {
	gw := &fakeGateway{voidStatuses: []payment.GatewayStatus{payment.GatewayStatusDeclined},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusDeclined}}
	f, _, intentID, itemID, r := resolvePaidInFull(t, gw)
	if r.status != http.StatusBadGateway {
		t.Fatalf("the resolve = %d, wanted 502: %s", r.status, r.raw)
	}
	if got := itemStateOf(t, f, itemID); got != "open" {
		t.Fatalf("the item is %s, wanted open again", got)
	}
	if _, spent, _ := f.intentFacts(t, intentID); spent {
		t.Fatalf("the leg is spent after a declined reversal")
	}
}

// RULE: the manager's whole charge reversal on an unknown void answer sends
// no refund and books nothing.
func TestCP05R3_AResolvedWholeChargeUnknownVoidSendsNoRefund(t *testing.T) {
	gw := &fakeGateway{voidStatuses: []payment.GatewayStatus{payment.GatewayStatusPending}}
	f, _, intentID, _, r := resolvePaidInFull(t, gw)
	if r.status != http.StatusBadGateway {
		t.Fatalf("the resolve = %d, wanted 502: %s", r.status, r.raw)
	}
	if len(gw.refunds) != 0 {
		t.Fatalf("refunds %v sent after an unknown void", gw.refunds)
	}
	if _, spent, _ := f.intentFacts(t, intentID); spent {
		t.Fatalf("the leg is spent after an unknown reversal")
	}
}

// RULE: the rest of a partly applied charge refunded with a decline and no
// error books nothing: 502, no card tender, the leg unspent.
func TestCP05R3_ADeclinedRestRefundBooksNothing(t *testing.T) {
	gw := &fakeGateway{refundStatuses: []payment.GatewayStatus{payment.GatewayStatusDeclined}}
	f := terminalFixture(t, payment.SimNoAnswer, gw)
	customer := f.customerID
	saleID := f.openSale(t, &customer)
	intentID, itemID := f.lostLegOf(t, saleID)
	total := f.totalOf(t, saleID)
	if r := f.completeSale(saleID, tender("cash", 100), tender("account", total-100)); r.status != http.StatusOK {
		t.Fatalf("the cash and account completion = %d: %s", r.status, r.raw)
	}
	r := f.resolveItem(itemID, "authorized", f.recordedRefOf(t, intentID, itemID))
	if r.status != http.StatusBadGateway {
		t.Fatalf("the resolve = %d, wanted 502 (the rest did not go back): %s", r.status, r.raw)
	}
	if len(gw.refunds) != 1 {
		t.Fatalf("refunds %v, wanted the one declined rest refund", gw.refunds)
	}
	if got := f.cardTendersOf(t, saleID); got != 0 {
		t.Fatalf("%d card tenders booked after a declined rest refund", got)
	}
	if _, spent, _ := f.intentFacts(t, intentID); spent {
		t.Fatalf("the leg is spent after a declined rest refund")
	}
	if got := itemStateOf(t, f, itemID); got != "open" {
		t.Fatalf("the item is %s, wanted open again", got)
	}
}

// keyedGateway is the fake gateway with the idempotency key extension, and
// records the key of every call in order.
type keyedGateway struct {
	*fakeGateway
	keys []string
}

func (g *keyedGateway) VoidWithKey(ctx context.Context, id, key string) (*payment.GatewayResult, error) {
	g.mu.Lock()
	g.keys = append(g.keys, "void "+key)
	g.mu.Unlock()
	return g.Void(ctx, id)
}

func (g *keyedGateway) RefundWithKey(ctx context.Context, id string, cents int64, key string) (*payment.GatewayResult, error) {
	g.mu.Lock()
	g.keys = append(g.keys, "refund "+key)
	g.mu.Unlock()
	return g.Refund(ctx, id, cents)
}

// RULE (CP-05 round 3 P2-B): the refund sent after a declined void goes
// under a key of its own, so a gateway that answers a retried key from its
// record (or a driver that sends a key once, as Run does) never answers the
// refund with the void's no.
func TestCP05R3_TheFallbackRefundHasItsOwnKey(t *testing.T) {
	gw := &keyedGateway{fakeGateway: &fakeGateway{voidStatuses: []payment.GatewayStatus{payment.GatewayStatusDeclined}}}
	f := terminalFixture(t, payment.SimNoAnswer, nil)
	f.service.WithGateway(gw)
	saleID := f.openSale(t, nil)
	intentID, itemID := f.lostLegOf(t, saleID)
	if r := f.completeSale(saleID, f.cashAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the cash completion = %d: %s", r.status, r.raw)
	}
	if r := f.resolveItem(itemID, "authorized", f.recordedRefOf(t, intentID, itemID)); r.status != http.StatusOK {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	if len(gw.keys) != 2 {
		t.Fatalf("calls %v, wanted the declined void then the refund", gw.keys)
	}
	voidKey, refundKey := gw.keys[0][len("void "):], gw.keys[1][len("refund "):]
	if voidKey == "" || refundKey == "" || voidKey == refundKey {
		t.Fatalf("the void went under %q and the refund under %q, wanted two distinct keys", voidKey, refundKey)
	}
}

// runRetryServer is a scripted Run void-or-refund endpoint that answers every
// reversal with a Run B (retry), and counts the POSTs.
func runRetryServer(t *testing.T) (*payment.RunTerminalGateway, *atomic.Int32) {
	t.Helper()
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/void-or-refund" {
			http.NotFound(w, r)
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "B", "resp_code": "91", "resp_text": "RETRY"})
	}))
	t.Cleanup(srv.Close)
	term := payment.NewRunTerminal(func() payment.GatewayConfig {
		return payment.GatewayConfig{APIKey: "teststub" + "teststub", MID: "MID-TESTSTUB", BaseURL: srv.URL}
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return payment.NewRunTerminalGateway(term), &posts
}

// RULE (CP-05 round 3b): a Run B on the compensation void is unknown: the
// one void POST is the only reversal sent, no refund goes on top, and the
// sale leg stays unspent for the sweep.
func TestCP05R3b_ARunRetryOnTheCompensationVoidSendsNoRefund(t *testing.T) {
	gw, posts := runRetryServer(t)
	f := terminalFixture(t, payment.SimApprove, nil)
	f.service.WithGateway(gw)
	saleID := f.openSale(t, nil)
	mustExec(t, f.db, `UPDATE inventory SET quantity = 0 WHERE product_id = $1`, f.productID)
	r := f.completeSale(saleID, f.cardAll(t, saleID))
	if r.status != http.StatusBadGateway {
		t.Fatalf("the completion = %d, wanted 502 (the reversal is unknown): %s", r.status, r.raw)
	}
	if n := posts.Load(); n != 1 {
		t.Fatalf("%d void-or-refund posts, wanted only the void (no refund on top of a B)", n)
	}
	saleLegID, _, _ := f.terminalIntentOfKind(t, saleID, "sale")
	if _, spent, _ := f.intentFacts(t, saleLegID); spent {
		t.Fatalf("the sale leg is spent after an unknown reversal")
	}
}

// RULE (CP-05 round 3b): a Run B on the manager's whole charge reversal
// sends no refund: 502, the item is open again, the leg unspent.
func TestCP05R3b_ARunRetryOnAResolutionSendsNoRefund(t *testing.T) {
	gw, posts := runRetryServer(t)
	f := terminalFixture(t, payment.SimNoAnswer, nil)
	f.service.WithGateway(gw)
	saleID := f.openSale(t, nil)
	intentID, itemID := f.lostLegOf(t, saleID)
	if r := f.completeSale(saleID, f.cashAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the cash completion = %d: %s", r.status, r.raw)
	}
	r := f.resolveItem(itemID, "authorized", f.recordedRefOf(t, intentID, itemID))
	if r.status != http.StatusBadGateway {
		t.Fatalf("the resolve = %d, wanted 502: %s", r.status, r.raw)
	}
	if n := posts.Load(); n != 1 {
		t.Fatalf("%d void-or-refund posts, wanted only the void (no refund on top of a B)", n)
	}
	if got := itemStateOf(t, f, itemID); got != "open" {
		t.Fatalf("the item is %s, wanted open again", got)
	}
	if _, spent, _ := f.intentFacts(t, intentID); spent {
		t.Fatalf("the leg is spent after an unknown reversal")
	}
}
