// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	glint "github.com/gablelbm/gable/internal/integrations/gl"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// TestCreateInvoiceAuditRollsBackWithFailedMutation runs the real
// CreateInvoice inside a caller's transaction that then fails (the shape
// order fulfilment and POS account charges produce when their outer
// transaction fails): the invoice and its audit row ride that transaction,
// so a failed mutation leaves no audit row claiming an invoice that never
// happened.
func TestCreateInvoiceAuditRollsBackWithFailedMutation(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	logger := slog.Default()

	glSvc := gl.NewService(gl.NewRepository(db), glint.NewMockGLAdapter(), logger)
	accountSvc := account.NewService(db, glSvc, logger)
	svc := invoice.NewService(invoice.NewRepository(db), glSvc, accountSvc, db).
		WithAuditLog(audit.NewLogger(db))

	custID := uuid.New()
	productID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ($1, 'Invoice Rollback Customer', $2,
		         (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		custID, "IR-"+custID.String()[:8]); err != nil {
		t.Fatalf("insert customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		"INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'Audit Item', 'EA', 50)",
		productID, "IR-"+productID.String()[:8]); err != nil {
		t.Fatalf("insert product: %v", err)
	}

	inv := &invoice.LegacyInvoice{
		ID:         uuid.New(),
		CustomerID: custID,
		Lines:      []invoice.LegacyLine{{ProductID: productID, Quantity: 1, PriceEach: 7500}},
	}

	err := db.RunInTx(ctx, func(txCtx context.Context) error {
		if err := svc.CreateInvoice(txCtx, inv); err != nil {
			return err
		}
		return errors.New("deliberate rollback: the invoice and its audit row must both disappear")
	})
	if err == nil {
		t.Fatal("expected the deliberate error from the outer transaction")
	}

	var invoiceRows, lineRows, auditRows int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM invoices WHERE id = $1`, inv.ID).Scan(&invoiceRows); err != nil {
		t.Fatalf("count invoices: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM invoice_lines WHERE invoice_id = $1`, inv.ID).Scan(&lineRows); err != nil {
		t.Fatalf("count invoice lines: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'invoice' AND entity_id = $1 AND action = 'invoice.created'`,
		inv.ID).Scan(&auditRows); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if invoiceRows != 0 || lineRows != 0 || auditRows != 0 {
		t.Fatalf("after rollback: %d invoice row(s), %d line row(s), %d invoice.created audit row(s); all must be 0",
			invoiceRows, lineRows, auditRows)
	}
}
