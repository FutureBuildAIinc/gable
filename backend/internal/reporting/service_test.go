// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// --- fake repository -----------------------------------------------------

type fakeRepo struct {
	mu sync.Mutex

	tillCalls      []time.Time
	salesCalls     [][2]time.Time
	agingCalls     int
	statementCalls []struct {
		customerID string
		start, end time.Time
	}

	till      *DailyTillReport
	sales     *SalesSummaryReport
	aging     *ARAgingReport
	statement *CustomerStatement

	err error

	saved     map[string]*SavedReport
	schedules []ReportSchedule
	savedErr  error
	nextRuns  map[string]time.Time
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		till:      &DailyTillReport{TotalCollected: 1, ByMethod: map[string]float64{}},
		sales:     &SalesSummaryReport{},
		aging:     &ARAgingReport{},
		statement: &CustomerStatement{},
		saved:     map[string]*SavedReport{},
		nextRuns:  map[string]time.Time{},
	}
}

func (f *fakeRepo) GetDailyTill(_ context.Context, date time.Time) (*DailyTillReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tillCalls = append(f.tillCalls, date)
	if f.err != nil {
		return nil, f.err
	}
	return f.till, nil
}

func (f *fakeRepo) GetSalesSummary(_ context.Context, start, end time.Time) (*SalesSummaryReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.salesCalls = append(f.salesCalls, [2]time.Time{start, end})
	if f.err != nil {
		return nil, f.err
	}
	return f.sales, nil
}

func (f *fakeRepo) GetARAgingReport(context.Context) (*ARAgingReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agingCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.aging, nil
}

func (f *fakeRepo) GetCustomerStatement(_ context.Context, customerID string, start, end time.Time) (*CustomerStatement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statementCalls = append(f.statementCalls, struct {
		customerID string
		start, end time.Time
	}{customerID, start, end})
	if f.err != nil {
		return nil, f.err
	}
	return f.statement, nil
}

func (f *fakeRepo) CreateSavedReport(_ context.Context, r *SavedReport) error {
	if f.savedErr != nil {
		return f.savedErr
	}
	f.saved[r.ID] = r
	return nil
}

func (f *fakeRepo) GetSavedReport(_ context.Context, id string) (*SavedReport, error) {
	if f.savedErr != nil {
		return nil, f.savedErr
	}
	r, ok := f.saved[id]
	if !ok {
		return nil, errors.New("saved report not found")
	}
	return r, nil
}

func (f *fakeRepo) ListSavedReports(context.Context) ([]SavedReport, error) {
	out := make([]SavedReport, 0, len(f.saved))
	for _, r := range f.saved {
		out = append(out, *r)
	}
	return out, f.savedErr
}

func (f *fakeRepo) UpdateSavedReport(_ context.Context, r *SavedReport) error {
	if f.savedErr != nil {
		return f.savedErr
	}
	f.saved[r.ID] = r
	return nil
}

func (f *fakeRepo) DeleteSavedReport(_ context.Context, id string) error {
	if f.savedErr != nil {
		return f.savedErr
	}
	delete(f.saved, id)
	return nil
}

func (f *fakeRepo) CreateReportSchedule(_ context.Context, s *ReportSchedule) error {
	if f.savedErr != nil {
		return f.savedErr
	}
	f.schedules = append(f.schedules, *s)
	return nil
}

func (f *fakeRepo) ListReportSchedules(context.Context) ([]ReportSchedule, error) {
	return f.schedules, f.savedErr
}

func (f *fakeRepo) UpdateReportScheduleNextRun(_ context.Context, id string, next time.Time) error {
	if f.savedErr != nil {
		return f.savedErr
	}
	f.nextRuns[id] = next
	return nil
}

