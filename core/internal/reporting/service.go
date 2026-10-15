// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gablelbm/gable/pkg/branchctx"
)

const maxCacheEntries = 1000

// reachKey is the caller's reach every cached report is keyed by
// (SEC-NOHEADER-1, the principal's ruling on the reporting surface): the
// branch a header names and the user sub whose user_locations grants scope a
// no header caller. Two callers share an entry only when both are nil (the
// every branch view an administrator or owner keeps), so a primed
// administrator entry never serves a finance caller.
func reachKey(ctx context.Context) string {
	key := "all"
	if id := branchctx.IDForQuery(ctx); id != nil {
		key = "branch:" + id.String()
	} else if sub := branchctx.GrantsSubForQuery(ctx); sub != nil {
		key = "grants:" + *sub
	}
	return key
}

type cachedReport struct {
	data      interface{}
	timestamp time.Time
}

type Service struct {
	repo       Repository
	cache      map[string]cachedReport
	cacheMutex sync.RWMutex
}

func NewService(repo Repository) *Service {
	return &Service{
		repo:  repo,
		cache: make(map[string]cachedReport),
	}
}

func (s *Service) GetDailyTill(ctx context.Context, dateStr string) (*DailyTillReport, error) {
	// Cache Key
	key := "daily_till:" + reachKey(ctx) + ":" + dateStr

	// Read Cache. An empty date is the branch's today, a day that can change
	// under a cached answer at the branch's midnight and a till read a
	// moment after a payment must show it: it is never served from the cache.
	if dateStr != "" {
		s.cacheMutex.RLock()
		if item, ok := s.cache[key]; ok {
			if time.Since(item.timestamp) < 60*time.Second {
				s.cacheMutex.RUnlock()
				return item.data.(*DailyTillReport), nil
			}
		}
		s.cacheMutex.RUnlock()
	}

	// Parse date; empty is the default branch's business day (a zero date),
	// the day the dashboard's Collected today card is cut on.
	var date time.Time
	var err error
	if dateStr != "" {
		date, err = time.Parse("2006-01-02", dateStr)
		if err != nil {
			return nil, err
		}
	}
	data, err := s.repo.GetDailyTill(ctx, date)
	if err != nil {
		return nil, err
	}

	// Write Cache (a dated read only)
	if dateStr != "" {
		s.cacheMutex.Lock()
		if len(s.cache) < maxCacheEntries {
			s.cache[key] = cachedReport{data: data, timestamp: time.Now()}
		}
		s.cacheMutex.Unlock()
	}

	return data, nil
}

func (s *Service) GetSalesSummary(ctx context.Context, startStr, endStr string) (*SalesSummaryReport, error) {
	// Validate the dates first: a bad date never reaches the repository.
	var fromT, toT time.Time
	var err error
	if startStr != "" {
		if fromT, err = time.Parse("2006-01-02", startStr); err != nil {
			return nil, err
		}
	}
	if endStr != "" {
		if toT, err = time.Parse("2006-01-02", endStr); err != nil {
			return nil, err
		}
	}

	// The yard's business day, read once: the cache key and the default window
	// both use it, and a failed read is an error, never a fall back to the
	// server's clock (in the yard's evening west of UTC the server's date is
	// the yard's tomorrow). A default bound is the day, or thirty days before.
	var day string
	if startStr == "" || endStr == "" {
		if day, err = s.repo.BusinessDay(ctx); err != nil {
			return nil, fmt.Errorf("could not read the yard's business day for the summary's window: %w", err)
		}
	}

	// Cache Key
	key := "sales_summary:" + reachKey(ctx) + ":" + startStr + ":" + endStr
	if day != "" {
		key += ":" + day
	}

	// Read Cache
	s.cacheMutex.RLock()
	if item, ok := s.cache[key]; ok {
		if time.Since(item.timestamp) < 60*time.Second {
			s.cacheMutex.RUnlock()
			return item.data.(*SalesSummaryReport), nil
		}
	}
	s.cacheMutex.RUnlock()

	to := toT.Format("2006-01-02")
	from := fromT.Format("2006-01-02")
	if endStr == "" {
		to = day
	}
	if startStr == "" {
		dayT, perr := time.Parse("2006-01-02", day)
		if perr != nil {
			return nil, fmt.Errorf("the yard's business day %q is not a date: %w", day, perr)
		}
		from = dayT.AddDate(0, 0, -30).Format("2006-01-02")
	}

	data, err := s.repo.GetSalesSummary(ctx, from, to)
	if err != nil {
		return nil, err
	}

	// Write Cache
	s.cacheMutex.Lock()
	if len(s.cache) < maxCacheEntries {
		s.cache[key] = cachedReport{data: data, timestamp: time.Now()}
	}
	s.cacheMutex.Unlock()

	return data, nil
}

