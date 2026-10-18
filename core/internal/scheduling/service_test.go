// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package scheduling

import (
	"testing"

	"github.com/google/uuid"
)

// The window definition's own rules, before any database: the two kinds, the
// weekday range, hh:mm times with end after start, positive capacity and a
// non negative cutoff.
func TestService_Validation(t *testing.T) {
	svc := NewService(nil)
	yard := uuid.New()
	req := func(kind string, weekday int, start, end string, capacity, cutoff int) WindowRequest {
		return WindowRequest{YardID: &yard, Kind: &kind, Weekday: &weekday,
			StartTime: &start, EndTime: &end, Capacity: &capacity, CutoffMinutes: &cutoff}
	}

	// With a nil repository a valid payload reaches persistence and panics;
	// each invalid one returns a validation error instead.
	mustRefuse := func(r WindowRequest, why string) {
		t.Helper()
		defer func() {
			if recover() != nil {
				t.Errorf("%s: the payload reached persistence with a nil repository", why)
			}
		}()
		if _, err := svc.Create(t.Context(), r); err == nil {
			t.Errorf("%s: accepted", why)
		}
	}
	mustRefuse(req("courier", 3, "08:00", "12:00", 2, 0), "an unknown kind")
	mustRefuse(req("delivery", 7, "08:00", "12:00", 2, 0), "weekday 7")
	mustRefuse(req("delivery", -1, "08:00", "12:00", 2, 0), "weekday -1")
	mustRefuse(req("delivery", 3, "08am", "12:00", 2, 0), "a time that is not hh:mm")
	if _, err := svc.Create(t.Context(), req("delivery", 3, "08am", "12:00", 2, 0)); err == nil || err.Error() != "start_time must be hh:mm" {
		t.Errorf("invalid start_time error = %v, want start_time must be hh:mm", err)
	}
	if _, err := svc.Create(t.Context(), req("delivery", 3, "08:00", "12pm", 2, 0)); err == nil || err.Error() != "end_time must be hh:mm" {
		t.Errorf("invalid end_time error = %v, want end_time must be hh:mm", err)
	}
	mustRefuse(req("delivery", 3, "12:00", "12:00", 2, 0), "end equal to start")
	mustRefuse(req("delivery", 3, "13:00", "12:00", 2, 0), "end before start")
	mustRefuse(req("delivery", 3, "08:00", "12:00", 0, 0), "capacity zero")
	mustRefuse(req("delivery", 3, "08:00", "12:00", 2, -1), "a negative cutoff")

	// A valid one reaches persistence (the panic on the nil repository).
	func() {
		defer func() {
			if recover() == nil {
				t.Error("a valid window did not reach persistence")
			}
		}()
		_, _ = svc.Create(t.Context(), req("pickup", 5, "07:30", "10:45", 3, 90))
	}()
}
