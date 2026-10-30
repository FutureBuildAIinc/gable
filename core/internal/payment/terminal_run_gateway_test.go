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
	if _, err := payment.RefundUnderKey(context.Background(), gw, "TRX-SALE3", 500, "refund-key-3"); err == nil {
		t.Fatal("the retried key was answered: the customer would be refunded twice")
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

// A Run B (retry) says nothing about the money, so it stays an unknown.
func TestRunWayBackARetryAnswerIsNotADecline(t *testing.T) {
	fs, gw := runWayBack(t)
	fs.fake.voidAnswer = func(map[string]any) map[string]any {
		return map[string]any{"result": "B", "resp_code": "91", "resp_text": "RETRY"}
	}
	if res, err := payment.RefundUnderKey(context.Background(), gw, "TRX-SALE6", 500, "refund-key-6"); err == nil {
		t.Fatalf("a Run B was filed as %s: the outcome is unknown", res.Status)
	}
}
