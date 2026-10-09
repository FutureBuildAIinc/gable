// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	glint "github.com/gablelbm/gable/internal/integrations/gl"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// TestProcessPaymentAuditRollsBackWithFailedMutation runs the real
// ProcessPayment inside a caller's transaction that then fails: the payment,
// its account posting and its audit row all ride that transaction, so a
// failed mutation leaves no audit row claiming a payment that never happened.
func TestProcessPaymentAuditRollsBackWithFailedMutation(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	logger := slog.Default()

	glSvc := gl.NewService(gl.NewRepository(db), glint.NewMockGLAdapter(), logger)
	accountSvc := account.NewService(account.NewRepository(db), db, logger)
	invoiceSvc := invoice.NewService(invoice.NewRepository(db), glSvc, accountSvc, db).
		WithAuditLog(audit.NewLogger(db))
	svc := payment.NewService(db, payment.NewRepository(db), invoice.NewRepository(db), accountSvc).
		WithAuditLog(audit.NewLogger(db))

	custID := uuid.New()
	productID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ($1, 'Audit Rollback Customer', $2,
		         (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		custID, "AR-"+custID.String()[:8]); err != nil {
		t.Fatalf("insert customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		"INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'Audit Item', 'EA', 50)",
		productID, "AR-"+productID.String()[:8]); err != nil {
		t.Fatalf("insert product: %v", err)
	}

	inv := &invoice.LegacyInvoice{
		ID:         uuid.New(),
		CustomerID: custID,
		Lines:      []invoice.LegacyLine{{ProductID: productID, Quantity: 2, PriceEach: 5000}},
	}
	if err := invoiceSvc.CreateInvoice(ctx, inv); err != nil {
		t.Fatalf("create invoice: %v", err)
	}

	var paid *payment.Payment
	err := db.RunInTx(ctx, func(txCtx context.Context) error {
		var err error
		paid, err = svc.ProcessPayment(txCtx, inv.ID, 1000, payment.PaymentMethodCash, "audit-rollback", "")
		if err != nil {
			return err
		}
		return errors.New("deliberate rollback: the payment and its audit row must both disappear")
	})
	if err == nil {
		t.Fatal("expected the deliberate error from the outer transaction")
	}
	if paid == nil {
		t.Fatal("ProcessPayment did not report success inside the transaction")
	}

	var paymentRows, auditRows int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM payments WHERE id = $1`, paid.ID).Scan(&paymentRows); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'payment' AND entity_id = $1 AND action = 'payment.processed'`,
		paid.ID).Scan(&auditRows); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if paymentRows != 0 || auditRows != 0 {
		t.Fatalf("after rollback: %d payment row(s), %d payment.processed audit row(s); both must be 0",
			paymentRows, auditRows)
	}
}

// approvingGateway stands in for the Run Payments gateway in tests: every
// charge is approved. Only Charge is exercised by the card-path tests.
type approvingGateway struct{}

func (approvingGateway) Charge(_ context.Context, req payment.ChargeRequest) (*payment.GatewayResult, error) {
	return &payment.GatewayResult{
		TransactionID: "gw-" + req.InvoiceID[:8],
		Status:        payment.GatewayStatusApproved,
		AuthCode:      "TESTOK",
		CardLast4:     "4242",
		CardBrand:     "VISA",
		AmountCents:   req.AmountCents,
	}, nil
}

func (approvingGateway) Capture(_ context.Context, _ string, _ int64) (*payment.GatewayResult, error) {
	return nil, errors.New("not implemented in test gateway")
}

func (approvingGateway) Void(_ context.Context, _ string) (*payment.GatewayResult, error) {
	return nil, errors.New("not implemented in test gateway")
}

func (approvingGateway) Refund(_ context.Context, _ string, _ int64) (*payment.GatewayResult, error) {
	return nil, errors.New("not implemented in test gateway")
}