func (f *fakeRepo) DeleteReportSchedule(_ context.Context, id string) error {
	if f.savedErr != nil {
		return f.savedErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.schedules[:0]
	for _, s := range f.schedules {
		if s.ID != id {
			kept = append(kept, s)
		}
	}
	f.schedules = kept
	return nil
}

var _ Repository = (*fakeRepo)(nil)

// --- date-window handling ------------------------------------------------

// CORRECTNESS: a report is only as trustworthy as the window it covers. An
// unparseable date must be rejected rather than silently reinterpreted, and an
// end date must include the whole of that day — an invoice raised at 16:00 on
// the last day of a period belongs in the period.
func TestGetSalesSummary_DateWindow(t *testing.T) {
	t.Run("explicit window covers the whole end day", func(t *testing.T) {
		repo := newFakeRepo()
		svc := NewService(repo)

		if _, err := svc.GetSalesSummary(context.Background(), "2026-03-01", "2026-03-31"); err != nil {
			t.Fatalf("GetSalesSummary: %v", err)
		}
		if len(repo.salesCalls) != 1 {
			t.Fatalf("repository called %d times, want 1", len(repo.salesCalls))
		}
		start, end := repo.salesCalls[0][0], repo.salesCalls[0][1]
		if start.Format("2006-01-02 15:04:05") != "2026-03-01 00:00:00" {
			t.Errorf("start = %v, want the beginning of 2026-03-01", start)
		}
		if end.Format("2006-01-02") != "2026-03-31" {
			t.Errorf("end = %v, want a time on 2026-03-31", end)
		}
		// 23:59:59.999999999 — the last instant of the day.
		if end.Hour() != 23 || end.Minute() != 59 || end.Second() != 59 {
			t.Errorf("end = %v, want the last instant of the end day so same-day activity is included", end)
		}
	})

	t.Run("an empty window defaults to the last 30 days", func(t *testing.T) {
		repo := newFakeRepo()
		svc := NewService(repo)

		before := time.Now()
		if _, err := svc.GetSalesSummary(context.Background(), "", ""); err != nil {
			t.Fatalf("GetSalesSummary: %v", err)
		}
		start, end := repo.salesCalls[0][0], repo.salesCalls[0][1]
		span := end.Sub(start)
		if span < 29*24*time.Hour || span > 31*24*time.Hour {
			t.Errorf("default window spans %v, want about 30 days", span)
		}
		if end.Before(before.Add(-time.Minute)) {
			t.Errorf("default end = %v, want approximately now", end)
		}
	})

	t.Run("unparseable dates are rejected without querying", func(t *testing.T) {
		for _, tc := range [][2]string{
			{"03/01/2026", "2026-03-31"},
			{"2026-03-01", "31-03-2026"},
			{"2026-13-01", "2026-03-31"},
		} {
			repo := newFakeRepo()
			svc := NewService(repo)
			if _, err := svc.GetSalesSummary(context.Background(), tc[0], tc[1]); err == nil {
				t.Errorf("GetSalesSummary(%q, %q) succeeded, want a parse error", tc[0], tc[1])
			}
			if len(repo.salesCalls) != 0 {
				t.Errorf("queried the database despite a bad date: %v", repo.salesCalls)
			}
		}
	})
}

// CORRECTNESS: the same window rules apply to a customer statement, which is a
// document a customer is sent and may pay against.
func TestGetCustomerStatement_DateWindow(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)

	if _, err := svc.GetCustomerStatement(context.Background(), "cust-1", "2026-03-01", "2026-03-31"); err != nil {
		t.Fatalf("GetCustomerStatement: %v", err)
	}
	if len(repo.statementCalls) != 1 {
		t.Fatalf("repository called %d times, want 1", len(repo.statementCalls))
	}
	call := repo.statementCalls[0]
	if call.customerID != "cust-1" {
		t.Errorf("customerID = %q, want cust-1", call.customerID)
	}
	if call.end.Hour() != 23 || call.end.Minute() != 59 {
		t.Errorf("end = %v, want the last instant of 2026-03-31", call.end)
	}

	// Defaults to the previous month.
	repo2 := newFakeRepo()
	if _, err := NewService(repo2).GetCustomerStatement(context.Background(), "cust-1", "", ""); err != nil {
		t.Fatalf("GetCustomerStatement (defaults): %v", err)
	}
	span := repo2.statementCalls[0].end.Sub(repo2.statementCalls[0].start)
	if span < 27*24*time.Hour || span > 32*24*time.Hour {
		t.Errorf("default statement window spans %v, want about one month", span)
	}

	// And a bad date must not reach the database.
	repo3 := newFakeRepo()
	if _, err := NewService(repo3).GetCustomerStatement(context.Background(), "cust-1", "nope", ""); err == nil {
		t.Fatal("want a parse error")
	}
	if len(repo3.statementCalls) != 0 {
		t.Error("queried the database despite a bad start date")
	}
}

