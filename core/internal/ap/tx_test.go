// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ap_test

// The transaction proofs of the AP module (the module recipe's set and the
// lane rule on transactions): a mutation, its journal entry, its audit row
// and its events are one fact, and every transaction runs on its own
// connection, never reaching for a second one from the pool. The concurrency
// tests run the whole service at pool size 4, the two payments of one
// invoice and the payment racing a void among them (ADR 0008 7.4).

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/ap"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type failingEvents struct{}

func (failingEvents) Write(context.Context, outbox.Event) error {
	return errors.New("outbox insert failed")
}

type failingAudit struct{}

func (failingAudit) Log(context.Context, audit.Entry) error {
	return errors.New("audit insert failed")
}

// createInput is the parsed bill the tx tests enter.
func (f *fixture) createInput(number string) *ap.Input {
	req := &ap.CreateRequest{
		VendorID:            ptr(f.vendor.String()),
		VendorInvoiceNumber: ptr(number),
		InvoiceDate:         ptr("2026-01-10"),
		DueDate:             ptr("2026-02-10"),
		Lines: []ap.CreateLineRequest{
			{Description: ptr("2x4x8 SPF"), Quantity: raw(`"1"`), UnitPriceTenThousand: raw(`1000`), GLAccountID: ptr(f.expense.String())},
		},
	}
	in, err := req.Parse()
	if err != nil {
		f.t.Fatal(err)
	}
	return in
}

func ptr[T any](v T) *T { return &v }

func raw(s string) []byte { return []byte(s) }

func payInput(f *fixture, cents int64, ids ...uuid.UUID) *ap.PayInput {
	amount := float64(cents) / 100.0
	req := &ap.PayRequest{
		VendorID: ptr(f.vendor.String()), Amount: &amount, Method: ptr("CHECK"),
		PaymentDate: ptr("2026-01-15"), InvoiceIDs: nil,
	}
	for _, id := range ids {
		req.InvoiceIDs = append(req.InvoiceIDs, id.String())
	}
	in, err := req.Parse()
	if err != nil {
		panic(err)
	}
	return in
}

func revision(n int64) ap.Precondition { return ap.Precondition{Revision: &n} }