func (s *Service) GetARAgingReport(ctx context.Context) (*ARAgingReport, error) {
	key := "ar_aging:" + reachKey(ctx)

	s.cacheMutex.RLock()
	if item, ok := s.cache[key]; ok {
		if time.Since(item.timestamp) < 60*time.Second {
			s.cacheMutex.RUnlock()
			return item.data.(*ARAgingReport), nil
		}
	}
	s.cacheMutex.RUnlock()

	data, err := s.repo.GetARAgingReport(ctx)
	if err != nil {
		return nil, err
	}

	s.cacheMutex.Lock()
	if len(s.cache) < maxCacheEntries {
		s.cache[key] = cachedReport{data: data, timestamp: time.Now()}
	}
	s.cacheMutex.Unlock()

	return data, nil
}

func (s *Service) GetCustomerStatement(ctx context.Context, customerID, startStr, endStr string) (*CustomerStatement, error) {
	now := time.Now()
	start := now.AddDate(0, -1, 0) // Default last month
	end := now

	var err error
	if startStr != "" {
		start, err = time.Parse("2006-01-02", startStr)
		if err != nil {
			return nil, err
		}
	}
	if endStr != "" {
		end, err = time.Parse("2006-01-02", endStr)
		if err != nil {
			return nil, err
		}
		end = end.Add(24*time.Hour - time.Nanosecond)
	}

	return s.repo.GetCustomerStatement(ctx, customerID, start, end)
}

func (s *Service) CreateSavedReport(ctx context.Context, report *SavedReport) error {
	return s.repo.CreateSavedReport(ctx, report)
}

func (s *Service) GetSavedReport(ctx context.Context, id string) (*SavedReport, error) {
	return s.repo.GetSavedReport(ctx, id)
}

func (s *Service) ListSavedReports(ctx context.Context) ([]SavedReport, error) {
	return s.repo.ListSavedReports(ctx)
}

func (s *Service) UpdateSavedReport(ctx context.Context, report *SavedReport) error {
	return s.repo.UpdateSavedReport(ctx, report)
}

func (s *Service) DeleteSavedReport(ctx context.Context, id string) error {
	return s.repo.DeleteSavedReport(ctx, id)
}

func (s *Service) CreateReportSchedule(ctx context.Context, schedule *ReportSchedule) error {
	return s.repo.CreateReportSchedule(ctx, schedule)
}

func (s *Service) ListReportSchedules(ctx context.Context) ([]ReportSchedule, error) {
	return s.repo.ListReportSchedules(ctx)
}

func (s *Service) UpdateReportScheduleNextRun(ctx context.Context, scheduleID string, nextRun time.Time) error {
	return s.repo.UpdateReportScheduleNextRun(ctx, scheduleID, nextRun)
}

func (s *Service) DeleteReportSchedule(ctx context.Context, id string) error {
	return s.repo.DeleteReportSchedule(ctx, id)
}

// ScheduleCreatorGrants counts a schedule creator's current user_locations
// grants: the fail closed check the scheduler makes at every run for a
// creator the schedule does not record as an administrator.
func (s *Service) ScheduleCreatorGrants(ctx context.Context, sub string) (int, error) {
	return s.repo.ScheduleCreatorGrants(ctx, sub)
}

// PauseReportSchedule marks a schedule PAUSED after a refused run.
func (s *Service) PauseReportSchedule(ctx context.Context, id string) error {
	return s.repo.PauseReportSchedule(ctx, id)
}

// MultiBranchEnabled reads the single branch switch the branch middleware
// reads, so a scheduled run treats its creator as the middleware treats every
// caller.
func (s *Service) MultiBranchEnabled(ctx context.Context) (bool, error) {
	return s.repo.MultiBranchEnabled(ctx)
}

func (s *Service) ExecuteReportDefinition(ctx context.Context, def *ReportDefinition, entityType string) ([]map[string]interface{}, error) {
	pgRepo, ok := s.repo.(*PostgresRepository)
	if !ok {
		return nil, fmt.Errorf("report builder requires PostgresRepository")
	}
	return BuildAndExecuteQuery(ctx, pgRepo.db.Pool, def, entityType)
}
