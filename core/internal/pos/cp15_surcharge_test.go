// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-15 card surcharges (audit CP-15, brief CP15-SURCHARGES-r1), on PR 216's
// seam. The processor decides who is surcharged and says so in its answer
// (TerminalAnswer.FeeCents); Gable keeps a per branch switch, off by default,
// and books what the answer says: the fee rides the card tender as its own
// figure, never inflates the sale's total or tax, and posts to its own income
// account. The branch columns named here are the brief's proposal
// (locations.card_surcharge_enabled, locations.card_surcharge_bps) and the
// account code is the brief's proposal; the migration number is the lead's to
// give, so until it lands these fail on the missing columns.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

const surchargeIncomeCode = "4070"

// feeSink adds the processor's surcharge to every sale answer the simulated
// device delivers: bps of the leg's amount, rounded half up, as the
// processor's own rate would.
type feeSink struct {
	ledger *payment.TerminalLedger
	bps    int64
}

func (s feeSink) ApplyDeviceAnswer(ctx context.Context, a payment.TerminalAnswer) (payment.AnswerOutcome, error) {
	in, err := s.ledger.Get(ctx, a.IntentID)
	if err == nil && in.Kind == payment.TerminalKindSale && a.Verdict == "authorized" {
		a.FeeCents = (in.AmountCents*s.bps + 5000) / 10000
	}
	return s.ledger.ApplyDeviceAnswer(ctx, a)
}

// withSurchargingTerminal wires the simulated terminal whose approvals carry
// a surcharge of bps, and (when on) turns the fixture branch's switch on.
func withSurchargingTerminal(bps int64, on bool) func(*fixture) {
	return func(f *fixture) {
		ledger := payment.NewTerminalLedger(f.db)
		f.terminal = payment.NewSimulatedTerminal(payment.SimApprove, feeSink{ledger: ledger, bps: bps})
		f.service.WithTerminal(f.terminal, ledger)
		if on {
			f.setBranchSurcharge(true, bps)
		}
	}
}

func (f *fixture) setBranchSurcharge(on bool, bps int64) {
	f.t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE locations SET card_surcharge_enabled = $2, card_surcharge_bps = $3 WHERE id = $1`,
		f.branchID, on, bps); err != nil {
		f.t.Fatalf("set the branch surcharge: %v", err)
	}
}

func (f *fixture) cardTenderSurcharge(saleID string) int64 {
	f.t.Helper()
	return countOf(f.t, f.db,
		`SELECT COALESCE(SUM(surcharge_cents), 0)::bigint FROM pos_tenders WHERE transaction_id = $1 AND method = 'CARD'`, saleID)
}

// cardSale makes a one line sale paid whole by card and answers its id.
func (f *fixture) cardSale(t *testing.T) (saleID string, total int64) {
	t.Helper()
	saleID = f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total = f.totalOf(t, saleID)
	if r := f.completeSale(saleID, tender("card", total)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	return saleID, total
}

// TestCP15_OffByDefaultBooksNoSurcharge: a branch that never switched it on
// carries a zero surcharge on its card tender and nothing in the income
// account, with the default off (the column default is false).
func TestCP15_OffByDefaultBooksNoSurcharge(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withSurchargingTerminal(0, false))
	saleID, _ := f.cardSale(t)
	if got := f.cardTenderSurcharge(saleID); got != 0 {
		t.Fatalf("surcharge %d on a branch with the switch off, wanted 0", got)
	}
	var on bool
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT card_surcharge_enabled FROM locations WHERE id = $1`, f.branchID).Scan(&on); err != nil || on {
		t.Fatalf("the branch switch is %t (%v), wanted off by default", on, err)
	}
}

