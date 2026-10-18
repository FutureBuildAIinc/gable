// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The outcome the operator records cannot contradict the claim's own record
// (ninth review P2-1, tenth review P3-1): the whole point of writing the
// terminal's refund id onto the claim in its own committed write is that a
// PENDING claim carrying one is a refund that moved. A no refund recorded
// over such a claim would release the tender with the money gone; a refund
// recorded under a different id would book the money under an id the terminal
// never issued. Both are refused with 409 naming the conflict, and the claim
// stands.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// strandedReturnWithID strands a card return whose terminal refund moved and
// whose money transaction refused, answering the sale, its first line and
// the standing claim (PENDING, carrying the gateway's refund id).
func strandedReturnWithID(t *testing.T, f *fixture) (saleID, lineID, intentID, refundTxID string) {
	t.Helper()
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID = body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status == http.StatusCreated {
		t.Fatalf("the return committed under a failing event write: %s", r.raw)
	}
	var id, rtx string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id::text, COALESCE(refund_tx_id, '') FROM pos_refund_intents WHERE sale_id = $1 AND state = 'PENDING'`, saleID).
		Scan(&id, &rtx); err != nil {
		t.Fatal(err)
	}
	if rtx == "" {
		t.Fatal("the stranded claim carries no refund id: the terminal's answer never landed")
	}
	return saleID, lineID, id, rtx
}

// RULE: a no refund recorded over a claim that carries the terminal's refund
// id is refused 409 naming the conflict: the money moved, so finance must
// record the refund, never a release.
func TestANoRefundOverARecordedRefundIdIsRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.setFail("pos_return.completed")
	})
	f.openTill(0)
	_, _, intentID, refundTxID := strandedReturnWithID(t, f)
	r := f.resolveOn(t, intentID, map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-X", "reason": "operator error",
	}, "finance")
	if r.status != http.StatusConflict {
		t.Fatalf("the no refund over the recorded id = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_recorded" {
		t.Errorf("blockers = %v, want refund_recorded naming the conflict", blockers)
	}
	if !strings.Contains(string(r.raw), refundTxID) {
		t.Errorf("the refusal does not name the recorded id %s: %s", refundTxID, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'PENDING'`, intentID); got != 1 {
		t.Errorf("the claim was released over its own record: %d PENDING rows, want 1", got)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (the charge stands beside the moved refund)", got)
	}
	f.assertARInvariants(t)
}

// RULE: a refund recorded under an id the terminal never issued, when the
// claim carries the terminal's own, is refused 409 naming the conflict; the
// claim then settles under its own recorded id.
func TestARefundIdThatDiffersFromTheRecordedOneIsRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.setFail("pos_return.completed")
	})
	f.openTill(0)
	_, _, intentID, refundTxID := strandedReturnWithID(t, f)
	r := f.resolveOn(t, intentID, map[string]any{
		"outcome": "refund", "refund_tx_id": "some-other-id", "reason": "operator typo",
	}, "finance")
	if r.status != http.StatusConflict {
		t.Fatalf("the differing refund id = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_tx_id_conflict" {
		t.Errorf("blockers = %v, want refund_tx_id_conflict naming the conflict", blockers)
	}
	if !strings.Contains(string(r.raw), refundTxID) {
		t.Errorf("the refusal does not name the recorded id %s: %s", refundTxID, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'PENDING'`, intentID); got != 1 {
		t.Errorf("the claim was settled under the wrong id: %d PENDING rows, want 1", got)
	}
	// the claim's own recorded id settles it
	f.events.setFail("")
	rr := f.resolveOn(t, intentID, map[string]any{
		"outcome": "refund", "refund_tx_id": refundTxID, "reason": "the terminal's own id",
	}, "finance")
	if rr.status != http.StatusOK {
		t.Fatalf("the resolution under the recorded id = %d, want 200: %s", rr.status, rr.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'DONE' AND refund_tx_id = $2`, intentID, refundTxID); got != 1 {
		t.Errorf("the claim did not settle DONE under its own recorded id")
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds WHERE gateway_refund_id = $1`, refundTxID); got != 1 {
		t.Errorf("the refund row does not name the terminal's own id")
	}
	f.assertARInvariants(t)
}
