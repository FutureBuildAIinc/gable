// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The round 4 rechecks the eleventh review could not kill (r11 P2-1): every
// figure the money transaction restates under the sale row lock, and every
// fact of the intent table it rereads there, refuses a booking whose money
// moved on figures that no longer hold. The routes the counter offers refuse
// the writes that would reach them (the intent table's unique claim, the
// status wall), so each test stands the concurrent act in through the real
// tables the recheck reads, in the window between an unlocked read and the
// lock the act takes: the pricing against the claim, the claim against the
// money transaction, the replay's read against its own transaction, and the
// money transaction's two reads of the intent facts against each other.

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// holdRowLock takes one row FOR NO KEY UPDATE on its own held connection and
// keeps it: the mode the acts themselves lock in, strong enough to park the
// sale row lock or the session share lock, soft enough that a stand-in's
// foreign keys still take their KEY SHARE on the held row. blocked answers
// once another transaction is waiting on that row lock (the stand-in window
// between an unlocked read and the lock the act takes), and release commits
// and gives the connection back. The wait is probed on the holding
// transaction's own id, so a package running beside this one against the
// same database never answers it.
func holdRowLock(t *testing.T, f *fixture, table, id string) (blocked func(), release func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := f.db.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire the holding connection: %v", err)
	}
	if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin the hold: %v", err)
	}
	if _, err := conn.Exec(ctx, "SELECT 1 FROM "+table+" WHERE id = $1 FOR NO KEY UPDATE", id); err != nil {
		t.Fatalf("lock the row: %v", err)
	}
	var xid string
	if err := conn.QueryRow(ctx, "SELECT pg_current_xact_id()::text").Scan(&xid); err != nil {
		t.Fatalf("read the hold's transaction id: %v", err)
	}
	blocked = func() {
		t.Helper()
		deadline := time.Now().Add(requestDeadline)
		for {
			var waiting int64
			if err := f.db.Pool.QueryRow(ctx,
				`SELECT count(*) FROM pg_locks WHERE locktype = 'transactionid' AND NOT granted AND transactionid = $1::xid`, xid).
				Scan(&waiting); err != nil {
				t.Fatalf("read the lock waits: %v", err)
			}
			if waiting > 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("no transaction reached the %s row lock within %s", table, requestDeadline)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	var once sync.Once
	release = func() {
		t.Helper()
		once.Do(func() {
			if _, err := conn.Exec(ctx, "COMMIT"); err != nil {
				t.Fatalf("release the %s row lock: %v", table, err)
			}
			conn.Release()
		})
	}
	t.Cleanup(release)
	return blocked, release
}

// cardTenderID answers the sale's card tender id.
func cardTenderID(t *testing.T, f *fixture, saleID string) string {
	t.Helper()
	var id string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id::text FROM pos_tenders WHERE transaction_id = $1 AND method = 'CARD'`, saleID).Scan(&id); err != nil {
		t.Fatalf("read the card tender: %v", err)
	}
	return id
}

// insertVoidIntentDone stands a void claim of the tender in as DONE: the row
// a resolution of the sale's void leaves, standing for the void whose tender
// the terminal already refunded whole.
func insertVoidIntentDone(t *testing.T, f *fixture, saleID, tenderID string) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(), `
		INSERT INTO pos_refund_intents (id, sale_id, tender_id, gateway_tx_id, amount, act, state, created_at, updated_at)
		VALUES ($1, $2, $3, 'gw-1', 0, 'void', 'DONE', NOW(), NOW())`,
		uuid.New(), saleID, tenderID); err != nil {
		t.Fatalf("insert the stand-in void intent: %v", err)
	}
}

// settleIntentAsWinner stands a winning refund resolution's footprint in: the
// claim DONE under the recorded refund id, and the return rows its booking
// wrote (the memo side is not read by any guard here). This is the state a
// contender that won the claim while this act priced leaves behind.
func settleIntentAsWinner(t *testing.T, f *fixture, intentID, saleID, lineID string, cents int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx,
		`UPDATE pos_refund_intents SET state = 'DONE', refund_tx_id = 'ref-winner', updated_at = NOW() WHERE id = $1`, intentID); err != nil {
		t.Fatalf("settle the claim: %v", err)
	}
	returnID := uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO pos_returns (id, number, register_id, original_transaction_id, customer_id, branch_id,
		cashier_id, currency, subtotal, tax_amount, total, refund_method, reason, status, created_at, updated_at, revision)
		VALUES ($1, 'RTN-WINNER', $2, $3, $4, NULL, $5, 'USD', 0, 0, $6::bigint::numeric / 100, 'CARD', 'stand-in winner', 'COMPLETED', NOW(), NOW(), 1)`,
		returnID, f.register, saleID, f.customerID, mustUUID(t), -cents); err != nil {
		t.Fatalf("insert the winner's return: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO pos_return_lines (return_id, position, line_type, product_id, description, quantity, uom,
		unit_price, line_total, restock, sale_line_id, created_at)
		VALUES ($1, 0, 'PRODUCT', $2, 'stand-in winner', -1, 'EA', 0, $3::bigint::numeric / 100, FALSE, $4, NOW())`,
		returnID, f.productID, -cents, lineID); err != nil {
		t.Fatalf("insert the winner's return line: %v", err)
	}
}

// insertBookedCardReturn stands a booked card refund of the sale in: a CARD
// return row with no lines, so the caps and the remainder rule read nothing
// of it and only the card cap's own sum counts it.
func insertBookedCardReturn(t *testing.T, f *fixture, saleID string, cents int64) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO pos_returns (id, number, register_id, original_transaction_id, customer_id, branch_id,
		cashier_id, currency, subtotal, tax_amount, total, refund_method, reason, status, created_at, updated_at, revision)
		VALUES ($1, 'RTN-CARDED', $2, $3, $4, NULL, $5, 'USD', 0, 0, $6::bigint::numeric / 100, 'CARD', 'stand-in booked card refund', 'COMPLETED', NOW(), NOW(), 1)`,
		uuid.New(), f.register, saleID, f.customerID, mustUUID(t), -cents); err != nil {
		t.Fatalf("insert the stand-in card return: %v", err)
	}
}

