// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The large cash capture, classification and aggregation at the counter
// (CP-17a): the United States Form 8300 aggregation (related transactions,
// the 12 month running total, central across branches, strictly greater
// than the threshold), the Canadian dealer threshold (off by default, a
// static 24 hour window, at or above), the personal check that never
// counts, the payer capture the crossing refuses without, and the sealed
// storage of the sensitive values. The concurrency test runs three
// registers crossing for one payer at once at pool size 4: one event, the
// right total.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// lcUSPayer is the full United States payer capture: every field the rule
// needs, with an invented taxpayer identification number.
func lcUSPayer() map[string]any {
	return map[string]any{
		"name": "Riverbend Builders", "address": "12 Mill Road, Kelowna",
		"occupation": "contractor", "date_of_birth": "1984-05-06",
		"id_type": "drivers_license", "id_number": "DL-00-1234567",
		"taxpayer_id": "00-1234567", "third_party_payer": false,
	}
}

// lcCAPayer is the Canadian payer capture: name, address, date of birth,
// occupation and the ID kind, no taxpayer identification number.
func lcCAPayer() map[string]any {
	return map[string]any{
		"name": "Riverbend Builders", "address": "12 Mill Road, Kelowna",
		"occupation": "contractor", "date_of_birth": "1984-05-06",
		"id_type": "passport",
	}
}

// lcSetCountry sets the fixture branch's country and clears it after.
func lcSetCountry(t *testing.T, f *fixture, country string) {
	t.Helper()
	mustExec(t, f.db, `UPDATE locations SET country_code = $1 WHERE id = $2`, country, f.branchID)
	t.Cleanup(func() {
		mustExec(f.t, f.db, `UPDATE locations SET country_code = NULL WHERE id = $1`, f.branchID)
	})
}

