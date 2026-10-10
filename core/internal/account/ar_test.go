// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// The AR core's acts and reads against a real Postgres (ADR 0005 sections 9
// and 10): every test drives the core through its own transactions and sums
// the ledger after every act, because the invariants of 9.3 (every entry
// balances; the subledger equals the 1020 control account; unapplied cash
// equals the 2200 deposits account) are the thing the core exists to keep.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

const arBranch = `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`

// arWorld owns one throwaway customer (more on demand) and everything the AR
// core writes for them.
type arWorld struct {
	t          *testing.T
	db         *database.DB
	svc        *account.Service
	customerID uuid.UUID
	branchID   uuid.UUID
	entries    []uuid.UUID // every entry the core posted for the world's own customer
	side       []uuid.UUID // extra customers a test made (cleaned up, outside the invariants)
}

func newARWorld(t *testing.T) *arWorld {
	t.Helper()
	return newARWorldOn(t, testutil.RequireDB(t))
}

func newARWorldOn(t *testing.T, db *database.DB) *arWorld {
	t.Helper()
	w := &arWorld{t: t, db: db, customerID: uuid.New()}
	w.svc = account.NewService(db, gl.NewService(gl.NewRepository(db), nil, slog.Default()), slog.Default())
	ctx := context.Background()
	if err := db.Pool.QueryRow(ctx, `SELECT `+arBranch).Scan(&w.branchID); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	w.exec(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'AR Core Co', $2, `+arBranch+`)`,
		w.customerID, "ARC-"+uuid.NewString()[:8])
	t.Cleanup(w.cleanup)
	return w
}

func (w *arWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.db.Pool.Exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
	}
}

func (w *arWorld) queryInt(sql string, args ...any) int64 {
	w.t.Helper()
	var n int64
	if err := w.db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		w.t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
	}
	return n
}

func (w *arWorld) cleanup() {
	ctx := context.Background()
	exec := func(sql string, args ...any) { _, _ = w.db.Pool.Exec(ctx, sql, args...) }
	// Every customer the world created; other packages' tests run in parallel
	// against the same database with their own.
	for _, c := range append([]uuid.UUID{w.customerID}, w.side...) {
		entries := `SELECT e.id FROM gl_journal_entries e WHERE e.source_ref_id IN
			(SELECT id FROM invoices WHERE customer_id = $1)
			OR e.source_ref_id IN (SELECT id FROM credit_memos WHERE customer_id = $1)
			OR e.source_ref_id IN (SELECT id FROM payments WHERE customer_id = $1)
			OR e.source_ref_id IN (SELECT id FROM ar_applications WHERE customer_id = $1)
			OR e.source_ref_id IN (SELECT id FROM payment_refunds WHERE payment_id IN (SELECT id FROM payments WHERE customer_id = $1)
				OR credit_memo_id IN (SELECT id FROM credit_memos WHERE customer_id = $1))`
		exec(`DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT r.id FROM gl_journal_entries r WHERE r.reverses_entry_id IN (`+entries+`))`, c)
		exec(`DELETE FROM gl_journal_entries WHERE reverses_entry_id IN (`+entries+`)`, c)
		exec(`DELETE FROM gl_journal_lines WHERE journal_entry_id IN (`+entries+`)`, c)
		exec(`DELETE FROM gl_journal_entries WHERE id IN (`+entries+`)`, c)
		exec(`DELETE FROM payment_refunds WHERE payment_id IN (SELECT id FROM payments WHERE customer_id = $1) OR credit_memo_id IN (SELECT id FROM credit_memos WHERE customer_id = $1)`, c)
		exec(`DELETE FROM ar_applications WHERE customer_id = $1`, c)
		exec(`DELETE FROM payments WHERE customer_id = $1`, c)
		exec(`DELETE FROM credit_memo_lines WHERE credit_memo_id IN (SELECT id FROM credit_memos WHERE customer_id = $1)`, c)
		exec(`DELETE FROM credit_memos WHERE customer_id = $1`, c)
		exec(`DELETE FROM customer_transactions WHERE customer_id = $1`, c)
		exec(`DELETE FROM invoice_lines WHERE invoice_id IN (SELECT id FROM invoices WHERE customer_id = $1)`, c)
		exec(`DELETE FROM invoices WHERE customer_id = $1`, c)
		exec(`DELETE FROM customer_ship_tos WHERE customer_id = $1`, c)
		exec(`DELETE FROM projects WHERE customer_id = $1`, c)
		exec(`DELETE FROM customers WHERE id = $1`, c)
	}
}

// ---------------------------------------------------------------------------
// The invariants of 9.3, asserted after every act.
// ---------------------------------------------------------------------------

// assertInvariants sums the world's own ledger: every entry the core posted
// balances; the subledger equals the customer balance and the 1020 control
// account; the unapplied cash of the world's payments equals the 2200
// deposits account.
func (w *arWorld) assertInvariants(stage string) {
	w.t.Helper()
	ctx := context.Background()
	c := w.customerID

	var unbalanced int64
	if err := w.db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT l.journal_entry_id FROM gl_journal_lines l WHERE l.journal_entry_id = ANY($1)
			GROUP BY l.journal_entry_id HAVING SUM(l.debit) <> SUM(l.credit)) x`, w.entries).Scan(&unbalanced); err != nil {
		w.t.Fatal(err)
	}
	if unbalanced != 0 {
		w.t.Errorf("%s: %d entries do not balance", stage, unbalanced)
	}

	rows, err := w.db.Pool.Query(ctx, `
		SELECT t.currency, SUM(t.amount) FROM customer_transactions t WHERE t.customer_id = $1 GROUP BY t.currency`, c)
	if err != nil {
		w.t.Fatal(err)
	}
	sub := map[string]int64{}
	for rows.Next() {
		var cur string
		var n int64
		if err := rows.Scan(&cur, &n); err != nil {
			rows.Close()
			w.t.Fatal(err)
		}
		sub[cur] = n
	}
	rows.Close()

	var balance int64
	if err := w.db.Pool.QueryRow(ctx, `SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1`, c).Scan(&balance); err != nil {
		w.t.Fatal(err)
	}
	var subTotal int64
	for _, n := range sub {
		subTotal += n
	}
	if balance != subTotal {
		w.t.Errorf("%s: balance_due = %d, subledger sum = %d", stage, balance, subTotal)
	}

	accountNet := func(code string, debitMinusCredit bool) map[string]int64 {
		rows, err := w.db.Pool.Query(ctx, `
			SELECT e.currency, ROUND(SUM(CASE WHEN $3 THEN l.debit - l.credit ELSE l.credit - l.debit END) * 100)::bigint
			FROM gl_journal_lines l
			JOIN gl_journal_entries e ON e.id = l.journal_entry_id
			JOIN gl_accounts a ON a.id = l.account_id AND a.code = $2
			WHERE e.id = ANY($1) GROUP BY e.currency`, w.entries, code, debitMinusCredit)
		if err != nil {
			w.t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]int64{}
		for rows.Next() {
			var cur string
			var n int64
			if err := rows.Scan(&cur, &n); err != nil {
				w.t.Fatal(err)
			}
			out[cur] = n
		}
		return out
	}

	ar := accountNet("1020", true)
	for cur, n := range sub {
		if ar[cur] != n {
			w.t.Errorf("%s: subledger %s = %d, 1020 control = %d", stage, cur, n, ar[cur])
		}
	}

	dep := accountNet("2200", false)
	rows, err = w.db.Pool.Query(ctx, `
		SELECT currency, SUM(ROUND(amount_unapplied * 100)::bigint) FROM payments WHERE customer_id = $1 AND status = 'POSTED' GROUP BY currency`, c)
	if err != nil {
		w.t.Fatal(err)
	}
	un := map[string]int64{}
	for rows.Next() {
		var cur string
		var n int64
		if err := rows.Scan(&cur, &n); err != nil {
			rows.Close()
			w.t.Fatal(err)
		}
		un[cur] = n
	}
	rows.Close()
	for cur, n := range dep {
		if un[cur] != n {
			w.t.Errorf("%s: unapplied %s = %d, 2200 deposits = %d", stage, cur, un[cur], n)
		}
	}
	for cur, n := range un {
		if dep[cur] != n {
			w.t.Errorf("%s: unapplied %s = %d, 2200 deposits = %d", stage, cur, n, dep[cur])
		}
	}
}

