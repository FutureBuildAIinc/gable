// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

// The card route's reversal seam (ADR 0005 9.4, the PR 46 round 4 review
// carried into C2-4): a charge the gateway approved and the system refused is
// given back before the refusal returns; the 502 when nothing gave it back;
// and a failing audit write during the reversal never hides the outcome from
// the caller.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

const cardBranch = `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`

// approvingGateway approves every charge with a stable transaction id.
type approvingGateway struct {
	txPrefix string
	voidErr  error
	// voids flips the named invoice to VOID inside Charge, the window between
	// the route's early check and its transaction.
	voidInvoice func()
	voids       []string
	// cancel ends the request's context inside Charge: the client gave up
	// while the charge was at the gateway.
	cancel      func()
	voidCtxErrs []error
	refunds     map[string]int64
	refundErr   error
}

func (g *approvingGateway) Charge(ctx context.Context, req payment.ChargeRequest) (*payment.GatewayResult, error) {
	tx := g.txPrefix + "-" + req.CustomerID[:8]
	if g.voidInvoice != nil {
		g.voidInvoice()
	}
	if g.cancel != nil {
		g.cancel()
	}
	return &payment.GatewayResult{TransactionID: tx, Status: payment.GatewayStatusApproved,
		CardLast4: "4242", CardBrand: "visa", AuthCode: "A1"}, nil
}

func (g *approvingGateway) Void(ctx context.Context, txID string) (*payment.GatewayResult, error) {
	g.voidCtxErrs = append(g.voidCtxErrs, ctx.Err())
	if g.voidErr != nil {
		return nil, g.voidErr
	}
	g.voids = append(g.voids, txID)
	return &payment.GatewayResult{TransactionID: txID, Status: payment.GatewayStatusApproved}, nil
}

func (g *approvingGateway) Refund(ctx context.Context, txID string, cents int64) (*payment.GatewayResult, error) {
	if g.refundErr != nil {
		return nil, g.refundErr
	}
	if g.refunds == nil {
		g.refunds = map[string]int64{}
	}
	g.refunds[txID] = cents
	return &payment.GatewayResult{TransactionID: txID, Status: payment.GatewayStatusApproved}, nil
}

func (g *approvingGateway) Capture(ctx context.Context, txID string, cents int64) (*payment.GatewayResult, error) {
	return &payment.GatewayResult{TransactionID: txID, Status: payment.GatewayStatusApproved}, nil
}

type cardWorld struct {
	t          *testing.T
	db         *database.DB
	svc        *payment.Service
	customerID uuid.UUID
	invoiceID  uuid.UUID
}

func newCardWorld(t *testing.T) *cardWorld {
	t.Helper()
	db := testutil.RequireDB(t)
	w := &cardWorld{t: t, db: db, customerID: uuid.New(), invoiceID: uuid.New()}
	must := func(sql string, args ...any) {
		if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
		}
	}
	must(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Card Co', $2, `+cardBranch+`)`,
		w.customerID, "CARD-"+uuid.NewString()[:8])
	must(`INSERT INTO invoices (id, customer_id, branch_id, status, origin, number, currency, total_amount, subtotal,
			tax_rate, tax_amount, amount_open, invoice_date)
		VALUES ($1, $2, `+cardBranch+`, 'UNPAID', 'POS', $3, 'USD', 50, 50, 0, 0, 50, CURRENT_DATE)`,
		w.invoiceID, w.customerID, "IN-"+w.invoiceID.String()[:8])

	glSvc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	acct := account.NewService(db, glSvc, slog.Default())
	w.svc = payment.NewService(db, payment.NewRepository(db), acct).
		WithAuditLog(audit.NewLogger(db)).WithOutbox(outbox.NewWriter(db, ""))

	t.Cleanup(func() {
		ctx := context.Background()
		exec := func(sql string, args ...any) { _, _ = db.Pool.Exec(ctx, sql, args...) }
		exec(`DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT e.id FROM gl_journal_entries e WHERE e.source_ref_id IN (SELECT id FROM invoices WHERE customer_id = $1) OR e.source_ref_id IN (SELECT id FROM payments WHERE customer_id = $1))`, w.customerID)
		exec(`DELETE FROM gl_journal_entries WHERE source_ref_id IN (SELECT id FROM invoices WHERE customer_id = $1) OR source_ref_id IN (SELECT id FROM payments WHERE customer_id = $1)`, w.customerID)
		exec(`DELETE FROM events_outbox WHERE entity_id IN (SELECT id FROM payments WHERE customer_id = $1)`, w.customerID)
		exec(`DELETE FROM audit_log WHERE entity_id = $1 OR entity_id IN (SELECT id FROM payments WHERE customer_id = $1)`, w.customerID)
		exec(`DELETE FROM customer_transactions WHERE customer_id = $1`, w.customerID)
		exec(`DELETE FROM ar_applications WHERE customer_id = $1`, w.customerID)
		exec(`DELETE FROM payments WHERE customer_id = $1`, w.customerID)
		exec(`DELETE FROM invoice_lines WHERE invoice_id = $1`, w.invoiceID)
		exec(`DELETE FROM invoices WHERE customer_id = $1`, w.customerID)
		exec(`DELETE FROM customers WHERE id = $1`, w.customerID)
	})
	return w
}

