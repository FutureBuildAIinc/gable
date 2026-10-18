// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/bankrecon"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// Events of the branch day (ADR 0005 section 12).
const (
	EventBranchDayClosed   = "till.day_closed"
	EventDepositMatched    = "till.deposit_matched"
	EventBranchDayReopened = "till.day_reopened"
	dayCandidateSpanBefore = 3
	dayCandidateSpanAfter  = 10
	dayCandidateLimit      = 20
)

// BankLines is the bank statement as the day close reads it
// (bankrecon.DepositLines satisfies it): the line, the free deposits near a
// day, and the claim that takes one for a day.
type BankLines interface {
	Line(ctx context.Context, id uuid.UUID) (*bankrecon.BankTransaction, error)
	Claim(ctx context.Context, id uuid.UUID, entry *uuid.UUID) (*bankrecon.BankTransaction, error)
	AccountGL(ctx context.Context, id uuid.UUID) (string, error)
	Candidates(ctx context.Context, from, to time.Time, amount int64, limit int) ([]bankrecon.BankTransaction, error)
}

// WithBankLines wires the bank statement lines the deposit match reads.
func (s *Service) WithBankLines(b BankLines) *Service { s.bank = b; return s }

// DaySession is one drawer shift of the day, with the figures its count froze
// (or, for a drawer still open, the figures so far).
type DaySession struct {
	ID                   uuid.UUID         `json:"id"`
	Status               TillSessionStatus `json:"status"`
	CashierID            uuid.UUID         `json:"cashier_id"`
	ClosedBy             *string           `json:"closed_by"`
	OpenedAt             httpx.Timestamp   `json:"opened_at"`
	ClosedAt             *httpx.Timestamp  `json:"closed_at"`
	OpeningFloatCents    httpx.Cents       `json:"opening_float_cents"`
	SaleCount            int               `json:"sale_count"`
	SalesTotalCents      httpx.Cents       `json:"sales_total_cents"`
	TenderedByMethod     map[string]int64  `json:"tendered_by_method"`
	MovementTotalsByKind map[string]int64  `json:"movement_totals_by_kind"`
	ExpectedByMethod     map[string]int64  `json:"expected_by_method"`
	CountedByMethod      map[string]int64  `json:"counted_by_method"`
	OverShortCents       httpx.Cents       `json:"over_short_cents"`
	SignedOffBy          *string           `json:"signed_off_by"`
}

// DayRegister is one register's sessions of the day and what they add to.
type DayRegister struct {
	RegisterID string       `json:"register_id"`
	Sessions   []DaySession `json:"sessions"`
	DayFigures
}

// DayFigures are the additive figures of a register or of the whole branch
// day: every one is the sum of its sessions' figures, in cents.
type DayFigures struct {
	SessionCount         int              `json:"session_count"`
	SaleCount            int              `json:"sale_count"`
	SalesTotalCents      httpx.Cents      `json:"sales_total_cents"`
	TenderedByMethod     map[string]int64 `json:"tendered_by_method"`
	MovementTotalsByKind map[string]int64 `json:"movement_totals_by_kind"`
	ExpectedByMethod     map[string]int64 `json:"expected_by_method"`
	CountedByMethod      map[string]int64 `json:"counted_by_method"`
	OverShortCents       httpx.Cents      `json:"over_short_cents"`
	CashToDepositCents   httpx.Cents      `json:"cash_to_deposit_cents"`
	ChecksToDepositCents httpx.Cents      `json:"checks_to_deposit_cents"`
	CardTotalCents       httpx.Cents      `json:"card_total_cents"`
}

// DepositMatch is the bank statement line a day's deposit was matched to.
type DepositMatch struct {
	BankTransactionID   uuid.UUID       `json:"bank_transaction_id"`
	BankAmountCents     httpx.Cents     `json:"bank_amount_cents"`
	BankTransactionDate string          `json:"bank_transaction_date"`
	BankDescription     string          `json:"bank_description"`
	DifferenceCents     httpx.Cents     `json:"difference_cents"`
	MatchedBy           string          `json:"matched_by"`
	MatchedAt           httpx.Timestamp `json:"matched_at"`
}

