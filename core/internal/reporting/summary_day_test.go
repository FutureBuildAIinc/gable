// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// dayFake is a repository that can read the branch's business day, counting
// the reads, and can fail them.
type dayFake struct {
	*fakeRepo
	day   string
	err   error
	reads int32
}

func (d *dayFake) BusinessDay(context.Context) (string, error) {
	atomic.AddInt32(&d.reads, 1)
	return d.day, d.err
}

// The Sales Summary reads the yard's business day once and uses it for the
// cache key and the window both: two reads near the yard's midnight could name
// two days.
func TestGetSalesSummary_ReadsTheBusinessDayOnce(t *testing.T) {
	repo := &dayFake{fakeRepo: newFakeRepo(), day: "2026-10-14"}
	svc := NewService(repo)
	if _, err := svc.GetSalesSummary(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&repo.reads); n != 1 {
		t.Errorf("the business day was read %d times, want once", n)
	}
	if got := repo.fakeRepo.salesCalls[0][1]; got != "2026-10-14" {
		t.Errorf("window ends on %s, want the yard's day 2026-10-14", got)
	}
}

// A failed read of the business day is an error in words, never a silent
// fall back to the server's clock.
func TestGetSalesSummary_AFailedDayReadIsAnError(t *testing.T) {
	repo := &dayFake{fakeRepo: newFakeRepo(), err: errors.New("database is down")}
	svc := NewService(repo)
	_, err := svc.GetSalesSummary(context.Background(), "", "")
	if err == nil {
		t.Fatal("the summary answered with no business day, want an error")
	}
	if len(repo.fakeRepo.salesCalls) != 0 {
		t.Errorf("the repository was read %d times on the server's clock after the day failed", len(repo.fakeRepo.salesCalls))
	}
}