func (w *arWorld) keep(fx *account.Effects) {
	if fx != nil {
		w.entries = append(w.entries, fx.EntryIDs...)
	}
	w.assertInvariants("after act")
}

// netOf answers the world's net of one account code, optionally up to a date.
func (w *arWorld) netOf(code string, debitMinusCredit bool, upTo *time.Time) int64 {
	w.t.Helper()
	cutoff := "9999-12-31"
	if upTo != nil {
		cutoff = date(*upTo)
	}
	return w.queryInt(`
		SELECT COALESCE(ROUND(SUM(CASE WHEN $3 THEN l.debit - l.credit ELSE l.credit - l.debit END) * 100)::bigint, 0)
		FROM gl_journal_lines l
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		JOIN gl_accounts a ON a.id = l.account_id AND a.code = $2
		WHERE e.id = ANY($1) AND e.entry_date <= $4::date`, w.entries, code, debitMinusCredit, cutoff)
}

// ---------------------------------------------------------------------------
// The world's documents.
// ---------------------------------------------------------------------------

func daysFromNow(n int) time.Time {
	return time.Now().UTC().AddDate(0, 0, n).Truncate(24 * time.Hour)
}

func date(t time.Time) string { return t.Format("2006-01-02") }

type arInvoice struct {
	ID         uuid.UUID
	Total      int64
	Due        time.Time
	ProjectID  *uuid.UUID
	ShipToID   *uuid.UUID
	DiscountPP *int64 // ten thousandths of a percent
	DiscountTo *time.Time
}

// invoice inserts an unpaid invoice row and posts it through the core (the
// revenue leg balances the receivable the core adds).
func (w *arWorld) invoice(total int64, on time.Time, opts ...func(*arInvoice)) *arInvoice {
	w.t.Helper()
	inv := &arInvoice{ID: uuid.New(), Total: total, Due: on.AddDate(0, 0, 30)}
	for _, o := range opts {
		o(inv)
	}
	w.postInvoiceRow(w.customerID, inv, on)
	var fx *account.Effects
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		var e error
		fx, e = w.svc.PostInvoice(ctx, account.PostInvoiceIn{InvoiceID: inv.ID, CustomerID: w.customerID,
			Number: "IN-" + inv.ID.String()[:8], Currency: "USD", TotalCents: inv.Total, On: on, Actor: "u-test",
			Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Credit: inv.Total}}})
		return e
	})
	if err != nil {
		w.t.Fatalf("post invoice: %v", err)
	}
	w.keep(fx)
	return inv
}

// invoiceOn posts an invoice of another customer the world keeps (its money
// stays out of the world's own invariants).
func (w *arWorld) invoiceOn(customer uuid.UUID, total int64, on time.Time) uuid.UUID {
	w.t.Helper()
	inv := &arInvoice{ID: uuid.New(), Total: total, Due: on.AddDate(0, 0, 30)}
	w.postInvoiceRow(customer, inv, on)
	var fx *account.Effects
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		var e error
		fx, e = w.svc.PostInvoice(ctx, account.PostInvoiceIn{InvoiceID: inv.ID, CustomerID: customer, Number: "IN-" + inv.ID.String()[:8],
			Currency: "USD", TotalCents: total, On: on, Actor: "u-test",
			Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Credit: total}}})
		return e
	})
	if err != nil {
		w.t.Fatalf("post invoice on: %v", err)
	}
	// Not kept in w.entries: the row belongs to another customer, and the
	// world's invariants sum its own customer only.
	w.side = append(w.side, customer)
	_ = fx
	return inv.ID
}

func (w *arWorld) postInvoiceRow(customer uuid.UUID, inv *arInvoice, on time.Time) {
	var project, shipTo, discountDue, discountPP any
	if inv.ProjectID != nil {
		project = *inv.ProjectID
	}
	if inv.ShipToID != nil {
		shipTo = *inv.ShipToID
	}
	if inv.DiscountTo != nil {
		discountDue = date(*inv.DiscountTo)
	}
	if inv.DiscountPP != nil {
		discountPP = float64(*inv.DiscountPP) / 10000
	}
	w.exec(`INSERT INTO invoices (id, customer_id, branch_id, status, origin, number, currency, total_amount, subtotal,
			tax_rate, tax_amount, amount_open, due_date, discount_due_date, discount_percent, invoice_date, project_id, ship_to_id)
		VALUES ($1, $2, `+arBranch+`, 'UNPAID', 'POS', $3, 'USD', $4::bigint::numeric / 100, $4::bigint::numeric / 100,
			0, 0, $4::bigint::numeric / 100, $5::date, $6::date, $7::numeric, $8::date, $9, $10)`,
		inv.ID, customer, "IN-"+inv.ID.String()[:8], inv.Total, date(inv.Due), discountDue, discountPP, date(on), project, shipTo)
}

func onProject(p uuid.UUID) func(*arInvoice) {
	return func(i *arInvoice) { i.ProjectID = &p }
}
func onShipTo(s uuid.UUID) func(*arInvoice) {
	return func(i *arInvoice) { i.ShipToID = &s }
}
func withDiscount(tt int64, due time.Time) func(*arInvoice) {
	return func(i *arInvoice) { i.DiscountPP, i.DiscountTo = &tt, &due }
}

// payment records cash through the core; applications settle in the same act
// when given.
func (w *arWorld) payment(amount int64, on time.Time, method string, apps ...account.ApplyLine) uuid.UUID {
	w.t.Helper()
	id := uuid.New()
	var fx *account.Effects
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		var e error
		_, fx, e = w.svc.RecordPayment(ctx, account.RecordPaymentIn{PaymentID: id, CustomerID: w.customerID,
			BranchID: w.branchID, Currency: "USD", Method: method, AmountCents: amount, ReceivedOn: on, Actor: "u-test",
			Applications: apps})
		return e
	})
	if err != nil {
		w.t.Fatalf("record payment: %v", err)
	}
	w.keep(fx)
	return id
}

// act runs one core act in its own transaction, keeps its effects and asserts
// the invariants; a refused act answers its error with nothing kept.
func (w *arWorld) act(name string, fn func(ctx context.Context) (*account.Effects, error)) (*account.Effects, error) {
	w.t.Helper()
	var fx *account.Effects
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		var e error
		fx, e = fn(ctx)
		return e
	})
	if err != nil {
		return nil, err
	}
	w.keep(fx)
	return fx, nil
}

func (w *arWorld) mustAct(name string, fn func(ctx context.Context) (*account.Effects, error)) *account.Effects {
	w.t.Helper()
	fx, err := w.act(name, fn)
	if err != nil {
		w.t.Fatalf("%s: %v", name, err)
	}
	return fx
}

