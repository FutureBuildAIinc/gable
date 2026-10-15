// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// dayWorld is a yard with one customer and one invoice, so a payment made
// against it is walled to the yard.
type dayWorld struct {
	t             *testing.T
	repo          *PostgresRepository
	yard, invoice uuid.UUID
	customer      uuid.UUID
	pay           func(amount string, createdAt string)
}

func newDayWorld(t *testing.T) *dayWorld {
	t.Helper()
	db := testutil.RequireDB(t)
	// The branch is put on a zone that is on an earlier day than UTC at every
	// hour; that takes the branch day exclusively (it is held for the test).
	testutil.SetBranchDayZone(t, db, testutil.AlwaysBehindUTC)
	ctx := context.Background()
	w := &dayWorld{t: t, repo: NewRepository(db), yard: uuid.New(), customer: uuid.New(), invoice: uuid.New()}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, w.yard, "BDR-"+w.yard.String()[:8])
	exec(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`,
		w.customer, "Branch Day Rev "+w.customer.String()[:8], "BDR-"+w.customer.String()[:8], w.yard)
	exec(`INSERT INTO invoices (id, customer_id, branch_id, status, origin, number, currency,
			total_amount, subtotal, tax_rate, tax_amount, amount_open, due_date, invoice_date)
		VALUES ($1, $2, $3, 'UNPAID', 'POS', $4, 'USD', 500, 500, 0, 0, 500, CURRENT_DATE + 30, CURRENT_DATE)`,
		w.invoice, w.customer, w.yard, "IN-"+w.invoice.String()[:8])
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = db.Pool.Exec(bg, `DELETE FROM payments WHERE customer_id = $1`, w.customer)
		_, _ = db.Pool.Exec(bg, `DELETE FROM invoices WHERE customer_id = $1`, w.customer)
		_, _ = db.Pool.Exec(bg, `DELETE FROM customers WHERE id = $1`, w.customer)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, w.yard)
	})
	w.pay = func(amount, createdAt string) {
		t.Helper()
		exec(`INSERT INTO payments (invoice_id, customer_id, branch_id, currency, received_on, amount, method, amount_unapplied, created_at)
			VALUES ($1, $2, $3, 'USD', CURRENT_DATE, $4::numeric, 'CASH', 0, `+createdAt+`)`,
			w.invoice, w.customer, w.yard, amount)
	}
	return w
}

// CORRECTNESS (item 130, dashboard today): the dashboard's day is the default
// branch's business day, as the open receivable's is, never the server's. The
// branch sits in a zone on an earlier day than UTC at every hour; the server's
// clock (a day ahead here, as it is through the branch's evening) must not move
// today's figure.
func TestGetDashboardSummary_TodayAndYesterdayAreTheBranchsDay(t *testing.T) {
	w := newDayWorld(t)
	w.repo.now = func() time.Time { return time.Now().AddDate(0, 0, 1) }

	w.pay("12.34", "NOW()")                      // made in the branch's today
	w.pay("5.00", "NOW() - INTERVAL '24 hours'") // the branch's yesterday

	s, err := w.repo.GetDashboardSummary(context.Background(), &w.yard, nil)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if s.TodayRevenue != 1234 {
		t.Errorf("TodayRevenue = %d, want 1234 (the payment made in the branch's day)", s.TodayRevenue)
	}
	if s.TodayRevenueChange != (float64(1234-500)/500)*100 {
		t.Errorf("TodayRevenueChange = %v, want the change against the branch's yesterday (500)", s.TodayRevenueChange)
	}
}

// The revenue trend's days are the branch's days too.
func TestGetRevenueTrend_DaysAreTheBranchsDays(t *testing.T) {
	w := newDayWorld(t)
	db := testutil.RequireDB(t)
	w.pay("7.00", "NOW()")
	today := testutil.BranchDay(t, db).Format("2006-01-02")

	points, err := w.repo.GetRevenueTrend(context.Background(), &w.yard, nil, 7)
	if err != nil {
		t.Fatalf("trend: %v", err)
	}
	if len(points) != 1 || points[0].Date != today || points[0].Revenue != 700 {
		t.Errorf("trend = %+v, want one point of 700 on the branch's day %s", points, today)
	}
}

// The Daily Till the Today's Revenue card opens reads the same day: with no
// date it is the branch's business day, and its total equals the card's.
func TestDailyTill_IsTheCardsDay(t *testing.T) {
	w := newDayWorld(t)
	db := testutil.RequireDB(t)
	w.pay("12.34", "NOW()")
	ctx := branchctx.With(context.Background(), &branchctx.Context{UserSub: "u", BranchID: &w.yard})

	card, err := w.repo.GetDashboardSummary(ctx, &w.yard, nil)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	till, err := reporting.NewService(reporting.NewRepository(db)).GetDailyTill(ctx, "")
	if err != nil {
		t.Fatalf("daily till: %v", err)
	}
	if want := testutil.BranchDay(t, db).Format("2006-01-02"); till.Date != want {
		t.Errorf("till date = %s, want the branch's day %s", till.Date, want)
	}
	if got := int64(till.TotalCollected*100 + 0.5); got != card.TodayRevenue || card.TodayRevenue != 1234 {
		t.Errorf("till %d cents vs card %d cents, want both 1234", got, card.TodayRevenue)
	}
}
