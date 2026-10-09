// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// voidingGateway approves every charge but voids the invoice while the charge
// is at the gateway, the window between the service's early check and its
// transaction. It records the reversals asked of it; voidErr makes the
// same-day void fail so the refund fallback is exercised.
type voidingGateway struct {
	approvingGateway
	db        *database.DB
	invoiceID uuid.UUID
	voidErr   error
	refundErr error
	// cancel, when set, runs inside Charge: the client gives up while the
	// charge is at the gateway.
	cancel func()
	// ctxErrAtReversal records the context error each reversal call saw.
	ctxErrAtReversal []error
	voided           []string
	refunded         map[string]int64
}

func (g *voidingGateway) Charge(ctx context.Context, req payment.ChargeRequest) (*payment.GatewayResult, error) {
	res, err := g.approvingGateway.Charge(ctx, req)
	if err != nil {
		return nil, err
	}
	if _, err := g.db.Pool.Exec(ctx, `UPDATE invoices SET status = 'VOID', voided_at = now(), voided_on = current_date WHERE id = $1`, g.invoiceID); err != nil {
		return nil, err
	}
	if g.cancel != nil {
		g.cancel()
	}
	return res, nil
}

func (g *voidingGateway) Void(ctx context.Context, txID string) (*payment.GatewayResult, error) {
	g.ctxErrAtReversal = append(g.ctxErrAtReversal, ctx.Err())
	if g.voidErr != nil {
		return nil, g.voidErr
	}
	g.voided = append(g.voided, txID)
	return &payment.GatewayResult{TransactionID: txID, Status: payment.GatewayStatusApproved}, nil
}

func (g *voidingGateway) Refund(ctx context.Context, txID string, cents int64) (*payment.GatewayResult, error) {
	g.ctxErrAtReversal = append(g.ctxErrAtReversal, ctx.Err())
	if g.refundErr != nil {
		return nil, g.refundErr
	}
	if g.refunded == nil {
		g.refunded = map[string]int64{}
	}
	g.refunded[txID] = cents
	return &payment.GatewayResult{TransactionID: txID, Status: payment.GatewayStatusApproved}, nil
}

// RULE (review F3): a charge the gateway approved for an invoice that a void
// committed during the call is reversed through the gateway before the
// refusal returns, the customer is not left charged with no document.
func TestCardChargeOnAnInvoiceVoidedDuringTheCallIsReversed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		voidErr error
		outcome string
		message string
	}{
		{"same day void", nil, "voided", "the card charge was voided"},
		{"settled, so refund", errors.New("cannot void a settled capture"), "refunded", "the card charge was refunded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, inv, db := cardPaymentFixture(t)
			gw := &voidingGateway{db: db, invoiceID: inv.ID, voidErr: tc.voidErr}
			svc.WithGateway(gw, "pk-test")

			_, err := svc.ProcessCardPayment(context.Background(), inv.ID, "tok_test", 1000, "")
			if !errors.Is(err, payment.ErrInvoiceVoid) {
				t.Fatalf("err = %v, want ErrInvoiceVoid", err)
			}
			if errors.Is(err, payment.ErrChargeNotReversed) || !strings.Contains(err.Error(), tc.message) {
				t.Errorf("err = %q, want it to say %q", err, tc.message)
			}
			txID := "gw-" + inv.ID.String()[:8]
			if tc.voidErr == nil {
				if len(gw.voided) != 1 || gw.voided[0] != txID || len(gw.refunded) != 0 {
					t.Errorf("voided %v refunded %v, want the one void of %s", gw.voided, gw.refunded, txID)
				}
			} else if gw.refunded[txID] != 1000 {
				t.Errorf("refunded %v, want %s refunded 1000 after the void failed", gw.refunded, txID)
			}
			var n int
			if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM payments WHERE invoice_id = $1`, inv.ID).Scan(&n); err != nil || n != 0 {
				t.Errorf("%d payments recorded (err %v), want none", n, err)
			}
			assertReversalAudit(t, db, inv.ID, txID, tc.outcome)
		})
	}
}

// assertReversalAudit reads the durable record of a reversal: one audit row
// naming the invoice, the gateway transaction id and the outcome, and no card
// data. It is written after the rolled back transaction ended, so it survives.
func assertReversalAudit(t *testing.T, db *database.DB, invoiceID uuid.UUID, txID, outcome string) {
	t.Helper()
	var changes []byte
	var n int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*), coalesce(max(changes::text), '{}')::bytea FROM audit_log WHERE action = 'payment.charge_reversal' AND entity_type = 'invoice' AND entity_id = $1`,
		invoiceID).Scan(&n, &changes); err != nil || n != 1 {
		t.Fatalf("%d reversal audit rows (err %v), want 1", n, err)
	}
	var got map[string]any
	if err := json.Unmarshal(changes, &got); err != nil {
		t.Fatalf("audit changes: %v", err)
	}
	if got["gateway_tx_id"] != txID || got["outcome"] != outcome {
		t.Errorf("audit changes %v, want gateway_tx_id %s outcome %s", got, txID, outcome)
	}
	for _, banned := range []string{"card_last4", "card_brand", "token_id", "auth_code"} {
		if _, ok := got[banned]; ok {
			t.Errorf("audit changes carry %s", banned)
		}
	}
}

