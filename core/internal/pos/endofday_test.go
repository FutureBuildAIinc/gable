// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-09: the end of day by branch. A summary of each register's sessions for a
// business date, a close that is refused while a till is open or waiting for
// sign off and happens once per branch per date, and the deposit matched to a
// bank statement line.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// dayBranch is a branch of its own with two registers, so a summary never
// reads another test's rows.
type dayBranch struct {
	id        uuid.UUID
	yard      uuid.UUID
	registers [2]string
}

// dayFixture is a fixture plus the extra branches a test made.
type dayFixture struct {
	*fixture
	db       *database.DB
	branches []*dayBranch
	bankIDs  []uuid.UUID
	accounts []uuid.UUID
}

// newDayFixture builds the fixture. The cleanup of the extra branches is
// registered first, so it runs after the fixture's own (cleanups run last in,
// first out) once no session or sale names a register of theirs.
func newDayFixture(t *testing.T, db *database.DB, opts ...func(*fixture)) *dayFixture {
	t.Helper()
	d := &dayFixture{db: db}
	t.Cleanup(d.removeBranches)
	d.fixture = newFixture(t, db, opts...)
	return d
}

func (d *dayFixture) removeBranches() {
	ctx := context.Background()
	exec := func(sql string, args ...any) { _, _ = d.db.Pool.Exec(ctx, sql, args...) }
	for _, id := range d.bankIDs {
		exec(`DELETE FROM bank_transactions WHERE id = $1`, id)
	}
	for _, id := range d.accounts {
		exec(`DELETE FROM bank_accounts WHERE id = $1`, id)
	}
	for _, b := range d.branches {
		exec(`DELETE FROM pos_registers WHERE branch_id = $1`, b.id)
		exec(`DELETE FROM inventory WHERE location_id = $1`, b.yard)
		exec(`DELETE FROM locations WHERE id = $1`, b.yard)
		exec(`DELETE FROM locations WHERE id = $1`, b.id)
	}
}

