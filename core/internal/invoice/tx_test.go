// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

// The transaction proofs of the recipe (step 2) for every write of the module:
// a failing event write rolls the act back (no row, no entry, no stock move, no
// subledger move, no audit row); three contenders at pool size 4 finish with one
// winner where they race for one document; and the gated saturation test holds
// as many contenders as the pool has connections INSIDE their transactions
// before any statement runs, so a statement that reached for the pool instead of
// the transaction would starve the pool and the deadline would fire.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// flaky fails the event types it is told to.
type flaky struct {
	inner invoice.EventRecorder
	mu    sync.Mutex
	fail  map[string]bool
}

func (f *flaky) set(typ string, fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string]bool{}
	}
	f.fail[typ] = fail
}

func (f *flaky) Write(ctx context.Context, ev outbox.Event) error {
	f.mu.Lock()
	failing := f.fail[ev.Type]
	f.mu.Unlock()
	if failing {
		return fmt.Errorf("outbox insert failed")
	}
	return f.inner.Write(ctx, ev)
}

// snapshot is the observable state a rolled back act must leave untouched.
type snapshot struct {
	stock                     string
	balance                   int64
	entries, auditRows        int64
	invoiceRev, memoRev       int64
	invoiceStatus, memoStatus string
}

func (f *fixture) snap(invoiceID, memoID string) snapshot {
	f.t.Helper()
	s := snapshot{stock: f.stock(), balance: f.balance()}
	s.entries = countOf(f.t, f.db, `SELECT count(*) FROM gl_journal_entries e WHERE e.source_ref_id IN (SELECT id FROM invoices WHERE customer_id = $1 UNION SELECT id FROM credit_memos WHERE customer_id = $1)
		OR e.reverses_entry_id IN (SELECT gl_entry_id FROM invoices WHERE customer_id = $1 UNION SELECT gl_entry_id FROM credit_memos WHERE customer_id = $1)`, f.customerID)
	s.auditRows = countOf(f.t, f.db, `SELECT count(*) FROM audit_log WHERE entity_type IN ('invoice', 'credit_memo') AND (entity_id IN (SELECT id FROM invoices WHERE customer_id = $1) OR entity_id IN (SELECT id FROM credit_memos WHERE customer_id = $1))`, f.customerID)
	if invoiceID != "" {
		_ = f.db.Pool.QueryRow(context.Background(), `SELECT revision, status FROM invoices WHERE id = $1`, invoiceID).Scan(&s.invoiceRev, &s.invoiceStatus)
	}
	if memoID != "" {
		_ = f.db.Pool.QueryRow(context.Background(), `SELECT revision, status FROM credit_memos WHERE id = $1`, memoID).Scan(&s.memoRev, &s.memoStatus)
	}
	return s
}

