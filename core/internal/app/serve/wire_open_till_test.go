// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// openTillOn seeds an open drawer on a register of the branch, straight in
// the table: the counter's API refuses a sale at a register with no open till
// (CP-08), and the walls these tests prove are not about opening one.
func openTillOn(t *testing.T, f *keyPinFixture, register string, branch uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO till_sessions (id, register_id, branch_id, cashier_id, status, opening_float)
		VALUES ($1, $2, $3, $4, 'OPEN', 0)`, id, register, branch, uuid.New()); err != nil {
		t.Fatalf("seed the open drawer: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM till_z_reports WHERE till_session_id = $1`, id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE till_session_id = $1`, id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM till_sessions WHERE id = $1`, id)
	})
}