// DayClose is the act of closing a branch's business date: who, when, the
// deposit slip reference and the frozen totals.
type DayClose struct {
	ID                   uuid.UUID       `json:"id"`
	BranchID             uuid.UUID       `json:"branch_id"`
	BusinessDate         string          `json:"business_date"`
	Status               string          `json:"-"`
	DepositSlipRef       string          `json:"deposit_slip_ref"`
	ClosedBy             string          `json:"closed_by"`
	ClosedAt             httpx.Timestamp `json:"closed_at"`
	CashToDepositCents   httpx.Cents     `json:"cash_to_deposit_cents"`
	ChecksToDepositCents httpx.Cents     `json:"checks_to_deposit_cents"`
	CardTotalCents       httpx.Cents     `json:"card_total_cents"`
	Totals               json.RawMessage `json:"totals"`
	// BankAccountID is the bank account the deposit goes to and GLEntryID the
	// journal entry that posted it; both null for a day with nothing to bank.
	BankAccountID *uuid.UUID    `json:"bank_account_id"`
	GLEntryID     *uuid.UUID    `json:"gl_entry_id"`
	DepositMatch  *DepositMatch `json:"deposit_match"`
}

// BranchDay is the end of day by branch (CP-09): each register's sessions and
// figures for a business date in the branch's time zone, the branch totals,
// and the day's close once it exists.
type BranchDay struct {
	BranchID     uuid.UUID     `json:"branch_id"`
	BusinessDate string        `json:"business_date"`
	Registers    []DayRegister `json:"registers"`
	Totals       DayFigures    `json:"totals"`
	// OpenSessionCount counts the drawers still open and PendingSignoffCount
	// the counted ones waiting for a manager; the day closes at zero of each.
	OpenSessionCount    int  `json:"open_session_count"`
	PendingSignoffCount int  `json:"pending_signoff_count"`
	ReadyToClose        bool `json:"ready_to_close"`
	// CollectionsNetCents is the branch's net collections of the date as the
	// Daily Till counts them (payments received less refunds, pkg/collections),
	// every payment the branch took that day, counter or not.
	CollectionsNetCents httpx.Cents `json:"collections_net_cents"`
	Close               *DayClose   `json:"close"`
}

// newFigures is an empty set of figures with its maps made.
func newFigures() DayFigures {
	return DayFigures{TenderedByMethod: map[string]int64{}, MovementTotalsByKind: map[string]int64{},
		ExpectedByMethod: map[string]int64{}, CountedByMethod: map[string]int64{}}
}

func addMap(dst, src map[string]int64) {
	for k, v := range src {
		dst[k] += v
	}
}

// add folds one session's figures into a register's or the branch's.
func (f *DayFigures) add(d *DaySession, drawerOpen bool) {
	f.SessionCount++
	f.SaleCount += d.SaleCount
	f.SalesTotalCents += d.SalesTotalCents
	addMap(f.TenderedByMethod, d.TenderedByMethod)
	addMap(f.MovementTotalsByKind, d.MovementTotalsByKind)
	addMap(f.ExpectedByMethod, d.ExpectedByMethod)
	addMap(f.CountedByMethod, d.CountedByMethod)
	f.OverShortCents += d.OverShortCents
	// The cash to deposit is what the drawer holds beyond its float plus what
	// left it for the safe: the counted cash (the expected, until it is
	// counted), less the opening float, plus the drops and pickups. Checks are
	// counted or, uncounted, what was tendered. The card total is the net card
	// the session collected.
	cash, ok := d.CountedByMethod["CASH"]
	if !ok || drawerOpen {
		cash = d.ExpectedByMethod["CASH"]
	}
	f.CashToDepositCents += httpx.Cents(cash - int64(d.OpeningFloatCents) + d.MovementTotalsByKind[string(MovementSafeDrop)] + d.MovementTotalsByKind[string(MovementPickup)])
	check, ok := d.CountedByMethod["CHECK"]
	if !ok || drawerOpen {
		check = d.ExpectedByMethod["CHECK"]
	}
	f.ChecksToDepositCents += httpx.Cents(check)
	f.CardTotalCents += httpx.Cents(d.ExpectedByMethod["CARD"])
}