// RULE (ADR 0003 section 3, ADR 0005 14.2): a failing event write rolls the whole
// act back, for each kind of write the module makes.
func TestFailingEventWriteRollsEachActBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	events := &flaky{inner: outbox.NewWriter(db, "")}
	f := newFixture(t, db, func(f *fixture) { f.invoices.WithOutbox(events) })
	invID, orderID := f.invoice("10")
	line := f.firstLineID(invID)

	// credit_memo.created: no memo, no lines
	events.set("credit_memo.created", true)
	before := f.snap(invID, "")
	if r := f.do("POST", "/api/v1/credit-memos", f.creditBody(invID, returnLine(line, "-4", true))); r.status < 500 {
		t.Fatalf("create with a failing event = %d, want 5xx", r.status)
	}
	if n := countOf(t, db, `SELECT count(*) FROM credit_memos WHERE customer_id = $1`, f.customerID); n != 0 {
		t.Errorf("%d credit memos after a failed create", n)
	}
	if after := f.snap(invID, ""); after != before {
		t.Errorf("a failed create changed the state: %+v -> %+v", before, after)
	}
	events.set("credit_memo.created", false)
	cm := f.createCredit(f.creditBody(invID, returnLine(line, "-4", true)))
	id := str(t, cm.body, "id")

	// credit_memo.updated: the draft is as it was
	events.set("credit_memo.updated", true)
	edit := f.creditBody(invID, returnLine(line, "-2", false))
	if r := f.do("PUT", "/api/v1/credit-memos/"+id, edit, "If-Match", `"1"`); r.status < 500 {
		t.Fatalf("update with a failing event = %d, want 5xx", r.status)
	}
	got := f.do("GET", "/api/v1/credit-memos/"+id, nil)
	if rev(t, got) != 1 || num(t, got.body, "subtotal_cents") != -2200 {
		t.Errorf("a failed update changed the draft: revision %d subtotal %v", rev(t, got), got.body["subtotal_cents"])
	}
	events.set("credit_memo.updated", false)

	// credit_memo.posted: still a draft, no number, no entry, no restock, no balance move
	events.set("credit_memo.posted", true)
	before = f.snap(invID, id)
	if r := f.postCredit(id, 1); r.status < 500 {
		t.Fatalf("post with a failing event = %d, want 5xx", r.status)
	}
	if after := f.snap(invID, id); after != before {
		t.Errorf("a failed post changed the state: %+v -> %+v", before, after)
	}
	if got := f.do("GET", "/api/v1/credit-memos/"+id, nil); got.body["number"] != nil || str(t, got.body, "status") != "draft" {
		t.Errorf("after a failed post the memo is %v %v", got.body["status"], got.body["number"])
	}
	events.set("credit_memo.posted", false)
	if r := f.postCredit(id, 1); r.status != 200 {
		t.Fatalf("post = %d: %s", r.status, r.raw)
	}

	// credit_memo.voided on a posted, restocking memo: still open, stock as posted
	events.set("credit_memo.voided", true)
	before = f.snap(invID, id)
	if r := f.do("POST", "/api/v1/credit-memos/"+id+"/transitions", map[string]any{"to": "void", "revision": 2, "reason": "x"}); r.status < 500 {
		t.Fatalf("void with a failing event = %d, want 5xx", r.status)
	}
	if after := f.snap(invID, id); after != before {
		t.Errorf("a failed credit memo void changed the state: %+v -> %+v", before, after)
	}
	events.set("credit_memo.voided", false)
	if r := f.do("POST", "/api/v1/credit-memos/"+id+"/transitions", map[string]any{"to": "void", "revision": 2, "reason": "x"}); r.status != 200 {
		t.Fatalf("void = %d: %s", r.status, r.raw)
	}

	// invoice.voided: unpaid, nothing reversed, the order still fulfilled
	events.set("invoice.voided", true)
	before = f.snap(invID, "")
	if r := f.voidInvoice(invID, rev(t, f.getInvoice(invID)), "x"); r.status < 500 {
		t.Fatalf("invoice void with a failing event = %d, want 5xx", r.status)
	}
	if after := f.snap(invID, ""); after != before {
		t.Errorf("a failed invoice void changed the state: %+v -> %+v", before, after)
	}
	if o := f.do("GET", "/api/v1/orders/"+orderID, nil); str(t, o.body, "status") != "fulfilled" {
		t.Errorf("a failed invoice void reopened the order: %v", o.body["status"])
	}
	if n, _ := f.reversalLegs(invID); n != 0 {
		t.Errorf("%d reversal entries after a failed void", n)
	}
	events.set("invoice.voided", false)
	// the same act now succeeds: nothing was left half done
	if r := f.voidInvoice(invID, rev(t, f.getInvoice(invID)), "x"); r.status != 200 {
		t.Fatalf("void = %d: %s", r.status, r.raw)
	}
}

