// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/google/uuid"
)

// An in-memory gl.Repository so the invoice module's ledger postings can be
// exercised for real (through gl.Service) without Postgres.

const (
	cashCode    = gl.AccountCodeCash
	arCode      = gl.AccountCodeAR
	revenueCode = gl.AccountCodeRevenue
)

type fakeGLRepo struct {
	accounts []gl.GLAccount
	entries  []gl.JournalEntry
	periods  []gl.FiscalPeriod
}

// newFakeGLRepo seeds the chart of accounts the auto-posting paths resolve by
// code (see gl.AccountCode* — rows seeded by migration 025).
func newFakeGLRepo() *fakeGLRepo {
	return &fakeGLRepo{
		accounts: []gl.GLAccount{
			{ID: uuid.New(), Code: cashCode, Name: "Cash", Type: gl.AccountTypeAsset, NormalBalance: gl.NormalDebit},
			{ID: uuid.New(), Code: arCode, Name: "Accounts Receivable", Type: gl.AccountTypeAsset, NormalBalance: gl.NormalDebit},
			{ID: uuid.New(), Code: revenueCode, Name: "Sales Revenue", Type: gl.AccountTypeRevenue, NormalBalance: gl.NormalCredit},
		},
	}
}

func (f *fakeGLRepo) idFor(code string) uuid.UUID {
	for _, a := range f.accounts {
		if a.Code == code {
			return a.ID
		}
	}
	return uuid.Nil
}

func (f *fakeGLRepo) ListAccounts(context.Context) ([]gl.GLAccount, error) { return f.accounts, nil }

func (f *fakeGLRepo) GetAccount(_ context.Context, id uuid.UUID) (*gl.GLAccount, error) {
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			return &f.accounts[i], nil
		}
	}
	return nil, nil
}

func (f *fakeGLRepo) CreateAccount(_ context.Context, acct *gl.GLAccount) error {
	if acct.ID == uuid.Nil {
		acct.ID = uuid.New()
	}
	f.accounts = append(f.accounts, *acct)
	return nil
}

func (f *fakeGLRepo) UpdateAccount(context.Context, *gl.GLAccount) error { return nil }

func (f *fakeGLRepo) CreateJournalEntry(_ context.Context, entry *gl.JournalEntry) error {
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	entry.EntryNumber = len(f.entries) + 1
	f.entries = append(f.entries, *entry)
	return nil
}

func (f *fakeGLRepo) GetJournalEntry(_ context.Context, id uuid.UUID) (*gl.JournalEntry, error) {
	for i := range f.entries {
		if f.entries[i].ID == id {
			return &f.entries[i], nil
		}
	}
	return nil, nil
}

func (f *fakeGLRepo) ListJournalEntries(context.Context) ([]gl.JournalEntry, error) {
	return f.entries, nil
}

func (f *fakeGLRepo) UpdateJournalEntryStatus(_ context.Context, id uuid.UUID, status, postedBy string) error {
	for i := range f.entries {
		if f.entries[i].ID == id {
			f.entries[i].Status = status
			f.entries[i].PostedBy = postedBy
		}
	}
	return nil
}

func (f *fakeGLRepo) GetTrialBalance(context.Context, time.Time) ([]gl.TrialBalanceRow, error) {
	return nil, nil
}

// Invoice never reads financial statements; this satisfies gl.Repository only.
func (f *fakeGLRepo) GetAccountActivity(context.Context, *time.Time, time.Time, []string) ([]gl.AccountActivity, error) {
	return nil, nil
}

func (f *fakeGLRepo) ListFiscalPeriods(context.Context) ([]gl.FiscalPeriod, error) {
	return f.periods, nil
}

func (f *fakeGLRepo) GetFiscalPeriodForDate(context.Context, time.Time) (*gl.FiscalPeriod, error) {
	return nil, nil
}

func (f *fakeGLRepo) CloseFiscalPeriod(context.Context, uuid.UUID, string) error  { return nil }
func (f *fakeGLRepo) ReopenFiscalPeriod(context.Context, uuid.UUID, string) error { return nil }
func (f *fakeGLRepo) IsReversed(context.Context, uuid.UUID) (bool, error)         { return false, nil }

func newGLService(repo gl.Repository) *gl.Service {
	return gl.NewService(repo, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// assertBalanced enforces the double-entry invariant: total debits must equal
// total credits, and the entry must actually move money.
func assertBalanced(t *testing.T, entry gl.JournalEntry) {
	t.Helper()
	var debits, credits int64
	for _, l := range entry.Lines {
		debits += l.Debit
		credits += l.Credit
		if l.Debit != 0 && l.Credit != 0 {
			t.Errorf("line %q carries both a debit (%d) and a credit (%d)", l.Description, l.Debit, l.Credit)
		}
		if l.Debit < 0 || l.Credit < 0 {
			t.Errorf("line %q has a negative amount (debit=%d credit=%d)", l.Description, l.Debit, l.Credit)
		}
		if l.AccountID == uuid.Nil {
			t.Errorf("line %q has no account — a nil account_id would be rejected by the FK", l.Description)
		}
	}
	if debits != credits {
		t.Errorf("journal entry is unbalanced: debits %d != credits %d", debits, credits)
	}
	if debits == 0 {
		t.Error("journal entry moves no money")
	}
}

// assertLeg checks the entry debits drCode and credits crCode by amount.
func assertLeg(t *testing.T, repo *fakeGLRepo, entry gl.JournalEntry, drCode, crCode string, amount int64) {
	t.Helper()
	drID, crID := repo.idFor(drCode), repo.idFor(crCode)

	var gotDebit, gotCredit int64
	for _, l := range entry.Lines {
		switch l.AccountID {
		case drID:
			gotDebit += l.Debit
			if l.Credit != 0 {
				t.Errorf("account %s should be debited but carries a credit of %d", drCode, l.Credit)
			}
		case crID:
			gotCredit += l.Credit
			if l.Debit != 0 {
				t.Errorf("account %s should be credited but carries a debit of %d", crCode, l.Debit)
			}
		default:
			t.Errorf("unexpected account %s in entry", l.AccountID)
		}
	}
	if gotDebit != amount {
		t.Errorf("DR %s = %d, want %d", drCode, gotDebit, amount)
	}
	if gotCredit != amount {
		t.Errorf("CR %s = %d, want %d", crCode, gotCredit, amount)
	}
}
