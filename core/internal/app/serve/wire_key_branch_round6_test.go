// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Fix round 6 (r13 P2-1): a register whose location_id names no branched
// location belongs to the branch its own pos_registers.branch_id names (the
// seeded REG-01 is one). A bound key's pos sync on it must be held to the pin,
// not written at the default branch.
func TestKeyBranchPin_PosSyncRegisterWithoutBranchedLocation(t *testing.T) {
	f, _, first := round5Fixture(t)
	ctx := context.Background()
	client := uuid.New()
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_line_items WHERE transaction_id = $1`, client)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id = $1`, client)
	})
	body := fmt.Sprintf(`{"batch_id":"pin-reg01","register_id":"REG-01","items":[{"client_id":%q,"register_id":"REG-01",`+
		`"cashier_id":%q,"customer_id":%q,"items":[],"tenders":[],"client_created_at":%q}]}`,
		client, uuid.NewString(), f.custID, time.Now().UTC().Format(time.RFC3339))
	status, buf := f.key(t, first.raw, "POST", "/api/v1/pos/sync", body, nil)
	var branch *uuid.UUID
	err := f.db.Pool.QueryRow(ctx, `SELECT branch_id FROM pos_transactions WHERE id = $1`, client).Scan(&branch)
	if err == nil && branch != nil && *branch != f.branchA {
		t.Errorf("bound key sync on REG-01: %d %s; the replayed transaction landed at branch %s, not the pin %s",
			status, buf, branch, f.branchA)
	}
}
