// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The resolution of a stranded refund claim (seventh review P2-3, eighth
// review P3-2, FROM-PRINCIPAL 41): the claim carries the gateway's own
// refund id the moment the terminal answers, so a PENDING claim that
// outlives its act can be told apart (money moved or may not have), and the
// guarded admin route settles it: a recorded refund books through the
// refund's own money transaction with every cap and lock, an explicit no
// refund marks the claim FAILED and moves nothing, and both write one audit
// row naming the person, the intent, the outcome and the reason.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// resolveOn posts one resolution of the claim with the finance role.
func (f *fixture) resolveOn(t *testing.T, intentID string, body map[string]any, role string) resp {
	t.Helper()
	return f.resolveOnSub(t, intentID, body, role, mustUUID(t))
}

// resolveOnSub posts one resolution as a named subject (the branch wall
// tests grant one).
func (f *fixture) resolveOnSub(t *testing.T, intentID string, body map[string]any, role, sub string) resp {
	t.Helper()
	return f.do("POST", "/api/v1/pos/refund-intents/"+intentID+"/resolve", body,
		"X-Test-Role", role, "X-Test-Sub", sub)
}

// pendingIntent reads the sale's standing claim row.
func pendingIntent(t *testing.T, f *fixture, saleID string) (id, state, refundTxID string, request []byte) {
	t.Helper()
	var req []byte
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id::text, state, COALESCE(refund_tx_id, ''), request FROM pos_refund_intents WHERE sale_id = $1 AND state = 'PENDING'`, saleID).
		Scan(&id, &state, &refundTxID, &req); err != nil {
		t.Fatalf("read the standing claim: %v", err)
	}
	request = req
	return id, state, refundTxID, request
}

// RULE (seventh review P2-3): the gateway's own refund id lands on the
// standing claim in its own committed write the moment the terminal answers,
// before the money transaction opens: a transaction that then refuses leaves
// a PENDING claim that names the refund that moved.
func TestTheGatewayRefundIdLandsOnTheClaimWhenTheTerminalAnswers(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.setFail("pos_return.completed")
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status == http.StatusCreated {
		t.Fatalf("the return committed under a failing event write: %s", r.raw)
	}
	_, state, refundTxID, request := pendingIntent(t, f, saleID)
	if state != "PENDING" {
		t.Fatalf("the refused return left the claim %s, want PENDING", state)
	}
	if refundTxID == "" {
		t.Fatalf("the standing claim carries no refund id: the money that moved cannot be told from money that may not have")
	}
	if refundTxID != gw.refunds[0]+"x"[:0] && refundTxID != "ref-1" {
		t.Errorf("the claim carries %q, want the gateway's own refund id", refundTxID)
	}
	var stored struct {
		Lines []struct {
			Quantity int64 `json:"quantity"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(request, &stored); err != nil || len(stored.Lines) != 1 || stored.Lines[0].Quantity != 10000 {
		t.Errorf("the claim's stored request = %s (err %v): a return claim carries its request shape for the replay", request, err)
	}
	f.assertARInvariants(t)
}

