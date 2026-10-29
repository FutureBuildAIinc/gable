// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

// The no double charge rules of the Run driver (CP-05), proved against the
// scripted fake: the transaction lookup answers three things, found, not
// found and could not ask, and a report that could not answer refuses the
// dispatch and leaves the leg for reconciliation, never charges blind; the
// driver's own guard holds the check and the charge-card POST together, so
// four concurrent dispatches of one leg make one charge; the refund and void
// path takes the same guard, so a retried reversal refunds once; and the
// poller's window fits inside the ledger's lease with room for the longest
// charge-card call, so the sweep never closes a leg the driver is still
// working.
//
// Every read of an intent in this file goes through the suite's own
// waitForIntentState: a row that another suite's terminal table cleanup
// deleted answers false and the scenario runs again, the way the rest of the
// run suite does, instead of failing on a row that is not there.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// brokenReportDriver wires the driver on the fake with a reporting base that
// cannot answer: a 500 on the transaction list, a 401 on the token, or a
// refused connection.
func brokenReportDriver(t *testing.T, fs *runFakeServer, sink payment.AnswerSink,
	reportStatus, tokenStatus int, unreachable bool) *payment.RunTerminal {
	t.Helper()
	base := fs.server.URL
	if unreachable {
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		base = dead.URL
		dead.Close()
	}
	fs.fake.breakReporting(reportStatus, tokenStatus)
	return payment.NewRunTerminal(func() payment.GatewayConfig {
		return payment.GatewayConfig{APIKey: "teststub" + "teststub", MID: "MID-TESTSTUB", BaseURL: fs.server.URL}
	}, sink, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithPollInterval(5*time.Millisecond).WithInteractionWindow(50*time.Millisecond).
		WithReporting(base, "clientid-teststub", "clientsecret-teststub")
}

// ledgerRecordingSink is the real ledger with a record of every answer the
// driver lands: the test sees that the driver landed no answer at all, which
// is what a leg left pending at the device means.
type ledgerRecordingSink struct {
	ledger  *payment.TerminalLedger
	mu      sync.Mutex
	answers []payment.TerminalAnswer
}

// ApplyDeviceAnswer records the answer and lands it on the real ledger.
func (s *ledgerRecordingSink) ApplyDeviceAnswer(ctx context.Context, a payment.TerminalAnswer) (payment.AnswerOutcome, error) {
	s.mu.Lock()
	s.answers = append(s.answers, a)
	s.mu.Unlock()
	return s.ledger.ApplyDeviceAnswer(ctx, a)
}

// waitForAnswer waits for the driver to land an answer and says whether one
// arrived. The driver lands its answers on a twenty millisecond timer, so a
// wait well past that timer sees every answer it would land.
func (s *ledgerRecordingSink) waitForAnswer(t *testing.T, within time.Duration) (payment.TerminalAnswer, bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		s.mu.Lock()
		n := len(s.answers)
		s.mu.Unlock()
		if n > 0 {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.answers[0], true
		}
		if time.Now().After(deadline) {
			return payment.TerminalAnswer{}, false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// newRunDriverOn wires the driver on the fake with any answer sink, so a test
// can watch the answers the driver lands beside the ledger's own.
func newRunDriverOn(t *testing.T, fs *runFakeServer, sink payment.AnswerSink) *payment.RunTerminal {
	t.Helper()
	return payment.NewRunTerminal(func() payment.GatewayConfig {
		return payment.GatewayConfig{APIKey: "teststub" + "teststub", MID: "MID-TESTSTUB", BaseURL: fs.server.URL}
	}, sink, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithPollInterval(5*time.Millisecond).
		WithInteractionWindow(2*time.Second).
		WithReporting(fs.server.URL, "clientid-teststub", "clientsecret-teststub")
}

// waitForLookupCount waits until the fake has served the wanted number of
// reporting lookups. The lookup before the charge-card POST is the first one;
// the poll's own lookup after an uncertain end is the second, and the driver
// has made its decision once that one is served.
func waitForLookupCount(t *testing.T, fs *runFakeServer, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for fs.fake.lookupCount() < want {
		if time.Now().After(deadline) {
			t.Fatalf("the fake served %d reporting lookups, want %d", fs.fake.lookupCount(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRunTerminalRefusesTheDispatchWhenTheTransactionLookupCannotAsk: the
// transaction lookup answers three things, found, not found and could not
// ask. A report that could not answer (a 500, a refused connection, an
// expired token) is not a report that holds no charge: the dispatch is
// refused, no charge-card POST is sent, and the leg stays pending at the
// device for reconciliation.
func TestRunTerminalRefusesTheDispatchWhenTheTransactionLookupCannotAsk(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reportStatus int
		tokenStatus  int
		unreachable  bool
	}{
		{"the report answers 500", 500, 0, false},
		{"the reporting token is refused", 0, 401, false},
		{"the report is unreachable", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger, branch := newRunLedger(t)
			runUntilItSticks(t, func(t *testing.T) bool {
				sale := runSaleRow(t, branch)
				fs := newRunFake(t)
				term := brokenReportDriver(t, fs, ledger, tc.reportStatus, tc.tokenStatus, tc.unreachable)
				in, ok := claimRunLegAt(t, ledger, sale, branch, 2500)
				if !ok {
					// the row was deleted by another suite's terminal table
					// cleanup: the scenario runs again rather than failing on a
					// row that is not there
					return false
				}
				_, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
					IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents, RegisterID: in.RegisterID,
					DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey})
				if err == nil {
					t.Fatal("the dispatch was answered: the driver must never charge blind when the report cannot answer")
				}
				if posts := fs.fake.chargePostCount(); posts != 0 {
					t.Fatalf("the driver posted %d charges with a report that could not answer, want 0", posts)
				}
				got, ok := waitForIntentState(t, ledger, in.ID,
					payment.TerminalPendingAtDevice, payment.TerminalDeclined, payment.TerminalAuthorized)
				if !ok {
					// the row was deleted by another suite's terminal table
					// cleanup: the scenario runs again rather than reading a
					// foreign state
					return false
				}
				if got.State != payment.TerminalPendingAtDevice {
					t.Fatalf("the intent is %s, want it left pending at the device for reconciliation", got.State)
				}
				return true
			})
		})
	}
}

// TestRunTerminalARefusedLookupNeverChargesAgainAfterARestart: the first
// dispatch charges; the report then breaks; a fresh driver (a process
// restart, with a guard of its own still empty) re dispatches the same
// idempotency key. The report could not ask, so the re dispatch is refused
// and the leg is never charged twice.
func TestRunTerminalARefusedLookupNeverChargesAgainAfterARestart(t *testing.T) {
	ledger, branch := newRunLedger(t)
	runUntilItSticks(t, func(t *testing.T) bool {
		sale := runSaleRow(t, branch)
		fs := newRunFake(t)
		fs.fake.pollStep = func(string, int) (int, map[string]any) {
			return 200, map[string]any{"status": "pending", "result": nil, "updated_at": "2026-01-01T00:00:00Z"}
		}
		term := newRunDriver(t, fs, ledger)
		in, ok := claimRunLegAt(t, ledger, sale, branch, 2500)
		if !ok {
			// the row was deleted by another suite's terminal table
			// cleanup: the scenario runs again rather than failing on a
			// row that is not there
			return false
		}
		d := payment.TerminalDispatch{IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents,
			RegisterID: in.RegisterID, DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey}
		if _, err := term.Dispatch(context.Background(), d); err != nil {
			t.Fatalf("first dispatch: %v", err)
		}
		if posts := fs.fake.chargePostCount(); posts != 1 {
			t.Fatalf("the first dispatch posted %d charges, want 1", posts)
		}
		// the report breaks: it can no longer say whether this order id
		// carries a charge
		fs.fake.breakReporting(500, 0)
		// a fresh driver, as after a process restart: its own guard is empty
		restarted := newRunDriver(t, fs, ledger)
		if _, err := restarted.Dispatch(context.Background(), d); err == nil {
			t.Fatal("the re dispatch was answered: the driver must never charge blind when the report cannot answer")
		}
		if posts := fs.fake.chargePostCount(); posts != 1 {
			t.Fatalf("the re dispatch posted a second charge (%d posts): the leg would be charged twice", posts)
		}
		return true
	})
}

// TestRunTerminalTheReportThatCannotAnswerNeverDeclinesTheLeg: an
// interaction that ends with no verdict is uncertain, not a decline. When the
// report cannot answer, the driver leaves the leg pending at the device for
// reconciliation: it never books a decline with the money moved.
func TestRunTerminalTheReportThatCannotAnswerNeverDeclinesTheLeg(t *testing.T) {
	ledger, branch := newRunLedger(t)
	runUntilItSticks(t, func(t *testing.T) bool {
		sale := runSaleRow(t, branch)
		fs := newRunFake(t)
		fs.fake.pollStep = func(_ string, poll int) (int, map[string]any) {
			if poll < 1 {
				return 200, map[string]any{"status": "pending", "result": nil, "updated_at": "2026-01-01T00:00:00Z"}
			}
			// the report breaks: from here the driver cannot ask what
			// happened to this order id
			fs.fake.breakReporting(500, 0)
			return 200, map[string]any{"status": "failed", "result": nil, "updated_at": "2026-01-01T00:00:00Z"}
		}
		sink := &ledgerRecordingSink{ledger: ledger}
		term := newRunDriverOn(t, fs, sink)
		in, ok := claimRunLegAt(t, ledger, sale, branch, 2500)
		if !ok {
			// the row was deleted by another suite's terminal table
			// cleanup: the scenario runs again rather than failing on a
			// row that is not there
			return false
		}
		if _, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
			IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents, RegisterID: in.RegisterID,
			DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey}); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		// the driver has consulted the report after the uncertain end: the
		// lookup before the POST is the first one, the poll's own is the
		// second, and the driver has decided once that one is served
		waitForLookupCount(t, fs, 2)
		got, ok := waitForIntentState(t, ledger, in.ID,
			payment.TerminalPendingAtDevice, payment.TerminalDeclined, payment.TerminalAuthorized)
		if !ok {
			// the row was deleted by another suite's terminal table cleanup:
			// the scenario runs again rather than reading a foreign state
			return false
		}
		if got.State != payment.TerminalPendingAtDevice {
			t.Fatalf("the intent is %s, want it left pending at the device for reconciliation", got.State)
		}
		// the driver lands its answers on a twenty millisecond timer, so a
		// wait well past that timer sees every answer it would land: the
		// driver landed none, and the leg is left for the sweep's manager
		// item
		if answer, landed := sink.waitForAnswer(t, 60*time.Millisecond); landed {
			t.Fatalf("the driver landed a %s answer, want the leg left pending at the device for reconciliation",
				answer.Verdict)
		}
		return true
	})
}

// TestRunTerminalConcurrentDispatchesOfOneLegMakeOneCharge (BRIEF-COMMON:
// transactions, pool size 4 with three contenders): four dispatches of one
// idempotency key at the same time, against a fake that holds the
// charge-card POST open. The driver's own guard holds the check and the POST
// together, so exactly one charge-card POST is sent and the other three
// dispatches are refused.
func TestRunTerminalConcurrentDispatchesOfOneLegMakeOneCharge(t *testing.T) {
	testutil.LockTerminalTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	var branch uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branch); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	for i := 0; i < 5; i++ {
		concurrentDispatchOnce(t, db, ctx, branch, i)
	}
}

// concurrentDispatchOnce runs one iteration of the concurrent dispatch.
func concurrentDispatchOnce(t *testing.T, db *database.DB, ctx context.Context, branch uuid.UUID, i int) {
	t.Helper()
	sale := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO pos_transactions
			(id, register_id, cashier_id, subtotal, tax_amount, total, status, branch_id, change_due, revision, currency, number, created_at, updated_at)
			VALUES ($1, $2, $3, 0, 0, 0, 'OPEN', $4, 0, 1, 'USD', $5, NOW(), NOW())`,
		sale, "REG-M29-"+sale.String()[:8], uuid.New(), branch, "POS-"+sale.String()[:8]); err != nil {
		t.Fatalf("sale row: %v", err)
	}
	defer func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_lost_items WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_intents WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM pos_transactions WHERE id = $1`, sale)
	}()
	ledger := payment.NewTerminalLedger(db)
	fs := newRunFake(t)
	fs.fake.pollStep = func(string, int) (int, map[string]any) {
		return 200, map[string]any{"status": "pending", "result": nil, "updated_at": "2026-01-01T00:00:00Z"}
	}
	release := fs.fake.holdPosts()
	term := newRunDriver(t, fs, ledger)
	in := &payment.TerminalIntent{ID: uuid.New(), TransactionID: sale, LegKey: "card-0",
		RegisterID: "REG-" + sale.String()[:12], BranchID: branch, AmountCents: 2500,
		Kind: payment.TerminalKindSale, IdempotencyKey: "sale-" + sale.String() + "-card-0",
		LeaseUntil: time.Now().Add(time.Minute)}
	if err := ledger.Claim(ctx, in); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if ok, err := ledger.MarkDispatched(ctx, in.ID, "", in.LeaseUntil); err != nil || !ok {
		t.Fatalf("dispatch move: ok=%v err=%v", ok, err)
	}
	d := payment.TerminalDispatch{IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents,
		RegisterID: in.RegisterID, DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey}
	start := make(chan struct{})
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, errs[n] = term.Dispatch(ctx, d)
		}(n)
	}
	close(start)
	// wait until the charge-card POST is in flight and held open: this is
	// the window in which a second dispatch would post a second charge
	deadline := time.Now().Add(5 * time.Second)
	for fs.fake.chargePostCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the charge-card POST never arrived")
		}
		time.Sleep(2 * time.Millisecond)
	}
	release()
	wg.Wait()
	if posts := fs.fake.chargePostCount(); posts != 1 {
		t.Fatalf("iteration %d: four dispatches of one leg posted %d charges, want 1", i, posts)
	}
	answered := 0
	for n, err := range errs {
		if err == nil {
			answered++
			continue
		}
		if !strings.Contains(err.Error(), "not re sent") {
			t.Fatalf("iteration %d: contender %d failed with %v, want the refusal of a re sent dispatch", i, n, err)
		}
	}
	if answered != 1 {
		t.Fatalf("iteration %d: %d dispatches were answered, want 1", i, answered)
	}
}