// lcSetCA turns the Canadian dealer threshold on or off with its amount,
// and clears the settings after.
func lcSetCA(t *testing.T, f *fixture, enabled string, thresholdCents int64) {
	t.Helper()
	mustExec(t, f.db, `INSERT INTO system_settings (key, value) VALUES ('largecash.ca.enabled', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, enabled)
	mustExec(t, f.db, `INSERT INTO system_settings (key, value) VALUES ('largecash.ca.threshold_cents', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, thresholdCentsString(thresholdCents))
	t.Cleanup(func() {
		mustExec(f.t, f.db, `DELETE FROM system_settings WHERE key LIKE 'largecash.ca.%'`)
	})
}

func thresholdCentsString(n int64) string { return strconv.FormatInt(n, 10) }

// lcStock lifts the fixture product's on hand so a large sale can complete.
func lcStock(t *testing.T, f *fixture) {
	t.Helper()
	mustExec(t, f.db, `UPDATE inventory SET quantity = 100000 WHERE product_id = $1`, f.productID)
}

// lcTotalCents reads a sale's total in cents.
func lcTotalCents(t *testing.T, f *fixture, saleID string) int64 {
	t.Helper()
	var total int64
	if err := f.db.Pool.QueryRow(ctxBG(), `SELECT ROUND(total * 100)::bigint FROM pos_transactions WHERE id = $1`, saleID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	return total
}

func ctxBG() context.Context { return context.Background() }

// lcComplete completes the sale with the tenders and whatever extra body
// the test names (the payer, the suspected avoidance mark).
func lcComplete(f *fixture, t *testing.T, saleID string, extra map[string]any, tenders ...map[string]any) resp {
	t.Helper()
	cur := f.getSale(t, saleID)
	body := map[string]any{"tenders": tenders, "revision": num(t, cur, "revision")}
	for k, v := range extra {
		body[k] = v
	}
	return f.do("POST", "/api/v1/pos/transactions/"+saleID+"/complete", body,
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
}

// lcBigSale opens a sale of qty of the fixture product and returns its id.
func lcBigSale(f *fixture, t *testing.T, customer *uuid.UUID, qty string) string {
	t.Helper()
	saleID := f.startSale(customer)
	if r := f.addLine(saleID, f.productLine(qty)); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	return saleID
}

// TestCP17_USRelatedTransactionsAggregate: two cash sales inside 24 hours,
// each below the threshold, together over it: the second refuses without
// the payer and writes one reportable event for the pair's total.
func TestCP17_USRelatedTransactionsAggregate(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	first := lcBigSale(f, t, &f.customerID, "1000")
	if r := lcComplete(f, t, first, nil, tender("cash", lcTotalCents(t, f, first))); r.status != http.StatusOK {
		t.Fatalf("the first sale below the threshold = %d: %s", r.status, r.raw)
	}
	second := lcBigSale(f, t, &f.customerID, "1000")
	r := lcComplete(f, t, second, nil, tender("cash", lcTotalCents(t, f, second)))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "payer_details_required") {
		t.Fatalf("the second sale over the threshold together = %d: %s, want 409 payer_details_required", r.status, r.raw)
	}
	r = lcComplete(f, t, second, map[string]any{"payer": lcUSPayer()}, tender("cash", lcTotalCents(t, f, second)))
	if r.status != http.StatusOK {
		t.Fatalf("the second sale with the payer = %d: %s", r.status, r.raw)
	}
	want := lcTotalCents(t, f, first) + lcTotalCents(t, f, second)
	events := countOf(t, f.db, `SELECT count(*) FROM large_cash_events WHERE branch_id = $1`, f.branchID)
	var total int64
	var state, rule string
	if err := f.db.Pool.QueryRow(ctxBG(), `SELECT total_cents, state, rule_family FROM large_cash_events WHERE branch_id = $1`, f.branchID).Scan(&total, &state, &rule); err != nil {
		t.Fatalf("no crossing was recorded: %v", err)
	}
	if events != 1 {
		t.Errorf("events = %d, want 1", events)
	}
	if rule != "US_8300" || state != "reportable" {
		t.Errorf("crossing rule %s state %s, want US_8300 reportable", rule, state)
	}
	if total != want {
		t.Errorf("crossing total = %d, want the pair's %d", total, want)
	}
}

// TestCP17_US12MonthRunning: a counted cash tender inside the 12 month
// window aggregates into this sale's total and the crossing resets the
// counter; a tender older than the window never counts.
func TestCP17_US12MonthRunning(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	// a counted tender a day old, inside the window: the running total a
	// fresh sale aggregates with
	mustExec(t, f.db, `INSERT INTO large_cash_tenders (transaction_id, branch_id, rule_family, payer_key, counted_cents, instrument, reason, tender_index, created_at)
		VALUES ($1, $2, 'US_8300', $3, 700000, 'currency', 'currency', 0, NOW() - INTERVAL '25 hours')`,
		lcDummySale(t, f), f.branchID, "cus:"+f.customerID.String())
	sale := lcBigSale(f, t, &f.customerID, "1000")
	r := lcComplete(f, t, sale, map[string]any{"payer": lcUSPayer()}, tender("cash", lcTotalCents(t, f, sale)))
	if r.status != http.StatusOK {
		t.Fatalf("the crossing with the payer = %d: %s", r.status, r.raw)
	}
	want := lcTotalCents(t, f, sale) + 700000
	var total int64
	if err := f.db.Pool.QueryRow(ctxBG(), `SELECT total_cents FROM large_cash_events WHERE branch_id = $1`, f.branchID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != want {
		t.Errorf("crossing total = %d, want the running %d", total, want)
	}
	// after the crossing the counter resets: the same day's next sale is
	// below the threshold on its own and asks nothing
	next := lcBigSale(f, t, &f.customerID, "1000")
	r = lcComplete(f, t, next, nil, tender("cash", lcTotalCents(t, f, next)))
	if r.status != http.StatusOK {
		t.Fatalf("the sale after the crossing = %d: %s", r.status, r.raw)
	}
	var events int64
	if err := f.db.Pool.QueryRow(ctxBG(), `SELECT count(*) FROM large_cash_events WHERE branch_id = $1`, f.branchID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("events = %d, want 1 (the counter resets at the crossing)", events)
	}
	// a tender older than 12 months never counts: a walk-in payer's sale
	// stays below the threshold with it (the walk-in aggregates under its
	// captured taxpayer identification number's digest)
	mustExec(t, f.db, `INSERT INTO large_cash_tenders (transaction_id, branch_id, rule_family, payer_key, counted_cents, instrument, reason, tender_index, created_at)
		VALUES ($1, $2, 'US_8300', $3, 900000, 'currency', 'currency', 0, NOW() - INTERVAL '370 days')`,
		lcDummySale(t, f), f.branchID, lcTinKey())
	old := lcBigSale(f, t, nil, "1000")
	r = lcComplete(f, t, old, map[string]any{"payer": lcUSPayer()}, tender("cash", lcTotalCents(t, f, old)))
	if r.status != http.StatusOK {
		t.Fatalf("a sale with only a 12 month old tender = %d: %s, want no aggregation across the window", r.status, r.raw)
	}
}

// TestCP17_USAcrossBranches: the United States aggregation is central: the
// same named customer's cash at a second branch crosses on the pair.
func TestCP17_USAcrossBranches(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	branchB, registerB := lcBranchWithRegister(t, f, "LCB", "US")
	first := lcBigSale(f, t, &f.customerID, "1000")
	if r := lcComplete(f, t, first, nil, tender("cash", lcTotalCents(t, f, first))); r.status != http.StatusOK {
		t.Fatalf("the first branch's sale = %d: %s", r.status, r.raw)
	}
	f.ensureTillOn(registerB)
	second := lcStartSaleOn(f, t, registerB, &f.customerID)
	if r := f.addLine(second, f.productLine("1000")); r.status != http.StatusOK {
		t.Fatalf("add line at the second branch = %d: %s", r.status, r.raw)
	}
	r := lcComplete(f, t, second, nil, tender("cash", lcTotalCents(t, f, second)))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "payer_details_required") {
		t.Fatalf("the second branch's sale over the threshold together = %d: %s, want the central aggregation's refusal", r.status, r.raw)
	}
	r = lcComplete(f, t, second, map[string]any{"payer": lcUSPayer()}, tender("cash", lcTotalCents(t, f, second)))
	if r.status != http.StatusOK {
		t.Fatalf("the second branch's sale with the payer = %d: %s", r.status, r.raw)
	}
	var ids []uuid.UUID
	if err := f.db.Pool.QueryRow(ctxBG(),
		`SELECT transaction_ids FROM large_cash_events WHERE customer_id = $1`, f.customerID).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Errorf("the crossing names %d transactions, want the two branches'", len(ids))
	}
	if branchB == f.branchID {
		t.Errorf("the second register was not on its own branch")
	}
}

// TestCP17_PersonalCheckNeverCounts: a personal check never counts even
// for a consumer durable under the avoidance mark; a cashier's check at or
// under the face cap counts for a consumer durable line priced over the
// threshold.
func TestCP17_PersonalCheckNeverCounts(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	mustExec(t, f.db, `UPDATE products SET consumer_durable = TRUE WHERE id = $1`, f.productID)
	personal := lcBigSale(f, t, nil, "2000")
	pr := lcComplete(f, t, personal, nil, map[string]any{"method": "check", "instrument": "personal_check",
		"amount_cents": lcTotalCents(t, f, personal)})
	if pr.status != http.StatusOK {
		t.Fatalf("a personal check over the threshold = %d: %s, want it never to count", pr.status, pr.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM large_cash_tenders WHERE branch_id = $1`, f.branchID); n != 0 {
		t.Errorf("a personal check wrote %d counted tenders, want none", n)
	}
	cashiers := lcBigSale(f, t, nil, "2000")
	// the instrument must sit at or under the face cap to count, so the
	// pair is a cashier's check of 9,000 and the rest in currency
	total := lcTotalCents(t, f, cashiers)
	cr := lcComplete(f, t, cashiers, nil,
		map[string]any{"method": "check", "instrument": "cashiers_check", "amount_cents": 900000},
		tender("cash", total-900000))
	if cr.status != http.StatusConflict || !strings.Contains(string(cr.raw), "payer_details_required") {
		t.Fatalf("a cashier's check for a consumer durable = %d: %s, want the crossing's refusal", cr.status, cr.raw)
	}
	cr = lcComplete(f, t, cashiers, map[string]any{"payer": lcUSPayer()},
		map[string]any{"method": "check", "instrument": "cashiers_check", "amount_cents": 900000},
		tender("cash", total-900000))
	if cr.status != http.StatusOK {
		t.Fatalf("the cashier's check with the payer = %d: %s", cr.status, cr.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM large_cash_events WHERE branch_id = $1`, f.branchID); n != 1 {
		t.Errorf("events = %d, want the cashier's check's single crossing", n)
	}
}

// TestCP17_CanadaOffByDefault: a Canadian branch with the dealer threshold
// off asks nothing and records nothing.
func TestCP17_CanadaOffByDefault(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "CA")
	mustExec(t, f.db, `DELETE FROM system_settings WHERE key LIKE 'largecash.ca.%'`)
	lcStock(t, f)
	sale := lcBigSale(f, t, &f.customerID, "2000")
	r := lcComplete(f, t, sale, nil, tender("cash", lcTotalCents(t, f, sale)))
	if r.status != http.StatusOK {
		t.Fatalf("a Canadian cash sale over the threshold with the dealer threshold off = %d: %s", r.status, r.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM large_cash_events WHERE branch_id = $1`, f.branchID); n != 0 {
		t.Errorf("events = %d, want none with the dealer threshold off", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM large_cash_payers WHERE branch_id = $1`, f.branchID); n != 0 {
		t.Errorf("payer rows = %d, want none asked", n)
	}
}

// TestCP17_CanadaWindowAtOrAbove: with the dealer threshold on, the static
// 24 hour window aggregates the payer's cash business wide at or above the
// dealer's amount; a tender outside the window never counts.
func TestCP17_CanadaWindowAtOrAbove(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "CA")
	lcStock(t, f)
	first := lcBigSale(f, t, &f.customerID, "850")
	t1 := lcTotalCents(t, f, first)
	second := lcBigSale(f, t, &f.customerID, "850")
	t2 := lcTotalCents(t, f, second)
	// the dealer's amount is exactly the pair: at or above crosses
	lcSetCA(t, f, "on", t1+t2)
	// a counted tender outside today's static window never counts
	mustExec(t, f.db, `INSERT INTO large_cash_tenders (transaction_id, branch_id, rule_family, payer_key, counted_cents, instrument, reason, tender_index, created_at)
		VALUES ($1, $2, 'CA_DEALER', $3, 900000, 'currency', 'currency', 0, NOW() - INTERVAL '25 hours')`,
		lcDummySale(t, f), f.branchID, "cus:"+f.customerID.String())
	if r := lcComplete(f, t, first, nil, tender("cash", t1)); r.status != http.StatusOK {
		t.Fatalf("the first sale below the dealer's amount = %d: %s", r.status, r.raw)
	}
	r := lcComplete(f, t, second, nil, tender("cash", t2))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "payer_details_required") {
		t.Fatalf("the second sale at the dealer's amount = %d: %s, want 409 payer_details_required", r.status, r.raw)
	}
	r = lcComplete(f, t, second, map[string]any{"payer": lcCAPayer()}, tender("cash", t2))
	if r.status != http.StatusOK {
		t.Fatalf("the second sale with the payer = %d: %s", r.status, r.raw)
	}
	var total int64
	var state, rule string
	if err := f.db.Pool.QueryRow(ctxBG(), `SELECT total_cents, state, rule_family FROM large_cash_events WHERE branch_id = $1`, f.branchID).Scan(&total, &state, &rule); err != nil {
		t.Fatal(err)
	}
	if rule != "CA_DEALER" || state != "recorded" {
		t.Errorf("crossing rule %s state %s, want CA_DEALER recorded (the dealer's own records, never a FINTRAC report)", rule, state)
	}
	if total != t1+t2 {
		t.Errorf("crossing total = %d, want the window's %d (the yesterday tender outside the window must not count)", total, t1+t2)
	}
}