func (w *arWorld) paymentRow(id uuid.UUID) (status, method string, amount, unapplied int64) {
	w.t.Helper()
	if err := w.db.Pool.QueryRow(context.Background(),
		`SELECT status, method, ROUND(amount * 100)::bigint, ROUND(amount_unapplied * 100)::bigint FROM payments WHERE id = $1`, id).
		Scan(&status, &method, &amount, &unapplied); err != nil {
		w.t.Fatal(err)
	}
	return
}

func (w *arWorld) invoiceRow(id uuid.UUID) (status string, open int64) {
	w.t.Helper()
	if err := w.db.Pool.QueryRow(context.Background(),
		`SELECT status, ROUND(amount_open * 100)::bigint FROM invoices WHERE id = $1`, id).Scan(&status, &open); err != nil {
		w.t.Fatal(err)
	}
	return
}

func (w *arWorld) balance() int64 {
	return w.queryInt(`SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1`, w.customerID)
}

// appID answers the id of the invoice's first live application of a kind.
func (w *arWorld) appID(invoiceID uuid.UUID, kind string) uuid.UUID {
	w.t.Helper()
	var id uuid.UUID
	if err := w.db.Pool.QueryRow(context.Background(),
		`SELECT id FROM ar_applications WHERE invoice_id = $1 AND kind = $2 AND reversed_at IS NULL ORDER BY created_at, id LIMIT 1`,
		invoiceID, kind).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	return id
}

func entryOf(w *arWorld, invoiceID uuid.UUID) *uuid.UUID {
	var id uuid.UUID
	if err := w.db.Pool.QueryRow(context.Background(),
		`SELECT id FROM gl_journal_entries WHERE source_ref_id = $1 AND source = 'INVOICE' AND reverses_entry_id IS NULL LIMIT 1`, invoiceID).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	return &id
}

// ---------------------------------------------------------------------------
// The acts.
// ---------------------------------------------------------------------------

// EXIT LINE (ADR 0005 14.2, C2-4): an unapplied payment exists without an
// invoice and applies later: a payment with no applications posts 1010 / 2200,
// shows unapplied cash; applied later across two invoices partly, each
// application its own 2200 / 1020 entry and subledger row, the invoices
// partial then paid.
func TestARUnappliedPaymentExistsAndAppliesLater(t *testing.T) {
	w := newARWorld(t)
	i1 := w.invoice(6000, daysFromNow(-40))
	i2 := w.invoice(4000, daysFromNow(-10))

	pay := w.payment(10000, daysFromNow(-2), "CHECK")
	if status, _, _, unapplied := w.paymentRow(pay); status != "POSTED" || unapplied != 10000 {
		t.Fatalf("payment = %s unapplied %d, want POSTED 10000", status, unapplied)
	}
	if got := w.netOf("1010", true, nil); got != 10000 {
		t.Errorf("cash 1010 net = %d, want 10000", got)
	}
	if got := w.netOf("2200", false, nil); got != 10000 {
		t.Errorf("deposits 2200 net = %d, want 10000", got)
	}
	if got := w.netOf("1020", true, nil); got != 10000 {
		t.Errorf("AR 1020 net = %d, want 10000 (the receipt credits no receivable)", got)
	}
	if got := w.balance(); got != 10000 {
		t.Errorf("balance = %d, want 10000 (the receipt writes no subledger row)", got)
	}

	// Later, partly: invoice 1 goes partial.
	w.mustAct("apply 2000 to invoice 1", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay, Lines: []account.ApplyLine{{InvoiceID: i1.ID, AmountCents: 2000}},
			On: daysFromNow(-1), Actor: "u-test"})
	})
	if status, open := w.invoiceRow(i1.ID); status != "PARTIAL" || open != 4000 {
		t.Errorf("invoice 1 = %s open %d, want PARTIAL 4000", status, open)
	}
	if _, _, _, unapplied := w.paymentRow(pay); unapplied != 8000 {
		t.Errorf("unapplied = %d, want 8000", unapplied)
	}

	// Closing both: invoice 1 the rest (4000), invoice 2 all of it (4000).
	fx := w.mustAct("close both", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay, Lines: []account.ApplyLine{
			{InvoiceID: i1.ID, AmountCents: 4000}, {InvoiceID: i2.ID, AmountCents: 4000}}, On: daysFromNow(0), Actor: "u-test"})
	})
	if status, open := w.invoiceRow(i1.ID); status != "PAID" || open != 0 {
		t.Errorf("invoice 1 = %s open %d, want PAID 0", status, open)
	}
	if status, open := w.invoiceRow(i2.ID); status != "PAID" || open != 0 {
		t.Errorf("invoice 2 = %s open %d, want PAID 0", status, open)
	}
	if _, _, _, unapplied := w.paymentRow(pay); unapplied != 0 {
		t.Errorf("unapplied = %d, want 0", unapplied)
	}
	if len(fx.EntryIDs) != 2 {
		t.Errorf("the two lines posted %d entries, want one each", len(fx.EntryIDs))
	}
	if got := w.netOf("2200", false, nil); got != 0 {
		t.Errorf("deposits 2200 net = %d, want 0 (all applied)", got)
	}
	if got := w.netOf("1020", true, nil); got != 0 {
		t.Errorf("AR 1020 net = %d, want 0 (all settled)", got)
	}
	if got := w.balance(); got != 0 {
		t.Errorf("balance = %d, want 0", got)
	}

	// The events of the act, in the order section 9.4 gives.
	types := eventTypesOf(fx)
	want := "[payment.applied invoice.paid invoice.paid customer.updated]"
	if types != want {
		t.Errorf("events = %s, want %s", types, want)
	}
}

func eventTypesOf(fx *account.Effects) string {
	var out []string
	for _, ev := range fx.Events() {
		out = append(out, ev.Type)
	}
	return fmt.Sprint(out)
}

// EXIT LINE: a payment applied to two invoices at receipt has one application
// reversed alone, the other and the cash entry untouched.
func TestARApplicationReversedAlone(t *testing.T) {
	w := newARWorld(t)
	i1 := w.invoice(6000, daysFromNow(-40))
	i2 := w.invoice(4000, daysFromNow(-40))

	pay := w.payment(10000, daysFromNow(-5), "CASH",
		account.ApplyLine{InvoiceID: i1.ID, AmountCents: 6000},
		account.ApplyLine{InvoiceID: i2.ID, AmountCents: 4000})
	if status, _ := w.invoiceRow(i1.ID); status != "PAID" {
		t.Fatalf("invoice 1 = %s, want PAID", status)
	}
	if got := w.netOf("2200", false, nil); got != 0 {
		t.Fatalf("deposits net = %d, want 0", got)
	}

	// Reverse invoice 1's application alone.
	app1 := w.appID(i1.ID, "PAYMENT")
	w.mustAct("reverse application 1", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Reverse(ctx, account.ReverseIn{ApplicationID: app1, Reason: "wrong invoice", Actor: "u-finance", On: daysFromNow(0)})
	})
	if status, open := w.invoiceRow(i1.ID); status != "UNPAID" || open != 6000 {
		t.Errorf("invoice 1 = %s open %d, want UNPAID 6000", status, open)
	}
	if status, _ := w.invoiceRow(i2.ID); status != "PAID" {
		t.Errorf("invoice 2 = %s, want PAID (untouched)", status)
	}
	if _, _, _, unapplied := w.paymentRow(pay); unapplied != 6000 {
		t.Errorf("unapplied = %d, want 6000 back", unapplied)
	}
	if got := w.netOf("2200", false, nil); got != 6000 {
		t.Errorf("deposits net = %d, want 6000", got)
	}
	if got := w.netOf("1010", true, nil); got != 10000 {
		t.Errorf("cash entry untouched: 1010 net = %d, want 10000", got)
	}

	// Reversing it again is refused.
	_, err := w.act("reverse twice", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Reverse(ctx, account.ReverseIn{ApplicationID: app1, Reason: "again", Actor: "u-finance", On: daysFromNow(0)})
	})
	if err == nil || !strings.Contains(err.Error(), "already reversed") {
		t.Errorf("reversing twice = %v, want the already reversed refusal", err)
	}
	w.assertInvariants("after the refused reversal")
}

