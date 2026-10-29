// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// The batch statement runs (M30): the figures a run records, its items and
// totals, the queue record behind queue_email, and the fact that a run is an
// artefact: two runs of one period answer the same numbers and neither moves
// a balance, a document or a subledger row.

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// statementWorld posts the world's fixture book: an invoice before the
// period, one inside it, and a payment applied inside it.
func statementWorld(t *testing.T) *arWorld {
	t.Helper()
	w := newARWorld(t)
	w.svc.WithOutbox(outbox.NewWriter(w.db, "test"))
	// Opening: posted before the period starts, 15 days past due at day zero.
	w.invoice(50_000, daysFromNow(-45))
	// In period: posted 20 days ago, due in 10 days.
	inv := w.invoice(100_000, daysFromNow(-20))
	// The payment inside the period that settles part of it.
	w.payment(40_000, daysFromNow(-10), "CHECK", account.ApplyLine{InvoiceID: inv.ID, AmountCents: 40_000})
	return w
}

// TestStatementRunRecordsTheFigures walks one run end to end.
func TestStatementRunRecordsTheFigures(t *testing.T) {
	testutil.LockOutboxTables(t)
	w := statementWorld(t)
	start, end := daysFromNow(-30), daysFromNow(0)

	// The run names the world's own customer: a run over the whole book
	// reads every account on the shared test database, and the totals and
	// the balance it records are then over accounts this test never wrote.
	run, err := w.svc.CreateStatementRun(context.Background(), account.StatementRunIn{
		PeriodStart: start, PeriodEnd: end, QueueEmail: true, CustomerIDs: []uuid.UUID{w.customerID},
		Actor: "u-test", Role: "finance"})
	if err != nil {
		t.Fatalf("create the run: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("the run reads %+v", run)
	}
	if run.PeriodEnd != end.Format("2006-01-02") || run.AsOfDate != end.Format("2006-01-02") {
		t.Fatalf("the run's period reads %s to %s as of %s", run.PeriodStart, run.PeriodEnd, run.AsOfDate)
	}
	if run.TotalCustomers != 1 || run.QueuedCount != 1 || run.SentCount != 0 {
		t.Fatalf("the run totals read %d/%d/%d, want 1/1/0", run.TotalCustomers, run.QueuedCount, run.SentCount)
	}
	if run.TotalBalanceCents != 110_000 {
		t.Fatalf("the run's total balance is %d, want 110000", run.TotalBalanceCents)
	}
	if len(run.Items) != 1 {
		t.Fatalf("%d items, want one", len(run.Items))
	}
	it := run.Items[0]
	if it.CustomerID != w.customerID {
		t.Fatalf("the item names customer %s", it.CustomerID)
	}
	if it.OpeningBalanceCents != 50_000 || it.InvoicesCents != 100_000 || it.PaymentsCents != 40_000 ||
		it.FinanceChargesCents != 0 || it.ClosingBalanceCents != 110_000 {
		t.Fatalf("the item figures read open %d, invoices %d, payments %d, charges %d, closing %d",
			it.OpeningBalanceCents, it.InvoicesCents, it.PaymentsCents, it.FinanceChargesCents, it.ClosingBalanceCents)
	}
	if it.DeliveryStatus != "queued" || it.OutboxID == nil {
		t.Fatalf("the delivery reads %q with outbox %v, want queued with an id", it.DeliveryStatus, it.OutboxID)
	}
	if it.AgingBuckets["current_cents"] != 60_000 || it.AgingBuckets["days_1_30_cents"] != 50_000 {
		t.Fatalf("the aging buckets read %v", it.AgingBuckets)
	}
	// The queue's event is in the outbox, inside the run's own record.
	if n := w.queryInt(`SELECT count(*) FROM events_outbox WHERE event_id = $1 AND type = 'statement.queued'`, *it.OutboxID); n != 1 {
		t.Fatalf("%d statement.queued events carry the item's outbox id", n)
	}
	// The run moved nothing: the balance is what the acts left.
	if got := w.queryInt(`SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1`, w.customerID); got != 110_000 {
		t.Fatalf("the run moved the balance to %d", got)
	}
	// A run of another id is missing; the list answers newest first.
	if _, err := w.svc.GetStatementRun(context.Background(), uuid.New()); err == nil {
		t.Fatal("a run of an unknown id was answered")
	}
	runs, err := w.svc.ListStatementRuns(context.Background(), account.StatementRunFilter{Limit: 10})
	if err != nil || len(runs) != 1 {
		t.Fatalf("the list answered %d runs, %v", len(runs), err)
	}
	got, err := w.svc.GetStatementRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get the run: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != it.ID {
		t.Fatalf("the run's items read %+v", got.Items)
	}
}

// TestStatementRunIsAnArtefact: a second run of the same period answers the
// same numbers, writes no document and no subledger row, and both runs keep
// their own items.
func TestStatementRunIsAnArtefact(t *testing.T) {
	w := statementWorld(t)
	start, end := daysFromNow(-30), daysFromNow(0)

	in := account.StatementRunIn{PeriodStart: start, PeriodEnd: end, CustomerIDs: []uuid.UUID{w.customerID},
		Actor: "u-test", Role: "finance"}
	first, err := w.svc.CreateStatementRun(context.Background(), in)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	rowsBefore := w.queryInt(`SELECT count(*) FROM customer_transactions WHERE customer_id = $1`, w.customerID)
	invoicesBefore := w.queryInt(`SELECT count(*) FROM invoices WHERE customer_id = $1`, w.customerID)

	second, err := w.svc.CreateStatementRun(context.Background(), in)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("the two runs share an id")
	}
	a, b := first.Items[0], second.Items[0]
	if a.OpeningBalanceCents != b.OpeningBalanceCents || a.InvoicesCents != b.InvoicesCents ||
		a.PaymentsCents != b.PaymentsCents || a.ClosingBalanceCents != b.ClosingBalanceCents {
		t.Fatalf("the two runs disagree: %+v against %+v", a, b)
	}
	if got := w.queryInt(`SELECT count(*) FROM customer_transactions WHERE customer_id = $1`, w.customerID); got != rowsBefore {
		t.Fatalf("the runs wrote %d subledger rows", got-rowsBefore)
	}
	if got := w.queryInt(`SELECT count(*) FROM invoices WHERE customer_id = $1`, w.customerID); got != invoicesBefore {
		t.Fatalf("the runs wrote %d invoices", got-invoicesBefore)
	}
	w.assertInvariants("after two runs")
}