// RULE (r11 P2-1, the claim's restate): a remainder that moves between the
// pricing and the claim is restated under the sale row lock before the
// terminal is asked, so the figure the terminal refunds is the figure the
// books can take. Without the restate the terminal is asked the stale naive
// share, the money transaction then restates under it and refuses, and the
// money that moved is orphaned beside a claim that cannot book it.
func TestTheClaimRestatesItsRemainderUnderTheLockBeforeTheTerminalIsAsked(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "4",
		"discount_cents": 2, "discount_reason": "two off"}); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, withToken(tender("card", 2393))); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)

	blocked, release := holdRowLock(t, f, "pos_transactions", saleID)
	returnDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() { defer close(fin); returnDone <- f.cardReturnOn(t, saleID, lineID, "3") }()
	t.Cleanup(func() { release(); <-fin })
	// the return priced on the pre-transaction figures and its claim is now
	// parked behind the sale row lock: move the remainder inside the window
	blocked()
	insertUnlinkedReturnLine(t, f, saleID, 599)
	release()
	rr := awaitResp(t, returnDone)
	if rr.status != http.StatusCreated {
		t.Fatalf("the card return under the moved remainder = %d, want 201 (the claim restated before the terminal): %s", rr.status, rr.raw)
	}
	gw.mu.Lock()
	asked := gw.refundCents[0]
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if asked != 1794 {
		t.Errorf("the terminal was asked for %d cents, want 1794 (the restated remainder under the lock, never the naive 1795)", asked)
	}
	if calls != 1 {
		t.Errorf("%d gateway refund calls, want 1", calls)
	}
	if got := num(t, rr.body, "total_cents"); got != -1794 {
		t.Errorf("the booked return total = %d, want -1794 (the books take the figure the terminal was asked for)", got)
	}
	if got := f.scalar(`SELECT ROUND(amount * 100)::bigint FROM pos_refund_intents WHERE sale_id = $1 AND act = 'return'`, saleID).(int64); got != 1794 {
		t.Errorf("the claim holds %d cents, want 1794", got)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (2393 charged, 1794 refunded once)", got)
	}
	f.assertARInvariants(t)
}