// CORRECTNESS: an omitted till date means today, and an explicit date is used
// verbatim rather than being shifted by a timezone conversion.
func TestGetDailyTill_DateResolution(t *testing.T) {
	repo := newFakeRepo()
	if _, err := NewService(repo).GetDailyTill(context.Background(), "2026-03-04"); err != nil {
		t.Fatalf("GetDailyTill: %v", err)
	}
	if got := repo.tillCalls[0].Format("2006-01-02"); got != "2026-03-04" {
		t.Errorf("queried %s, want 2026-03-04", got)
	}

	repo2 := newFakeRepo()
	if _, err := NewService(repo2).GetDailyTill(context.Background(), ""); err != nil {
		t.Fatalf("GetDailyTill(\"\"): %v", err)
	}
	if got := repo2.tillCalls[0].Format("2006-01-02"); got != time.Now().Format("2006-01-02") {
		t.Errorf("queried %s, want today", got)
	}

	repo3 := newFakeRepo()
	if _, err := NewService(repo3).GetDailyTill(context.Background(), "04/03/2026"); err == nil {
		t.Fatal("want a parse error for a non-ISO date")
	}
	if len(repo3.tillCalls) != 0 {
		t.Error("queried the database despite a bad date")
	}
}

// --- caching -------------------------------------------------------------