// TestRunTerminalARetriedReversalRefundsOnce: a retried reversal is the
// same leg. The driver's own guard takes the refund and void path too, so
// the customer is refunded once and the retried reversal is refused.
func TestRunTerminalARetriedReversalRefundsOnce(t *testing.T) {
	ledger, branch := newRunLedger(t)
	runUntilItSticks(t, func(t *testing.T) bool {
		sale := runSaleRow(t, branch)
		fs, term := newRunTerminalFixtureOn(t, ledger)
		in := &payment.TerminalIntent{ID: uuid.New(), TransactionID: sale, LegKey: "refund-1",
			RegisterID: "REG-" + sale.String()[:12], BranchID: branch, AmountCents: 1200,
			Kind: payment.TerminalKindRefund, IdempotencyKey: "refund-" + sale.String(),
			LeaseUntil: time.Now().Add(time.Minute)}
		if err := ledger.Claim(context.Background(), in); err != nil {
			t.Fatalf("claim: %v", err)
		}
		if ok, err := ledger.MarkDispatched(context.Background(), in.ID, "", in.LeaseUntil); err != nil || !ok {
			t.Fatalf("dispatch move: ok=%v err=%v", ok, err)
		}
		d := payment.TerminalDispatch{IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents,
			RegisterID: in.RegisterID, DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey,
			Reference: "TRX-SALE1"}
		if _, err := term.Dispatch(context.Background(), d); err != nil {
			t.Fatalf("first reversal: %v", err)
		}
		if n := len(fs.fake.voidPosts_()); n != 1 {
			t.Fatalf("the first reversal posted %d voids, want 1", n)
		}
		if _, err := term.Dispatch(context.Background(), d); err == nil {
			t.Fatal("the retried reversal was answered: the customer would be refunded twice")
		}
		if n := len(fs.fake.voidPosts_()); n != 1 {
			t.Fatalf("the retried reversal posted %d voids, want 1: the customer would be refunded twice", n)
		}
		return true
	})
}