// TestCP17_PayerDetailsRequired: the completion refuses 409
// payer_details_required without the payer, names the field a partial
// capture misses, and completes with the full capture.
func TestCP17_PayerDetailsRequired(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	sale := lcBigSale(f, t, &f.customerID, "2000")
	r := lcComplete(f, t, sale, nil, tender("cash", lcTotalCents(t, f, sale)))
	if r.status != http.StatusConflict {
		t.Fatalf("a crossing without the payer = %d: %s, want 409", r.status, r.raw)
	}
	code, blockers, _ := errorOf(t, r)
	if code != "conflict" || len(blockers) == 0 || blockers[0] != "payer_details_required" {
		t.Errorf("the refusal's code and blockers = %s %v, want payer_details_required", code, blockers)
	}
	if f.getSale(t, sale)["status"] != "open" {
		t.Errorf("the refused completion left the sale %v, want open with nothing booked", f.getSale(t, sale)["status"])
	}
	partial := lcUSPayer()
	delete(partial, "taxpayer_id")
	r = lcComplete(f, t, sale, map[string]any{"payer": partial}, tender("cash", lcTotalCents(t, f, sale)))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "taxpayer identification number") {
		t.Fatalf("a crossing without the taxpayer identification number = %d: %s, want the refusal naming it", r.status, r.raw)
	}
	r = lcComplete(f, t, sale, map[string]any{"payer": lcUSPayer()}, tender("cash", lcTotalCents(t, f, sale)))
	if r.status != http.StatusOK {
		t.Fatalf("the crossing with the full capture = %d: %s", r.status, r.raw)
	}
}

