// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-03 card as a tender (audit CP-03, brief CP03-CARD-TENDER-r1): the card
// leg is charged for what is owed on the card and no more, a split sale
// charges the card only its own portion, and two clerks completing one sale
// at once charge the card once. They run on PR 216's seam, the simulated
// terminal standing in for the Run driver.

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// TestCP03_CardOverTenderIsRefusedBeforeTheDevice: a card tender may not
// exceed what is still owed once the other tenders are counted. A card for
// the whole total plus 1000 in cash would be change out of a card charge:
// the cash covers the change, so only the card rule catches it. It is refused
// 409 card_over_tender and the terminal is never called (no intent, no
// dispatch). Without the rule the sale completes, the card is charged 1000
// more than the sale owes on it and the cash is handed back as change.
func TestCP03_CardOverTenderIsRefusedBeforeTheDevice(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimApprove, 0))
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := f.totalOf(t, saleID)
	r := f.completeSale(saleID, tender("card", total), tender("cash", 1000))
	requireBlocker(t, r, "card_over_tender")
	if n := len(f.terminal.Dispatched()); n != 0 {
		t.Fatalf("the terminal was called %d times for a refused over tender, wanted 0", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM payment_terminal_intents WHERE transaction_id = $1`, saleID); n != 0 {
		t.Fatalf("%d terminal intents for a refused over tender, wanted 0", n)
	}
	var status string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT status FROM pos_transactions WHERE id = $1`, saleID).Scan(&status); err != nil || status != "OPEN" {
		t.Fatalf("the refused sale is %s (%v), wanted OPEN", status, err)
	}
}

// TestCP03_SplitCardAndCashChargesOnlyTheCardLeg: card for part of the total
// and cash for the rest. The device is asked for the card portion alone, and
// the sale keeps one CARD tender and one CASH tender that sum to the total.
func TestCP03_SplitCardAndCashChargesOnlyTheCardLeg(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimApprove, 0))
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := f.totalOf(t, saleID)
	const cardPart = int64(500)
	r := f.completeSale(saleID, tender("card", cardPart), tender("cash", total-cardPart))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	sent := f.terminal.Dispatched()
	if len(sent) != 1 || sent[0].AmountCents != cardPart || sent[0].Kind != payment.TerminalKindSale {
		t.Fatalf("the device was asked for %+v, wanted one sale leg of %d", sent, cardPart)
	}
	if got := countOf(t, f.db,
		`SELECT COALESCE(SUM(ROUND(amount * 100)), 0)::bigint FROM pos_tenders WHERE transaction_id = $1 AND method = 'CARD'`, saleID); got != cardPart {
		t.Fatalf("the CARD tender is %d cents, wanted %d", got, cardPart)
	}
	if got := countOf(t, f.db,
		`SELECT COALESCE(SUM(ROUND(amount * 100)), 0)::bigint FROM pos_tenders WHERE transaction_id = $1`, saleID); got != total {
		t.Fatalf("the tenders sum to %d cents, wanted the total %d", got, total)
	}
}

// TestCP03_DeclinedCardInASplitTakesNoCash: the card half is declined, so the
// sale stays open with no tender rows at all: the cash half is not booked
// behind a card that never paid.
func TestCP03_DeclinedCardInASplitTakesNoCash(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimDecline, 0))
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := f.totalOf(t, saleID)
	r := f.completeSale(saleID, tender("card", 500), tender("cash", total-500))
	requireBlocker(t, r, "card_declined")
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_tenders WHERE transaction_id = $1`, saleID); n != 0 {
		t.Fatalf("%d tender rows on a sale whose card was declined, wanted 0", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM payments WHERE invoice_id IN (SELECT invoice_id FROM pos_transactions WHERE id = $1)`, saleID); n != 0 {
		t.Fatalf("%d payments on a sale whose card was declined, wanted 0", n)
	}
}

// TestCP03_RacingCardCompletesChargeTheCardOnce: pool size 4, three clerks
// complete the same sale with the same card tender at once. The card is
// charged exactly once: one dispatch, one intent, one CARD tender, and one
// completion succeeds. The stored figures are asserted, not a recomputed one.
func TestCP03_RacingCardCompletesChargeTheCardOnce(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), withTerminal(payment.SimApprove, 0))
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := f.totalOf(t, saleID)
	revision := num(t, f.getSale(t, saleID), "revision")
	const contenders = 3
	var wg sync.WaitGroup
	results := make([]resp, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = f.completeSaleWithRevision(saleID, revision, tender("card", total))
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, r := range results {
		if r.status == http.StatusOK {
			ok++
		} else if r.status >= 500 {
			t.Fatalf("a racing completion answered %d: %s", r.status, r.raw)
		}
	}
	if ok != 1 {
		t.Fatalf("%d completions succeeded, wanted exactly 1", ok)
	}
	if n := len(f.terminal.Dispatched()); n != 1 {
		t.Fatalf("the card was dispatched %d times, wanted 1", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM payment_terminal_intents WHERE transaction_id = $1 AND kind = 'sale'`, saleID); n != 1 {
		t.Fatalf("%d sale intents, wanted 1", n)
	}
	if got := countOf(t, f.db,
		`SELECT COALESCE(SUM(ROUND(amount * 100)), 0)::bigint FROM pos_tenders WHERE transaction_id = $1 AND method = 'CARD'`, saleID); got != total {
		t.Fatalf("the stored CARD tender is %d cents, wanted %d once", got, total)
	}
}

// A card under the total beside a few coins whose exact cash leg rounds to
// nothing (CP-18): the pre-check takes the 2 cents exactly, as the transaction
// does, so the sale completes and the card is not called over.
func TestCP03_RoundedCoinsBesideAnUnderCardPass(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimApprove, 0))
	f.noTax()
	f.setRounding("NEAREST_5_CENTS")
	saleID, _ := f.saleAtPrice(103)
	r := f.completeSale(saleID, tender("card", 101), tender("cash", 5))
	if r.status != http.StatusOK {
		t.Fatalf("a card of 101 on a 103 sale with 5 cents cash = %d, wanted 200: %s", r.status, r.raw)
	}
}
