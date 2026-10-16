// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The split void's interim invoice (the fourteenth review's P2-1): while a claim of
// the sale's void still stands, the booked cents show as owed on the
// statement, but nothing may be applied to that invoice: an application
// would leave a live row the void's completion cannot reverse, stranding the
// last claim and the drawer. The apply routes refuse with 409
// void_claim_pending naming the standing claim, and the void always
// completes. A declined tender's credit is refused one guard earlier (409
// tender_credit_held, its own hold, before any invoice is read), so the
// named customer's receipt is the shape that pins the invoice's own
// refusal; the walk-in test below names its guard.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// invoiceOf reads the sale's invoice id.
func (f *fixture) invoiceOf(t *testing.T, saleID string) string {
	t.Helper()
	var id string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT invoice_id::text FROM pos_transactions WHERE id = $1`, saleID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// namedTwoCardSaleOf makes a named customer's one line sale paid by two cards.
func (f *fixture) namedTwoCardSaleOf(t *testing.T, cents1, cents2 int64) (string, resp) {
	t.Helper()
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, withToken(tender("card", cents1)), withToken(tender("card", cents2)))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	return saleID, r
}

// paymentOfTender reads the tender's payment id and revision.
func (f *fixture) paymentOfTender(t *testing.T, tenderID string) (string, int64) {
	t.Helper()
	var id string
	var rev int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT p.id::text, p.revision FROM pos_tenders d JOIN payments p ON p.id = d.payment_id WHERE d.id = $1`,
		tenderID).Scan(&id, &rev); err != nil {
		t.Fatal(err)
	}
	return id, rev
}