// three racers, one document: exactly one wins the post, one wins the void.
func TestThreeContendersAtPool4(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1. three drafts on one invoice posted at once: each a distinct number, the
	// invoice credited exactly once per unit, no deadlock
	invID, _ := f.invoice("9")
	line := f.firstLineID(invID)
	var drafts []string
	for i := 0; i < 3; i++ {
		drafts = append(drafts, str(t, f.createCredit(f.creditBody(invID, returnLine(line, "-3", true))).body, "id"))
	}
	var wg sync.WaitGroup
	numbers := make(chan string, 3)
	for _, d := range drafts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.postCredit(d, 1)
			if r.status != 200 {
				t.Errorf("concurrent post = %d: %s", r.status, r.raw)
				return
			}
			numbers <- str(t, r.body, "number")
		}()
	}
	wg.Wait()
	close(numbers)
	seen := map[string]bool{}
	for n := range numbers {
		if seen[n] {
			t.Errorf("number %s minted twice", n)
		}
		seen[n] = true
	}
	if len(seen) != 3 {
		t.Errorf("%d numbers, want 3 distinct", len(seen))
	}
	if f.stock() != "100.0000/0.0000" {
		t.Errorf("stock after three restocks of 3 = %s, want 100 (9 billed, 9 returned)", f.stock())
	}
	if f.balance() != 0 {
		t.Errorf("balance after crediting the whole invoice = %d, want 0", f.balance())
	}
	if n := countOf(t, db, `SELECT count(*) FROM events_outbox WHERE type = 'credit_memo.posted' AND entity_id = ANY($1)`, toUUIDs(drafts)); n != 3 {
		t.Errorf("%d credit_memo.posted events, want 3", n)
	}

	// 2. three racers on one invoice's revision: exactly one void wins
	inv2, _ := f.invoice("1")
	r2 := rev(t, f.getInvoice(inv2))
	var winners, losers int32
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch r := f.voidInvoice(inv2, r2, "race"); r.status {
			case 200:
				atomic.AddInt32(&winners, 1)
			case http.StatusConflict:
				atomic.AddInt32(&losers, 1)
			default:
				t.Errorf("racing void = %d: %s", r.status, r.raw)
			}
		}()
	}
	wg.Wait()
	if winners != 1 || losers != 2 {
		t.Errorf("winners %d losers %d, want exactly one winner and two refused", winners, losers)
	}
	if n := countOf(t, db, `SELECT count(*) FROM events_outbox WHERE type = 'invoice.voided' AND entity_id = $1`, inv2); n != 1 {
		t.Errorf("%d invoice.voided events, want 1", n)
	}

	// 3. mixed writers (voids of distinct invoices, credit memo creates) while a
	// reader pages the list on the same four connections
	var invoices []string
	for i := 0; i < 3; i++ {
		id, _ := f.invoice("1")
		invoices = append(invoices, id)
	}
	stop := make(chan struct{})
	readerDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				readerDone <- nil
				return
			default:
			}
			if _, _, _, err := f.invoices.ListInvoices(ctx, invoice.ListFilter{Limit: 5, CustomerID: &f.customerID}, true); err != nil {
				readerDone <- err
				return
			}
		}
	}()
	for _, id := range invoices {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if r := f.voidInvoice(id, rev(t, f.getInvoice(id)), "mixed"); r.status != 200 {
				t.Errorf("mixed void = %d: %s", r.status, r.raw)
			}
		}()
		go func() {
			defer wg.Done()
			body := map[string]any{"customer_id": f.customerID.String(), "reason_code": "price_adjustment", "reason": "mixed",
				"lines": []map[string]any{{"line_type": "charge", "charge_code": "ADJUST", "quantity": "-1", "unit_price_ten_thousandths": 1000}}}
			if r := f.do("POST", "/api/v1/credit-memos", body); r.status != 201 {
				t.Errorf("mixed create = %d: %s", r.status, r.raw)
			}
		}()
	}
	wg.Wait()
	close(stop)
	if err := <-readerDone; err != nil {
		t.Errorf("reader: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("the contenders did not finish inside the deadline: a transaction waited on a second pool connection")
	}
}

func toUUIDs(ids []string) []uuid.UUID {
	out := make([]uuid.UUID, len(ids))
	for i, s := range ids {
		out[i] = uuid.MustParse(s)
	}
	return out
}

// gatedTx is a TxRunner that makes the first `want` transactions meet inside
// their transactions before any of them runs a statement.
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