// CORRECTNESS: the cache exists so a dashboard refresh does not re-run an
// expensive aggregate. It must return the same figure for the same question,
// and must key on the question — two different dates must not share an answer.
func TestReportCache_HitsAndKeying(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := svc.GetDailyTill(ctx, "2026-03-04"); err != nil {
			t.Fatalf("GetDailyTill: %v", err)
		}
	}
	if len(repo.tillCalls) != 1 {
		t.Errorf("repository hit %d times for one date, want 1 (the cache did not serve the repeat)", len(repo.tillCalls))
	}

	if _, err := svc.GetDailyTill(ctx, "2026-03-05"); err != nil {
		t.Fatalf("GetDailyTill: %v", err)
	}
	if len(repo.tillCalls) != 2 {
		t.Errorf("repository hit %d times for two distinct dates, want 2 — a different date must not reuse a cached answer", len(repo.tillCalls))
	}

	// Sales summaries key on both ends of the window.
	if _, err := svc.GetSalesSummary(ctx, "2026-03-01", "2026-03-31"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetSalesSummary(ctx, "2026-03-01", "2026-03-31"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetSalesSummary(ctx, "2026-03-01", "2026-04-30"); err != nil {
		t.Fatal(err)
	}
	if len(repo.salesCalls) != 2 {
		t.Errorf("sales repository hit %d times, want 2 (one cached repeat, one distinct window)", len(repo.salesCalls))
	}

	// AR aging has a single key.
	for i := 0; i < 3; i++ {
		if _, err := svc.GetARAgingReport(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if repo.agingCalls != 1 {
		t.Errorf("aging repository hit %d times, want 1", repo.agingCalls)
	}
}

// CORRECTNESS: a failed query must not be cached as a success, and must not
// poison the next attempt.
func TestReportCache_FailuresAreNotCached(t *testing.T) {
	repo := newFakeRepo()
	repo.err = errors.New("db down")
	svc := NewService(repo)
	ctx := context.Background()

	if _, err := svc.GetARAgingReport(ctx); err == nil {
		t.Fatal("want an error")
	}
	repo.err = nil
	got, err := svc.GetARAgingReport(ctx)
	if err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if got == nil {
		t.Fatal("second attempt returned nil after the first failed")
	}
	if repo.agingCalls != 2 {
		t.Errorf("repository hit %d times, want 2 — the failed attempt must not have been cached", repo.agingCalls)
	}
}

// CORRECTNESS: the cache is shared by concurrent HTTP handlers, so it must be
// safe under -race. This drives every cached entry point at once.
func TestReportCache_ConcurrentAccessIsSafe(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = svc.GetDailyTill(ctx, "2026-03-04")
			_, _ = svc.GetSalesSummary(ctx, "2026-03-01", "2026-03-31")
			_, _ = svc.GetARAgingReport(ctx)
		}(i)
	}
	wg.Wait()
}

// CHARACTERIZATION: the cache is capped at maxCacheEntries and has no
// eviction. Once the cap is reached, nothing is ever written again — including
// the refresh of an entry that has already expired — so the cache silently
// stops working and the stale keys occupy the map forever. Pinned here so that
// adding real eviction is a deliberate, visible change.
func TestReportCache_StopsWritingOnceCapped(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	ctx := context.Background()

	// Fill the cache to its cap with distinct keys.
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < maxCacheEntries; i++ {
		if _, err := svc.GetDailyTill(ctx, base.AddDate(0, 0, i).Format("2006-01-02")); err != nil {
			t.Fatalf("filling cache: %v", err)
		}
	}
	if len(svc.cache) != maxCacheEntries {
		t.Fatalf("cache holds %d entries, want %d", len(svc.cache), maxCacheEntries)
	}

	callsBefore := len(repo.tillCalls)
	newKey := base.AddDate(0, 0, maxCacheEntries+1).Format("2006-01-02")
	for i := 0; i < 3; i++ {
		if _, err := svc.GetDailyTill(ctx, newKey); err != nil {
			t.Fatalf("GetDailyTill: %v", err)
		}
	}
	if got := len(repo.tillCalls) - callsBefore; got != 3 {
		t.Errorf("repository hit %d times for 3 identical requests past the cap, want 3: a capped cache never caches again", got)
	}
	if len(svc.cache) != maxCacheEntries {
		t.Errorf("cache grew to %d entries past its cap of %d", len(svc.cache), maxCacheEntries)
	}
}

// --- saved reports and schedules ----------------------------------------

// CORRECTNESS: the saved-report CRUD is the persistence layer for report
// definitions; each call must reach the repository and surface its failures.
func TestSavedReportsPassThroughAndPropagateErrors(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	ctx := context.Background()

	r := &SavedReport{ID: "r1", Name: "AR by customer", EntityType: "invoices"}
	if err := svc.CreateSavedReport(ctx, r); err != nil {
		t.Fatalf("CreateSavedReport: %v", err)
	}
	got, err := svc.GetSavedReport(ctx, "r1")
	if err != nil {
		t.Fatalf("GetSavedReport: %v", err)
	}
	if got.Name != "AR by customer" {
		t.Errorf("Name = %q", got.Name)
	}
	if list, err := svc.ListSavedReports(ctx); err != nil || len(list) != 1 {
		t.Fatalf("ListSavedReports = %v, %v; want one report", list, err)
	}

	r.Name = "renamed"
	if err := svc.UpdateSavedReport(ctx, r); err != nil {
		t.Fatalf("UpdateSavedReport: %v", err)
	}
	if got, _ := svc.GetSavedReport(ctx, "r1"); got.Name != "renamed" {
		t.Errorf("update did not stick: %q", got.Name)
	}

	if err := svc.DeleteSavedReport(ctx, "r1"); err != nil {
		t.Fatalf("DeleteSavedReport: %v", err)
	}
	if _, err := svc.GetSavedReport(ctx, "r1"); err == nil {
		t.Error("want an error after the report was deleted")
	}

	repo.savedErr = errors.New("db down")
	if err := svc.CreateSavedReport(ctx, r); err == nil {
		t.Error("CreateSavedReport must propagate repository failures")
	}
	if _, err := svc.ListSavedReports(ctx); err == nil {
		t.Error("ListSavedReports must propagate repository failures")
	}
	if err := svc.CreateReportSchedule(ctx, &ReportSchedule{ID: "s1"}); err == nil {
		t.Error("CreateReportSchedule must propagate repository failures")
	}
	if err := svc.UpdateReportScheduleNextRun(ctx, "s1", time.Now()); err == nil {
		t.Error("UpdateReportScheduleNextRun must propagate repository failures")
	}
}

// CORRECTNESS: the ad-hoc builder needs the raw pgx pool, which only the
// Postgres repository owns. Any other repository must produce a clear error
// rather than a nil-pointer panic inside the query builder.
func TestExecuteReportDefinition_RequiresPostgresRepository(t *testing.T) {
	svc := NewService(newFakeRepo())
	def := &ReportDefinition{Columns: []ReportColumn{{Field: "id"}}}

	_, err := svc.ExecuteReportDefinition(context.Background(), def, "invoices")
	if err == nil {
		t.Fatal("want an error when the repository is not the Postgres one")
	}
	if want := "report builder requires PostgresRepository"; err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}
