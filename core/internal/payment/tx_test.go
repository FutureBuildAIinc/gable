// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

// The transaction proofs of the recipe (step 2) for every money act the
// payment, invoice and account services write for ADR 0005 section 9.4:
// create, apply, void, refund, credit memo refund, credit memo application,
// write off and application reversal. Each act is run (1) with an event
// writer that fails, (2) with an audit writer that fails (the payment acts
// take one as an interface), (3) inside an outer transaction that is rolled
// back after the act succeeded, and in each case no payment, application,
// entry, subledger row, balance, audit row or event survives; then (4) three
// contenders at pool size 4 race the act, and (5) as many contenders as the
// pool has connections are held inside their transactions at a gate before
// any statement runs, so a statement that reached for the pool instead of the
// transaction would starve it and the deadline would fire.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// flakyEvents fails every event write while on is set.
type flakyEvents struct {
	inner *outbox.Writer
	on    atomic.Bool
}

func (f *flakyEvents) Write(ctx context.Context, ev outbox.Event) error {
	if f.on.Load() {
		return errors.New("outbox insert failed")
	}
	return f.inner.Write(ctx, ev)
}

// gate makes the first want transactions meet inside their transactions
// before any of them runs a statement.
type gate struct {
	db      *database.DB
	want    int32
	entered int32
	wg      sync.WaitGroup
}

func newGate(db *database.DB, want int) *gate {
	g := &gate{db: db, want: int32(want)}
	g.wg.Add(want)
	return g
}

func (g *gate) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return g.db.RunInTx(ctx, func(txCtx context.Context) error {
		if atomic.AddInt32(&g.entered, 1) <= g.want {
			g.wg.Done()
			g.wg.Wait()
		}
		return fn(txCtx)
	})
}

// services are the three services that write the acts, wired alike.
type services struct {
	pay *payment.Service
	inv *invoice.Service
	acc *account.Service
}

// txWorld owns one customer and the documents a test builds for it.
type txWorld struct {
	t        *testing.T
	db       *database.DB
	customer uuid.UUID
	branch   uuid.UUID
	core     *account.Service // plain: builds fixtures
	glSvc    *gl.Service
	events   *flakyEvents
}

func newTxWorld(t *testing.T, db *database.DB) *txWorld {
	t.Helper()
	w := &txWorld{t: t, db: db, customer: uuid.New(), events: &flakyEvents{inner: outbox.NewWriter(db, "")}}
	ctx := context.Background()
	if err := db.Pool.QueryRow(ctx, cardBranchSelect).Scan(&w.branch); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	w.exec(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Tx Proof Co', $2, $3)`,
		w.customer, "TXP-"+w.customer.String()[:8], w.branch)
	w.glSvc = gl.NewService(gl.NewRepository(db), nil, slog.Default())
	w.core = account.NewService(db, w.glSvc, slog.Default())
	t.Cleanup(w.cleanup)
	return w
}

const cardBranchSelect = `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`

func (w *txWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.db.Pool.Exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
	}
}

func (w *txWorld) cleanup() {
	ctx := context.Background()
	exec := func(sql string) { _, _ = w.db.Pool.Exec(ctx, sql, w.customer) }
	docs := `(SELECT id FROM payments WHERE customer_id = $1 UNION SELECT id FROM invoices WHERE customer_id = $1
		UNION SELECT id FROM credit_memos WHERE customer_id = $1 UNION SELECT id FROM ar_applications WHERE customer_id = $1
		UNION SELECT f.id FROM payment_refunds f WHERE f.payment_id IN (SELECT id FROM payments WHERE customer_id = $1)
		OR f.credit_memo_id IN (SELECT id FROM credit_memos WHERE customer_id = $1))`
	entries := `SELECT e.id FROM gl_journal_entries e WHERE e.source_ref_id IN ` + docs
	exec(`DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT r.id FROM gl_journal_entries r WHERE r.reverses_entry_id IN (` + entries + `))`)
	exec(`DELETE FROM gl_journal_entries WHERE reverses_entry_id IN (` + entries + `)`)
	exec(`DELETE FROM gl_journal_lines WHERE journal_entry_id IN (` + entries + `)`)
	exec(`DELETE FROM gl_journal_entries WHERE id IN (` + entries + `)`)
	exec(`DELETE FROM events_outbox WHERE entity_id IN ` + docs + ` OR entity_id = $1`)
	exec(`DELETE FROM audit_log WHERE entity_id IN ` + docs + ` OR entity_id = $1`)
	exec(`DELETE FROM payment_refunds WHERE payment_id IN (SELECT id FROM payments WHERE customer_id = $1) OR credit_memo_id IN (SELECT id FROM credit_memos WHERE customer_id = $1)`)
	exec(`DELETE FROM ar_applications WHERE customer_id = $1`)
	exec(`DELETE FROM payments WHERE customer_id = $1`)
	exec(`DELETE FROM credit_memo_lines WHERE credit_memo_id IN (SELECT id FROM credit_memos WHERE customer_id = $1)`)
	exec(`DELETE FROM credit_memos WHERE customer_id = $1`)
	exec(`DELETE FROM customer_transactions WHERE customer_id = $1`)
	exec(`DELETE FROM invoices WHERE customer_id = $1`)
	exec(`DELETE FROM customers WHERE id = $1`)
}

