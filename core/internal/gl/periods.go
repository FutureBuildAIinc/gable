// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl

// Period administration, the year end close, the tie outs and the branch cut
// of the statements (rows GL-02, GL-03, MY-02). Every mutation runs in one
// transaction with its audit row and outbox event.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// ErrStaleRevision is the refusal of an update whose revision no longer
// matches the row: the caller answers 409 (stale_revision).
var ErrStaleRevision = errors.New("gl: the row was changed after this revision was read")

// ErrEntryNotFound and ErrAccountNotFound are ids that name no row: the
// act answers 404.
var (
	ErrEntryNotFound   = errors.New("gl: no such journal entry")
	ErrAccountNotFound = errors.New("gl: no such account")
)

// ErrPeriodNotFound is a period id that names no period: the act answers 404.
var ErrPeriodNotFound = errors.New("gl: no such fiscal period")

// ErrPeriodState is a close of a period that is not open or a reopen of one
// that is not closed: the act answers 409.
var ErrPeriodState = errors.New("gl: the fiscal period is not in the state the act needs")

// ErrTieOutHistoric is a tie out asked for a day before the business day: the
// subledgers keep only their current balances, so only today can be tied out.
var ErrTieOutHistoric = errors.New("gl: the tie out compares current balances and takes no earlier day")

// ErrReopenReason is the refusal to reopen a period without a reason.
var ErrReopenReason = errors.New("gl: a fiscal period is reopened with a reason")

// WithAuditTrail wires the transaction runner, the audit logger and the
// outbox writer the period and year end mutations ride. Any of them may be
// nil: the mutation then runs without its audit row or event (the tests that
// only exercise the ledger shapes do that).
func (s *Service) WithAuditTrail(db *database.DB, al *audit.Logger, ow *outbox.Writer) *Service {
	s.db, s.audit, s.events = db, al, ow
	return s
}

func (s *Service) auditRow(ctx context.Context, action, entityType string, entityID uuid.UUID, actor string, changes map[string]any) error {
	if s.audit == nil {
		return nil
	}
	return s.audit.Log(ctx, audit.Entry{Action: action, EntityType: entityType, EntityID: entityID, UserID: actor, Changes: changes})
}

func (s *Service) emit(ctx context.Context, typ, entityType string, entityID uuid.UUID, branch *uuid.UUID, data map[string]any) error {
	if s.events == nil {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{Type: typ, EntityType: entityType, EntityID: entityID, BranchID: branch, Data: raw})
}

// record writes an act's audit row and its outbox event, in the act's own
// transaction, attributed to the request's actor.
func (s *Service) record(ctx context.Context, typ, entityType string, entityID uuid.UUID, branch *uuid.UUID, data map[string]any) error {
	who := actor.FromContext(ctx).ID
	if who == "" {
		who = "system"
	}
	if err := s.auditRow(ctx, typ, entityType, entityID, who, data); err != nil {
		return err
	}
	return s.emit(ctx, typ, entityType, entityID, branch, data)
}

// runTx runs fn inside one transaction when a runner is wired, else inline
// (the mock repository tests have no database).
func (s *Service) runTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.db == nil {
		return fn(ctx)
	}
	return s.db.RunInTx(ctx, fn)
}

// filterFor composes the branch arm of a statement read: the named branch
// (nil reads consolidated) beside the caller's reach as the branch middleware
// settled it on the request.
func filterFor(ctx context.Context, branch *uuid.UUID) branchFilter {
	b := branch
	if b == nil {
		b = middleware.BranchIDForQuery(ctx)
	}
	return branchFilter{Branch: b, Grants: middleware.GrantsSubForQuery(ctx)}
}

// --- Statements by branch ---

// GetTrialBalanceFor sums per account, currency and branch at the caller's
// reach; branch nil reads consolidated.
func (s *Service) GetTrialBalanceFor(ctx context.Context, asOfDate time.Time, currency string, branch *uuid.UUID) ([]TrialBalanceRow, error) {
	if asOfDate.IsZero() {
		day, err := s.repo.BranchBusinessDay(ctx, branch)
		if err != nil {
			return nil, err
		}
		asOfDate = day
	}
	return s.repo.GetTrialBalanceFor(ctx, asOfDate, currency, filterFor(ctx, branch))
}

