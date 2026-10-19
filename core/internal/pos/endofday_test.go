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
	"strings"
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

// dayAfter is the business day n days from a day, as YYYY-MM-DD.
func (d *dayFixture) dayAfter(day string, n int) string {
	d.t.Helper()
	var out string
	if err := d.db.Pool.QueryRow(context.Background(), `SELECT ($1::date + $2::int)::text`, day, n).Scan(&out); err != nil {
		d.t.Fatal(err)
	}
	return out
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
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(d.t))
}

// closeDay closes the branch's day; the bank account the deposit goes to is
// named when one is given.
func (d *dayFixture) closeDay(b *dayBranch, date, slip string, bank ...uuid.UUID) resp {
	d.t.Helper()
	body := map[string]any{"branch_id": b.id.String(), "date": date, "deposit_slip_ref": slip}
	if len(bank) > 0 {
		body["bank_account_id"] = bank[0].String()
	}
	return d.do("POST", "/api/v1/pos/end-of-day/close", body, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(d.t))
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
	// the end of day is the managers': a cashier reads none
	if r := d.do("GET", "/api/v1/pos/end-of-day?branch_id="+a.id.String(), nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusForbidden {
		t.Errorf("a cashier reading the summary = %d, want 403", r.status)
	}
	// reads validate their names
	for name, path := range map[string]string{
		"no branch":  "/api/v1/pos/end-of-day",
		"a bad date": "/api/v1/pos/end-of-day?branch_id=" + a.id.String() + "&date=tomorrow",
		"an unknown": "/api/v1/pos/end-of-day?branch_id=" + a.id.String() + "&x=1",
	} {
		if r := d.do("GET", path, nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, r.status, r.raw)
		}
	}
	if r := d.do("GET", "/api/v1/pos/end-of-day?branch_id="+uuid.NewString(), nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusNotFound {
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
		"a future date":  {map[string]any{"branch_id": a.id.String(), "date": d.dayAfter(day, 30), "deposit_slip_ref": "S"}, "finance", http.StatusConflict},
		"no such branch": {map[string]any{"branch_id": uuid.NewString(), "date": day, "deposit_slip_ref": "S"}, "finance", http.StatusNotFound},
	} {
		if r := d.do("POST", "/api/v1/pos/end-of-day/close", c.body, "X-Test-Role", c.role, "X-Test-Sub", mustUUID(t)); r.status != c.want {
			t.Errorf("%s = %d, want %d: %s", name, r.status, c.want, r.raw)
		}
	}
	closer := mustUUID(t)
	r = d.do("POST", "/api/v1/pos/end-of-day/close", map[string]any{"branch_id": a.id.String(), "date": day, "deposit_slip_ref": " SLIP-1 ", "bank_account_id": d.defaultBank().String()},
		"X-Test-Role", "finance", "X-Test-Sub", closer)
	if r.status != http.StatusCreated {
		t.Fatalf("close after every drawer settled = %d: %s", r.status, r.raw)
	}
	c := obj(t, r.body, "close")
	if c["closed_by"] != closer || c["deposit_slip_ref"] != "SLIP-1" || c["business_date"] != day || c["closed_at"] == nil || c["deposit_match"] != nil {
		t.Errorf("the close record = %v, want who, when, the trimmed slip, the date, no match yet", c)
	}
	// 1599-1000 = 599 from register 1; register 2 counted 400 against a 1000
	// float (short 600, signed off) and deposits no cash, floored at 0
	if num(t, c, "cash_to_deposit_cents") != 599 {
		t.Errorf("cash_to_deposit_cents = %v, want 599", c["cash_to_deposit_cents"])
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

// bankAccountOn makes a bank account whose GL account has the code, and
// removes it when the test ends.
func (d *dayFixture) bankAccountOn(glCode string) uuid.UUID {
	d.t.Helper()
	id := uuid.New()
	mustExec(d.t, d.db, `INSERT INTO bank_accounts (id, name, gl_account_id) VALUES ($1, 'deposit test', (SELECT id FROM gl_accounts WHERE code = $2))`, id, glCode)
	d.accounts = append(d.accounts, id)
	return id
}

// defaultBank is the test's first bank account, on the cash account 1010.
func (d *dayFixture) defaultBank() uuid.UUID {
	d.t.Helper()
	if len(d.accounts) == 0 {
		return d.bankAccountOn("1010")
	}
	return d.accounts[0]
}

// bankLine adds a statement line to the test's default bank account.
func (d *dayFixture) bankLine(amount int64, date string) uuid.UUID {
	d.t.Helper()
	return d.bankLineOn(d.defaultBank(), amount, date)
}

// bankLineOn adds a statement line to a bank account and answers its id.
func (d *dayFixture) bankLineOn(account uuid.UUID, amount int64, date string) uuid.UUID {
	d.t.Helper()
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
	r := d.closeDay(b, d.today(b), "SLIP-"+b.id.String()[:6], d.defaultBank())
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
	tooOld := d.bankLine(599, d.dayAfter(day, -400))
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

// RULE (concurrency, pool size 4, three contenders): three matches of one
// closed day to three different statement lines end with exactly one match,
// and the two lines that lost are still free: their claims rolled back.
func TestRacingDepositMatchesTakeOneLine(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDBMaxConns(t, 4))
	a := d.makeBranch()
	closeID, _ := d.closedDay(a)
	day := d.today(a)
	lines := []uuid.UUID{d.bankLine(599, day), d.bankLine(600, day), d.bankLine(601, day)}
	var wg sync.WaitGroup
	statuses := make([]int, len(lines))
	for i, line := range lines {
		wg.Add(1)
		go func(i int, line uuid.UUID) {
			defer wg.Done()
			statuses[i] = d.do("POST", "/api/v1/pos/end-of-day/"+closeID+"/deposit-match", map[string]any{"bank_transaction_id": line.String()},
				"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)).status
		}(i, line)
	}
	wg.Wait()
	wins, conflicts := 0, 0
	for _, st := range statuses {
		switch st {
		case http.StatusOK:
			wins++
		case http.StatusConflict:
			conflicts++
		}
	}
	if wins != 1 || conflicts != 2 {
		t.Errorf("statuses = %v, want one 200 and two 409", statuses)
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM bank_transactions WHERE id = ANY($1) AND status = 'MATCHED'`, lines); got != 1 {
		t.Errorf("%d of the three lines are claimed, want exactly the winner's", got)
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM till_day_closes WHERE id = $1::uuid AND bank_transaction_id = ANY($2)`, closeID, lines); got != 1 {
		t.Errorf("the day does not hold one of the three lines")
	}
}

// saleAtPrice rings a one line sale priced at the dollars and cents given
// before tax (an override of the line's price) and pays it in cash, exactly.
func (d *dayFixture) saleAtPrice(register string, unitTenThousandths int64, cashCents int64) {
	d.t.Helper()
	saleID := d.startSaleOn(register)
	line := d.productLine("1")
	line["unit_price_ten_thousandths"] = unitTenThousandths
	line["override_reason"] = "deposit test"
	if r := d.addLine(saleID, line); r.status != http.StatusOK {
		d.t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := d.completeSaleOn(register, saleID, tender("cash", cashCents)); r.status != http.StatusOK {
		d.t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
}

// netOn is an account's net debit, in cents, over the entries sourced by the
// ids.
func (d *dayFixture) netOn(code string, ids ...string) int64 {
	d.t.Helper()
	var net int64
	if err := d.db.Pool.QueryRow(context.Background(), `
		SELECT COALESCE(ROUND(SUM(l.debit - l.credit) * 100), 0)::bigint
		FROM gl_journal_lines l JOIN gl_journal_entries e ON e.id = l.journal_entry_id JOIN gl_accounts a ON a.id = l.account_id
		WHERE a.code = $1 AND e.source_ref_id::text = ANY($2)`, code, ids).Scan(&net); err != nil {
		d.t.Fatal(err)
	}
	return net
}

// RULE (P2-1): the day close posts the deposit it records, in its own
// transaction. A float of 10000, a cash sale of 2000, a drop of 500 and a
// count of 11500 deposit 2000: debit the bank account's GL account 2000,
// credit 1015 for the 500 the drop put there and 1010 for the 1500 beyond the
// float; the entry is named on the close and the bank account is kept.
func TestTheDayClosePostsTheDeposit(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDB(t))
	glCode := "T" + uuid.NewString()[:7]
	mustExec(t, d.db, `INSERT INTO gl_accounts (code, name, type, subtype, normal_balance) VALUES ($1, 'test operating bank', 'ASSET', 'Current Asset', 'DEBIT')`, glCode)
	t.Cleanup(func() {
		_, _ = d.db.Pool.Exec(context.Background(), `DELETE FROM bank_transactions WHERE bank_account_id IN (SELECT id FROM bank_accounts WHERE gl_account_id = (SELECT id FROM gl_accounts WHERE code = $1))`, glCode)
		_, _ = d.db.Pool.Exec(context.Background(), `DELETE FROM bank_accounts WHERE gl_account_id = (SELECT id FROM gl_accounts WHERE code = $1)`, glCode)
		_, _ = d.db.Pool.Exec(context.Background(), `DELETE FROM gl_journal_lines WHERE account_id = (SELECT id FROM gl_accounts WHERE code = $1)`, glCode)
		_, _ = d.db.Pool.Exec(context.Background(), `DELETE FROM gl_accounts WHERE code = $1`, glCode)
	})
	bank := d.bankAccountOn(glCode)
	a := d.makeBranch()
	reg := a.registers[0]
	s := d.openTillOn(reg, 10000)
	d.saleAtPrice(reg, 183700, 2000) // 18.37 and its tax is 20.00
	drop := d.mustMovement(s, map[string]any{"kind": "safe_drop", "amount_cents": 500}, mustUUID(t))
	if r := d.count(s, map[string]any{"cash": 11500}); r.status != http.StatusOK || sessionOf(t, r)["status"] != "closed" {
		t.Fatalf("count = %d: %s", r.status, r.raw)
	}
	day := d.today(a)

	// the deposit needs an active bank account to go to
	if r := d.closeDay(a, day, "DEP-1"); r.status != http.StatusBadRequest {
		t.Fatalf("a close of a day with a deposit and no bank account = %d, want 400: %s", r.status, r.raw)
	}
	if r := d.closeDay(a, day, "DEP-1", uuid.New()); r.status != http.StatusNotFound {
		t.Fatalf("a close naming an unknown bank account = %d, want 404: %s", r.status, r.raw)
	}
	inactive := d.bankAccountOn(glCode)
	mustExec(t, d.db, `UPDATE bank_accounts SET is_active = FALSE WHERE id = $1`, inactive)
	if r := d.closeDay(a, day, "DEP-1", inactive); r.status != http.StatusConflict {
		t.Fatalf("a close naming an inactive bank account = %d, want 409: %s", r.status, r.raw)
	} else if _, bl, _ := errorOf(t, r); len(bl) != 1 || bl[0] != "bank_account_inactive" {
		t.Errorf("blockers = %v, want bank_account_inactive", bl)
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM till_day_closes WHERE branch_id = $1`, a.id); got != 0 {
		t.Fatalf("%d closes written by refused requests", got)
	}
	r := d.closeDay(a, day, "DEP-1", bank)
	if r.status != http.StatusCreated {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	c := obj(t, r.body, "close")
	closeID := str(t, c, "id")
	if num(t, c, "cash_to_deposit_cents") != 2000 || c["bank_account_id"] != bank.String() || c["gl_entry_id"] == nil {
		t.Fatalf("the close = %v, want a 2000 deposit to the bank account with its entry", c)
	}
	entries, legs := d.legsFor(closeID)
	if entries != 1 {
		t.Fatalf("%d entries sourced by the close, want 1", entries)
	}
	want := map[string]leg{glCode: {debit: 2000}, "1015": {credit: 500}, "1010": {credit: 1500}}
	for code, w := range want {
		if legs[code] != w {
			t.Errorf("account %s = %+v, want %+v", code, legs[code], w)
		}
	}
	if len(legs) != len(want) {
		t.Errorf("legs = %v, want exactly %v", legs, want)
	}
	var linked string
	if err := d.db.Pool.QueryRow(context.Background(), `SELECT gl_entry_id::text FROM till_day_closes WHERE id = $1`, closeID).Scan(&linked); err != nil || linked != str(t, c, "gl_entry_id") {
		t.Errorf("the stored entry = %q (%v), want %v", linked, err, c["gl_entry_id"])
	}
	// undeposited funds net to zero for a day whose drops are all deposited
	if got := d.netOn("1015", drop, closeID); got != 0 {
		t.Errorf("1015 nets %d over the drop and the deposit, want 0", got)
	}

	// when cash_to_deposit_cents < undeposited (rest < 0), 1010 is debited for -rest:
	// 10000 float, 300 cash sale, 500 safe_drop, counted at 9600, so deposit = 100, undeposited = 500, rest = -400
	b := d.makeBranch()
	bReg := b.registers[0]
	sb := d.openTillOn(bReg, 10000)
	d.saleAtPrice(bReg, 27600, 300) // 2.76 + 0.24 tax = 3.00
	bDrop := d.mustMovement(sb, map[string]any{"kind": "safe_drop", "amount_cents": 500}, mustUUID(t))
	if r := d.count(sb, map[string]any{"cash": 9600}); r.status != http.StatusOK || sessionOf(t, r)["status"] != "closed" {
		t.Fatalf("count b = %d: %s", r.status, r.raw)
	}
	rb := d.closeDay(b, d.today(b), "DEP-2", bank)
	if rb.status != http.StatusCreated {
		t.Fatalf("close b = %d: %s", rb.status, rb.raw)
	}
	cb := obj(t, rb.body, "close")
	closeBID := str(t, cb, "id")
	if num(t, cb, "cash_to_deposit_cents") != 100 {
		t.Fatalf("close b cash_to_deposit_cents = %v, want 100", cb["cash_to_deposit_cents"])
	}
	bEntries, bLegs := d.legsFor(closeBID)
	if bEntries != 1 {
		t.Fatalf("%d entries sourced by close b, want 1", bEntries)
	}
	wantB := map[string]leg{glCode: {debit: 100}, "1010": {debit: 400}, "1015": {credit: 500}}
	for code, w := range wantB {
		if bLegs[code] != w {
			t.Errorf("close b account %s = %+v, want %+v", code, bLegs[code], w)
		}
	}
	if len(bLegs) != len(wantB) {
		t.Errorf("close b legs = %v, want exactly %v", bLegs, wantB)
	}
	if got := d.netOn("1015", bDrop, closeBID); got != 0 {
		t.Errorf("close b 1015 nets %d over the drop and deposit, want 0", got)
	}
}

// RULE (P2-1, P3-1): the deposit candidates offer only lines of the close's
// bank account, the deposit match reconciles the bank line against the close's
// deposit entry, and a line of another bank account is refused.
func TestTheDepositMatchLinksTheDepositEntry(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDB(t))
	a := d.makeBranch()
	closeID, deposit := d.closedDay(a)
	day := d.today(a)
	other := d.bankAccountOn("1010")
	elsewhere := d.bankLineOn(other, deposit, day)
	line := d.bankLine(deposit, day)
	cands := d.do("GET", "/api/v1/pos/end-of-day/"+closeID+"/deposit-candidates", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if cands.status != http.StatusOK {
		t.Fatalf("candidates = %d: %s", cands.status, cands.raw)
	}
	items := cands.body["items"].([]any)
	foundOwn := false
	for _, it := range items {
		id := it.(map[string]any)["bank_transaction_id"]
		if id == elsewhere.String() {
			t.Errorf("deposit-candidates returned line %s from another bank account: %s", elsewhere, cands.raw)
		}
		if id == line.String() {
			foundOwn = true
		}
	}
	if !foundOwn {
		t.Errorf("deposit-candidates did not return the close's own bank account line %s: %s", line, cands.raw)
	}
	match := func(l uuid.UUID) resp {
		return d.do("POST", "/api/v1/pos/end-of-day/"+closeID+"/deposit-match", map[string]any{"bank_transaction_id": l.String()},
			"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	}
	r := match(elsewhere)
	if r.status != http.StatusConflict {
		t.Fatalf("a line of another bank account = %d, want 409: %s", r.status, r.raw)
	}
	if _, bl, _ := errorOf(t, r); len(bl) != 1 || bl[0] != "bank_account_mismatch" {
		t.Errorf("blockers = %v, want bank_account_mismatch", bl)
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM bank_transactions WHERE id = $1 AND status = 'UNMATCHED'`, elsewhere); got != 1 {
		t.Errorf("the refused match claimed the other account's line")
	}
	if r := match(line); r.status != http.StatusOK {
		t.Fatalf("match = %d: %s", r.status, r.raw)
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM bank_transactions b JOIN till_day_closes c ON c.gl_entry_id = b.matched_journal_entry_id
		WHERE b.id = $1 AND c.id = $2::uuid AND c.gl_entry_id IS NOT NULL`, line, closeID); got != 1 {
		t.Errorf("the bank line is not matched to the close's deposit entry")
	}
}

// RULE (P2-1): a day with nothing to deposit needs no bank account and posts
// no entry.
func TestADayWithNothingToDepositPostsNoEntry(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDB(t))
	a := d.makeBranch()
	s := d.openTillOn(a.registers[0], 1000)
	if r := d.count(s, map[string]any{"cash": 1000}); r.status != http.StatusOK {
		t.Fatalf("count = %d: %s", r.status, r.raw)
	}
	r := d.closeDay(a, d.today(a), "NONE-1")
	if r.status != http.StatusCreated {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	c := obj(t, r.body, "close")
	if c["gl_entry_id"] != nil || c["bank_account_id"] != nil {
		t.Errorf("the close = %v, want no entry and no bank account", c)
	}
	if entries, _ := d.legsFor(str(t, c, "id")); entries != 0 {
		t.Errorf("%d entries posted for a day with nothing to deposit", entries)
	}
}

// reopen reopens a closed day as a manager of the role.
func (d *dayFixture) reopen(closeID, reason, role, sub string) resp {
	d.t.Helper()
	return d.do("POST", "/api/v1/pos/end-of-day/"+closeID+"/reopen", map[string]any{"reason": reason},
		"X-Test-Role", role, "X-Test-Sub", sub)
}

// bankWithGL makes a bank account on a GL account of its own, so a test reads
// the deposit's legs apart from the cash account's, and removes both.
func (d *dayFixture) bankWithGL() (bank uuid.UUID, code string) {
	d.t.Helper()
	code = "T" + uuid.NewString()[:7]
	mustExec(d.t, d.db, `INSERT INTO gl_accounts (code, name, type, subtype, normal_balance) VALUES ($1, 'test operating bank', 'ASSET', 'Current Asset', 'DEBIT')`, code)
	d.t.Cleanup(func() {
		ctx := context.Background()
		_, _ = d.db.Pool.Exec(ctx, `DELETE FROM bank_transactions WHERE bank_account_id IN (SELECT id FROM bank_accounts WHERE gl_account_id = (SELECT id FROM gl_accounts WHERE code = $1))`, code)
		_, _ = d.db.Pool.Exec(ctx, `DELETE FROM bank_accounts WHERE gl_account_id = (SELECT id FROM gl_accounts WHERE code = $1)`, code)
		_, _ = d.db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE account_id = (SELECT id FROM gl_accounts WHERE code = $1)`, code)
		_, _ = d.db.Pool.Exec(ctx, `DELETE FROM gl_accounts WHERE code = $1`, code)
	})
	return d.bankAccountOn(code), code
}

// RULE (P2-2): a closed day reopens, with a reason: the deposit entry is
// reversed (a reversal naming it), the close row is kept with status REOPENED
// and its frozen snapshot intact, the day is open again, a drawer opens, and
// the day closes again with a new CLOSED row. The act is audited and evented.
func TestAClosedDayReopensAndClosesAgain(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDB(t))
	bank, code := d.bankWithGL()
	a := d.makeBranch()
	reg := a.registers[0]
	s := d.openTillOn(reg, 10000)
	d.saleAtPrice(reg, 183700, 2000)
	drop := d.mustMovement(s, map[string]any{"kind": "safe_drop", "amount_cents": 500}, mustUUID(t))
	d.count(s, map[string]any{"cash": 11500})
	day := d.today(a)
	r := d.closeDay(a, day, "FIRST", bank)
	if r.status != http.StatusCreated {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	first := obj(t, r.body, "close")
	closeID, entryID := str(t, first, "id"), str(t, first, "gl_entry_id")
	if got := d.netOn(code, closeID, entryID); got != 2000 {
		t.Fatalf("the bank account nets %d after the close, want 2000", got)
	}

	manager := mustUUID(t)
	rr := d.reopen(closeID, "a late drawer", "owner", manager)
	if rr.status != http.StatusOK {
		t.Fatalf("reopen = %d: %s", rr.status, rr.raw)
	}
	if rr.body["close"] != nil || rr.body["business_date"] != day {
		t.Errorf("the reopened day = %v, want the open day with no close", rr.body["close"])
	}
	if rd := d.summary(a, day); rd.status != http.StatusOK || rd.body["close"] != nil {
		t.Errorf("summary while reopened = %d %v, want close null", rd.status, rd.body["close"])
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM till_day_closes c JOIN gl_journal_entries r ON r.id = c.reversal_gl_entry_id
		WHERE c.id = $1::uuid AND c.status = 'REOPENED' AND c.reopened_by = $2 AND c.reopened_at IS NOT NULL
		  AND c.reopen_reason = 'a late drawer' AND c.cash_to_deposit_cents = 2000 AND c.bank_account_id = $3
		  AND c.gl_entry_id = $4::uuid AND c.totals IS NOT NULL AND r.reverses_entry_id = c.gl_entry_id AND r.source = 'REVERSAL'`,
		closeID, manager, bank, entryID); got != 1 {
		t.Errorf("the reopened close row with its snapshot and reversal link = %d, want 1", got)
	}
	for _, c := range []string{code, "1015"} {
		if got := d.netOn(c, closeID, entryID); c == code && got != 0 {
			t.Errorf("the bank account nets %d after the reopen, want 0", got)
		}
	}
	// 1015 holds the drop again: the deposit that relieved it is reversed
	if got := d.netOn("1015", drop, closeID, entryID); got != 500 {
		t.Errorf("1015 nets %d after the reopen, want the drop's 500", got)
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.day.reopened' AND entity_id = $1::uuid AND user_id = $2`, closeID, manager); got != 1 {
		t.Errorf("%d audit rows for the reopen by the manager, want 1", got)
	}
	if got := eventTypes(t, d.db, "till_day_close", closeID); len(got) != 2 || got[1] != "till.day_reopened" {
		t.Errorf("events = %v, want till.day_closed then till.day_reopened", got)
	}
	// a drawer opens, is counted, and the day closes again with a new deposit
	s2 := d.openTillOn(a.registers[1], 0)
	if r := d.count(s2, map[string]any{"cash": 0}); r.status != http.StatusOK {
		t.Fatalf("count 2 = %d: %s", r.status, r.raw)
	}
	again := d.closeDay(a, day, "SECOND", bank)
	if again.status != http.StatusCreated {
		t.Fatalf("close again = %d: %s", again.status, again.raw)
	}
	second := obj(t, again.body, "close")
	if second["id"] == first["id"] || second["gl_entry_id"] == first["gl_entry_id"] || second["deposit_slip_ref"] != "SECOND" {
		t.Errorf("the second close = %v, want a new record and a new entry", second)
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM till_day_closes WHERE branch_id = $1 AND business_date = $2::date AND status = 'REOPENED'`, a.id, day); got != 1 {
		t.Errorf("%d reopened closes for the branch day, want 1", got)
	}
	if got := countOf(t, d.db, `SELECT count(*) FROM till_day_closes WHERE branch_id = $1 AND business_date = $2::date AND status = 'CLOSED'`, a.id, day); got != 1 {
		t.Errorf("%d active closes for the branch day, want 1", got)
	}
	if got := d.netOn(code, closeID, entryID, str(t, second, "id"), str(t, second, "gl_entry_id")); got != 2000 {
		t.Errorf("the bank account nets %d after the second close, want one deposit of 2000", got)
	}
}

// RULE (P2-2): the reopen is refused without a reason, for a cashier, for a
// machine key, for a day that does not exist, for a close already reopened,
// and once the deposit is matched to a bank line; a refusal changes nothing.
func TestTheReopenRefusals(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDB(t))
	a := d.makeBranch()
	closeID, deposit := d.closedDay(a)
	untouched := func(what string) {
		t.Helper()
		if got := countOf(t, d.db, `SELECT count(*) FROM till_day_closes WHERE id = $1::uuid AND status = 'CLOSED' AND reopened_at IS NULL`, closeID); got != 1 {
			t.Errorf("%s: the close is no longer CLOSED", what)
		}
		if got := countOf(t, d.db, `SELECT count(*) FROM gl_journal_entries WHERE source = 'REVERSAL' AND reverses_entry_id = (SELECT gl_entry_id FROM till_day_closes WHERE id = $1::uuid)`, closeID); got != 0 {
			t.Errorf("%s: the deposit entry was reversed", what)
		}
	}
	for name, reason := range map[string]string{"no reason": "", "a blank reason": "   "} {
		if r := d.reopen(closeID, reason, "finance", mustUUID(t)); r.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, r.status, r.raw)
		}
	}
	if r := d.reopen(closeID, "why", "cashier", mustUUID(t)); r.status != http.StatusForbidden {
		t.Errorf("a cashier = %d, want 403", r.status)
	}
	keyed := keyedServer(d.fixture, uuid.NewString())
	req, _ := http.NewRequest(http.MethodPost, keyed.URL+"/api/v1/pos/end-of-day/"+closeID+"/reopen", strings.NewReader(`{"reason":"why"}`))
	req.Header.Set("Content-Type", "application/json")
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusForbidden {
		t.Errorf("a machine key = %v %v, want 403", res, err)
	}
	if r := d.reopen(uuid.NewString(), "why", "finance", mustUUID(t)); r.status != http.StatusNotFound {
		t.Errorf("an unknown close = %d, want 404: %s", r.status, r.raw)
	}
	if r := d.reopen("not-a-uuid", "why", "finance", mustUUID(t)); r.status != http.StatusBadRequest {
		t.Errorf("a malformed id = %d, want 400", r.status)
	}
	untouched("after the refusals")
	// matched to a bank line, the day cannot reopen
	line := d.bankLine(deposit, d.today(a))
	if r := d.do("POST", "/api/v1/pos/end-of-day/"+closeID+"/deposit-match", map[string]any{"bank_transaction_id": line.String()},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("match = %d: %s", r.status, r.raw)
	}
	r := d.reopen(closeID, "too late", "owner", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("reopen of a matched day = %d, want 409: %s", r.status, r.raw)
	}
	if _, bl, _ := errorOf(t, r); len(bl) != 1 || bl[0] != "deposit_already_matched" {
		t.Errorf("blockers = %v, want deposit_already_matched", bl)
	}
	untouched("after the matched refusal")

	// an already-reopened close cannot be reopened or matched again (409 day_not_closed)
	b := d.makeBranch()
	reopenedID, bDeposit := d.closedDay(b)
	bLine := d.bankLine(bDeposit, d.today(b))
	if rr := d.reopen(reopenedID, "first reopen", "owner", mustUUID(t)); rr.status != http.StatusOK {
		t.Fatalf("reopen b = %d: %s", rr.status, rr.raw)
	}
	if r2 := d.reopen(reopenedID, "second reopen", "owner", mustUUID(t)); r2.status != http.StatusConflict {
		t.Fatalf("reopen of an already-reopened close = %d, want 409: %s", r2.status, r2.raw)
	} else if _, bl, _ := errorOf(t, r2); len(bl) != 1 || bl[0] != "day_not_closed" {
		t.Errorf("blockers on second reopen = %v, want day_not_closed", bl)
	}
	if rm := d.do("POST", "/api/v1/pos/end-of-day/"+reopenedID+"/deposit-match", map[string]any{"bank_transaction_id": bLine.String()},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); rm.status != http.StatusConflict {
		t.Fatalf("match of an already-reopened close = %d, want 409: %s", rm.status, rm.raw)
	} else if _, bl, _ := errorOf(t, rm); len(bl) != 1 || bl[0] != "day_not_closed" {
		t.Errorf("blockers on match of reopened close = %v, want day_not_closed", bl)
	}
}

// RULE (P2-2): the reopen carries the branch wall.
func TestTheReopenCarriesTheBranchWall(t *testing.T) {
	w := newWallFixture(t)
	sessionID := w.openDrawerA()
	if r := w.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": 5000}}); r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	var day string
	if err := w.db.Pool.QueryRow(contextB(), `SELECT (NOW() AT TIME ZONE COALESCE((SELECT timezone FROM locations WHERE id = $1), 'UTC'))::date::text`, w.branchID).Scan(&day); err != nil {
		t.Fatal(err)
	}
	r := w.do("POST", "/api/v1/pos/end-of-day/close", map[string]any{"branch_id": w.branchID.String(), "date": day, "deposit_slip_ref": "WALL-R"})
	if r.status != http.StatusCreated {
		t.Fatalf("day close = %d: %s", r.status, r.raw)
	}
	closeID := str(t, obj(t, r.body, "close"), "id")
	scoped := append([]string{}, w.scoped...)
	scoped[1] = "finance"
	noHdr := append([]string{}, w.noHdr...)
	noHdr[1] = "finance"
	if r := w.do("POST", "/api/v1/pos/end-of-day/"+closeID+"/reopen", map[string]any{"reason": "wall"}, scoped...); r.status != http.StatusNotFound {
		t.Errorf("another branch's reopen, naming branch B = %d, want 404: %s", r.status, r.raw)
	}
	if r := w.do("POST", "/api/v1/pos/end-of-day/"+closeID+"/reopen", map[string]any{"reason": "wall"}, noHdr...); r.status != http.StatusForbidden {
		t.Errorf("another branch's reopen, naming no branch = %d, want the 403 record rule: %s", r.status, r.raw)
	}
	if got := countOf(t, w.db, `SELECT count(*) FROM till_day_closes WHERE id = $1::uuid AND status = 'CLOSED'`, closeID); got != 1 {
		t.Errorf("the day reopened through the wall")
	}
	if r := w.do("POST", "/api/v1/pos/end-of-day/"+closeID+"/reopen", map[string]any{"reason": "own branch"}); r.status != http.StatusOK {
		t.Errorf("the unscoped reopen = %d, want 200: %s", r.status, r.raw)
	}
	if got := countOf(t, w.db, `SELECT count(*) FROM till_day_closes WHERE id = $1::uuid AND status = 'REOPENED'`, closeID); got != 1 {
		t.Errorf("the unscoped reopen did not mark the close REOPENED")
	}
}

// RULE (concurrency, pool size 4, three contenders): a reopen racing two
// matches of the day ends in one state: reopened with no line claimed, or
// matched once with the reopen refused.
func TestARacingReopenAndMatchesEndConsistent(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDBMaxConns(t, 4))
	for round := 0; round < 3; round++ {
		a := d.makeBranch()
		closeID, deposit := d.closedDay(a)
		day := d.today(a)
		lines := []uuid.UUID{d.bankLine(deposit, day), d.bankLine(deposit+1, day)}
		var wg sync.WaitGroup
		statuses := make([]int, 3)
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[0] = d.reopen(closeID, "racing", "finance", mustUUID(t)).status
		}()
		for i, line := range lines {
			wg.Add(1)
			go func(i int, line uuid.UUID) {
				defer wg.Done()
				statuses[i+1] = d.do("POST", "/api/v1/pos/end-of-day/"+closeID+"/deposit-match", map[string]any{"bank_transaction_id": line.String()},
					"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)).status
			}(i, line)
		}
		wg.Wait()
		closed := countOf(t, d.db, `SELECT count(*) FROM till_day_closes WHERE id = $1::uuid AND status = 'CLOSED' AND bank_transaction_id IS NOT NULL AND reversal_gl_entry_id IS NULL`, closeID)
		reopened := countOf(t, d.db, `SELECT count(*) FROM till_day_closes WHERE id = $1::uuid AND status = 'REOPENED' AND bank_transaction_id IS NULL AND reversal_gl_entry_id IS NOT NULL`, closeID)
		claimed := countOf(t, d.db, `SELECT count(*) FROM bank_transactions WHERE id = ANY($1) AND status = 'MATCHED'`, lines)
		reversals := countOf(t, d.db, `SELECT count(*) FROM gl_journal_entries WHERE source = 'REVERSAL' AND reverses_entry_id = (SELECT gl_entry_id FROM till_day_closes WHERE id = $1::uuid)`, closeID)
		switch {
		case statuses[0] == http.StatusOK:
			if reopened != 1 || closed != 0 || claimed != 0 || reversals != 1 {
				t.Errorf("round %d: reopened but %d reopened, %d closed, %d lines claimed, %d reversals (statuses %v)", round, reopened, closed, claimed, reversals, statuses)
			}
		case statuses[0] == http.StatusConflict:
			if closed != 1 || reopened != 0 || claimed != 1 || reversals != 0 {
				t.Errorf("round %d: reopen refused but %d closed, %d reopened, %d lines claimed, %d reversals (statuses %v)", round, closed, reopened, claimed, reversals, statuses)
			}
		default:
			t.Errorf("round %d: the reopen answered %d, want 200 or 409", round, statuses[0])
		}
	}
}