// wire builds the services over the world's database. tx replaces the
// transaction runner of all three when given; audit replaces the payment
// service's audit sink when given.
func (w *txWorld) wire(tx interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}, auditSink payment.AuditSink) *services {
	logger := slog.New(slog.NewTextHandler(discard{}, nil))
	acc := account.NewService(w.db, w.glSvc, logger).WithAuditLog(audit.NewLogger(w.db)).WithOutbox(w.events)
	pay := payment.NewService(w.db, payment.NewRepository(w.db), acc).WithOutbox(w.events)
	inv := invoice.NewService(invoice.NewRepository(w.db), w.glSvc, acc, w.db).WithAuditLog(audit.NewLogger(w.db)).WithOutbox(w.events)
	if auditSink != nil {
		pay.WithAuditLog(auditSink)
	} else {
		pay.WithAuditLog(audit.NewLogger(w.db))
	}
	if tx != nil {
		acc.WithTxRunner(tx)
		pay.WithTxRunner(tx)
		inv.WithTxRunner(tx)
	}
	return &services{pay: pay, inv: inv, acc: acc}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Fixtures, built with the plain core outside any act under test.

func (w *txWorld) invoice(total int64) uuid.UUID {
	w.t.Helper()
	id := uuid.New()
	w.exec(`INSERT INTO invoices (id, customer_id, branch_id, status, origin, number, currency, total_amount, subtotal,
			tax_rate, tax_amount, amount_open, due_date, invoice_date)
		VALUES ($1, $2, $3, 'UNPAID', 'POS', $4, 'USD', $5::bigint::numeric / 100, $5::bigint::numeric / 100,
			0, 0, $5::bigint::numeric / 100, CURRENT_DATE + 30, CURRENT_DATE - 5)`, id, w.customer, w.branch, "IN-"+id.String()[:8], total)
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		_, e := w.core.PostInvoice(ctx, account.PostInvoiceIn{InvoiceID: id, CustomerID: w.customer, Number: "IN-" + id.String()[:8],
			Currency: "USD", TotalCents: total, On: time.Now(), Actor: "u-test",
			Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Credit: total}}})
		return e
	})
	if err != nil {
		w.t.Fatalf("post invoice: %v", err)
	}
	return id
}

func (w *txWorld) payment(amount int64, apps ...account.ApplyLine) uuid.UUID {
	w.t.Helper()
	var id uuid.UUID
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		var e error
		id, _, e = w.core.RecordPayment(ctx, account.RecordPaymentIn{CustomerID: w.customer, BranchID: w.branch, Currency: "USD",
			Method: "CASH", AmountCents: amount, ReceivedOn: time.Now(), Actor: "u-test", Applications: apps})
		return e
	})
	if err != nil {
		w.t.Fatalf("record payment: %v", err)
	}
	return id
}

func (w *txWorld) memo(credit int64) uuid.UUID {
	w.t.Helper()
	id := uuid.New()
	w.exec(`INSERT INTO credit_memos (id, customer_id, branch_id, currency, reason_code, reason, amount, subtotal, tax_amount,
			total_amount, status, memo_date)
		VALUES ($1, $2, $3, 'USD', 'RETURN', 'returned goods', 0, 0, 0, 0, 'DRAFT', CURRENT_DATE)`, id, w.customer, w.branch)
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		_, e := w.core.PostCreditMemo(ctx, account.PostCreditMemoIn{MemoID: id, CustomerID: w.customer, Number: "CM-" + id.String()[:8],
			Currency: "USD", MemoDate: time.Now(), SubtotalCents: -credit, TotalCents: -credit, Actor: "u-test",
			Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Debit: credit}}})
		return e
	})
	if err != nil {
		w.t.Fatalf("post credit memo: %v", err)
	}
	return id
}