// makeBranch adds a branch with a stocked yard and two registers.
func (d *dayFixture) makeBranch() *dayBranch {
	d.t.Helper()
	b := &dayBranch{id: uuid.New(), yard: uuid.New()}
	mustExec(d.t, d.db, `INSERT INTO locations (id, type, code, name, timezone, default_tax_rate) VALUES ($1, 'BRANCH', $2, 'day branch', 'America/New_York', 0.088750)`,
		b.id, "DB-"+b.id.String()[:8])
	mustExec(d.t, d.db, `INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`,
		b.yard, "DY-"+b.yard.String()[:8], b.id)
	mustExec(d.t, d.db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 1000, 0)`, d.productID, b.yard)
	for i := range b.registers {
		b.registers[i] = "DR-" + uuid.NewString()[:8]
		mustExec(d.t, d.db, `INSERT INTO pos_registers (id, location_id, name, branch_id) VALUES ($1, $2, 'day register', $3)`, b.registers[i], b.yard, b.id)
	}
	d.branches = append(d.branches, b)
	return b
}

// today is the branch's business date now.
func (d *dayFixture) today(b *dayBranch) string {
	d.t.Helper()
	var day string
	if err := d.db.Pool.QueryRow(context.Background(), `SELECT (NOW() AT TIME ZONE 'America/New_York')::date::text`).Scan(&day); err != nil {
		d.t.Fatal(err)
	}
	return day
}

// sell makes a one unit sale on the register with the tenders.
func (d *dayFixture) sell(register string, tenders ...map[string]any) {
	d.t.Helper()
	saleID := d.startSaleOn(register)
	if r := d.addLine(saleID, d.productLine("1")); r.status != http.StatusOK {
		d.t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := d.completeSaleOn(register, saleID, tenders...); r.status != http.StatusOK {
		d.t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
}

// count closes a session counting the methods given.
func (d *dayFixture) count(sessionID string, counted map[string]any) resp {
	d.t.Helper()
	return d.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": counted},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(d.t))
}

func (d *dayFixture) summary(b *dayBranch, date string) resp {
	d.t.Helper()
	return d.do("GET", fmt.Sprintf("/api/v1/pos/end-of-day?branch_id=%s&date=%s", b.id, date), nil,
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(d.t))
}

func (d *dayFixture) closeDay(b *dayBranch, date, slip string) resp {
	d.t.Helper()
	return d.do("POST", "/api/v1/pos/end-of-day/close", map[string]any{"branch_id": b.id.String(), "date": date, "deposit_slip_ref": slip},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(d.t))
}

func obj(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	o, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T (%v), want an object", key, m[key], m[key])
	}
	return o
}

// RULE: the branch summary for two registers on one date adds up to the cent,
// and a second branch's tills never appear in it.
func TestTheBranchSummaryAddsUpToTheCent(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDB(t))
	a, other := d.makeBranch(), d.makeBranch()
	r1, r2 := a.registers[0], a.registers[1]

	// register 1: a 10000 float, a cash and a card sale, a 500 drop to the safe
	s1 := d.openTillOn(r1, 10000)
	d.sell(r1, tender("cash", 599))
	d.sell(r1, tender("card", 599))
	d.mustMovement(s1, map[string]any{"kind": "safe_drop", "amount_cents": 500}, mustUUID(t))
	// register 2: a 5000 float, two cash sales and a check sale, 100 paid out
	s2 := d.openTillOn(r2, 5000)
	d.sell(r2, tender("cash", 599))
	d.sell(r2, tender("cash", 599))
	d.sell(r2, tender("check", 599))
	d.mustMovement(s2, map[string]any{"kind": "paid_out", "amount_cents": 100, "reason_code": "postage"}, mustUUID(t))
	// the other branch sells too, and its drawer stays open
	o1 := d.openTillOn(other.registers[0], 7000)
	d.sell(other.registers[0], tender("cash", 599))
	_ = o1

	// register 1 expects 10000+599-500 = 10099 and counts it; register 2
	// expects 5000+1198-100 = 6098 and counts 6048, short 50, with its check
	if r := d.count(s1, map[string]any{"cash": 10099}); r.status != http.StatusOK {
		t.Fatalf("close 1 = %d: %s", r.status, r.raw)
	}
	if r := d.count(s2, map[string]any{"cash": 6048, "check": 599}); r.status != http.StatusOK {
		t.Fatalf("close 2 = %d: %s", r.status, r.raw)
	}
	day := d.today(a)
	r := d.summary(a, day)
	if r.status != http.StatusOK {
		t.Fatalf("summary = %d: %s", r.status, r.raw)
	}
	regs := r.body["registers"].([]any)
	if len(regs) != 2 {
		t.Fatalf("%d registers in the summary, want the 2 of the branch (the other branch's never appear): %s", len(regs), r.raw)
	}
	byReg := map[string]map[string]any{}
	for _, g := range regs {
		m := g.(map[string]any)
		byReg[m["register_id"].(string)] = m
	}
	want := func(name string, m map[string]any, key string, wants map[string]int64) {
		got := obj(t, m, key)
		for k, w := range wants {
			if num(t, got, k) != w {
				t.Errorf("%s %s[%s] = %v, want %d", name, key, k, got[k], w)
			}
		}
		if len(got) != len(wants) {
			t.Errorf("%s %s = %v, want exactly %v", name, key, got, wants)
		}
	}
	reg1, reg2 := byReg[r1], byReg[r2]
	want("register 1", reg1, "tendered_by_method", map[string]int64{"CASH": 599, "CARD": 599})
	want("register 1", reg1, "movement_totals_by_kind", map[string]int64{"safe_drop": 500})
	if num(t, reg1, "cash_to_deposit_cents") != 599 || num(t, reg1, "card_total_cents") != 599 || num(t, reg1, "over_short_cents") != 0 {
		t.Errorf("register 1 figures = %v, want deposit 599, card 599, over/short 0", reg1)
	}
	want("register 2", reg2, "tendered_by_method", map[string]int64{"CASH": 1198, "CHECK": 599})
	want("register 2", reg2, "movement_totals_by_kind", map[string]int64{"paid_out": 100})
	if num(t, reg2, "cash_to_deposit_cents") != 1048 || num(t, reg2, "checks_to_deposit_cents") != 599 || num(t, reg2, "over_short_cents") != -50 {
		t.Errorf("register 2 figures = %v, want cash 1048, checks 599, over/short -50", reg2)
	}
	totals := obj(t, r.body, "totals")
	want("branch", totals, "tendered_by_method", map[string]int64{"CASH": 1797, "CARD": 599, "CHECK": 599})
	want("branch", totals, "movement_totals_by_kind", map[string]int64{"safe_drop": 500, "paid_out": 100})
	want("branch", totals, "expected_by_method", map[string]int64{"CASH": 10099 + 6098, "CARD": 599, "CHECK": 599})
	want("branch", totals, "counted_by_method", map[string]int64{"CASH": 10099 + 6048, "CHECK": 599})
	for key, w := range map[string]int64{"session_count": 2, "sale_count": 5, "sales_total_cents": 5 * 599, "over_short_cents": -50,
		"cash_to_deposit_cents": 599 + 1048, "checks_to_deposit_cents": 599, "card_total_cents": 599} {
		if got := num(t, totals, key); got != w {
			t.Errorf("branch %s = %d, want %d", key, got, w)
		}
	}
	// the branch is the sum of its registers, field by field, to the cent
	for _, key := range []string{"sale_count", "sales_total_cents", "over_short_cents", "cash_to_deposit_cents", "checks_to_deposit_cents", "card_total_cents"} {
		if num(t, totals, key) != num(t, reg1, key)+num(t, reg2, key) {
			t.Errorf("branch %s is not the sum of its registers", key)
		}
	}
	// and it ties to the Daily Till's net collections for the branch and date
	if got := num(t, r.body, "collections_net_cents"); got != 5*599 {
		t.Errorf("collections_net_cents = %d, want %d", got, 5*599)
	}
	if r.body["ready_to_close"] != true || num(t, r.body, "open_session_count") != 0 || r.body["close"] != nil {
		t.Errorf("a day with every drawer counted: %v, want ready and not closed", r.body)
	}

	// the other branch: its own drawer, open, only its own sale
	or := d.summary(other, d.today(other))
	if or.status != http.StatusOK {
		t.Fatalf("other summary = %d: %s", or.status, or.raw)
	}
	ototals := obj(t, or.body, "totals")
	if num(t, ototals, "sales_total_cents") != 599 || or.body["ready_to_close"] != false || num(t, or.body, "open_session_count") != 1 {
		t.Errorf("other branch summary = %s, want its one 599 sale and an open drawer", or.raw)
	}
	// a session opened another business date is not in today's summary
	mustExec(t, d.db, `UPDATE till_sessions SET opened_at = opened_at - interval '1 day' WHERE id = $1`, s1)
	if got := len(d.summary(a, day).body["registers"].([]any)); got != 1 {
		t.Errorf("%d registers after one session moved to yesterday, want 1", got)
	}
	var yesterday string
	if err := d.db.Pool.QueryRow(contextB(), `SELECT ($1::date - 1)::text`, day).Scan(&yesterday); err != nil {
		t.Fatal(err)
	}
	ys := d.summary(a, yesterday)
	if got := len(ys.body["registers"].([]any)); got != 1 || num(t, obj(t, ys.body, "totals"), "sales_total_cents") != 2*599 {
		t.Errorf("yesterday's summary = %s, want register 1's two sales", ys.raw)
	}
	// reads validate their names
	for name, path := range map[string]string{
		"no branch":  "/api/v1/pos/end-of-day",
		"a bad date": "/api/v1/pos/end-of-day?branch_id=" + a.id.String() + "&date=17-10-2026",
		"an unknown": "/api/v1/pos/end-of-day?branch_id=" + a.id.String() + "&x=1",
	} {
		if r := d.do("GET", path, nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, r.status, r.raw)
		}
	}
	if r := d.do("GET", "/api/v1/pos/end-of-day?branch_id="+uuid.NewString(), nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusNotFound {
		t.Errorf("an unknown branch = %d, want 404", r.status)
	}
}

// RULE: the day close is refused while any till of the branch is open or
// waiting for sign off, accepted after, once per branch per date, and a closed
// day takes no new drawer.
func TestTheDayCloseIsRefusedWithATillOpenAndAcceptedAfter(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDB(t))
	a := d.makeBranch()
	day := d.today(a)
	s1 := d.openTillOn(a.registers[0], 1000)
	s2 := d.openTillOn(a.registers[1], 1000)
	d.sell(a.registers[0], tender("cash", 599))

	blockers := func(r resp) []string {
		_, b, _ := errorOf(t, r)
		return b
	}
	r := d.closeDay(a, day, "SLIP-1")
	if r.status != http.StatusConflict || len(blockers(r)) != 1 || blockers(r)[0] != "till_open" {
		t.Fatalf("close with two drawers open = %d %v, want 409 till_open: %s", r.status, blockers(r), r.raw)
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM till_day_closes WHERE branch_id = $1`, a.id); got != 0 {
		t.Errorf("%d closes written by a refused act", got)
	}
	// register 2 counts but far off the threshold: it waits for a manager
	d.do("PUT", "/api/v1/pos/till/policy", map[string]any{"branch_id": a.id.String(), "variance_threshold_cents": 100},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r := d.count(s2, map[string]any{"cash": 400}); r.status != http.StatusOK || sessionOf(t, r)["status"] != "pending_signoff" {
		t.Fatalf("close 2 = %d: %s, want pending_signoff", r.status, r.raw)
	}
	if r := d.count(s1, map[string]any{"cash": 1599}); r.status != http.StatusOK {
		t.Fatalf("close 1 = %d: %s", r.status, r.raw)
	}
	r = d.closeDay(a, day, "SLIP-1")
	if r.status != http.StatusConflict || len(blockers(r)) != 1 || blockers(r)[0] != "signoff_pending" {
		t.Fatalf("close with a drawer waiting = %d %v, want 409 signoff_pending: %s", r.status, blockers(r), r.raw)
	}
	if s := d.signOff(s2, "short banked in the other bag", "finance", mustUUID(t)); s.status != http.StatusOK {
		t.Fatalf("sign off = %d: %s", s.status, s.raw)
	}
	// refusals that are not about drawers
	for name, c := range map[string]struct {
		body map[string]any
		role string
		want int
	}{
		"no slip":        {map[string]any{"branch_id": a.id.String(), "date": day}, "finance", http.StatusBadRequest},
		"a blank slip":   {map[string]any{"branch_id": a.id.String(), "date": day, "deposit_slip_ref": "  "}, "finance", http.StatusBadRequest},
		"no date":        {map[string]any{"branch_id": a.id.String(), "deposit_slip_ref": "S"}, "finance", http.StatusBadRequest},
		"a cashier":      {map[string]any{"branch_id": a.id.String(), "date": day, "deposit_slip_ref": "S"}, "cashier", http.StatusForbidden},
		"a future date":  {map[string]any{"branch_id": a.id.String(), "date": "2999-01-01", "deposit_slip_ref": "S"}, "finance", http.StatusConflict},
		"no such branch": {map[string]any{"branch_id": uuid.NewString(), "date": day, "deposit_slip_ref": "S"}, "finance", http.StatusNotFound},
	} {
		if r := d.do("POST", "/api/v1/pos/end-of-day/close", c.body, "X-Test-Role", c.role, "X-Test-Sub", mustUUID(t)); r.status != c.want {
			t.Errorf("%s = %d, want %d: %s", name, r.status, c.want, r.raw)
		}
	}
	closer := mustUUID(t)
	r = d.do("POST", "/api/v1/pos/end-of-day/close", map[string]any{"branch_id": a.id.String(), "date": day, "deposit_slip_ref": " SLIP-1 "},
		"X-Test-Role", "finance", "X-Test-Sub", closer)
	if r.status != http.StatusCreated {
		t.Fatalf("close after every drawer settled = %d: %s", r.status, r.raw)
	}
	c := obj(t, r.body, "close")
	if c["closed_by"] != closer || c["deposit_slip_ref"] != "SLIP-1" || c["business_date"] != day || c["closed_at"] == nil || c["deposit_match"] != nil {
		t.Errorf("the close record = %v, want who, when, the trimmed slip, the date, no match yet", c)
	}
	// 1599-1000 = 599 from register 1, 400-1000+0 = -600 from register 2 (its count short 600, signed off)
	if num(t, c, "cash_to_deposit_cents") != 599-600 {
		t.Errorf("cash_to_deposit_cents = %v, want -1", c["cash_to_deposit_cents"])
	}
	snap := obj(t, c, "totals")
	if num(t, obj(t, snap, "totals"), "sales_total_cents") != 599 || snap["close"] != nil {
		t.Errorf("the totals snapshot = %v, want the day's figures without the close", snap)
	}
	// the day reads back closed, with the same record
	if rd := d.summary(a, day); obj(t, rd.body, "close")["id"] != c["id"] {
		t.Errorf("the summary's close = %v, want %v", rd.body["close"], c["id"])
	}
	// once per branch per date
	again := d.closeDay(a, day, "SLIP-2")
	if again.status != http.StatusConflict || len(blockers(again)) != 1 || blockers(again)[0] != "day_already_closed" {
		t.Errorf("a second close = %d %v, want 409 day_already_closed", again.status, blockers(again))
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM till_day_closes WHERE branch_id = $1`, a.id); got != 1 {
		t.Errorf("%d closes for the branch day, want 1", got)
	}
	// a closed day takes no new drawer, and another branch is unaffected
	r = d.do("POST", "/api/v1/pos/till/open", map[string]any{"register_id": a.registers[0], "opening_float_cents": 0},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict || len(blockers(r)) != 1 || blockers(r)[0] != "day_closed" {
		t.Errorf("open a drawer on a closed day = %d %v, want 409 day_closed", r.status, blockers(r))
	}
	if o := d.makeBranch(); d.openTillOn(o.registers[0], 0) == "" {
		t.Errorf("another branch could not open a drawer")
	}
	// the close is audited and evented
	if got := countOf(t, d.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.day.closed' AND entity_id = $1::uuid AND user_id = $2`, str(t, c, "id"), closer); got != 1 {
		t.Errorf("%d audit rows for the close by the closer, want 1", got)
	}
	if got := eventTypes(t, d.db, "till_day_close", str(t, c, "id")); len(got) != 1 || got[0] != "till.day_closed" {
		t.Errorf("close events = %v, want till.day_closed", got)
	}
}

