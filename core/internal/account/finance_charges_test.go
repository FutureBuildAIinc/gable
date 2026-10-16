// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// The periodic finance charges (M30): the preview and the execution of one
// run, the grace day filter, the minimum charge and minimum balance, the
// non-compounding default, and the balanced entry the charge posts. Every
// executed run is narrowed to the world's own customers, so the shared
// database's other accounts never take a charge from a fixture.

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/google/uuid"
)

// chargeRules writes the tenant's finance charge rules for a test. Each
// finance charge test sets its own, so no test inherits another's policy.
func (w *arWorld) chargeRules(rateBps int64, graceDays int64, minCharge, minBalance int64, compound bool) {
	w.t.Helper()
	w.exec(`UPDATE ar_finance_charge_rules SET monthly_rate_bps = $1, grace_days = $2, min_charge_cents = $3,
		min_balance_cents = $4, compound = $5, income_account_code = '4060', updated_by = 'u-test'`,
		rateBps, graceDays, minCharge, minBalance, compound)
}

func (w *arWorld) rulesRow() account.FinanceChargeRules {
	w.t.Helper()
	rules, err := w.svc.GetFinanceChargeRules(context.Background())
	if err != nil {
		w.t.Fatalf("read the rules: %v", err)
	}
	return *rules
}

// runCharges runs the finance charges as of a day (zero means today),
// narrowed to the named customers.
func (w *arWorld) runCharges(dryRun bool, asOf time.Time, only ...uuid.UUID) *account.FinanceChargeRun {
	w.t.Helper()
	run, err := w.svc.RunFinanceCharges(context.Background(), account.FinanceChargeRunIn{
		AsOf: asOf, DryRun: dryRun, Actor: "u-test", CustomerIDs: only})
	if err != nil {
		w.t.Fatalf("run the charges (dry %v): %v", dryRun, err)
	}
	return run
}

// TestFinanceChargeDryRunPreviews previews the charge and writes no document,
// no entry and no balance.
func TestFinanceChargeDryRunPreviews(t *testing.T) {
	w := newARWorld(t)
	w.chargeRules(200, 5, 100, 0, false)
	w.invoice(1_000_000, daysFromNow(-95)) // due 65 days ago

	before := w.queryInt(`SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1`, w.customerID)
	run := w.runCharges(true, daysFromNow(-35), w.customerID)
	if run.Status != "dry_run" {
		t.Fatalf("the run reads %q, want dry_run", run.Status)
	}
	if run.RateBps != 200 || run.GraceDays != 5 || run.MinChargeCents != 100 {
		t.Fatalf("the run's rules read %d bps, %d grace, %d min", run.RateBps, run.GraceDays, run.MinChargeCents)
	}
	if len(run.Items) != 1 {
		t.Fatalf("%d items, want the one preview", len(run.Items))
	}
	it := run.Items[0]
	if it.OverdueCents != 1_000_000 || it.ChargeCents != 20_000 {
		t.Fatalf("the preview reads %d overdue, %d charge, want 1000000 and 20000", it.OverdueCents, it.ChargeCents)
	}
	if it.InvoiceID != nil || it.InvoiceNumber != nil || it.JournalEntryID != nil {
		t.Fatalf("the preview carries documents: %+v", it)
	}
	if run.TotalChargedCents != 20_000 || run.CustomerCount != 1 {
		t.Fatalf("the preview totals read %d over %d customers", run.TotalChargedCents, run.CustomerCount)
	}
	if after := w.balance(); after != before {
		t.Fatalf("the preview moved the balance from %d to %d", before, after)
	}
	// The invoice count and the subledger are untouched.
	if n := w.queryInt(`SELECT count(*) FROM invoices WHERE customer_id = $1 AND origin = 'FINANCE_CHARGE'`, w.customerID); n != 0 {
		t.Fatalf("%d charge invoices after a dry run", n)
	}
	w.assertInvariants("after the dry run")
}