// cardPaymentFixture stands up the real invoice/account/payment stack with an
// approving gateway and one unpaid invoice, the shared setup of the card-path
// audit tests.
func cardPaymentFixture(t *testing.T) (*payment.Service, *invoice.LegacyInvoice, *database.DB) {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()
	logger := slog.Default()

	glSvc := gl.NewService(gl.NewRepository(db), glint.NewMockGLAdapter(), logger)
	accountSvc := account.NewService(account.NewRepository(db), db, logger)
	svc := payment.NewService(db, payment.NewRepository(db), invoice.NewRepository(db), accountSvc).
		WithGateway(approvingGateway{}, "pk-test").
		WithAuditLog(audit.NewLogger(db))

	custID := uuid.New()
	productID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ($1, 'Card Audit Customer', $2,
		         (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		custID, "CA-"+custID.String()[:8]); err != nil {
		t.Fatalf("insert customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		"INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'Card Audit Item', 'EA', 50)",
		productID, "CA-"+productID.String()[:8]); err != nil {
		t.Fatalf("insert product: %v", err)
	}

	// The invoice is created through its own service so its GL and account
	// side effects exist exactly as in production.
	invoiceSvc := invoice.NewService(invoice.NewRepository(db), glSvc, accountSvc, db)
	inv := &invoice.LegacyInvoice{
		ID:         uuid.New(),
		CustomerID: custID,
		Lines:      []invoice.LegacyLine{{ProductID: productID, Quantity: 2, PriceEach: 5000}},
	}
	if err := invoiceSvc.CreateInvoice(ctx, inv); err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	return svc, inv, db
}

func countPaymentAuditRows(t *testing.T, db *database.DB, paymentID uuid.UUID) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE entity_type = 'payment' AND entity_id = $1 AND action = 'payment.processed'`,
		paymentID).Scan(&n); err != nil {
		t.Fatalf("count payment.processed audit rows: %v", err)
	}
	return n
}

// TestProcessCardPaymentAuditWrittenOnCommit proves the card path writes its
// audit row at all: the rollback companion below asserts the row disappears
// with a failed mutation, which is vacuous unless a committed card payment
// leaves exactly one row behind.
func TestProcessCardPaymentAuditWrittenOnCommit(t *testing.T) {
	svc, inv, db := cardPaymentFixture(t)

	paid, err := svc.ProcessCardPayment(context.Background(), inv.ID, "tok_test", 1000, "")
	if err != nil {
		t.Fatalf("ProcessCardPayment: %v", err)
	}

	if n := countPaymentAuditRows(t, db, paid.ID); n != 1 {
		t.Fatalf("committed card payment wrote %d payment.processed audit row(s), want exactly 1", n)
	}
}

// TestProcessCardPaymentAuditRollsBackWithFailedMutation runs the real
// ProcessCardPayment inside a caller's transaction that then fails: the
// payment, its account posting and its audit row all ride that transaction,
// so a failed mutation leaves no audit row claiming a charge that never
// became a payment.
func TestProcessCardPaymentAuditRollsBackWithFailedMutation(t *testing.T) {
	ctx := context.Background()
	svc, inv, db := cardPaymentFixture(t)

	var paid *payment.Payment
	err := db.RunInTx(ctx, func(txCtx context.Context) error {
		var err error
		paid, err = svc.ProcessCardPayment(txCtx, inv.ID, "tok_test", 1000, "")
		if err != nil {
			return err
		}
		return errors.New("deliberate rollback: the card payment and its audit row must both disappear")
	})
	if err == nil {
		t.Fatal("expected the deliberate error from the outer transaction")
	}
	if paid == nil {
		t.Fatal("ProcessCardPayment did not report success inside the transaction")
	}

	var paymentRows int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM payments WHERE id = $1`, paid.ID).Scan(&paymentRows); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	auditRows := countPaymentAuditRows(t, db, paid.ID)
	if paymentRows != 0 || auditRows != 0 {
		t.Fatalf("after rollback: %d payment row(s), %d payment.processed audit row(s); both must be 0",
			paymentRows, auditRows)
	}
}