func (w *txWorld) application(pay, inv uuid.UUID, cents int64) uuid.UUID {
	w.t.Helper()
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		_, e := w.core.Apply(ctx, account.ApplyIn{PaymentID: pay, Lines: []account.ApplyLine{{InvoiceID: inv, AmountCents: cents}}, On: time.Now(), Actor: "u-test"})
		return e
	})
	if err != nil {
		w.t.Fatalf("apply: %v", err)
	}
	var id uuid.UUID
	if err := w.db.Pool.QueryRow(context.Background(), `SELECT id FROM ar_applications WHERE payment_id = $1 AND invoice_id = $2 AND reversed_at IS NULL`, pay, inv).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *txWorld) revision(table string, id uuid.UUID) *int64 {
	w.t.Helper()
	var r int64
	if err := w.db.Pool.QueryRow(context.Background(), `SELECT revision FROM `+table+` WHERE id = $1`, id).Scan(&r); err != nil {
		w.t.Fatal(err)
	}
	return &r
}

// state is everything a rolled back act must leave untouched, as one string.
func (w *txWorld) state() string { return w.stateIn(context.Background()) }

// stateIn reads through the executor of ctx, so a test inside a transaction sees it.
func (w *txWorld) stateIn(ctx context.Context) string {
	w.t.Helper()
	const docs = `(SELECT id FROM payments WHERE customer_id = $1 UNION SELECT id FROM invoices WHERE customer_id = $1
		UNION SELECT id FROM credit_memos WHERE customer_id = $1 UNION SELECT id FROM ar_applications WHERE customer_id = $1
		UNION SELECT f.id FROM payment_refunds f WHERE f.payment_id IN (SELECT id FROM payments WHERE customer_id = $1)
		OR f.credit_memo_id IN (SELECT id FROM credit_memos WHERE customer_id = $1))`
	var s string
	err := w.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT concat_ws('|',
			(SELECT count(*) FROM payments WHERE customer_id = $1),
			(SELECT COALESCE(string_agg(status || ':' || amount_unapplied::text || ':' || revision::text, ',' ORDER BY id), '') FROM payments WHERE customer_id = $1),
			(SELECT COALESCE(string_agg(status || ':' || amount_open::text || ':' || revision::text, ',' ORDER BY id), '') FROM invoices WHERE customer_id = $1),
			(SELECT COALESCE(string_agg(status || ':' || amount_open::text || ':' || revision::text, ',' ORDER BY id), '') FROM credit_memos WHERE customer_id = $1),
			(SELECT count(*) FROM ar_applications WHERE customer_id = $1),
			(SELECT count(*) FROM ar_applications WHERE customer_id = $1 AND reversed_at IS NULL),
			(SELECT count(*) FROM payment_refunds f WHERE f.payment_id IN (SELECT id FROM payments WHERE customer_id = $1)
				OR f.credit_memo_id IN (SELECT id FROM credit_memos WHERE customer_id = $1)),
			(SELECT count(*) FROM customer_transactions WHERE customer_id = $1),
			(SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1),
			(SELECT count(*) FROM gl_journal_entries e WHERE e.source_ref_id IN `+docs+`
				OR e.reverses_entry_id IN (SELECT x.id FROM gl_journal_entries x WHERE x.source_ref_id IN `+docs+`)),
			(SELECT count(*) FROM audit_log WHERE entity_id IN `+docs+`),
			(SELECT count(*) FROM events_outbox WHERE entity_id IN `+docs+` OR entity_id = $1))`, w.customer).Scan(&s)
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

// consistent holds the bounds that must hold whatever the interleaving.
func (w *txWorld) consistent(stage string) {
	w.t.Helper()
	ctx := context.Background()
	var bad int
	if err := w.db.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM payments WHERE customer_id = $1 AND (amount_unapplied < 0 OR amount_unapplied > amount))
		+ (SELECT count(*) FROM invoices WHERE customer_id = $1 AND (amount_open < 0 OR amount_open > total_amount))
		+ (SELECT count(*) FROM credit_memos WHERE customer_id = $1 AND amount_open > 0)
		+ (SELECT CASE WHEN COALESCE((SELECT SUM(amount) FROM customer_transactions WHERE customer_id = $1), 0)
			= (SELECT ROUND(balance_due * 100) FROM customers WHERE id = $1) THEN 0 ELSE 1 END)`, w.customer).Scan(&bad); err != nil {
		w.t.Fatal(err)
	}
	if bad != 0 {
		w.t.Errorf("%s: %d bounds broken (a negative or over-full amount, or the subledger apart from the balance)", stage, bad)
	}
}