// A void invoice that still carries a live application (a state a migration
// once left) is not reopened by reversing it: the refusal is a 409, not a
// failed update of the void columns.
func TestARReversalNeverReopensAVoidInvoice(t *testing.T) {
	w := newARWorld(t)
	inv := w.invoice(8000, daysFromNow(-20))
	w.payment(3000, daysFromNow(-10), "CASH", account.ApplyLine{InvoiceID: inv.ID, AmountCents: 3000})
	app := w.appID(inv.ID, "PAYMENT")
	w.exec(`UPDATE invoices SET status = 'VOID', voided_at = NOW(), voided_on = CURRENT_DATE, amount_open = 0 WHERE id = $1`, inv.ID)

	_, err := w.act("reverse onto a void invoice", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Reverse(ctx, account.ReverseIn{ApplicationID: app, Reason: "wrong", Actor: "u-finance", On: daysFromNow(0)})
	})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 409 || len(he.Details) == 0 || he.Details[0].Code != "invoice_void" {
		t.Fatalf("reversal onto a void invoice = %v, want a 409 with the invoice_void blocker", err)
	}
	if n := w.queryInt(`SELECT count(*) FROM ar_applications WHERE id = $1 AND reversed_at IS NULL`, app); n != 1 {
		t.Error("the refused reversal changed the application")
	}
}

// EXIT LINE: AR aging splits by job and ship-to, unapplied cash on its job's
// row, and ties to the 1020 and 2200 balances.
func TestARAgingByJobAndShipTo(t *testing.T) {
	w := newARWorld(t)
	job1, job2 := uuid.New(), uuid.New()
	w.exec(`INSERT INTO projects (id, customer_id, name) VALUES ($1, $2, 'Deck rebuild'), ($3, $2, 'Fence repair')`, job1, w.customerID, job2)
	ship1, ship2 := uuid.New(), uuid.New()
	w.exec(`INSERT INTO customer_ship_tos (id, customer_id, code, name, line1) VALUES ($1, $2, 'YARD', 'Main yard', '1 Site Rd'), ($3, $2, 'SITE2', 'Back site', '2 Site Rd')`,
		ship1, w.customerID, ship2)

	// Current (due ahead), 1-30 late on ship 1 with no job, 31-60 late on job 2.
	w.invoice(5000, daysFromNow(-5), onProject(job1), onShipTo(ship1))            // due +25: current
	w.invoice(3000, daysFromNow(-35), onShipTo(ship1))                           // due -5: 1-30
	w.invoice(7000, daysFromNow(-70), onProject(job2), onShipTo(ship2))          // due -40: 31-60
	// Unapplied cash on job 1.
	pay := uuid.New()
	var fx *account.Effects
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		var e error
		_, fx, e = w.svc.RecordPayment(ctx, account.RecordPaymentIn{PaymentID: pay, CustomerID: w.customerID,
			BranchID: w.branchID, Currency: "USD", Method: "CHECK", AmountCents: 2000, ReceivedOn: daysFromNow(-1),
			Actor: "u-test", ProjectID: &job1})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	w.keep(fx)

	aging := func(groupBy string) map[string]account.AgingItem {
		t.Helper()
		items, err := w.svc.Aging(context.Background(), account.AgingQuery{GroupBy: groupBy, AsOf: daysFromNow(0), Basis: "due_date", CustomerID: &w.customerID})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]account.AgingItem{}
		for _, it := range items {
			key := "customer"
			if it.JobID != nil {
				key = "job:" + it.JobID.String()
			}
			if it.ShipToID != nil {
				key = "ship:" + it.ShipToID.String()
			}
			out[key] = it
		}
		return out
	}

	byCustomer := aging("customer")
	one, ok := byCustomer["customer"]
	if !ok {
		t.Fatalf("aging by customer = %v", byCustomer)
	}
	if one.CurrentCents != 5000 || one.Days1To30 != 3000 || one.Days31To60 != 7000 || one.UnappliedCents != -2000 || one.TotalCents != 13000 {
		t.Errorf("customer row = current %d, 1-30 %d, 31-60 %d, unapplied %d, total %d", one.CurrentCents, one.Days1To30, one.Days31To60, one.UnappliedCents, one.TotalCents)
	}
	// The tie: the five buckets are the 1020 control, the unapplied column is
	// the 2200 deposits (held negative).
	if buckets := one.CurrentCents + one.Days1To30 + one.Days31To60 + one.Days61To90 + one.Over90; int64(buckets) != w.netOf("1020", true, nil) {
		t.Errorf("buckets %d != 1020 net %d", buckets, w.netOf("1020", true, nil))
	}
	if int64(-one.UnappliedCents) != w.netOf("2200", false, nil) {
		t.Errorf("unapplied %d != 2200 net %d", -one.UnappliedCents, w.netOf("2200", false, nil))
	}

	byJob := aging("job")
	j1, ok := byJob["job:"+job1.String()]
	if !ok {
		t.Fatalf("aging by job = %v", byJob)
	}
	if j1.CurrentCents != 5000 || j1.UnappliedCents != -2000 || j1.TotalCents != 3000 {
		t.Errorf("job 1 row = %+v", j1)
	}
	if j1.JobName == nil || *j1.JobName != "Deck rebuild" {
		t.Errorf("job 1 name = %v", j1.JobName)
	}
	if j2 := byJob["job:"+job2.String()]; j2.Days31To60 != 7000 || j2.TotalCents != 7000 {
		t.Errorf("job 2 row = %+v", j2)
	}
	if none := byJob["customer"]; none.Days1To30 != 3000 || none.TotalCents != 3000 {
		t.Errorf("no-job row = %+v", none)
	}

	byShip := aging("ship_to")
	if s1 := byShip["ship:"+ship1.String()]; s1.CurrentCents != 5000 || s1.Days1To30 != 3000 || s1.TotalCents != 8000 {
		t.Errorf("ship 1 row = %+v", s1)
	}
	if s2 := byShip["ship:"+ship2.String()]; s2.Days31To60 != 7000 || s2.TotalCents != 7000 {
		t.Errorf("ship 2 row = %+v", s2)
	}
	// The payment has no ship-to: its unapplied cash sits in the no-group row.
	if got := byShip["customer"].UnappliedCents; got != -2000 {
		t.Errorf("no-ship row unapplied = %d, want -2000", got)
	}

	// The summary totals per currency.
	sum, err := w.svc.AgingSummary(context.Background(), account.AgingQuery{AsOf: daysFromNow(0), Basis: "due_date", CustomerID: &w.customerID})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Totals) != 1 || sum.Totals[0].Currency != "USD" || sum.Totals[0].TotalCents != 13000 {
		t.Errorf("summary = %+v", sum.Totals)
	}
}

