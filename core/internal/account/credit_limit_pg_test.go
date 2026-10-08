// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// RULE (C2-1, ADR 0005 section 7.4): customers.credit_limit is NULL for "no
// limit". The AR summary reads it as 0 (no credit line to draw on), as it did
// when "no limit" was stored as 0, and a NULL must not fail the read.
func TestGetCreditLimit_NullLimitReadsAsZero(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, credit_limit, primary_branch_id)
		VALUES ($1, 'Null Limit Co', $2, NULL, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		id, "ACC-"+uuid.NewString()[:8]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, id) })

	repo := NewRepository(db)
	got, err := repo.GetCreditLimit(ctx, id)
	if err != nil {
		t.Fatalf("GetCreditLimit on a NULL limit: %v", err)
	}
	if got != 0 {
		t.Errorf("credit limit = %d, want 0", got)
	}

	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET credit_limit = 1234.56 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if got, err = repo.GetCreditLimit(ctx, id); err != nil || got != 123456 {
		t.Errorf("credit limit = %d (%v), want 123456", got, err)
	}
}