// RULE (r11 P2-1, the card cap under the money transaction's lock): a booked
// card refund that lands between the claim and the money transaction is
// counted before the booking is allowed, so the tender is never refunded
// past what it kept less every refund already booked.
func TestACardReturnPastTheBookedRefundsRefusesUnderTheLock(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)

	parked, release := parkRefund(t, gw)
	returnDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() { defer close(fin); returnDone <- f.cardReturnOn(t, saleID, lineID, "1") }()
	t.Cleanup(func() { release(); <-fin })
	awaitParked(t, parked)
	// a card refund of the sale booked while this return sat at the terminal:
	// the claim passed the cap on the figures it saw, the booking must not
	insertBookedCardReturn(t, f, saleID, 500)
	release()
	rr := awaitResp(t, returnDone)
	if rr.status != http.StatusConflict {
		t.Fatalf("the card return past the booked refunds = %d, want 409: %s", rr.status, rr.raw)
	}
	_, blockers, _ := errorOf(t, rr)
	if len(blockers) == 0 || blockers[0] != "exceeds_card_tender" {
		t.Fatalf("blockers = %v, want exceeds_card_tender (599 asked against the tender's 599 less the 500 booked)", blockers)
	}
	_, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID == "" {
		t.Fatalf("the claim = (%s, %q), want PENDING carrying the refund id", state, refundTxID)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d orphaned refund rows, want 1 recording the money that moved", got)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (the charge stands: nothing booked past the cap)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`); got != 0 {
		t.Errorf("%d refund rows, want 0 (nothing booked)", got)
	}
	if got := f.counterReturns(t); got != 1 {
		t.Errorf("%d returns of the sale, want 1 (the stand-in booked refund alone)", got)
	}
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 1 {
		t.Errorf("%d gateway refund calls, want 1 (the terminal was reached once)", calls)
	}
	f.assertARInvariants(t)
}

// RULE (r11 P2-1, the held figure): the money transaction rereads the
// tender's standing claims and refuses when the table holds a different
// figure than the claim this return books under: the booking follows the
// intent table's own record, never a figure the table no longer holds.
func TestAClaimWhoseTableAmountMovedRefusesTheBooking(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)

	parked, release := parkRefund(t, gw)
	returnDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() { defer close(fin); returnDone <- f.cardReturnOn(t, saleID, lineID, "1") }()
	t.Cleanup(func() { release(); <-fin })
	awaitParked(t, parked)
	// the claim's own row now holds a different figure than the booking's
	// in-memory claim: the stand-in for a second standing claim the unique
	// index refuses, whose sum the transaction must not book beside
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE pos_refund_intents SET amount = 5.00, updated_at = NOW() WHERE sale_id = $1 AND state = 'PENDING'`, saleID); err != nil {
		t.Fatalf("move the claim's table figure: %v", err)
	}
	release()
	rr := awaitResp(t, returnDone)
	if rr.status != http.StatusConflict {
		t.Fatalf("the card return under a moved table figure = %d, want 409: %s", rr.status, rr.raw)
	}
	_, blockers, _ := errorOf(t, rr)
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Fatalf("blockers = %v, want refund_in_flight (the table holds 500 against the booking's 599)", blockers)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state = 'PENDING'
		AND ROUND(amount * 100)::bigint = 500`, saleID); got != 1 {
		t.Errorf("the claim's table figure was not kept at 500: %d rows", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d orphaned refund rows, want 1 recording the money that moved", got)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (nothing booked beside a moved figure)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`); got != 0 {
		t.Errorf("%d refund rows, want 0 (nothing booked)", got)
	}
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 1 {
		t.Errorf("%d gateway refund calls, want 1", calls)
	}
	f.assertARInvariants(t)
}