// EXIT LINE: the as_of aging of a past date ignores later applications, counts
// an invoice voided after that date, and ties to the 1020 and 2200 balances on
// that date.
func TestARAgingAsOfPastDate(t *testing.T) {
	w := newARWorld(t)
	asOf := daysFromNow(-15)

	// Both invoices are due 20 days ago: 5 days late on the as_of date of 15
	// days ago, 20 days late today.
	voided := w.invoice(4000, daysFromNow(-50))
	open := w.invoice(6000, daysFromNow(-50))
	pay := w.payment(4000, daysFromNow(-20), "CHECK")

	w.mustAct("void the invoice after the as_of date", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.VoidInvoice(ctx, account.VoidInvoiceIn{InvoiceID: voided.ID,
			EntryID: entryOf(w, voided.ID), Actor: "u-finance", Reason: "wrong job", On: daysFromNow(-5)})
	})
	w.mustAct("apply after the as_of date", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay, Lines: []account.ApplyLine{{InvoiceID: open.ID, AmountCents: 2000}},
			On: daysFromNow(-10), Actor: "u-test"})
	})
	w.mustAct("apply yesterday", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay, Lines: []account.ApplyLine{{InvoiceID: open.ID, AmountCents: 2000}},
			On: daysFromNow(-1), Actor: "u-test"})
	})

	items, err := w.svc.Aging(context.Background(), account.AgingQuery{GroupBy: "customer", AsOf: asOf, Basis: "due_date", CustomerID: &w.customerID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("as_of aging rows = %d, want 1: %+v", len(items), items)
	}
	it := items[0]
	// The open invoice counts whole (both applications are later than as_of),
	// and the voided one counts because it was voided after as_of.
	if it.Days1To30 != 10000 {
		t.Errorf("1-30 bucket = %d, want 10000 (both invoices whole)", it.Days1To30)
	}
	if it.UnappliedCents != -4000 {
		t.Errorf("unapplied = %d, want -4000", it.UnappliedCents)
	}
	if buckets := it.CurrentCents + it.Days1To30 + it.Days31To60 + it.Days61To90 + it.Over90; int64(buckets) != w.netOf("1020", true, &asOf) {
		t.Errorf("buckets %d != 1020 net %d on the as_of date", buckets, w.netOf("1020", true, &asOf))
	}
	if int64(-it.UnappliedCents) != w.netOf("2200", false, &asOf) {
		t.Errorf("unapplied %d != 2200 net %d on the as_of date", -it.UnappliedCents, w.netOf("2200", false, &asOf))
	}

	// Today the voided invoice is gone and both applications count.
	items, err = w.svc.Aging(context.Background(), account.AgingQuery{GroupBy: "customer", AsOf: daysFromNow(0), Basis: "due_date", CustomerID: &w.customerID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Days1To30 != 2000 || items[0].UnappliedCents != 0 {
		t.Errorf("today's aging = %+v, want the invoice at 2000 and no unapplied", items)
	}
}

// Payment void reopens its invoices and reverses the ledger; a void after a
// partial refund posts only the unrefunded amount; a card payment void is
// refused; a refund is limited to the unapplied amount.
func TestARPaymentVoid(t *testing.T) {
	w := newARWorld(t)
	inv := w.invoice(10000, daysFromNow(-30))
	pay := w.payment(10000, daysFromNow(-3), "CASH",
		account.ApplyLine{InvoiceID: inv.ID, AmountCents: 10000})
	if status, _ := w.invoiceRow(inv.ID); status != "PAID" {
		t.Fatalf("invoice = %s, want PAID", status)
	}

	w.mustAct("void the payment", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.VoidPayment(ctx, account.VoidPaymentIn{PaymentID: pay, Reason: "wrong customer", Actor: "u-finance", On: daysFromNow(0)})
	})
	if status, open := w.invoiceRow(inv.ID); status != "UNPAID" || open != 10000 {
		t.Errorf("invoice = %s open %d, want UNPAID 10000 (reopened)", status, open)
	}
	if status, _, _, unapplied := w.paymentRow(pay); status != "VOIDED" || unapplied != 0 {
		t.Errorf("payment = %s unapplied %d, want VOIDED 0", status, unapplied)
	}
	for code, want := range map[string]int64{"1010": 0, "2200": 0, "1020": 10000} {
		if got := w.netOf(code, code != "2200", nil); got != want {
			t.Errorf("%s net = %d, want %d (the void reverses exactly what it posted)", code, got, want)
		}
	}
	if got := w.balance(); got != 10000 {
		t.Errorf("balance = %d, want 10000", got)
	}

	// A card payment is never voided here.
	card := w.payment(5000, daysFromNow(-2), "CARD")
	_, err := w.act("void card", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.VoidPayment(ctx, account.VoidPaymentIn{PaymentID: card, Reason: "no", Actor: "u-finance", On: daysFromNow(0)})
	})
	if err == nil || !strings.Contains(err.Error(), "card payment") {
		t.Errorf("voiding a card payment = %v, want the card refusal", err)
	}
	w.assertInvariants("after the refused card void")

	// A refund limited to the unapplied amount, then a void of what is left.
	pay2 := w.payment(10000, daysFromNow(-1), "CHECK")
	_, err = w.act("refund too much", func(ctx context.Context) (*account.Effects, error) {
		_, fx, e := w.svc.RefundPayment(ctx, account.RefundPaymentIn{PaymentID: pay2, AmountCents: 11000, Reason: "too much", Actor: "u-finance", On: daysFromNow(0)})
		return fx, e
	})
	if err == nil || !strings.Contains(err.Error(), "unapplied") {
		t.Errorf("over refund = %v, want the unapplied refusal", err)
	}
	w.mustAct("refund 3000", func(ctx context.Context) (*account.Effects, error) {
		_, fx, e := w.svc.RefundPayment(ctx, account.RefundPaymentIn{PaymentID: pay2, AmountCents: 3000, Reason: "customer asked", Actor: "u-finance", On: daysFromNow(0)})
		return fx, e
	})
	if _, _, _, unapplied := w.paymentRow(pay2); unapplied != 7000 {
		t.Errorf("unapplied after refund = %d, want 7000", unapplied)
	}
	w.mustAct("void the refunded payment", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.VoidPayment(ctx, account.VoidPaymentIn{PaymentID: pay2, Reason: "duplicate", Actor: "u-finance", On: daysFromNow(0)})
	})
	// The void posts only the unrefunded amount: 2200 held 7000 before it
	// (5000 card + 10000 pay2 - 3000 refund - the void's 7000 leaves the card).
	if got := w.netOf("2200", false, nil); got != 5000 {
		t.Errorf("deposits net = %d, want 5000 (the card payment alone)", got)
	}
	var voidLegs int64
	if err := w.db.Pool.QueryRow(context.Background(), `
		SELECT ROUND(SUM(l.debit) * 100)::bigint FROM gl_journal_lines l
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id AND e.memo = 'Void of payment ' || (SELECT number FROM payments WHERE id = $1)
		JOIN gl_accounts a ON a.id = l.account_id AND a.code = '2200'`, pay2).Scan(&voidLegs); err != nil {
		t.Fatal(err)
	}
	if voidLegs != 7000 {
		t.Errorf("the void took %d from deposits, want the unrefunded 7000", voidLegs)
	}
}

