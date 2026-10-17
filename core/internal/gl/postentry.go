// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Account codes the sales postings name (ADR 0005 section 8.1), beside the
// codes above.
const (
	AccountCodeInventory      = "1030"
	AccountCodeSalesTax       = "2020"
	AccountCodeAP             = "2010" // Accounts Payable: what vendors are owed (C4-1b, ADR 0008 7.4)
	AccountCodeCOGS           = "5010"
	AccountCodeDeliveryRev    = "4020"
	AccountCodeSalesDiscounts = "4050"
	AccountCodeBadDebt        = "5040"
	SourceCreditMemo          = "CREDIT_MEMO"
	SourceWriteOff            = "WRITE_OFF"
	postedBySystemFallback    = "system"
	closedPeriodMessagePart   = "closed fiscal period"
)

var (
	// ErrNoTransaction is PostEntry's refusal to write outside a transaction:
	// a posting always rides the act that caused it (ADR 0005 section 8.2).
	ErrNoTransaction = errors.New("gl: a journal entry is posted inside the act's transaction")
	// ErrUnbalanced is a posting whose debits and credits differ.
	ErrUnbalanced = errors.New("gl: the entry does not balance")
	// ErrPeriodClosed is the 077 trigger's refusal, mapped: the act answers 409
	// with the blocker period_closed.
	ErrPeriodClosed = errors.New("gl: the entry is dated into a closed fiscal period")
	// ErrAccountMissing names a code the chart of accounts does not hold, so
	// the act can answer a 409 telling the operator what to mend.
	ErrAccountMissing = errors.New("gl: a required account is missing from the chart of accounts")
	// ErrNoBranch is PostEntry's refusal of a posting that names no branch:
	// every entry belongs to a branch (migration 125), and a writer that has
	// none fails here rather than posting silently on the default.
	ErrNoBranch = errors.New("gl: a journal entry names no branch")
)

// Leg is one leg of a posting, by stable account code. A leg with a zero
// amount is left out of the entry. A leg may name its own BranchID when the
// line belongs to another branch than the entry (an inter-branch leg); nil
// takes the entry's branch.
type Leg struct {
	AccountCode string
	Description string
	Debit       int64
	Credit      int64
	BranchID    *uuid.UUID
}

// PostingInput is one balanced journal entry to post (ADR 0005 section 8.2).
type PostingInput struct {
	EntryDate       time.Time // the document's business date in the branch's time zone
	Memo            string
	Source          string
	SourceRefID     *uuid.UUID
	ReversesEntryID *uuid.UUID
	Currency        string
	PostedBy        string
	BranchID        uuid.UUID // the branch the entry belongs to (required)
	Legs            []Leg
}

// PostEntry writes one balanced, POSTED journal entry through the caller's
// transaction and answers it; legs with a zero amount are left out, and an
// entry whose legs are all zero is not written (nil, nil). It refuses a call
// with no transaction open (ErrNoTransaction), an unbalanced entry
// (ErrUnbalanced) and an entry dated into a closed period (ErrPeriodClosed).
func (s *Service) PostEntry(ctx context.Context, in PostingInput) (*JournalEntry, error) {
	if !database.InTx(ctx) {
		return nil, ErrNoTransaction
	}
	if in.BranchID == uuid.Nil {
		return nil, ErrNoBranch
	}
	var debit, credit int64
	legs := make([]Leg, 0, len(in.Legs))
	codes := make([]string, 0, len(in.Legs))
	for _, l := range in.Legs {
		if l.Debit < 0 || l.Credit < 0 {
			return nil, fmt.Errorf("%w: a leg to %s carries a negative amount", ErrUnbalanced, l.AccountCode)
		}
		if l.Debit == 0 && l.Credit == 0 {
			continue
		}
		debit += l.Debit
		credit += l.Credit
		legs = append(legs, l)
		codes = append(codes, l.AccountCode)
	}
	if debit != credit {
		return nil, fmt.Errorf("%w: debits %d, credits %d", ErrUnbalanced, debit, credit)
	}
	if len(legs) == 0 {
		return nil, nil
	}
	ids, err := s.resolveAccountIDs(ctx, codes...)
	if err != nil {
		return nil, err
	}
	postedBy := in.PostedBy
	if postedBy == "" {
		postedBy = postedBySystemFallback
	}
	source := in.Source
	if source == "" {
		source = SourceManual
	}
	entry := &JournalEntry{
		EntryDate: in.EntryDate, Memo: in.Memo, Source: source, SourceRefID: in.SourceRefID,
		ReversesEntryID: in.ReversesEntryID, Currency: in.Currency,
		Status: StatusPosted, PostedBy: postedBy, BranchID: in.BranchID,
		TotalDebit: debit, TotalCredit: credit,
	}
	for _, l := range legs {
		line := JournalLine{AccountID: ids[l.AccountCode], Description: l.Description, Debit: l.Debit, Credit: l.Credit, BranchID: in.BranchID}
		if l.BranchID != nil {
			line.BranchID = *l.BranchID
		}
		entry.Lines = append(entry.Lines, line)
	}
	if err := s.repo.CreateJournalEntry(ctx, entry); err != nil {
		if strings.Contains(err.Error(), closedPeriodMessagePart) {
			return nil, fmt.Errorf("%w: %v", ErrPeriodClosed, err)
		}
		// A reversal whose entry was reversed by a concurrent act while this
		// one ran: the insert meets the one reversal index (a pre-check
		// cannot see an uncommitted contender), and the loss maps to
		// ErrAlreadyReversed so the caller answers the same refusal as the
		// belt, never a 500 (review r4 P3-1). The map is keyed on the index's
		// own name, not on the SQLSTATE alone (review r5 P3-3): today
		// ux_gl_entry_single_reversal is the only unique index an entry
		// insert can meet, and a later one must pass through unmapped rather
		// than read as a lost reversal race.
		if in.ReversesEntryID != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "ux_gl_entry_single_reversal" {
				return nil, fmt.Errorf("%w: another reversal of the entry committed first", ErrAlreadyReversed)
			}
		}
		return nil, err
	}
	return entry, nil
}