// TestCP17_TINEncryptedNeverLogged: the taxpayer identification number is
// stored sealed with the payment vault's envelope, and neither the audit
// rows nor the events carry it.
func TestCP17_TINEncryptedNeverLogged(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	sale := lcBigSale(f, t, &f.customerID, "2000")
	r := lcComplete(f, t, sale, map[string]any{"payer": lcUSPayer()}, tender("cash", lcTotalCents(t, f, sale)))
	if r.status != http.StatusOK {
		t.Fatalf("the crossing = %d: %s", r.status, r.raw)
	}
	var tin, idNumber string
	if err := f.db.Pool.QueryRow(ctxBG(), `SELECT tin_sealed, id_number_sealed FROM large_cash_payers WHERE transaction_id = $1`, sale).Scan(&tin, &idNumber); err != nil {
		t.Fatalf("no payer row was written: %v", err)
	}
	if !strings.HasPrefix(tin, "enc:v1:") || !strings.HasPrefix(idNumber, "enc:v1:") {
		t.Fatalf("the sensitive columns hold %q and %q, want the vault's sealed envelope", tin, idNumber)
	}
	if strings.Contains(tin, "1234567") || strings.Contains(idNumber, "1234567") {
		t.Fatalf("a sealed column holds plaintext")
	}
	for name, sql := range map[string]string{
		"the audit rows": `SELECT count(*) FROM audit_log WHERE entity_id::text = $1 AND changes::text LIKE '%1234567%'`,
		"the events":     `SELECT count(*) FROM events_outbox WHERE entity_id::text = $1 AND data::text LIKE '%1234567%'`,
	} {
		if n := countOf(t, f.db, sql, sale); n != 0 {
			t.Errorf("%s carry the taxpayer identification number: %d rows", name, n)
		}
	}
}