// TestRunTerminalConcurrentReversalsOfOneLegRefundOnce (BRIEF-COMMON:
// transactions, pool size 4 with three contenders): four reversals of one
// leg at the same time, against a fake that holds the void-or-refund POST
// open. The guard holds the check and the POST together, so exactly one
// void-or-refund POST is sent and the other three reversals are refused.
func TestRunTerminalConcurrentReversalsOfOneLegRefundOnce(t *testing.T) {
	testutil.LockTerminalTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	var branch uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branch); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	for i := 0; i < 5; i++ {
		concurrentReversalOnce(t, db, ctx, branch, i)
	}
}

// concurrentReversalOnce runs one iteration of the concurrent reversal.
func concurrentReversalOnce(t *testing.T, db *database.DB, ctx context.Context, branch uuid.UUID, i int) {
	t.Helper()
	sale := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO pos_transactions
			(id, register_id, cashier_id, subtotal, tax_amount, total, status, branch_id, change_due, revision, currency, number, created_at, updated_at)
			VALUES ($1, $2, $3, 0, 0, 0, 'OPEN', $4, 0, 1, 'USD', $5, NOW(), NOW())`,
		sale, "REG-M29-"+sale.String()[:8], uuid.New(), branch, "POS-"+sale.String()[:8]); err != nil {
		t.Fatalf("sale row: %v", err)
	}
	defer func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_lost_items WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_intents WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM pos_transactions WHERE id = $1`, sale)
	}()
	ledger := payment.NewTerminalLedger(db)
	fs := newRunFake(t)
	release := fs.fake.holdPosts()
	term := newRunDriver(t, fs, ledger)
	in := &payment.TerminalIntent{ID: uuid.New(), TransactionID: sale, LegKey: "refund-1",
		RegisterID: "REG-" + sale.String()[:12], BranchID: branch, AmountCents: 1200,
		Kind: payment.TerminalKindRefund, IdempotencyKey: "refund-" + sale.String(),
		LeaseUntil: time.Now().Add(time.Minute)}
	if err := ledger.Claim(ctx, in); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if ok, err := ledger.MarkDispatched(ctx, in.ID, "", in.LeaseUntil); err != nil || !ok {
		t.Fatalf("dispatch move: ok=%v err=%v", ok, err)
	}
	d := payment.TerminalDispatch{IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents,
		RegisterID: in.RegisterID, DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey,
		Reference: "TRX-SALE1"}
	start := make(chan struct{})
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, errs[n] = term.Dispatch(ctx, d)
		}(n)
	}
	close(start)
	// wait until the void-or-refund POST is in flight and held open: this is
	// the window in which a second reversal would refund again
	deadline := time.Now().Add(5 * time.Second)
	for len(fs.fake.voidPosts_()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the void-or-refund POST never arrived")
		}
		time.Sleep(2 * time.Millisecond)
	}
	release()
	wg.Wait()
	if n := len(fs.fake.voidPosts_()); n != 1 {
		t.Fatalf("iteration %d: four reversals of one leg posted %d voids, want 1: the customer would be refunded twice", i, n)
	}
	answered := 0
	for n, err := range errs {
		if err == nil {
			answered++
			continue
		}
		if !strings.Contains(err.Error(), "not sent again") {
			t.Fatalf("iteration %d: contender %d failed with %v, want the refusal of a repeated reversal", i, n, err)
		}
	}
	if answered != 1 {
		t.Fatalf("iteration %d: %d reversals were answered, want 1", i, answered)
	}
}

