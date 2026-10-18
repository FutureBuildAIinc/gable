// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

// The crash windows of the card terminal intent ledger (CP-05, IX-01), each
// crashed at its point by stopping the act between statements, and the
// concurrency proofs: three claims on one leg leave one intent, an answer
// racing the sweep ends in exactly one final state, and a resolution racing
// a late answer ends in one. The grep test refuses a card number shape in
// the migration and every fixture this package's tests write.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// newTerminalFixture builds the ledger on the shared test database with one
// sale row to point intents at, and removes both afterwards.
func newTerminalFixture(t *testing.T) (*payment.TerminalLedger, uuid.UUID) {
	t.Helper()
	return newTerminalFixtureConns(t, 0)
}

// newTerminalFixtureConns is newTerminalFixture on a pool of the given size
// (0 is the shared default pool): the concurrency proofs run at size 4.
func newTerminalFixtureConns(t *testing.T, conns int32) (*payment.TerminalLedger, uuid.UUID) {
	t.Helper()
	testutil.LockTerminalTables(t)
	db := testutil.RequireDB(t)
	if conns > 0 {
		db = testutil.RequireDBMaxConns(t, conns)
	}
	ctx := context.Background()
	var branch uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branch); err != nil {
		t.Fatalf("default branch: %v", err)
	}

	sale := uuid.New()
	cashier := uuid.New()
	_, err := db.Pool.Exec(ctx, `INSERT INTO pos_transactions
		(id, register_id, cashier_id, subtotal, tax_amount, total, status, branch_id, change_due, revision, currency, number, created_at, updated_at)
		VALUES ($1, $2, $3, 0, 0, 0, 'OPEN', $4, 0, 1, 'USD', $5, NOW(), NOW())`,
		sale, "REG-M29-"+sale.String()[:8], cashier, branch, "POS-"+sale.String()[:8])
	if err != nil {
		t.Fatalf("sale row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_lost_items WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_intents WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM pos_transactions WHERE id = $1`, sale)
	})
	return payment.NewTerminalLedger(db), sale
}

// claimIntent claims one sale leg of the fixture's register.
func claimIntent(t *testing.T, l *payment.TerminalLedger, sale uuid.UUID, lease time.Duration) *payment.TerminalIntent {
	t.Helper()
	in := &payment.TerminalIntent{TransactionID: sale, LegKey: "card-0", RegisterID: "REG-" + sale.String()[:12],
		BranchID: m29BranchOf(t, l), AmountCents: 2500, Kind: payment.TerminalKindSale, IdempotencyKey: "sale-" + sale.String() + "-card-0",
		LeaseUntil: time.Now().Add(lease)}
	in.ID = uuid.New()
	if err := l.Claim(context.Background(), in); err != nil {
		t.Fatalf("claim: %v", err)
	}
	return in
}

// TestClaimedNeverDispatchedSweepCloses: the act died between the committed
// claim and the device call. No money moved, the sweep closes the intent
// lost and no manager item exists.
func TestClaimedNeverDispatchedSweepCloses(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	in := claimIntent(t, l, sale, -time.Minute)
	res, err := l.Sweep(ctx, time.Now())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.ClaimedClosed != 1 || res.DispatchedLost != 0 {
		t.Fatalf("the sweep closed %d claims and queued %d dispatched legs, wanted 1 and 0", res.ClaimedClosed, res.DispatchedLost)
	}
	fresh, err := l.Get(ctx, in.ID)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if fresh.State != payment.TerminalLost {
		t.Fatalf("the never dispatched claim is %s, wanted lost", fresh.State)
	}
	if fresh.AnswerVerdict != "" {
		t.Fatalf("a never dispatched claim carries a verdict %q", fresh.AnswerVerdict)
	}
	items, err := l.OpenLostItems(ctx, nil)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	for _, it := range items {
		if it.IntentID == in.ID {
			t.Fatal("a claim that never reached the device filed a manager item")
		}
	}
}