// RULE (ADR 0003 section 2): a mutation whose event cannot be recorded does
// not happen, for each kind of write.
func TestTx_FailedEventWriteRollsBackEachWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	fail := f.service(failingEvents{}, audit.NewLogger(f.db), f.db)
	good := f.svc
	// An in-process caller marks itself a system caller: no branch context to
	// hold a payload branch against (ADR 0007 section 2.3).
	ctx := branchctx.WithSystem(context.Background())

	if _, err := fail.Create(ctx, f.createInput("TX-EV-1"), ap.Caller{}); err == nil {
		t.Error("create succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d bills survived a rolled back create", n)
	}

	created, err := good.Create(ctx, f.createInput("TX-EV-2"), ap.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fail.Transition(ctx, created.ID, &ap.TransitionInput{To: ap.StatusApproved}, revision(1), ap.Caller{}); err == nil {
		t.Error("approve succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'PENDING' AND revision = 1`, created.ID); n != 1 {
		t.Error("an approve survived a rolled back event")
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries WHERE source_ref_id = $1`, created.ID); n != 0 {
		t.Errorf("%d entries survived a rolled back approve", n)
	}

	approved, err := good.Transition(ctx, created.ID, &ap.TransitionInput{To: ap.StatusApproved}, revision(1), ap.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fail.Transition(ctx, approved.ID, &ap.TransitionInput{To: ap.StatusVoided, Reason: "wrong"}, revision(2), ap.Caller{}); err == nil {
		t.Error("void succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'APPROVED' AND revision = 2`, approved.ID); n != 1 {
		t.Error("a void survived a rolled back event")
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries r JOIN gl_journal_entries o ON o.id = r.reverses_entry_id WHERE o.source_ref_id = $1`, approved.ID); n != 0 {
		t.Errorf("%d reversals survived a rolled back void", n)
	}

	if _, err := fail.PayVendor(ctx, payInput(f, 1000, approved.ID), ap.Caller{}); err == nil {
		t.Error("payment succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM ap_payments WHERE vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d payments survived a rolled back payment", n)
	}
	if n := f.count(`SELECT count(*) FROM ap_payment_applications a JOIN vendor_invoices i ON i.id = a.invoice_id WHERE i.vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d applications survived a rolled back payment", n)
	}
	if got := f.count(`SELECT ROUND(amount_open * 100)::bigint FROM vendor_invoices WHERE id = $1`, approved.ID); got != 10 {
		t.Errorf("amount_open = %d after a rolled back payment, want 10", got)
	}
}

// The same rule for the audit row, for each kind of write.
func TestTx_FailedAuditWriteRollsBackEachWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	fail := f.service(outbox.NewWriter(f.db, ""), failingAudit{}, f.db)
	good := f.svc
	ctx := branchctx.WithSystem(context.Background())

	if _, err := fail.Create(ctx, f.createInput("TX-AU-1"), ap.Caller{}); err == nil {
		t.Error("create succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d bills survived a rolled back create", n)
	}

	created, err := good.Create(ctx, f.createInput("TX-AU-2"), ap.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fail.Transition(ctx, created.ID, &ap.TransitionInput{To: ap.StatusApproved}, revision(1), ap.Caller{}); err == nil {
		t.Error("approve succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries WHERE source_ref_id = $1`, created.ID); n != 0 {
		t.Errorf("%d entries survived a rolled back approve", n)
	}

	approved, err := good.Transition(ctx, created.ID, &ap.TransitionInput{To: ap.StatusApproved}, revision(1), ap.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fail.PayVendor(ctx, payInput(f, 1000, approved.ID), ap.Caller{}); err == nil {
		t.Error("payment succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM ap_payments WHERE vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d payments survived a rolled back payment", n)
	}
}

// gatedTx opens as many transactions as the pool has connections and holds
// each INSIDE its transaction at a gate before fn runs. The pool is then
// empty, so any statement that goes to the pool instead of the transaction
// blocks forever.
type gatedTx struct {
	db      *database.DB
	want    int32
	entered int32
	gate    sync.WaitGroup
}

func newGatedTx(db *database.DB, want int) *gatedTx {
	g := &gatedTx{db: db, want: int32(want)}
	g.gate.Add(want)
	return g
}

func (g *gatedTx) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return g.db.RunInTx(ctx, func(txCtx context.Context) error {
		if atomic.AddInt32(&g.entered, 1) <= g.want {
			g.gate.Done()
			g.gate.Wait()
		}
		return fn(txCtx)
	})
}

// Saturation: as many contenders as the pool has connections, held inside
// their transactions at a gate, for each kind of write. This is the test that
// fails when code inside a transaction uses the pool.
func TestTx_Pool4SaturationNeedsNoSecondConnection(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	events := outbox.NewWriter(f.db, "")
	good := f.service(events, audit.NewLogger(f.db), db)
	ctx, cancel := context.WithTimeout(branchctx.WithSystem(context.Background()), 40*time.Second)
	defer cancel()

	const contenders = 4
	var seeds []uuid.UUID
	for i := 0; i < contenders; i++ {
		inv, err := good.Create(ctx, f.createInput("TX-GATE-"+string(rune('A'+i))), ap.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := good.Transition(ctx, inv.ID, &ap.TransitionInput{To: ap.StatusApproved}, revision(1), ap.Caller{}); err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, inv.ID)
	}

	phase := func(name string, run func(svc *ap.Service, i int) error) {
		t.Helper()
		svc := f.service(events, audit.NewLogger(f.db), newGatedTx(db, contenders))
		var wg sync.WaitGroup
		errs := make(chan error, contenders)
		for i := 0; i < contenders; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if err := run(svc, i); err != nil {
					errs <- err
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("%s: %v", name, err)
		}
	}

	phase("create", func(svc *ap.Service, i int) error {
		_, err := svc.Create(ctx, f.createInput("TX-GATED-"+string(rune('a'+i))), ap.Caller{})
		return err
	})
	phase("approve", func(svc *ap.Service, i int) error {
		inv, err := svc.Create(ctx, f.createInput("TX-GATED-AP-"+string(rune('a'+i))), ap.Caller{})
		if err != nil {
			return err
		}
		// The create ran inside its own gated transaction; the approve is the
		// write under test.
		_, err = svc.Transition(ctx, inv.ID, &ap.TransitionInput{To: ap.StatusApproved}, ap.Precondition{Any: true}, ap.Caller{})
		return err
	})
	phase("void", func(svc *ap.Service, i int) error {
		inv, err := svc.Create(ctx, f.createInput("TX-GATED-VO-"+string(rune('a'+i))), ap.Caller{})
		if err != nil {
			return err
		}
		_, err = svc.Transition(ctx, inv.ID, &ap.TransitionInput{To: ap.StatusVoided, Reason: "gated"}, ap.Precondition{Any: true}, ap.Caller{})
		return err
	})
	phase("pay", func(svc *ap.Service, i int) error {
		_, err := svc.PayVendor(ctx, payInput(f, 5, seeds[i]), ap.Caller{}) // each seed owes 10 cents
		return err
	})
}

// Three contenders at pool size 4 (the lane rule on transactions): concurrent
// creates get distinct numbers and one event each, three racers on one
// revision have exactly one winner, two payments of one invoice apply
// exactly the money once, and a payment racing a void leaves exactly one
// winner. The GL ties after every race.
func TestTx_Concurrency_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	svc := f.svc
	ctx, cancel := context.WithTimeout(branchctx.WithSystem(context.Background()), 60*time.Second)
	defer cancel()

	// Concurrent creates: distinct numbers, one event each.
	const contenders, each = 3, 4
	var wg sync.WaitGroup
	errs := make(chan error, contenders*each)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if _, err := svc.Create(ctx, f.createInput("TX-CC-"+string(rune('A'+i))+string(rune('0'+j))), ap.Caller{}); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent create: %v", err)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE vendor_id = $1`, f.vendor); n != contenders*each {
		t.Fatalf("%d bills created, want %d", n, contenders*each)
	}
	if n := f.count(`SELECT count(*) FROM events_outbox WHERE entity_type = 'vendor_invoice' AND entity_id IN (SELECT id FROM vendor_invoices WHERE vendor_id = $1)`, f.vendor); n != contenders*each {
		t.Errorf("%d events for %d bills", n, contenders*each)
	}
	if n := f.count(`SELECT count(DISTINCT number) FROM vendor_invoices WHERE vendor_id = $1`, f.vendor); n != contenders*each {
		t.Errorf("%d distinct numbers for %d bills", n, contenders*each)
	}

	// Three racers on one revision: exactly one winner.
	made, err := svc.Create(ctx, f.createInput("TX-RACE"), ap.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	var won, stale, failed atomic.Int32
	var raceWG sync.WaitGroup
	for i := 0; i < contenders; i++ {
		raceWG.Add(1)
		go func() {
			defer raceWG.Done()
			_, err := svc.Transition(ctx, made.ID, &ap.TransitionInput{To: ap.StatusApproved}, revision(1), ap.Caller{})
			switch {
			case err == nil:
				won.Add(1)
			default:
				if e, ok := err.(*httpx.Error); ok && e.Status == http.StatusConflict {
					stale.Add(1)
				} else {
					t.Errorf("racer: %v", err)
					failed.Add(1)
				}
			}
		}()
	}
	raceWG.Wait()
	if won.Load() != 1 || stale.Load() != contenders-1 || failed.Load() != 0 {
		t.Errorf("%d winners, %d stale, %d failed; want 1, %d, 0", won.Load(), stale.Load(), failed.Load(), contenders-1)
	}

	// Two payments of one invoice: the locks serialize them, the open amount
	// is applied exactly once, and the control account still ties.
	payable, err := svc.Create(ctx, f.createInput("TX-PAY2"), ap.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transition(ctx, payable.ID, &ap.TransitionInput{To: ap.StatusApproved}, revision(1), ap.Caller{}); err != nil {
		t.Fatal(err)
	}
	var payErrs = make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// each pays 5 of the bill's 10 cents: both succeed under the
			// locks, and the 10 cents are applied exactly once between them.
			if _, err := svc.PayVendor(ctx, payInput(f, 5, payable.ID), ap.Caller{}); err != nil {
				payErrs <- err
			}
		}()
	}
	wg.Wait()
	close(payErrs)
	for err := range payErrs {
		t.Fatalf("concurrent payment: %v", err)
	}
	if applied := f.count(`SELECT ROUND(COALESCE(SUM(amount), 0) * 100)::bigint FROM ap_payment_applications WHERE invoice_id = $1`, payable.ID); applied != 10 {
		t.Errorf("%d cents applied to a 10 cent bill, want 10 exactly once", applied)
	}
	if open := f.count(`SELECT ROUND(amount_open * 100)::bigint FROM vendor_invoices WHERE id = $1`, payable.ID); open != 0 {
		t.Errorf("amount_open = %d after the payments, want 0", open)
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Errorf("after the payments: control %d, open %d", got, want)
	}
	if !f.balanced() {
		t.Error("a payment entry does not balance")
	}

	// A payment racing a void on one bill: the row lock serializes them, and
	// exactly one wins.
	raced, err := svc.Create(ctx, f.createInput("TX-PAYVOID"), ap.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transition(ctx, raced.ID, &ap.TransitionInput{To: ap.StatusApproved}, revision(1), ap.Caller{}); err != nil {
		t.Fatal(err)
	}
	var succeeded atomic.Int32
	var raceErrs = make(chan error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := svc.PayVendor(ctx, payInput(f, 1000, raced.ID), ap.Caller{}); err != nil {
			if e, ok := err.(*httpx.Error); !ok || e.Status != http.StatusConflict {
				raceErrs <- err
			}
		} else {
			succeeded.Add(1)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := svc.Transition(ctx, raced.ID, &ap.TransitionInput{To: ap.StatusVoided, Reason: "racing"}, revision(2), ap.Caller{}); err != nil {
			if e, ok := err.(*httpx.Error); !ok || e.Status != http.StatusConflict {
				raceErrs <- err
			}
		} else {
			succeeded.Add(1)
		}
	}()
	wg.Wait()
	close(raceErrs)
	for err := range raceErrs {
		t.Fatalf("payment racing void: %v", err)
	}
	if succeeded.Load() != 1 {
		t.Errorf("%d of the payment and the void won, want exactly 1", succeeded.Load())
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Errorf("after the race: control %d, open %d", got, want)
	}
}

// RULE (ADR 0008 7.4 and section 9): the payment locks the named bills in id
// order whatever order the request named them, so concurrent payments of the
// same bills serialize on the locks instead of deadlocking. Each round races
// three payments that name the same three bills in rotated orders; a lock
// order that follows the request would cancel a contender with a deadlock.
func TestTx_PaymentLocksInIDOrder(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	svc := f.svc
	ctx, cancel := context.WithTimeout(branchctx.WithSystem(context.Background()), 60*time.Second)
	defer cancel()

	const rounds = 5
	for r := 0; r < rounds; r++ {
		ids := make([]uuid.UUID, 3)
		for i := range ids {
			inv, err := svc.Create(ctx, f.createInput("TX-ORD-"+string(rune('A'+r))+string(rune('0'+i))), ap.Caller{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Transition(ctx, inv.ID, &ap.TransitionInput{To: ap.StatusApproved}, revision(1), ap.Caller{}); err != nil {
				t.Fatal(err)
			}
			ids[i] = inv.ID
		}
		orders := [][]uuid.UUID{
			{ids[0], ids[1], ids[2]},
			{ids[2], ids[1], ids[0]},
			{ids[1], ids[2], ids[0]},
		}
		var wg sync.WaitGroup
		errs := make(chan error, len(orders))
		for i := range orders {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// each bill owes 10 cents and every payment asks for 3, so
				// every contender succeeds whatever order it wins the locks in.
				if _, err := svc.PayVendor(ctx, payInput(f, 3, orders[i]...), ap.Caller{}); err != nil {
					errs <- err
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: concurrent payment across the same bills: %v", r, err)
		}
		applied := f.count(`SELECT ROUND(COALESCE(SUM(amount), 0) * 100)::bigint FROM ap_payment_applications a
			JOIN vendor_invoices i ON i.id = a.invoice_id WHERE i.vendor_id = $1 AND i.number LIKE 'AP-%'`, f.vendor)
		if want := int64(9 * (r + 1)); applied != want {
			t.Fatalf("round %d: %d cents applied so far, want %d", r, applied, want)
		}
		if got, want := f.controlBalance(), f.openSum(); got != want {
			t.Fatalf("round %d: control %d, open %d", r, got, want)
		}
	}
}
