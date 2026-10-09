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
)

// Account codes the sales postings name (ADR 0005 section 8.1), beside the
// codes above.
const (
	AccountCodeInventory    = "1030"
	AccountCodeSalesTax     = "2020"
	AccountCodeCOGS         = "5010"
	AccountCodeDeliveryRev  = "4020"
	SourceCreditMemo        = "CREDIT_MEMO"
	SourceWriteOff          = "WRITE_OFF"
	postedBySystemFallback  = "system"
	closedPeriodMessagePart = "closed fiscal period"
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
)

// Leg is one leg of a posting, by stable account code. A leg with a zero
// amount is left out of the entry.
type Leg struct {
	AccountCode string
	Description string
	Debit       int64
	Credit      int64
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
		Status: StatusPosted, PostedBy: postedBy,
		TotalDebit: debit, TotalCredit: credit,
	}
	for _, l := range legs {
		entry.Lines = append(entry.Lines, JournalLine{AccountID: ids[l.AccountCode], Description: l.Description, Debit: l.Debit, Credit: l.Credit})
	}
	if err := s.repo.CreateJournalEntry(ctx, entry); err != nil {
		if strings.Contains(err.Error(), closedPeriodMessagePart) {
			return nil, fmt.Errorf("%w: %v", ErrPeriodClosed, err)
		}
		return nil, err
	}
	return entry, nil
}

// ReversalInput names the entry to reverse and the business date and currency
// of the reversal (ADR 0005 section 8.2: a void reverses the document's whole
// entry, dated the void date, in the document's currency).
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
		return nil, fmt.Errorf("gl: only a POSTED entry can be reversed (current: %s)", orig.Status)
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
		legs = append(legs, Leg{AccountCode: l.AccountCode, Description: "Reversal: " + l.Description,
			Debit: l.Credit, Credit: l.Debit})
	}
	origID := orig.ID
	return s.PostEntry(ctx, PostingInput{
		EntryDate: in.EntryDate, Memo: memo, Source: SourceReversal, SourceRefID: &origID,
		ReversesEntryID: &origID, Currency: in.Currency, PostedBy: in.PostedBy, Legs: legs,
	})
}