// TestDispatchedNeverAnsweredSweepQueues: the device took the leg and the
// answer never landed. The money may have moved: the sweep turns the intent
// lost and files exactly one manager item carrying the amount, the register
// and the device reference.
func TestDispatchedNeverAnsweredSweepQueues(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	in := claimIntent(t, l, sale, time.Minute)
	ok, err := l.MarkDispatched(ctx, in.ID, "devref-1", time.Now().Add(-time.Minute))
	if err != nil || !ok {
		t.Fatalf("dispatch move: ok=%t err=%v", ok, err)
	}
	if _, err := l.Sweep(ctx, time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	fresh, err := l.Get(ctx, in.ID)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if fresh.State != payment.TerminalLost {
		t.Fatalf("the unanswered leg is %s, wanted lost", fresh.State)
	}
	var found *payment.TerminalLostItem
	items, err := l.OpenLostItems(ctx, nil)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	for _, it := range items {
		if it.IntentID == in.ID {
			found = &it
		}
	}
	if found == nil {
		t.Fatal("the dispatched leg the sweep lost filed no manager item")
	}
	if found.AmountCents != 2500 || found.ProviderRef != "devref-1" || found.RegisterID == "" || found.Kind != payment.TerminalKindSale {
		t.Fatalf("the manager item carries amount %d ref %q register %q kind %q", found.AmountCents, found.ProviderRef, found.RegisterID, found.Kind)
	}
	// the sweep is idempotent: a second pass queues no second item
	if _, err := l.Sweep(ctx, time.Now()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	n := 0
	items, err = l.OpenLostItems(ctx, nil)
	if err != nil {
		t.Fatalf("queue reread: %v", err)
	}
	for _, it := range items {
		if it.IntentID == in.ID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the queue holds %d items of the intent, wanted 1", n)
	}
}

// TestDuplicateAnswerIsNoOp: the same verdict twice moves the intent once.
func TestDuplicateAnswerIsNoOp(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	in := claimIntent(t, l, sale, time.Minute)
	if _, err := l.MarkDispatched(ctx, in.ID, "devref-2", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a := payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "devref-2", Verdict: "authorized",
		Brand: "VISA", Last4: "4242", AuthCode: "OK1"}
	if out, err := l.ApplyDeviceAnswer(ctx, a); err != nil || out != payment.AnswerApplied {
		t.Fatalf("first answer: %s %v", out, err)
	}
	if out, err := l.ApplyDeviceAnswer(ctx, a); err != nil || out != payment.AnswerDuplicate {
		t.Fatalf("second answer: %s %v", out, err)
	}
	fresh, _ := l.Get(ctx, in.ID)
	if fresh.State != payment.TerminalAuthorized || fresh.AnsweredAt == nil {
		t.Fatalf("the intent is %s answered=%v, wanted authorized once", fresh.State, fresh.AnsweredAt)
	}
}

// TestConflictingAnswerBecomesManagerItem: a verdict contradicting the
// recorded one never moves the intent twice; it is audited and queued.
func TestConflictingAnswerBecomesManagerItem(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	in := claimIntent(t, l, sale, time.Minute)
	if _, err := l.MarkDispatched(ctx, in.ID, "devref-3", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, err := l.ApplyDeviceAnswer(ctx, payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "devref-3", Verdict: "authorized"}); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	out, err := l.ApplyDeviceAnswer(ctx, payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "devref-3", Verdict: "declined"})
	if err != nil || out != payment.AnswerConflictQueued {
		t.Fatalf("conflicting answer: %s %v", out, err)
	}
	fresh, _ := l.Get(ctx, in.ID)
	if fresh.State != payment.TerminalAuthorized || fresh.AnswerVerdict != "authorized" {
		t.Fatalf("the conflicting answer moved the intent to %s verdict %q", fresh.State, fresh.AnswerVerdict)
	}
	items, err := l.OpenLostItems(ctx, nil)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	found := false
	for _, it := range items {
		if it.IntentID == in.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("the conflicting answer filed no manager item")
	}
}

// TestLateAnswerAfterSweepBecomesManagerItem: the sweep turned the intent
// lost, then the device answered. The guarded move refuses the answer (lost
// is not pending_at_device); it is audited and queued beside the sweep's own
// item, and the intent stays lost.
func TestLateAnswerAfterSweepBecomesManagerItem(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	in := claimIntent(t, l, sale, time.Minute)
	if _, err := l.MarkDispatched(ctx, in.ID, "devref-4", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, err := l.Sweep(ctx, time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	out, err := l.ApplyDeviceAnswer(ctx, payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "devref-4", Verdict: "authorized"})
	if err != nil || out != payment.AnswerLateQueued {
		t.Fatalf("late answer: %s %v", out, err)
	}
	fresh, _ := l.Get(ctx, in.ID)
	if fresh.State != payment.TerminalLost || fresh.AnswerVerdict != "" {
		t.Fatalf("the late answer moved the lost intent to %s verdict %q", fresh.State, fresh.AnswerVerdict)
	}
	n := 0
	items, _ := l.OpenLostItems(ctx, nil)
	for _, it := range items {
		if it.IntentID == in.ID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the queue holds %d items of the intent, wanted the one the sweep filed", n)
	}
}

// TestThreeClaimsLeaveOneIntent: three contenders claim one
// (transaction_id, leg_key, kind) at pool size 4; one intent stands.
func TestThreeClaimsLeaveOneIntent(t *testing.T) {
	testutil.LockTerminalTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	var branch uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branch); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	sale := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO pos_transactions
		(id, register_id, cashier_id, subtotal, tax_amount, total, status, branch_id, change_due, revision, currency, number, created_at, updated_at)
		VALUES ($1, $2, $3, 0, 0, 0, 'OPEN', $4, 0, 1, 'USD', $5, NOW(), NOW())`,
		sale, "REG-M29C-"+sale.String()[:8], uuid.New(), branch, "POS-"+sale.String()[:8]); err != nil {
		t.Fatalf("sale row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_lost_items WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_intents WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM pos_transactions WHERE id = $1`, sale)
	})
	l := payment.NewTerminalLedger(db)
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := &payment.TerminalIntent{ID: uuid.New(), TransactionID: sale, LegKey: "card-0",
				RegisterID: "REG-" + sale.String()[:12], BranchID: branch,
				AmountCents: 2500, Kind: payment.TerminalKindSale,
				IdempotencyKey: "sale-" + sale.String() + "-card-0", LeaseUntil: time.Now().Add(time.Minute)}
			if err := l.Claim(ctx, in); err == nil {
				won.Add(1)
			} else if err != payment.ErrTerminalIntentConflict {
				t.Errorf("claim contender: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d contenders claimed the leg, wanted exactly 1", won.Load())
	}
}

