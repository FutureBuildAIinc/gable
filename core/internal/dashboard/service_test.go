// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// The dashboard is a read-only aggregate over money (today's revenue,
// outstanding AR) and it caches. Two things matter and are tested here:
//
//   - The cache must be keyed by BRANCH. A cache that ignored the branch would
//     serve one branch's revenue and AR to another for up to a minute — a
//     cross-tenant data leak in the most visible numbers in the product.
//   - Money stays int64 cents on this surface (unlike the portal), so the
//     JSON contract is pinned.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// --- fake repository -----------------------------------------------------

type call struct {
	branch *uuid.UUID
	args   []int
}

type fakeRepo struct {
	mu sync.Mutex

	summaryCalls  []call
	alertCalls    []call
	customerCalls []call
	activityCalls []call
	revenueCalls  []call

	summary  *DashboardSummary
	alerts   []InventoryAlert
	tops     []TopCustomer
	activity *OrderActivity
	revenue  []RevenueTrendPoint

	// perBranch lets a test return a different summary per branch so a
	// cross-branch cache hit is detectable by value, not just by call count.
	perBranch map[uuid.UUID]*DashboardSummary

	err error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		summary:   &DashboardSummary{TodayRevenue: 1},
		activity:  &OrderActivity{StatusBreakdown: map[string]int{}},
		perBranch: map[uuid.UUID]*DashboardSummary{},
	}
}

func key(b *uuid.UUID) uuid.UUID {
	if b == nil {
		return uuid.Nil
	}
	return *b
}

func (f *fakeRepo) GetDashboardSummary(_ context.Context, branchID *uuid.UUID) (*DashboardSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaryCalls = append(f.summaryCalls, call{branch: branchID})
	if f.err != nil {
		return nil, f.err
	}
	if s, ok := f.perBranch[key(branchID)]; ok {
		return s, nil
	}
	return f.summary, nil
}

func (f *fakeRepo) GetInventoryAlerts(_ context.Context, branchID *uuid.UUID, limit int) ([]InventoryAlert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alertCalls = append(f.alertCalls, call{branch: branchID, args: []int{limit}})
	if f.err != nil {
		return nil, f.err
	}
	return f.alerts, nil
}

func (f *fakeRepo) GetTopCustomers(_ context.Context, branchID *uuid.UUID, limit, days int) ([]TopCustomer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.customerCalls = append(f.customerCalls, call{branch: branchID, args: []int{limit, days}})
	if f.err != nil {
		return nil, f.err
	}
	return f.tops, nil
}

func (f *fakeRepo) GetOrderActivity(_ context.Context, branchID *uuid.UUID, limit int) (*OrderActivity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activityCalls = append(f.activityCalls, call{branch: branchID, args: []int{limit}})
	if f.err != nil {
		return nil, f.err
	}
	return f.activity, nil
}

func (f *fakeRepo) GetRevenueTrend(_ context.Context, branchID *uuid.UUID, days int) ([]RevenueTrendPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revenueCalls = append(f.revenueCalls, call{branch: branchID, args: []int{days}})
	if f.err != nil {
		return nil, f.err
	}
	return f.revenue, nil
}

var _ Repository = (*fakeRepo)(nil)

func branchCtx(id uuid.UUID) context.Context {
	return branchctx.With(context.Background(), &branchctx.Context{BranchID: &id})
}

// --- caching -------------------------------------------------------------

// CORRECTNESS: a repeated request inside the TTL is served from cache. That is
// the whole point of the layer.
func TestCache_RepeatWithinTTLDoesNotRequery(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := svc.GetSummary(ctx); err != nil {
			t.Fatalf("GetSummary: %v", err)
		}
	}
	if len(repo.summaryCalls) != 1 {
		t.Errorf("repository hit %d times, want 1", len(repo.summaryCalls))
	}
}

