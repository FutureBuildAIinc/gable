// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware_test

import (
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
)

// The till and end of day routes of CP-08 and CP-09 each hold one class in
// the key branch allow table. The manager's routes (the day's close and
// reopen, the deposit candidates and match, the till policy, the sign offs)
// are a person's: a machine key is refused. The two movement routes are the
// till session's own, walled by its branch. This pins the class by name, so
// a reclassifying fails here instead of silently widening a machine key.
func TestKeyBranchAllowlist_TheTillAndEndOfDayRoutesHoldTheirClass(t *testing.T) {
	cases := []struct {
		method  string
		pattern string
		want    middleware.KeyBranchRouteClass
		why     string
	}{
		{"GET", "/api/v1/pos/end-of-day", middleware.KeyBranchClassRefused, "the branch end of day is the managers' screen"},
		{"GET", "/api/v1/pos/end-of-day/{id}/deposit-candidates", middleware.KeyBranchClassRefused, "the bank statement spans every branch"},
		{"POST", "/api/v1/pos/end-of-day/close", middleware.KeyBranchClassRefused, "a branch day is closed by a manager, a person"},
		{"POST", "/api/v1/pos/end-of-day/{id}/deposit-match", middleware.KeyBranchClassRefused, "the bank statement spans every branch"},
		{"POST", "/api/v1/pos/end-of-day/{id}/reopen", middleware.KeyBranchClassRefused, "a branch day is reopened by a manager, a person"},
		{"GET", "/api/v1/pos/till/policy", middleware.KeyBranchClassRefused, "the variance threshold is a manager's setting"},
		{"PUT", "/api/v1/pos/till/policy", middleware.KeyBranchClassRefused, "the variance threshold is a manager's setting"},
		{"GET", "/api/v1/pos/till/signoffs", middleware.KeyBranchClassRefused, "the sign off queue is a person's screen"},
		{"POST", "/api/v1/pos/till/{id}/signoff", middleware.KeyBranchClassRefused, "a variance is signed off by a manager, a person"},
		{"GET", "/api/v1/pos/till/{id}/movements", middleware.KeyBranchClassWalled, "a movement belongs to its session's branch"},
		{"POST", "/api/v1/pos/till/{id}/movements", middleware.KeyBranchClassWalled, "a movement belongs to its session's branch"},
	}
	for _, c := range cases {
		class, ok := middleware.KeyBranchRouteClassFor(c.method, c.pattern)
		if !ok {
			t.Errorf("%s %s is a machine key reachable route the allowlist names nowhere: give it a class decision",
				c.method, c.pattern)
			continue
		}
		if class != c.want {
			t.Errorf("%s %s holds the class %s, want %s (%s)", c.method, c.pattern, class, c.want, c.why)
		}
		if allowed := middleware.KeyBranchRouteAllowed(c.method, c.pattern); allowed != (c.want != middleware.KeyBranchClassRefused) {
			t.Errorf("%s %s is allowed %v, want %v for the class %s", c.method, c.pattern, allowed,
				c.want != middleware.KeyBranchClassRefused, c.want)
		}
	}
}
