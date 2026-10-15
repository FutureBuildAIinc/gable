// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"fmt"
	"os"
	"time"
)

// seedNowEnv names the instant a test gives the seed to read as "now" for its
// business day, its history window and every stamp it writes (RFC 3339). The
// production seed leaves it unset and reads the real clock. A test that seeds
// twice sets it once, so the two runs share one instant and a boundary between
// them (the branch's midnight or UTC's) cannot move the second history.
const seedNowEnv = "SEED_NOW"

// seedNow is the seed's clock: the instant SEED_NOW names, else the real time.
func seedNow() time.Time {
	if raw := os.Getenv(seedNowEnv); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

// checkSeedEnv refuses, in words and before anything is written, a seed
// setting that is not valid: a clock that is not RFC 3339 or a branch zone
// that is not a zone name the database and Go both know.
func checkSeedEnv() error {
	if raw := os.Getenv(seedNowEnv); raw != "" {
		if _, err := time.Parse(time.RFC3339, raw); err != nil {
			return fmt.Errorf("%s is %q: it must be an instant in RFC 3339, for example 2031-01-15T03:30:00Z", seedNowEnv, raw)
		}
	}
	if z := os.Getenv(seedBranchZoneEnv); z != "" {
		if _, err := time.LoadLocation(z); err != nil {
			return fmt.Errorf("%s is %q: it must be a time zone name such as America/Vancouver or Etc/UTC", seedBranchZoneEnv, z)
		}
	}
	return nil
}

// historyFrame is the history's anchor (noon of the seed's business day), the
// moment today's documents are stamped from, and whether today trades at all.
// seedDay is the branch's calendar day at now. Today's documents are stamped
// from two hours before the run, never before the UTC day began, and are left
// out while the day is too young to hold them. When the UTC date is ahead of
// the branch's, the branch's today trades from its midday, which has passed.
func historyFrame(now, seedDay time.Time) (anchor, todayFrom time.Time, withToday bool) {
	now = now.UTC()
	anchor = time.Date(seedDay.Year(), seedDay.Month(), seedDay.Day(), 12, 0, 0, 0, time.UTC)
	todayFrom = now.Add(-2 * time.Hour)
	if dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC); todayFrom.Before(dayStart) {
		todayFrom = dayStart
	}
	withToday = now.Sub(todayFrom) >= 10*time.Minute
	if dayOf(now) != dayOf(seedDay) {
		todayFrom, withToday = anchor, true
	}
	return anchor, todayFrom, withToday
}
