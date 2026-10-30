// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// RULE: the desk and the portal answer the same about "overdue", and the day
// an invoice falls due on is the branch's own calendar day. The portal reads
// the desk's expression (invoice.OverdueExpr), never the database session's
// day. A branch west of UTC and a session in UTC disagree about the date
// every evening, and on those evenings the two surfaces answered differently
// about the same invoice, to the same dealer.
//
// The test pins the clock so that both sides of the branch's midnight can be
// read: a schema whose now() shadows the database's, reached through the same
// NOW() the shared expression calls, so the desk and the portal see the same
// instant. The session's own day (CURRENT_DATE, which the old portal read)
// stays the real one, which is what a deployment with no session zone has.

// branchDayClock is the test's clock. Its shadow is read through NOW(), the
// function the shared expression calls, so both surfaces see one instant.
type branchDayClock struct {
	schema string
}

// installBranchDayClock creates the shadow and drops it at the end of the
// test. The schema is the test's own, named so two runs never collide.
func installBranchDayClock(t *testing.T, db *database.DB) *branchDayClock {
	t.Helper()
	ctx := context.Background()
	schema := "gc539_clock_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	exec := func(q string) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, q); err != nil {
			t.Fatalf("clock %q: %v", q, err)
		}
	}
	exec(`CREATE SCHEMA ` + schema)
	exec(`CREATE FUNCTION ` + schema + `.now() RETURNS timestamptz LANGUAGE sql STABLE AS $$
		SELECT COALESCE(NULLIF(current_setting('gc539.now', true), '')::timestamptz, pg_catalog.now())
	$$`)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})
	return &branchDayClock{schema: schema}
}

// at places the clock at the given hour of the session's own day and puts the
// session in UTC. It runs inside the transaction that reads, so the shadow and
// the setting are the ones those reads see. The session's day and the
// simulated day's UTC date are the same day, so the session reads as it would
// on the day the clock names.
//
// The instant the shadow names comes back with it, because the database's
// clock and Go's clock have to be one instant: the SQL clock shadow does not
// reach Go's time.Now, so a service that dates money in Go needs the instant
// handed to it.
func (c *branchDayClock) at(ctx context.Context, db *database.DB, hourUTC string) (time.Time, error) {
	exec := db.GetExecutor(ctx)
	if _, err := exec.Exec(ctx, `SET LOCAL search_path TO `+c.schema+`, public, pg_catalog`); err != nil {
		return time.Time{}, fmt.Errorf("search path: %w", err)
	}
	if _, err := exec.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return time.Time{}, fmt.Errorf("time zone: %w", err)
	}
	if _, err := exec.Exec(ctx,
		`SELECT set_config('gc539.now', CURRENT_DATE::text || ' ' || $1, true)`, hourUTC); err != nil {
		return time.Time{}, fmt.Errorf("clock: %w", err)
	}
	var instant time.Time
	if err := exec.QueryRow(ctx, `SELECT (current_setting('gc539.now'))::timestamptz`).Scan(&instant); err != nil {
		return time.Time{}, fmt.Errorf("the shadowed instant: %w", err)
	}
	return instant.UTC(), nil
}

// overdueAnswers is what the desk and the portal said about the same
// invoices, read through every surface that says whether an invoice is
// overdue.
type overdueAnswers struct {
	desk         map[uuid.UUID]bool // the desk's invoice list
	list         map[uuid.UUID]bool // the portal's invoice list
	single       map[uuid.UUID]bool // the portal's one invoice read
	today        map[uuid.UUID]bool // the portal's Today board
	todayIDs     []uuid.UUID        // the Today board's invoices, in its order
	todayCount   int                // the Today board's count
	openCount    int                // the billing summary's open invoice count
	overdueCount int                // the billing summary's overdue invoice count
	pastDue      float64            // the AR summary's past due total
	balance      float64            // the AR summary's open balance
	pastDueCents int64              // the billing summary's past due tile, in cents
	agingAsOf    string             // the billing summary's aging as of
}

// overdueWorld is one branch, one customer and three open invoices: one due
// two days before the session's day, one due the day before and one due on
// the session's day. Which of them are overdue is the branch's day to say.
type overdueWorld struct {
	customerID   uuid.UUID
	invoiceIDs   []uuid.UUID // in the order of dueOld, dueYesterday, dueToday
	sessionDay   string
	issued       string
	dueOld       string
	dueYesterday string
	dueToday     string
}