// failingAudit fails every write, standing in for an audit store that is down
// during a reversal.
type failingAudit struct{ inner payment.AuditSink }

func (f failingAudit) Log(ctx context.Context, e audit.Entry) error {
	return errors.New("audit store unreachable")
}

// A charge approved for an invoice a void committed during the gateway call is
// reversed before the refusal returns; the customer is not left charged with
// no document, and the durable record of the reversal is the audit row.
func TestCardChargeReversedWhenTheRefusalComesAfterTheCharge(t *testing.T) {
	for _, tc := range []struct {
		name    string
		voidErr error
		outcome string
	}{
		{name: "same day void", outcome: "voided"},
		{name: "settled, so refund", voidErr: errors.New("cannot void a settled capture"), outcome: "refunded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCardWorld(t)
			gw := &approvingGateway{txPrefix: "gw", voidErr: tc.voidErr}
			gw.voidInvoice = func() {
				if _, err := w.db.Pool.Exec(context.Background(),
					`UPDATE invoices SET status = 'VOID', voided_at = now(), voided_on = current_date WHERE id = $1`, w.invoiceID); err != nil {
					t.Fatal(err)
				}
			}
			w.svc.WithGateway(gw, "pk-test")

			_, err := w.svc.CreateCard(context.Background(), &payment.Input{
				CustomerID: w.customerID, AmountCents: 5000, Method: payment.PaymentMethodCard, TokenID: "tok_test",
				Applications: []account.ApplyLine{{InvoiceID: w.invoiceID, AmountCents: 5000}},
			}, payment.Caller{Actor: "u-test"})
			if err == nil {
				t.Fatal("the refused card payment was recorded")
			}
			if !strings.Contains(err.Error(), "the invoice is void") {
				t.Errorf("err = %v, want the invoice_void refusal", err)
			}
			if strings.Contains(err.Error(), "could not be reversed") {
				t.Errorf("err = %v, want the reversal to have succeeded", err)
			}
			if !strings.Contains(err.Error(), "the card charge was "+tc.outcome) {
				t.Errorf("err = %v, want it to say the charge was %s", err, tc.outcome)
			}
			var n int
			if err := w.db.Pool.QueryRow(context.Background(),
				`SELECT count(*) FROM payments WHERE customer_id = $1`, w.customerID).Scan(&n); err != nil || n != 0 {
				t.Errorf("%d payments recorded (err %v), want none", n, err)
			}
			var audited string
			if err := w.db.Pool.QueryRow(context.Background(),
				`SELECT changes->>'outcome' FROM audit_log WHERE action = 'payment.charge_reversal' AND entity_id = $1`, w.customerID).Scan(&audited); err != nil || audited != tc.outcome {
				t.Errorf("reversal audit outcome = %q (err %v), want %q", audited, err, tc.outcome)
			}
		})
	}
}

