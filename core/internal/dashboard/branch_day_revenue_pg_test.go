// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// dayWorld is a yard (and a second one) with a customer. Every payment is
// recorded through account.RecordPayment, the application's only payment
// writer, which stamps the payment's own branch_id and never an invoice_id.
type dayWorld struct {
	t           *testing.T
	db          *database.DB
	repo        *PostgresRepository
	svc         *account.Service
	yard, other uuid.UUID
	customer    uuid.UUID
	zone        string
}

func newDayWorld(t *testing.T) *dayWorld {
	t.Helper()
	db := testutil.RequireDB(t)
	// The branch is on a zone that is on an earlier day than UTC at every
	// hour; that takes the branch day exclusively for the test.
	testutil.SetBranchDayZone(t, db, testutil.AlwaysBehindUTC)
	ctx := context.Background()
	w := &dayWorld{t: t, db: db, repo: NewRepository(db), yard: uuid.New(), other: uuid.New(), customer: uuid.New(), zone: testutil.AlwaysBehindUTC,
		svc: account.NewService(db, gl.NewService(gl.NewRepository(db), nil, slog.Default()), slog.Default())}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	for _, y := range []uuid.UUID{w.yard, w.other} {
		exec(`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, y, "BDR-"+y.String()[:8])
	}
	exec(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`,
		w.customer, "Branch Day Rev "+w.customer.String()[:8], "BDR-"+w.customer.String()[:8], w.yard)
	t.Cleanup(func() {
		bg := context.Background()
		for _, q := range []string{
			`DELETE FROM payment_refunds WHERE payment_id IN (SELECT id FROM payments WHERE customer_id = $1)`,
			`DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT e.id FROM gl_journal_entries e WHERE e.source_ref_id IN
				(SELECT id FROM payments WHERE customer_id = $1) OR e.source_ref_id IN
				(SELECT r.id FROM payment_refunds r JOIN payments p ON p.id = r.payment_id WHERE p.customer_id = $1))`,
			`DELETE FROM gl_journal_entries WHERE source_ref_id IN (SELECT id FROM payments WHERE customer_id = $1)`,
			`DELETE FROM ar_applications WHERE customer_id = $1`,
			`DELETE FROM payments WHERE customer_id = $1`,
			`DELETE FROM customer_transactions WHERE customer_id = $1`,
			`DELETE FROM customers WHERE id = $1`,
		} {
			_, _ = db.Pool.Exec(bg, q, w.customer)
		}
		_, _ = db.Pool.Exec(bg, `DELETE FROM user_locations WHERE branch_id IN ($1, $2)`, w.yard, w.other)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id IN ($1, $2)`, w.yard, w.other)
	})
	return w
}

// day is the branch's business date now.
func (w *dayWorld) day() time.Time { return testutil.BranchDay(w.t, w.db) }

// record writes a cash payment at yard through the AR core and places it at
// the instant createdAt (a SQL expression of timestamptz), which the writer
// leaves at NOW().
func (w *dayWorld) record(yard uuid.UUID, cents int64, createdAt string) uuid.UUID {
	w.t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	err := w.db.RunInTx(ctx, func(ctx context.Context) error {
		var e error
		id, _, e = w.svc.RecordPayment(ctx, account.RecordPaymentIn{CustomerID: w.customer, BranchID: yard, Currency: "USD",
			Method: "CASH", AmountCents: cents, ReceivedOn: w.day(), Actor: "u-test"})
		return e
	})
	if err != nil {
		w.t.Fatalf("record payment: %v", err)
	}
	if createdAt != "NOW()" {
		if _, err := w.db.Pool.Exec(ctx, `UPDATE payments SET created_at = `+createdAt+` WHERE id = $1`, id); err != nil {
			w.t.Fatalf("place payment: %v", err)
		}
	}
	return id
}

func (w *dayWorld) refund(payment uuid.UUID, cents int64) {
	w.t.Helper()
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		_, _, e := w.svc.RefundPayment(ctx, account.RefundPaymentIn{PaymentID: payment, AmountCents: cents, Reason: "test", Actor: "u-test", On: w.day()})
		return e
	})
	if err != nil {
		w.t.Fatalf("refund: %v", err)
	}
}

func (w *dayWorld) void(payment uuid.UUID) {
	w.t.Helper()
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		_, e := w.svc.VoidPayment(ctx, account.VoidPaymentIn{PaymentID: payment, Reason: "test", Actor: "u-test", On: w.day()})
		return e
	})
	if err != nil {
		w.t.Fatalf("void: %v", err)
	}
}

// midnight is the instant the branch's today began, as a SQL expression.
func (w *dayWorld) midnight() string {
	return `((` + testutil.BranchDaySQL + `)::timestamp AT TIME ZONE '` + w.zone + `')`
}

// figures reads the card, the trend's point for today and the Daily Till for
// the reach (a yard header, or a user's grants with no header).
type figures struct{ today, yesterday, trendToday, tillNet int64 }

func (w *dayWorld) read(yard *uuid.UUID, grants *string) figures {
	w.t.Helper()
	ctx := context.Background()
	s, err := w.repo.GetDashboardSummary(ctx, yard, grants)
	if err != nil {
		w.t.Fatalf("summary: %v", err)
	}
	points, err := w.repo.GetRevenueTrend(ctx, yard, grants, 7)
	if err != nil {
		w.t.Fatalf("trend: %v", err)
	}
	today := w.day().Format("2006-01-02")
	var trendToday int64
	for _, p := range points {
		if p.Date == today {
			trendToday = p.Revenue
		}
	}
	bc := &branchctx.Context{UserSub: "u"}
	if yard != nil {
		bc.BranchID = yard
	}
	if grants != nil {
		bc.UserSub = *grants
	}
	till, err := reporting.NewService(reporting.NewRepository(w.db)).GetDailyTill(branchctx.With(ctx, bc), "")
	if err != nil {
		w.t.Fatalf("daily till: %v", err)
	}
	if till.Date != today {
		w.t.Errorf("till date = %s, want the branch's day %s", till.Date, today)
	}
	return figures{today: s.TodayRevenue, yesterday: yesterdayOf(s), trendToday: trendToday, tillNet: netCents(w.t, till)}
}

// netCents is the Daily Till's net collections in cents, read from its JSON
// (the field is net_collected), so a till without it reads zero rather than
// failing to compile.
func netCents(t *testing.T, till *reporting.DailyTillReport) int64 {
	t.Helper()
	raw, err := json.Marshal(till)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	net, _ := m["net_collected"].(float64)
	return int64(net*100 + 0.5)
}

// yesterdayOf recovers yesterday from the change figure and today's.
func yesterdayOf(s *DashboardSummary) int64 {
	if s.TodayRevenueChange == 0 {
		return 0
	}
	return int64(float64(s.TodayRevenue)/(1+s.TodayRevenueChange/100) + 0.5)
}

// CORRECTNESS (item 130, dashboard today): a payment the AR core records
// (branch_id set, invoice_id never) is at its own yard on the card, on the
// trend's point and in the Daily Till, all equal, for a yard header and for a
// granted user with no header alike.
func TestToday_APaymentRecordedAtTheYardIsOnTheCardTheTrendAndTheTill(t *testing.T) {
	w := newDayWorld(t)
	w.record(w.yard, 1234, "NOW()")
	grantee := "bdr-" + uuid.NewString()[:8]
	if _, err := w.db.Pool.Exec(context.Background(), `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, true, 'test')`, grantee, w.yard); err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]figures{
		"yard header":  w.read(&w.yard, nil),
		"granted user": w.read(nil, &grantee),
	} {
		if got.today != 1234 || got.trendToday != 1234 || got.tillNet != 1234 {
			t.Errorf("%s: card %d, trend %d, till %d, want all 1234", name, got.today, got.trendToday, got.tillNet)
		}
	}
}

// Cash a yard took belongs to that yard: a payment recorded at the other yard
// for the same customer is on that yard's card and its till, not this one's.
func TestToday_APaymentBelongsToTheYardThatTookIt(t *testing.T) {
	w := newDayWorld(t)
	w.record(w.yard, 1000, "NOW()")
	w.record(w.other, 640, "NOW()")
	a, b := w.read(&w.yard, nil), w.read(&w.other, nil)
	if a.today != 1000 || a.tillNet != 1000 || b.today != 640 || b.tillNet != 640 {
		t.Errorf("yard A card %d till %d, yard B card %d till %d, want 1000/1000 and 640/640", a.today, a.tillNet, b.today, b.tillNet)
	}
}

// A voided payment is never counted.
func TestToday_AVoidedPaymentIsNeverCounted(t *testing.T) {
	w := newDayWorld(t)
	w.record(w.yard, 1000, "NOW()")
	voided := w.record(w.yard, 3200, "NOW()")
	w.void(voided)
	got := w.read(&w.yard, nil)
	if got.today != 1000 || got.trendToday != 1000 || got.tillNet != 1000 {
		t.Errorf("card %d, trend %d, till %d, want all 1000 (the voided 3200 is out)", got.today, got.trendToday, got.tillNet)
	}
}

// The day's figure is NET collections: payments received that day less
// refunds paid out that day, a refund of an older payment included.
func TestToday_NetCollectionsAreReceivedLessRefunds(t *testing.T) {
	w := newDayWorld(t)
	same := w.record(w.yard, 4000, "NOW()")
	w.refund(same, 400) // a refund of today's payment, today
	older := w.record(w.yard, 2000, "NOW() - INTERVAL '48 hours'")
	w.refund(older, 500) // a refund today of an older payment
	got := w.read(&w.yard, nil)
	if want := int64(4000 - 400 - 500); got.today != want || got.trendToday != want || got.tillNet != want {
		t.Errorf("card %d, trend %d, till %d, want all %d (net collections)", got.today, got.trendToday, got.tillNet, want)
	}
}

// The day is cut as a half open range on the instant: one second before the
// branch's midnight is yesterday's, one second after is today's.
func TestToday_TheDaysEdgesAreTheBranchsMidnight(t *testing.T) {
	w := newDayWorld(t)
	w.record(w.yard, 100, w.midnight()+` - INTERVAL '1 second'`)                    // yesterday's last second
	w.record(w.yard, 200, w.midnight()+` + INTERVAL '1 second'`)                    // today's first second
	w.record(w.yard, 400, w.midnight()+` + INTERVAL '1 day' - INTERVAL '1 second'`) // today's last second
	w.record(w.yard, 800, w.midnight()+` + INTERVAL '1 day' + INTERVAL '1 second'`) // tomorrow
	s, err := w.repo.GetDashboardSummary(context.Background(), &w.yard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.TodayRevenue != 600 {
		t.Errorf("TodayRevenue = %d, want 600 (200 and 400)", s.TodayRevenue)
	}
	if got := yesterdayOf(s); got != 100 {
		t.Errorf("yesterday = %d, want 100", got)
	}
}

// The trend is today and the days before it, each on the branch's own day:
// seven days is today and the six before, and nothing older.
func TestTrend_TheWindowIsTodayAndTheDaysBefore(t *testing.T) {
	w := newDayWorld(t)
	for back, cents := range map[int]int64{0: 100, 6: 200, 7: 400} {
		w.record(w.yard, cents, w.midnight()+` - INTERVAL '`+strconv.Itoa(back)+` days' + INTERVAL '12 hours'`)
	}
	points, err := w.repo.GetRevenueTrend(context.Background(), &w.yard, nil, 7)
	if err != nil {
		t.Fatal(err)
	}
	day := w.day()
	want := map[string]int64{
		day.Format("2006-01-02"):                   100,
		day.AddDate(0, 0, -6).Format("2006-01-02"): 200,
	}
	if len(points) != len(want) {
		t.Fatalf("trend = %+v, want exactly %v", points, want)
	}
	for _, p := range points {
		if want[p.Date] != p.Revenue {
			t.Errorf("trend point %s = %d, want %d", p.Date, p.Revenue, want[p.Date])
		}
	}
}

// The Daily Till with no date is never served from the cache: the day it
// resolved can change at the branch's midnight and a payment made a moment ago
// must show.
func TestDailyTill_ANoDateReadIsAlwaysFresh(t *testing.T) {
	w := newDayWorld(t)
	svc := reporting.NewService(reporting.NewRepository(w.db))
	ctx := branchctx.With(context.Background(), &branchctx.Context{UserSub: "u", BranchID: &w.yard})
	w.record(w.yard, 1000, "NOW()")
	first, err := svc.GetDailyTill(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	w.record(w.yard, 500, "NOW()")
	second, err := svc.GetDailyTill(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if a, b := netCents(t, first), netCents(t, second); a != 1000 || b != 1500 {
		t.Errorf("till %d then %d cents, want 1000 then 1500", a, b)
	}
}
