// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"testing"
	"time"
)

func TestSeedNowReadsItsInstantAndKeepsTheRealClockWhenUnset(t *testing.T) {
	t.Setenv(seedNowEnv, "2031-01-15T03:30:00Z")
	if got := seedNow(); !got.Equal(time.Date(2031, 1, 15, 3, 30, 0, 0, time.UTC)) {
		t.Errorf("seedNow = %s, want the instant the environment names", got)
	}
	t.Setenv(seedNowEnv, "")
	before := time.Now().UTC()
	if got := seedNow(); got.Before(before.Add(-time.Second)) || got.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("seedNow = %s with no instant set, want the real clock", got)
	}
}

func TestCheckSeedEnvRefusesABadInstantAndABadZoneInWords(t *testing.T) {
	t.Setenv(seedNowEnv, "tomorrow")
	if err := checkSeedEnv(); err == nil {
		t.Error("an instant that is not RFC 3339 was accepted")
	}
	t.Setenv(seedNowEnv, "")
	t.Setenv(seedBranchZoneEnv, "Mars/Olympus")
	if err := checkSeedEnv(); err == nil {
		t.Error("a zone name nobody knows was accepted")
	}
	t.Setenv(seedBranchZoneEnv, "Etc/UTC")
	if err := checkSeedEnv(); err != nil {
		t.Errorf("a zone name Go knows was refused: %v", err)
	}
}

// The history's frame is a function of the instant and the seed's day alone:
// two seed runs at one instant read the same frame, and the instants either
// side of the branch's midnight and of UTC's give the same frame at each, so a
// boundary between two runs of one test cannot move the second.
func TestHistoryFrameIsOneFunctionOfTheInstant(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		name    string
		now     time.Time
		seedDay time.Time
	}{
		{"just before UTC's midnight", time.Date(2031, 1, 14, 23, 59, 0, 0, time.UTC), day(2031, 1, 14)},
		{"just after UTC's midnight, the branch a day behind", time.Date(2031, 1, 15, 0, 1, 0, 0, time.UTC), day(2031, 1, 14)},
		{"just before the branch's midnight", time.Date(2031, 1, 15, 4, 59, 0, 0, time.UTC), day(2031, 1, 14)},
		{"just after the branch's midnight", time.Date(2031, 1, 15, 5, 1, 0, 0, time.UTC), day(2031, 1, 15)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a1, f1, w1 := historyFrame(c.now, c.seedDay)
			a2, f2, w2 := historyFrame(c.now, c.seedDay)
			if !a1.Equal(a2) || !f1.Equal(f2) || w1 != w2 {
				t.Fatalf("one instant gave two frames")
			}
			if !w1 {
				t.Errorf("today does not trade")
			}
			if dayOf(a1) != dayOf(c.seedDay) {
				t.Errorf("anchor %s is not the seed's day %s", a1, dayOf(c.seedDay))
			}
			if f1.After(c.now) {
				t.Errorf("today's documents start at %s, after the instant %s", f1, c.now)
			}
		})
	}
}
