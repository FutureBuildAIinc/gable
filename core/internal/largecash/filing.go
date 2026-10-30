// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// The filing row's own states (migration 149). A crossing that carries no
// filing row has not been filed; a filing row that has been taken says which
// kind of filing it was.
const (
	FilingRowNotFiled = "not_filed"
	FilingRowFiled    = "filed"
	FilingRowVoluntary = "voluntary"
)

// The 409s a filing answers.
//
// A Canadian crossing is the dealer's own record and not a report, so there
// is no filing to make and the dealer is not entitled to make one.
var ErrFilingNotReportable = &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
	Message: "this crossing is the dealer's own record, not a report: there is no filing to make",
	Details: []httpx.FieldError{httpx.Blocker("filing_not_reportable",
		"this crossing is the dealer's own record, not a report: there is no filing to make")}}

// An identity declined crossing has no payer to file for: the walk-in would
// not say who they were, and a manager reviews the sale instead.
var ErrFilingIdentityDeclined = &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
	Message: "the walk-in declined to identify: a manager reviews this crossing, there is no filing to make",
	Details: []httpx.FieldError{httpx.Blocker("filing_identity_declined",
		"the walk-in declined to identify: a manager reviews this crossing, there is no filing to make")}}

// A reportable crossing is over the threshold, so its Form 8300 is required.
// A voluntary filing is a below threshold report and no crossing here is below
// the threshold, so a voluntary filing on a reportable crossing is refused.
var ErrFilingVoluntaryRefused = &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
	Message: "this crossing is over the threshold, so its Form 8300 is required: it cannot be recorded as voluntary",
	Details: []httpx.FieldError{httpx.Blocker("filing_voluntary_refused",
		"this crossing is over the threshold, so its Form 8300 is required: it cannot be recorded as voluntary")}}

// The crossing already carries a filing. Two managers recording a filing on
// one crossing at the same time is the race this answers: one wins and the
// other is told the crossing is already filed.
var ErrFilingAlreadyRecorded = &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
	Message: "this crossing is already filed",
	Details: []httpx.FieldError{httpx.Blocker("filing_already_recorded",
		"this crossing is already filed")}}

// RecordFilingIn is the manager's record of a filing: the day the Form 8300
// was filed, the BSA E-Filing acknowledgement number the manager typed, and
// who recorded it. Voluntary asks for a below threshold report; every
// reportable crossing is over the threshold, so the ask is refused.
type RecordFilingIn struct {
	FiledDate         time.Time
	AcknowledgementNo string
	Voluntary         bool
	Actor             string
}

// Filing is the filing state of one crossing after a manager recorded it.
type Filing struct {
	EventID           uuid.UUID  `json:"event_id"`
	State             string     `json:"state"`
	FiledDate         *time.Time `json:"filed_date,omitempty"`
	AcknowledgementNo string     `json:"acknowledgement_no,omitempty"`
	RecordedBy        string     `json:"recorded_by"`
	RecordedAt        time.Time  `json:"recorded_at"`
	PastDeadline      bool       `json:"past_deadline"`
}