// ReversalInput names the entry to reverse and the business date of the
// reversal (ADR 0005 section 8.2: a void reverses the document's whole entry,
// dated the void date). The reversal posts in the entry's own currency (the
// entry priced its credit into that currency when it posted, so undoing it
// in another would leave the control account holding both halves in
// different currencies, review r4 P3-3); the input's currency is only the
// fallback for an entry that carries none.
type ReversalInput struct {
	EntryID   uuid.UUID
	EntryDate time.Time
	Currency  string
	Reason    string
	PostedBy  string
}

// ErrAlreadyReversed is the refusal to reverse an entry a reversal already
// points at (the one-reversal-per-entry index would refuse it too).
var ErrAlreadyReversed = errors.New("gl: the entry is already reversed")

// ErrNotPosted is the refusal to reverse an entry that is no longer POSTED
// (a concurrent act voided it while this one ran between its own two reads
// of the entry): the callers that race a GL act map it beside
// ErrAlreadyReversed, so the loss answers the same 409, never a 500 (review
// r6 P3-1).
var ErrNotPosted = errors.New("gl: only a POSTED entry can be reversed")

// PostReversal books the net zero reversal of a POSTED entry through the
// caller's transaction: the same accounts, debits and credits swapped, source
// REVERSAL, reverses_entry_id set, dated the given business date (period
// checked like any posting). It refuses a call with no transaction open, an
// entry that is not POSTED, and an entry already reversed.
func (s *Service) PostReversal(ctx context.Context, in ReversalInput) (*JournalEntry, error) {
	if !database.InTx(ctx) {
		return nil, ErrNoTransaction
	}
	orig, err := s.repo.GetJournalEntry(ctx, in.EntryID)
	if err != nil {
		return nil, err
	}
	if orig.Status != StatusPosted {
		return nil, fmt.Errorf("%w (current: %s)", ErrNotPosted, orig.Status)
	}
	if reversed, err := s.repo.IsReversed(ctx, in.EntryID); err != nil {
		return nil, err
	} else if reversed {
		return nil, ErrAlreadyReversed
	}
	memo := fmt.Sprintf("Reversal of #%d", orig.EntryNumber)
	if in.Reason != "" {
		memo += ": " + in.Reason
	}
	legs := make([]Leg, 0, len(orig.Lines))
	for _, l := range orig.Lines {
		leg := Leg{AccountCode: l.AccountCode, Description: "Reversal: " + l.Description,
			Debit: l.Credit, Credit: l.Debit}
		if l.BranchID != uuid.Nil && l.BranchID != orig.BranchID {
			branch := l.BranchID
			leg.BranchID = &branch
		}
		legs = append(legs, leg)
	}
	origID := orig.ID
	currency := orig.Currency
	if currency == "" {
		currency = in.Currency
	}
	return s.PostEntry(ctx, PostingInput{
		EntryDate: in.EntryDate, Memo: memo, Source: SourceReversal, SourceRefID: &origID,
		ReversesEntryID: &origID, Currency: currency, PostedBy: in.PostedBy, BranchID: orig.BranchID, Legs: legs,
	})
}