// Saturation: as many contenders as the pool has connections, held inside their
// transactions at a gate, for each kind of write. A statement that reached for a
// second connection from the pool would leave four holders each waiting for a
// fifth that never frees, and the deadline would fire.
func TestSaturationNeedsNoSecondConnection(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const contenders = 4
	// the documents each phase works on, one per contender, prepared on a roomy path
	type doc struct{ invoice, line string }
	var docs []doc
	for i := 0; i < 5*contenders; i++ {
		id, _ := f.invoice("1")
		docs = append(docs, doc{id, f.firstLineID(id)})
	}
	var postable, voidable []string // drafts to post, posted memos to void
	for i := 0; i < contenders; i++ {
		postable = append(postable, str(t, f.createCredit(f.creditBody(docs[i].invoice, returnLine(docs[i].line, "-1", true))).body, "id"))
		m := f.createCredit(f.creditBody(docs[contenders+i].invoice, returnLine(docs[contenders+i].line, "-1", true)))
		id := str(t, m.body, "id")
		if r := f.postCredit(id, 1); r.status != 200 {
			t.Fatalf("prepare a posted memo = %d: %s", r.status, r.raw)
		}
		voidable = append(voidable, id)
	}
	var drafts []string // for update and draft void
	for i := 0; i < 2*contenders; i++ {
		drafts = append(drafts, str(t, f.createCredit(f.creditBody(docs[2*contenders+i%contenders].invoice, returnLine(docs[2*contenders+i%contenders].line, "-0.5", false))).body, "id"))
	}

	logger := slog.Default()
	glSvc := gl.NewService(gl.NewRepository(db), nil, logger)
	acct := account.NewService(db, glSvc, logger)
	stock := inventory.NewService(inventory.NewRepository(db))
	phase := func(name string, run func(svc *invoice.Service, i int) error) {
		t.Helper()
		svc := invoice.NewService(invoice.NewRepository(db), glSvc, acct, db).WithAuditLog(audit.NewLogger(db)).
			WithOutbox(outbox.NewWriter(db, "")).WithStock(stock).WithOrders(f.orders).WithTxRunner(newGatedTx(db, contenders))
		var wg sync.WaitGroup
		errs := make(chan error, contenders)
		for i := 0; i < contenders; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := run(svc, i); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("%s: %v", name, err)
		}
		if ctx.Err() != nil {
			t.Fatalf("%s: four contenders at pool size 4 did not finish: a transaction waited on a second pool connection", name)
		}
	}
	draftFor := func(inv, line string) *invoice.CreditInput {
		req := invoice.CreditRequest{InvoiceID: &inv, ReasonCode: ptr("return"), Reason: ptr("saturation"),
			Lines: []invoice.CreditLineRequest{{InvoiceLineID: &line, Quantity: []byte(`"-0.25"`)}}}
		d, err := req.Parse()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	one := int64(1)

	phase("credit memo create", func(svc *invoice.Service, i int) error {
		d := docs[3*contenders+i]
		_, err := svc.CreateCreditMemo(ctx, draftFor(d.invoice, d.line), "tx")
		return err
	})
	phase("credit memo update", func(svc *invoice.Service, i int) error {
		d := docs[2*contenders+i]
		_, err := svc.UpdateCreditMemo(ctx, uuid.MustParse(drafts[i]), draftFor(d.invoice, d.line), invoice.Precondition{Revision: &one}, "tx")
		return err
	})
	phase("credit memo post", func(svc *invoice.Service, i int) error {
		_, err := svc.TransitionCreditMemo(ctx, uuid.MustParse(postable[i]), invoice.CreditOpen, invoice.Precondition{Revision: &one}, invoice.Transition{Actor: "tx"})
		return err
	})
	phase("credit memo void (posted)", func(svc *invoice.Service, i int) error {
		two := int64(2)
		_, err := svc.TransitionCreditMemo(ctx, uuid.MustParse(voidable[i]), invoice.CreditVoid, invoice.Precondition{Revision: &two}, invoice.Transition{Actor: "tx", Reason: "sat"})
		return err
	})
	phase("credit memo void (draft)", func(svc *invoice.Service, i int) error {
		_, err := svc.TransitionCreditMemo(ctx, uuid.MustParse(drafts[contenders+i]), invoice.CreditVoid, invoice.Precondition{Revision: &one}, invoice.Transition{Actor: "tx", Reason: "sat"})
		return err
	})
	phase("invoice void", func(svc *invoice.Service, i int) error {
		d := docs[4*contenders+i] // no credit memo names these
		_, err := svc.VoidInvoice(ctx, uuid.MustParse(d.invoice), invoice.Precondition{Revision: &one}, invoice.Transition{Actor: "tx", Reason: "sat"})
		return err
	})
}

func ptr[T any](v T) *T { return &v }

