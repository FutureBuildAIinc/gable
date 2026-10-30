// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-05 round 4 P1-E: a Run void whose outcome is unknown sends no refund on
// top, whether the unknown came back as an answer (a B, round 3b) or as a
// failure after the POST went out: an HTTP error status, a lost answer, an
// unreadable body, or the reversal guard refusing a retry whose first
// attempt is still unknown. Each case wants exactly one void-or-refund POST,
// the 502, the leg unspent and, for a resolution, the item open again.

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
)

// runScriptServer is a scripted Run void-or-refund endpoint: the first POST
// gets first's answer (an HTTP status, and a JSON body when 200), every
// later POST an A, so a refund sent on top would go through and show.
func runScriptServer(t *testing.T, firstStatus int, firstResult string) (*payment.RunTerminalGateway, *atomic.Int32) {
	t.Helper()
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/void-or-refund" {
			http.NotFound(w, r)
			return
		}
		n := posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 && firstStatus != http.StatusOK {
			w.WriteHeader(firstStatus)
			_, _ = w.Write([]byte(`{"message":"internal error"}`))
			return
		}
		result := "A"
		if n == 1 {
			result = firstResult
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "trans_id": "TRX-REV", "resp_code": "00", "resp_text": result})
	}))
	t.Cleanup(srv.Close)
	term := payment.NewRunTerminal(func() payment.GatewayConfig {
		return payment.GatewayConfig{APIKey: "teststub" + "teststub", MID: "MID-TESTSTUB", BaseURL: srv.URL}
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return payment.NewRunTerminalGateway(term), &posts
}

// RULE: a manager's whole charge resolution whose void answers HTTP 500
// sends no refund: 502, one POST, the item open again, the leg unspent.
func TestCP05R4_AFailedVoidOnAResolutionSendsNoRefund(t *testing.T) {
	gw, posts := runScriptServer(t, http.StatusInternalServerError, "")
	f := terminalFixture(t, payment.SimNoAnswer, nil)
	f.service.WithGateway(gw)
	saleID := f.openSale(t, nil)
	intentID, itemID := f.lostLegOf(t, saleID)
	if r := f.completeSale(saleID, f.cashAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the cash completion = %d: %s", r.status, r.raw)
	}
	r := f.resolveItem(itemID, "authorized", f.recordedRefOf(t, intentID, itemID))
	if r.status != http.StatusBadGateway {
		t.Fatalf("the resolve = %d, wanted 502 (the void's outcome is unknown): %s", r.status, r.raw)
	}
	if n := posts.Load(); n != 1 {
		t.Fatalf("%d void-or-refund posts, wanted only the void (no refund on top of a failed void)", n)
	}
	if got := itemStateOf(t, f, itemID); got != "open" {
		t.Fatalf("the item is %s, wanted open again", got)
	}
	if _, spent, _ := f.intentFacts(t, intentID); spent {
		t.Fatalf("the leg is spent after an unknown reversal")
	}
}

// RULE: a compensation void that answers HTTP 500 sends no refund: the
// completion answers 502, one POST, the sale leg unspent for the sweep.
func TestCP05R4_AFailedCompensationVoidSendsNoRefund(t *testing.T) {
	gw, posts := runScriptServer(t, http.StatusInternalServerError, "")
	f := terminalFixture(t, payment.SimApprove, nil)
	f.service.WithGateway(gw)
	saleID := f.openSale(t, nil)
	mustExec(t, f.db, `UPDATE inventory SET quantity = 0 WHERE product_id = $1`, f.productID)
	r := f.completeSale(saleID, f.cardAll(t, saleID))
	if r.status != http.StatusBadGateway {
		t.Fatalf("the completion = %d, wanted 502 (the reversal is unknown): %s", r.status, r.raw)
	}
	if n := posts.Load(); n != 1 {
		t.Fatalf("%d void-or-refund posts, wanted only the void (no refund on top of a failed void)", n)
	}
	saleLegID, _, _ := f.terminalIntentOfKind(t, saleID, "sale")
	if _, spent, _ := f.intentFacts(t, saleLegID); spent {
		t.Fatalf("the sale leg is spent after an unknown reversal")
	}
}

// RULE: the round 3b case retried in the same process. The first resolve's
// void answers a B (unknown); the manager resolves again, the reversal guard
// refuses the void, and still no refund goes: 502 both times, one POST in
// all, the item open, the leg unspent.
func TestCP05R4_ARetriedResolutionAfterAnUnknownVoidSendsNoRefund(t *testing.T) {
	gw, posts := runScriptServer(t, http.StatusOK, "B")
	f := terminalFixture(t, payment.SimNoAnswer, nil)
	f.service.WithGateway(gw)
	saleID := f.openSale(t, nil)
	intentID, itemID := f.lostLegOf(t, saleID)
	if r := f.completeSale(saleID, f.cashAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the cash completion = %d: %s", r.status, r.raw)
	}
	ref := f.recordedRefOf(t, intentID, itemID)
	for i := 1; i <= 2; i++ {
		r := f.resolveItem(itemID, "authorized", ref)
		if r.status != http.StatusBadGateway {
			t.Fatalf("resolve %d = %d, wanted 502 (the void's outcome is unknown): %s", i, r.status, r.raw)
		}
	}
	if n := posts.Load(); n != 1 {
		t.Fatalf("%d void-or-refund posts, wanted only the first void (no refund on top of an unknown)", n)
	}
	if got := itemStateOf(t, f, itemID); got != "open" {
		t.Fatalf("the item is %s, wanted open again", got)
	}
	if _, spent, _ := f.intentFacts(t, intentID); spent {
		t.Fatalf("the leg is spent after an unknown reversal")
	}
}
