// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import "testing"

// TestNormaliseRunTimingEta pins the normalisation that makes the route
// goldens independent of the wall clock near UTC midnight: an ETA is the
// server's now plus minutes, so the same script run at 23:59:50 and at midday
// must produce the same transcript. The end-of-run guard cannot catch it (the
// run ends before midnight; only the ETA crosses it).
func TestNormaliseRunTimingEta(t *testing.T) {
	n := &normaliser{ids: map[string]string{}, seedDay: "2031-01-15"}
	for _, eta := range []string{
		"2031-01-15T12:15:00Z", // midday
		"2031-01-16T00:09:50Z", // 23:59:50 plus a 10 minute leg
		"2031-01-15T23:59:50-08:00",
	} {
		body := map[string]any{"eta": eta, "estimated_arrival": eta, "other": "2031-01-16T00:09:50Z"}
		got := n.value(body, "").(map[string]any)
		if got["eta"] != "<eta>" || got["estimated_arrival"] != "<eta>" {
			t.Errorf("eta %q normalised to %v / %v, want <eta>", eta, got["eta"], got["estimated_arrival"])
		}
		if got["other"] != "<ts+1d>" {
			t.Errorf("a field that is not run timing must keep its day offset, got %v", got["other"])
		}
	}
	if got := n.value(map[string]any{"eta": nil}, "").(map[string]any)["eta"]; got != nil {
		t.Errorf("a null eta must stay null, got %v", got)
	}
}

// TestNormaliseSeedFixedDriverDates pins that the demo seed's hardcoded driver
// dates carry no seed-day offset: the same row normalises identically on any
// day the suite runs.
func TestNormaliseSeedFixedDriverDates(t *testing.T) {
	row := func() any {
		return map[string]any{"hire_date": "2022-04-15T00:00:00Z", "cdl_expiry": "2027-01-20T00:00:00Z"}
	}
	a := (&normaliser{ids: map[string]string{}, seedDay: "2031-01-15"}).value(row(), "")
	b := (&normaliser{ids: map[string]string{}, seedDay: "2031-01-16"}).value(row(), "")
	for _, got := range []any{a, b} {
		m := got.(map[string]any)
		if m["hire_date"] != "<ts>" || m["cdl_expiry"] != "<ts>" {
			t.Errorf("driver dates normalised to %v, want <ts>", m)
		}
	}
}
