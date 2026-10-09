// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

import (
	"context"
	"errors"
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
	voided    []string
	refunded  map[string]int64
}

func (g *voidingGateway) Charge(ctx context.Context, req payment.ChargeRequest) (*payment.GatewayResult, error) {
	res, err := g.approvingGateway.Charge(ctx, req)
	if err != nil {
		return nil, err
	}
	if _, err := g.db.Pool.Exec(ctx, `UPDATE invoices SET status = 'VOID', voided_at = now(), voided_on = current_date WHERE id = $1`, g.invoiceID); err != nil {
		return nil, err
	}
	return res, nil
}

func (g *voidingGateway) Void(_ context.Context, txID string) (*payment.GatewayResult, error) {
	if g.voidErr != nil {
		return nil, g.voidErr
	}
	g.voided = append(g.voided, txID)
	return &payment.GatewayResult{TransactionID: txID, Status: payment.GatewayStatusApproved}, nil
}

func (g *voidingGateway) Refund(_ context.Context, txID string, cents int64) (*payment.GatewayResult, error) {
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
	}{
		{"same day void", nil},
		{"settled, so refund", errors.New("cannot void a settled capture")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, inv, db := cardPaymentFixture(t)
			gw := &voidingGateway{db: db, invoiceID: inv.ID, voidErr: tc.voidErr}
			svc.WithGateway(gw, "pk-test")

			_, err := svc.ProcessCardPayment(context.Background(), inv.ID, "tok_test", 1000, "")
			if !errors.Is(err, payment.ErrInvoiceVoid) {
				t.Fatalf("err = %v, want ErrInvoiceVoid", err)
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
		})
	}
}