// RULE (ADR 0005 6.2 and 11): a payment and a void of the same invoice race on
// the invoice row, and either order leaves the books consistent: the void wins
// (and the payment is refused, the invoice void) or the payment wins (and the
// void is refused with has_applications), never a void invoice with a payment.
func TestPaymentAndVoidRaceLeavesNoVoidInvoiceWithAPayment(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	glSvc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	acct := account.NewService(db, glSvc, slog.Default())
	pay := payment.NewService(db, payment.NewRepository(db), acct)
	voided, paid := 0, 0
	for i := 0; i < 16; i++ {
		invID, _ := f.invoice("1")
		revision := rev(t, f.getInvoice(invID))
		var wg sync.WaitGroup
		var voidStatus int
		var payErr error
		wg.Add(2)
		skew := time.Duration(i%4) * 1500 * time.Microsecond
		go func() {
			defer wg.Done()
			if i%2 == 1 {
				time.Sleep(skew) // the payment starts first on odd rounds
			}
			voidStatus = f.voidInvoice(invID, revision, "race").status
		}()
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				time.Sleep(skew)
			}
			_, payErr = pay.Create(context.Background(), &payment.Input{CustomerID: f.customerID, AmountCents: 100, Method: payment.PaymentMethodCash,
				Reference: "race", Applications: []account.ApplyLine{{InvoiceID: uuid.MustParse(invID), AmountCents: 100}}}, payment.Caller{})
		}()
		wg.Wait()
		status := str(t, f.getInvoice(invID).body, "status")
		payments := countOf(t, db, `SELECT count(*) FROM ar_applications WHERE invoice_id = $1 AND reversed_at IS NULL`, invID)
		switch {
		case status == "void" && payments == 0 && voidStatus == 200 && payErr != nil:
			voided++
		case status != "void" && payments == 1 && voidStatus == http.StatusConflict && payErr == nil:
			paid++
		default:
			t.Fatalf("round %d: invoice %s, %d payments, void answered %d, payment error %v: the books disagree", i, status, payments, voidStatus, payErr)
		}
	}
	t.Logf("16 races: %d voids won, %d payments won", voided, paid)
}

// RULE (ADR 0005 8.1): an entry dated into a closed fiscal period fails the act
// with 409 and the blocker period_closed, for the fulfilment's invoice entry,
// an invoice void's reversal and a credit memo's post. Each runs inside a
// transaction that is rolled back, so the closed period exists for that act only
// and no other test's postings see it.
func TestClosedPeriodRefusesEachPosting(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()
	voidID, _ := f.invoice("2")
	memoInvoice, _ := f.invoice("3")
	memo := f.createCredit(f.creditBody(memoInvoice, returnLine(f.firstLineID(memoInvoice), "-1", false)))
	orderID, orderRev := f.confirmedOrder(f.pickupLine("1"))

	closed := func(name string, act func(ctx context.Context) error) {
		t.Helper()
		errRollback := fmt.Errorf("roll back")
		err := db.RunInTx(ctx, func(ctx context.Context) error {
			// close the period that covers today (or make one): periods may not
			// overlap, and this transaction's closing is rolled back below
			ct, err := db.GetExecutor(ctx).Exec(ctx, `UPDATE gl_fiscal_periods SET status = 'CLOSED' WHERE CURRENT_DATE - 3 <= end_date AND CURRENT_DATE + 3 >= start_date`)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				if _, err := db.GetExecutor(ctx).Exec(ctx, `INSERT INTO gl_fiscal_periods (name, start_date, end_date, status) VALUES ('c23-closed', CURRENT_DATE - 3, CURRENT_DATE + 3, 'CLOSED')`); err != nil {
					return err
				}
			}
			got := act(ctx)
			var he *httpx.Error
			if !errors.As(got, &he) || he.Status != http.StatusConflict || len(he.Details) != 1 || he.Details[0].Code != "period_closed" {
				t.Errorf("%s into a closed period = %v, want 409 period_closed", name, got)
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	closed("an invoice void", func(ctx context.Context) error {
		_, err := f.invoices.VoidInvoice(ctx, uuid.MustParse(voidID), invoice.Precondition{Revision: ptr(rev(t, f.getInvoice(voidID)))}, invoice.Transition{Reason: "closed"})
		return err
	})
	closed("a credit memo post", func(ctx context.Context) error {
		_, err := f.invoices.TransitionCreditMemo(ctx, uuid.MustParse(str(t, memo.body, "id")), invoice.CreditOpen, invoice.Precondition{Revision: ptr(int64(1))}, invoice.Transition{})
		return err
	})
	closed("a fulfilment", func(ctx context.Context) error {
		_, err := f.orders.Fulfil(ctx, uuid.MustParse(orderID), &order.Precondition{Revision: &orderRev}, order.FulfilRequest{PickedUpBy: "X"})
		return err
	})
	// nothing of the refused acts remained
	if o := f.do("GET", "/api/v1/orders/"+orderID, nil); str(t, o.body, "status") != "confirmed" {
		t.Errorf("the order moved: %v", o.body["status"])
	}
	if got := f.getInvoice(voidID); str(t, got.body, "status") != "unpaid" {
		t.Errorf("the invoice moved: %v", got.body["status"])
	}
}
