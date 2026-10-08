// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	glint "github.com/gablelbm/gable/internal/integrations/gl"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// TestCompleteTransactionAuditRollsBackWithFailedMutation runs the real POS
// sale completion (StartTransaction and AddItem committed first, exactly as
// the terminal does) inside a caller's transaction that then fails: the
// tenders, the inventory deduction, the completed status and the audit row
// all ride that transaction, so a failed completion leaves the sale OPEN and
// no audit row claiming a completed sale that never happened.
func TestCompleteTransactionAuditRollsBackWithFailedMutation(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	logger := slog.Default()

	glSvc := gl.NewService(gl.NewRepository(db), glint.NewMockGLAdapter(), logger)
	accountSvc := account.NewService(account.NewRepository(db), db, logger)
	invoiceSvc := invoice.NewService(invoice.NewRepository(db), glSvc, accountSvc, db).
		WithAuditLog(audit.NewLogger(db))
	paymentSvc := payment.NewService(db, payment.NewRepository(db), invoice.NewRepository(db), accountSvc).
		WithAuditLog(audit.NewLogger(db))
	svc := pos.NewService(db, pos.NewRepository(db),
		product.NewService(product.NewRepository(db)),
		inventory.NewService(inventory.NewRepository(db)),
		invoiceSvc, paymentSvc, logger).
		WithAuditLog(audit.NewLogger(db))

	productID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		"INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'POS Audit Item', 'EA', 20)",
		productID, "PR-"+productID.String()[:8]); err != nil {
		t.Fatalf("insert product: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		"INSERT INTO inventory (product_id, location, quantity) VALUES ($1, 'POS Audit Rack', 100)",
		productID); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
	registerID := "AUDIT-REG-" + productID.String()[:8]
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO pos_registers (id, location_id, branch_id, name)
		 VALUES ($1,
		         (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'),
		         (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'),
		         'Audit Test Register')`,
		registerID); err != nil {
		t.Fatalf("insert register: %v", err)
	}

	tx, err := svc.StartTransaction(ctx, registerID, uuid.New(), nil)
	if err != nil {
		t.Fatalf("start POS transaction: %v", err)
	}
	if _, err := svc.AddItem(ctx, tx.ID, pos.AddLineItemRequest{ProductID: productID, Quantity: 2}); err != nil {
		t.Fatalf("add item: %v", err)
	}
	open, err := svc.GetTransaction(ctx, tx.ID)
	if err != nil {
		t.Fatalf("reload transaction: %v", err)
	}

	err = db.RunInTx(ctx, func(txCtx context.Context) error {
		_, err := svc.CompleteTransaction(txCtx, tx.ID, []pos.AddTenderRequest{
			{Method: "CASH", Amount: float64(open.Total) / 100.0},
		})
		if err != nil {
			return err
		}
		return errors.New("deliberate rollback: the completed sale and its audit row must both disappear")
	})
	if err == nil {
		t.Fatal("expected the deliberate error from the outer transaction")
	}

	var status string
	if err := db.Pool.QueryRow(ctx,
		`SELECT status FROM pos_transactions WHERE id = $1`, tx.ID).Scan(&status); err != nil {
		t.Fatalf("read pos transaction: %v", err)
	}
	if status != string(pos.TransactionStatusOpen) {
		t.Errorf("status after rollback = %s, want OPEN (the completion rolled back)", status)
	}
	var tenderRows, auditRows int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM pos_tenders WHERE transaction_id = $1`, tx.ID).Scan(&tenderRows); err != nil {
		t.Fatalf("count tenders: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'pos_transaction' AND entity_id = $1 AND action = 'pos.transaction.completed'`,
		tx.ID).Scan(&auditRows); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if tenderRows != 0 || auditRows != 0 {
		t.Fatalf("after rollback: %d tender row(s), %d pos.transaction.completed audit row(s); both must be 0",
			tenderRows, auditRows)
	}
}
