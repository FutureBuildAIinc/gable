// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

// CP-05 P2-1: the counter's way back to a Run card is the Run driver's own
// reversal, the one shaped to Run's public page: the leg's idempotency key
// goes as user_identifier, a void sends no amount, a retried key is never
// sent twice, and a Run C decline is a definite decline, not an unknown
// outcome. Proven on the scripted Run fake.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
)

func runWayBack(t *testing.T) (*runFakeServer, *payment.RunTerminalGateway) {
	t.Helper()
	fs := newRunFake(t)
	term := payment.NewRunTerminal(func() payment.GatewayConfig {
		return payment.GatewayConfig{APIKey: "teststub" + "teststub", MID: "MID-TESTSTUB", BaseURL: fs.server.URL}
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return fs, payment.NewRunTerminalGateway(term)
}

func TestRunWayBackRefundSendsTheKeyAndTheAmount(t *testing.T) {
	fs, gw := runWayBack(t)
	res, err := payment.RefundUnderKey(context.Background(), gw, "TRX-SALE1", 1250, "refund-key-1")
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if res.Status != payment.GatewayStatusRefunded {
		t.Fatalf("status %s, want REFUNDED", res.Status)
	}
	posts := fs.fake.voidPosts_()
	if len(posts) != 1 {
		t.Fatalf("%d void-or-refund posts, want 1", len(posts))
	}
	p := posts[0]
	if p["user_identifier"] != "refund-key-1" {
		t.Errorf("user_identifier = %v, want the idempotency key", p["user_identifier"])
	}
	if p["trans_id"] != "TRX-SALE1" || p["amount"] != float64(1250) {
		t.Errorf("trans_id %v amount %v, want TRX-SALE1 and 1250 cents", p["trans_id"], p["amount"])
	}
	if _, ok := p["action"]; ok {
		t.Errorf("the body carries an action field Run's page does not document: %v", p)
	}
}

func TestRunWayBackVoidOmitsTheAmount(t *testing.T) {
	fs, gw := runWayBack(t)
	res, err := payment.VoidUnderKey(context.Background(), gw, "TRX-SALE2", "void-key-1")
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if res.Status != payment.GatewayStatusVoided {
		t.Fatalf("status %s, want VOIDED", res.Status)
	}
	p := fs.fake.voidPosts_()[0]
	if _, ok := p["amount"]; ok {
		t.Errorf("a void sent an amount: %v", p)
	}
	if p["user_identifier"] != "void-key-1" {
		t.Errorf("user_identifier = %v, want the idempotency key", p["user_identifier"])
	}
}

func TestRunWayBackNeverSendsAKeyTwice(t *testing.T) {
	fs, gw := runWayBack(t)
	if _, err := payment.RefundUnderKey(context.Background(), gw, "TRX-SALE3", 500, "refund-key-3"); err != nil {
		t.Fatalf("first refund: %v", err)
	}
	// the guard's refusal is an unknown (CP-05 round 4 P1-E): the first
	// attempt's outcome is not known here, so it is never a refund
	res, err := payment.RefundUnderKey(context.Background(), gw, "TRX-SALE3", 500, "refund-key-3")
	if err != nil || res.Status != payment.GatewayStatusError {
		t.Fatalf("the retried key answered %v, %v; want ERROR (unknown) with a nil error", res, err)
	}
	if n := len(fs.fake.voidPosts_()); n != 1 {
		t.Fatalf("%d posts, want 1", n)
	}
}

func TestRunWayBackSendsNothingWithoutAKey(t *testing.T) {
	fs, gw := runWayBack(t)
	if _, err := gw.Refund(context.Background(), "TRX-SALE4", 500); err == nil {
		t.Fatal("a refund with no key was sent")
	}
	if _, err := gw.Void(context.Background(), "TRX-SALE4"); err == nil {
		t.Fatal("a void with no key was sent")
	}
	if n := len(fs.fake.voidPosts_()); n != 0 {
		t.Fatalf("%d posts, want 0", n)
	}
}

// A Run C is the processor saying no: a definite decline the counter files as
// a failed refund, not a transport error that leaves the claim standing as an
// unknown outcome.
func TestRunWayBackADeclineIsDefinite(t *testing.T) {
	fs, gw := runWayBack(t)
	fs.fake.voidAnswer = func(map[string]any) map[string]any {
		return map[string]any{"result": "C", "trans_id": "", "resp_code": "05", "resp_text": "DECLINE"}
	}
	res, err := payment.RefundUnderKey(context.Background(), gw, "TRX-SALE5", 500, "refund-key-5")
	if err != nil {
		t.Fatalf("a Run C decline came back as an error (an unknown outcome): %v", err)
	}
	if res.Status != payment.GatewayStatusDeclined {
		t.Fatalf("status %s, want DECLINED", res.Status)
	}
}

// A Run B (retry) says nothing about the money, so it stays an unknown. It
// answers status ERROR with a nil error (CP-05 round 3b), on a void and on a
// refund alike: the counter reads ERROR as unknown, so no refund is sent on
// top of a void that may have gone through. An error would send the
// counter's reversal on to the refund, risking a double reversal.
func TestRunWayBackARetryAnswerIsUnknown(t *testing.T) {
	fs, gw := runWayBack(t)
	fs.fake.voidAnswer = func(map[string]any) map[string]any {
		return map[string]any{"result": "B", "resp_code": "91", "resp_text": "RETRY"}
	}
	res, err := payment.VoidUnderKey(context.Background(), gw, "TRX-SALE6", "void-key-6")
	if err != nil {
		t.Fatalf("a Run B on a void came back as an error (the counter would refund on top): %v", err)
	}
	if res.Status != payment.GatewayStatusError {
		t.Fatalf("a Run B on a void was filed as %s, want ERROR (unknown)", res.Status)
	}
	res, err = payment.RefundUnderKey(context.Background(), gw, "TRX-SALE6", 500, "refund-key-6")
	if err != nil || res.Status != payment.GatewayStatusError {
		t.Fatalf("a Run B on a refund answered %v, %v; want ERROR with a nil error", res, err)
	}
}

// CP-05 round 3 P2-B: the reversal guard is keyed apart from the charge
// guard. A charge, then its reversal under the same intent key (the manager's
// resolution of a lost sale leg reverses it under the leg's own key), sends
// exactly one reversal POST; the charge's guard does not swallow it, and the
// reversal is still never sent twice.
func TestRunAReversalUnderTheChargeKeyIsSentOnce(t *testing.T) {
	ledger, branch := newRunLedger(t)
	runUntilItSticks(t, func(t *testing.T) bool {
		sale := runSaleRow(t, branch)
		fs := newRunFake(t)
		term := newRunDriver(t, fs, ledger)
		in, ok := claimRunLegAt(t, ledger, sale, branch, 2500)
		if !ok {
			return false
		}
		d := payment.TerminalDispatch{IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents,
			RegisterID: in.RegisterID, DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey}
		if _, err := term.Dispatch(context.Background(), d); err != nil {
			t.Fatalf("the charge: %v", err)
		}
		if fs.fake.chargePostCount() != 1 {
			t.Fatalf("the charge posted %d times, want 1", fs.fake.chargePostCount())
		}
		gw := payment.NewRunTerminalGateway(term)
		res, err := payment.VoidUnderKey(context.Background(), gw, "TRX-LOST1", in.IdempotencyKey)
		if err != nil {
			t.Fatalf("the reversal under the charge's key was refused before any POST: %v", err)
		}
		if res.Status != payment.GatewayStatusVoided {
			t.Fatalf("status %s, want VOIDED", res.Status)
		}
		// the retry is refused by the guard, an unknown (CP-05 round 4 P1-E)
		if res, err := payment.VoidUnderKey(context.Background(), gw, "TRX-LOST1", in.IdempotencyKey); err != nil || res.Status != payment.GatewayStatusError {
			t.Fatalf("the retried reversal answered %v, %v; want ERROR (unknown) with a nil error", res, err)
		}
		posts := fs.fake.voidPosts_()
		if len(posts) != 1 {
			t.Fatalf("%d reversal posts, want exactly 1", len(posts))
		}
		if posts[0]["user_identifier"] != in.IdempotencyKey {
			t.Errorf("user_identifier = %v, want the intent key unchanged on the wire", posts[0]["user_identifier"])
		}
		return true
	})
}

// CP-05 round 4 P1-E: a void that fails after its POST went out (here an
// HTTP error status) may have gone through at Run, so it is an unknown:
// status ERROR with a nil error, never an error the counter would send on to
// a refund on top.
func TestRunWayBackAFailedPostIsUnknown(t *testing.T) {
	fs, gw := runWayBack(t)
	fs.fake.voidStatus = http.StatusInternalServerError
	res, err := payment.VoidUnderKey(context.Background(), gw, "TRX-SALE7", "void-key-7")
	if err != nil {
		t.Fatalf("a failed void POST came back as an error (the counter would refund on top): %v", err)
	}
	if res.Status != payment.GatewayStatusError {
		t.Fatalf("a failed void POST was filed as %s, want ERROR (unknown)", res.Status)
	}
	if n := len(fs.fake.voidPosts_()); n != 1 {
		t.Fatalf("%d posts, want 1", n)
	}
}
