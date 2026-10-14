// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The claim races the reviews proved by probe (seventh review P1-1, eighth
// review P2-1 and P2-2): a claim whose pre-read passed can reach the claim's
// lock after the sale moved under it. A test transaction holds the sale row
// FOR UPDATE, the act blocks inside its claim, and the holder commits the
// state a finished opposite act leaves. The claim must refuse under its lock
// before the terminal is reached: the stub gateway's refund count stays at
// zero, because the tender was already refunded (a committed void) or the
// sale was already returned against (a committed card return).

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// heldSale holds the sale row FOR UPDATE in one open transaction, answering a
// commit function that writes what the racing act leaves and releases the row.
func heldSale(t *testing.T, f *fixture, saleID string) func(func(tx pgx.Tx) error) {
	t.Helper()
	ctx := context.Background()
	conn, err := f.db.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("holder connection: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("holder transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM pos_transactions WHERE id = $1 FOR UPDATE`, saleID); err != nil {
		t.Fatalf("hold the sale row: %v", err)
	}
	return func(write func(tx pgx.Tx) error) {
		t.Helper()
		if err := write(tx); err != nil {
			t.Fatalf("holder write: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("holder commit: %v", err)
		}
		conn.Release()
	}
}

// waitForBlockedClaim polls until a transaction is waiting on another's row
// lock, so the test never races the contender's arrival at the lock.
func waitForBlockedClaim(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		var waiting int
		if err := f.db.Pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'transactionid' AND NOT granted`).Scan(&waiting); err != nil {
			t.Fatalf("poll locks: %v", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no claim ever blocked on the held sale row")
}

// RULE (seventh review P1-1, the probe as a test): a card return whose claim
// comes after a committed void must never reach the gateway: the void already
// refunded the tender, and a second terminal refund pays the sale twice. The
// claim rechecks the sale's status under the sale row lock and refuses the
// tender that a DONE void intent already refunded.
func TestACardReturnAfterACommittedVoidNeverReachesTheTerminal(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	commit := heldSale(t, f, saleID)
	done := make(chan resp, 1)
	go func() { done <- f.cardReturnOn(t, saleID, lineID, "1") }()
	waitForBlockedClaim(t, f)
	// what a committed void leaves: the sale VOIDED and the tender's intent
	// DONE carrying the gateway's refund id
	commit(func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(),
			`UPDATE pos_transactions SET status = 'VOIDED', updated_at = NOW() WHERE id = $1`, saleID); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO pos_refund_intents (id, sale_id, tender_id, gateway_tx_id, amount, act, state, refund_tx_id)
			SELECT $1, t.id, d.id, d.gateway_tx_id, d.amount, 'void', 'DONE', 'ref-void-1'
			FROM pos_transactions t JOIN pos_tenders d ON d.transaction_id = t.id
			WHERE t.id = $2 AND d.method = 'CARD'`, uuid.New(), saleID)
		return err
	})
	r := awaitResp(t, done)
	if r.status != http.StatusConflict {
		t.Fatalf("the card return after a committed void = %d, want 409: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "invalid_state_transition" {
		t.Errorf("code = %q, want invalid_state_transition (the claim rechecks the status under the lock)", code)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 0 {
		t.Fatalf("%d gateway refunds, want 0 (the void already refunded the tender: a second call pays the sale twice)", refunds)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state = 'PENDING'`, saleID); got != 0 {
		t.Errorf("%d PENDING refund intents, want 0 (the refused claim left nothing standing)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 0 {
		t.Errorf("%d orphaned refund rows, want 0 (no refund was made)", got)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (the charge stands, nothing refunded by the return)", got)
	}
	f.assertARInvariants(t)
}

// RULE (seventh review P1-1, the mirror): a void whose claim comes after a
// committed card return must never reach the gateway: the return already
// refunded the tender and took the goods back, and the way back is another
// return. The claim rechecks the sale's returns under the sale row lock.
func TestAVoidAfterACommittedCardReturnNeverReachesTheTerminal(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	commit := heldSale(t, f, saleID)
	done := make(chan resp, 1)
	go func() {
		done <- f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
			map[string]any{"reason": "racing a committed return", "revision": rev(t, body)},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}()
	waitForBlockedClaim(t, f)
	// what a committed card return leaves: the return row and the tender's
	// DONE intent carrying the gateway's refund id
	commit(func(tx pgx.Tx) error {
		ctx := context.Background()
		ret := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO pos_returns (id, register_id, cashier_id, original_transaction_id, subtotal, tax_amount, total,
				refund_method, reason, currency) VALUES ($1, $2, $3, $4, -5.50, -0.49, -5.99, 'CARD', 'raced', 'USD')`,
			ret, f.register, mustUUID(t), saleID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO pos_refund_intents (id, sale_id, tender_id, gateway_tx_id, amount, act, state, refund_tx_id)
			SELECT $1, t.id, d.id, d.gateway_tx_id, d.amount, 'return', 'DONE', 'ref-ret-1'
			FROM pos_transactions t JOIN pos_tenders d ON d.transaction_id = t.id
			WHERE t.id = $2 AND d.method = 'CARD'`, uuid.New(), saleID)
		return err
	})
	r := awaitResp(t, done)
	if r.status != http.StatusConflict {
		t.Fatalf("the void after a committed card return = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "has_returns" {
		t.Errorf("blockers = %v, want has_returns (the claim rechecks the returns under the lock)", blockers)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 0 {
		t.Fatalf("%d gateway refunds, want 0 (the return already refunded the tender)", refunds)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Errorf("sale status = %q, want completed (the void was refused)", got)
	}
	f.assertARInvariants(t)
}