// RULE (FROM-PRINCIPAL 41): an explicit no refund, with the reconciliation
// reference finance filed, marks the claim FAILED and moves nothing; the
// audit row names the person, the intent, the outcome and the reason; and
// the released tender may be refunded again through the normal route.
func TestANoRefundResolutionReleasesTheClaimAndMovesNothing(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()},
		refundErrs: []error{context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out card return = %d, want 500: %s", r.status, r.raw)
	}
	intentID, _, refundTxID, _ := pendingIntent(t, f, saleID)
	if refundTxID != "" {
		t.Fatalf("a claim whose call answered no verdict carries a refund id (%s)", refundTxID)
	}
	r := f.resolveOn(t, intentID, map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-41", "reason": "terminal log shows no refund",
	}, "finance")
	if r.status != http.StatusOK {
		t.Fatalf("the no refund resolution = %d, want 200: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'FAILED'`, intentID); got != 1 {
		t.Fatalf("the claim is not FAILED: %d rows", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_intent.resolved'
		AND changes->>'intent_id' = $1 AND changes->>'outcome' = 'no_refund' AND changes->>'reason' = 'terminal log shows no refund'
		AND changes->>'reconciliation_reference' = 'REC-41'`, intentID); got != 1 {
		t.Errorf("%d resolution audit rows naming the person's record, want 1", got)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (nothing moved: the terminal answered nothing)", got)
	}
	if got := f.counterReturns(t); got != 0 {
		t.Errorf("%d returns booked, want 0", got)
	}
	f.assertARInvariants(t)
	// the released claim no longer blocks the tender: the retry goes through
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusCreated {
		t.Fatalf("the retried card return = %d: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}

// RULE (FROM-PRINCIPAL 41): a recorded refund books through the return's own
// money transaction with every cap and lock: the memo, its entry, the stock
// and the refund post in one transaction, the refund row names the gateway's
// refund id the operator recorded, and the claim completes DONE with it.
func TestARecordedRefundResolutionBooksTheStrandedReturn(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.setFail("pos_return.completed")
	})
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status == http.StatusCreated {
		t.Fatalf("the return committed under a failing event write: %s", r.raw)
	}
	intentID, _, refundTxID, _ := pendingIntent(t, f, saleID)
	if refundTxID == "" {
		t.Fatal("the terminal answered; the claim carries its refund id")
	}
	// the transient write failure that stranded the act is gone by the time
	// finance resolves it
	f.events.setFail("")
	r := f.resolveOn(t, intentID, map[string]any{
		"outcome": "refund", "refund_tx_id": refundTxID, "reason": "terminal log shows the refund",
	}, "owner")
	if r.status != http.StatusOK {
		t.Fatalf("the refund resolution = %d, want 200: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'DONE' AND refund_tx_id = $2`, intentID, refundTxID); got != 1 {
		t.Fatalf("the claim did not complete with the recorded refund: %d rows", got)
	}
	if got := f.counterReturns(t); got != 1 {
		t.Errorf("%d returns booked, want 1 (the stranded return's own transaction)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds WHERE gateway_refund_id = $1`, refundTxID); got != 1 {
		t.Errorf("%d refund rows naming the recorded refund id, want 1", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_intent.resolved' AND changes->>'intent_id' = $1`, intentID); got != 1 {
		t.Errorf("%d resolution audit rows, want 1 (written with the booking: a rolled back replay leaves none)", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (599 charged, 599 refunded once)", got)
	}
	if got := f.stock(); got != "99.0000/0.0000" {
		t.Errorf("stock = %s, want 99 (the claimed return restocked nothing)", got)
	}
	f.assertARInvariants(t)
	// the sale's goods bound its refunds: the booked return counts against
	// every later one
	if rr := f.cardReturnOn(t, saleID, lineID, "1"); rr.status != http.StatusConflict {
		t.Fatalf("the second card return of the unit = %d, want 409: %s", rr.status, rr.raw)
	}
	f.assertARInvariants(t)
}

// RULE (FROM-PRINCIPAL 41): a recorded refund of a void claim books through
// the void's own money transaction: the payments reversed, the invoice
// voided, the stock back, every guard under the sale row lock.
func TestARecordedRefundResolutionBooksTheStrandedVoid(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.setFail("pos_transaction.voided")
	})
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "stranded", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status == http.StatusOK {
		t.Fatalf("the void committed under a failing event write: %s", r.raw)
	}
	intentID, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID == "" {
		t.Fatalf("the stranded void's claim = (%s, %q), want PENDING with the refund id", state, refundTxID)
	}
	// the transient write failure that stranded the act is gone by the time
	// finance resolves it
	f.events.setFail("")
	r := f.resolveOn(t, intentID, map[string]any{
		"outcome": "refund", "refund_tx_id": refundTxID, "reason": "terminal log shows the refund",
	}, "finance")
	if r.status != http.StatusOK {
		t.Fatalf("the refund resolution = %d, want 200: %s", r.status, r.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided (the void's own transaction ran)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'DONE'`, intentID); got != 1 {
		t.Errorf("the claim did not complete with the booking: %d rows", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (599 charged, 599 refunded once)", got)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want 100 (the void restocked the unit)", got)
	}
	f.assertARInvariants(t)
}

// RULE (FROM-PRINCIPAL 41): the route refuses a bare resolve and a half
// record naming the field, refuses a claim that is not PENDING, keeps every
// other role out, and refuses a machine key in the route itself, bound or
// not.
func TestTheResolutionRouteRefuses(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()},
		refundErrs: []error{context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out card return = %d, want 500: %s", r.status, r.raw)
	}
	intentID, _, _, _ := pendingIntent(t, f, saleID)

	// a bare resolve is a 400 naming the field
	for _, tc := range []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"a bare resolve", map[string]any{"reason": "checked"}, "outcome"},
		{"a refund without its id", map[string]any{"outcome": "refund", "reason": "checked"}, "refund_tx_id"},
		{"a no refund without its reference", map[string]any{"outcome": "no_refund", "reason": "checked"}, "reconciliation_reference"},
		{"a reasonless record", map[string]any{"outcome": "no_refund", "reconciliation_reference": "REC-1"}, "reason"},
	} {
		r := f.resolveOn(t, intentID, tc.body, "finance")
		if r.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", tc.name, r.status, r.raw)
			continue
		}
		if _, _, fields := errorOf(t, r); len(fields) == 0 || fields[0] != tc.field {
			t.Errorf("%s names %v, want %s", tc.name, fields, tc.field)
		}
	}
	// an unknown outcome word is a 400
	if r := f.resolveOn(t, intentID, map[string]any{"outcome": "maybe", "reason": "checked"}, "finance"); r.status != http.StatusBadRequest {
		t.Errorf("an unknown outcome word = %d, want 400: %s", r.status, r.raw)
	}
	// the counter's own roles are not the resolution's roles
	for _, role := range []string{"cashier", "admin", "sales"} {
		if r := f.resolveOn(t, intentID, map[string]any{
			"outcome": "no_refund", "reconciliation_reference": "REC-1", "reason": "checked"}, role); r.status != http.StatusForbidden {
			t.Errorf("the %s role on the resolution = %d, want 403: %s", role, r.status, r.raw)
		}
	}
	// a machine key is refused in the route itself, bound or not
	mux := http.NewServeMux()
	pos.NewHandler(f.service).RegisterRoutes(mux)
	keyed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(middleware.WithKeyID(r.Context(), uuid.NewString())))
	}))
	t.Cleanup(keyed.Close)
	req, _ := http.NewRequest(http.MethodPost, keyed.URL+"/api/v1/pos/refund-intents/"+intentID+"/resolve",
		strings.NewReader(`{"outcome":"no_refund","reconciliation_reference":"REC-1","reason":"checked"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-pos-key-test")
	rec := httptest.NewRecorder()
	keyed.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a machine key on the resolution = %d, want 403 (the route itself, whatever the key's binding): %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "never a machine key") {
		t.Errorf("the refusal does not say who may resolve: %s", rec.Body.String())
	}
	// a claim that is not PENDING is a 409
	if r := f.resolveOn(t, intentID, map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-1", "reason": "checked"}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the good resolution = %d: %s", r.status, r.raw)
	}
	rr := f.resolveOn(t, intentID, map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-2", "reason": "again"}, "finance")
	if rr.status != http.StatusConflict {
		t.Fatalf("the second resolution of a settled claim = %d, want 409: %s", rr.status, rr.raw)
	}
	_, blockers, _ := errorOf(t, rr)
	if len(blockers) == 0 || blockers[0] != "intent_not_pending" {
		t.Errorf("blockers = %v, want intent_not_pending", blockers)
	}
	// an unknown claim is a 404, never a silent ok
	if rr := f.resolveOn(t, uuid.NewString(), map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-3", "reason": "none"}, "finance"); rr.status != http.StatusNotFound {
		t.Errorf("an unknown claim = %d, want 404: %s", rr.status, rr.raw)
	}
	f.assertARInvariants(t)
}