// RecordFiling records the manager's filing of a Form 8300 for one crossing.
// The crossing's own state decides whether there is a filing to make, and
// the filing row is taken in place: a second manager finds nothing to take
// and is told the crossing is already filed.
func (s *Service) RecordFiling(ctx context.Context, eventID uuid.UUID, in RecordFilingIn) (*Filing, error) {
	facts, err := s.repo.EventFilingFacts(ctx, eventID)
	if err != nil {
		return nil, err
	}
	switch facts.State {
	case EventStateRecorded:
		return nil, ErrFilingNotReportable
	case EventStateIdentityDeclined:
		return nil, ErrFilingIdentityDeclined
	}
	if facts.FilingState == FilingRowFiled || facts.FilingState == FilingRowVoluntary {
		return nil, ErrFilingAlreadyRecorded
	}
	// Every reportable crossing is over the threshold, so its filing is
	// required; a voluntary filing is a below threshold report.
	if in.Voluntary {
		return nil, ErrFilingVoluntaryRefused
	}
	state := FilingRowFiled
	ack := strings.TrimSpace(in.AcknowledgementNo)
	if ack == "" {
		return nil, httpx.BadRequest("the BSA E-Filing acknowledgement number is required for a filing",
			httpx.FieldError{Field: "acknowledgement_no", Message: "the BSA E-Filing acknowledgement number is required for a filing"})
	}
	if len(ack) > 64 {
		return nil, httpx.BadRequest("the BSA E-Filing acknowledgement number is too long",
			httpx.FieldError{Field: "acknowledgement_no", Message: "the BSA E-Filing acknowledgement number is too long"})
	}
	filedDate := in.FiledDate
	if filedDate.IsZero() {
		filedDate = s.now()
	}
	actor := actorOrSystem(in.Actor)
	var filing *Filing
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		// The standing row is the worklist's record of the filing obligation.
		// It is created on demand, so the claim never waits for the sweep.
		if err := s.repo.EnsureFilingRow(ctx, eventID); err != nil {
			return err
		}
		claimed, err := s.repo.ClaimFiling(ctx, eventID, state, filedDate, ack, actor)
		if err != nil {
			return err
		}
		if claimed == nil {
			// Another manager took the row first. The crossing is filed and
			// this manager's filing is not recorded.
			return ErrFilingAlreadyRecorded
		}
		filing = claimed
		// The audit row names the filing and its figures and that the payer
		// was identified. It never carries an identity value.
		return audit.NewLogger(s.db).Log(ctx, audit.Entry{
			Action: "large_cash.filing.recorded", EntityType: "large_cash_event", EntityID: eventID, UserID: actor,
			Changes: map[string]interface{}{
				"state": state, "rule_family": facts.RuleFamily, "branch_id": facts.BranchID.String(),
				"total_cents": facts.TotalCents, "filed_date": filedDate.Format("2006-01-02"),
				"acknowledgement_no": ack, "payer_captured": facts.PayerCaptured,
			}})
	})
	if err != nil {
		return nil, err
	}
	return filing, nil
}

// Form8300 is the data set the filer types into BSA E-Filing (research 2.4
// and 2.9). It is the one surface that carries the payer's taxpayer
// identification number, because the IRS form cannot be completed without
// it, and the route that serves it is the dealer's own and admin's only.
type Form8300 struct {
	EventID           uuid.UUID      `json:"event_id"`
	Form              string         `json:"form"`
	Currency          string         `json:"currency"`
	AmountCents       int64          `json:"amount_cents"`
	TransactionCount  int            `json:"transaction_count"`
	TenderCount       int            `json:"tender_count"`
	FirstDate         time.Time      `json:"first_date"`
	LastDate          time.Time      `json:"last_date"`
	ReceivedAt        time.Time      `json:"received_at"`
	FilingDeadline    time.Time      `json:"filing_deadline"`
	Payer             Form8300Payer  `json:"payer"`
	ThirdPartyPayer   *Form8300Third `json:"third_party_payer,omitempty"`
	Filer             Form8300Filer  `json:"filer"`
	Note              string         `json:"note"`
	AdviserNote       string         `json:"adviser_note"`
	TaxpayerIDHint    *string        `json:"taxpayer_id_hint,omitempty"`
}

// Form8300Payer is the payer as Form 8300 Part I takes it: for an
// individual, the name, the address, the taxpayer identification number, the
// date of birth and the occupation.
type Form8300Payer struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	TaxpayerID  string `json:"taxpayer_id"`
	DateOfBirth string `json:"date_of_birth,omitempty"`
	Occupation  string `json:"occupation,omitempty"`
}

// Form8300Third is Part II: the person on whose behalf the transaction was
// conducted, when the payer is a third party payer.
type Form8300Third struct {
	Name string `json:"name"`
}

// Form8300Filer is the legal person the filing is made for: the branch that
// received the cash, in a one legal person dealer.
type Form8300Filer struct {
	BranchID   uuid.UUID `json:"branch_id"`
	BranchName string    `json:"branch_name"`
	Address    string    `json:"address"`
}