// TestFinanceChargeExecution charges the overdue balance, writes the charge
// invoice, moves the balance through the subledger, and posts the balanced
// entry (Debit AR 1020, Credit Finance Charge Income 4060).
func TestFinanceChargeExecution(t *testing.T) {
	w := newARWorld(t)
	w.chargeRules(200, 5, 100, 0, false)
	w.invoice(1_000_000, daysFromNow(-95))

	before := w.queryInt(`SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1`, w.customerID)
	run := w.runCharges(false, daysFromNow(-35), w.customerID)
	if run.Status != "completed" || run.TotalChargedCents != 20_000 || run.CustomerCount != 1 {
		t.Fatalf("the executed run reads %q, %d cents over %d customers", run.Status, run.TotalChargedCents, run.CustomerCount)
	}
	if len(run.Items) != 1 {
		t.Fatalf("%d items, want the one charge", len(run.Items))
	}
	it := run.Items[0]
	if it.InvoiceID == nil || it.InvoiceNumber == nil || it.JournalEntryID == nil {
		t.Fatalf("the charge item names no documents: %+v", it)
	}
	// The charge invoice: a real invoice the core posted, open at the charge.
	var total, open int64
	var origin, status string
	if err := w.db.Pool.QueryRow(context.Background(),
		`SELECT ROUND(total_amount * 100)::bigint, ROUND(amount_open * 100)::bigint, origin::text, status::text
		 FROM invoices WHERE id = $1`, *it.InvoiceID).Scan(&total, &open, &origin, &status); err != nil {
		t.Fatalf("read the charge invoice: %v", err)
	}
	if total != 20_000 || open != 20_000 || origin != "FINANCE_CHARGE" || status != "UNPAID" {
		t.Fatalf("the charge invoice reads %d/%d %s %s", total, open, origin, status)
	}
	if *it.InvoiceNumber == "" {
		t.Fatal("the charge invoice carries no number")
	}
	// The balance moved by the charge, and the subledger with it.
	after := w.queryInt(`SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1`, w.customerID)
	if after != before+20_000 {
		t.Fatalf("the balance went from %d to %d, want +%d", before, after, 20_000)
	}
	// The entry is balanced and lands where the chart says: 1020 debited,
	// 4060 credited.
	w.entries = append(w.entries, *it.JournalEntryID)
	var debit1020, credit4060, debitSum, creditSum int64
	if err := w.db.Pool.QueryRow(context.Background(), `
		SELECT COALESCE(ROUND(SUM(l.debit) FILTER (WHERE a.code = '1020'), 2) * 100, 0),
		       COALESCE(ROUND(SUM(l.credit) FILTER (WHERE a.code = '4060'), 2) * 100, 0),
		       COALESCE(ROUND(SUM(l.debit), 2) * 100, 0), COALESCE(ROUND(SUM(l.credit), 2) * 100, 0)
		FROM gl_journal_lines l
		JOIN gl_accounts a ON a.id = l.account_id
		WHERE l.journal_entry_id = $1`, *it.JournalEntryID).Scan(&debit1020, &credit4060, &debitSum, &creditSum); err != nil {
		t.Fatalf("read the charge entry: %v", err)
	}
	if debit1020 != 20_000 || credit4060 != 20_000 || debitSum != 20_000 || creditSum != 20_000 {
		t.Fatalf("the entry reads 1020 debit %d, 4060 credit %d, sums %d/%d", debit1020, credit4060, debitSum, creditSum)
	}
	w.assertInvariants("after the charge")
}

// TestFinanceChargeGraceAndMinimums: inside the grace days nobody is charged;
// a charge under the minimum is not taken; a balance under the minimum is
// not charged at all.
func TestFinanceChargeGraceAndMinimums(t *testing.T) {
	w := newARWorld(t)
	w.chargeRules(200, 10, 0, 0, false)
	w.invoice(1_000_000, daysFromNow(-35)) // due 5 days ago: inside a 10 day grace

	if run := w.runCharges(true, time.Time{}, w.customerID); len(run.Items) != 0 {
		t.Fatalf("inside the grace days the run would charge %+v", run.Items)
	}
	// The same invoice past a 3 day grace charges.
	w.chargeRules(200, 3, 0, 0, false)
	if run := w.runCharges(true, time.Time{}, w.customerID); len(run.Items) != 1 || run.Items[0].ChargeCents != 20_000 {
		t.Fatalf("past the grace the run answers %+v", run.Items)
	}
	// A charge under the minimum is not taken.
	w.chargeRules(200, 3, 30_000, 0, false)
	if run := w.runCharges(true, time.Time{}, w.customerID); len(run.Items) != 0 {
		t.Fatalf("a charge under the minimum would be taken: %+v", run.Items)
	}
	// A balance under the minimum is not charged at all, whatever the rate.
	w.chargeRules(200, 3, 0, 2_000_000, false)
	if run := w.runCharges(true, time.Time{}, w.customerID); len(run.Items) != 0 {
		t.Fatalf("a balance under the minimum would be charged: %+v", run.Items)
	}
}

