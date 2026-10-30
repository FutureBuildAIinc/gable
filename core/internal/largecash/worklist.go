// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// The worklist's own words. Every surface that shows a large cash row says
// the same things, in the house voice, so a manager reads one sentence and
// knows what the row wants.
const (
	// AdviserNote is the plain sentence every large cash surface carries: the
	// dealer confirms each filing with its own adviser (item 236).
	AdviserNote = "The dealer confirms each filing with its own adviser."
	// IdentityDeclinedNote is what an identity declined row says: the walk-in
	// did not identify, a manager looks at it, and there is no filing to make.
	IdentityDeclinedNote = "Identity declined, manager review."
	// GableDoesNotFile is what the Form 8300 export says: Gable does not
	// file, the filer types the figures into BSA E-Filing.
	GableDoesNotFile = "Gable does not file this. The filer types these figures into BSA E-Filing."
	// StatementFurnishedNote is the sentence the customer statement carries:
	// the information was furnished to the IRS.
	StatementFurnishedNote = "The information above was furnished to the Internal Revenue Service."
)

// The filing state of a worklist row. It is the crossing's own state and the
// filing row's state read together: a row is not yet reportable (nothing is
// owed to the IRS), reportable and not filed (the duty is live), filed (the
// duty is done) or voluntary (a below threshold report; no crossing is below
// the threshold, so the service never writes one and the state only reads one).
const (
	FilingStateNotReportable = "not_reportable"
	FilingStateReportable    = "reportable"
	FilingStateFiled         = "filed"
	FilingStateVoluntary     = "voluntary"
)

// The statement state of a worklist row. The customer statement is a
// separate obligation from the filing and runs on its own clock.
const (
	StatementStateNotIssued = "not_issued"
	StatementStateIssued    = "issued"
)

// The recommended action ladder, in the AR worklist's spirit (research 6.3):
// capture missing payer identity, then file, then chase the filing, then
// issue the customer statement.
const (
	// ActionCapturePayer is the first rung: the row has no payer identity, so
	// the IRS form cannot be completed and the counter has to ask again.
	ActionCapturePayer = "capture_payer_details"
	// ActionFile is the rung for a live filing inside its window.
	ActionFile = "file"
	// ActionChaseFiling is the rung for a filing past its deadline.
	ActionChaseFiling = "chase_filing"
	// ActionIssueStatement is the rung for a filed row whose customer
	// statement has not gone out yet.
	ActionIssueStatement = "issue_statement"
	// ActionManagerReview is the rung for an identity declined row: a manager
	// looks at it, and there is no filing to make.
	ActionManagerReview = "manager_review"
	// ActionDealerRecord is the rung for a Canadian row: it is the dealer's
	// own record, not a FINTRAC report, and there is no filing to make.
	ActionDealerRecord = "dealer_record"
)

// FilingDeadlineDays is the rule: a reportable crossing is due 15 days after
// the receipt that crossed the threshold. The count is in days, not months,
// and it resets per receipt: every crossing carries its own anchor.
const FilingDeadlineDays = 15

// actorOrSystem names the manager who recorded a filing or issued a
// statement: the person, or the system when the call carries no identity (a
// dev mode caller). It is the till's own rule.
func actorOrSystem(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "system"
	}
	return actor
}

// FilingDeadline is the deadline rule as a pure function: the receipt anchor
// plus the 15 days. A zero anchor has no deadline, which is what a crossing
// that is not reportable carries.
func FilingDeadline(anchor time.Time) time.Time {
	if anchor.IsZero() {
		return time.Time{}
	}
	return anchor.AddDate(0, 0, FilingDeadlineDays)
}

// The severity score's parts. The score ranks the worklist by how close to
// or past the deadline a filing is, and the amount only breaks the tie
// (research 6.3): a small filing one day from the deadline is worth more of
// the controller's morning than a large filing comfortably inside the window.
const (
	// severityDueCap is the number of days the deadline part weighs: a filing
	// due further out than this ranks below every filing inside the window.
	severityDueCap int64 = 365
	// severityOverdueCap is the number of days past the deadline the score
	// weighs: a filing overdue by more than this tops the list anyway.
	severityOverdueCap int64 = 3650
	// severityAmountBase is the base the amount is added in. The deadline's
	// part always outweighs any amount below the base, so the amount can
	// never carry a row past a row with a nearer deadline. The base is a
	// trillion cents, ten billion dollars, far past any cash sale.
	severityAmountBase int64 = 1_000_000_000_000
)