// Form8300 assembles the data set the filer types into BSA E-Filing. It
// opens the payer's taxpayer identification number out of the vault, because
// the form cannot be completed without it, and it audits the export without
// the number in the audit body.
func (s *Service) Form8300(ctx context.Context, eventID uuid.UUID, actor string) (*Form8300, error) {
	facts, err := s.repo.Form8300Facts(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if facts.State != EventStateReportable {
		// A Canadian crossing has no IRS form and an identity declined one
		// has no payer to put on it.
		if facts.State == EventStateIdentityDeclined {
			return nil, ErrFilingIdentityDeclined
		}
		return nil, ErrFilingNotReportable
	}
	tin := ""
	if facts.TINSealed != "" {
		opened, err := s.vault.Open(facts.TINSealed)
		if err != nil {
			return nil, err
		}
		tin = opened
	}
	out := &Form8300{
		EventID:          eventID,
		Form:             "Form 8300",
		Currency:         facts.Currency,
		AmountCents:      facts.TotalCents,
		TransactionCount: facts.TransactionCount,
		TenderCount:      facts.TenderCount,
		FirstDate:        facts.FirstDate,
		LastDate:         facts.LastDate,
		ReceivedAt:       facts.ReceivedAt,
		FilingDeadline:   FilingDeadline(facts.ReceivedAt),
		Payer: Form8300Payer{
			Name:        facts.PayerName,
			Address:     facts.PayerAddress,
			TaxpayerID:  tin,
			DateOfBirth: facts.PayerDOB,
			Occupation:  facts.PayerOccupation,
		},
		Filer: Form8300Filer{
			BranchID:   facts.BranchID,
			BranchName: facts.BranchName,
			Address:    facts.BranchAddress,
		},
		Note:         GableDoesNotFile,
		AdviserNote:  AdviserNote,
		TaxpayerIDHint: tinHint(tin),
	}
	if facts.ThirdPartyPayer {
		out.ThirdPartyPayer = &Form8300Third{Name: facts.ThirdPartyName}
	}
	// The audit row names the export and its figures. It never carries the
	// taxpayer identification number, not even a masked one.
	if err := audit.NewLogger(s.db).Log(ctx, audit.Entry{
		Action: "large_cash.form_8300.exported", EntityType: "large_cash_event", EntityID: eventID, UserID: actor,
		Changes: map[string]interface{}{
			"rule_family": facts.RuleFamily, "branch_id": facts.BranchID.String(),
			"total_cents": facts.TotalCents, "payer_captured": facts.PayerCaptured,
		}}); err != nil {
		return nil, err
	}
	return out, nil
}

// tinHint is the masked hint a taxpayer identification number shows: four
// asterisks and its last four characters, or four asterisks alone when the
// number is too short to hint safely. It is the only form of the number that
// may sit on a surface that is not the Form 8300 export.
func tinHint(tin string) *string {
	t := strings.TrimSpace(tin)
	if t == "" {
		return nil
	}
	hint := "****"
	if len(t) > 4 {
		hint += t[len(t)-4:]
	}
	return &hint
}

// MarkPastDeadlines is the deadline sweep: it marks the filings that are
// past their deadline and still not filed, so the worklist carries the mark
// and a report can find them without recomputing the clock. It is
// idempotent: a second pass marks nothing and changes nothing.
func (s *Service) MarkPastDeadlines(ctx context.Context, now time.Time) (int64, error) {
	return s.repo.MarkPastDeadlines(ctx, now)
}

// EventWallFacts are the branches a crossing belongs to: the branch that
// received the cash and every branch that contributed to the aggregate. A
// branch scoped key sees a crossing when its own branch is one of them.
type EventWallFacts struct {
	EventID              uuid.UUID
	BranchID             uuid.UUID
	ContributedBranchIDs []uuid.UUID
}

// EventWall answers the branches a crossing belongs to, for the wall that
// holds a crossing to the caller's branch.
func (s *Service) EventWall(ctx context.Context, eventID uuid.UUID) (*EventWallFacts, error) {
	facts, err := s.repo.EventWallFacts(ctx, eventID)
	if err != nil {
		return nil, err
	}
	return facts, nil
}