// TestStatementRunScopeAndQueueing: a named zero balance customer gets an
// item marked skipped; an unnamed positive balance gets queued; an unnamed
// run covers the whole book's balances; and a period that ends past today is
// refused.
func TestStatementRunScopeAndQueueing(t *testing.T) {
	testutil.LockOutboxTables(t)
	w := statementWorld(t)
	// A customer with no balance at all.
	quiet := uuid.New()
	w.exec(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Quiet Co', $2, `+arBranch+`)`,
		quiet, "QUIET-"+uuid.NewString()[:8])
	w.side = append(w.side, quiet)

	run, err := w.svc.CreateStatementRun(context.Background(), account.StatementRunIn{
		PeriodStart: daysFromNow(-30), PeriodEnd: daysFromNow(0), QueueEmail: true,
		CustomerIDs: []uuid.UUID{w.customerID, quiet}, Actor: "u-test", Role: "finance"})
	if err != nil {
		t.Fatalf("create the run: %v", err)
	}
	if len(run.Items) != 2 {
		t.Fatalf("%d items, want one per named customer", len(run.Items))
	}
	byCustomer := map[uuid.UUID]account.StatementRunItem{}
	for _, it := range run.Items {
		byCustomer[it.CustomerID] = it
	}
	if it := byCustomer[w.customerID]; it.DeliveryStatus != "queued" {
		t.Fatalf("the owing customer's delivery is %q, want queued", it.DeliveryStatus)
	}
	if it := byCustomer[quiet]; it.DeliveryStatus != "skipped" || it.ClosingBalanceCents != 0 {
		t.Fatalf("the quiet customer's item reads %+v, want skipped at zero", it)
	}
	if run.QueuedCount != 1 || run.TotalCustomers != 2 {
		t.Fatalf("the totals read %d queued of %d, want 1 of 2", run.QueuedCount, run.TotalCustomers)
	}
	// A period ending after today is refused.
	if _, err := w.svc.CreateStatementRun(context.Background(), account.StatementRunIn{
		PeriodStart: daysFromNow(0), PeriodEnd: daysFromNow(7), Actor: "u-test", Role: "finance"}); err == nil {
		t.Fatal("a run into the future was taken")
	}
	// A period that runs backwards is refused.
	if _, err := w.svc.CreateStatementRun(context.Background(), account.StatementRunIn{
		PeriodStart: daysFromNow(0), PeriodEnd: daysFromNow(-30), Actor: "u-test", Role: "finance"}); err == nil {
		t.Fatal("a backwards period was taken")
	}
	// A caller whose role is not one of the finance roles is refused.
	if _, err := w.svc.CreateStatementRun(context.Background(), account.StatementRunIn{
		PeriodStart: daysFromNow(-30), PeriodEnd: daysFromNow(0), Actor: "u-test", Role: "sales"}); err == nil {
		t.Fatal("a sales role generated a run")
	}
}