// GetProfitAndLossFor assembles the income statement of one branch or the
// consolidated one, with the comparison period when both its bounds are
// given.
func (s *Service) GetProfitAndLossFor(ctx context.Context, startDate, endDate, currency string, branch *uuid.UUID, compareStart, compareEnd string) (*ProfitAndLossReport, error) {
	report, err := s.GetProfitAndLoss(ctx, startDate, endDate, currency, branch)
	if err != nil {
		return nil, err
	}
	report.BranchID = branch
	if compareStart != "" || compareEnd != "" {
		previous, err := s.GetProfitAndLoss(ctx, compareStart, compareEnd, currency, branch)
		if err != nil {
			return nil, err
		}
		report.Comparison = &ProfitAndLossComparison{
			StartDate: previous.StartDate, EndDate: previous.EndDate,
			TotalRevenue: previous.TotalRevenue, TotalCOGS: previous.TotalCOGS,
			GrossProfit: previous.GrossProfit, TotalExpenses: previous.TotalExpenses,
			NetIncome: previous.NetIncome,
		}
	}
	return report, nil
}

// BusinessDay is the branch's own today (the default branch's for a
// consolidated read), the day a statement without a date reads.
func (s *Service) BusinessDay(ctx context.Context, branch *uuid.UUID) (time.Time, error) {
	return s.repo.BranchBusinessDay(ctx, branch)
}

// GetBalanceSheetFor assembles the statement of financial position of one
// branch or the consolidated one.
func (s *Service) GetBalanceSheetFor(ctx context.Context, asOfDate, currency string, branch *uuid.UUID) (*BalanceSheetReport, error) {
	report, err := s.GetBalanceSheet(ctx, asOfDate, currency, branch)
	if err != nil {
		return nil, err
	}
	report.BranchID = branch
	return report, nil
}

// GetGLDetail answers the posted lines behind a statement row: one account,
// a date window, one branch or every branch, at the caller's reach.
func (s *Service) GetGLDetail(ctx context.Context, accountID uuid.UUID, from, to time.Time, branch *uuid.UUID) ([]GLDetailRow, error) {
	return s.repo.GetGLDetail(ctx, accountID, from, to, filterFor(ctx, branch))
}

// --- Tie out (GL-02) ---

// TieOut answers the AR control account against the customer subledger and
// the AP control account against the vendor bill subledger, each difference
// in cents, as of the business day (the branch's own day, the only day the
// subledgers can answer), for one branch or consolidated.
func (s *Service) TieOut(ctx context.Context, asOf time.Time, currency string, branch *uuid.UUID) (*TieOutReport, error) {
	if currency == "" {
		var err error
		currency, err = s.repo.DefaultCurrency(ctx)
		if err != nil {
			return nil, err
		}
	}
	day, err := s.repo.BranchBusinessDay(ctx, branch)
	if err != nil {
		return nil, err
	}
	if asOf.IsZero() {
		asOf = day
	} else if asOf.Before(day) {
		// the subledgers hold only their current balances
		return nil, ErrTieOutHistoric
	}
	f := filterFor(ctx, branch)
	arControl, err := s.repo.ControlBalance(ctx, AccountCodeAR, asOf, currency, f)
	if err != nil {
		return nil, err
	}
	arSub, err := s.repo.ARSubledgerCents(ctx, f, currency)
	if err != nil {
		return nil, err
	}
	// The AP control is credit normal: its balance is credits less debits.
	apDr, err := s.repo.ControlBalance(ctx, AccountCodeAP, asOf, currency, f)
	if err != nil {
		return nil, err
	}
	apControl := -apDr
	apSub, err := s.repo.APSubledgerCents(ctx, f, currency)
	if err != nil {
		return nil, err
	}
	return &TieOutReport{
		AsOf: asOf.Format(statementDateLayout), Currency: currency, BranchID: branch,
		AR: TieOutLeg{ControlCents: arControl, SubledgerCents: arSub, DifferenceCents: arControl - arSub},
		AP: TieOutLeg{ControlCents: apControl, SubledgerCents: apSub, DifferenceCents: apControl - apSub},
	}, nil
}

// --- Year end close (GL-02) ---

// YearEndResult names the branches the year end close wrote an entry for.
type YearEndResult struct {
	Year     int         `json:"year"`
	Currency string      `json:"currency"`
	Entries  []uuid.UUID `json:"entries"`
}