// The acts. prep builds fixtures for n independent runs and answers one run
// each; a run executes the act on the services it is handed.

type run func(ctx context.Context, s *services) error

type act struct {
	name string
	// payment acts take an audit sink, so a failing audit writer can be installed
	paymentAct bool
	prep       func(w *txWorld, n int) []run
	// contend builds three runs that race for one thing; winners is how many
	// of them may succeed
	contend func(w *txWorld) []run
	winners int
}

var finance = payment.Caller{Actor: "u-finance", Role: "finance"}

func acts() []act {
	cash := func(w *txWorld, inv uuid.UUID, n int64) account.ApplyLine {
		return account.ApplyLine{InvoiceID: inv, AmountCents: n}
	}
	return []act{
		{name: "create", paymentAct: true, winners: 2,
			prep: func(w *txWorld, n int) []run {
				var out []run
				for i := 0; i < n; i++ {
					inv := w.invoice(10000)
					out = append(out, func(ctx context.Context, s *services) error {
						_, err := s.pay.Create(ctx, &payment.Input{CustomerID: w.customer, AmountCents: 4000, Method: payment.PaymentMethodCash,
							Applications: []account.ApplyLine{cash(w, inv, 4000)}}, finance)
						return err
					})
				}
				return out
			},
			contend: func(w *txWorld) []run {
				inv := w.invoice(10000)
				one := func(ctx context.Context, s *services) error {
					_, err := s.pay.Create(ctx, &payment.Input{CustomerID: w.customer, AmountCents: 4000, Method: payment.PaymentMethodCash,
						Applications: []account.ApplyLine{cash(w, inv, 4000)}}, finance)
					return err
				}
				return []run{one, one, one}
			}},
		{name: "apply", paymentAct: true, winners: 1,
			prep: func(w *txWorld, n int) []run {
				var out []run
				for i := 0; i < n; i++ {
					inv, pay := w.invoice(10000), w.payment(6000)
					rev := w.revision("payments", pay)
					out = append(out, func(ctx context.Context, s *services) error {
						_, err := s.pay.Apply(ctx, pay, []account.ApplyLine{cash(w, inv, 6000)}, payment.Precondition{Revision: rev}, finance)
						return err
					})
				}
				return out
			},
			contend: func(w *txWorld) []run {
				pay := w.payment(10000)
				rev := w.revision("payments", pay)
				var out []run
				for i := 0; i < 3; i++ {
					inv := w.invoice(7000)
					out = append(out, func(ctx context.Context, s *services) error {
						_, err := s.pay.Apply(ctx, pay, []account.ApplyLine{cash(w, inv, 6000)}, payment.Precondition{Revision: rev}, finance)
						return err
					})
				}
				return out
			}},
		{name: "void", paymentAct: true, winners: 1,
			prep: func(w *txWorld, n int) []run {
				var out []run
				for i := 0; i < n; i++ {
					inv := w.invoice(10000)
					pay := w.payment(6000, cash(w, inv, 3000))
					rev := w.revision("payments", pay)
					out = append(out, func(ctx context.Context, s *services) error {
						_, err := s.pay.Void(ctx, pay, payment.Precondition{Revision: rev}, "keyed twice", finance)
						return err
					})
				}
				return out
			},
			contend: func(w *txWorld) []run {
				inv := w.invoice(10000)
				pay := w.payment(6000, cash(w, inv, 3000))
				rev := w.revision("payments", pay)
				one := func(ctx context.Context, s *services) error {
					_, err := s.pay.Void(ctx, pay, payment.Precondition{Revision: rev}, "race", finance)
					return err
				}
				return []run{one, one, one}
			}},
		{name: "refund", paymentAct: true, winners: 1,
			prep: func(w *txWorld, n int) []run {
				var out []run
				for i := 0; i < n; i++ {
					pay := w.payment(5000)
					rev := w.revision("payments", pay)
					out = append(out, func(ctx context.Context, s *services) error {
						_, err := s.pay.Refund(ctx, pay, &payment.RefundInput{AmountCents: 3000, Reason: "change of mind", Method: payment.PaymentMethodCash},
							payment.Precondition{Revision: rev}, finance)
						return err
					})
				}
				return out
			},
			contend: func(w *txWorld) []run {
				pay := w.payment(5000)
				rev := w.revision("payments", pay)
				one := func(ctx context.Context, s *services) error {
					_, err := s.pay.Refund(ctx, pay, &payment.RefundInput{AmountCents: 3000, Reason: "race", Method: payment.PaymentMethodCash},
						payment.Precondition{Revision: rev}, finance)
					return err
				}
				return []run{one, one, one}
			}},
		{name: "credit refund", paymentAct: true, winners: 1,
			prep: func(w *txWorld, n int) []run {
				var out []run
				for i := 0; i < n; i++ {
					memo := w.memo(5000)
					rev := w.revision("credit_memos", memo)
					out = append(out, func(ctx context.Context, s *services) error {
						_, err := s.pay.RefundCredit(ctx, memo, &payment.RefundInput{AmountCents: 3000, Reason: "cash back", Method: payment.PaymentMethodCheck},
							payment.Precondition{Revision: rev}, finance)
						return err
					})
				}
				return out
			},
			contend: func(w *txWorld) []run {
				memo := w.memo(5000)
				rev := w.revision("credit_memos", memo)
				one := func(ctx context.Context, s *services) error {
					_, err := s.pay.RefundCredit(ctx, memo, &payment.RefundInput{AmountCents: 3000, Reason: "race", Method: payment.PaymentMethodCheck},
						payment.Precondition{Revision: rev}, finance)
					return err
				}
				return []run{one, one, one}
			}},
		{name: "credit apply", winners: 1,
			prep: func(w *txWorld, n int) []run {
				var out []run
				for i := 0; i < n; i++ {
					inv, memo := w.invoice(10000), w.memo(5000)
					rev := w.revision("credit_memos", memo)
					out = append(out, func(ctx context.Context, s *services) error {
						_, err := s.inv.ApplyCreditMemo(ctx, memo, []invoice.CreditApplyLine{{InvoiceID: inv, AmountCents: 3000}},
							invoice.Precondition{Revision: rev}, invoice.Transition{Actor: "u-finance", Role: "finance"})
						return err
					})
				}
				return out
			},
			contend: func(w *txWorld) []run {
				memo := w.memo(5000)
				rev := w.revision("credit_memos", memo)
				var out []run
				for i := 0; i < 3; i++ {
					inv := w.invoice(10000)
					out = append(out, func(ctx context.Context, s *services) error {
						_, err := s.inv.ApplyCreditMemo(ctx, memo, []invoice.CreditApplyLine{{InvoiceID: inv, AmountCents: 3000}},
							invoice.Precondition{Revision: rev}, invoice.Transition{Actor: "u-finance", Role: "finance"})
						return err
					})
				}
				return out
			}},
		{name: "write off", winners: 1,
			prep: func(w *txWorld, n int) []run {
				var out []run
				for i := 0; i < n; i++ {
					inv := w.invoice(9000)
					rev := w.revision("invoices", inv)
					out = append(out, func(ctx context.Context, s *services) error {
						_, err := s.inv.WriteOff(ctx, inv, 4000, "uncollectable", invoice.Precondition{Revision: rev}, invoice.Transition{Actor: "u-finance", Role: "finance"})
						return err
					})
				}
				return out
			},
			contend: func(w *txWorld) []run {
				inv := w.invoice(9000)
				rev := w.revision("invoices", inv)
				one := func(ctx context.Context, s *services) error {
					_, err := s.inv.WriteOff(ctx, inv, 5000, "race", invoice.Precondition{Revision: rev}, invoice.Transition{Actor: "u-finance", Role: "finance"})
					return err
				}
				return []run{one, one, one}
			}},
		{name: "reverse", winners: 1,
			prep: func(w *txWorld, n int) []run {
				var out []run
				for i := 0; i < n; i++ {
					inv := w.invoice(10000)
					pay := w.payment(6000)
					app := w.application(pay, inv, 6000)
					out = append(out, func(ctx context.Context, s *services) error {
						_, err := s.acc.ReverseApplication(ctx, app, account.ReverseRequest{Reason: "wrong invoice", Actor: "u-finance", Role: "finance"})
						return err
					})
				}
				return out
			},
			contend: func(w *txWorld) []run {
				inv := w.invoice(10000)
				pay := w.payment(6000)
				app := w.application(pay, inv, 6000)
				one := func(ctx context.Context, s *services) error {
					_, err := s.acc.ReverseApplication(ctx, app, account.ReverseRequest{Reason: "race", Actor: "u-finance", Role: "finance"})
					return err
				}
				return []run{one, one, one}
			}},
	}
}