// Reversing a payment application reverses the discount taken in the same
// act; a discount is refused past its date and over its cap.
func TestARDiscountTakenAndReversed(t *testing.T) {
	w := newARWorld(t)
	twoPct := int64(20000) // ten thousandths of a percent
	inv := w.invoice(10000, daysFromNow(-1), withDiscount(twoPct, daysFromNow(5)))
	pay := w.payment(10000, daysFromNow(0), "CHECK")

	// Past the due date: refused.
	_, err := w.act("discount late", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay, Lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 5000, DiscountCents: 100}},
			On: daysFromNow(6), Actor: "u-test"})
	})
	if err == nil || !strings.Contains(err.Error(), "discount") {
		t.Errorf("late discount = %v, want the discount refusal", err)
	}
	w.assertInvariants("after the refused discount")

	w.mustAct("apply with discount", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay, Lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 5000, DiscountCents: 100}},
			On: daysFromNow(0), Actor: "u-test"})
	})
	if status, open := w.invoiceRow(inv.ID); status != "PARTIAL" || open != 4900 {
		t.Errorf("invoice = %s open %d, want PARTIAL 4900", status, open)
	}
	if got := w.netOf("4050", true, nil); got != 100 {
		t.Errorf("sales discounts net = %d, want 100", got)
	}

	// Over the cap (2 percent of 10000 = 200, 100 taken): refused.
	_, err = w.act("discount over cap", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay, Lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 100, DiscountCents: 150}},
			On: daysFromNow(0), Actor: "u-test"})
	})
	if err == nil || !strings.Contains(err.Error(), "discount") {
		t.Errorf("over cap discount = %v, want the discount refusal", err)
	}
	w.assertInvariants("after the refused cap")

	// A discount never stands alone for reversing.
	dApp := w.appID(inv.ID, "DISCOUNT")
	_, err = w.act("reverse the discount", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Reverse(ctx, account.ReverseIn{ApplicationID: dApp, Reason: "no", Actor: "u-finance", On: daysFromNow(0)})
	})
	if err == nil || !strings.Contains(err.Error(), "stands beside the payment") {
		t.Errorf("reversing a discount = %v, want the stands-with-payment refusal", err)
	}

	// Reversing the payment application takes its discount with it.
	pApp := w.appID(inv.ID, "PAYMENT")
	w.mustAct("reverse with discount", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Reverse(ctx, account.ReverseIn{ApplicationID: pApp, Reason: "wrong invoice", Actor: "u-finance", On: daysFromNow(0)})
	})
	if got := w.netOf("4050", true, nil); got != 0 {
		t.Errorf("sales discounts net = %d, want 0 (reversed in the same act)", got)
	}
	if status, open := w.invoiceRow(inv.ID); status != "UNPAID" || open != 10000 {
		t.Errorf("invoice = %s open %d, want UNPAID 10000", status, open)
	}
	if _, _, _, unapplied := w.paymentRow(pay); unapplied != 10000 {
		t.Errorf("unapplied = %d, want 10000 (cash and discount back)", unapplied)
	}
}

// Write off posts 5040 / 1020 and closes the invoice written off; its
// reversal reopens it.
func TestARWriteOffAndReversal(t *testing.T) {
	w := newARWorld(t)
	inv := w.invoice(8000, daysFromNow(-120))
	part := w.invoice(5000, daysFromNow(-120))

	w.mustAct("write off part", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.WriteOff(ctx, account.WriteOffIn{InvoiceID: part.ID, AmountCents: 3000, Reason: "bankruptcy", Actor: "u-finance", On: daysFromNow(-30)})
	})
	if status, open := w.invoiceRow(part.ID); status != "PARTIAL" || open != 2000 {
		t.Errorf("partly written off = %s open %d, want PARTIAL 2000", status, open)
	}

	w.mustAct("write off the whole invoice", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.WriteOff(ctx, account.WriteOffIn{InvoiceID: inv.ID, AmountCents: 8000, Reason: "bankruptcy", Actor: "u-finance", On: daysFromNow(-30)})
	})
	if status, open := w.invoiceRow(inv.ID); status != "WRITTEN_OFF" || open != 0 {
		t.Errorf("invoice = %s open %d, want WRITTEN_OFF 0", status, open)
	}
	if got := w.netOf("5040", true, nil); got != 11000 {
		t.Errorf("bad debt net = %d, want 11000", got)
	}
	if got := w.netOf("1020", true, nil); got != 2000 {
		t.Errorf("AR net = %d, want 2000 (the part invoice's remainder)", got)
	}

	// Over the open amount is refused.
	_, err := w.act("over write off", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.WriteOff(ctx, account.WriteOffIn{InvoiceID: part.ID, AmountCents: 3000, Reason: "too much", Actor: "u-finance", On: daysFromNow(0)})
	})
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Errorf("over write off = %v, want the open amount refusal", err)
	}
	w.assertInvariants("after the refused write off")

	// Reversing the write off reopens the invoice.
	app := w.appID(inv.ID, "WRITE_OFF")
	w.mustAct("reverse the write off", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Reverse(ctx, account.ReverseIn{ApplicationID: app, Reason: "paid after all", Actor: "u-finance", On: daysFromNow(0)})
	})
	if status, open := w.invoiceRow(inv.ID); status != "UNPAID" || open != 8000 {
		t.Errorf("invoice = %s open %d, want UNPAID 8000 (reopened)", status, open)
	}
	if got := w.netOf("5040", true, nil); got != 3000 {
		t.Errorf("bad debt net = %d, want 3000 (only the part write off stands)", got)
	}
}

// A credit memo's credit applies to invoices without a ledger entry (both
// sides are 1020) and refunds out through 1020 / 1010.
func TestARCreditMemoApplyAndRefund(t *testing.T) {
	w := newARWorld(t)
	inv := w.invoice(10000, daysFromNow(-30))

	memo := uuid.New()
	w.exec(`INSERT INTO credit_memos (id, customer_id, branch_id, currency, reason_code, reason, amount, subtotal, tax_amount,
			total_amount, status, memo_date)
		VALUES ($1, $2, `+arBranch+`, 'USD', 'RETURN', 'returned goods', 0, 0, 0, 0, 'DRAFT', CURRENT_DATE)`, memo, w.customerID)
	w.mustAct("post the memo", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.PostCreditMemo(ctx, account.PostCreditMemoIn{MemoID: memo, CustomerID: w.customerID, Number: "CM-" + memo.String()[:8],
			Currency: "USD", MemoDate: daysFromNow(-10), SubtotalCents: -5000, TotalCents: -5000, Actor: "u-test",
			Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Debit: 5000}}})
	})
	if got := w.netOf("1020", true, nil); got != 5000 {
		t.Errorf("AR net = %d, want 5000", got)
	}
	if got := w.balance(); got != 5000 {
		t.Errorf("balance = %d, want 5000", got)
	}

	// Apply 3000 of the memo: no new entry (both sides would be 1020).
	before := len(w.entries)
	w.mustAct("apply the memo", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.ApplyCreditMemo(ctx, account.ApplyCreditMemoIn{MemoID: memo,
			Lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 3000}}, On: daysFromNow(-5), Actor: "u-test"})
	})
	if len(w.entries) != before {
		t.Errorf("the memo application posted %d entries, want none", len(w.entries)-before)
	}
	if status, open := w.invoiceRow(inv.ID); status != "PARTIAL" || open != 7000 {
		t.Errorf("invoice = %s open %d, want PARTIAL 7000", status, open)
	}

	// Refund the rest out: DR 1020 / CR 1010.
	w.mustAct("refund the memo", func(ctx context.Context) (*account.Effects, error) {
		_, fx, e := w.svc.RefundCreditMemo(ctx, account.RefundCreditMemoIn{MemoID: memo, AmountCents: 2000, Reason: "cash back", Method: "CHECK", Actor: "u-finance", On: daysFromNow(0)})
		return fx, e
	})
	var memoStatus string
	var memoOpen int64
	if err := w.db.Pool.QueryRow(context.Background(), `SELECT status, ROUND(amount_open * 100)::bigint FROM credit_memos WHERE id = $1`, memo).Scan(&memoStatus, &memoOpen); err != nil {
		t.Fatal(err)
	}
	if memoStatus != "APPLIED" || memoOpen != 0 {
		t.Errorf("memo = %s open %d, want APPLIED 0", memoStatus, memoOpen)
	}
	if got := w.netOf("1020", true, nil); got != 7000 {
		t.Errorf("AR net = %d, want 7000 (invoice less the applied credit)", got)
	}
	if got := w.netOf("1010", true, nil); got != -2000 {
		t.Errorf("cash net = %d, want -2000 (the refund paid out)", got)
	}

	// A memo with applications is not voided.
	_, err := w.act("void the used memo", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.VoidCreditMemo(ctx, account.VoidCreditMemoIn{MemoID: memo, Actor: "u-finance", Reason: "no", On: daysFromNow(0)})
	})
	if err == nil || !strings.Contains(err.Error(), "has been used") {
		t.Errorf("voiding a used memo = %v, want the has_applications refusal", err)
	}
	w.assertInvariants("after the refused memo void")
}

