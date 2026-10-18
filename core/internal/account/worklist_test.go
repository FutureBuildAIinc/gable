// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// The controller's worklist (M25): the ranking, the aging buckets, the
// utilisation and the hold state, read against real posted invoices.

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/google/uuid"
)

// TestWorklistRanksBySeverityAndActs posts the ladder of an overdue book and
// reads the rows back in severity order, with the action each figure asks
// for. Only the world's own customers are judged: the shared database's
// other accounts are nobody's fixture.
func TestWorklistRanksBySeverityAndActs(t *testing.T) {
	w := newARWorld(t)
	// 90+ days: 100 days past its due date.
	deep := w.invoice(10_000, daysFromNow(-130))
	// 61 to 90 days: 70 days past due.
	mid := w.invoice(10_000, daysFromNow(-100))
	// 31 to 60 days: 40 days past due.
	shallow := w.invoice(10_000, daysFromNow(-70))
	// Current: posted today, due in 30 days.
	w.invoice(5_000, daysFromNow(0))
	// Over limit: a small limit under a fresh balance.
	w.setCreditLimit(1_000)
	_ = w.invoice(5_000, daysFromNow(0))
	_ = deep
	_ = mid
	_ = shallow

	// The action ladder of the unheld accounts: one bucket each, no hold,
	// no bump in the score.
	ladder := map[uuid.UUID]string{
		w.sideOn(8_000, daysFromNow(-100)): "finance_charge", // 70 days past due
		w.sideOn(8_000, daysFromNow(-70)):  "call",           // 40 days past due
		w.sideOn(2_000, daysFromNow(0)):    "send_statement", // not yet due
	}
	out, err := w.svc.Worklist(context.Background(), daysFromNow(0), 500)
	if err != nil {
		t.Fatalf("worklist: %v", err)
	}
	seen := map[uuid.UUID]account.WorklistRow{}
	for _, r := range out.Rows {
		if r.CustomerID == w.customerID {
			seen[w.customerID] = r
			continue
		}
		if want, ok := ladder[r.CustomerID]; ok {
			if r.RecommendedAction != want {
				t.Errorf("the unheld %d days account's action is %q, want %q", r.PastDueCents, r.RecommendedAction, want)
			}
			if r.HoldStatus != "none" {
				t.Errorf("an unheld account reads hold status %q, want none", r.HoldStatus)
			}
			if r.SeverityScore >= 1_000_000 {
				t.Errorf("an unheld account carries the hold bump: %d", r.SeverityScore)
			}
			seen[r.CustomerID] = r
		}
	}
	for id, want := range ladder {
		if _, ok := seen[id]; !ok {
			t.Errorf("the ladder's customer %s is missing from the worklist", id)
		}
		_ = want
	}

	row, ok := seen[w.customerID]
	if !ok {
		t.Fatal("the world's account is missing from the worklist")
	}
	if row.Currency != "USD" {
		t.Fatalf("the row's currency is %q", row.Currency)
	}
	if row.BalanceCents != 40_000 {
		t.Fatalf("the balance is %d, want 40000", row.BalanceCents)
	}
	if row.Over90 != 10_000 || row.Days61To90 != 10_000 || row.Days31To60 != 10_000 || row.CurrentCents != 10_000 {
		t.Fatalf("the buckets read %d/%d/%d/%d, want 10000 each in 90+, 61-90, 31-60 and current",
			row.Over90, row.Days61To90, row.Days31To60, row.CurrentCents)
	}
	if row.PastDueCents != 30_000 {
		t.Fatalf("the past due is %d, want 30000", row.PastDueCents)
	}
	if row.CreditLimitCents == nil || *row.CreditLimitCents != 1_000 {
		t.Fatalf("the limit reads %v, want 1000", row.CreditLimitCents)
	}
	if row.OverLimitCents != 39_000 {
		t.Fatalf("the over limit amount is %d, want 39000", row.OverLimitCents)
	}
	if row.UtilizationPct == nil || *row.UtilizationPct != 4_000 {
		t.Fatalf("the utilisation reads %v, want 4000 percent", row.UtilizationPct)
	}
	if row.HoldStatus != "none" {
		t.Fatalf("an unheld account reads hold status %q, want none", row.HoldStatus)
	}
	if row.RecommendedAction != "hold" {
		t.Fatalf("the action is %q, want hold", row.RecommendedAction)
	}
	// The score is what orders the rows: the weighted buckets and the
	// over-limit amount, no hold bump before anything is held.
	if row.SeverityScore != 4*10_000+2*10_000+10_000+39_000 {
		t.Fatalf("the score is %d, want the weighted sum without a hold bump", row.SeverityScore)
	}
	// The score is what orders the rows: a held account jumps the queue.
	if _, _, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID,
		Reason: "the office", Actor: "u-test", Role: "finance"}); err != nil {
		t.Fatalf("place the hold: %v", err)
	}
	held, err := w.svc.Worklist(context.Background(), daysFromNow(0), 500)
	if err != nil {
		t.Fatalf("worklist: %v", err)
	}
	first := -1
	for i, r := range held.Rows {
		if r.CustomerID == w.customerID {
			first = i
			break
		}
	}
	if first < 0 {
		t.Fatal("the world's account left the worklist")
	}
	if first != 0 {
		t.Fatalf("the held account ranks %d, want first", first+1)
	}
	if held.Rows[first].SeverityScore != 4*10_000+2*10_000+10_000+39_000+1_000_000 {
		t.Fatalf("the held account's score is %d, want the weighted sum plus the hold bump", held.Rows[first].SeverityScore)
	}
	found := held.Rows[first]
	if found.HoldStatus != "active" || found.HoldID == "" {
		t.Fatalf("the hold state reads %q with id %q", found.HoldStatus, found.HoldID)
	}
	if found.RecommendedAction != "hold" {
		t.Fatalf("a held account's action is %q, want hold", found.RecommendedAction)
	}
	// The as_of cut is honoured: an aging of the day the invoices were young
	// has no deep buckets at all.
	past, err := w.svc.Worklist(context.Background(), daysFromNow(-125), 500)
	if err != nil {
		t.Fatalf("worklist as of the past: %v", err)
	}
	for _, r := range past.Rows {
		if r.CustomerID != w.customerID {
			continue
		}
		if r.Over90 != 0 || r.Days61To90 != 0 || r.Days31To60 != 0 {
			t.Fatalf("the past aging carries deep buckets: %+v", r)
		}
	}
}
