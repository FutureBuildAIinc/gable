// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	glint "github.com/gablelbm/gable/internal/integrations/gl"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
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

	inv := &invoice.Invoice{
		ID:         uuid.New(),
		CustomerID: custID,
		Lines:      []invoice.InvoiceLine{{ProductID: productID, Quantity: 2, PriceEach: 5000}},
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
