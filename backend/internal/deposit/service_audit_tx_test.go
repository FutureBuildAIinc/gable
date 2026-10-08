// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package deposit_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/deposit"
	glint "github.com/gablelbm/gable/internal/integrations/gl"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// TestRecordDepositAuditRollsBackWithFailedMutation runs the real
// RecordDeposit inside a caller's transaction that then fails: the deposit,
// its GL posting and its audit row all ride that transaction, so a failed
// mutation leaves no audit row claiming a deposit that never happened.
func TestRecordDepositAuditRollsBackWithFailedMutation(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	logger := slog.Default()

	glSvc := gl.NewService(gl.NewRepository(db), glint.NewMockGLAdapter(), logger)
	accountSvc := account.NewService(account.NewRepository(db), db, logger)
	svc := deposit.NewService(db, deposit.NewRepository(db), glSvc, accountSvc, logger).
		WithAuditLog(audit.NewLogger(db))

	custID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ($1, 'Deposit Rollback Customer', $2,
		         (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		custID, "DR-"+custID.String()[:8]); err != nil {
		t.Fatalf("insert customer: %v", err)
	}

	var dep *deposit.CustomerDeposit
	err := db.RunInTx(ctx, func(txCtx context.Context) error {
		var err error
		dep, err = svc.RecordDeposit(txCtx, deposit.RecordDepositRequest{
			CustomerID: custID,
			Amount:     5000,
			Method:     "CASH",
			Reference:  "audit-rollback",
		})
		if err != nil {
			return err
		}
		return errors.New("deliberate rollback: the deposit and its audit row must both disappear")
	})
	if err == nil {
		t.Fatal("expected the deliberate error from the outer transaction")
	}
	if dep == nil {
		t.Fatal("RecordDeposit did not report success inside the transaction")
	}

	var depositRows, auditRows int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_deposits WHERE id = $1`, dep.ID).Scan(&depositRows); err != nil {
		t.Fatalf("count deposits: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'customer_deposit' AND entity_id = $1 AND action = 'deposit.recorded'`,
		dep.ID).Scan(&auditRows); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if depositRows != 0 || auditRows != 0 {
		t.Fatalf("after rollback: %d deposit row(s), %d deposit.recorded audit row(s); both must be 0",
			depositRows, auditRows)
	}
}