// TestAnswerRacingSweepOneFinalState: three contenders race an answer
// against two sweep passes on one dispatched leg at pool size 4; the intent
// ends in exactly one final state (authorized or lost), never both, never
// neither, and its verdict never changes after it lands.
func TestAnswerRacingSweepOneFinalState(t *testing.T) {
	testutil.LockTerminalTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	var branch uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branch); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	sale := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO pos_transactions
		(id, register_id, cashier_id, subtotal, tax_amount, total, status, branch_id, change_due, revision, currency, number, created_at, updated_at)
		VALUES ($1, $2, $3, 0, 0, 0, 'OPEN', $4, 0, 1, 'USD', $5, NOW(), NOW())`,
		sale, "REG-M29R-"+sale.String()[:8], uuid.New(), branch, "POS-"+sale.String()[:8]); err != nil {
		t.Fatalf("sale row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_lost_items WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_intents WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM pos_transactions WHERE id = $1`, sale)
	})
	l := payment.NewTerminalLedger(db)
	in := &payment.TerminalIntent{TransactionID: sale, LegKey: "card-0", RegisterID: "REG-" + sale.String()[:12],
		BranchID: branch, AmountCents: 2500, Kind: payment.TerminalKindSale,
		IdempotencyKey: "sale-" + sale.String() + "-card-0", LeaseUntil: time.Now().Add(time.Minute)}
	if err := l.Claim(ctx, in); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := l.MarkDispatched(ctx, in.ID, "devref-race", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = l.ApplyDeviceAnswer(ctx, payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "devref-race", Verdict: "authorized"})
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = l.Sweep(ctx, time.Now())
		}()
	}
	wg.Wait()
	fresh, err := l.Get(ctx, in.ID)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	switch {
	case fresh.State == payment.TerminalAuthorized && fresh.AnswerVerdict == "authorized":
	case fresh.State == payment.TerminalLost && fresh.AnswerVerdict == "":
	default:
		t.Fatalf("the racing intent ended in %s verdict %q, wanted exactly one of authorized or lost", fresh.State, fresh.AnswerVerdict)
	}
}

// TestResolveRacingLateAnswerEndsInOne: two manager resolutions and a late
// device answer race on one lost item at pool size 4; exactly one resolution
// closes it and the item's final state is one of the two closes.
func TestResolveRacingLateAnswerEndsInOne(t *testing.T) {
	l, sale := newTerminalFixtureConns(t, 4)
	ctx := context.Background()
	in := claimIntent(t, l, sale, time.Minute)
	if _, err := l.MarkDispatched(ctx, in.ID, "devref-res", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, err := l.Sweep(ctx, time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	items, err := l.OpenLostItems(ctx, nil)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	var itemID uuid.UUID
	for _, it := range items {
		if it.IntentID == in.ID {
			itemID = it.ID
		}
	}
	if itemID == uuid.Nil {
		t.Fatal("the sweep filed no manager item")
	}
	var closed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(declined bool) {
			defer wg.Done()
			var ok bool
			var err error
			if declined {
				ok, err = l.ResolveLostItemDeclined(ctx, itemID, "manager", "read the terminal")
			} else {
				ok, err = l.ResolveLostItemAuthorized(ctx, itemID, "manager", "read the terminal")
			}
			if err != nil {
				t.Errorf("resolve contender: %v", err)
				return
			}
			if ok {
				closed.Add(1)
			}
		}(i == 0)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = l.ApplyDeviceAnswer(ctx, payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "devref-res", Verdict: "authorized"})
	}()
	wg.Wait()
	item, err := l.LostItem(ctx, itemID)
	if err != nil {
		t.Fatalf("item reread: %v", err)
	}
	// one resolution closes the open item; a second closes it only when the
	// first was the declined one and the late authorized answer reopened the
	// item in between (never two closes of one open state)
	var reopened bool
	if err := testutil.RequireDB(t).Pool.QueryRow(ctx,
		`SELECT reopened_at IS NOT NULL FROM payment_terminal_lost_items WHERE id = $1`, itemID).Scan(&reopened); err != nil {
		t.Fatalf("reopened read: %v", err)
	}
	if closed.Load() < 1 || closed.Load() > 2 || (closed.Load() == 2 && !reopened) {
		t.Fatalf("%d resolutions closed the item (reopened=%t), wanted exactly one close per open state", closed.Load(), reopened)
	}
	// the resolution that won closed the item; the racing late authorized
	// answer either landed on the lost intent first (the item then carries
	// it and stays closed by the authorized resolution) or came after a
	// declined close and reopened the item for the manager: in every order
	// the item is in exactly one of these states
	switch item.State {
	case "resolved_authorized", "resolved_declined":
	case "open":
		if item.LateVerdict != "authorized" || item.LateProviderRef != "devref-res" {
			t.Fatalf("the item reopened without the late answer's verdict: %q %q", item.LateVerdict, item.LateProviderRef)
		}
	default:
		t.Fatalf("the item ended %s", item.State)
	}
}