// daySession builds a session's figures. A closed drawer is read from its
// frozen Z snapshot (a snapshot is never recomputed); one still open, or
// counted and waiting for a manager, from the live report with the expected
// figures the count stored.
func (s *Service) daySession(ctx context.Context, t *TillSession) (*DaySession, error) {
	var report *TillReport
	if t.Status == TillClosed {
		if z, err := s.repo.GetZReportBySession(ctx, t.ID); err == nil {
			var frozen TillReport
			if jerr := json.Unmarshal(z.Payload, &frozen); jerr == nil {
				report = &frozen
			}
		}
	}
	if report == nil {
		live, err := s.TillReport(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		if t.Status != TillOpen {
			live.ExpectedByMethod = t.ExpectedByMethod
		}
		report = live
	}
	d := &DaySession{ID: t.ID, Status: t.Status, CashierID: t.CashierID, ClosedBy: t.ClosedBy, OpenedAt: t.OpenedAt,
		ClosedAt: t.ClosedAt, OpeningFloatCents: t.OpeningFloat, SaleCount: report.SaleCount,
		SalesTotalCents: report.SalesTotalCents, TenderedByMethod: nonNil(report.TenderedByMethod),
		MovementTotalsByKind: nonNil(report.MovementTotalsByKind), ExpectedByMethod: nonNil(report.ExpectedByMethod),
		CountedByMethod: nonNil(t.CountedByMethod)}
	if t.OverShort != nil {
		d.OverShortCents = *t.OverShort
	}
	if t.SignOff != nil {
		by := t.SignOff.SignedOffBy
		d.SignedOffBy = &by
	}
	return d, nil
}

func nonNil(m map[string]int64) map[string]int64 {
	if m == nil {
		return map[string]int64{}
	}
	return m
}

// parseBusinessDate reads a YYYY-MM-DD business date.
func parseBusinessDate(s string) (time.Time, error) {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "date", Message: "must be a date as YYYY-MM-DD"})
	}
	return d, nil
}

// BranchDay reads the end of day summary for a branch and a business date
// ("" is the branch's today), inside the caller's wall.
func (s *Service) BranchDay(ctx context.Context, branch uuid.UUID, date string) (*BranchDay, error) {
	if err := s.grants.CheckPayloadBranch(ctx, branch); err != nil {
		return nil, branchRefusal(err, "branch_id")
	}
	return s.branchDay(ctx, branch, date)
}