// TestCP15_AnswerFeeIsStoredOnTheCardTender: switch on at 3 percent, the
// device's answer carries the fee, the card tender stores it as its own
// figure, the tender amount and the sale total stay the goods' price (the fee
// is on top of the sale, not part of it).
func TestCP15_AnswerFeeIsStoredOnTheCardTender(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withSurchargingTerminal(300, true))
	saleID, total := f.cardSale(t)
	want := (total*300 + 5000) / 10000
	if got := f.cardTenderSurcharge(saleID); got != want {
		t.Fatalf("stored surcharge %d, wanted %d (3 percent of %d)", got, want, total)
	}
	if got := f.totalOf(t, saleID); got != total {
		t.Fatalf("the sale total moved to %d, wanted %d: the fee is not part of the sale", got, total)
	}
	if got := countOf(t, f.db,
		`SELECT COALESCE(SUM(ROUND(amount * 100)), 0)::bigint FROM pos_tenders WHERE transaction_id = $1 AND method = 'CARD'`, saleID); got != total {
		t.Fatalf("the CARD tender amount is %d, wanted %d", got, total)
	}
}

// TestCP15_FeeBooksToItsOwnIncomeAccountNotRevenueOrTax: the books take the
// fee as a credit to the surcharge income account, and the sales revenue and
// tax figures are what they were without it.
func TestCP15_FeeBooksToItsOwnIncomeAccountNotRevenueOrTax(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withSurchargingTerminal(300, true))
	saleID, total := f.cardSale(t)
	fee := f.cardTenderSurcharge(saleID)
	if fee <= 0 {
		t.Fatalf("no surcharge stored (%d): nothing to book", fee)
	}
	if got := f.accountBalance(surchargeIncomeCode); got != -fee {
		t.Fatalf("surcharge income balance %d, wanted a credit of %d", got, fee)
	}
	tax := countOf(t, f.db, `SELECT ROUND(tax_total * 100)::bigint FROM pos_transactions WHERE id = $1`, saleID)
	if rev := -f.accountBalance("4010"); rev != total-tax {
		t.Fatalf("sales revenue %d, wanted %d: the fee must not touch it", rev, total-tax)
	}
}

// TestCP15_SwitchOffAfterwardsDoesNotRewriteTheStoredFee: the fee is the
// figure the sale was charged; turning the branch switch off later changes
// nothing already stored.
func TestCP15_SwitchOffAfterwardsDoesNotRewriteTheStoredFee(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withSurchargingTerminal(300, true))
	saleID, _ := f.cardSale(t)
	before := f.cardTenderSurcharge(saleID)
	f.setBranchSurcharge(false, 300)
	if after := f.cardTenderSurcharge(saleID); after != before || before == 0 {
		t.Fatalf("stored fee %d before and %d after the switch went off", before, after)
	}
}

// TestCP15_ReturnRefundsTheFeeWithTheCard: a full return of the surcharged
// card sale sends the terminal a refund of the goods and the fee together,
// and the income account nets to zero. Run's own rule: refund by the original
// transaction so the fee comes back with it.
func TestCP15_ReturnRefundsTheFeeWithTheCard(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withSurchargingTerminal(300, true))
	saleID, total := f.cardSale(t)
	fee := f.cardTenderSurcharge(saleID)
	sale := f.getSale(t, saleID)
	lineID := sale["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "original_sale_id": saleID,
		"refund_method": "card", "reason": "cp15",
		"lines": []map[string]any{{"line_id": lineID, "quantity": "2", "restock": true}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusCreated {
		t.Fatalf("card return = %d: %s", r.status, r.raw)
	}
	var refund int64
	for _, d := range f.terminal.Dispatched() {
		if d.Kind == payment.TerminalKindRefund {
			refund += d.AmountCents
		}
	}
	if refund != total+fee {
		t.Fatalf("refunded %d at the terminal, wanted %d (goods %d plus fee %d)", refund, total+fee, total, fee)
	}
	if got := f.accountBalance(surchargeIncomeCode); got != 0 {
		t.Fatalf("surcharge income %d after a full return, wanted 0", got)
	}
}
