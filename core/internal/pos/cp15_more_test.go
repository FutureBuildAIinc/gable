// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-15 beyond the first red tests: the branch switch's settings round trip
// and the 3 percent cap, a fee that arrives with the switch off, the fee's
// proportional share on a partial card return, a void taking the fee back, and
// two returns of one surcharged sale racing at pool size 4.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

func gatewayRefundTotal(gw *fakeGateway) int64 {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	var n int64
	for _, c := range gw.refundCents {
		n += c
	}
	return n
}

// surchargedFixture builds the fixture with a surcharging terminal and a
// refunding gateway; maxConns > 0 sets the pool size.
func surchargedFixture(t *testing.T, bps int64, on bool, gw *fakeGateway) *fixture {
	t.Helper()
	testutil.LockOutboxTables(t)
	return newFixture(t, testutil.RequireDB(t), withSurchargingTerminal(bps, on), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
}

func (f *fixture) cardReturn(saleID, lineID, qty string) resp {
	return f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "original_sale_id": saleID,
		"refund_method": "card", "reason": "cp15",
		"lines": []map[string]any{{"line_id": lineID, "quantity": qty, "restock": true}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

func (f *fixture) firstLineID(t *testing.T, saleID string) string {
	t.Helper()
	return f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
}

// TestCP15_SettingsRoundTripAndRateCap: an owner switches the active branch's
// card fee on at 3 percent on the revision, the counter reads it back, the
// write is audited with the branch and the rate, a cashier is refused, a
// rate above 300 basis points is refused 422 and nothing is written, and a
// write with no branch in context is refused.
func TestCP15_SettingsRoundTripAndRateCap(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	t.Cleanup(func() { mustExec(t, f.db, `DELETE FROM admin_revisions WHERE resource = 'pos.settings'`) })
	// the fixture runs the single branch way (the branch switch off): the
	// rule speaks for the system's default branch
	var prior string
	hadDefault := f.db.Pool.QueryRow(context.Background(), `SELECT value FROM system_settings WHERE key = 'default_branch_id'`).Scan(&prior) == nil
	t.Cleanup(func() {
		if hadDefault {
			mustExec(t, f.db, `UPDATE system_settings SET value = $1 WHERE key = 'default_branch_id'`, prior)
		} else {
			mustExec(t, f.db, `DELETE FROM system_settings WHERE key = 'default_branch_id'`)
		}
	})
	setDefault := func() {
		mustExec(t, f.db, `INSERT INTO system_settings (key, value, updated_at) VALUES ('default_branch_id', $1, NOW())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, f.branchID.String())
	}
	setDefault()
	branch := "X-Branch-Id"
	get := f.do("GET", "/api/v1/pos/settings", nil, "X-Test-Role", "owner", "X-Test-Sub", mustUUID(t), branch, f.branchID.String())
	if get.status != http.StatusOK {
		t.Fatalf("read = %d: %s", get.status, get.raw)
	}
	cs, _ := get.body["rules"].(map[string]any)["card_surcharge"].(map[string]any)
	if cs == nil || cs["enabled"] != false {
		t.Fatalf("the branch reads card_surcharge %v, wanted off by default", cs)
	}
	rev0 := rev(t, get)
	put := func(role string, rate int, revision int64, extra ...string) resp {
		return f.do("PUT", "/api/v1/pos/settings", map[string]any{"revision": revision, "rules": map[string]any{
			"check_limit_cents": nil, "check_id_required": map[string]any{"mode": "never"},
			"restocking_fees": allReasonFees(0, 0, 0),
			"card_surcharge":  map[string]any{"enabled": true, "rate_bps": rate}}},
			append([]string{"X-Test-Role", role, "X-Test-Sub", mustUUID(t), "If-Match", fmt.Sprintf(`"%d"`, revision)}, extra...)...)
	}
	if r := put("cashier", 300, rev0, branch, f.branchID.String()); r.status != http.StatusForbidden {
		t.Errorf("a cashier switching the fee on = %d, want 403: %s", r.status, r.raw)
	}
	if r := put("owner", 301, rev0, branch, f.branchID.String()); r.status != http.StatusBadRequest {
		t.Fatalf("301 basis points = %d, want 400 validation_failed (the repository's refusal for a bad field): %s", r.status, r.raw)
	}
	if on := f.scalar(`SELECT card_surcharge_enabled::text FROM locations WHERE id = '` + f.branchID.String() + `'`); on != "false" {
		t.Fatalf("a refused rate wrote the switch: %v", on)
	}
	mustExec(t, f.db, `DELETE FROM system_settings WHERE key = 'default_branch_id'`)
	if r := put("owner", 300, rev0); r.status != http.StatusConflict {
		t.Errorf("a write with no branch in context = %d, want 409: %s", r.status, r.raw)
	}
	setDefault()
	ok := put("owner", 300, rev0, branch, f.branchID.String())
	if ok.status != http.StatusOK {
		t.Fatalf("the owner switching it on = %d: %s", ok.status, ok.raw)
	}
	if bps := f.scalar(`SELECT card_surcharge_bps::text FROM locations WHERE id = '` + f.branchID.String() + `'`); bps != "300" {
		t.Errorf("stored rate %v, want 300", bps)
	}
	if put("owner", 300, rev0, branch, f.branchID.String()).status != http.StatusConflict &&
		put("owner", 300, rev0, branch, f.branchID.String()).status != http.StatusPreconditionFailed {
		t.Errorf("a second write on the old revision was not refused")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.settings.saved'
		AND changes->>'card_surcharge_enabled' = 'true' AND changes->>'card_surcharge_bps' = '300'
		AND changes->>'card_surcharge_branch_id' = $1`, f.branchID.String()); n != 1 {
		t.Errorf("%d audit rows name the switch, want 1", n)
	}
	// an omitted card_surcharge leaves the branch's rule alone
	body := map[string]any{"revision": rev(t, ok), "rules": map[string]any{
		"check_limit_cents": nil, "check_id_required": map[string]any{"mode": "never"},
		"restocking_fees": allReasonFees(0, 0, 0)}}
	if r := f.do("PUT", "/api/v1/pos/settings", body, "X-Test-Role", "owner", "X-Test-Sub", mustUUID(t),
		"If-Match", fmt.Sprintf(`"%d"`, rev(t, ok)), branch, f.branchID.String()); r.status != http.StatusOK {
		t.Fatalf("omitting the rule = %d: %s", r.status, r.raw)
	}
	if on := f.scalar(`SELECT card_surcharge_enabled::text FROM locations WHERE id = '` + f.branchID.String() + `'`); on != "true" {
		t.Errorf("a write that omitted card_surcharge turned the switch %v", on)
	}
}

// TestCP15_FeeWithTheSwitchOffIsStoredBookedAndAudited: the processor's
// enrolment ran ahead of the setting. The money moved, so the fee is stored on
// the tender and booked, and an audit row names the sale.
func TestCP15_FeeWithTheSwitchOffIsStoredBookedAndAudited(t *testing.T) {
	f := surchargedFixture(t, 300, false, &fakeGateway{})
	saleID, _ := f.cardSale(t)
	fee := f.cardTenderSurcharge(saleID)
	if fee <= 0 {
		t.Fatalf("no fee stored (%d)", fee)
	}
	if got := f.accountBalance(surchargeIncomeCode); got != -fee {
		t.Fatalf("surcharge income %d, wanted a credit of %d", got, fee)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.surcharge_unexpected' AND entity_id = $1`, saleID); n != 1 {
		t.Fatalf("%d surcharge_unexpected audit rows, want 1", n)
	}
}

// TestCP15_FeeWithTheSwitchOnWritesNoUnexpectedRow: a branch that switched the
// fee on has no surcharge_unexpected row.
func TestCP15_FeeWithTheSwitchOnWritesNoUnexpectedRow(t *testing.T) {
	f := surchargedFixture(t, 300, true, &fakeGateway{})
	saleID, _ := f.cardSale(t)
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.surcharge_unexpected' AND entity_id = $1`, saleID); n != 0 {
		t.Fatalf("%d surcharge_unexpected rows on a branch that switched it on", n)
	}
}

// TestCP15_ADebitOrWaivedAnswerStoresNoFee: an answer with fee zero (a debit
// card, or a waived fee) stores zero and books nothing to the income account.
func TestCP15_ADebitOrWaivedAnswerStoresNoFee(t *testing.T) {
	f := surchargedFixture(t, 0, true, &fakeGateway{})
	saleID, _ := f.cardSale(t)
	if got := f.cardTenderSurcharge(saleID); got != 0 {
		t.Fatalf("stored fee %d, wanted 0", got)
	}
	if got := f.accountBalance(surchargeIncomeCode); got != 0 {
		t.Fatalf("surcharge income %d, wanted 0", got)
	}
}

// TestCP15_APartialReturnRefundsTheFeeInProportion: two card returns of one
// unit each from a two unit sale return the fee in proportion, the last one
// takes the remainder, and the fees returned sum to the fee charged exactly.
func TestCP15_APartialReturnRefundsTheFeeInProportion(t *testing.T) {
	gw := &fakeGateway{}
	f := surchargedFixture(t, 300, true, gw)
	saleID, total := f.cardSale(t)
	fee := f.cardTenderSurcharge(saleID)
	line := f.firstLineID(t, saleID)
	if r := f.cardReturn(saleID, line, "1"); r.status != http.StatusCreated {
		t.Fatalf("first return = %d: %s", r.status, r.raw)
	}
	gw.mu.Lock()
	first := gw.refundCents[0]
	gw.mu.Unlock()
	if first <= 0 || first >= total+fee {
		t.Fatalf("the first refund %d is not a part of %d", first, total+fee)
	}
	if got := countOf(t, f.db, `SELECT COALESCE(SUM(fee_cents), 0)::bigint FROM pos_refund_intents WHERE sale_id = $1`, saleID); got <= 0 || got >= fee {
		t.Fatalf("the first return returned a fee of %d of %d, wanted a part", got, fee)
	}
	if r := f.cardReturn(saleID, line, "1"); r.status != http.StatusCreated {
		t.Fatalf("second return = %d: %s", r.status, r.raw)
	}
	if got := gatewayRefundTotal(gw); got != total+fee {
		t.Fatalf("the processor was asked for %d in all, wanted %d (goods %d plus fee %d)", got, total+fee, total, fee)
	}
	if got := countOf(t, f.db, `SELECT COALESCE(SUM(fee_cents), 0)::bigint FROM pos_refund_intents WHERE sale_id = $1`, saleID); got != fee {
		t.Fatalf("the fees returned sum to %d, wanted exactly %d", got, fee)
	}
	if got := f.accountBalance(surchargeIncomeCode); got != 0 {
		t.Fatalf("surcharge income %d after both returns, wanted 0", got)
	}
}

// TestCP15_AVoidTakesTheFeeBack: voiding the surcharged card sale refunds the
// goods and the fee together and the income account nets to zero.
func TestCP15_AVoidTakesTheFeeBack(t *testing.T) {
	gw := &fakeGateway{}
	f := surchargedFixture(t, 300, true, gw)
	saleID, total := f.cardSale(t)
	fee := f.cardTenderSurcharge(saleID)
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "rung in error", "revision": num(t, f.getSale(t, saleID), "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("void = %d: %s", r.status, r.raw)
	}
	if got := gatewayRefundTotal(gw); got != total+fee {
		t.Fatalf("the void refunded %d, wanted %d", got, total+fee)
	}
	if got := f.accountBalance(surchargeIncomeCode); got != 0 {
		t.Fatalf("surcharge income %d after the void, wanted 0", got)
	}
}

// TestCP15_TwoReturnsOfOneSurchargedSaleRefundTheFeeOnce: three contenders
// return the same two units at once at pool size 4; one wins, the fee is
// returned once in total, and the processor is asked once.
func TestCP15_TwoReturnsOfOneSurchargedSaleRefundTheFeeOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), withSurchargingTerminal(300, true), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, total := f.cardSale(t)
	fee := f.cardTenderSurcharge(saleID)
	line := f.firstLineID(t, saleID)
	var wg sync.WaitGroup
	results := make([]resp, 3)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = f.cardReturn(saleID, line, "2")
		}()
	}
	wg.Wait()
	created := 0
	for _, r := range results {
		if r.status == http.StatusCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("%d of 3 contenders returned the sale, want exactly 1: %v %v %v", created, string(results[0].raw), string(results[1].raw), string(results[2].raw))
	}
	if got := gatewayRefundTotal(gw); got != total+fee {
		t.Fatalf("the processor was asked for %d, wanted %d once", got, total+fee)
	}
	if got := countOf(t, f.db, `SELECT COALESCE(SUM(fee_cents), 0)::bigint FROM pos_refund_intents WHERE sale_id = $1 AND state IN ('PENDING','DONE')`, saleID); got != fee {
		t.Fatalf("the stored fee refunded is %d, wanted %d", got, fee)
	}
	if got := f.accountBalance(surchargeIncomeCode); got != 0 {
		t.Fatalf("surcharge income %d, wanted 0", got)
	}
}

// TestCP15_TheDemoSimulatorSurchargesOnlyWhileTheSwitchIsOn: the demo stack's
// simulated processor adds the branch's rate to an approved card answer while
// the switch is on and nothing once it is off, so the counter shows a fee on a
// demo stack with no real processor.
func TestCP15_TheDemoSimulatorSurchargesOnlyWhileTheSwitchIsOn(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		ledger := payment.NewTerminalLedger(f.db)
		f.terminal = payment.NewSimulatedTerminal(payment.SimApprove, payment.NewSimulatedSurchargeSink(ledger))
		f.service.WithTerminal(f.terminal, ledger)
	})
	f.setBranchSurcharge(true, 300)
	onSale, total := f.cardSale(t)
	if got, want := f.cardTenderSurcharge(onSale), (total*300+5000)/10000; got != want {
		t.Fatalf("with the switch on the demo fee is %d, wanted %d", got, want)
	}
	f.setBranchSurcharge(false, 300)
	offSale, _ := f.cardSale(t)
	if got := f.cardTenderSurcharge(offSale); got != 0 {
		t.Fatalf("with the switch off the demo fee is %d, wanted 0", got)
	}
}