// SeverityScore is the whole ranking of the worklist, a pure function so the
// order the controller sees can be argued with without a database. It takes
// the filing deadline, the clock and the reportable amount. A row with no
// deadline (nothing is owed to the IRS) scores lowest.
func SeverityScore(deadline time.Time, now time.Time, amountCents int64) int64 {
	if deadline.IsZero() {
		return 0
	}
	urgency := severityUrgency(deadlineDaysLeft(deadline, now))
	amount := amountCents
	if amount < 0 {
		amount = 0
	}
	if amount >= severityAmountBase {
		amount = severityAmountBase - 1
	}
	return urgency*severityAmountBase + amount
}

// severityUrgency is how near the deadline a filing is. A filing already
// past its deadline weighs more the longer it has been past, and a filing
// due soon weighs more the closer it is: the thing that gets worse weighs
// more, the AR worklist's rule.
func severityUrgency(daysLeft int64) int64 {
	if daysLeft < -severityOverdueCap {
		daysLeft = -severityOverdueCap
	}
	if daysLeft > severityDueCap {
		daysLeft = severityDueCap
	}
	return severityDueCap - daysLeft + 1
}

// deadlineDaysLeft is the whole days from now until the deadline, rounded
// away from zero: a deadline thirty hours away is two days out and a
// deadline that passed an hour ago is a day out in the past.
func deadlineDaysLeft(deadline, now time.Time) int64 {
	hours := int64(deadline.Sub(now).Hours())
	if hours >= 0 {
		return (hours + 23) / 24
	}
	return -((-hours + 23) / 24)
}

// RecommendedAction is the ladder, a pure function: what the manager does
// next with this row. The ladder is capture the payer's details, then file,
// then chase a filing past its deadline, then issue the customer statement.
// An identity declined row never offers to file, and a Canadian row offers
// no filing at all: it is the dealer's own record.
func RecommendedAction(row WorklistRow) string {
	switch row.EventState {
	case StateIdentityDeclined:
		return ActionManagerReview
	case EventStateRecorded:
		return ActionDealerRecord
	}
	if !row.PayerDetailsCaptured {
		return ActionCapturePayer
	}
	switch row.FilingState {
	case FilingStateFiled:
		if row.StatementState != StatementStateIssued {
			return ActionIssueStatement
		}
		return ""
	case FilingStateVoluntary:
		// a voluntary filing is never stated: the payer asked not to be
		// reported, so there is nothing left to do
		return ""
	}
	if row.PastDeadline {
		return ActionChaseFiling
	}
	return ActionFile
}

// WorklistRow is one crossing on the manager's worklist: who paid, where
// the cash was taken, how much the dealer owes a filing on, when the filing
// is due, and what the manager does next. It never carries the payer's
// taxpayer identification number: that number belongs to the Form 8300
// export alone.
type WorklistRow struct {
	EventID              uuid.UUID   `json:"event_id"`
	PayerKey             string      `json:"payer_key"`
	PayerName            string      `json:"payer_name"`
	CustomerID           *uuid.UUID  `json:"customer_id,omitempty"`
	CustomerAccount      string      `json:"customer_account,omitempty"`
	BranchID             uuid.UUID   `json:"branch_id"`
	BranchName           string      `json:"branch_name"`
	BranchAddress        string      `json:"branch_address"`
	ContributedBranchIDs []uuid.UUID `json:"contributed_branch_ids"`
	Currency             string      `json:"currency"`
	TotalCents           int64       `json:"total_cents"`
	RuleFamily           string      `json:"rule_family"`
	ThresholdCents       int64       `json:"threshold_cents"`
	TransactionCount     int         `json:"transaction_count"`
	TenderCount          int         `json:"tender_count"`
	FirstDate            *time.Time  `json:"first_date,omitempty"`
	LastDate             *time.Time  `json:"last_date,omitempty"`
	EventState           string      `json:"event_state"`
	FilingState          string      `json:"filing_state"`
	FilingDeadline       *time.Time  `json:"filing_deadline,omitempty"`
	PastDeadline         bool        `json:"past_deadline"`
	FiledDate            *time.Time  `json:"filed_date,omitempty"`
	AcknowledgementNo    string      `json:"acknowledgement_no,omitempty"`
	StatementState       string      `json:"statement_state"`
	StatementIssuedDate  *time.Time  `json:"statement_issued_date,omitempty"`
	PayerDetailsCaptured bool        `json:"payer_details_captured"`
	Severity             int64       `json:"severity_score"`
	Action               string      `json:"recommended_action"`
	Note                 string      `json:"note,omitempty"`
	AdviserNote          string      `json:"adviser_note"`
}

// Worklist is the manager's queue of large cash crossings.
type Worklist struct {
	Items       []WorklistRow `json:"items"`
	Count       int           `json:"count"`
	GeneratedAt time.Time     `json:"generated_at"`
}

