// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// dayRepo is a repository whose business day can move, counting how often the
// summary is read.
type dayRepo struct {
	Repository
	day   atomic.Value
	reads int32
}

func (r *dayRepo) BusinessDay(context.Context) (string, error) { return r.day.Load().(string), nil }

func (r *dayRepo) GetDashboardSummary(context.Context, *uuid.UUID, *string) (*DashboardSummary, error) {
	atomic.AddInt32(&r.reads, 1)
	return &DashboardSummary{}, nil
}

// The summary's cache is keyed by the branch's business day: a read after the
// branch's midnight is never the old day's cached figure.
func TestSummaryCacheIsKeyedByTheBusinessDay(t *testing.T) {
	repo := &dayRepo{}
	repo.day.Store("2026-10-14")
	svc := NewService(repo)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := svc.GetSummary(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := atomic.LoadInt32(&repo.reads); n != 1 {
		t.Fatalf("two reads on one day hit the repository %d times, want 1 (cached)", n)
	}
	repo.day.Store("2026-10-15")
	if _, err := svc.GetSummary(ctx); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&repo.reads); n != 2 {
		t.Errorf("a read on the next day hit the repository %d times in all, want 2 (a new day is not served from the old day's cache)", n)
	}
}