// CORRECTNESS (security): the cache must be keyed by branch. This is the test
// that would catch a global cache leaking one branch's revenue and AR to
// another. It asserts by VALUE, not by call count, so a shared cache cannot
// pass by accident.
func TestCache_IsKeyedByBranch(t *testing.T) {
	branchA, branchB := uuid.New(), uuid.New()

	repo := newFakeRepo()
	repo.perBranch[branchA] = &DashboardSummary{TodayRevenue: 111111, OutstandingAR: 999999, ActiveOrders: 3}
	repo.perBranch[branchB] = &DashboardSummary{TodayRevenue: 222222, OutstandingAR: 888888, ActiveOrders: 7}
	repo.perBranch[uuid.Nil] = &DashboardSummary{TodayRevenue: 333333, OutstandingAR: 777777, ActiveOrders: 10}

	svc := NewService(repo)

	// Warm branch A, then read branch B, then re-read A from cache.
	a1, err := svc.GetSummary(branchCtx(branchA))
	if err != nil {
		t.Fatalf("GetSummary(A): %v", err)
	}
	b1, err := svc.GetSummary(branchCtx(branchB))
	if err != nil {
		t.Fatalf("GetSummary(B): %v", err)
	}
	all, err := svc.GetSummary(context.Background()) // admin "all branches"
	if err != nil {
		t.Fatalf("GetSummary(all): %v", err)
	}
	a2, err := svc.GetSummary(branchCtx(branchA))
	if err != nil {
		t.Fatalf("GetSummary(A again): %v", err)
	}

	if a1.TodayRevenue != 111111 || a1.OutstandingAR != 999999 {
		t.Errorf("branch A summary = %+v, want its own figures", a1)
	}
	if b1.TodayRevenue != 222222 || b1.OutstandingAR != 888888 {
		t.Errorf("branch B was served branch A's cached figures: %+v", b1)
	}
	if all.TodayRevenue != 333333 {
		t.Errorf("the all-branches view was served a branch's cached figures: %+v", all)
	}
	if a2.TodayRevenue != 111111 {
		t.Errorf("branch A re-read got %+v, want its own cached figures back", a2)
	}
	if len(repo.summaryCalls) != 3 {
		t.Errorf("repository hit %d times, want 3 (one per distinct branch key; the 4th was a cache hit)", len(repo.summaryCalls))
	}
}