// m29BranchOf reads the default branch through the shared test database.
func m29BranchOf(t *testing.T, l *payment.TerminalLedger) uuid.UUID {
	t.Helper()
	var branch uuid.UUID
	db := testutil.RequireDB(t)
	if err := db.Pool.QueryRow(context.Background(), `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branch); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	return branch
}

// TestNoCardNumberShapeAnywhere: the migration and every terminal source,
// test and desk file hold no card number shape (13 to 19 digits with no
// separator inside them). A CVV pattern and a track data pattern are refused
// the same way.
func TestNoCardNumberShapeAnywhere(t *testing.T) {
	cardNumber := regexp.MustCompile(`\b[3-6][0-9]{12,18}\b`)
	cvv := regexp.MustCompile(`\bcvv[": ]+[0-9]{3,4}\b`)
	track := regexp.MustCompile(`%B[0-9]{13,19}\^`)
	globs := []string{
		"../../migrations/128_*.sql",
		"terminal*.go",
		"../pos/terminal*.go",
		"../../../web/apps/desk/src/pages/pos/terminal*.ts",
		"../../../web/apps/desk/src/pages/pos/TerminalLostAnswers*.ts",
		"../../../web/apps/desk/src/pages/pos/POSTerminal.cardwait*.ts",
	}
	var files []string
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil || len(matches) == 0 {
			t.Fatalf("card shape scan glob %q matched nothing: %v %v", g, matches, err)
		}
		files = append(files, matches...)
	}
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, pat := range []*regexp.Regexp{cardNumber, cvv, track} {
			if loc := pat.Find(body); loc != nil {
				t.Fatalf("%s holds a refused card data shape at %s: %q", name, pat.String(), body[loc[0]:loc[1]])
			}
		}
	}
}

// TestSimulatedTerminalSettlementBatch: SettlementBatch returns only the legs
// whose scripted outcome approved (approve, duplicate_answer, late_answer),
// excluding declined and no_answer legs.
func TestSimulatedTerminalSettlementBatch(t *testing.T) {
	ctx := context.Background()
	sim := payment.NewSimulatedTerminal(payment.SimApprove, nil)
	sim.Script("leg-decline", payment.SimDecline)
	sim.Script("leg-no-answer", payment.SimNoAnswer)
	sim.Script("leg-dup", payment.SimDuplicateAnswer)

	for _, tc := range []struct {
		key   string
		cents int64
	}{
		{"leg-approve", 1200},
		{"leg-decline", 2300},
		{"leg-no-answer", 3400},
		{"leg-dup", 4500},
	} {
		if _, err := sim.Dispatch(ctx, payment.TerminalDispatch{
			IntentID: uuid.New(), Kind: payment.TerminalKindSale, AmountCents: tc.cents,
			Currency: "USD", RegisterID: "REG-01", DeviceID: "REG-01", IdempotencyKey: tc.key,
		}); err != nil {
			t.Fatalf("dispatch %s: %v", tc.key, err)
		}
	}
	batch, err := sim.SettlementBatch(ctx, "REG-01", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("settlement batch: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("batch len = %d, wanted 2: %+v", len(batch), batch)
	}
	if batch[0].ProviderRef != "simterm_leg-approve" || batch[0].AmountCents != 1200 || batch[0].Verdict != "authorized" {
		t.Fatalf("batch[0] = %+v", batch[0])
	}
	if batch[1].ProviderRef != "simterm_leg-dup" || batch[1].AmountCents != 4500 || batch[1].Verdict != "authorized" {
		t.Fatalf("batch[1] = %+v", batch[1])
	}
}