// refusalText flattens an httpx error into the text its details carry, so a
// refusal can be matched on the field it names, not only its message.
func refusalText(err error) string {
	var e *httpx.Error
	if !errors.As(err, &e) || e == nil {
		return err.Error()
	}
	out := []string{e.Message}
	for _, d := range e.Details {
		out = append(out, d.Field, d.Message, d.Code)
	}
	return strings.Join(out, " ")
}

// Over application, cross currency, a voided target, a foreign customer's
// invoice and a duplicated line are refused, each with its blocker.
func TestARApplicationRefusals(t *testing.T) {
	w := newARWorld(t)
	other := uuid.New()
	w.exec(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'AR Second Co', $2, `+arBranch+`)`,
		other, "AR2-"+other.String()[:8])
	inv := w.invoice(6000, daysFromNow(-30))
	foreign := w.invoiceOn(other, 6000, daysFromNow(-30))
	voided := w.invoice(5000, daysFromNow(-30))
	w.mustAct("void it", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.VoidInvoice(ctx, account.VoidInvoiceIn{InvoiceID: voided.ID, EntryID: entryOf(w, voided.ID), Actor: "u-finance", Reason: "no", On: daysFromNow(0)})
	})
	pay := w.payment(4000, daysFromNow(-1), "CASH")

	for _, tc := range []struct {
		name, want string
		lines      []account.ApplyLine
		pay        uuid.UUID
	}{
		{name: "over unapplied", want: "unapplied", pay: pay,
			lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 5000}}},
		{name: "over open", want: "open", pay: pay,
			lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 4000, DiscountCents: 3000}}},
		{name: "voided target", want: "void", pay: pay,
			lines: []account.ApplyLine{{InvoiceID: voided.ID, AmountCents: 100}}},
		{name: "foreign customer", want: "different customers", pay: pay,
			lines: []account.ApplyLine{{InvoiceID: foreign, AmountCents: 100}}},
		{name: "unknown invoice", want: "no such invoice", pay: pay,
			lines: []account.ApplyLine{{InvoiceID: uuid.New(), AmountCents: 100}}},
		{name: "duplicate line", want: "once", pay: pay,
			lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 100}, {InvoiceID: inv.ID, AmountCents: 200}}},
	} {
		_, err := w.act(tc.name, func(ctx context.Context) (*account.Effects, error) {
			return w.svc.Apply(ctx, account.ApplyIn{PaymentID: tc.pay, Lines: tc.lines, On: daysFromNow(0), Actor: "u-test"})
		})
		if err == nil || !strings.Contains(refusalText(err), tc.want) {
			t.Errorf("%s = %v, want a refusal naming %q", tc.name, err, tc.want)
		}
	}
	// Nothing was written by any refusal.
	if status, open := w.invoiceRow(inv.ID); status != "UNPAID" || open != 6000 {
		t.Errorf("invoice = %s open %d, want UNPAID 6000", status, open)
	}
	if _, _, _, unapplied := w.paymentRow(pay); unapplied != 4000 {
		t.Errorf("unapplied = %d, want 4000", unapplied)
	}
	w.assertInvariants("after every refusal")

	// Cross currency: a EUR payment against the USD invoice.
	eurPay := uuid.New()
	var fxE *account.Effects
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		var e error
		_, fxE, e = w.svc.RecordPayment(ctx, account.RecordPaymentIn{PaymentID: eurPay, CustomerID: w.customerID,
			BranchID: w.branchID, Currency: "EUR", Method: "CHECK", AmountCents: 1000, ReceivedOn: daysFromNow(-1), Actor: "u-test"})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	w.keep(fxE)
	_, err = w.act("cross currency", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Apply(ctx, account.ApplyIn{PaymentID: eurPay, Lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 1000}},
			On: daysFromNow(0), Actor: "u-test"})
	})
	if err == nil || !strings.Contains(err.Error(), "different currencies") {
		t.Errorf("cross currency = %v, want the currency_mismatch refusal", err)
	}
	w.assertInvariants("after the cross currency refusal")
}

// The deposits of an order apply to its fulfilment's invoice oldest first, up
// to the open amount (ADR 0005 5.6 step 7).
func TestARApplyDepositsAtFulfilment(t *testing.T) {
	w := newARWorld(t)
	inv := w.invoice(10000, daysFromNow(-1))
	old := w.payment(3000, daysFromNow(-10), "CHECK")
	fresh := w.payment(8000, daysFromNow(-2), "CHECK")

	fx := w.mustAct("apply deposits", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.ApplyDeposits(ctx, inv.ID, []uuid.UUID{old, fresh}, daysFromNow(0), "u-test")
	})
	if status, open := w.invoiceRow(inv.ID); status != "PAID" || open != 0 {
		t.Errorf("invoice = %s open %d, want PAID 0", status, open)
	}
	if _, _, _, unapplied := w.paymentRow(old); unapplied != 0 {
		t.Errorf("older deposit unapplied = %d, want 0", unapplied)
	}
	if _, _, _, unapplied := w.paymentRow(fresh); unapplied != 1000 {
		t.Errorf("newer deposit unapplied = %d, want 1000 left", unapplied)
	}
	if len(fx.ApplicationIDs) != 2 {
		t.Errorf("deposits wrote %d applications, want one per payment", len(fx.ApplicationIDs))
	}
}

// Three payments applying to one invoice at pool size 4 never over apply; a
// payment void racing an application ends consistent (ADR 0005 14.2 C2-4,
// section 11's lane rule).
func TestARThreePaymentsOneInvoiceAtPool4(t *testing.T) {
	w := newARWorldOn(t, testutil.RequireDBMaxConns(t, 4))
	inv := w.invoice(10000, daysFromNow(-30))

	var pays []uuid.UUID
	for i := 0; i < 3; i++ {
		pays = append(pays, w.payment(4000, daysFromNow(-1), "CASH"))
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, refused int
	for _, p := range pays {
		wg.Add(1)
		go func(p uuid.UUID) {
			defer wg.Done()
			err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
				_, err := w.svc.Apply(ctx, account.ApplyIn{PaymentID: p,
					Lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 4000}}, On: daysFromNow(0), Actor: "u-test"})
				return err
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if strings.Contains(err.Error(), "open") {
				refused++
			} else {
				w.t.Errorf("contender: %v", err)
			}
		}(p)
	}
	wg.Wait()

	if ok != 2 || refused != 1 {
		t.Errorf("applied %d refused %d, want 2 and 1 (never over apply)", ok, refused)
	}
	if status, open := w.invoiceRow(inv.ID); status != "PARTIAL" || open != 2000 {
		t.Errorf("invoice = %s open %d, want PARTIAL 2000", status, open)
	}
	var applied int64
	if err := w.db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(ROUND(amount * 100)::bigint), 0) FROM ar_applications WHERE invoice_id = $1 AND reversed_at IS NULL`, inv.ID).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 8000 {
		t.Errorf("applied total = %d, want 8000", applied)
	}
	w.entries = append(w.entries, entriesOfPayment(w, pays...)...)
	w.assertInvariants("after the race")
}

