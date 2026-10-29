// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The branch's walk-in identity floor (CP-17a, item 296): the amount over
// which the counter asks a walk-in who paid cash to identify themselves, so
// the walk-in's cash sales are counted together. The default is 3,000 dollars
// on a United States branch and off on a Canadian one. Over the floor the
// counter asks before Pay, the preview and the completion key the same
// identified person, and that person's sales aggregate. With the floor off
// nothing aggregates and the counter says so. A walk-in who declines to
// identify over the floor lets the sale go ahead and lands in the worklist as
// an identity declined; a decline never answers the 10,000 dollar crossing
// (item 299), which refuses until the payer's details are given.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// lcWalkInFloorCents is the United States default floor, 3,000 dollars.
const lcWalkInFloorCents = 300_000

// lcSetFloor sets the fixture branch's walk-in identity floor and puts the
// branch country's default back after the test. A nil floor is the country
// default.
func lcSetFloor(t *testing.T, f *fixture, cents *int64) {
	t.Helper()
	mustExec(t, f.db, `UPDATE locations SET walk_in_identity_cents = $1 WHERE id = $2`, cents, f.branchID)
	t.Cleanup(func() {
		mustExec(f.t, f.db, `UPDATE locations SET walk_in_identity_cents = NULL WHERE id = $1`, f.branchID)
	})
}

// lcFloorOff is the branch's floor off.
func lcFloorOff() *int64 {
	off := int64(0)
	return &off
}