// TestRunTerminalInteractionWindowFitsInsideTheIntentLease: the poller has
// to be done before the ledger's lease runs out, or the sweep closes the leg
// while the device is still answering and files a manager item for an answer
// that is about to land. The driver's window is at most the lease, and a
// window set beyond it is clamped to it.
func TestRunTerminalInteractionWindowFitsInsideTheIntentLease(t *testing.T) {
	term := payment.NewRunTerminal(func() payment.GatewayConfig {
		return payment.GatewayConfig{APIKey: "teststub" + "teststub", MID: "MID-TESTSTUB", BaseURL: "http://127.0.0.1:1"}
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if got := term.InteractionWindow(); got > payment.TerminalAnswerLease {
		t.Fatalf("the interaction window is %s, want at most the intent lease %s", got, payment.TerminalAnswerLease)
	}
	term.WithInteractionWindow(10 * time.Minute)
	if got := term.InteractionWindow(); got > payment.TerminalAnswerLease {
		t.Fatalf("the interaction window is %s, want at most the intent lease %s", got, payment.TerminalAnswerLease)
	}
}

// TestRunTerminalTheDispatchRoomCoversTheLongestChargeCardCall: the lease
// clock starts before the charge-card call and the poller's window starts
// after that call answers, so the room the window leaves inside the lease has
// to cover the longest charge-card call the driver allows. A call that
// outlasted the room left the poller asking Run after the sweep had closed
// the leg: the window and the call have to fit inside the lease together.
func TestRunTerminalTheDispatchRoomCoversTheLongestChargeCardCall(t *testing.T) {
	term := payment.NewRunTerminal(func() payment.GatewayConfig {
		return payment.GatewayConfig{APIKey: "teststub" + "teststub", MID: "MID-TESTSTUB", BaseURL: "http://127.0.0.1:1"}
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	window, call := term.InteractionWindow(), term.CallTimeout()
	if window+call > payment.TerminalAnswerLease {
		t.Fatalf("the interaction window %s plus the longest charge-card call %s is %s, want at most the intent lease %s",
			window, call, window+call, payment.TerminalAnswerLease)
	}
}