// RULE (r11 P2-1, the money transaction's tender fact): a void claim of the
// tender that reaches DONE between the transaction's two reads of the intent
// facts refuses the booking: the tender was refunded whole by the sale's
// void, and no second refund of it can be booked, whatever the claim this
// return holds. The window is the transaction's own till session lock, taken
// between the first read (before the caps) and the second (the booking's
// rules).
func TestATenderTheVoidRefundedBetweenTheFactsReadsRefusesTheBooking(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	sessionID := f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)

	blocked, release := holdRowLock(t, f, "till_sessions", sessionID)
	returnDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() { defer close(fin); returnDone <- f.cardReturnOn(t, saleID, lineID, "1") }()
	t.Cleanup(func() { release(); <-fin })
	// the money transaction passed its first read of the claim facts and now
	// parks on the session row: the tender's void resolution lands inside
	blocked()
	insertVoidIntentDone(t, f, saleID, cardTenderID(t, f, saleID))
	release()
	rr := awaitResp(t, returnDone)
	if rr.status != http.StatusConflict {
		t.Fatalf("the card return under a void refunded tender = %d, want 409: %s", rr.status, rr.raw)
	}
	_, blockers, _ := errorOf(t, rr)
	if len(blockers) == 0 || blockers[0] != "tender_refunded" {
		t.Fatalf("blockers = %v, want tender_refunded (the void's resolution refunded the tender whole between the reads)", blockers)
	}
	_, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID == "" {
		t.Fatalf("the claim = (%s, %q), want PENDING carrying the refund id", state, refundTxID)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d orphaned refund rows, want 1 recording the money that moved", got)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (the tender was refunded by the void: this booking never ran)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`); got != 0 {
		t.Errorf("%d refund rows, want 0", got)
	}
	if got := f.counterReturns(t); got != 0 {
		t.Errorf("%d returns booked, want 0", got)
	}
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 1 {
		t.Errorf("%d gateway refund calls, want 1", calls)
	}
	f.assertARInvariants(t)
}

// RULE (r11 P2-1, the money transaction's pending fact): the claim settled
// by another resolution between the transaction's two reads of the intent
// facts refuses the booking with the race named (intent_not_pending), never
// the misleading in-flight figure the later checks would answer with.
func TestAClaimSettledBetweenTheFactsReadsRefusesWithTheRaceNamed(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	sessionID := f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)

	blocked, release := holdRowLock(t, f, "till_sessions", sessionID)
	returnDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() { defer close(fin); returnDone <- f.cardReturnOn(t, saleID, lineID, "1") }()
	t.Cleanup(func() { release(); <-fin })
	blocked()
	// the claim was settled by a concurrent resolution inside the window
	intentID, _, _, _ := pendingIntent(t, f, saleID)
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE pos_refund_intents SET state = 'DONE', refund_tx_id = 'ref-won', updated_at = NOW() WHERE id = $1`, intentID); err != nil {
		t.Fatalf("settle the claim: %v", err)
	}
	release()
	rr := awaitResp(t, returnDone)
	if rr.status != http.StatusConflict {
		t.Fatalf("the card return under its settled claim = %d, want 409: %s", rr.status, rr.raw)
	}
	_, blockers, _ := errorOf(t, rr)
	if len(blockers) == 0 || blockers[0] != "intent_not_pending" {
		t.Fatalf("blockers = %v, want intent_not_pending (the claim was settled by another resolution first)", blockers)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'DONE'`, intentID); got != 1 {
		t.Errorf("the settled claim was not kept settled: %d rows", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d orphaned refund rows, want 1 recording the money that moved", got)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (the winner's booking is the only one)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`); got != 0 {
		t.Errorf("%d refund rows, want 0 (the stand-in settled no money)", got)
	}
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 1 {
		t.Errorf("%d gateway refund calls, want 1", calls)
	}
	f.assertARInvariants(t)
}

