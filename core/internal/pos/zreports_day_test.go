// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The stored Z reports' business day (CP-08 slice 8): the list buckets by the
// branch's own zone, as the end of day summary does, never by a UTC day and
// never by the server's clock. A drawer closed in the yard's evening sits in
// the yard's day, though it is already the next day in UTC.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// zListOf reads the Z reports list of a register (a date when given) and
// answers the session ids it holds.
func (d *dayFixture) zListOf(register, date string) []string {
	d.t.Helper()
	path := "/api/v1/pos/zreports?register_id=" + register
	if date != "" {
		path += "&date=" + date
	}
	r := d.do("GET", path, nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(d.t))
	if r.status != http.StatusOK {
		d.t.Fatalf("the Z list = %d: %s", r.status, r.raw)
	}
	var rows []struct {
		TillSessionID string `json:"till_session_id"`
	}
	if err := json.Unmarshal(r.raw, &rows); err != nil {
		d.t.Fatalf("the Z list is not a list: %v: %s", err, r.raw)
	}
	out := []string{}
	for _, row := range rows {
		out = append(out, row.TillSessionID)
	}
	return out
}

// closedAt opens and closes a drawer on the register, then places its Z
// report at a local wall time of the branch's business day.
func (d *dayFixture) closedAt(register, day, wall string) string {
	d.t.Helper()
	s := d.openTillOn(register, 5000)
	if r := d.count(s, map[string]any{"cash": 5000}); r.status != http.StatusOK {
		d.t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	mustExec(d.t, d.db, `UPDATE till_z_reports SET generated_at = (($1::date + $2::time) AT TIME ZONE 'America/New_York') WHERE till_session_id = $3`,
		day, wall, s)
	return s
}

func has(list []string, id string) bool {
	for _, s := range list {
		if s == id {
			return true
		}
	}
	return false
}

// RULE: a drawer closed in the yard's evening is listed under the yard's own
// business day, not under the UTC day it has already turned into.
func TestTheZListBucketsByTheBranchsOwnDay(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDB(t))
	b := d.makeBranch()
	today := d.today(b)
	picked := d.dayAfter(today, -3)

	evening := d.closedAt(b.registers[0], picked, "20:30")

	if got := d.zListOf(b.registers[0], picked); !has(got, evening) {
		t.Errorf("the yard's day %s lists %v, want the drawer closed at 20:30 local to be in it", picked, got)
	}
	// 20:30 local is already the next UTC day, so a UTC bucket would put the
	// drawer under the day after
	if got := d.zListOf(b.registers[0], d.dayAfter(picked, 1)); has(got, evening) {
		t.Errorf("the day after %s lists the drawer closed at 20:30 local of %s: %v", picked, picked, got)
	}
	// no date is the branch's today, so a drawer of three days back is not
	// in it
	if got := d.zListOf(b.registers[0], ""); has(got, evening) {
		t.Errorf("no date lists a drawer of %s as today's: %v", picked, got)
	}
}

// RULE: no date is the branch's today, read in the branch's zone: a drawer
// closed just after the yard's midnight is today's whatever the UTC date is.
func TestTheZListWithNoDateIsTheBranchsToday(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDB(t))
	b := d.makeBranch()
	today := d.today(b)

	early := d.closedAt(b.registers[0], today, "00:30")
	late := d.closedAt(b.registers[1], today, "23:30")

	if got := d.zListOf(b.registers[0], ""); !has(got, early) {
		t.Errorf("no date lists %v, want the drawer closed at 00:30 local today", got)
	}
	if got := d.zListOf(b.registers[1], ""); !has(got, late) {
		t.Errorf("no date lists %v, want the drawer closed at 23:30 local today", got)
	}
	var tomorrowUTC string
	if err := d.db.Pool.QueryRow(context.Background(), `SELECT (($1::date + time '23:30') AT TIME ZONE 'America/New_York' AT TIME ZONE 'UTC')::date::text`, today).Scan(&tomorrowUTC); err != nil {
		t.Fatal(err)
	}
	// the 23:30 drawer's UTC date is the day after today; asking for that
	// day does not find it
	if got := d.zListOf(b.registers[1], tomorrowUTC); has(got, late) {
		t.Errorf("the UTC date %s lists the drawer closed at 23:30 local of %s: %v", tomorrowUTC, today, got)
	}
}