// RULE (review round 3 P2-1): when the void and the refund both fail the card
// WAS charged and nothing gave it back. The caller gets a distinct error, not
// the plain void invoice refusal; the handler answers a 5xx that says the
// card was charged and names the gateway transaction id (the id only); and
// the outcome is on record.
func TestCardChargeThatCannotBeReversedIsLoudAndRecorded(t *testing.T) {
	failing := func(db *database.DB, invoiceID uuid.UUID) *voidingGateway {
		return &voidingGateway{db: db, invoiceID: invoiceID,
			voidErr: errors.New("void refused"), refundErr: errors.New("refund refused")}
	}

	// The service: the distinct error, the audit row.
	svc, inv, db := cardPaymentFixture(t)
	svc.WithGateway(failing(db, inv.ID), "pk-test")
	_, err := svc.ProcessCardPayment(context.Background(), inv.ID, "tok_test", 1000, "")
	if !errors.Is(err, payment.ErrChargeNotReversed) {
		t.Fatalf("err = %v, want ErrChargeNotReversed", err)
	}
	if errors.Is(err, payment.ErrInvoiceVoid) {
		t.Errorf("err = %v must not read as the plain void refusal", err)
	}
	assertReversalAudit(t, db, inv.ID, "gw-"+inv.ID.String()[:8], "failed")

	// The handler: a 5xx saying the card was charged, with the id only.
	svc, inv, db = cardPaymentFixture(t)
	svc.WithGateway(failing(db, inv.ID), "pk-test")
	txID := "gw-" + inv.ID.String()[:8]
	body, _ := json.Marshal(payment.ProcessCardPaymentRequest{InvoiceID: inv.ID, TokenID: "tok_test", Amount: 1000})
	rec := httptest.NewRecorder()
	payment.NewHandler(svc).ProcessCardPayment(rec, httptest.NewRequest(http.MethodPost, "/api/v1/payments/card", bytes.NewReader(body)))
	if rec.Code < 500 {
		t.Fatalf("status %d, want a 5xx", rec.Code)
	}
	msg := rec.Body.String()
	for _, want := range []string{"charged", "reconcile", txID} {
		if !strings.Contains(msg, want) {
			t.Errorf("body %s lacks %q", msg, want)
		}
	}
	for _, banned := range []string{"4242", "VISA", "tok_test", "TESTOK", "refund refused"} {
		if strings.Contains(msg, banned) {
			t.Errorf("body carries %q", banned)
		}
	}
}

// RULE (review round 3 P2-1, handler side): a reversal that worked keeps the
// 409 and the message says which reversal happened.
func TestCardRefusalOverTheWireSaysWhichReversalHappened(t *testing.T) {
	svc, inv, db := cardPaymentFixture(t)
	svc.WithGateway(&voidingGateway{db: db, invoiceID: inv.ID}, "pk-test")
	body, _ := json.Marshal(payment.ProcessCardPaymentRequest{InvoiceID: inv.ID, TokenID: "tok_test", Amount: 1000})
	rec := httptest.NewRecorder()
	payment.NewHandler(svc).ProcessCardPayment(rec, httptest.NewRequest(http.MethodPost, "/api/v1/payments/card", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "voided") {
		t.Errorf("status %d body %s, want 409 saying the charge was voided", rec.Code, rec.Body.String())
	}
}

// RULE (review round 3 P3-1): the reversal does not ride the request's
// context. A client that gives up while the charge is at the gateway must not
// stop the charge being given back.
func TestReversalSurvivesACancelledRequestContext(t *testing.T) {
	svc, inv, db := cardPaymentFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw := &voidingGateway{db: db, invoiceID: inv.ID, cancel: cancel}
	svc.WithGateway(gw, "pk-test")

	_, err := svc.ProcessCardPayment(ctx, inv.ID, "tok_test", 1000, "")
	if err == nil || errors.Is(err, payment.ErrChargeNotReversed) {
		t.Fatalf("err = %v, want a refusal after a reversal", err)
	}
	if len(gw.voided) != 1 {
		t.Fatalf("voided %v, want the one void despite the cancelled request", gw.voided)
	}
	for _, e := range gw.ctxErrAtReversal {
		if e != nil {
			t.Errorf("the reversal saw a context error: %v", e)
		}
	}
	assertReversalAudit(t, db, inv.ID, "gw-"+inv.ID.String()[:8], "voided")
}
