// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash

import (
	"context"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// The statement's own state (migration 149). The customer statement is a
// separate obligation from the filing and runs on its own clock: the year
// boundary, not the 15 day deadline.
const (
	StatementRowNotIssued = "not_issued"
	StatementRowIssued    = "issued"
)

// ErrStatementRefusesSuspicion is the first of the two refusals research 6.3
// names: the customer statement must not say the dealer noted the
// transaction was suspicious, so the generator refuses to put the flag on it.
// The rule belongs in the code, not in a training note.
var ErrStatementRefusesSuspicion = &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
	Message: "the customer statement must not say the dealer noted the transaction was suspicious",
	Details: []httpx.FieldError{httpx.Blocker("statement_refuses_suspicion",
		"the customer statement must not say the dealer noted the transaction was suspicious")}}

// ErrStatementRefusesVoluntary is the second refusal research 6.3 names: a
// voluntarily filed Form 8300 is never stated, because the statement says the
// information was furnished to the IRS and a voluntary filing was not.
var ErrStatementRefusesVoluntary = &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
	Message: "a voluntary filing is never stated: the customer statement says the information was furnished to the IRS",
	Details: []httpx.FieldError{httpx.Blocker("statement_refuses_voluntary",
		"a voluntary filing is never stated: the customer statement says the information was furnished to the IRS")}}

// ErrStatementNotFiled is the statement's gate: the statement says the
// information was furnished to the IRS, so it goes out only for a reportable
// crossing whose Form 8300 is recorded as filed.
var ErrStatementNotFiled = &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
	Message: "the customer statement goes out only after the crossing's Form 8300 is recorded as filed",
	Details: []httpx.FieldError{httpx.Blocker("statement_not_filed",
		"the customer statement goes out only after the crossing's Form 8300 is recorded as filed")}}

// ErrStatementAlreadyIssued is the state conflict: the statement for a payer
// and a year goes out once.
var ErrStatementAlreadyIssued = &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
	Message: "this customer's statement for the year has already been issued",
	Details: []httpx.FieldError{httpx.Blocker("statement_already_issued",
		"this customer's statement for the year has already been issued")}}

// Statement is the statement the customer gets for the year's large cash
// receipts: the business name and address, the contact name and telephone
// number, the aggregate amount of reportable cash received in the year, and
// the sentence that says the information was furnished to the IRS. It never
// carries the dealer's suspicion of the transaction.
type Statement struct {
	PayerKey        string     `json:"payer_key"`
	Year            int        `json:"year"`
	PayerName       string     `json:"payer_name"`
	PayerAddress    string     `json:"payer_address"`
	BusinessName    string     `json:"business_name"`
	BusinessAddress string     `json:"business_address"`
	ContactName     string     `json:"contact_name"`
	Telephone       string     `json:"telephone"`
	AggregateCents  int64      `json:"aggregate_cents"`
	Currency        string     `json:"currency"`
	CrossingCount   int        `json:"crossing_count"`
	FirstDate       *time.Time `json:"first_date,omitempty"`
	LastDate        *time.Time `json:"last_date,omitempty"`
	State           string     `json:"state"`
	IssuedDate      *time.Time `json:"issued_date,omitempty"`
	IssuedBy        string     `json:"issued_by,omitempty"`
	DueDate         time.Time  `json:"due_date"`
	Note            string     `json:"note"`
	AdviserNote     string     `json:"adviser_note"`
}

// Statements is the year's statements, one per payer.
type Statements struct {
	Year  int         `json:"year"`
	Items []Statement `json:"items"`
	Count int         `json:"count"`
}

// StatementDueDate is the statement's own deadline as a rule: the statement
// goes out on or before the last day of the first month of the year after
// the year the cash was received.
func StatementDueDate(year int) time.Time {
	return time.Date(year+1, time.January, 31, 0, 0, 0, 0, time.UTC)
}

// Statements answers the year's customer statements, one per payer who had
// reportable cash in it. A branch scoped caller sees the payers whose
// crossings touched its branch; the aggregate each statement carries is the
// dealer's whole year for that payer, because the United States aggregation
// is deliberately central.
func (s *Service) Statements(ctx context.Context, year int) (*Statements, error) {
	if year <= 0 {
		year = s.now().Year()
	}
	rows, err := s.repo.StatementRows(ctx, year, branchctx.IDForQuery(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]Statement, 0, len(rows))
	for _, rec := range rows {
		out = append(out, statementOf(rec, year))
	}
	return &Statements{Year: year, Items: out, Count: len(out)}, nil
}

