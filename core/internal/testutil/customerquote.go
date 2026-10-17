// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package testutil

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// DeleteCustomerQuotes removes every customer quote of the given contractor
// accounts, the approval log included. The log refuses UPDATE, DELETE and
// TRUNCATE for everybody, so this switches its triggers off and on again
// inside one transaction: nothing outside the transaction ever sees them off.
func DeleteCustomerQuotes(t *testing.T, db *database.DB, customerIDs ...uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Logf("customer quote cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	stmts := []string{
		`ALTER TABLE pro_customer_quote_approval_log DISABLE TRIGGER USER`,
		`DELETE FROM pro_customer_quote_approval_log WHERE quote_id IN (SELECT id FROM pro_customer_quotes WHERE customer_id = ANY($1))`,
		`ALTER TABLE pro_customer_quote_approval_log ENABLE TRIGGER USER`,
		`DELETE FROM pro_portal_verification_codes WHERE quote_id IN (SELECT id FROM pro_customer_quotes WHERE customer_id = ANY($1))`,
		`DELETE FROM pro_portal_share_tokens WHERE quote_id IN (SELECT id FROM pro_customer_quotes WHERE customer_id = ANY($1))`,
		`DELETE FROM pro_customer_quotes WHERE customer_id = ANY($1)`,
		`DELETE FROM pro_contractor_settings WHERE customer_id = ANY($1)`,
	}
	for _, s := range stmts {
		var args []any
		if len(s) > 0 && containsParam(s) {
			args = []any{customerIDs}
		}
		if _, err := tx.Exec(ctx, s, args...); err != nil {
			t.Logf("customer quote cleanup %q: %v", s, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Logf("customer quote cleanup commit: %v", err)
	}
}

func containsParam(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '$' && s[i+1] == '1' {
			return true
		}
	}
	return false
}