// seedOverdueWorld writes the branch, the customer and the invoices. The
// branch's zone is the test's: America/Vancouver for a branch west of UTC,
// America/New_York for one in the United States.
//
// The invoices are issued a week before the session's day, so the receivable
// ages on either the session's day or the branch's day (a branch behind UTC
// is a day behind the session): the aging counts an invoice issued on or
// before the day it ages on, and an invoice issued on the session's day would
// be an invoice from the branch's future.
func seedOverdueWorld(ctx context.Context, db *database.DB, zone string) (overdueWorld, error) {
	exec := db.GetExecutor(ctx)
	var sessionDay string
	if err := exec.QueryRow(ctx, `SELECT CURRENT_DATE::text`).Scan(&sessionDay); err != nil {
		return overdueWorld{}, fmt.Errorf("the session's day: %w", err)
	}
	day, err := time.Parse("2006-01-02", sessionDay)
	if err != nil {
		return overdueWorld{}, fmt.Errorf("the session's day: %w", err)
	}
	w := overdueWorld{
		sessionDay:   sessionDay,
		issued:       day.AddDate(0, 0, -7).Format("2006-01-02"),
		dueOld:       day.AddDate(0, 0, -2).Format("2006-01-02"),
		dueYesterday: day.AddDate(0, 0, -1).Format("2006-01-02"),
		dueToday:     day.Format("2006-01-02"),
	}
	branchID := uuid.New()
	if _, err := exec.Exec(ctx,
		`INSERT INTO locations (id, type, code, name, timezone, default_tax_rate)
			VALUES ($1, 'BRANCH', $2, 'Gc539 yard', $3, 0.0825)`,
		branchID, "GC539-"+branchID.String()[:8], zone); err != nil {
		return overdueWorld{}, fmt.Errorf("the branch: %w", err)
	}
	w.customerID = uuid.New()
	if _, err := exec.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
			VALUES ($1, 'Gc539 customer', $2, $3)`,
		w.customerID, "GC539-"+w.customerID.String()[:8], branchID); err != nil {
		return overdueWorld{}, fmt.Errorf("the customer: %w", err)
	}
	for _, due := range []string{w.dueOld, w.dueYesterday, w.dueToday} {
		id := uuid.New()
		if _, err := exec.Exec(ctx,
			`INSERT INTO invoices (id, customer_id, status, branch_id, total_amount, subtotal,
				tax_amount, amount_open, due_date, invoice_date)
				VALUES ($1, $2, 'UNPAID', $3, 100.00, 100.00, 0.00, 100.00, $4::date, $5::date)`,
			id, w.customerID, branchID, due, w.issued); err != nil {
			return overdueWorld{}, fmt.Errorf("an invoice due %s: %w", due, err)
		}
		w.invoiceIDs = append(w.invoiceIDs, id)
	}
	return w, nil
}

// readOverdueAnswers reads the same invoices through the desk and through
// every portal surface that says whether an invoice is overdue. The instant
// is the clock's, because the portal dates its money in Go and the SQL clock
// shadow does not reach Go's time.Now.
func readOverdueAnswers(ctx context.Context, db *database.DB, w overdueWorld, instant time.Time) (overdueAnswers, error) {
	out := overdueAnswers{
		desk:   map[uuid.UUID]bool{},
		list:   map[uuid.UUID]bool{},
		single: map[uuid.UUID]bool{},
		today:  map[uuid.UUID]bool{},
	}
	deskRows, err := invoice.NewRepository(db).ListInvoices(ctx,
		invoice.ListFilter{CustomerID: &w.customerID, Limit: 50})
	if err != nil {
		return out, fmt.Errorf("the desk's invoice list: %w", err)
	}
	for _, row := range deskRows {
		out.desk[row.ID] = row.IsOverdue
	}
	repo := NewRepository(db)
	portalRows, err := repo.ListInvoicesByCustomer(ctx, w.customerID)
	if err != nil {
		return out, fmt.Errorf("the portal's invoice list: %w", err)
	}
	for _, row := range portalRows {
		out.list[row.ID] = row.IsOverdue
	}
	for _, id := range w.invoiceIDs {
		one, err := repo.GetInvoiceByIDAndCustomer(ctx, id, w.customerID)
		if err != nil {
			return out, fmt.Errorf("the portal's one invoice read: %w", err)
		}
		out.single[id] = one.IsOverdue
	}
	todayRows, err := repo.TodayInvoices(ctx, w.customerID, nil, 50)
	if err != nil {
		return out, fmt.Errorf("the portal's Today board: %w", err)
	}
	for _, row := range todayRows {
		out.today[row.ID] = row.IsOverdue
		out.todayIDs = append(out.todayIDs, row.ID)
	}
	if out.todayCount, err = repo.TodayInvoicesCount(ctx, w.customerID); err != nil {
		return out, fmt.Errorf("the portal's Today count: %w", err)
	}
	svc := NewService(repo, testSecret, testLogger(), nil, nil, nil, nil, nil).
		WithAccountService(account.NewService(db, nil, testLogger())).
		WithClockForTest(func() time.Time { return instant })
	summary, err := svc.GetBillingSummary(ctx, w.customerID)
	if err != nil {
		return out, fmt.Errorf("the portal's billing summary: %w", err)
	}
	out.openCount = summary.BalanceTiles.OpenInvoiceCount
	out.overdueCount = summary.BalanceTiles.OverdueInvoiceCount
	out.pastDueCents = summary.BalanceTiles.PastDueCents
	out.agingAsOf = summary.Aging.AsOf
	balance, _, pastDue, err := repo.GetCustomerARSummary(ctx, w.customerID)
	if err != nil {
		return out, fmt.Errorf("the portal's AR summary: %w", err)
	}
	out.balance = balance
	out.pastDue = pastDue
	return out, nil
}

// overduePhase is one reading of the two surfaces: an hour of the clock, the
// branch's zone, and how far the branch's day sits behind the session's day
// at that hour.
type overduePhase struct {
	name string
	hour string
	zone string
	// branchDayBehind is how many days the branch's day sits behind the
	// session's day at this hour.
	branchDayBehind int
}

// TestPortalOverdueAgreesWithTheDeskOnTheBranchesDay covers the branch west
// of UTC. At 05:00 UTC the session's day has turned to the next day and the
// branch's day has not: the invoice due yesterday is due today at the branch,
// so it is not overdue, and the old portal said it was. At 08:00 UTC the
// branch's midnight has passed and the same invoice is overdue. The desk and
// the portal have to agree in both, on every surface that says so.
//
// The two instants are 21:00 and 22:00 the day before in Vancouver, so the
// branch's day is behind the session's day whichever side of daylight saving
// the day falls on.
func TestPortalOverdueAgreesWithTheDeskOnTheBranchesDay(t *testing.T) {
	db := testutil.RequireDB(t)
	clock := installBranchDayClock(t, db)
	for _, phase := range []overduePhase{
		{"before the branch's midnight", "05:00:00+00", "America/Vancouver", 1},
		{"after the branch's midnight", "08:00:00+00", "America/Vancouver", 0},
	} {
		t.Run(phase.name, func(t *testing.T) {
			overdueLeg(t, db, clock, phase)
		})
	}
}

// TestPortalOverdueForAUSBranchIsUnchanged covers a branch in the United
// States. At a business hour there the branch's day is the session's day, so
// the portal's answers are the ones it gave before this rule was shared, and
// they are the desk's answers too. At the branch's own midnight the two
// surfaces have to agree there as well, the same rule the west of UTC branch
// gets.
//
// The instant before the branch's midnight is 22:00 or 23:00 the day before
// in New York, so the branch's day is behind the session's day whichever side
// of daylight saving the day falls on.
func TestPortalOverdueForAUSBranchIsUnchanged(t *testing.T) {
	db := testutil.RequireDB(t)
	clock := installBranchDayClock(t, db)
	for _, phase := range []overduePhase{
		{"a business hour in the branch", "16:00:00+00", "America/New_York", 0},
		{"the branch's own midnight", "03:00:00+00", "America/New_York", 1},
	} {
		t.Run(phase.name, func(t *testing.T) {
			overdueLeg(t, db, clock, phase)
		})
	}
}

// overdueLeg reads both surfaces in one transaction and checks that they say
// the same thing about the same invoices, and that they say the branch's
// day, not the session's.
func overdueLeg(t *testing.T, db *database.DB, clock *branchDayClock, phase overduePhase) {
	t.Helper()
	ctx := context.Background()
	var (
		world   overdueWorld
		got     overdueAnswers
		instant time.Time
	)
	err := db.RunInTx(ctx, func(ctx context.Context) error {
		at, err := clock.at(ctx, db, phase.hour)
		if err != nil {
			return err
		}
		instant = at
		w, err := seedOverdueWorld(ctx, db, phase.zone)
		if err != nil {
			return err
		}
		world = w
		got, err = readOverdueAnswers(ctx, db, w, instant)
		return err
	})
	if err != nil {
		t.Fatalf("read the two surfaces: %v", err)
	}

	dueOld, dueYesterday, dueToday := world.invoiceIDs[0], world.invoiceIDs[1], world.invoiceIDs[2]
	// The branch's day sits behind the session's day, so the invoice due the
	// day before the session's day is due today at the branch and is not yet
	// overdue while the branch's day is behind; once the branch's day has
	// caught up it is overdue. The invoice due the session's day is never
	// overdue, and the one due two days before is always overdue.
	want := map[uuid.UUID]bool{
		dueOld:       true,
		dueYesterday: phase.branchDayBehind == 0,
		dueToday:     false,
	}
	for id, expected := range want {
		if got.desk[id] != expected {
			t.Errorf("the desk on the invoice due %s: overdue = %t, want %t",
				dueOf(world, id), got.desk[id], expected)
		}
		if got.list[id] != got.desk[id] {
			t.Errorf("the portal's list on the invoice due %s: overdue = %t, the desk says %t",
				dueOf(world, id), got.list[id], got.desk[id])
		}
		if got.single[id] != got.desk[id] {
			t.Errorf("the portal's one invoice read on the invoice due %s: overdue = %t, the desk says %t",
				dueOf(world, id), got.single[id], got.desk[id])
		}
	}

	// The Today board lists the invoices due on or before the branch's own
	// day, and the flag beside each one agrees with the desk.
	wantToday := []uuid.UUID{dueOld, dueYesterday}
	if phase.branchDayBehind == 0 {
		wantToday = append(wantToday, dueToday)
	}
	for _, id := range wantToday {
		if _, listed := got.today[id]; !listed {
			t.Errorf("the Today board is missing the invoice due %s", dueOf(world, id))
			continue
		}
		if got.today[id] != got.desk[id] {
			t.Errorf("the Today board on the invoice due %s: overdue = %t, the desk says %t",
				dueOf(world, id), got.today[id], got.desk[id])
		}
	}
	for _, id := range world.invoiceIDs {
		_, listed := got.today[id]
		if listed != containsID(wantToday, id) {
			t.Errorf("the Today board holds the invoice due %s: %t, want %t",
				dueOf(world, id), listed, containsID(wantToday, id))
		}
	}
	if got.todayCount != len(wantToday) {
		t.Errorf("the Today count is %d, the board holds %d", got.todayCount, len(wantToday))
	}

	// The billing summary counts what the desk flags, and the AR summary
	// totals the same set.
	wantOverdue := 0
	for _, overdue := range want {
		if overdue {
			wantOverdue++
		}
	}
	if got.overdueCount != wantOverdue {
		t.Errorf("the billing summary's overdue count is %d, want %d", got.overdueCount, wantOverdue)
	}
	if got.openCount != len(world.invoiceIDs) {
		t.Errorf("the billing summary's open count is %d, want %d", got.openCount, len(world.invoiceIDs))
	}
	if got.pastDue != float64(100*wantOverdue) {
		t.Errorf("the AR summary's past due total is %v, want %v", got.pastDue, float64(100*wantOverdue))
	}
	if got.balance != 300 {
		t.Errorf("the AR summary's balance is %v, want 300", got.balance)
	}

	// The billing summary's past due tile is the money the desk calls
	// overdue: the same set, in the same cents. It ages the receivable on the
	// branch's own day, so the tile and the overdue count beside it are one
	// answer, and the answer is the branch's day.
	if got.pastDueCents != gc539InvoiceCents*int64(wantOverdue) {
		t.Errorf("the billing summary's past due is %d cents, the overdue set is %d cents",
			got.pastDueCents, gc539InvoiceCents*int64(wantOverdue))
	}
	wantAsOf := branchDayOf(world, phase.branchDayBehind)
	if got.agingAsOf != wantAsOf {
		t.Errorf("the billing summary's aging is as of %s, want the branch's day %s", got.agingAsOf, wantAsOf)
	}
}

// gc539InvoiceCents is what one of the world's invoices is worth, in cents.
const gc539InvoiceCents = 10000

// branchDayOf is the branch's own calendar day in the world: the session's
// day less how far the branch's day sits behind it.
func branchDayOf(w overdueWorld, behind int) string {
	d, err := time.Parse("2006-01-02", w.sessionDay)
	if err != nil {
		return w.sessionDay
	}
	return d.AddDate(0, 0, -behind).Format("2006-01-02")
}

// dueOf names the due date of an invoice, for the failure messages.
func dueOf(w overdueWorld, id uuid.UUID) string {
	for n, invoiceID := range w.invoiceIDs {
		if invoiceID == id {
			switch n {
			case 0:
				return w.dueOld
			case 1:
				return w.dueYesterday
			case 2:
				return w.dueToday
			}
		}
	}
	return "an unknown invoice"
}

// containsID reports whether the list holds the id.
func containsID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