// TestStatementRunSplitsFinanceChargePayments: a payment applied to a
// finance charge invoice counts as a payment, never as a negative charge, so
// the period figures reconcile.
func TestStatementRunSplitsFinanceChargePayments(t *testing.T) {
	w := newARWorld(t)
	w.chargeRules(200, 3, 0, 0, false)
	w.invoice(1_000_000, daysFromNow(-95))
	chargeRun := w.runCharges(false, daysFromNow(-35), w.customerID) // the charge invoice is due day-5
	for _, it := range chargeRun.Items {
		if it.JournalEntryID != nil {
			w.entries = append(w.entries, *it.JournalEntryID)
		}
	}

	var chargeInvoice uuid.UUID
	if err := w.db.Pool.QueryRow(context.Background(),
		`SELECT id FROM invoices WHERE customer_id = $1 AND origin = 'FINANCE_CHARGE' LIMIT 1`,
		w.customerID).Scan(&chargeInvoice); err != nil {
		t.Fatalf("read the charge invoice: %v", err)
	}
	// The customer pays the charge inside the statement period.
	w.payment(20_000, daysFromNow(-10), "CHECK", account.ApplyLine{InvoiceID: chargeInvoice, AmountCents: 20_000})

	run, err := w.svc.CreateStatementRun(context.Background(), account.StatementRunIn{
		PeriodStart: daysFromNow(-40), PeriodEnd: daysFromNow(0),
		CustomerIDs: []uuid.UUID{w.customerID}, Actor: "u-test", Role: "finance"})
	if err != nil {
		t.Fatalf("create the run: %v", err)
	}
	if len(run.Items) != 1 {
		t.Fatalf("%d items, want one", len(run.Items))
	}
	it := run.Items[0]
	if it.OpeningBalanceCents != 1_000_000 || it.InvoicesCents != 0 || it.PaymentsCents != 20_000 ||
		it.FinanceChargesCents != 20_000 || it.ClosingBalanceCents != 1_000_000 {
		t.Fatalf("the figures read open %d, invoices %d, payments %d, charges %d, closing %d",
			it.OpeningBalanceCents, it.InvoicesCents, it.PaymentsCents, it.FinanceChargesCents, it.ClosingBalanceCents)
	}
	if got := int64(it.OpeningBalanceCents) + int64(it.InvoicesCents) - int64(it.PaymentsCents) + int64(it.FinanceChargesCents); got != int64(it.ClosingBalanceCents) {
		t.Fatalf("the figures do not reconcile: %d against closing %d", got, it.ClosingBalanceCents)
	}
}
