// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/payment"
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

// record writes a cash payment at yard through the AR core, received on the
// branch's day plus dayOffset. The writer stamps created_at at NOW() whatever
// the date it names, as the demo import does for its whole history; no leg
// moves created_at.
func (w *dayWorld) record(yard uuid.UUID, cents int64, dayOffset int) uuid.UUID {
	w.t.Helper()
	var id uuid.UUID
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		var e error
		id, _, e = w.svc.RecordPayment(ctx, account.RecordPaymentIn{CustomerID: w.customer, BranchID: yard, Currency: "USD",
			Method: "CASH", AmountCents: cents, ReceivedOn: w.day().AddDate(0, 0, dayOffset), Actor: "u-test"})
		return e
	})
	if err != nil {
		w.t.Fatalf("record payment: %v", err)
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
	return int64(math.Round(net * 100))
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
	w.record(w.yard, 1234, 0)
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
	w.record(w.yard, 1000, 0)
	w.record(w.other, 640, 0)
	a, b := w.read(&w.yard, nil), w.read(&w.other, nil)
	if a.today != 1000 || a.tillNet != 1000 || b.today != 640 || b.tillNet != 640 {
		t.Errorf("yard A card %d till %d, yard B card %d till %d, want 1000/1000 and 640/640", a.today, a.tillNet, b.today, b.tillNet)
	}
}

// A voided payment is never counted.
func TestToday_AVoidedPaymentIsNeverCounted(t *testing.T) {
	w := newDayWorld(t)
	w.record(w.yard, 1000, 0)
	voided := w.record(w.yard, 3200, 0)
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
	same := w.record(w.yard, 4000, 0)
	w.refund(same, 400) // a refund of today's payment, today
	older := w.record(w.yard, 2000, -2)
	w.refund(older, 500) // a refund today of an older payment
	got := w.read(&w.yard, nil)
	if want := int64(4000 - 400 - 500); got.today != want || got.trendToday != want || got.tillNet != want {
		t.Errorf("card %d, trend %d, till %d, want all %d (net collections)", got.today, got.trendToday, got.tillNet, want)
	}
}