// RULE (ADR 0003 section 3, ADR 0005 14.2): a failing event write, a failing
// audit write and a failed outer transaction each roll the whole act back.
func TestEveryMoneyActRollsBackWhole(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	for _, a := range acts() {
		t.Run(a.name, func(t *testing.T) {
			w := newTxWorld(t, db)
			runs := a.prep(w, 3)
			s := w.wire(nil, nil)
			before := w.state()

			// 1. a failing event write
			w.events.on.Store(true)
			if err := runs[0](context.Background(), s); err == nil {
				t.Fatal("the act succeeded with a failing event writer")
			}
			w.events.on.Store(false)
			if after := w.state(); after != before {
				t.Errorf("a failing event write left the act half done:\n before %s\n after  %s", before, after)
			}

			// 2. a failing audit write (the payment acts take the sink; the other
			// services write through the audit table inside the transaction, which
			// the outer rollback below proves)
			if a.paymentAct {
				bad := w.wire(nil, failingAudit{})
				if err := runs[0](context.Background(), bad); err == nil {
					t.Fatal("the act succeeded with a failing audit writer")
				}
				if after := w.state(); after != before {
					t.Errorf("a failing audit write left the act half done:\n before %s\n after  %s", before, after)
				}
			}

			// 3. an outer transaction that fails after the act succeeded: the row,
			// the entry, the subledger row, the audit row and the events all go
			errOuter := errors.New("deliberate rollback")
			err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
				if err := runs[1](ctx, s); err != nil {
					return err
				}
				if inside := w.stateIn(ctx); inside == before {
					t.Error("the act wrote nothing inside its transaction: the rollback proves nothing")
				}
				return errOuter
			})
			if !errors.Is(err, errOuter) {
				t.Fatalf("the act inside the outer transaction = %v, want the deliberate rollback", err)
			}
			if after := w.state(); after != before {
				t.Errorf("an outer rollback left the act half done:\n before %s\n after  %s", before, after)
			}

			// the control: the same act succeeds when nothing fails, so the refusals
			// above were the faults, not the fixture
			if err := runs[2](context.Background(), s); err != nil {
				t.Fatalf("the control act: %v", err)
			}
			if w.state() == before {
				t.Error("the control act changed nothing")
			}
			w.consistent("after the control act")
		})
	}
}

