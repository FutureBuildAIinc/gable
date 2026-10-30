// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash_test

import (
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/largecash"
)

// anchor is the receipt a crossing hangs off, so the tests read in days.
func anchor() time.Time {
	return time.Date(2026, time.March, 2, 10, 0, 0, 0, time.UTC)
}

// TestFilingDeadlineIsFifteenDaysAfterTheReceipt: the deadline is a rule and
// not a stored column, so it can be argued about without a database. The
// count is in days, not months, and it resets per receipt.
func TestFilingDeadlineIsFifteenDaysAfterTheReceipt(t *testing.T) {
	got := largecash.FilingDeadline(anchor())
	want := anchor().AddDate(0, 0, 15)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got, want)
	}
	if got.Sub(anchor()).Hours() != 15*24 {
		t.Fatalf("the deadline is %v from the receipt, want 15 days", got.Sub(anchor()))
	}
	// A crossing with no receipt has no deadline at all.
	if !largecash.FilingDeadline(time.Time{}).IsZero() {
		t.Fatal("a crossing with no receipt must have no deadline")
	}
}

// TestSeverityRanksTheDeadlineFirstThenTheAmount: a small filing due
// tomorrow is worth more of the controller's morning than a large filing due
// in ten days, so the deadline carries the rank and the amount only breaks a
// tie.
func TestSeverityRanksTheDeadlineFirstThenTheAmount(t *testing.T) {
	now := anchor()
	smallSoon := largecash.SeverityScore(now.AddDate(0, 0, 1), now, 1_000_001)
	largeLater := largecash.SeverityScore(now.AddDate(0, 0, 10), now, 100_000_000)
	if smallSoon <= largeLater {
		t.Fatalf("the small filing due tomorrow (%d) does not outrank the large filing due in ten days (%d)",
			smallSoon, largeLater)
	}
	// Inside one deadline, the bigger amount ranks first.
	small := largecash.SeverityScore(now.AddDate(0, 0, 3), now, 1_000_001)
	big := largecash.SeverityScore(now.AddDate(0, 0, 3), now, 2_000_001)
	if big <= small {
		t.Fatalf("the bigger filing (%d) does not outrank the smaller one (%d)", big, small)
	}
	// A filing already past its deadline outranks one that is not.
	overdue := largecash.SeverityScore(now.AddDate(0, 0, -1), now, 1)
	dueSoon := largecash.SeverityScore(now.AddDate(0, 0, 1), now, 100_000_000)
	if overdue <= dueSoon {
		t.Fatalf("the overdue filing (%d) does not outrank the filing due tomorrow (%d)", overdue, dueSoon)
	}
	// A row with no deadline is nothing is owed to the IRS, so it ranks last.
	if largecash.SeverityScore(time.Time{}, now, 100_000_000) != 0 {
		t.Fatal("a row with no deadline must not carry a score")
	}
}

// TestRecommendedActionLadder: the ladder is capture the payer's details,
// then file, then chase a filing past its deadline, then issue the customer
// statement. An identity declined row never offers to file, and a Canadian
// row offers no filing at all.
func TestRecommendedActionLadder(t *testing.T) {
	reportable := largecash.WorklistRow{
		EventState:           largecash.EventStateReportable,
		FilingState:          largecash.FilingStateReportable,
		StatementState:       largecash.StatementStateNotIssued,
		PayerDetailsCaptured: true,
	}
	if got := largecash.RecommendedAction(reportable); got != largecash.ActionFile {
		t.Fatalf("a live filing inside its window says %q, want %q", got, largecash.ActionFile)
	}

	missingIdentity := reportable
	missingIdentity.PayerDetailsCaptured = false
	if got := largecash.RecommendedAction(missingIdentity); got != largecash.ActionCapturePayer {
		t.Fatalf("a row with no payer identity says %q, want %q", got, largecash.ActionCapturePayer)
	}

	chase := reportable
	chase.PastDeadline = true
	if got := largecash.RecommendedAction(chase); got != largecash.ActionChaseFiling {
		t.Fatalf("a filing past its deadline says %q, want %q", got, largecash.ActionChaseFiling)
	}

	filed := reportable
	filed.FilingState = largecash.FilingStateFiled
	if got := largecash.RecommendedAction(filed); got != largecash.ActionIssueStatement {
		t.Fatalf("a filed row says %q, want %q", got, largecash.ActionIssueStatement)
	}

	stated := filed
	stated.StatementState = largecash.StatementStateIssued
	if got := largecash.RecommendedAction(stated); got != "" {
		t.Fatalf("a filed and stated row says %q, want no action", got)
	}

	declined := reportable
	declined.EventState = largecash.EventStateIdentityDeclined
	declined.PayerDetailsCaptured = false
	if got := largecash.RecommendedAction(declined); got != largecash.ActionManagerReview {
		t.Fatalf("an identity declined row says %q, want %q", got, largecash.ActionManagerReview)
	}

	canada := reportable
	canada.EventState = largecash.EventStateRecorded
	canada.FilingState = largecash.FilingStateNotReportable
	if got := largecash.RecommendedAction(canada); got != largecash.ActionDealerRecord {
		t.Fatalf("a Canadian row says %q, want %q", got, largecash.ActionDealerRecord)
	}

	voluntary := reportable
	voluntary.FilingState = largecash.FilingStateVoluntary
	if got := largecash.RecommendedAction(voluntary); got != "" {
		t.Fatalf("a voluntary filing says %q, want no action", got)
	}
}
