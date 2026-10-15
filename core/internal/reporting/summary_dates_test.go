// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"context"
	"testing"
)

// The Sales Summary's period is business dates: what reaches the repository is
// the two dates, midnight to midnight, never a last instant of the end day.
func TestGetSalesSummary_PassesBusinessDatesNotInstants(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	if _, err := svc.GetSalesSummary(context.Background(), "2026-03-01", "2026-03-31"); err != nil {
		t.Fatal(err)
	}
	start, end := repo.salesCalls[0][0], repo.salesCalls[0][1]
	if start.Format("2006-01-02 15:04:05") != "2026-03-01 00:00:00" || end.Format("2006-01-02 15:04:05.999999999") != "2026-03-31 00:00:00" {
		t.Errorf("period = %v to %v, want the dates 2026-03-01 and 2026-03-31 at midnight", start, end)
	}
}

// A repository that cannot name the yard's business day (a fake) is an error
// for a default window, in words: there is no fall back to the server's clock
// anywhere in the window's path.
func TestGetSalesSummary_ARepositoryThatCannotReadTheDayIsAnError(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	if _, err := svc.GetSalesSummary(context.Background(), "", ""); err == nil {
		t.Fatal("a default window on the server's clock was answered, want an error")
	}
	if len(repo.salesCalls) != 0 {
		t.Errorf("the repository was read %d times on the server's clock", len(repo.salesCalls))
	}
}