// RULE (BRIEF-COMMON, transactions): three contenders at pool size 4 race each
// act; the winners are exactly as many as the document allows, the losers are
// refused, nothing deadlocks, and the amounts stay inside their bounds.
func TestEveryMoneyActWithThreeContendersAtPool4(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	for _, a := range acts() {
		t.Run(a.name, func(t *testing.T) {
			w := newTxWorld(t, db)
			s := w.wire(nil, nil)
			runs := a.contend(w)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			errs := make([]error, len(runs))
			var wg sync.WaitGroup
			for i := range runs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs[i] = runs[i](ctx, s)
				}()
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-ctx.Done():
			}
			if ctx.Err() != nil {
				t.Fatal("the contenders did not finish inside the deadline: a transaction waited on a second pool connection")
			}
			won := 0
			for _, err := range errs {
				if err == nil {
					won++
				}
			}
			if won != a.winners {
				t.Errorf("%d winners, want %d: %v", won, a.winners, errs)
			}
			w.consistent("after the race")
		})
	}
}

// RULE (BRIEF-COMMON, transactions): as many contenders as the pool has
// connections are held inside their transactions at a gate before any
// statement runs. A statement that reached for the pool instead of the
// transaction would leave four holders each waiting for a fifth connection
// that never frees, and the deadline would fire.
func TestEveryMoneyActSaturatesWithoutASecondConnection(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	const contenders = 4
	for _, a := range acts() {
		t.Run(a.name, func(t *testing.T) {
			w := newTxWorld(t, db)
			runs := a.prep(w, contenders) // fixtures on the roomy path, before the gate
			s := w.wire(newGate(db, contenders), nil)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			errs := make([]error, contenders)
			var wg sync.WaitGroup
			for i := range runs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs[i] = runs[i](ctx, s)
				}()
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-ctx.Done():
			}
			if ctx.Err() != nil {
				t.Fatal("four contenders at pool size 4 did not finish: a transaction waited on a second pool connection")
			}
			for i, err := range errs {
				if err != nil {
					t.Errorf("contender %d: %v", i, err)
				}
			}
			w.consistent("after the saturation")
		})
	}
}