// The day is the payment's own business date (received_on): the day before
// and the day after are not today's, for the card, the trend and the till.
func TestToday_TheDaysEdgesAreTheBusinessDate(t *testing.T) {
	w := newDayWorld(t)
	w.record(w.yard, 100, -1) // yesterday's
	w.record(w.yard, 200, 0)  // today's
	w.record(w.yard, 400, 0)  // today's
	w.record(w.yard, 800, 1)  // tomorrow's: not in today
	ctx := context.Background()
	s, err := w.repo.GetDashboardSummary(ctx, &w.yard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.TodayRevenue != 600 {
		t.Errorf("TodayRevenue = %d, want 600 (200 and 400)", s.TodayRevenue)
	}
	if got := yesterdayOf(s); got != 100 {
		t.Errorf("yesterday = %d, want 100", got)
	}
	points, err := w.repo.GetRevenueTrend(ctx, &w.yard, nil, 7)
	if err != nil {
		t.Fatal(err)
	}
	byDay := map[string]int64{}
	for _, p := range points {
		byDay[p.Date] = p.Revenue
	}
	today, yest := w.day().Format("2006-01-02"), w.day().AddDate(0, 0, -1).Format("2006-01-02")
	if byDay[today] != 600 || byDay[yest] != 100 || len(byDay) != 2 {
		t.Errorf("trend = %v, want today 600 and yesterday 100 only", byDay)
	}
	svc := reporting.NewService(reporting.NewRepository(w.db))
	bctx := branchctx.With(ctx, &branchctx.Context{UserSub: "u", BranchID: &w.yard})
	for day, want := range map[string]int64{today: 600, yest: 100} {
		till, err := svc.GetDailyTill(bctx, day)
		if err != nil {
			t.Fatal(err)
		}
		if got := netCents(t, till); got != want {
			t.Errorf("till for %s = %d, want %d", day, got, want)
		}
	}
}

// The seeded demo's shape: every payment shares one created_at (the seed's
// instant) and its received_on spreads over months. Today's card, the trend's
// points and the till follow received_on, never created_at.
func TestToday_FollowsReceivedOnNotTheSharedCreatedAt(t *testing.T) {
	w := newDayWorld(t)
	var total int64
	for off := -60; off <= 0; off += 6 {
		w.record(w.yard, int64(1000+off), off)
		total += int64(1000 + off)
	}
	w.record(w.yard, 4000, -7) // seven days back: outside a seven day window
	ctx := context.Background()
	s, err := w.repo.GetDashboardSummary(ctx, &w.yard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.TodayRevenue != 1000 {
		t.Errorf("TodayRevenue = %d, want only today's 1000 (the whole history is %d)", s.TodayRevenue, total)
	}
	points, err := w.repo.GetRevenueTrend(ctx, &w.yard, nil, 7)
	if err != nil {
		t.Fatal(err)
	}
	// Seven days back from today take offsets 0 and -6.
	want := map[string]int64{
		w.day().Format("2006-01-02"):                   1000,
		w.day().AddDate(0, 0, -6).Format("2006-01-02"): 994,
	}
	if len(points) != len(want) {
		t.Fatalf("trend = %+v, want exactly %v", points, want)
	}
	for _, p := range points {
		if want[p.Date] != p.Revenue {
			t.Errorf("trend %s = %d, want %d", p.Date, p.Revenue, want[p.Date])
		}
	}
	if got := w.read(&w.yard, nil); got.tillNet != 1000 {
		t.Errorf("till = %d, want 1000", got.tillNet)
	}
}

// A payment recorded today with a received_on of yesterday is yesterday's: the
// card's comparison, the trend and the till never count it as today's.
func TestToday_ABackdatedPaymentIsOnTheDayItNames(t *testing.T) {
	w := newDayWorld(t)
	w.record(w.yard, 1000, 0)
	w.record(w.yard, 500, -1)
	got := w.read(&w.yard, nil)
	if got.today != 1000 || got.tillNet != 1000 || got.trendToday != 1000 {
		t.Errorf("card %d, trend %d, till %d, want all 1000", got.today, got.trendToday, got.tillNet)
	}
	if got.yesterday != 500 {
		t.Errorf("yesterday = %d, want the backdated 500", got.yesterday)
	}
}

// A refund today of an older payment alone makes today's net negative, read
// as such on the card, the trend and the till.
func TestToday_ARefundOnlyDayIsNegative(t *testing.T) {
	w := newDayWorld(t)
	older := w.record(w.yard, 2000, -3)
	w.refund(older, 500)
	got := w.read(&w.yard, nil)
	if got.today != -500 || got.trendToday != -500 || got.tillNet != -500 {
		t.Errorf("card %d, trend %d, till %d, want all -500", got.today, got.trendToday, got.tillNet)
	}
}

// The Daily Till with no date is never served from the cache: the day it
// resolved can change at the branch's midnight and a payment made a moment ago
// must show.
func TestDailyTill_ANoDateReadIsAlwaysFresh(t *testing.T) {
	w := newDayWorld(t)
	svc := reporting.NewService(reporting.NewRepository(w.db))
	ctx := branchctx.With(context.Background(), &branchctx.Context{UserSub: "u", BranchID: &w.yard})
	w.record(w.yard, 1000, 0)
	first, err := svc.GetDailyTill(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	w.record(w.yard, 500, 0)
	second, err := svc.GetDailyTill(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if a, b := netCents(t, first), netCents(t, second); a != 1000 || b != 1500 {
		t.Errorf("till %d then %d cents, want 1000 then 1500", a, b)
	}
}

// The Daily Till's Sales Summary counts collections the same way: by the
// payment's business date, voided payments out and refunds in.
func TestSalesSummary_CollectedIsNetOfRefundsAndVoids(t *testing.T) {
	w := newDayWorld(t)
	kept := w.record(w.yard, 1000, -2)
	voided := w.record(w.yard, 3200, -2)
	w.void(voided)
	w.record(w.yard, 700, -10) // outside the period
	w.refund(kept, 400)        // refunded today, inside the period
	start := w.day().AddDate(0, 0, -3).Format("2006-01-02")
	end := w.day().Format("2006-01-02")
	ctx := branchctx.With(context.Background(), &branchctx.Context{UserSub: "u", BranchID: &w.yard})
	got, err := reporting.NewService(reporting.NewRepository(w.db)).GetSalesSummary(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if math.Round(got.TotalCollected*100) != 600 {
		t.Errorf("TotalCollected = %v, want 6.00 (1000 less the 400 refund; the voided 3200 and the 700 outside are out)", got.TotalCollected)
	}
}

// With no start and end the Sales Summary's window is the yard's business day
// and the thirty days before it, not the server's: a payment dated the day
// after the yard's today is out, and one dated thirty days before it is in.
func TestSalesSummary_TheDefaultWindowIsTheYardsDay(t *testing.T) {
	w := newDayWorld(t)
	w.record(w.yard, 500, 0)
	w.record(w.yard, 700, -30) // the window's first day
	w.record(w.yard, 900, 1)   // the yard's tomorrow: never in it
	ctx := branchctx.With(context.Background(), &branchctx.Context{UserSub: "u", BranchID: &w.yard})
	got, err := reporting.NewService(reporting.NewRepository(w.db)).GetSalesSummary(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if math.Round(got.TotalCollected*100) != 1200 {
		t.Errorf("TotalCollected = %v, want 12.00 (500 today and 700 thirty days back; the yard's tomorrow 900 out)", got.TotalCollected)
	}
	if want := w.day().Format("2006-01-02"); got.EndDate != want {
		t.Errorf("EndDate = %s, want the yard's day %s", got.EndDate, want)
	}
}

// A payment taken through the payment service with no received_on is dated by
// its own yard's day, so it is on today's card (the yard's evening, west of
// UTC, is the case this PR was opened for).
func TestToday_APaymentWithNoReceivedOnLandsOnTheYardsDay(t *testing.T) {
	w := newDayWorld(t)
	if _, err := w.db.Pool.Exec(context.Background(), `UPDATE locations SET timezone = $2 WHERE id = $1`, w.yard, w.zone); err != nil {
		t.Fatal(err)
	}
	svc := payment.NewService(w.db, payment.NewRepository(w.db), w.svc)
	if _, err := svc.Create(context.Background(), &payment.Input{CustomerID: w.customer, BranchID: &w.yard, AmountCents: 1500, Method: payment.PaymentMethod("CASH")},
		payment.Caller{Actor: "u-test"}); err != nil {
		t.Fatalf("create payment: %v", err)
	}
	got := w.read(&w.yard, nil)
	if got.today != 1500 || got.trendToday != 1500 || got.tillNet != 1500 {
		t.Errorf("card %d, trend %d, till %d, want all 1500", got.today, got.trendToday, got.tillNet)
	}
}

// invoice records an UNPAID invoice at yard whose business date is the yard's
// today plus dayOffset, made now (created_at is the instant the row is
// written, as the document's date is its own).
func (w *dayWorld) invoice(cents int64, dayOffset int) {
	w.t.Helper()
	id := uuid.New()
	if _, err := w.db.Pool.Exec(context.Background(), `INSERT INTO invoices (id, customer_id, branch_id, status, origin, number, currency,
			total_amount, subtotal, tax_rate, tax_amount, amount_open, due_date, invoice_date)
		VALUES ($1, $2, $3, 'UNPAID', 'POS', $4, 'USD', $5::bigint::numeric / 100, $5::bigint::numeric / 100, 0, 0, $5::bigint::numeric / 100,
			$6::date + 30, $6::date)`,
		id, w.customer, w.yard, "IN-"+id.String()[:8], cents, w.day().AddDate(0, 0, dayOffset).Format("2006-01-02")); err != nil {
		w.t.Fatalf("seed invoice: %v", err)
	}
}

// The invoiced and outstanding halves cut on the document's own business date
// (invoice_date), the same dates the collected half uses: an invoice made now
// at a yard west of UTC with its date the yard's today is in the default
// window; one dated thirty days back is in, thirty one is out.
func TestSalesSummary_InvoicesAreCutOnTheirBusinessDate(t *testing.T) {
	w := newDayWorld(t)
	w.invoice(12345, 0)
	w.invoice(2000, -30)
	w.invoice(4000, -31)
	ctx := branchctx.With(context.Background(), &branchctx.Context{UserSub: "u", BranchID: &w.yard})
	svc := reporting.NewService(reporting.NewRepository(w.db))

	got, err := svc.GetSalesSummary(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if math.Round(got.TotalInvoiced*100) != 14345 || got.InvoiceCount != 2 || math.Round(got.OutstandingAR*100) != 14345 {
		t.Errorf("invoiced %v over %d, outstanding %v, want 143.45 over 2 and 143.45 (123.45 today and 20.00 thirty days back; the thirty first day's 40.00 out)",
			got.TotalInvoiced, got.InvoiceCount, got.OutstandingAR)
	}
	day := w.day().Format("2006-01-02")
	explicit, err := svc.GetSalesSummary(ctx, day, day)
	if err != nil {
		t.Fatal(err)
	}
	if math.Round(explicit.TotalInvoiced*100) != 12345 || explicit.InvoiceCount != 1 {
		t.Errorf("explicit day: invoiced %v over %d, want 123.45 over 1", explicit.TotalInvoiced, explicit.InvoiceCount)
	}
}