func (s *Service) branchDay(ctx context.Context, branch uuid.UUID, date string) (*BranchDay, error) {
	if _, ok, err := s.repo.BranchZone(ctx, branch); err != nil {
		return nil, err
	} else if !ok {
		return nil, httpx.NotFound("branch not found")
	}
	if date == "" {
		today, err := s.repo.BranchToday(ctx, branch)
		if err != nil {
			return nil, err
		}
		date = today.Format("2006-01-02")
	} else if _, err := parseBusinessDate(date); err != nil {
		return nil, err
	}
	sessions, err := s.repo.ListBranchDaySessions(ctx, branch, date)
	if err != nil {
		return nil, err
	}
	day := &BranchDay{BranchID: branch, BusinessDate: date, Registers: []DayRegister{}, Totals: newFigures()}
	byRegister := map[string]*DayRegister{}
	for i := range sessions {
		t := &sessions[i]
		d, err := s.daySession(ctx, t)
		if err != nil {
			return nil, err
		}
		reg := byRegister[t.RegisterID]
		if reg == nil {
			reg = &DayRegister{RegisterID: t.RegisterID, Sessions: []DaySession{}, DayFigures: newFigures()}
			byRegister[t.RegisterID] = reg
		}
		open := t.Status == TillOpen
		reg.Sessions = append(reg.Sessions, *d)
		reg.add(d, open)
		day.Totals.add(d, open)
		switch t.Status {
		case TillOpen:
			day.OpenSessionCount++
		case TillPendingSignoff:
			day.PendingSignoffCount++
		}
	}
	ids := make([]string, 0, len(byRegister))
	for id := range byRegister {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		day.Registers = append(day.Registers, *byRegister[id])
	}
	day.ReadyToClose = day.OpenSessionCount == 0 && day.PendingSignoffCount == 0
	if day.CollectionsNetCents, err = s.repo.BranchCollectionsNet(ctx, branch, date); err != nil {
		return nil, err
	}
	if day.Close, err = s.repo.GetDayClose(ctx, branch, date); err != nil {
		return nil, err
	}
	return day, nil
}

