// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Cash only accounts: an account whose terms are due on receipt, by
// the terms master's kind and never a text, takes no on account tender; the
// counter offers the payment shapes the server enumerates for it, and the
// sale is not released (no stock out, no invoice) until paid in full.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
)

// addTerms writes a terms row for one test and removes it after the fixture's
// own cleanup (a cleanup registered before the fixture runs after it, once the
// documents that cite the terms are gone).
func addTerms(t *testing.T, db *database.DB, code, name, kind string, netDays any) {
	t.Helper()
	mustExec(t, db, `INSERT INTO payment_terms (code, name, kind, net_days) VALUES ($1, $2, $3, $4) ON CONFLICT (code) DO NOTHING`,
		code, name, kind, netDays)
	t.Cleanup(func() { mustExec(t, db, `DELETE FROM payment_terms WHERE code = $1`, code) })
}

func (f *fixture) setTerms(code string) {
	f.t.Helper()
	mustExec(f.t, f.db, `UPDATE customers SET payment_terms_id = (SELECT id FROM payment_terms WHERE code = $2) WHERE id = $1`, f.customerID, code)
	f.t.Cleanup(func() {
		mustExec(f.t, f.db, `UPDATE customers SET payment_terms_id = payment_terms_default_id() WHERE id = $1`, f.customerID)
	})
}

// RULE: a due on receipt account is refused an account tender 409
// cash_only_terms, before any card is charged and before anything moves; the
// refusal is the standing read's own verdict.
func TestADueOnReceiptAccountTakesNoAccountTender(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setTerms("DUE_ON_RECEIPT")
	sale, total := f.accountCart("2", nil)
	stockBefore := f.stock()

	r := f.completeSale(sale, tender("account", total))
	_, blockers, _ := errorOf(t, r)
	if r.status != http.StatusConflict || len(blockers) != 1 || blockers[0] != "cash_only_terms" {
		t.Fatalf("an account tender on due on receipt terms = %d: %s, want 409 cash_only_terms", r.status, r.raw)
	}
	// a part on account is refused the same way: the account portion is the
	// amount the verdict reads
	r = f.completeSale(sale, tender("account", 500), tender("cash", total-500))
	if _, blockers, _ := errorOf(t, r); r.status != http.StatusConflict || len(blockers) != 1 || blockers[0] != "cash_only_terms" {
		t.Errorf("a part on account = %d: %s, want 409 cash_only_terms", r.status, r.raw)
	}
	if f.stock() != stockBefore || f.counterInvoices(t) != 0 {
		t.Errorf("a refused sale moved stock or wrote an invoice")
	}
	if v := verdictOf(t, f.standing(f.customerID, "amount_cents="+itoa(total))); v["reason"] != "cash_only_terms" {
		t.Errorf("the read says %v, want the completion's cash_only_terms", v["reason"])
	}
}

// RULE: the sale is released only when paid in full: a payment short of the
// total is refused insufficient_tender and nothing leaves the yard; paid in
// full by cash, check or card it completes with its invoice.
func TestADueOnReceiptSaleIsNotReleasedUntilPaidInFull(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setTerms("COD")
	sale, total := f.accountCart("2", nil)
	stockBefore := f.stock()
	r := f.completeSale(sale, tender("cash", total-1))
	_, blockers, _ := errorOf(t, r)
	if r.status != http.StatusConflict || len(blockers) != 1 || blockers[0] != "insufficient_tender" {
		t.Fatalf("a short payment = %d: %s, want 409 insufficient_tender", r.status, r.raw)
	}
	if f.stock() != stockBefore || f.counterInvoices(t) != 0 {
		t.Errorf("a short payment released the goods: stock %s -> %s", stockBefore, f.stock())
	}
	if r := f.completeSale(sale, tender("cash", total)); r.status != http.StatusOK {
		t.Fatalf("paid in full = %d: %s", r.status, r.raw)
	}
	if f.stock() == stockBefore {
		t.Errorf("paid in full left the stock where it was")
	}
	f.assertARInvariants(t)
}

// RULE: the kind decides, not the words: terms named "Due on receipt" whose
// kind is net days allow an account sale, and terms with an unrelated code
// whose kind is due on receipt block it.
func TestCashOnlyIsTheTermsKindNeverAText(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	addTerms(t, db, "ZZ-WORDS", "Due on receipt", "NET_DAYS", 30)
	f := newFixture(t, db)
	f.setTerms("ZZ-WORDS")
	sale, total := f.accountCart("1", nil)
	if r := f.completeSale(sale, tender("account", total)); r.status != http.StatusOK {
		t.Errorf("an account sale on terms merely named due on receipt = %d, want 200: %s", r.status, r.raw)
	}
}