func TestARVoidRacesApplicationAtPool4(t *testing.T) {
	for i := 0; i < 3; i++ {
		w := newARWorldOn(t, testutil.RequireDBMaxConns(t, 4))
		inv := w.invoice(10000, daysFromNow(-30))
		pay := w.payment(10000, daysFromNow(-2), "CASH")
		w.mustAct("first application", func(ctx context.Context) (*account.Effects, error) {
			return w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay,
				Lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 6000}}, On: daysFromNow(-1), Actor: "u-test"})
		})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = w.db.RunInTx(context.Background(), func(ctx context.Context) error {
				_, err := w.svc.VoidPayment(ctx, account.VoidPaymentIn{PaymentID: pay, Reason: "race", Actor: "u-finance", On: daysFromNow(0)})
				return err
			})
		}()
		var applyErr error
		go func() {
			defer wg.Done()
			applyErr = w.db.RunInTx(context.Background(), func(ctx context.Context) error {
				_, err := w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay,
					Lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 4000}}, On: daysFromNow(0), Actor: "u-test"})
				return err
			})
		}()
		wg.Wait()

		// Whichever won, the payment ends voided with its invoices whole, no
		// unapplied cash and nothing half undone: the apply either landed
		// before the void (and the void reversed it) or was refused.
		if status, _, _, unapplied := w.paymentRow(pay); status != "VOIDED" || unapplied != 0 {
			t.Errorf("iter %d: payment = %s unapplied %d, want VOIDED 0", i, status, unapplied)
		}
		if status, open := w.invoiceRow(inv.ID); status != "UNPAID" || open != 10000 {
			t.Errorf("iter %d: invoice = %s open %d, want UNPAID 10000", i, status, open)
		}
		if applyErr != nil && !strings.Contains(applyErr.Error(), "voided") {
			t.Errorf("iter %d: the racing apply = %v", i, applyErr)
		}
		w.entries = append(w.entries, entriesOfPayment(w, pay)...)
		w.assertInvariants("after the void race")
	}
}

func entriesOfPayment(w *arWorld, pays ...uuid.UUID) []uuid.UUID {
	rows, err := w.db.Pool.Query(context.Background(), `
		SELECT e.id FROM gl_journal_entries e WHERE e.source_ref_id = ANY($1)
		   OR e.source_ref_id IN (SELECT a.id FROM ar_applications a WHERE a.payment_id = ANY($1))
		   OR e.source_ref_id IN (SELECT f.id FROM payment_refunds f WHERE f.payment_id = ANY($1))
		   OR e.reverses_entry_id IN (
		       SELECT o.id FROM gl_journal_entries o WHERE o.source_ref_id = ANY($1)
		          OR o.source_ref_id IN (SELECT a.id FROM ar_applications a WHERE a.payment_id = ANY($1))
		          OR o.source_ref_id IN (SELECT f.id FROM payment_refunds f WHERE f.payment_id = ANY($1)))`, pays)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// Every write kind in one walk, the reconciliation read clean at the end: the
// invariants of 9.3 hold after each act (assertInvariants runs inside every
// act helper above).
func TestAREveryWriteKindKeepsTheReconciliationClean(t *testing.T) {
	w := newARWorld(t)
	inv1 := w.invoice(9000, daysFromNow(-100))
	inv2 := w.invoice(4000, daysFromNow(-40))

	// A payment applied at receipt, then voided (which reopens the invoice).
	pay1 := w.payment(9000, daysFromNow(-30), "CHECK", account.ApplyLine{InvoiceID: inv1.ID, AmountCents: 9000})
	_, err := w.act("a zero refund is refused", func(ctx context.Context) (*account.Effects, error) {
		_, fx, e := w.svc.RefundPayment(ctx, account.RefundPaymentIn{PaymentID: pay1, AmountCents: 0, Reason: "never", Actor: "u-finance", On: daysFromNow(0)})
		return fx, e
	})
	if err == nil {
		t.Fatal("a zero refund was taken")
	}
	w.mustAct("void pay1 (reopens inv1)", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.VoidPayment(ctx, account.VoidPaymentIn{PaymentID: pay1, Reason: "duplicate", Actor: "u-finance", On: daysFromNow(0)})
	})

	// A credit memo posted, applied, refunded.
	memo := uuid.New()
	w.exec(`INSERT INTO credit_memos (id, customer_id, branch_id, currency, reason_code, reason, amount, subtotal, tax_amount,
			total_amount, status, memo_date)
		VALUES ($1, $2, `+arBranch+`, 'USD', 'PRICE_ADJUSTMENT', 'price protection', 0, 0, 0, 0, 'DRAFT', CURRENT_DATE)`, memo, w.customerID)
	w.mustAct("post memo", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.PostCreditMemo(ctx, account.PostCreditMemoIn{MemoID: memo, CustomerID: w.customerID, Number: "CM-" + memo.String()[:8],
			Currency: "USD", MemoDate: daysFromNow(-20), SubtotalCents: -3000, TotalCents: -3000, Actor: "u-test",
			Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Debit: 3000}}})
	})
	w.mustAct("apply memo", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.ApplyCreditMemo(ctx, account.ApplyCreditMemoIn{MemoID: memo,
			Lines: []account.ApplyLine{{InvoiceID: inv2.ID, AmountCents: 2000}}, On: daysFromNow(-15), Actor: "u-test"})
	})
	w.mustAct("refund memo", func(ctx context.Context) (*account.Effects, error) {
		_, fx, e := w.svc.RefundCreditMemo(ctx, account.RefundCreditMemoIn{MemoID: memo, AmountCents: 1000, Reason: "cash", Method: "CHECK", Actor: "u-finance", On: daysFromNow(0)})
		return fx, e
	})

	// An application, a write off and its reversal.
	pay2 := w.payment(4000, daysFromNow(-5), "CASH")
	w.mustAct("apply pay2", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay2, Lines: []account.ApplyLine{{InvoiceID: inv2.ID, AmountCents: 1000}},
			On: daysFromNow(-4), Actor: "u-test"})
	})
	w.mustAct("write off inv1", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.WriteOff(ctx, account.WriteOffIn{InvoiceID: inv1.ID, AmountCents: 9000, Reason: "bankruptcy", Actor: "u-finance", On: daysFromNow(-3)})
	})
	w.mustAct("reverse the write off", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Reverse(ctx, account.ReverseIn{ApplicationID: w.appID(inv1.ID, "WRITE_OFF"), Reason: "paid", Actor: "u-finance", On: daysFromNow(-2)})
	})

	// A voided invoice of its own.
	inv3 := w.invoice(2000, daysFromNow(-10))
	w.mustAct("void inv3", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.VoidInvoice(ctx, account.VoidInvoiceIn{InvoiceID: inv3.ID, EntryID: entryOf(w, inv3.ID), Actor: "u-finance", Reason: "mistake", On: daysFromNow(-1)})
	})

	rec, err := w.svc.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rec.Customers {
		if d.CustomerID == w.customerID {
			t.Errorf("reconciliation drift: %+v", d)
		}
	}
}