// CORRECTNESS: the branch from the request context is what reaches the
// repository, so the SQL filter matches what the cache was keyed on.
func TestBranchContextReachesTheRepository(t *testing.T) {
	branch := uuid.New()
	repo := newFakeRepo()
	svc := NewService(repo)

	if _, err := svc.GetSummary(branchCtx(branch)); err != nil {
		t.Fatal(err)
	}
	if got := repo.summaryCalls[0].branch; got == nil || *got != branch {
		t.Errorf("repository got branch %v, want %s", got, branch)
	}

	if _, err := svc.GetSummary(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := repo.summaryCalls[1].branch; got != nil {
		t.Errorf("repository got branch %v for an unscoped request, want nil (all branches)", got)
	}
}

// CORRECTNESS: every cached endpoint must be keyed by branch, not just the
// summary. AR-adjacent figures appear in top customers and the revenue trend
// too.
func TestCache_EveryEndpointIsBranchKeyed(t *testing.T) {
	branchA, branchB := uuid.New(), uuid.New()
	repo := newFakeRepo()
	svc := NewService(repo)

	ctxs := []context.Context{branchCtx(branchA), branchCtx(branchB), context.Background()}
	for _, ctx := range ctxs {
		// Two calls each: the second must be a cache hit.
		for i := 0; i < 2; i++ {
			if _, err := svc.GetInventoryAlerts(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.GetTopCustomers(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.GetOrderActivity(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.GetRevenueTrend(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}

	for name, calls := range map[string][]call{
		"inventory alerts": repo.alertCalls,
		"top customers":    repo.customerCalls,
		"order activity":   repo.activityCalls,
		"revenue trend":    repo.revenueCalls,
	} {
		if len(calls) != 3 {
			t.Errorf("%s: repository hit %d times, want 3 (one per branch key)", name, len(calls))
		}
		seen := map[uuid.UUID]bool{}
		for _, c := range calls {
			seen[key(c.branch)] = true
		}
		if len(seen) != 3 {
			t.Errorf("%s: repository saw %d distinct branch keys, want 3", name, len(seen))
		}
	}
}

// CORRECTNESS: the query-shaping arguments are part of the endpoint contract.
// If the top-customer window silently changed from 30 days to 7, the number on
// the dashboard would change with no other visible cause.
func TestRepositoryQueryArguments(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	ctx := context.Background()

	if _, err := svc.GetInventoryAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if got := repo.alertCalls[0].args; len(got) != 1 || got[0] != 10 {
		t.Errorf("inventory alerts limit = %v, want [10]", got)
	}

	if _, err := svc.GetTopCustomers(ctx); err != nil {
		t.Fatal(err)
	}
	if got := repo.customerCalls[0].args; len(got) != 2 || got[0] != 5 || got[1] != 30 {
		t.Errorf("top customers limit/days = %v, want [5 30]", got)
	}

	if _, err := svc.GetOrderActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if got := repo.activityCalls[0].args; len(got) != 1 || got[0] != 10 {
		t.Errorf("order activity limit = %v, want [10]", got)
	}

	if _, err := svc.GetRevenueTrend(ctx); err != nil {
		t.Fatal(err)
	}
	if got := repo.revenueCalls[0].args; len(got) != 1 || got[0] != 7 {
		t.Errorf("revenue trend days = %v, want [7]", got)
	}
}

// CORRECTNESS: a failed query must not be cached as a result. Caching an error
// as an empty dashboard would show $0 revenue for a minute after a transient
// database blip.
func TestCache_FailuresAreNotCached(t *testing.T) {
	repo := newFakeRepo()
	repo.err = errors.New("db down")
	svc := NewService(repo)
	ctx := context.Background()

	if _, err := svc.GetSummary(ctx); err == nil {
		t.Fatal("want an error")
	}
	repo.err = nil
	repo.summary = &DashboardSummary{TodayRevenue: 4242}

	got, err := svc.GetSummary(ctx)
	if err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if got == nil || got.TodayRevenue != 4242 {
		t.Fatalf("got %+v, want the real figures on the retry", got)
	}
	if len(repo.summaryCalls) != 2 {
		t.Errorf("repository hit %d times, want 2 — the failure must not have been cached", len(repo.summaryCalls))
	}
}

// CORRECTNESS: the same applies to every endpoint.
func TestCache_FailuresAreNotCachedOnAnyEndpoint(t *testing.T) {
	repo := newFakeRepo()
	repo.err = errors.New("db down")
	svc := NewService(repo)
	ctx := context.Background()

	if _, err := svc.GetInventoryAlerts(ctx); err == nil {
		t.Error("GetInventoryAlerts must propagate the failure")
	}
	if _, err := svc.GetTopCustomers(ctx); err == nil {
		t.Error("GetTopCustomers must propagate the failure")
	}
	if _, err := svc.GetOrderActivity(ctx); err == nil {
		t.Error("GetOrderActivity must propagate the failure")
	}
	if _, err := svc.GetRevenueTrend(ctx); err == nil {
		t.Error("GetRevenueTrend must propagate the failure")
	}

	repo.err = nil
	repo.alerts = []InventoryAlert{{SKU: "2X4-8", AlertType: "OUT_OF_STOCK"}}
	got, err := svc.GetInventoryAlerts(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("retry returned %v, %v; want the real alerts", got, err)
	}
	if len(repo.alertCalls) != 2 {
		t.Errorf("alerts repository hit %d times, want 2", len(repo.alertCalls))
	}
}

// CORRECTNESS: the cache is shared by concurrent HTTP handlers. Under -race
// this drives the read and write paths across several branches at once.
func TestCache_ConcurrentAccessIsSafe(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	branches := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := branchCtx(branches[i%len(branches)])
			_, _ = svc.GetSummary(ctx)
			_, _ = svc.GetInventoryAlerts(ctx)
			_, _ = svc.GetTopCustomers(ctx)
			_, _ = svc.GetOrderActivity(ctx)
			_, _ = svc.GetRevenueTrend(ctx)
		}(i)
	}
	wg.Wait()
}

// CORRECTNESS: an entry older than the TTL must be refetched. Driven by
// rewinding the stored timestamp rather than sleeping for a minute.
func TestCache_ExpiredEntryIsRefetched(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	ctx := context.Background()

	if _, err := svc.GetSummary(ctx); err != nil {
		t.Fatal(err)
	}
	if len(repo.summaryCalls) != 1 {
		t.Fatalf("repository hit %d times, want 1", len(repo.summaryCalls))
	}

	// Age the entry past the TTL.
	svc.store.summary.mu.Lock()
	svc.store.summary.entries[uuid.Nil].timestamp = time.Now().Add(-2 * svc.store.ttl)
	svc.store.summary.mu.Unlock()

	if _, err := svc.GetSummary(ctx); err != nil {
		t.Fatal(err)
	}
	if len(repo.summaryCalls) != 2 {
		t.Errorf("repository hit %d times, want 2 — an expired entry was served", len(repo.summaryCalls))
	}
}

// --- wire format ---------------------------------------------------------

// CORRECTNESS (contract): the dashboard is on the int64-cents side of the
// dollars/cents boundary. A $73.88 revenue figure is 7388 here, and every
// money field must serialise as a bare integer so a client knows to divide by
// 100 (formatCents in the frontend). If this ever emits 73.88, the frontend
// helper would render it as $7,388.00.
func TestDashboardJSON_MoneyIsIntegerCents(t *testing.T) {
	b, err := json.Marshal(DashboardSummary{
		TodayRevenue:       7388,
		TodayRevenueChange: 12.5,
		ActiveOrders:       3,
		PendingDispatch:    1,
		OutstandingAR:      1234567,
		OutstandingARCount: 9,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for field, want := range map[string]string{
		"today_revenue":        "7388",
		"outstanding_ar":       "1234567",
		"today_revenue_change": "12.5", // a percentage, not money — stays fractional
	} {
		if got := string(raw[field]); got != want {
			t.Errorf("%s = %s, want %s", field, got, want)
		}
	}

	trend, err := json.Marshal(RevenueTrendPoint{Date: "2026-03-04", Revenue: 7388})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(trend) {
		t.Fatal("invalid JSON")
	}
	var trendRaw map[string]json.RawMessage
	_ = json.Unmarshal(trend, &trendRaw)
	if got := string(trendRaw["revenue"]); got != "7388" {
		t.Errorf("revenue = %s, want integer cents", got)
	}

	top, _ := json.Marshal(TopCustomer{CustomerID: "c1", TotalRevenue: 500000})
	var topRaw map[string]json.RawMessage
	_ = json.Unmarshal(top, &topRaw)
	if got := string(topRaw["total_revenue"]); got != "500000" {
		t.Errorf("total_revenue = %s, want integer cents", got)
	}

	order, _ := json.Marshal(RecentOrder{OrderID: "o1", TotalAmount: 108250})
	var orderRaw map[string]json.RawMessage
	_ = json.Unmarshal(order, &orderRaw)
	if got := string(orderRaw["total_amount"]); got != "108250" {
		t.Errorf("total_amount = %s, want integer cents", got)
	}
}

// CHARACTERIZATION: an empty inventory-alert or top-customer result comes back
// as a nil slice, which encodes as JSON null rather than []. The dashboard
// handler does not normalise it, so clients must guard.
func TestEmptySlices_SerialiseAsNull(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)

	alerts, err := svc.GetInventoryAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if alerts != nil {
		t.Fatalf("alerts = %v; if the service now normalises to an empty slice, this characterization test should become a correctness test", alerts)
	}
	b, _ := json.Marshal(alerts)
	if string(b) != "null" {
		t.Fatalf("marshalled to %s, want null", b)
	}
}
