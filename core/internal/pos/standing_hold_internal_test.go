// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The verdict's hold branch (CP-11): a standing hold record blocks any
// amount on the account whatever the limit, an override window on the hold
// allows up to the hold's own cap, and a window that has passed counts as
// standing.

import (
	"strings"
	"testing"
	"time"
)

func i64(n int64) *int64 { return &n }

func TestOnAccountVerdictHoldRecordCases(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ahead := now.Add(time.Hour)
	behind := now.Add(-time.Hour)
	cases := []struct {
		name   string
		in     verdictIn
		want   string // verdict
		reason string
		words  []string
	}{
		{
			name: "an ACTIVE hold blocks whatever the limit",
			in: verdictIn{currency: "USD", creditLimit: nil, hold: &standingHold{status: "ACTIVE", reason: "past due"},
				now: now, amount: 100},
			want: VerdictBlocked, reason: ReasonAccountHold,
			words: []string{"credit hold", "past due"},
		},
		{
			name: "an ACTIVE hold blocks before the credit limit is read",
			in: verdictIn{currency: "USD", creditLimit: i64(10000), hold: &standingHold{status: "ACTIVE", reason: "review"},
				open: 0, now: now, amount: 100},
			want: VerdictBlocked, reason: ReasonAccountHold,
			words: []string{"review"},
		},
		{
			name: "an ACTIVE hold blocks whatever override_until says",
			in: verdictIn{currency: "USD", creditLimit: nil,
				hold: &standingHold{status: "ACTIVE", reason: "past due", overrideUntil: &ahead}, now: now, amount: 100},
			want: VerdictBlocked, reason: ReasonAccountHold,
			words: []string{"past due"},
		},
		{
			name: "a window that has passed counts as standing",
			in: verdictIn{currency: "USD", creditLimit: nil,
				hold: &standingHold{status: "OVERRIDDEN", reason: "past due", overrideUntil: &behind, maxOrderCents: nil}, now: now, amount: 100},
			want: VerdictBlocked, reason: ReasonAccountHold,
			words: []string{"past due"},
		},
		{
			name: "a live window with no cap allows the sale on to the limit",
			in: verdictIn{currency: "USD", creditLimit: nil,
				hold: &standingHold{status: "OVERRIDDEN", reason: "past due", overrideUntil: &ahead}, now: now, amount: 100},
			want: VerdictAllowed, reason: "",
		},
		{
			name: "a live window allows up to the cap",
			in: verdictIn{currency: "USD", creditLimit: nil,
				hold: &standingHold{status: "OVERRIDDEN", reason: "past due", overrideUntil: &ahead, maxOrderCents: i64(599)}, now: now, amount: 599},
			want: VerdictAllowed, reason: "",
		},
		{
			name: "a live window blocks over the cap with the cap in the words",
			in: verdictIn{currency: "USD", creditLimit: nil,
				hold: &standingHold{status: "OVERRIDDEN", reason: "past due", overrideUntil: &ahead, maxOrderCents: i64(599)}, now: now, amount: 600},
			want: VerdictBlocked, reason: ReasonAccountHold,
			words: []string{"5.99"},
		},
		{
			name: "no hold reads as it did",
			in: verdictIn{currency: "USD", creditLimit: nil, now: now, amount: 100},
			want: VerdictAllowed, reason: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := onAccountVerdict(c.in)
			if v.Verdict != c.want || v.Reason != c.reason {
				t.Fatalf("verdict = %s reason %q, want %s %q", v.Verdict, v.Reason, c.want, c.reason)
			}
			for _, w := range c.words {
				if !strings.Contains(v.Message, w) {
					t.Errorf("message = %q, want %q in it", v.Message, w)
				}
			}
		})
	}
}

// The ways forward of a hold: paying down does not lift it, a manager may
// approve the one sale, another tender always works.
func TestOnAccountVerdictHoldWaysForward(t *testing.T) {
	now := time.Now()
	v := onAccountVerdict(verdictIn{currency: "USD", hold: &standingHold{reason: "past due"}, now: now, amount: 100})
	if v.Verdict != VerdictBlocked {
		t.Fatal("a standing hold must block")
	}
	var pay, mgr, sw *WayForward
	for i := range v.WaysForward {
		switch v.WaysForward[i].Kind {
		case WayPayDown:
			pay = &v.WaysForward[i]
		case WayManagerOverride:
			mgr = &v.WaysForward[i]
		case WaySwitchTender:
			sw = &v.WaysForward[i]
		}
	}
	if pay == nil || pay.Available {
		t.Errorf("pay_down = %v, want unavailable (paying the balance does not lift a hold)", pay)
	}
	if !strings.Contains(pay.Message, "accounts receivable releases it") {
		t.Errorf("pay_down message = %q, want the words that name who releases a hold", pay.Message)
	}
	if mgr == nil || !mgr.Available {
		t.Errorf("manager_override = %v, want available", mgr)
	}
	if sw == nil || !sw.Available {
		t.Errorf("switch_tender = %v, want available", sw)
	}
	if !overridable(ReasonAccountHold) {
		t.Errorf("a manager must be able to waive a hold for one sale")
	}
}