// RULE (r11 P2-1, the replay's pre-cap pending fact): a replay whose claim a
// concurrent resolution won while it priced answers the race under the sale
// row lock and before the caps (intent_not_pending), never the winner's own
// booking tripping the caps of the act that lost (exceeds_sold). The
// stand-in is the winner's whole footprint: the settled claim and the return
// rows its booking wrote.
func TestAReplayWhoseClaimWasWonUnderItAnswersTheRaceNeverTheCaps(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()},
		refundErrs: []error{context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out card return = %d, want 500: %s", r.status, r.raw)
	}
	intentID, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID != "" {
		t.Fatalf("the stranded claim = (%s, %q)", state, refundTxID)
	}

	blocked, release := holdRowLock(t, f, "pos_transactions", saleID)
	resolveDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		resolveDone <- f.resolveOn(t, intentID, map[string]any{
			"outcome": "refund", "refund_tx_id": "ref-winner", "reason": "the terminal refunded",
		}, "finance")
	}()
	t.Cleanup(func() { release(); <-fin })
	// the replay read its claim PENDING and priced, and its transaction now
	// parks on the sale row: the concurrent resolution wins inside
	blocked()
	settleIntentAsWinner(t, f, intentID, saleID, lineID, 599)
	release()
	rr := awaitResp(t, resolveDone)
	if rr.status != http.StatusConflict {
		t.Fatalf("the replay under a won claim = %d, want 409: %s", rr.status, rr.raw)
	}
	_, blockers, _ := errorOf(t, rr)
	if len(blockers) == 0 || blockers[0] != "intent_not_pending" {
		t.Fatalf("blockers = %v, want intent_not_pending (the race, never the caps the winner's booking trips)", blockers)
	}
	// the replay writes no orphan row of its own and moves nothing: the
	// stranded act's timeout left its own unknown outcome row, and the claim
	// carrying no refund id means no money to orphan
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 0 {
		t.Errorf("%d orphaned refund rows, want 0 (the replay refused before any money of its own)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_outcome_unknown' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d unknown outcome rows, want 1 (the stranded act's own record alone)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`); got != 0 {
		t.Errorf("%d refund rows, want 0 (the stand-in winner booked none)", got)
	}
	if got := f.counterReturns(t); got != 1 {
		t.Errorf("%d returns booked, want 1 (the stand-in winner's alone)", got)
	}
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 1 {
		t.Errorf("%d gateway refund calls, want 1 (the stranded act's own call)", calls)
	}
	f.assertARInvariants(t)
}

// RULE (r11 P2-1, the replay's pre-cap tender fact): a replay that reaches
// its transaction under a tender the sale's void already refunded answers
// the tender (tender_refunded) before the caps, whatever else the window
// wrote: the intent table's authority outranks the figure guards, and the
// answer names the money, not the goods.
func TestAReplayUnderAVoidRefundedTenderAnswersTheTenderNeverTheCaps(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()},
		refundErrs: []error{context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out card return = %d, want 500: %s", r.status, r.raw)
	}
	intentID, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID != "" {
		t.Fatalf("the stranded claim = (%s, %q)", state, refundTxID)
	}

	blocked, release := holdRowLock(t, f, "pos_transactions", saleID)
	resolveDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		resolveDone <- f.resolveOn(t, intentID, map[string]any{
			"outcome": "refund", "refund_tx_id": "ref-winner", "reason": "the terminal refunded",
		}, "finance")
	}()
	t.Cleanup(func() { release(); <-fin })
	blocked()
	// the window writes the void's DONE claim beside the winner's footprint:
	// the caps would trip on the winner's rows, the tender fact outranks them
	settleIntentAsWinner(t, f, intentID, saleID, lineID, 599)
	insertVoidIntentDone(t, f, saleID, cardTenderID(t, f, saleID))
	release()
	rr := awaitResp(t, resolveDone)
	if rr.status != http.StatusConflict {
		t.Fatalf("the replay under a void refunded tender = %d, want 409: %s", rr.status, rr.raw)
	}
	_, blockers, _ := errorOf(t, rr)
	if len(blockers) == 0 || blockers[0] != "tender_refunded" {
		t.Fatalf("blockers = %v, want tender_refunded (the intent table's authority, never the caps)", blockers)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 0 {
		t.Errorf("%d orphaned refund rows, want 0 (the replay refused before any money of its own)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_outcome_unknown' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d unknown outcome rows, want 1 (the stranded act's own record alone)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`); got != 0 {
		t.Errorf("%d refund rows, want 0", got)
	}
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 1 {
		t.Errorf("%d gateway refund calls, want 1", calls)
	}
	f.assertARInvariants(t)
}
