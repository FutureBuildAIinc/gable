// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

// The pure rules of the credit work: the hold decision, the cleared
// decision, the worklist's severity score and its action ladder, and the
// banker's rounding of the finance charge. No database, no clock: the rules
// are the thing a controller argues with.

import (
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// TestHoldDecisionRule: over limit beats past due when both breach, past due
// alone holds, a clean account and one with no limit hold nothing.
func TestHoldDecisionRule(t *testing.T) {
	cases := []struct {
		name  string
		facts holdAccountFacts
		kind  HoldType
	}{
		{"clean", holdAccountFacts{CreditLimit: 100_000, Balance: 90_000}, ""},
		{"no limit", holdAccountFacts{CreditLimit: 0, Balance: 90_000}, ""},
		{"over limit", holdAccountFacts{CreditLimit: 100_000, Balance: 100_001}, HoldTypeOverLimit},
		{"at the limit is not over", holdAccountFacts{CreditLimit: 100_000, Balance: 100_000}, ""},
		{"past due", holdAccountFacts{CreditLimit: 100_000, Balance: 50_000, PastDueCents: 1}, HoldTypePastDue},
		{"both breach as over limit", holdAccountFacts{CreditLimit: 100_000, Balance: 150_000, PastDueCents: 25_000}, HoldTypeOverLimit},
	}
	for _, tc := range cases {
		if kind, reason := holdDecision(tc.facts); kind != tc.kind {
			t.Errorf("%s: holds as %q, want %q", tc.name, kind, tc.kind)
		} else if kind != "" && reason == "" {
			t.Errorf("%s: a hold carries no reason", tc.name)
		}
	}
}

// TestClearedDecisionRule: only the trigger the hold was placed for counts,
// and only when it is gone.
func TestClearedDecisionRule(t *testing.T) {
	clean := holdAccountFacts{CreditLimit: 100_000, Balance: 90_000}
	if got := clearedDecision(clean, "over_limit"); got == "" {
		t.Error("an over limit hold with the balance back inside is cleared")
	}
	breached := holdAccountFacts{CreditLimit: 100_000, Balance: 150_000}
	if got := clearedDecision(breached, "over_limit"); got != "" {
		t.Error("an over limit hold still breaching is not cleared")
	}
	if got := clearedDecision(holdAccountFacts{}, "past_due"); got == "" {
		t.Error("a past due hold with nothing past due is cleared")
	}
	if got := clearedDecision(holdAccountFacts{PastDueCents: 5}, "past_due"); got != "" {
		t.Error("a past due hold still past due is not cleared")
	}
	// A limit that disappeared clears an over limit hold (the account has no
	// limit to breach any more).
	if got := clearedDecision(holdAccountFacts{}, "over_limit"); got == "" {
		t.Error("an over limit hold with no limit at all is cleared")
	}
}

func worklistRow(hold string, current, d1_30, d31_60, d61_90, over90, overLimit int64) WorklistRow {
	return WorklistRow{
		HoldStatus: hold, CurrentCents: httpx.Cents(current), Days1To30: httpx.Cents(d1_30),
		Days31To60: httpx.Cents(d31_60), Days61To90: httpx.Cents(d61_90), Over90: httpx.Cents(over90),
		OverLimitCents: httpx.Cents(overLimit),
	}
}

// TestSeverityScoreRanks: the deep buckets weigh double and quadruple, the
// over-limit amount counts once, and a standing hold outranks any plain
// overdue account.
func TestSeverityScoreRanks(t *testing.T) {
	deep := worklistRow("none", 0, 0, 0, 0, 10_000, 0)    // 90+ money
	mid := worklistRow("none", 0, 0, 0, 10_000, 0, 0)     // 61 to 90 money
	shallow := worklistRow("none", 0, 0, 10_000, 0, 0, 0) // 31 to 60 money
	overLimit := worklistRow("none", 10_000, 0, 0, 0, 0, 5_000)
	held := worklistRow("active", 0, 0, 0, 0, 0, 0)

	if got := severityScore(deep); got != 4*10_000 {
		t.Errorf("over 90 weighs %d, want 4x", got)
	}
	if got := severityScore(mid); got != 2*10_000 {
		t.Errorf("61 to 90 weighs %d, want 2x", got)
	}
	if got := severityScore(shallow); got != 10_000 {
		t.Errorf("31 to 60 weighs %d, want 1x", got)
	}
	if got := severityScore(overLimit); got != 5_000 {
		t.Errorf("the over limit amount weighs %d, want 1x", got)
	}
	if got := severityScore(held); got != holdScoreBump {
		t.Errorf("a hold weighs %d, want the bump", got)
	}
	if severityScore(held) <= severityScore(deep) {
		t.Error("a held account must outrank a plain overdue one")
	}
}

// TestRecommendedActionLadder: held, over limit or 90 day money holds; 61 to
// 90 is the finance charge; 31 to 60 gets the phone; the rest a statement.
func TestRecommendedActionLadder(t *testing.T) {
	cases := []struct {
		name string
		row  WorklistRow
		want string
	}{
		{"held", worklistRow("active", 5_000, 0, 0, 0, 0, 0), "hold"},
		{"overridden hold", worklistRow("overridden", 5_000, 0, 0, 0, 0, 0), "hold"},
		{"over limit", worklistRow("none", 10_000, 0, 0, 0, 0, 1), "hold"},
		{"ninety plus", worklistRow("none", 0, 0, 0, 0, 1, 0), "hold"},
		{"sixty one to ninety", worklistRow("none", 0, 0, 0, 1, 0, 0), "finance_charge"},
		{"thirty one to sixty", worklistRow("none", 0, 0, 1, 0, 0, 0), "call"},
		{"one to thirty", worklistRow("none", 0, 5_000, 0, 0, 0, 0), "send_statement"},
		{"current", worklistRow("none", 5_000, 0, 0, 0, 0, 0), "send_statement"},
	}
	for _, tc := range cases {
		if got := recommendedAction(tc.row); got != tc.want {
			t.Errorf("%s: the action is %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestRoundHalfEven: banker's rounding, a tie to the even cent.
func TestRoundHalfEven(t *testing.T) {
	cases := []struct {
		num, den, want int64
	}{
		{0, 10_000, 0},
		{9_999, 10_000, 1},                  // 0.9999 up
		{10_000, 10_000, 1},                 // exact
		{14_999, 10_000, 1},                 // 1.4999 down to 1
		{15_000, 10_000, 2},                 // 1.5 ties to the even 2
		{25_000, 10_000, 2},                 // 2.5 ties to the even 2
		{35_000, 10_000, 4},                 // 3.5 ties to the even 4
		{20_000_000 * 200, 10_000, 400_000}, // the exact monthly charge of $200 on $20,000 at 2 percent
	}
	for _, tc := range cases {
		if got := roundHalfEven(tc.num, tc.den); got != tc.want {
			t.Errorf("roundHalfEven(%d, %d) = %d, want %d", tc.num, tc.den, got, tc.want)
		}
	}
}