// Worklist answers the manager's queue: every crossing the dealer owes a
// filing on, in the order the manager should work them. A branch scoped
// caller sees only the crossings its branch received cash for or
// contributed cash to; the dealer's own administrator sees every branch.
func (s *Service) Worklist(ctx context.Context) (*Worklist, error) {
	threshold, err := s.caThreshold(ctx)
	if err != nil {
		return nil, err
	}
	records, err := s.repo.WorklistRecords(ctx, branchctx.IDForQuery(ctx))
	if err != nil {
		return nil, err
	}
	now := s.now()
	items := make([]WorklistRow, 0, len(records))
	for _, rec := range records {
		items = append(items, s.worklistRow(rec, threshold, now))
	}
	sortWorklist(items)
	return &Worklist{Items: items, Count: len(items), GeneratedAt: now}, nil
}

// caThreshold is the Canadian dealer threshold, the amount the dealer's own
// policy counts at. The United States threshold is the law's own and never
// moves.
func (s *Service) caThreshold(ctx context.Context) (int64, error) {
	settings, err := s.ReadSettings(ctx)
	if err != nil {
		return 0, err
	}
	return settings.CAThresholdCents, nil
}

// worklistRow finishes one row: the deadline is a rule, the severity is a
// pure function of the deadline and the amount, the filing state is the
// crossing's own state read with the filing row, and the action is the
// ladder.
func (s *Service) worklistRow(rec worklistRecord, caThresholdCents int64, now time.Time) WorklistRow {
	row := WorklistRow{
		EventID:              rec.EventID,
		PayerKey:             rec.PayerKey,
		PayerName:            rec.PayerName,
		CustomerID:           rec.CustomerID,
		CustomerAccount:      rec.CustomerAccount,
		BranchID:             rec.BranchID,
		BranchName:           rec.BranchName,
		BranchAddress:        rec.BranchAddress,
		ContributedBranchIDs: rec.ContributedBranchIDs,
		Currency:             rec.Currency,
		TotalCents:           rec.TotalCents,
		RuleFamily:           rec.RuleFamily,
		ThresholdCents:       ThresholdCents,
		TransactionCount:     rec.TransactionCount,
		TenderCount:          rec.TenderCount,
		FirstDate:            rec.FirstDate,
		LastDate:             rec.LastDate,
		EventState:           rec.EventState,
		FilingState:          filingState(rec.EventState, rec.FilingRowState),
		FiledDate:            rec.FiledDate,
		AcknowledgementNo:    rec.AcknowledgementNo,
		StatementState:       rec.StatementState,
		StatementIssuedDate:  rec.StatementIssuedDate,
		PayerDetailsCaptured: rec.PayerDetailsCaptured,
		AdviserNote:          AdviserNote,
	}
	if rec.StatementState == "" {
		row.StatementState = StatementStateNotIssued
	}
	if rec.RuleFamily == RuleCA {
		row.ThresholdCents = caThresholdCents
	}
	if rec.EventState == StateIdentityDeclined {
		row.Note = IdentityDeclinedNote
	}
	if rec.EventState == EventStateRecorded {
		row.Note = CanadaNote
	}
	// The deadline is a rule on the receipt anchor, and it only exists for a
	// crossing that is reportable: there is nothing to file for an identity
	// declined row or a Canadian row.
	if row.FilingState == FilingStateReportable {
		deadline := FilingDeadline(rec.ReceivedAt)
		row.FilingDeadline = &deadline
		row.PastDeadline = now.After(deadline) || rec.PastDeadline
	}
	row.Severity = SeverityScore(deadlineOf(row), now, row.TotalCents)
	row.Action = RecommendedAction(row)
	return row
}

// filingState is the crossing's state and the filing row's state read
// together: a reportable crossing with no filing yet is reportable and not
// filed, a filed one is filed, and a crossing that is not reportable (a
// Canadian record, an identity declined row) never carries a filing.
func filingState(eventState, filingRowState string) string {
	switch filingRowState {
	case "filed":
		return FilingStateFiled
	case "voluntary":
		return FilingStateVoluntary
	}
	if eventState == EventStateReportable {
		return FilingStateReportable
	}
	return FilingStateNotReportable
}

// deadlineOf is the row's deadline, or the zero time when it has none.
func deadlineOf(row WorklistRow) time.Time {
	if row.FilingDeadline == nil {
		return time.Time{}
	}
	return *row.FilingDeadline
}

// sortWorklist orders the rows the way the manager works them: the nearest
// deadline first, the biggest amount inside a deadline second, and the payer's
// name as the stable tie break.
func sortWorklist(rows []WorklistRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Severity != rows[j].Severity {
			return rows[i].Severity > rows[j].Severity
		}
		if rows[i].PayerName != rows[j].PayerName {
			return rows[i].PayerName < rows[j].PayerName
		}
		return rows[i].EventID.String() < rows[j].EventID.String()
	})
}
