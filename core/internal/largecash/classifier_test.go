// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash_test

// The classifier, table driven against every tender and instrument case the
// research states (CP-17 research sections 2.2 and 3.1): for the United
// States currency always counts, a negotiable instrument of the threshold
// face value or less counts only for a consumer durable sale line priced
// over the threshold or under the suspected avoidance flag, a personal
// check never counts and an instrument over the face cap never counts; for
// Canada currency only. The classifier is a pure function so the decision a
// clerk, a manager and a regulator will all argue about can be argued with
// without a database.

import (
	"testing"

	"github.com/gablelbm/gable/internal/largecash"
)

func TestClassifierUnitedStates(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		instrument string
		amount     int64
		durable    bool
		avoidance  bool
		counts     bool
		reason     string
	}{
		{name: "currency always counts", method: "cash", amount: 450000, counts: true, reason: largecash.ReasonCurrency},
		{name: "cashiers check under the cap on an ordinary sale", instrument: "cashiers_check", amount: 800000, counts: false, reason: largecash.ReasonNotDesignated},
		{name: "bank draft under the cap on an ordinary sale", instrument: "bank_draft", amount: 500000, counts: false, reason: largecash.ReasonNotDesignated},
		{name: "travelers check under the cap on an ordinary sale", instrument: "travelers_check", amount: 300000, counts: false, reason: largecash.ReasonNotDesignated},
		{name: "money order under the cap on an ordinary sale", instrument: "money_order", amount: 990000, counts: false, reason: largecash.ReasonNotDesignated},
		{name: "cashiers check at the cap for a consumer durable", instrument: "cashiers_check", amount: 1000000, durable: true, counts: true, reason: largecash.ReasonConsumerDurable},
		{name: "bank draft for a consumer durable", instrument: "bank_draft", amount: 700000, durable: true, counts: true, reason: largecash.ReasonConsumerDurable},
		{name: "money order under the avoidance flag", instrument: "money_order", amount: 600000, avoidance: true, counts: true, reason: largecash.ReasonSuspectedAvoidance},
		{name: "cashiers check under the avoidance flag on an ordinary sale", instrument: "cashiers_check", amount: 950000, avoidance: true, counts: true, reason: largecash.ReasonSuspectedAvoidance},
		{name: "personal check never counts", instrument: "personal_check", amount: 1200000, durable: true, avoidance: true, counts: false, reason: largecash.ReasonPersonalCheck},
		{name: "unlabelled check never counts", instrument: "", amount: 800000, counts: false, reason: largecash.ReasonPersonalCheck},
		{name: "cashiers check over the face cap", instrument: "cashiers_check", amount: 1000001, durable: true, avoidance: true, counts: false, reason: largecash.ReasonOverFaceCap},
		{name: "bank draft over the face cap", instrument: "bank_draft", amount: 1500000, durable: true, counts: false, reason: largecash.ReasonOverFaceCap},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			method := c.method
			if method == "" {
				method = "check"
			}
			d := largecash.Classify(
				largecash.SaleFacts{Country: "US", ConsumerDurableOverThreshold: c.durable, SuspectedAvoidance: c.avoidance},
				largecash.TenderFacts{Method: method, Instrument: c.instrument, AmountCents: c.amount})
			if d.RuleFamily != largecash.RuleUS {
				t.Fatalf("rule family = %q, want US_8300", d.RuleFamily)
			}
			if d.Counts != c.counts {
				t.Errorf("counts = %t, want %t (reason %s)", d.Counts, c.counts, d.Reason)
			}
			if d.Counts && d.CountedCents != c.amount {
				t.Errorf("counted cents = %d, want %d", d.CountedCents, c.amount)
			}
			if d.Reason != c.reason {
				t.Errorf("reason = %q, want %q", d.Reason, c.reason)
			}
		})
	}
}

func TestClassifierUnitedStatesNonCheckTendersNeverCount(t *testing.T) {
	for _, method := range []string{"card", "account", "store_credit"} {
		d := largecash.Classify(largecash.SaleFacts{Country: "US"},
			largecash.TenderFacts{Method: method, AmountCents: 2000000})
		if d.Counts {
			t.Errorf("a %s tender counts, want never", method)
		}
		if d.Reason != largecash.ReasonNotCash {
			t.Errorf("a %s tender reason = %q", method, d.Reason)
		}
	}
}

func TestClassifierCanadaCurrencyOnly(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		instrument string
		amount     int64
		durable    bool
		avoidance  bool
		counts     bool
	}{
		{name: "currency counts", method: "cash", amount: 1200000, counts: true},
		{name: "cashiers check never counts", method: "check", instrument: "cashiers_check", amount: 800000, durable: true, avoidance: true},
		{name: "bank draft never counts", method: "check", instrument: "bank_draft", amount: 400000},
		{name: "money order never counts", method: "check", instrument: "money_order", amount: 990000},
		{name: "personal check never counts", method: "check", instrument: "personal_check", amount: 1200000},
		{name: "card never counts", method: "card", amount: 1200000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := largecash.Classify(
				largecash.SaleFacts{Country: "CA", ConsumerDurableOverThreshold: c.durable, SuspectedAvoidance: c.avoidance},
				largecash.TenderFacts{Method: c.method, Instrument: c.instrument, AmountCents: c.amount})
			if d.RuleFamily != largecash.RuleCA {
				t.Fatalf("rule family = %q, want CA_DEALER", d.RuleFamily)
			}
			if d.Counts != c.counts {
				t.Errorf("counts = %t, want %t (reason %s)", d.Counts, c.counts, d.Reason)
			}
			if d.Reason != largecash.ReasonCanadaCurrencyOnly {
				t.Errorf("reason = %q, want the Canada currency only reason", d.Reason)
			}
		})
	}
}

func TestClassifierUnknownCountryCountsNothing(t *testing.T) {
	for _, country := range []string{"", "GB", "MX"} {
		d := largecash.Classify(largecash.SaleFacts{Country: country},
			largecash.TenderFacts{Method: "cash", AmountCents: 5000000})
		if d.RuleFamily != "" || d.Counts {
			t.Errorf("country %q produced rule %q counting %t, want no rule and nothing counted", country, d.RuleFamily, d.Counts)
		}
	}
}

// TestCrossesComparators: the United States comparator is strictly greater
// than the threshold, the Canadian one at or above it.
func TestCrossesComparators(t *testing.T) {
	const th = largecash.ThresholdCents
	for _, c := range []struct {
		rule  string
		total int64
		want  bool
	}{
		{largecash.RuleUS, th - 1, false},
		{largecash.RuleUS, th, false},
		{largecash.RuleUS, th + 1, true},
		{largecash.RuleCA, th - 1, false},
		{largecash.RuleCA, th, true},
		{largecash.RuleCA, th + 1, true},
	} {
		if got := largecash.Crosses(c.rule, c.total, th); got != c.want {
			t.Errorf("Crosses(%s, %d, %d) = %v, want %v", c.rule, c.total, th, got, c.want)
		}
	}
}
