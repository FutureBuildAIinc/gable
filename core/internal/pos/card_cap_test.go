// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The card cap and the legacy tax reads, each with a test that fails under
// the sign mutants (seventh review P2-4 and P3-1): a split cash and card sale
// whose card returns are booked negative, a legacy card return booked
// positive that carries cents past its quantity, and a legacy return that
// carries tax. Every refund act asserts the ledger ties after it.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// splitSale sells two units and tenders cashCents in cash and the rest on the
// card, returning the sale, its first line and the card tender's cents.
func splitSale(t *testing.T, f *fixture, cashCents int64) (saleID, lineID string, cardCents int64) {
	t.Helper()
	saleID = f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, saleID), "total_cents")
	cardCents = total - cashCents
	if r := f.completeSale(saleID, tender("cash", cashCents), withToken(tender("card", cardCents))); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID = f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	return saleID, lineID, cardCents
}

// RULE (seventh review P2-4): the card returns the sale already booked (each
// negative) bound the next one at the card tender less them. A split sale of
// 500 cash and the rest on the card: one unit back to the card fits, the
// second does not (99 left on the card), and the terminal is asked once.
func TestASplitSaleCardReturnsAreBoundedByTheCardTender(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, lineID, cardCents := splitSale(t, f, 500)
	if cardCents != 698 {
		t.Fatalf("the card tender = %d, want 698 (1198 less 500 cash)", cardCents)
	}
	f.assertARInvariants(t)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusCreated {
		t.Fatalf("the first card return = %d: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
	r := f.cardReturnOn(t, saleID, lineID, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("the second card return = %d, want 409 (99 left on the card): %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "exceeds_card_tender" {
		t.Errorf("blockers = %v, want exceeds_card_tender", blockers)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 1 {
		t.Errorf("%d gateway refunds, want 1 (the refused return never reached the terminal)", refunds)
	}
	f.assertARInvariants(t)
}

// RULE (seventh review P2-4): a legacy card return (positive in the shape
// migration 105 leaves) that carries cents past its quantity lowers what the
// card tender has left by those cents. A split sale of 800 cash and 398 on
// the card, a legacy card return of a tenth of a unit carrying 350: 48 left
// on the card, so a card return of one unit (599) is refused by the card cap
// alone (the quantity and the line's money both allow it).
func TestALegacyCardReturnCarryingCentsBoundsTheCardTender(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, lineID, cardCents := splitSale(t, f, 800)
	if cardCents != 398 {
		t.Fatalf("the card tender = %d, want 398 (1198 less 800 cash)", cardCents)
	}
	legacyReturnOf(t, f, saleID, "0.1", "5.50", "3.50", "3.50", "0.00", "3.50", "CARD")
	f.assertARInvariants(t)
	r := f.cardReturnOn(t, saleID, lineID, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("the card return past the card tender = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "exceeds_card_tender" {
		t.Errorf("blockers = %v, want exceeds_card_tender (the legacy 350 left 48 on the card)", blockers)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 0 {
		t.Errorf("%d gateway refunds, want 0", refunds)
	}
	f.assertARInvariants(t)
}

// RULE (seventh review P3-1): the tax a legacy return took back (positive in
// the shape migration 105 leaves) counts against the sale's tax. A legacy
// return of a tenth of a unit carrying 40 of the sale's 49 cents of tax
// leaves 9: a return of half a unit, whose share is 25, takes 9 and no more.
func TestALegacyReturnsTaxCountsAgainstTheSalesTax(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 599)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	legacyReturnOf(t, f, saleID, "0.1", "5.50", "0.55", "0.55", "0.40", "0.95", "CASH")
	f.assertARInvariants(t)
	r := f.returnOn(t, saleID, lineID, "0.5")
	if r.status != http.StatusCreated {
		t.Fatalf("the half return = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "tax_cents"); got != -9 {
		t.Errorf("the half return tax = %d, want -9 (49 less the legacy 40, under the 25 share)", got)
	}
	if got := num(t, r.body, "total_cents"); got != -284 {
		t.Errorf("the half return total = %d, want -284 (275 of line and 9 of tax)", got)
	}
	if got := f.accountBalance("1010"); got != 315 {
		t.Errorf("cash balance = %d, want 315 (the sale in, the linked refund out; the legacy cents never left this drawer)", got)
	}
	f.assertARInvariants(t)
}