// TestFinanceChargeNonCompoundingDefault: a charge's own invoice is not a
// basis for the next charge unless the tenant turned compounding on.
func TestFinanceChargeNonCompoundingDefault(t *testing.T) {
	w := newARWorld(t)
	w.chargeRules(200, 3, 0, 0, false)
	w.invoice(1_000_000, daysFromNow(-95))
	// The charge of the run as of 35 days ago is itself 5 days overdue today.
	w.runCharges(false, daysFromNow(-35), w.customerID)
	if got := w.queryInt(`SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1`, w.customerID); got != 1_020_000 {
		t.Fatalf("the balance after the charge is %d, want 1020000", got)
	}
	// Non-compounding: today's basis is the original invoice only.
	run := w.runCharges(true, time.Time{}, w.customerID)
	if len(run.Items) != 1 || run.Items[0].OverdueCents != 1_000_000 {
		t.Fatalf("the non-compounding basis reads %+v", run.Items)
	}
	// Compounding: the charge invoice joins the basis.
	rules := w.rulesRow()
	rules.Compound = true
	rules.UpdatedBy = nil
	if _, err := w.svc.UpdateFinanceChargeRules(context.Background(), &rules, "u-test", "finance"); err != nil {
		t.Fatalf("turn compounding on: %v", err)
	}
	if got := w.rulesRow(); !got.Compound {
		t.Fatal("the rules did not keep the compound flag")
	}
	compounded := w.runCharges(true, time.Time{}, w.customerID)
	if len(compounded.Items) != 1 || compounded.Items[0].OverdueCents != 1_020_000 || compounded.Items[0].ChargeCents != 20_400 {
		t.Fatalf("the compounding basis reads %+v, want 1020000 overdue charged at 20400", compounded.Items)
	}
}

// TestFinanceChargeRulesWorkflow reads the seeded rules, refuses a rate past
// the cap and an income account outside the chart, and writes what it accepts.
func TestFinanceChargeRulesWorkflow(t *testing.T) {
	w := newARWorld(t)
	in := account.FinanceChargeRules{MonthlyRateBps: 150, GraceDays: 7, MinChargeCents: 250,
		MinBalanceCents: 1_000, Compound: false, IncomeAccountCode: "4060"}
	out, err := w.svc.UpdateFinanceChargeRules(context.Background(), &in, "u-test", "finance")
	if err != nil {
		t.Fatalf("write the rules: %v", err)
	}
	if out.MonthlyRateBps != 150 || out.GraceDays != 7 || out.MinChargeCents != 250 ||
		out.MinBalanceCents != 1_000 || out.Compound || out.IncomeAccountCode != "4060" {
		t.Fatalf("the rules read %+v", out)
	}
	tooBig := in
	tooBig.MonthlyRateBps = 200_001
	if _, err := w.svc.UpdateFinanceChargeRules(context.Background(), &tooBig, "u-test", "finance"); err == nil {
		t.Fatal("a rate past the cap was taken")
	}
	noAccount := in
	noAccount.IncomeAccountCode = "9999"
	if _, err := w.svc.UpdateFinanceChargeRules(context.Background(), &noAccount, "u-test", "finance"); err == nil {
		t.Fatal("an income account outside the chart was taken")
	}
	if _, err := w.svc.UpdateFinanceChargeRules(context.Background(), &in, "u-test", "sales"); err == nil {
		t.Fatal("a sales role wrote the rules")
	}
	// A future as of is refused.
	if _, err := w.svc.RunFinanceCharges(context.Background(), account.FinanceChargeRunIn{
		AsOf: daysFromNow(3), Actor: "u-test"}); err == nil {
		t.Fatal("a charge in the future was taken")
	}
}