// CloseYear zeroes the year's revenue and expense accounts into Retained
// Earnings (3020), one balanced CLOSING entry per branch that holds activity,
// dated the year's last day. Each entry rides one transaction with its audit
// row and outbox event. A closed period on that date refuses the close the
// same way any posting is refused (the periods are reopened first, with a
// reason).
func (s *Service) CloseYear(ctx context.Context, year int, currency, actor string) (*YearEndResult, error) {
	if year < 1 || year > 9999 {
		return nil, fmt.Errorf("the year %d is not one the calendar holds", year)
	}
	if currency == "" {
		var err error
		currency, err = s.repo.DefaultCurrency(ctx)
		if err != nil {
			return nil, err
		}
	}
	closingDate := time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC)
	out := &YearEndResult{Year: year, Currency: currency}
	err := s.runTx(ctx, func(ctx context.Context) error {
		byBranch, err := s.repo.YearActivityByBranch(ctx, year, currency)
		if err != nil {
			return err
		}
		branches := make([]uuid.UUID, 0, len(byBranch))
		for branch := range byBranch {
			branches = append(branches, branch)
		}
		sort.Slice(branches, func(i, j int) bool { return branches[i].String() < branches[j].String() })
		for _, branch := range branches {
			rows := byBranch[branch]
			legs := make([]Leg, 0, len(rows)+1)
			var net int64
			for _, row := range rows {
				amount := signedBalance(row)
				if amount == 0 {
					continue
				}
				net += signedBalanceToEquity(row)
				if row.AccountType == AccountTypeRevenue {
					// zero a revenue account: debit what it holds
					legs = append(legs, Leg{AccountCode: row.AccountCode, Description: "Year end close: " + row.AccountName, Debit: amount})
				} else {
					legs = append(legs, Leg{AccountCode: row.AccountCode, Description: "Year end close: " + row.AccountName, Credit: amount})
				}
			}
			if net < 0 {
				legs = append(legs, Leg{AccountCode: AccountCodeRetainedEarnings, Description: "Year end close: net loss", Debit: -net})
			} else if net > 0 {
				legs = append(legs, Leg{AccountCode: AccountCodeRetainedEarnings, Description: "Year end close: net income", Credit: net})
			}
			if len(legs) == 0 {
				continue
			}
			b := branch
			entry, err := s.PostEntry(ctx, PostingInput{
				EntryDate: closingDate, Memo: fmt.Sprintf("Year end close %d", year),
				Source: SourceClosing, BranchID: b, Currency: currency, PostedBy: actor, Legs: legs,
			})
			if err != nil {
				return err
			}
			if entry == nil {
				continue
			}
			out.Entries = append(out.Entries, entry.ID)
			if err := s.auditRow(ctx, "gl.year_closed", "gl_journal_entry", entry.ID, actor,
				map[string]any{"year": year, "branch_id": b, "currency": currency}); err != nil {
				return err
			}
			if err := s.emit(ctx, "gl.year_closed", "gl_journal_entry", entry.ID, &b,
				map[string]any{"year": year, "branch_id": b, "currency": currency}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// signedBalanceToEquity answers what the row's signed balance adds to net
// income (revenue up, expense down), the amount Retained Earnings takes.
func signedBalanceToEquity(row AccountActivity) int64 {
	if row.AccountType == AccountTypeRevenue {
		return signedBalance(row)
	}
	return -signedBalance(row)
}

// --- Period close and reopen (GL-02) ---

// CloseFiscalPeriodFor closes an open period after the caller's precondition
// matched its revision, with the audit row and the outbox event inside the
// same transaction.
func (s *Service) CloseFiscalPeriodFor(ctx context.Context, id uuid.UUID, expectedRevision int64, actor string) (*FiscalPeriod, error) {
	var out *FiscalPeriod
	err := s.runTx(ctx, func(ctx context.Context) error {
		period, err := s.repo.GetFiscalPeriod(ctx, id)
		if err != nil {
			return err
		}
		if period.Revision != expectedRevision {
			return ErrStaleRevision
		}
		if period.Status == PeriodClosed {
			return ErrPeriodState
		}
		if err := s.repo.CloseFiscalPeriod(ctx, id, actor); err != nil {
			if errors.Is(err, ErrPeriodState) {
				// another writer closed it between the read and the update
				return ErrStaleRevision
			}
			return err
		}
		period, err = s.repo.GetFiscalPeriod(ctx, id)
		if err != nil {
			return err
		}
		if err := s.auditRow(ctx, "gl.period_closed", "gl_fiscal_period", id, actor,
			map[string]any{"name": period.Name, "start_date": period.StartDate.Format(statementDateLayout), "end_date": period.EndDate.Format(statementDateLayout)}); err != nil {
			return err
		}
		if err := s.emit(ctx, "gl.period_closed", "gl_fiscal_period", id, nil,
			map[string]any{"name": period.Name, "revision": period.Revision}); err != nil {
			return err
		}
		out = period
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReopenFiscalPeriodFor flips a closed period back open, refusing one without
// a reason; the reason, the actor and the revision ride the row, the audit
// and the event.
func (s *Service) ReopenFiscalPeriodFor(ctx context.Context, id uuid.UUID, expectedRevision int64, reason, actor string) (*FiscalPeriod, error) {
	if reason == "" {
		return nil, ErrReopenReason
	}
	var out *FiscalPeriod
	err := s.runTx(ctx, func(ctx context.Context) error {
		period, err := s.repo.GetFiscalPeriod(ctx, id)
		if err != nil {
			return err
		}
		if period.Revision != expectedRevision {
			return ErrStaleRevision
		}
		if period.Status != PeriodClosed {
			return ErrPeriodState
		}
		if err := s.repo.ReopenFiscalPeriod(ctx, id, reason, actor); err != nil {
			if errors.Is(err, ErrPeriodState) {
				return ErrStaleRevision
			}
			return err
		}
		period, err = s.repo.GetFiscalPeriod(ctx, id)
		if err != nil {
			return err
		}
		if err := s.auditRow(ctx, "gl.period_reopened", "gl_fiscal_period", id, actor,
			map[string]any{"name": period.Name, "reason": reason}); err != nil {
			return err
		}
		if err := s.emit(ctx, "gl.period_reopened", "gl_fiscal_period", id, nil,
			map[string]any{"name": period.Name, "reason": reason, "revision": period.Revision}); err != nil {
			return err
		}
		out = period
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- Wire facing wrappers ---

// ListAccountsPage answers one cursor page of the chart of accounts.
func (s *Service) ListAccountsPage(ctx context.Context, limit int, afterCode string) ([]GLAccount, error) {
	return s.repo.ListAccountsPage(ctx, limit, afterCode)
}

// ListJournalEntriesPage answers one cursor page of the journal entries at
// the caller's reach.
func (s *Service) ListJournalEntriesPage(ctx context.Context, limit, beforeNumber int, branch *uuid.UUID) ([]JournalEntry, error) {
	return s.repo.ListJournalEntriesPage(ctx, limit, beforeNumber, filterFor(ctx, branch))
}

// ListFiscalPeriodsPage answers one cursor page of the fiscal periods.
func (s *Service) ListFiscalPeriodsPage(ctx context.Context, limit int, afterStart time.Time) ([]FiscalPeriod, error) {
	return s.repo.ListFiscalPeriodsPage(ctx, limit, afterStart)
}

// GetFiscalPeriod answers one period by id, revision carried.
func (s *Service) GetFiscalPeriod(ctx context.Context, id uuid.UUID) (*FiscalPeriod, error) {
	return s.repo.GetFiscalPeriod(ctx, id)
}

// CreateManualJournalEntry writes a hand made entry with its audit row and
// outbox event inside the one transaction that writes it.
func (s *Service) CreateManualJournalEntry(ctx context.Context, entry *JournalEntry) error {
	return s.runTx(ctx, func(ctx context.Context) error {
		if err := s.CreateJournalEntry(ctx, entry); err != nil {
			return err
		}
		branch := entry.BranchID
		if err := s.auditRow(ctx, "gl.journal_entry_created", "gl_journal_entry", entry.ID, entry.PostedBy,
			map[string]any{"memo": entry.Memo, "branch_id": entry.BranchID, "source": entry.Source}); err != nil {
			return err
		}
		return s.emit(ctx, "gl.journal_entry_created", "gl_journal_entry", entry.ID, &branch,
			map[string]any{"memo": entry.Memo, "branch_id": entry.BranchID, "source": entry.Source})
	})
}