// When neither the void nor the refund gives the money back, the route's
// answer is the 502 charge_not_reversed carrying the gateway transaction id
// and nothing else about the card.
func TestCardChargeNotReversedAnswers502WithTheGatewayTransaction(t *testing.T) {
	w := newCardWorld(t)
	gw := &approvingGateway{txPrefix: "gwtx", voidErr: errors.New("void refused"), refundErr: errors.New("refund refused")}
	gw.voidInvoice = func() {
		if _, err := w.db.Pool.Exec(context.Background(),
			`UPDATE invoices SET status = 'VOID', voided_at = now(), voided_on = current_date WHERE id = $1`, w.invoiceID); err != nil {
			t.Fatal(err)
		}
	}
	w.svc.WithGateway(gw, "pk-test")

	_, err := w.svc.CreateCard(context.Background(), &payment.Input{
		CustomerID: w.customerID, AmountCents: 5000, Method: payment.PaymentMethodCard, TokenID: "tok_test",
		Applications: []account.ApplyLine{{InvoiceID: w.invoiceID, AmountCents: 5000}},
	}, payment.Caller{Actor: "u-test"})
	var cnr *payment.ChargeNotReversedError
	if !errors.As(err, &cnr) {
		t.Fatalf("err = %v, want ChargeNotReversedError", err)
	}
	if !strings.HasPrefix(cnr.GatewayTxID, "gwtx-") {
		t.Errorf("gateway tx = %q, want the one the gateway drew", cnr.GatewayTxID)
	}
	wire := cnr.Wire()
	if wire.Status != 502 || wire.Code != httpx.CodeChargeNotReversed {
		t.Errorf("wire = %d %s, want 502 charge_not_reversed", wire.Status, wire.Code)
	}
	if !strings.Contains(wire.Message, cnr.GatewayTxID) {
		t.Errorf("wire message = %q, want the gateway transaction id in it", wire.Message)
	}
	var n int
	if err := w.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM payments WHERE customer_id = $1`, w.customerID).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d payments recorded (err %v), want none", n, err)
	}
}

// CARRIED (PR 46 round 4 review): the audit logger failing during the
// reversal never hides the outcome from the caller: the refusal still names
// what happened to the charge, the gateway was still called, and no payment
// was recorded.
func TestCardReversalOutcomeSurvivesAFailingAuditLogger(t *testing.T) {
	w := newCardWorld(t)
	gw := &approvingGateway{txPrefix: "gwaud"}
	gw.voidInvoice = func() {
		if _, err := w.db.Pool.Exec(context.Background(),
			`UPDATE invoices SET status = 'VOID', voided_at = now(), voided_on = current_date WHERE id = $1`, w.invoiceID); err != nil {
			t.Fatal(err)
		}
	}
	w.svc.WithGateway(gw, "pk-test").WithAuditLog(failingAudit{})

	_, err := w.svc.CreateCard(context.Background(), &payment.Input{
		CustomerID: w.customerID, AmountCents: 5000, Method: payment.PaymentMethodCard, TokenID: "tok_test",
		Applications: []account.ApplyLine{{InvoiceID: w.invoiceID, AmountCents: 5000}},
	}, payment.Caller{Actor: "u-test"})
	if err == nil {
		t.Fatal("the refused card payment was recorded")
	}
	if strings.Contains(err.Error(), "audit") {
		t.Errorf("err = %v, want the refusal, not the audit failure", err)
	}
	if !strings.Contains(err.Error(), "the card charge was voided") {
		t.Errorf("err = %v, want the reversal outcome to reach the caller", err)
	}
	if len(gw.voids) != 1 || !strings.HasPrefix(gw.voids[0], "gwaud-") {
		t.Errorf("voided %v, want the one reversal at the gateway", gw.voids)
	}
	var n int
	if err := w.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM payments WHERE customer_id = $1`, w.customerID).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d payments recorded (err %v), want none", n, err)
	}
}

// RULE (PR 46 round 3 P3-1, carried): the reversal does not ride the request's
// context. A client that gives up while the charge is at the gateway must not
// stop the charge being given back.
func TestReversalSurvivesACancelledRequestContext(t *testing.T) {
	w := newCardWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw := &approvingGateway{txPrefix: "gwcancel", cancel: cancel}
	gw.voidInvoice = func() {
		if _, err := w.db.Pool.Exec(context.Background(),
			`UPDATE invoices SET status = 'VOID', voided_at = now(), voided_on = current_date WHERE id = $1`, w.invoiceID); err != nil {
			t.Fatal(err)
		}
	}
	w.svc.WithGateway(gw, "pk-test")

	_, err := w.svc.CreateCard(ctx, &payment.Input{
		CustomerID: w.customerID, AmountCents: 5000, Method: payment.PaymentMethodCard, TokenID: "tok_test",
		Applications: []account.ApplyLine{{InvoiceID: w.invoiceID, AmountCents: 5000}},
	}, payment.Caller{Actor: "u-test"})
	if err == nil || errors.Is(err, payment.ErrChargeNotReversed) {
		t.Fatalf("err = %v, want a refusal after a reversal", err)
	}
	if len(gw.voids) != 1 {
		t.Fatalf("voided %v, want the one void despite the cancelled request", gw.voids)
	}
	for _, e := range gw.voidCtxErrs {
		if e != nil {
			t.Errorf("the reversal saw a context error: %v", e)
		}
	}
	var outcome string
	if err := w.db.Pool.QueryRow(context.Background(),
		`SELECT changes->>'outcome' FROM audit_log WHERE action = 'payment.charge_reversal' AND entity_id = $1`, w.customerID).Scan(&outcome); err != nil || outcome != "voided" {
		t.Errorf("reversal audit outcome = %q (err %v), want voided", outcome, err)
	}
}