// TestCP17_CrossingWritesAnAuditRow: the crossing writes one audit row in the
// completion's transaction naming the rule, the figures and that a payer was
// captured, and the row carries no identity value (ADR 0003).
func TestCP17_CrossingWritesAnAuditRow(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	sale := lcBigSale(f, t, &f.customerID, "2000")
	if r := lcComplete(f, t, sale, map[string]any{"payer": lcUSPayer()}, tender("cash", lcTotalCents(t, f, sale))); r.status != http.StatusOK {
		t.Fatalf("the crossing = %d: %s", r.status, r.raw)
	}
	const q = `SELECT count(*) FROM audit_log WHERE action = 'large_cash.crossing.recorded' AND entity_id::text = $1`
	if n := countOf(t, f.db, q, sale); n != 1 {
		t.Fatalf("crossing audit rows = %d, want 1", n)
	}
	if n := countOf(t, f.db, q+` AND changes->>'rule_family' = 'US_8300' AND (changes->>'payer_captured')::boolean`, sale); n != 1 {
		t.Errorf("the crossing audit row does not name the rule and the capture")
	}
	if n := countOf(t, f.db, q+` AND changes::text LIKE '%1234567%'`, sale); n != 0 {
		t.Errorf("the crossing audit row carries an identity value")
	}
}

