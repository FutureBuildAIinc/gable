// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// Fix round 8 of the branch bound key pin: the r17 P3s, each through the real
// serve chain.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Fix round 8 (r17 P3-1): the till report read mapped every service error to
// 500, so a session the caller's branch context hides answered 500 where the
// other by id reads (the Z report, the return) answer 404. The hidden session
// now reads as missing, the caller's own still reads.
func TestKeyBranchPin_HiddenTillReportAnswers404(t *testing.T) {
	f, _, _ := round5Fixture(t)
	hdr := map[string]string{"X-Branch-Id": f.branchA.String()}
	status, buf := f.userAs(t, "admin", "GET", "/api/v1/pos/till/"+f.tillB.String()+"/report", "", hdr)
	if status != http.StatusNotFound {
		t.Errorf("another branch's till report: %d %s, want the 404 the other by id reads answer", status, buf)
	}
	if strings.Contains(string(buf), f.tillB.String()) {
		t.Errorf("the hidden till report answer carries the session's id: %s", buf)
	}
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/pos/till/"+f.tillA.String()+"/report", "", hdr)
	if status != http.StatusOK {
		t.Errorf("the caller's own till report: %d %s, want 200", status, buf)
	}
}

// Fix round 8 (r17 P3-3), under the money never queues rule: a sync batch
// naming a register that does not exist once ran its items and was logged
// under that register; rounds 8 and 9 taught the module to refuse it before
// anything was written. The rule retires the replay outright: every batch
// that parses is refused 409 money_never_queues before any item runs and
// before a log row is written, so the log never names a register nobody made.
func TestKeyBranchPin_SyncRefusesAnUnknownBatchRegister(t *testing.T) {
	f, _, first := round5Fixture(t)
	ctx := context.Background()
	client := uuid.New()
	batch := "pin-r8-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_line_items WHERE transaction_id = $1`, client)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id = $1`, client)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_sync_log WHERE batch_id = $1`, batch)
	})
	syncBody := func(batchRegister string) string {
		return `{"batch_id":"` + batch + `","register_id":"` + batchRegister +
			`","items":[{"client_id":"` + client.String() +
			`","cashier_id":"` + uuid.NewString() + `","customer_id":"` + f.custID.String() +
			`","items":[{"product_id":"` + f.posProd.String() + `","quantity":"1"}],` +
			`"tenders":[{"method":"cash","amount_cents":105}],"client_created_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`
	}
	logRows := func() (n int) {
		if err := f.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM pos_sync_log WHERE batch_id = $1`, batch).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The batch register that does not exist: the batch is refused by rule
	// before any item runs and before the log row is written.
	status, buf := f.key(t, first.raw, "POST", "/api/v1/pos/sync", syncBody("NOPE-PIN-R8"), nil)
	if status != http.StatusConflict || !strings.Contains(string(buf), "money_never_queues") {
		t.Errorf("bound key sync with a batch register that does not exist: %d %s, want the rule's 409", status, buf)
	}
	if n := logRows(); n != 0 {
		t.Errorf("the refused sync wrote %d pos_sync_log rows for the batch, want none", n)
	}
	if n := f.transactionsWithID(t, client); n != 0 {
		t.Errorf("the refused sync wrote %d transactions, want none", n)
	}

	// The empty batch register names no register either (r19 P3-1): the same
	// refusal, before any item runs and before a log row is written under a
	// register nobody made.
	status, buf = f.key(t, first.raw, "POST", "/api/v1/pos/sync", syncBody(""), nil)
	if status != http.StatusConflict || !strings.Contains(string(buf), "money_never_queues") {
		t.Errorf("bound key sync with an empty batch register: %d %s, want the rule's 409", status, buf)
	}
	if n := logRows(); n != 0 {
		t.Errorf("the empty register sync wrote %d pos_sync_log rows, want none", n)
	}
	if n := f.transactionsWithID(t, client); n != 0 {
		t.Errorf("the empty register sync wrote %d transactions, want none", n)
	}

	// The control: the pin's own register as the batch register is refused by
	// the same rule, nothing written.
	status, buf = f.key(t, first.raw, "POST", "/api/v1/pos/sync", syncBody(f.regA), nil)
	if status != http.StatusConflict || !strings.Contains(string(buf), "money_never_queues") {
		t.Errorf("bound key sync with the pin's own batch register: %d %s, want the rule's 409", status, buf)
	}
	if n := logRows(); n != 0 {
		t.Errorf("the refused control sync wrote %d pos_sync_log rows, want none", n)
	}
}