// lcFloorPreview previews the branch's large cash rule for a sale with one
// cash tender, with whatever extra the test names (the captured payer).
func lcFloorPreview(t *testing.T, f *fixture, saleID string, cents int64, extra map[string]any) resp {
	t.Helper()
	body := map[string]any{
		"transaction_id": saleID,
		"tenders":        []map[string]any{{"method": "cash", "amount_cents": cents}},
	}
	for k, v := range extra {
		body[k] = v
	}
	return f.do("POST", "/api/v1/pos/large-cash/preview", body, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
}

// TestCP17_WalkInFloorAsksAndAggregates: three walk-in cash sales of 5,988.13
// on a United States branch. The first one passes the branch's floor, so the
// preview asks for the walk-in's identity before Pay. With the identity
// captured, the preview and the completion key the same person: the second
// sale's preview counts the walk-in's earlier cash and crosses the threshold,
// so the crossing is captured for the pair, and the third sale starts a new
// running period.
func TestCP17_WalkInFloorAsksAndAggregates(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcSetFloor(t, f, nil)
	lcStock(t, f)
	f.openTill(0)

	sales := make([]string, 3)
	totals := make([]int64, 3)
	for i := range sales {
		sales[i] = lcBigSale(f, t, nil, "1000")
		totals[i] = lcTotalCents(t, f, sales[i])
	}

	// the first preview asks: the sale passes the floor and nobody is
	// identified yet
	preview := lcFloorPreview(t, f, sales[0], totals[0], nil)
	if preview.status != http.StatusOK {
		t.Fatalf("the preview = %d: %s", preview.status, preview.raw)
	}
	if got := num(t, preview.body, "walk_in_floor_cents"); got != lcWalkInFloorCents {
		t.Errorf("the preview's floor = %d, want the United States default %d", got, lcWalkInFloorCents)
	}
	if preview.body["asks_floor"] != true {
		t.Errorf("the preview's answer = %v, want it to ask for the walk-in's identity", preview.body)
	}
	if preview.body["asks_details"] != true {
		t.Errorf("the preview's answer = %v, want it to ask for the payer's details", preview.body)
	}
	if preview.body["walk_in_aggregated"] != true {
		t.Errorf("the preview's answer = %v, want the branch to aggregate the walk-in's cash", preview.body)
	}

	// the first sale is answered with the walk-in's identity
	r := lcComplete(f, t, sales[0], map[string]any{"payer": lcUSPayer()}, tender("cash", totals[0]))
	if r.status != http.StatusOK {
		t.Fatalf("the first sale with the payer = %d: %s", r.status, r.raw)
	}

	// the second preview carries the identity the till session holds, so it
	// keys the same person the completion will: the walk-in's earlier cash
	// and this sale come to more than the threshold
	preview = lcFloorPreview(t, f, sales[1], totals[1], map[string]any{"payer": lcUSPayer()})
	if preview.status != http.StatusOK {
		t.Fatalf("the second preview = %d: %s", preview.status, preview.raw)
	}
	if got := num(t, preview.body, "aggregate_cents"); got != totals[0]+totals[1] {
		t.Errorf("the preview's aggregate = %d, want the walk-in's earlier cash and this sale %d", got, totals[0]+totals[1])
	}
	if preview.body["asks_details"] != true {
		t.Errorf("the preview's answer = %v, want it to ask for the payer's details at the crossing", preview.body)
	}
	if preview.body["threshold_crossed"] != true {
		t.Errorf("the preview's answer = %v, want the crossing named", preview.body)
	}
	r = lcComplete(f, t, sales[1], map[string]any{"payer": lcUSPayer()}, tender("cash", totals[1]))
	if r.status != http.StatusOK {
		t.Fatalf("the second sale with the payer = %d: %s", r.status, r.raw)
	}

	// the third sale rides the same identity; the counter reset at the
	// crossing, so this sale is on its own again
	preview = lcFloorPreview(t, f, sales[2], totals[2], map[string]any{"payer": lcUSPayer()})
	if preview.status != http.StatusOK {
		t.Fatalf("the third preview = %d: %s", preview.status, preview.raw)
	}
	if got := num(t, preview.body, "aggregate_cents"); got != totals[2] {
		t.Errorf("the third preview's aggregate = %d, want the sale on its own %d (the counter resets at the crossing)", got, totals[2])
	}
	if preview.body["asks_floor"] != false {
		t.Errorf("the third preview's answer = %v, want the identity already held", preview.body)
	}
	r = lcComplete(f, t, sales[2], map[string]any{"payer": lcUSPayer()}, tender("cash", totals[2]))
	if r.status != http.StatusOK {
		t.Fatalf("the third sale with the payer = %d: %s", r.status, r.raw)
	}

	events := countOf(t, f.db, `SELECT count(*) FROM large_cash_events`)
	if events != 1 {
		t.Fatalf("events = %d, want exactly the crossing of the second sale", events)
	}
	var total int64
	var state, rule, key string
	var ids []uuid.UUID
	if err := f.db.Pool.QueryRow(ctxBG(),
		`SELECT total_cents, state, rule_family, payer_key, transaction_ids FROM large_cash_events`).
		Scan(&total, &state, &rule, &key, &ids); err != nil {
		t.Fatal(err)
	}
	if total != totals[0]+totals[1] {
		t.Errorf("the crossing's total = %d, want the pair's %d", total, totals[0]+totals[1])
	}
	if state != "reportable" || rule != "US_8300" {
		t.Errorf("the crossing's rule %s state %s, want US_8300 reportable", rule, state)
	}
	if key != lcTinKey() {
		t.Errorf("the crossing's payer key = %s, want the identified walk-in's %s", key, lcTinKey())
	}
	if len(ids) != 2 {
		t.Errorf("the crossing names %d transactions, want the pair's", len(ids))
	}
}

// TestCP17_WalkInFloorOffAggregatesNothing: with the branch's floor off the
// counter asks nothing, the screen says the walk-in's cash is not aggregated,
// and no two of the walk-in's cash sales are counted together.
func TestCP17_WalkInFloorOffAggregatesNothing(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	f.openTill(0)
	lcSetFloor(t, f, lcFloorOff())

	sale := lcBigSale(f, t, nil, "1000")
	total := lcTotalCents(t, f, sale)
	preview := lcFloorPreview(t, f, sale, total, nil)
	if preview.status != http.StatusOK {
		t.Fatalf("the preview = %d: %s", preview.status, preview.raw)
	}
	if got := num(t, preview.body, "walk_in_floor_cents"); got != 0 {
		t.Errorf("the preview's floor = %d, want the floor off", got)
	}
	if preview.body["asks_floor"] != false || preview.body["asks_details"] != false {
		t.Errorf("the preview's answer = %v, want it to ask for nothing", preview.body)
	}
	if preview.body["walk_in_aggregated"] != false {
		t.Errorf("the preview's answer = %v, want the branch not to aggregate the walk-in's cash", preview.body)
	}
	note, _ := preview.body["walk_in_note"].(string)
	if !strings.Contains(note, "not aggregated") {
		t.Errorf("the preview's note = %q, want it to say the walk-in's cash is not aggregated", note)
	}

	// three walk-in cash sales, none of them identified: they never count
	// together and nothing is recorded
	for i := 0; i < 3; i++ {
		s := lcBigSale(f, t, nil, "1000")
		r := lcComplete(f, t, s, nil, tender("cash", lcTotalCents(t, f, s)))
		if r.status != http.StatusOK {
			t.Fatalf("the walk-in sale %d with the floor off = %d: %s", i+1, r.status, r.raw)
		}
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM large_cash_events`); n != 0 {
		t.Errorf("events = %d, want none with the floor off", n)
	}
	if n := countOf(t, f.db, `SELECT count(DISTINCT payer_key) FROM large_cash_tenders`); n != 3 {
		t.Errorf("the counted tenders carry %d payers, want each walk-in on their own", n)
	}
}

// TestCP17_WalkInFloorRefusesWithoutAnAnswer: a walk-in's cash sale over the
// floor is refused when the counter has neither captured the payer nor
// recorded that the walk-in declined: the counter answers before Pay.
func TestCP17_WalkInFloorRefusesWithoutAnAnswer(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	f.openTill(0)

	sale := lcBigSale(f, t, nil, "1000")
	r := lcComplete(f, t, sale, nil, tender("cash", lcTotalCents(t, f, sale)))
	if r.status != http.StatusConflict {
		t.Fatalf("a walk-in's cash sale over the floor without an answer = %d: %s, want 409", r.status, r.raw)
	}
	code, blockers, _ := errorOf(t, r)
	if code != "conflict" || len(blockers) == 0 || blockers[0] != "walk_in_identity_required" {
		t.Errorf("the refusal's code and blockers = %s %v, want walk_in_identity_required", code, blockers)
	}
	if f.getSale(t, sale)["status"] != "open" {
		t.Errorf("the refused completion left the sale %v, want open with nothing booked", f.getSale(t, sale)["status"])
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM large_cash_tenders`); n != 0 {
		t.Errorf("the refused completion wrote %d counted tenders, want none", n)
	}
}

// TestCP17_WalkInIdentityDeclinedIsRecorded: a walk-in who declines to
// identify over the floor lets the sale go ahead, and the fact lands in the
// worklist as an identity declined for a manager to review.
func TestCP17_WalkInIdentityDeclinedIsRecorded(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	f.openTill(0)

	sale := lcBigSale(f, t, nil, "1000")
	total := lcTotalCents(t, f, sale)
	r := lcComplete(f, t, sale, map[string]any{"payer_declined": true}, tender("cash", total))
	if r.status != http.StatusOK {
		t.Fatalf("the sale with a declined identity = %d: %s, want it to go ahead", r.status, r.raw)
	}
	var state string
	var amount int64
	if err := f.db.Pool.QueryRow(ctxBG(),
		`SELECT state, amount_cents FROM large_cash_events WHERE branch_id = $1`, f.branchID).Scan(&state, &amount); err != nil {
		t.Fatalf("the declined identity was not recorded for the worklist: %v", err)
	}
	if state != "identity declined" {
		t.Errorf("the worklist row's state = %q, want identity declined", state)
	}
	if amount != total {
		t.Errorf("the worklist row's amount = %d, want the sale's %d", amount, total)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM large_cash_payers`); n != 0 {
		t.Errorf("payer rows = %d, want none for a declined identity", n)
	}
}

// TestCP17_CrossingRefusesADecline: a decline answers the branch's walk-in
// floor only, never the 10,000 dollar crossing (item 299). A walk-in's cash
// sale that crosses the threshold is refused with the payer's details
// required even when the counter records that the walk-in declined to
// identify: nothing is written, and the sale completes once the payer's
// details are given. A sale over the floor that does not cross still takes
// the decline, goes ahead and lands in the worklist as an identity declined.
func TestCP17_CrossingRefusesADecline(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcSetFloor(t, f, nil)
	lcStock(t, f)
	f.openTill(0)

	// a walk-in cash sale of 17,964.38: over the branch's 3,000 floor and
	// over the 10,000 threshold in one sale
	sale := lcBigSale(f, t, nil, "3000")
	total := lcTotalCents(t, f, sale)
	if total <= 1_000_000 {
		t.Fatalf("the fixture sale's total = %d, want it over the 10,000 dollar threshold", total)
	}
	r := lcComplete(f, t, sale, map[string]any{"payer_declined": true}, tender("cash", total))
	if r.status != http.StatusConflict {
		t.Fatalf("the crossing with a declined identity = %d: %s, want 409: a decline does not answer the crossing", r.status, r.raw)
	}
	code, blockers, _ := errorOf(t, r)
	if code != "conflict" || len(blockers) == 0 || blockers[0] != "payer_details_required" {
		t.Errorf("the refusal's code and blockers = %s %v, want payer_details_required", code, blockers)
	}
	// the refusal wrote nothing: the sale is still open, and no capture, no
	// counted tender, no payer row and no identity declined row exists
	if f.getSale(t, sale)["status"] != "open" {
		t.Errorf("the refused completion left the sale %v, want open with nothing booked", f.getSale(t, sale)["status"])
	}
	for _, table := range []string{"large_cash_events", "large_cash_tenders", "large_cash_payers"} {
		if n := countOf(t, f.db, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("the refused crossing wrote %d rows into %s, want none", n, table)
		}
	}

	// the sale completes once the payer's details are given
	r = lcComplete(f, t, sale, map[string]any{"payer": lcUSPayer()}, tender("cash", total))
	if r.status != http.StatusOK {
		t.Fatalf("the crossing with the payer = %d: %s", r.status, r.raw)
	}
	var state string
	var amount int64
	if err := f.db.Pool.QueryRow(ctxBG(),
		`SELECT state, amount_cents FROM large_cash_events WHERE branch_id = $1`, f.branchID).Scan(&state, &amount); err != nil {
		t.Fatal(err)
	}
	if state != "reportable" || amount != total {
		t.Errorf("the crossing's state %s amount %d, want reportable for the sale's %d", state, amount, total)
	}

	// a sale over the floor that does not cross still takes the decline: the
	// sale goes ahead and the worklist holds the identity declined
	floorSale := lcBigSale(f, t, nil, "1000")
	floorTotal := lcTotalCents(t, f, floorSale)
	if floorTotal <= lcWalkInFloorCents || floorTotal > 1_000_000 {
		t.Fatalf("the fixture sale's total = %d, want it over the floor and under the threshold", floorTotal)
	}
	r = lcComplete(f, t, floorSale, map[string]any{"payer_declined": true}, tender("cash", floorTotal))
	if r.status != http.StatusOK {
		t.Fatalf("the floor's sale with a declined identity = %d: %s, want it to go ahead", r.status, r.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM large_cash_events WHERE state = 'identity declined'`); n != 1 {
		t.Fatalf("identity declined rows = %d, want the floor's sale", n)
	}
	var declined int64
	if err := f.db.Pool.QueryRow(ctxBG(),
		`SELECT amount_cents FROM large_cash_events WHERE state = 'identity declined'`).Scan(&declined); err != nil {
		t.Fatal(err)
	}
	if declined != floorTotal {
		t.Errorf("the identity declined row's amount = %d, want the sale's %d", declined, floorTotal)
	}
}

// TestCP17_WalkInFloorFollowsTheBranchCountry: the floor's default follows
// the branch's country: 3,000 dollars on a United States branch, off on a
// Canadian one, and a branch's own amount overrides the default.
func TestCP17_WalkInFloorFollowsTheBranchCountry(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcStock(t, f)
	f.openTill(0)

	// Canada: the floor is off by default, so a walk-in's cash sale over the
	// amount asks nothing
	lcSetCountry(t, f, "CA")
	canada := lcBigSale(f, t, nil, "1000")
	preview := lcFloorPreview(t, f, canada, lcTotalCents(t, f, canada), nil)
	if preview.status != http.StatusOK {
		t.Fatalf("the Canadian preview = %d: %s", preview.status, preview.raw)
	}
	if got := num(t, preview.body, "walk_in_floor_cents"); got != 0 {
		t.Errorf("the Canadian floor = %d, want off by default", got)
	}
	if preview.body["asks_floor"] != false {
		t.Errorf("the Canadian preview's answer = %v, want it to ask for nothing", preview.body)
	}

	// the branch's own amount overrides the country default
	own := int64(500000)
	lcSetFloor(t, f, &own)
	lcSetCountry(t, f, "US")
	usa := lcBigSale(f, t, nil, "1000")
	preview = lcFloorPreview(t, f, usa, lcTotalCents(t, f, usa), nil)
	if preview.status != http.StatusOK {
		t.Fatalf("the preview = %d: %s", preview.status, preview.raw)
	}
	if got := num(t, preview.body, "walk_in_floor_cents"); got != 500000 {
		t.Errorf("the branch's floor = %d, want its own 500000", got)
	}
}