// statementOf finishes one statement row: the due date is a rule and the
// note says what the statement says.
func statementOf(rec statementRecord, year int) Statement {
	return Statement{
		PayerKey:        rec.PayerKey,
		Year:            year,
		PayerName:       rec.PayerName,
		PayerAddress:    rec.PayerAddress,
		BusinessName:    rec.BusinessName,
		BusinessAddress: rec.BusinessAddress,
		ContactName:     rec.ContactName,
		Telephone:       rec.Telephone,
		AggregateCents:  rec.AggregateCents,
		Currency:        rec.Currency,
		CrossingCount:   rec.CrossingCount,
		FirstDate:       rec.FirstDate,
		LastDate:        rec.LastDate,
		State:           rec.State,
		IssuedDate:      rec.IssuedDate,
		IssuedBy:        rec.IssuedBy,
		DueDate:         StatementDueDate(year),
		Note:            StatementFurnishedNote,
		AdviserNote:     AdviserNote,
	}
}

// IssueStatementIn is the manager's ask: the crossing whose payer the
// statement is for, and whether the manager asked for the dealer's suspicion
// to be put on it. The generator refuses that ask.
type IssueStatementIn struct {
	EventID uuid.UUID
	// IncludeSuspicion asks the generator to carry the dealer's suspicion of
	// the transaction on the customer's statement. It refuses.
	IncludeSuspicion bool
	Actor            string
}

// IssueStatement issues the customer statement for the crossing's payer and
// the year the cash was received. It refuses a crossing that is not a
// recorded filing, and a voluntary one, because the statement says the
// information was furnished to the IRS, and it refuses to carry the dealer's
// suspicion. Everything runs in one transaction.
func (s *Service) IssueStatement(ctx context.Context, in IssueStatementIn) (*Statement, error) {
	if in.IncludeSuspicion {
		return nil, ErrStatementRefusesSuspicion
	}
	actor := actorOrSystem(in.Actor)
	issuedDate := s.now()
	var statement *Statement
	err := s.db.RunInTx(ctx, func(ctx context.Context) error {
		facts, err := s.repo.StatementFacts(ctx, in.EventID)
		if err != nil {
			return err
		}
		if facts.FilingState == FilingRowVoluntary {
			return ErrStatementRefusesVoluntary
		}
		if facts.EventState != EventStateReportable || facts.FilingState != FilingRowFiled {
			return ErrStatementNotFiled
		}
		// The write names the state it may take the row from, so of several
		// managers issuing one statement exactly one gets the row.
		issued, err := s.repo.IssueStatement(ctx, facts.PayerKey, facts.Year, issuedDate, actor)
		if err != nil {
			return err
		}
		if !issued {
			return ErrStatementAlreadyIssued
		}
		statement = &Statement{
			PayerKey:        facts.PayerKey,
			Year:            facts.Year,
			PayerName:       facts.PayerName,
			PayerAddress:    facts.PayerAddress,
			BusinessName:    facts.BusinessName,
			BusinessAddress: facts.BusinessAddress,
			ContactName:     facts.ContactName,
			Telephone:       facts.Telephone,
			AggregateCents:  facts.AggregateCents,
			Currency:        facts.Currency,
			CrossingCount:   facts.CrossingCount,
			FirstDate:       facts.FirstDate,
			LastDate:        facts.LastDate,
			State:           StatementRowIssued,
			IssuedDate:      &issuedDate,
			IssuedBy:        actor,
			DueDate:         StatementDueDate(facts.Year),
			Note:            StatementFurnishedNote,
			AdviserNote:     AdviserNote,
		}
		return audit.NewLogger(s.db).Log(ctx, audit.Entry{
			Action: "large_cash.statement.issued", EntityType: "large_cash_event", EntityID: in.EventID, UserID: actor,
			Changes: map[string]interface{}{
				"payer_key": facts.PayerKey, "year": facts.Year, "aggregate_cents": facts.AggregateCents,
				"issued_date": issuedDate.Format("2006-01-02"),
			}})
	})
	if err != nil {
		return nil, err
	}
	return statement, nil
}