// CloseBranchDay closes a branch's business date: refused while any till of
// the branch dated that day or before is open or waiting for a manager, once
// per branch per date, and never a date the branch has not reached. It writes
// who, when, the deposit slip reference and the frozen totals in one
// transaction with its audit row and event.
func (s *Service) CloseBranchDay(ctx context.Context, branch uuid.UUID, date, slipRef string, bankAccount *uuid.UUID, actor string) (*BranchDay, error) {
	slipRef = strings.TrimSpace(slipRef)
	if slipRef == "" {
		return nil, invalid("deposit_slip_ref", "is required: the reference printed on the deposit slip")
	}
	if _, err := parseBusinessDate(date); err != nil {
		return nil, err
	}
	if err := s.grants.CheckPayloadBranch(ctx, branch); err != nil {
		return nil, branchRefusal(err, "branch_id")
	}
	var out *BranchDay
	err := s.inTx(ctx, func(ctx context.Context) error {
		// The branch's day lock first, the same one a till open takes: a drawer
		// cannot open between the check below and the close it guards.
		if err := s.repo.LockBranchDay(ctx, branch); err != nil {
			return err
		}
		today, err := s.repo.BranchToday(ctx, branch)
		if err != nil {
			return err
		}
		if date > today.Format("2006-01-02") {
			return conflict("date_not_reached", fmt.Sprintf("the branch's business date is %s: a later day cannot be closed", today.Format("2006-01-02")))
		}
		day, err := s.branchDay(ctx, branch, date)
		if err != nil {
			return err
		}
		if day.Close != nil {
			return conflict("day_already_closed", fmt.Sprintf("the branch's %s is already closed", date))
		}
		blocked, err := s.repo.ListUnsettledSessions(ctx, branch, date)
		if err != nil {
			return err
		}
		if len(blocked) > 0 {
			return dayBlockedError(blocked)
		}
		snapshot := *day
		snapshot.Close = nil
		totals, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		c := &DayClose{ID: uuid.New(), BranchID: branch, BusinessDate: date, DepositSlipRef: slipRef, ClosedBy: actor,
			CashToDepositCents: day.Totals.CashToDepositCents, ChecksToDepositCents: day.Totals.ChecksToDepositCents,
			CardTotalCents: day.Totals.CardTotalCents, Totals: totals, BankAccountID: bankAccount}
		if err := s.postDeposit(ctx, c, day, actor); err != nil {
			return err
		}
		if err := s.repo.CreateDayClose(ctx, c); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "pos.day.closed", EntityType: "till_day_close", EntityID: c.ID, UserID: actor,
				Changes: map[string]any{"branch_id": branch, "business_date": date, "deposit_slip_ref": slipRef,
					"cash_to_deposit_cents": int64(c.CashToDepositCents), "checks_to_deposit_cents": int64(c.ChecksToDepositCents),
					"card_total_cents": int64(c.CardTotalCents), "over_short_cents": int64(day.Totals.OverShortCents),
					"bank_account_id": c.BankAccountID, "gl_entry_id": c.GLEntryID}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		b := branch
		if err := s.recordEvent(ctx, outbox.Event{Type: EventBranchDayClosed, EntityType: "till_day_close", EntityID: c.ID, BranchID: &b,
			Data: eventJSON(map[string]any{"branch_id": branch, "business_date": date, "deposit_slip_ref": slipRef,
				"cash_to_deposit_cents": int64(c.CashToDepositCents), "checks_to_deposit_cents": int64(c.ChecksToDepositCents)})}); err != nil {
			return err
		}
		day.Close = c
		out = day
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// dayBlockedError is the 409 of a day with a drawer still open or waiting:
// one blocker for each kind, naming the registers.
func dayBlockedError(blocked []TillSession) error {
	var open, pending []string
	for _, t := range blocked {
		if t.Status == TillOpen {
			open = append(open, t.RegisterID)
		} else {
			pending = append(pending, t.RegisterID)
		}
	}
	e := &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
		Message: "the branch day cannot close while a till is open or waiting for sign off"}
	if len(open) > 0 {
		e.Details = append(e.Details, httpx.Blocker("till_open", "close the open till of register "+strings.Join(open, ", ")))
	}
	if len(pending) > 0 {
		e.Details = append(e.Details, httpx.Blocker("signoff_pending", "a manager must sign off the counted till of register "+strings.Join(pending, ", ")))
	}
	return e
}

// DepositCandidates lists the unmatched deposit lines near a closed day,
// nearest to its deposit first.
func (s *Service) DepositCandidates(ctx context.Context, closeID uuid.UUID) ([]bankrecon.BankTransaction, error) {
	if s.bank == nil {
		return nil, httpx.Unavailable("the bank statement is not wired on this service")
	}
	if err := s.holdDayCloseBranch(ctx, closeID); err != nil {
		return nil, err
	}
	c, err := s.repo.GetDayCloseByID(ctx, closeID, false)
	if err != nil {
		return nil, err
	}
	date, _ := time.Parse("2006-01-02", c.BusinessDate)
	return s.bank.Candidates(ctx, date.AddDate(0, 0, -dayCandidateSpanBefore), date.AddDate(0, 0, dayCandidateSpanAfter),
		int64(c.CashToDepositCents+c.ChecksToDepositCents), dayCandidateLimit)
}

// MatchDeposit matches a closed day's deposit to a bank statement line: the
// line is claimed through bankrecon (an unmatched deposit, never one already
// taken), and the day records the line, the difference in cents (the bank's
// amount less the cash and checks the slip carried) and who matched it. A day
// matches once.
func (s *Service) MatchDeposit(ctx context.Context, closeID, bankLineID uuid.UUID, actor string) (*DayClose, error) {
	if s.bank == nil {
		return nil, httpx.Unavailable("the bank statement is not wired on this service")
	}
	if err := s.holdDayCloseBranch(ctx, closeID); err != nil {
		return nil, err
	}
	var out *DayClose
	err := s.inTx(ctx, func(ctx context.Context) error {
		c, err := s.repo.GetDayCloseByID(ctx, closeID, true)
		if err != nil {
			return err
		}
		if c.Status != "CLOSED" {
			return conflict("day_not_closed", "this branch day close has already been reopened")
		}
		if c.DepositMatch != nil {
			return conflict("deposit_already_matched", "this day's deposit is already matched to a bank statement line")
		}
		if c.BankAccountID != nil {
			// the deposit went to one bank account: its line is on that account's statement
			if probe, err := s.bank.Line(ctx, bankLineID); err == nil && probe.BankAccountID != *c.BankAccountID {
				return conflict("bank_account_mismatch", "the bank statement line is on another bank account than the one this day's deposit went to")
			}
		}
		line, err := s.bank.Claim(ctx, bankLineID, c.GLEntryID)
		switch {
		case errors.Is(err, bankrecon.ErrDepositLineNotFound):
			return httpx.NotFound("bank statement line not found")
		case errors.Is(err, bankrecon.ErrDepositLineNotFree):
			return conflict("bank_line_unavailable", "the bank statement line is not an unmatched deposit")
		case err != nil:
			return err
		}
		diff := line.Amount - int64(c.CashToDepositCents+c.ChecksToDepositCents)
		m := &DepositMatch{BankTransactionID: line.ID, BankAmountCents: httpx.Cents(line.Amount),
			BankTransactionDate: line.TransactionDate.Format("2006-01-02"), BankDescription: line.Description,
			DifferenceCents: httpx.Cents(diff), MatchedBy: actor}
		if err := s.repo.MatchDayClose(ctx, c.ID, m); err != nil {
			return err
		}
		c.DepositMatch = m
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "pos.day.deposit_matched", EntityType: "till_day_close", EntityID: c.ID, UserID: actor,
				Changes: map[string]any{"branch_id": c.BranchID, "business_date": c.BusinessDate, "bank_transaction_id": line.ID,
					"bank_amount_cents": line.Amount, "difference_cents": diff}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		b := c.BranchID
		if err := s.recordEvent(ctx, outbox.Event{Type: EventDepositMatched, EntityType: "till_day_close", EntityID: c.ID, BranchID: &b,
			Data: eventJSON(map[string]any{"branch_id": c.BranchID, "business_date": c.BusinessDate,
				"bank_transaction_id": line.ID, "difference_cents": diff})}); err != nil {
			return err
		}
		out = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// postDeposit posts the deposit a day close records, in the close's own
// transaction: debit the GL account of the bank account it goes to for the
// cash and checks deposited, credit 1015 Undeposited Funds for what the day's
// drops and pickups put there, and credit 1010 Cash for the rest (the counted
// drawer cash and checks beyond the floats kept; a debit when the drawers held
// less than the safe's share). Cards are not part of the deposit. A day with
// nothing to bank and nothing in 1015 posts nothing. The entry and the bank
// account are set on the close.
func (s *Service) postDeposit(ctx context.Context, c *DayClose, day *BranchDay, actor string) error {
	deposit := int64(c.CashToDepositCents + c.ChecksToDepositCents)
	undeposited := day.Totals.MovementTotalsByKind[string(MovementSafeDrop)] + day.Totals.MovementTotalsByKind[string(MovementPickup)]
	if deposit > 0 && c.BankAccountID == nil {
		return invalid("bank_account_id", "is required: the bank account the deposit goes to")
	}
	var bankCode string
	if c.BankAccountID != nil {
		if s.bank == nil {
			return httpx.Unavailable("the bank statement is not wired on this service")
		}
		code, err := s.bank.AccountGL(ctx, *c.BankAccountID)
		switch {
		case errors.Is(err, bankrecon.ErrBankAccountNotFound):
			return httpx.NotFound("bank account not found")
		case errors.Is(err, bankrecon.ErrBankAccountInactive):
			return conflict("bank_account_inactive", "the bank account is not active")
		case err != nil:
			return err
		}
		bankCode = code
	}
	if s.ledger == nil || (deposit <= 0 && undeposited == 0) {
		return nil
	}
	bankDebit := deposit
	if bankDebit < 0 {
		bankDebit = 0
	}
	legs := []gl.Leg{{AccountCode: "1015", Description: "Undeposited funds banked", Credit: undeposited}}
	if bankDebit > 0 {
		legs = append(legs, gl.Leg{AccountCode: bankCode, Description: "Branch deposit " + c.DepositSlipRef, Debit: bankDebit})
	}
	// what the bank takes beyond the safe's share came out of the drawers' cash
	if rest := bankDebit - undeposited; rest > 0 {
		legs = append(legs, gl.Leg{AccountCode: gl.AccountCodeCash, Description: "Cash deposited from the drawers", Credit: rest})
	} else if rest < 0 {
		legs = append(legs, gl.Leg{AccountCode: gl.AccountCodeCash, Description: "Safe cash not banked", Debit: -rest})
	}
	date, err := parseBusinessDate(c.BusinessDate)
	if err != nil {
		return err
	}
	entry, err := s.ledger.PostEntry(ctx, gl.PostingInput{EntryDate: date,
		Memo:   fmt.Sprintf("Branch deposit %s for %s", c.DepositSlipRef, c.BusinessDate),
		Source: gl.SourceAdjustment, SourceRefID: &c.ID, PostedBy: actorOrSystem(actor), Legs: legs})
	if err != nil {
		return ledgerRefusal(err, "failed to post the branch deposit")
	}
	if entry != nil {
		c.GLEntryID = &entry.ID
	}
	return nil
}

// ReopenBranchDay reopens a closed branch day (CP-09) for a late drawer or a
// correction: under the branch's day lock, the close's deposit entry is
// reversed (a reversal naming it, dated the branch's today), the close row is
// marked REOPENED with its frozen totals intact, and the audit row (with the
// reason) and till.day_reopened are written, all in one transaction. A day
// whose deposit is matched to a bank statement line cannot reopen (409
// deposit_already_matched): the bank already holds it. The day then closes
// again like any open day.
func (s *Service) ReopenBranchDay(ctx context.Context, closeID uuid.UUID, reason, actor string) (*BranchDay, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, invalid("reason", "is required: why the day reopens")
	}
	if err := s.holdDayCloseBranch(ctx, closeID); err != nil {
		return nil, err
	}
	branch, found, err := s.repo.DayCloseBranch(ctx, closeID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, httpx.NotFound("branch day close not found")
	}
	var out *BranchDay
	err = s.inTx(ctx, func(ctx context.Context) error {
		// the branch's day lock first, as the close and a drawer opening take it
		if err := s.repo.LockBranchDay(ctx, branch); err != nil {
			return err
		}
		c, err := s.repo.GetDayCloseByID(ctx, closeID, true)
		if err != nil {
			return err
		}
		if c.Status != "CLOSED" {
			return conflict("day_not_closed", "this branch day close has already been reopened")
		}
		if c.DepositMatch != nil {
			return conflict("deposit_already_matched", "this day's deposit is already matched to a bank statement line: it cannot reopen")
		}
		var reversalID *uuid.UUID
		if c.GLEntryID != nil && s.ledger != nil {
			today, err := s.repo.BranchToday(ctx, branch)
			if err != nil {
				return err
			}
			revEntry, err := s.ledger.PostReversal(ctx, gl.ReversalInput{EntryID: *c.GLEntryID, EntryDate: today,
				Reason: "branch day reopened: " + reason, PostedBy: actorOrSystem(actor)})
			if err != nil {
				return ledgerRefusal(err, "failed to reverse the branch deposit")
			}
			if revEntry != nil {
				reversalID = &revEntry.ID
			}
		}
		if err := s.repo.ReopenDayClose(ctx, c.ID, actor, reason, reversalID); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "pos.day.reopened", EntityType: "till_day_close", EntityID: c.ID, UserID: actor,
				Changes: map[string]any{"branch_id": branch, "business_date": c.BusinessDate, "reason": reason,
					"deposit_slip_ref": c.DepositSlipRef, "reversed_entry_id": c.GLEntryID, "reversal_gl_entry_id": reversalID}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		b := branch
		if err := s.recordEvent(ctx, outbox.Event{Type: EventBranchDayReopened, EntityType: "till_day_close", EntityID: c.ID, BranchID: &b,
			Data: eventJSON(map[string]any{"branch_id": branch, "business_date": c.BusinessDate, "reason": reason})}); err != nil {
			return err
		}
		day, err := s.branchDay(ctx, branch, c.BusinessDate)
		if err != nil {
			return err
		}
		out = day
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