// bankLine adds a statement line to a bank account of the test and answers its id.
func (d *dayFixture) bankLine(amount int64, date string) uuid.UUID {
	d.t.Helper()
	var account uuid.UUID
	if len(d.accounts) == 0 {
		account = uuid.New()
		mustExec(d.t, d.db, `INSERT INTO bank_accounts (id, name, gl_account_id) VALUES ($1, 'deposit test', (SELECT id FROM gl_accounts WHERE code = '1010'))`, account)
		d.accounts = append(d.accounts, account)
	}
	account = d.accounts[0]
	id := uuid.New()
	mustExec(d.t, d.db, `INSERT INTO bank_transactions (id, bank_account_id, transaction_date, amount, description, status)
		VALUES ($1, $2, $3::date, $4, 'BRANCH DEPOSIT', 'UNMATCHED')`, id, account, date, amount)
	d.bankIDs = append(d.bankIDs, id)
	return id
}

// closedDay settles a branch with one cash sale of 599 and a 1000 float
// counted exactly, closes its day, and answers the close id and deposit.
func (d *dayFixture) closedDay(b *dayBranch) (closeID string, deposit int64) {
	d.t.Helper()
	s := d.openTillOn(b.registers[0], 1000)
	d.sell(b.registers[0], tender("cash", 599))
	if r := d.count(s, map[string]any{"cash": 1599}); r.status != http.StatusOK {
		d.t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	r := d.closeDay(b, d.today(b), "SLIP-"+b.id.String()[:6])
	if r.status != http.StatusCreated {
		d.t.Fatalf("day close = %d: %s", r.status, r.raw)
	}
	c := obj(d.t, r.body, "close")
	return str(d.t, c, "id"), num(d.t, c, "cash_to_deposit_cents") + num(d.t, c, "checks_to_deposit_cents")
}

// RULE: the deposit match records the matched statement line, the difference
// in cents between the bank's amount and the slip's, and who matched it; a
// line is taken once, only a free deposit can be taken, and a day matches once.
func TestTheDepositMatchRecordsTheDifference(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDB(t))
	a, b := d.makeBranch(), d.makeBranch()
	closeA, deposit := d.closedDay(a)
	closeB, _ := d.closedDay(b)
	if deposit != 599 {
		t.Fatalf("deposit = %d, want 599", deposit)
	}
	day := d.today(a)
	near := d.bankLine(604, day)   // five cents over the slip
	far := d.bankLine(90000, day)  // a different deposit
	debit := d.bankLine(-599, day) // not a deposit
	tooOld := d.bankLine(599, "2000-01-01")
	_ = tooOld

	cands := d.do("GET", "/api/v1/pos/end-of-day/"+closeA+"/deposit-candidates", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if cands.status != http.StatusOK {
		t.Fatalf("candidates = %d: %s", cands.status, cands.raw)
	}
	items := cands.body["items"].([]any)
	if len(items) < 2 || items[0].(map[string]any)["bank_transaction_id"] != near.String() {
		t.Fatalf("candidates = %s, want the nearest amount first", cands.raw)
	}
	for _, it := range items {
		id := it.(map[string]any)["bank_transaction_id"]
		if id == debit.String() || id == tooOld.String() {
			t.Errorf("candidate %v is a debit or far outside the day", id)
		}
	}
	match := func(closeID string, line uuid.UUID, role, sub string) resp {
		return d.do("POST", "/api/v1/pos/end-of-day/"+closeID+"/deposit-match", map[string]any{"bank_transaction_id": line.String()},
			"X-Test-Role", role, "X-Test-Sub", sub)
	}
	if r := match(closeA, debit, "finance", mustUUID(t)); r.status != http.StatusConflict {
		t.Errorf("matching a debit = %d, want 409: %s", r.status, r.raw)
	}
	if r := match(closeA, uuid.New(), "finance", mustUUID(t)); r.status != http.StatusNotFound {
		t.Errorf("matching an unknown line = %d, want 404: %s", r.status, r.raw)
	}
	if r := match(closeA, near, "cashier", mustUUID(t)); r.status != http.StatusForbidden {
		t.Errorf("matching as a cashier = %d, want 403", r.status)
	}
	matcher := mustUUID(t)
	r := match(closeA, near, "finance", matcher)
	if r.status != http.StatusOK {
		t.Fatalf("match = %d: %s", r.status, r.raw)
	}
	m := obj(t, r.body, "deposit_match")
	if m["bank_transaction_id"] != near.String() || num(t, m, "bank_amount_cents") != 604 || num(t, m, "difference_cents") != 5 ||
		m["matched_by"] != matcher || m["matched_at"] == nil || m["bank_transaction_date"] != day {
		t.Errorf("deposit_match = %v, want the line, 604, a difference of 5, who and when", m)
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM bank_transactions WHERE id = $1 AND status = 'MATCHED'`, near); got != 1 {
		t.Errorf("the statement line is not claimed in bankrecon")
	}
	// the summary reads the match back
	if rd := d.summary(a, day); obj(t, obj(t, rd.body, "close"), "deposit_match")["bank_transaction_id"] != near.String() {
		t.Errorf("the summary does not carry the match: %s", rd.raw)
	}
	// a day matches once, and a line is taken once
	if r := match(closeA, far, "finance", mustUUID(t)); r.status != http.StatusConflict {
		t.Errorf("a second match of the day = %d, want 409: %s", r.status, r.raw)
	} else if _, bl, _ := errorOf(t, r); len(bl) != 1 || bl[0] != "deposit_already_matched" {
		t.Errorf("blockers = %v, want deposit_already_matched", bl)
	}
	if r := match(closeB, near, "finance", mustUUID(t)); r.status != http.StatusConflict {
		t.Errorf("another day taking the same line = %d, want 409: %s", r.status, r.raw)
	} else if _, bl, _ := errorOf(t, r); len(bl) != 1 || bl[0] != "bank_line_unavailable" {
		t.Errorf("blockers = %v, want bank_line_unavailable", bl)
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM till_day_closes WHERE branch_id = $1 AND bank_transaction_id IS NULL`, b.id); got != 1 {
		t.Errorf("the refused match changed the other day")
	}
	// an under deposit records a negative difference
	under := d.bankLine(590, day)
	if r := match(closeB, under, "finance", mustUUID(t)); r.status != http.StatusOK || num(t, obj(t, r.body, "deposit_match"), "difference_cents") != -9 {
		t.Errorf("the under deposit = %d: %s, want a difference of -9", r.status, r.raw)
	}
	if got := eventTypes(t, d.db, "till_day_close", closeA); len(got) != 2 || got[1] != "till.deposit_matched" {
		t.Errorf("events of the matched day = %v, want till.day_closed then till.deposit_matched", got)
	}
}

// RULE (concurrency, pool size 4, three contenders): three closes of one day
// racing end with exactly one close; and a drawer opening races the close so
// that a closed day never holds an open drawer.
func TestRacingDayClosesEndWithOne(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDBMaxConns(t, 4))
	for round := 0; round < 3; round++ {
		a := d.makeBranch()
		day := d.today(a)
		s := d.openTillOn(a.registers[0], 0)
		if r := d.count(s, map[string]any{"cash": 0}); r.status != http.StatusOK {
			t.Fatalf("close = %d: %s", r.status, r.raw)
		}
		var wg sync.WaitGroup
		statuses := make([]int, 3)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				statuses[i] = d.closeDay(a, day, fmt.Sprintf("RACE-%d", i)).status
			}(i)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[2] = d.do("POST", "/api/v1/pos/till/open", map[string]any{"register_id": a.registers[1], "opening_float_cents": 0},
				"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)).status
		}()
		wg.Wait()
		closes := countOf(t, d.db, `SELECT count(*) FROM till_day_closes WHERE branch_id = $1`, a.id)
		open := countOf(t, d.db, `SELECT count(*) FROM till_sessions WHERE branch_id = $1 AND status = 'OPEN'`, a.id)
		if closes > 1 {
			t.Fatalf("round %d: %d closes of one branch day, want at most 1 (statuses %v)", round, closes, statuses)
		}
		if closes == 1 && open != 0 {
			t.Errorf("round %d: the day is closed and %d drawer(s) are open (statuses %v)", round, open, statuses)
		}
		wins := 0
		for _, st := range statuses[:2] {
			if st == http.StatusCreated {
				wins++
			} else if st != http.StatusConflict {
				t.Errorf("round %d: a close answered %d, want 201 or 409", round, st)
			}
		}
		if int64(wins) != closes {
			t.Errorf("round %d: %d closes answered 201 but %d rows exist (statuses %v)", round, wins, closes, statuses)
		}
		if statuses[2] != http.StatusCreated && statuses[2] != http.StatusConflict {
			t.Errorf("round %d: the open answered %d, want 201 or 409", round, statuses[2])
		}
	}
}