// RULE: the customer's own receipt applied to the interim invoice (the AR
// desk's check) is refused with 409 void_claim_pending naming the standing
// claim; the last claim then resolves either way, the void completes and the
// drawer closes.
func TestTheCustomersOwnReceiptCannotApplyToAnInterimVoidInvoice(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundErrs: []error{nil, errDeadline()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	till := f.openTill(10000)
	saleID, body := f.namedTwoCardSaleOf(t, 300, 299)
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "stranded", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusInternalServerError {
		t.Fatalf("the split void = %d, want 500: %s", r.status, r.raw)
	}
	// resolve the refunded tender: the sale stands completed with its invoice
	// open the refunded cents (the interim), the other claim still standing
	var standing, refunded map[string]any
	for _, in := range f.saleIntents(t, saleID) {
		if in["state"] == "PENDING" && in["refund_tx_id"] != "" {
			refunded = in
		}
		if in["state"] == "PENDING" && in["refund_tx_id"] == "" {
			standing = in
		}
	}
	if refunded == nil || standing == nil {
		t.Fatalf("the claims = %v, want one refunded PENDING and one unanswered PENDING", f.saleIntents(t, saleID))
	}
	if r := f.resolveOn(t, refunded["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": refunded["refund_tx_id"].(string), "reason": "terminal log shows it",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the refund resolution = %d: %s", r.status, r.raw)
	}
	owed := refunded["amount"].(int64)
	if got := f.balance(); got != owed {
		t.Fatalf("the interim balance = %d, want %d owed (the booked cents reopened the invoice)", got, owed)
	}
	f.assertARInvariants(t)

	// the customer pays the open cents by check at the AR desk, applied to
	// that invoice: refused, the void must stay completable
	r := f.do("POST", "/api/v1/payments", map[string]any{
		"customer_id": f.customerID, "method": "check", "amount_cents": owed,
		"reference": "CHK-72", "received_on": "2020-01-01",
		"applications": []map[string]any{{"invoice_id": f.invoiceOf(t, saleID), "amount_cents": owed}},
	}, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("the receipt applied to the interim invoice = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "void_claim_pending" {
		t.Errorf("blockers = %v, want void_claim_pending (the standing claim holds the invoice)", blockers)
	}
	if !containsClaim(r.raw, standing["id"].(string)) {
		t.Errorf("the refusal does not name the standing claim %s: %s", standing["id"], r.raw)
	}
	// nothing of the refused receipt landed
	if got := f.balance(); got != owed {
		t.Errorf("the balance after the refused receipt = %d, want %d (nothing applied)", got, owed)
	}
	f.assertARInvariants(t)

	// the void completes from the last claim, either way
	if r := f.resolveOn(t, standing["id"].(string), map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-72", "reason": "terminal log empty",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the last claim's resolution = %d: %s", r.status, r.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided (the void is always completable)", got)
	}
	if got := f.balance(); got != 0 {
		t.Errorf("the balance after the void = %d, want 0", got)
	}
	if r := f.do("POST", "/api/v1/pos/till/"+till+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 10000}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("the close = %d: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}

// RULE: another walk-in sale's declined card credit applied to the interim
// invoice through the payment apply route is refused the same way; the
// credit stays unapplied on its own payment, payable back to its card, and
// both sales and the drawer settle.
func TestAnotherWalkInCreditCannotApplyToAnInterimVoidInvoice(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge(), approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined},
		refundErrs:     []error{nil, nil, nil, errDeadline()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.paymentGateway = gw
	})
	till := f.openTill(10000)
	// S1: the void refunds one card and declines the other; resolving the
	// refunded card completes the void and leaves the declined card's charge
	// as unapplied credit on the walk-in customer
	s1, body1, _ := f.twoCardSaleOf(t, "1", 300, 299)
	if r := f.do("POST", "/api/v1/pos/transactions/"+s1+"/void",
		map[string]any{"reason": "declined card", "revision": rev(t, body1)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusConflict {
		t.Fatalf("S1's void = %d, want 409: %s", r.status, r.raw)
	}
	var standing1, declined1 map[string]any
	for _, in := range f.saleIntents(t, s1) {
		if in["state"] == "PENDING" {
			standing1 = in
		}
		if in["state"] == "FAILED" {
			declined1 = in
		}
	}
	if r := f.resolveOn(t, standing1["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": standing1["refund_tx_id"].(string), "reason": "terminal log shows it",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("S1's resolution = %d: %s", r.status, r.raw)
	}
	credit := f.unappliedOf(t, declined1["tender_id"].(string))
	if credit != declined1["amount"].(int64) {
		t.Fatalf("S1's declined card credit = %d, want the declined tender's own %d", credit, declined1["amount"].(int64))
	}

	// S2: the void refunds one card and loses the other's verdict; resolving
	// the refunded card opens S2's interim invoice
	s2, body2, _ := f.twoCardSaleOf(t, "1", 300, 299)
	if r := f.do("POST", "/api/v1/pos/transactions/"+s2+"/void",
		map[string]any{"reason": "stranded", "revision": rev(t, body2)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusInternalServerError {
		t.Fatalf("S2's void = %d, want 500: %s", r.status, r.raw)
	}
	var standing2, refunded2 map[string]any
	for _, in := range f.saleIntents(t, s2) {
		if in["state"] == "PENDING" && in["refund_tx_id"] != "" {
			refunded2 = in
		}
		if in["state"] == "PENDING" && in["refund_tx_id"] == "" {
			standing2 = in
		}
	}
	if r := f.resolveOn(t, refunded2["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": refunded2["refund_tx_id"].(string), "reason": "terminal log shows it",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("S2's refund resolution = %d: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)

	// S1's credit applied to S2's interim invoice: refused, and by the
	// payment's own hold first (FROM-PRINCIPAL 60a): the credit is held to
	// its tender before any invoice is even read, so the refusal names the
	// origin (S1's released claim), never S2's standing one. The interim
	// invoice's own guard (void_claim_pending) is never reached for this
	// shape: it is pinned by the named customer's receipt test above, whose
	// check is no held credit and so reaches the invoice's standing claims
	payID, payRev := f.paymentOfTender(t, declined1["tender_id"].(string))
	r := f.do("POST", "/api/v1/payments/"+payID+"/applications", map[string]any{
		"revision": payRev,
		"applications": []map[string]any{
			{"invoice_id": f.invoiceOf(t, s2), "amount_cents": credit},
		},
	}, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("the credit applied to the interim invoice = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "tender_credit_held" {
		t.Errorf("blockers = %v, want tender_credit_held (the credit's own hold answers before the interim invoice's guard is read)", blockers)
	}
	if !containsClaim(r.raw, declined1["id"].(string)) {
		t.Errorf("the refusal does not name the declined claim %s: %s", declined1["id"], r.raw)
	}
	// the credit is still on its own payment, and its one way back is the
	// counter terminal rail: the payback route pays it to the card while
	// S2's claim still stands (the credit belongs to S1's completed void,
	// not to the invoice it might have been spent on)
	if got := f.unappliedOf(t, declined1["tender_id"].(string)); got != credit {
		t.Errorf("S1's credit after the refused apply = %d, want %d (nothing was absorbed)", got, credit)
	}
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+declined1["id"].(string)+"/payback",
		map[string]any{"reason": "the declined tender's credit back to its card"},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("the credit's payback to its card = %d, want 200: %s", r.status, r.raw)
	}
	if got := f.unappliedOf(t, declined1["tender_id"].(string)); got != 0 {
		t.Errorf("S1's credit after the payback = %d, want 0", got)
	}
	f.assertARInvariants(t)

	// S2 completes and the drawer closes
	if r := f.resolveOn(t, standing2["id"].(string), map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-73", "reason": "terminal log empty",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("S2's last resolution = %d: %s", r.status, r.raw)
	}
	if got := str(t, f.getSale(t, s2), "status"); got != "voided" {
		t.Errorf("S2 status = %q, want voided", got)
	}
	if r := f.do("POST", "/api/v1/pos/till/"+till+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 10000}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("the close = %d: %s", r.status, r.raw)
	}
	if got := f.walkInBalance(t); got != 0 {
		t.Errorf("walk-in balance = %d, want 0", got)
	}
	f.assertARInvariants(t)
}

// errDeadline is the terminal call's lost verdict.
func errDeadline() error { return context.DeadlineExceeded }

// containsClaim answers whether the refusal body names the claim id.
func containsClaim(raw []byte, claimID string) bool {
	return strings.Contains(string(raw), claimID)
}
