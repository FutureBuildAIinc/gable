// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// sharedRefusalRows counts the customer.shared_restricted_refused rows of a
// customer, the audit row of a user's or an unbound key's refusal.
func (f *keyPinFixture) sharedRefusalRows(t *testing.T, customer uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action = 'customer.shared_restricted_refused' AND entity_id = $1`,
		customer).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Fix round 6 (r13 P3-1): the unbound key and the user refused on a shared
// customer's restricted fields get the bound key's words and leave a refusal
// row of their own; the callers the rule admits leave none.
func TestKeyBranchPin_SharedCallerRefusalRow(t *testing.T) {
	f, _, _ := round5Fixture(t)
	s := seedSharedCustomer(t, f, f.branchA, f.branchB)
	sh := "/api/v1/customers/" + s.id.String()
	change := func() string {
		return f.customerReplace(t, s.id, map[string]any{"credit_limit_cents": 555000 + int(time.Now().UnixNano()%1000)})
	}
	lowest := f.branchA
	if f.branchB.String() < f.branchA.String() {
		lowest = f.branchB
	}
	words := "a branch bound key may act only on its own branch: the customer is also held by branch " + lowest.String()

	check := func(who string, status int, buf []byte, rowsBefore int) {
		t.Helper()
		if status != http.StatusForbidden || !strings.Contains(string(buf), words) {
			t.Errorf("%s changing a restricted field: %d %s, want the 403 in the bound key's words naming branch %s", who, status, buf, lowest)
		}
		if got := f.sharedRefusalRows(t, s.id) - rowsBefore; got != 1 {
			t.Errorf("%s refusal left %d refusal rows, want one", who, got)
		}
	}
	before := f.sharedRefusalRows(t, s.id)
	status, buf := f.key(t, f.unbound, "PUT", sh, change(), nil)
	check("unbound key without the scope", status, buf, before)
	for _, role := range []string{"sales", "admin"} {
		before = f.sharedRefusalRows(t, s.id)
		status, buf = f.userAs(t, role, "PUT", sh, change(), nil)
		check(role+" user", status, buf, before)
	}
	// The row names the actor, the fields and the method.
	var kind, raw string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT changes->>'actor_kind', changes::text FROM audit_log
		 WHERE action = 'customer.shared_restricted_refused' AND entity_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
		s.id).Scan(&kind, &raw); err != nil {
		t.Fatal(err)
	}
	if kind != "user" || !strings.Contains(raw, "credit_limit_cents") || !strings.Contains(raw, "PUT") {
		t.Errorf("the refusal row reads %s, want a user actor, the refused field and the method", raw)
	}
	// Admitted callers leave no refusal row.
	before = f.sharedRefusalRows(t, s.id)
	for _, role := range []string{"finance", "owner"} {
		if status, buf := f.userAs(t, role, "PUT", sh, change(), nil); status != http.StatusOK {
			t.Errorf("%s user: %d %s, want 200", role, status, buf)
		}
	}
	if got := f.sharedRefusalRows(t, s.id) - before; got != 0 {
		t.Errorf("admitted callers left %d refusal rows, want none", got)
	}
}