// TestCP17_ThreeRegistersOneEvent: three registers complete cash sales for
// one payer at once at pool size 4: every completion lands, exactly one
// event row comes out of it, and its total is the right figure (the first
// two sales' cash, the counter having reset at the crossing).
func TestCP17_ThreeRegistersOneEvent(t *testing.T) {
	lcThreeRegisters(t, true)
}

// TestCP17_ThreeRegistersWalkInOneEvent: the same race for a walk-in payer,
// whose only key is the taxpayer identification number the payer gives. No
// customer row serializes the three sales here, so the payer's advisory lock
// is the only thing that does.
func TestCP17_ThreeRegistersWalkInOneEvent(t *testing.T) {
	lcThreeRegisters(t, false)
}

func lcThreeRegisters(t *testing.T, named bool) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	var customer *uuid.UUID
	if named {
		customer = &f.customerID
	}
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	registers := []string{f.register}
	branches := []uuid.UUID{f.branchID}
	for i := 0; i < 2; i++ {
		br, reg := lcBranchWithRegister(t, f, "LCC"+string(rune('A'+i)), "US")
		branches = append(branches, br)
		registers = append(registers, reg)
	}
	sales := make([]string, 3)
	totals := make([]int64, 3)
	for i, reg := range registers {
		f.ensureTillOn(reg)
		sales[i] = lcStartSaleOn(f, t, reg, customer)
		if r := f.addLine(sales[i], f.productLine("1000")); r.status != http.StatusOK {
			t.Fatalf("add line = %d: %s", r.status, r.raw)
		}
		totals[i] = lcTotalCents(t, f, sales[i])
	}
	var wg sync.WaitGroup
	statuses := make([]int, 3)
	for i := range sales {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = lcComplete(f, t, sales[i], map[string]any{"payer": lcUSPayer()},
				tender("cash", totals[i])).status
		}(i)
	}
	wg.Wait()
	for i, st := range statuses {
		if st != http.StatusOK {
			t.Errorf("contender %d = %d (%s), want 200", i, st, "")
		}
	}
	events := countOf(t, f.db, `SELECT count(*) FROM large_cash_events WHERE branch_id = ANY($1)`, branches)
	if events != 1 {
		t.Errorf("events = %d, want exactly one crossing", events)
	}
	var total int64
	if err := f.db.Pool.QueryRow(ctxBG(), `SELECT total_cents FROM large_cash_events WHERE branch_id = ANY($1)`, branches).Scan(&total); err != nil {
		t.Fatal(err)
	}
	// whichever two committed first crossed: equal sales, so the figure is
	// the same whichever pair it was
	want := totals[0] + totals[1]
	if total != want {
		t.Errorf("the crossing's total = %d, want the first two's %d", total, want)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM large_cash_tenders WHERE branch_id = ANY($1)`, branches); n != 3 {
		t.Errorf("counted tenders = %d, want the three sales'", n)
	}
}

// lcTinKey is the walk-in payer key of the test's taxpayer identification
// number, the same HMAC digest the large cash service derives.
func lcTinKey() string {
	key, _ := hex.DecodeString(lcTestVaultKey)
	m := hmac.New(sha256.New, key)
	m.Write([]byte("tin:00-1234567"))
	return "tin:" + hex.EncodeToString(m.Sum(nil))
}

// lcDummySale makes a bare completed sale row a seeded counted tender can
// hang from; the fixture's cleanup takes it with the rest.
func lcDummySale(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	id := uuid.New()
	mustExec(t, f.db, `INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, status, currency)
		VALUES ($1, $2, $3, $4, 'COMPLETED', 'USD')`, id, f.register, uuid.New(), f.branchID)
	return id
}

// lcBranchWithRegister makes a branch of the country with a yard, stock of
// the fixture product and a register; answers the branch id and register.
func lcBranchWithRegister(t *testing.T, f *fixture, code, country string) (uuid.UUID, string) {
	t.Helper()
	branch := uuid.New()
	yard := uuid.New()
	register := "REG-" + uuid.NewString()[:8]
	mustExec(t, f.db, `INSERT INTO locations (id, type, code, name, country_code, default_tax_rate) VALUES ($1, 'BRANCH', $2, $3, $4, 0.088750)`,
		branch, code+"-BR", code+" branch", country)
	mustExec(t, f.db, `INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`,
		yard, "PY-"+code, branch)
	mustExec(t, f.db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 100000, 0)`,
		f.productID, yard)
	mustExec(t, f.db, `INSERT INTO pos_registers (id, location_id, name, branch_id) VALUES ($1, $2, $3, $4)`,
		register, yard, "LC counter "+code, branch)
	t.Cleanup(func() {
		// the sessions' sales let go of them first: the fixture's own
		// cleanup takes the transactions with its global pass
		mustExec(f.t, f.db, `UPDATE pos_transactions SET till_session_id = NULL, branch_id = $2 WHERE register_id = $1`, register, f.branchID)
		mustExec(f.t, f.db, `UPDATE invoices SET branch_id = $2 WHERE branch_id = $1`, branch, f.branchID)
		mustExec(f.t, f.db, `UPDATE payments SET branch_id = $2 WHERE branch_id = $1`, branch, f.branchID)
		mustExec(f.t, f.db, `UPDATE gl_journal_entries SET branch_id = $2 WHERE branch_id = $1`, branch, f.branchID)
		mustExec(f.t, f.db, `UPDATE gl_journal_lines SET branch_id = $2 WHERE branch_id = $1`, branch, f.branchID)
		mustExec(f.t, f.db, `UPDATE large_cash_tenders SET branch_id = $2 WHERE branch_id = $1`, branch, f.branchID)
		mustExec(f.t, f.db, `UPDATE large_cash_payers SET branch_id = $2 WHERE branch_id = $1`, branch, f.branchID)
		mustExec(f.t, f.db, `UPDATE large_cash_events SET branch_id = $2 WHERE branch_id = $1`, branch, f.branchID)
		mustExec(f.t, f.db, `DELETE FROM till_sessions WHERE register_id = $1`, register)
		mustExec(f.t, f.db, `DELETE FROM pos_registers WHERE id = $1`, register)
		mustExec(f.t, f.db, `DELETE FROM inventory WHERE location_id = $1`, yard)
		mustExec(f.t, f.db, `DELETE FROM locations WHERE id = $1`, yard)
		mustExec(f.t, f.db, `DELETE FROM locations WHERE id = $1`, branch)
	})
	return branch, register
}

// lcStartSaleOn opens a cart on a named register.
func lcStartSaleOn(f *fixture, t *testing.T, register string, customer *uuid.UUID) string {
	t.Helper()
	body := map[string]any{"register_id": register}
	if customer != nil {
		body["customer_id"] = customer.String()
	}
	r := f.do("POST", "/api/v1/pos/transactions", body, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusCreated {
		t.Fatalf("start sale on %s = %d: %s", register, r.status, r.raw)
	}
	return str(t, r.body, "id")
}
