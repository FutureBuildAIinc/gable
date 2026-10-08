// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package bankrecon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Bank reconciliation is a ledger-balancing surface: a bank statement line is
// "cleared" only when it is provably the same money as a posted journal entry.
// The assertions below are CORRECTNESS tests (what must be true of a
// reconciliation) unless a comment says CHARACTERIZATION, which pins current —
// possibly wrong — behaviour so a change to it is visible in review.
//
// Nothing here needs Postgres: the repository is a fake, and the GL dependency
// is a real *gl.Service wrapped around a fake gl.Repository.

// --- fakes ---------------------------------------------------------------

type fakeRepo struct {
	accounts []BankAccount
	txns     []BankTransaction
	sessions map[uuid.UUID]*ReconciliationSession

	createTxnErr    error
	listTxnErr      error
	updateTxnErr    error
	createAcctErr   error
	getTxnErr       error
	createSessErr   error
	getSessErr      error
	updateSessErr   error
	updated         []BankTransaction // every txn handed to UpdateBankTransaction
	sessionsUpdated []ReconciliationSession
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{sessions: map[uuid.UUID]*ReconciliationSession{}}
}

func (f *fakeRepo) CreateBankAccount(_ context.Context, acct *BankAccount) error {
	if f.createAcctErr != nil {
		return f.createAcctErr
	}
	if acct.ID == uuid.Nil {
		acct.ID = uuid.New()
	}
	f.accounts = append(f.accounts, *acct)
	return nil
}

func (f *fakeRepo) GetBankAccount(_ context.Context, id uuid.UUID) (*BankAccount, error) {
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			return &f.accounts[i], nil
		}
	}
	return nil, errors.New("bank account not found")
}

func (f *fakeRepo) ListBankAccounts(context.Context) ([]BankAccount, error) {
	return f.accounts, nil
}

func (f *fakeRepo) CreateBankTransaction(_ context.Context, txn *BankTransaction) error {
	if f.createTxnErr != nil {
		return f.createTxnErr
	}
	if txn.ID == uuid.Nil {
		txn.ID = uuid.New()
	}
	f.txns = append(f.txns, *txn)
	return nil
}

func (f *fakeRepo) GetBankTransaction(_ context.Context, id uuid.UUID) (*BankTransaction, error) {
	if f.getTxnErr != nil {
		return nil, f.getTxnErr
	}
	for i := range f.txns {
		if f.txns[i].ID == id {
			cp := f.txns[i]
			return &cp, nil
		}
	}
	return nil, errors.New("bank transaction not found")
}

func (f *fakeRepo) ListTransactions(context.Context, uuid.UUID, *uuid.UUID) ([]BankTransaction, error) {
	if f.listTxnErr != nil {
		return nil, f.listTxnErr
	}
	out := make([]BankTransaction, len(f.txns))
	copy(out, f.txns)
	return out, nil
}

func (f *fakeRepo) UpdateBankTransaction(_ context.Context, txn *BankTransaction) error {
	if f.updateTxnErr != nil {
		return f.updateTxnErr
	}
	f.updated = append(f.updated, *txn)
	for i := range f.txns {
		if f.txns[i].ID == txn.ID {
			f.txns[i] = *txn
		}
	}
	return nil
}

func (f *fakeRepo) CreateSession(_ context.Context, s *ReconciliationSession) error {
	if f.createSessErr != nil {
		return f.createSessErr
	}
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	cp := *s
	f.sessions[s.ID] = &cp
	return nil
}

func (f *fakeRepo) GetSession(_ context.Context, id uuid.UUID) (*ReconciliationSession, error) {
	if f.getSessErr != nil {
		return nil, f.getSessErr
	}
	s, ok := f.sessions[id]
	if !ok {
		return nil, errors.New("session not found")
	}
	cp := *s
	return &cp, nil
}

func (f *fakeRepo) UpdateSession(_ context.Context, s *ReconciliationSession) error {
	if f.updateSessErr != nil {
		return f.updateSessErr
	}
	f.sessionsUpdated = append(f.sessionsUpdated, *s)
	cp := *s
	f.sessions[s.ID] = &cp
	return nil
}

func (f *fakeRepo) ListSessions(context.Context, *uuid.UUID) ([]ReconciliationSession, error) {
	out := make([]ReconciliationSession, 0, len(f.sessions))
	for _, s := range f.sessions {
		out = append(out, *s)
	}
	return out, nil
}

// fakeGLRepo satisfies gl.Repository by embedding the interface: only the one
// method bankrecon actually calls is implemented. Any other call is a nil
// dereference, which is the loud failure we want if the dependency grows.
type fakeGLRepo struct {
	gl.Repository
	entries []gl.JournalEntry
	err     error
}

func (f *fakeGLRepo) ListJournalEntries(context.Context) ([]gl.JournalEntry, error) {
	return f.entries, f.err
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newSvc(repo Repository, entries []gl.JournalEntry) *Service {
	glSvc := gl.NewService(&fakeGLRepo{entries: entries}, nil, testLogger())
	return NewService(nil, repo, glSvc, testLogger())
}

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func postedEntry(date time.Time, debitCents int64) gl.JournalEntry {
	return gl.JournalEntry{
		ID:          uuid.New(),
		EntryDate:   date,
		Status:      gl.StatusPosted,
		TotalDebit:  debitCents,
		TotalCredit: debitCents,
	}
}

// --- parseDate -----------------------------------------------------------

// CORRECTNESS: a bank export whose date column the parser rejects becomes a
// skipped row, i.e. money silently missing from the reconciliation. Every
// format the parser claims to accept must round-trip to the same calendar day.
func TestParseDate(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string // YYYY-MM-DD, empty means "must fail"
		wantErr bool
	}{
		{"ISO", "2026-03-04", "2026-03-04", false},
		{"US padded", "03/04/2026", "2026-03-04", false},
		{"US unpadded", "3/4/2026", "2026-03-04", false},
		{"US dashed", "03-04-2026", "2026-03-04", false},
		{"long form", "Mar 4, 2026", "2026-03-04", false},
		{"surrounding whitespace is tolerated", "  2026-03-04  ", "2026-03-04", false},
		{"empty", "", "", true},
		{"garbage", "not-a-date", "", true},
		// A day-first European export is NOT supported. 13 cannot be a month,
		// so it fails loudly rather than silently transposing to a wrong date.
		{"day-first is rejected, not transposed", "13/04/2026", "", true},
		// CHARACTERIZATION: an ambiguous day-first date that is also a valid
		// month-first date parses as month-first. 04/03/2026 means 3 April in
		// most of the world and is read here as 3 March.
		{"ambiguous date silently reads as US month-first", "04/03/2026", "2026-04-03", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDate(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseDate(%q) = %v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDate(%q): %v", tc.in, err)
			}
			if got.Format("2006-01-02") != tc.want {
				t.Errorf("parseDate(%q) = %s, want %s", tc.in, got.Format("2006-01-02"), tc.want)
			}
		})
	}
}

// --- parseCSVLine --------------------------------------------------------

func TestParseCSVLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", "2026-03-04,100.00,Deposit,REF1", []string{"2026-03-04", "100.00", "Deposit", "REF1"}},
		{"quoted field hides its comma", `2026-03-04,"1,234.56",Deposit,REF1`, []string{"2026-03-04", "1,234.56", "Deposit", "REF1"}},
		{"empty trailing field is preserved", "a,b,", []string{"a", "b", ""}},
		{"single field", "solo", []string{"solo"}},
		{"empty line yields one empty field", "", []string{""}},
		// CHARACTERIZATION: RFC 4180 escapes a literal quote by doubling it.
		// This hand-rolled splitter treats "" as open-then-close, so the quote
		// character is dropped rather than emitted. A description like
		// 6" PIPE round-trips as 6 PIPE.
		{"doubled quotes are dropped, not unescaped", `a,"say ""hi""",c`, []string{"a", "say hi", "c"}},
		// CHARACTERIZATION: an unterminated quote swallows every later comma.
		{"unbalanced quote swallows the rest of the line", `a,"b,c`, []string{"a", "b,c"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCSVLine(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseCSVLine(%q) = %#v (%d fields), want %#v (%d fields)", tc.in, got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("field %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// --- ImportCSV: dollars -> cents ----------------------------------------

// CORRECTNESS: the statement column is dollars and the stored column is cents.
// Every conversion must land on the exact cent, in both directions of sign —
// binary floating point makes 8.20*100 == 819.9999999999999, so a truncating
// conversion would lose a cent on ordinary amounts.
func TestImportCSV_AmountConversionIsExactInCents(t *testing.T) {
	tests := []struct {
		name   string
		amount string
		want   int64
	}{
		{"whole dollars", "100", 10000},
		{"two decimals", "12.34", 1234},
		{"float-repr trap 8.20", "8.20", 820},
		{"float-repr trap 0.29", "0.29", 29},
		{"negative withdrawal", "-12.34", -1234},
		{"negative float-repr trap", "-8.20", -820},
		{"currency symbol stripped", "$45.67", 4567},
		// A thousands separator only survives CSV splitting if the exporter
		// quoted the field; the quotes are stripped before the comma is.
		{"quoted thousands separator stripped", `"1,234.56"`, 123456},
		{"symbol and separator together", `"$1,234.56"`, 123456},
		{"zero", "0.00", 0},
		{"single cent", "0.01", 1},
		{"negative single cent", "-0.01", -1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			svc := newSvc(repo, nil)

			csv := "Date,Amount,Description,Reference\n2026-03-04," + tc.amount + ",Line,REF"
			res, err := svc.ImportCSV(context.Background(), ImportCSVRequest{
				BankAccountID: uuid.New(),
				CSVContent:    csv,
			})
			if err != nil {
				t.Fatalf("ImportCSV: %v", err)
			}
			if res.ImportedRows != 1 {
				t.Fatalf("ImportedRows = %d, want 1 (skipped=%d)", res.ImportedRows, res.SkippedRows)
			}
			if got := repo.txns[0].Amount; got != tc.want {
				t.Errorf("stored amount = %d cents, want %d cents (from %q)", got, tc.want, tc.amount)
			}
		})
	}
}

// CORRECTNESS: the import must account for every line it was handed —
// imported + skipped == total — so an operator can trust the summary rather
// than reconciling the reconciliation.
func TestImportCSV_RowAccounting(t *testing.T) {
	repo := newFakeRepo()
	svc := newSvc(repo, nil)

	csv := "Date,Amount,Description,Reference\n" +
		"2026-03-04,100.00,Good deposit,R1\n" +
		"\n" + // blank line: not a row at all
		"2026-03-05,not-money,Bad amount,R2\n" +
		"nope,50.00,Bad date,R3\n" +
		"2026-03-06,25.00,No reference column\n" + // exactly 3 fields: still valid
		"2026-03-07,10.00\n" // fewer than 3 fields: too short

	res, err := svc.ImportCSV(context.Background(), ImportCSVRequest{
		BankAccountID: uuid.New(),
		CSVContent:    csv,
	})
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}

	if res.TotalRows != 5 {
		t.Errorf("TotalRows = %d, want 5 (blank lines are not rows)", res.TotalRows)
	}
	if res.ImportedRows != 2 {
		t.Errorf("ImportedRows = %d, want 2", res.ImportedRows)
	}
	if res.SkippedRows != 3 {
		t.Errorf("SkippedRows = %d, want 3", res.SkippedRows)
	}
	if res.ImportedRows+res.SkippedRows != res.TotalRows {
		t.Errorf("imported(%d) + skipped(%d) != total(%d): rows went missing",
			res.ImportedRows, res.SkippedRows, res.TotalRows)
	}

	// The 3-field row must still import, with an empty reference.
	var threeField *BankTransaction
	for i := range repo.txns {
		if repo.txns[i].Amount == 2500 {
			threeField = &repo.txns[i]
		}
	}
	if threeField == nil {
		t.Fatal("the 3-field row was not imported")
	}
	if threeField.Reference != "" {
		t.Errorf("Reference = %q, want empty for a row with no reference column", threeField.Reference)
	}
	if threeField.Description != "No reference column" {
		t.Errorf("Description = %q, want %q", threeField.Description, "No reference column")
	}
}

// CORRECTNESS: an imported statement line starts life UNMATCHED and carries
// the account/session it was imported under. If it did not, it would never
// appear in the session's outstanding total.
func TestImportCSV_StampsAccountSessionAndUnmatchedStatus(t *testing.T) {
	repo := newFakeRepo()
	svc := newSvc(repo, nil)

	acctID := uuid.New()
	reconID := uuid.New()

	_, err := svc.ImportCSV(context.Background(), ImportCSVRequest{
		BankAccountID:    acctID,
		ReconciliationID: &reconID,
		CSVContent:       "Date,Amount,Description,Reference\n2026-03-04,100.00,\"Wire in\",REF-9",
	})
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}

	txn := repo.txns[0]
	if txn.BankAccountID != acctID {
		t.Errorf("BankAccountID = %s, want %s", txn.BankAccountID, acctID)
	}
	if txn.ReconciliationID == nil || *txn.ReconciliationID != reconID {
		t.Errorf("ReconciliationID = %v, want %s", txn.ReconciliationID, reconID)
	}
	if txn.Status != TransactionStatusUnmatched {
		t.Errorf("Status = %q, want %q", txn.Status, TransactionStatusUnmatched)
	}
	if txn.Description != "Wire in" {
		t.Errorf("Description = %q, want %q (quotes stripped)", txn.Description, "Wire in")
	}
	if txn.TransactionDate.Format("2006-01-02") != "2026-03-04" {
		t.Errorf("TransactionDate = %s, want 2026-03-04", txn.TransactionDate)
	}
}

// CHARACTERIZATION: the first line is discarded unconditionally. A headerless
// export therefore loses its first transaction with no error and no skip
// count. Documented here because it is a silent money-losing input handling
// rule, not because it is right.
func TestImportCSV_FirstLineIsAlwaysDiscarded(t *testing.T) {
	repo := newFakeRepo()
	svc := newSvc(repo, nil)

	res, err := svc.ImportCSV(context.Background(), ImportCSVRequest{
		BankAccountID: uuid.New(),
		CSVContent:    "2026-03-04,100.00,First real row,R1\n2026-03-05,200.00,Second row,R2",
	})
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if res.TotalRows != 1 {
		t.Fatalf("TotalRows = %d, want 1: the first data row is eaten as a header", res.TotalRows)
	}
	if len(repo.txns) != 1 || repo.txns[0].Amount != 20000 {
		t.Fatalf("stored %d txns (%v), want only the second row", len(repo.txns), repo.txns)
	}
}

// CORRECTNESS: a row the database refuses must be reported as skipped, never
// counted as imported.
func TestImportCSV_RepositoryFailureCountsAsSkipped(t *testing.T) {
	repo := newFakeRepo()
	repo.createTxnErr = errors.New("insert blew up")
	svc := newSvc(repo, nil)

	res, err := svc.ImportCSV(context.Background(), ImportCSVRequest{
		BankAccountID: uuid.New(),
		CSVContent:    "Date,Amount,Desc,Ref\n2026-03-04,100.00,X,R1\n2026-03-05,200.00,Y,R2",
	})
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if res.ImportedRows != 0 {
		t.Errorf("ImportedRows = %d, want 0", res.ImportedRows)
	}
	if res.SkippedRows != 2 {
		t.Errorf("SkippedRows = %d, want 2", res.SkippedRows)
	}
}

// CORRECTNESS: the GL being unreachable must not turn an import into a
// success-shaped lie about matching. Rows still import; AutoMatched stays 0.
func TestImportCSV_GLFailureLeavesRowsImportedAndUnmatched(t *testing.T) {
	repo := newFakeRepo()
	glSvc := gl.NewService(&fakeGLRepo{err: errors.New("gl down")}, nil, testLogger())
	svc := NewService(nil, repo, glSvc, testLogger())

	res, err := svc.ImportCSV(context.Background(), ImportCSVRequest{
		BankAccountID: uuid.New(),
		CSVContent:    "Date,Amount,Desc,Ref\n2026-03-04,100.00,X,R1",
	})
	if err != nil {
		t.Fatalf("ImportCSV should not fail when the GL is unavailable: %v", err)
	}
	if res.ImportedRows != 1 {
		t.Errorf("ImportedRows = %d, want 1", res.ImportedRows)
	}
	if res.AutoMatched != 0 {
		t.Errorf("AutoMatched = %d, want 0 when the GL could not be read", res.AutoMatched)
	}
	if repo.txns[0].Status != TransactionStatusUnmatched {
		t.Errorf("Status = %q, want UNMATCHED", repo.txns[0].Status)
	}
}

// --- auto-matching -------------------------------------------------------

// CORRECTNESS: only POSTED journal entries are real money. A draft entry must
// never clear a bank line.
func TestFindBestMatch_IgnoresUnpostedEntries(t *testing.T) {
	svc := newSvc(newFakeRepo(), nil)

	draft := gl.JournalEntry{
		ID:         uuid.New(),
		EntryDate:  day("2026-03-04"),
		Status:     gl.StatusDraft,
		TotalDebit: 10000,
	}
	txn := BankTransaction{TransactionDate: day("2026-03-04"), Amount: 10000}

	if got := svc.findBestMatch(txn, []gl.JournalEntry{draft}); got != nil {
		t.Fatalf("matched a DRAFT journal entry %s — unposted entries are not money", got.ID)
	}
}

// CORRECTNESS: an amount that differs by a single cent is not the same
// payment. Reconciliation must be exact, never "close enough".
func TestFindBestMatch_AmountMustBeExact(t *testing.T) {
	svc := newSvc(newFakeRepo(), nil)
	entry := postedEntry(day("2026-03-04"), 10000)
	txn := BankTransaction{TransactionDate: day("2026-03-04"), Amount: 10001}

	if got := svc.findBestMatch(txn, []gl.JournalEntry{entry}); got != nil {
		t.Fatalf("a $100.01 bank line matched a $100.00 entry (%s)", got.ID)
	}
}

// CHARACTERIZATION: the date window is documented as "±1 day" but implemented
// as a 1.5-day span on absolute hours. Pinned so that tightening or widening
// it is a visible decision.
func TestFindBestMatch_DateWindow(t *testing.T) {
	svc := newSvc(newFakeRepo(), nil)
	txnDate := day("2026-03-04")

	tests := []struct {
		name      string
		entryDate time.Time
		wantMatch bool
	}{
		{"same day", txnDate, true},
		{"one day earlier", txnDate.AddDate(0, 0, -1), true},
		{"one day later", txnDate.AddDate(0, 0, 1), true},
		{"36 hours later is still inside the 1.5-day span", txnDate.Add(36 * time.Hour), true},
		{"37 hours later is outside", txnDate.Add(37 * time.Hour), false},
		{"two days later", txnDate.AddDate(0, 0, 2), false},
		{"two days earlier", txnDate.AddDate(0, 0, -2), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := postedEntry(tc.entryDate, 10000)
			txn := BankTransaction{TransactionDate: txnDate, Amount: 10000}
			got := svc.findBestMatch(txn, []gl.JournalEntry{entry})
			if (got != nil) != tc.wantMatch {
				t.Fatalf("match=%v, want %v (entry %s vs txn %s)",
					got != nil, tc.wantMatch, tc.entryDate.Format(time.RFC3339), txnDate.Format(time.RFC3339))
			}
		})
	}
}

// REGRESSION (this was a documented, pinned defect). A withdrawal and a deposit of equal magnitude
// are opposite directions of money, but findBestMatch used to compare absolute
// values only, so a -$500 cheque cleared against a +$500 receipt entry. That
// silently reconciled a $1,000 real discrepancy to zero.
//
// findBestMatch now compares signed amounts: the entry's debit total is cash
// in, so it can only clear a deposit of the same size. A withdrawal is left
// UNMATCHED for manual review — see TestFindBestMatch_WithdrawalsAreNotAutoCleared.
func TestFindBestMatch_MustNotMatchOppositeDirections(t *testing.T) {
	svc := newSvc(newFakeRepo(), nil)
	deposit := postedEntry(day("2026-03-04"), 50000) // money in, per the GL

	withdrawal := BankTransaction{
		TransactionDate: day("2026-03-04"),
		Amount:          -50000, // money out, per the bank
	}

	if got := svc.findBestMatch(withdrawal, []gl.JournalEntry{deposit}); got != nil {
		t.Fatalf("a -$500.00 withdrawal cleared against a +$500.00 deposit entry (%s)", got.ID)
	}
}

// LIMITATION, documented deliberately: a withdrawal never auto-clears.
//
// gl.ListJournalEntries returns entry headers only, and a balanced entry has
// TotalDebit == TotalCredit, so the header cannot say which side of the entry
// the CASH account was on. With no way to prove direction, auto-matching a
// withdrawal would be a guess, and a wrong guess hides a discrepancy of twice
// the amount. Withdrawals are therefore left for ManualMatch.
//
// Lifting this needs a wider GL read — journal lines joined to the bank
// account's gl_account_id, so the cash-side debit/credit is known — which is a
// repository change beyond the scope of the direction fix.
func TestFindBestMatch_WithdrawalsAreNotAutoCleared(t *testing.T) {
	svc := newSvc(newFakeRepo(), nil)
	entry := postedEntry(day("2026-03-04"), 50000)
	withdrawal := BankTransaction{TransactionDate: day("2026-03-04"), Amount: -50000}

	if got := svc.findBestMatch(withdrawal, []gl.JournalEntry{entry}); got != nil {
		t.Fatalf("withdrawal auto-cleared against entry %s; direction cannot be proved from an entry header", got.ID)
	}
}

// REGRESSION (this was a documented, pinned defect). autoMatch re-scanned the full journal-entry
// list for every bank line and never removed an entry once it had been
// consumed, so N identical bank lines all cleared against the SAME journal
// entry. The reconciliation then reported N cleared items backed by one real
// posting. A matched entry is now removed from the candidate set.
func TestAutoMatch_MustNotReuseOneEntryForManyBankLines(t *testing.T) {
	repo := newFakeRepo()
	acctID := uuid.New()
	// Two genuinely separate $250.00 deposits on the same day.
	for i := 0; i < 2; i++ {
		repo.txns = append(repo.txns, BankTransaction{
			ID:              uuid.New(),
			BankAccountID:   acctID,
			TransactionDate: day("2026-03-04"),
			Amount:          25000,
			Status:          TransactionStatusUnmatched,
		})
	}
	// ...but only ONE posting in the GL.
	entries := []gl.JournalEntry{postedEntry(day("2026-03-04"), 25000)}
	svc := newSvc(repo, entries)

	matched, err := svc.autoMatch(context.Background(), acctID, nil)
	if err != nil {
		t.Fatalf("autoMatch: %v", err)
	}
	if matched != 1 {
		t.Fatalf("auto-matched %d bank lines against 1 journal entry, want 1", matched)
	}
}

// CORRECTNESS: auto-match must leave an already-matched line alone, and must
// stamp both the status and the journal-entry link on the lines it does clear.
func TestAutoMatch_SkipsNonUnmatchedAndStampsTheLink(t *testing.T) {
	repo := newFakeRepo()
	acctID := uuid.New()
	entry := postedEntry(day("2026-03-04"), 25000)

	excludedID := uuid.New()
	repo.txns = []BankTransaction{
		{ID: uuid.New(), BankAccountID: acctID, TransactionDate: day("2026-03-04"), Amount: 25000, Status: TransactionStatusUnmatched},
		{ID: excludedID, BankAccountID: acctID, TransactionDate: day("2026-03-04"), Amount: 25000, Status: TransactionStatusExcluded},
	}
	svc := newSvc(repo, []gl.JournalEntry{entry})

	matched, err := svc.autoMatch(context.Background(), acctID, nil)
	if err != nil {
		t.Fatalf("autoMatch: %v", err)
	}
	if matched != 1 {
		t.Fatalf("matched = %d, want 1 (the EXCLUDED line must be left alone)", matched)
	}
	if len(repo.updated) != 1 {
		t.Fatalf("wrote %d updates, want 1", len(repo.updated))
	}
	got := repo.updated[0]
	if got.ID == excludedID {
		t.Fatal("auto-match modified the EXCLUDED transaction")
	}
	if got.Status != TransactionStatusMatched {
		t.Errorf("Status = %q, want MATCHED", got.Status)
	}
	if got.MatchedJournalEntryID == nil || *got.MatchedJournalEntryID != entry.ID {
		t.Errorf("MatchedJournalEntryID = %v, want %s", got.MatchedJournalEntryID, entry.ID)
	}
}

// CORRECTNESS: if the update fails, the line must not be counted as cleared.
func TestAutoMatch_FailedWriteIsNotCounted(t *testing.T) {
	repo := newFakeRepo()
	acctID := uuid.New()
	repo.txns = []BankTransaction{
		{ID: uuid.New(), BankAccountID: acctID, TransactionDate: day("2026-03-04"), Amount: 25000, Status: TransactionStatusUnmatched},
	}
	repo.updateTxnErr = errors.New("write failed")
	svc := newSvc(repo, []gl.JournalEntry{postedEntry(day("2026-03-04"), 25000)})

	matched, err := svc.autoMatch(context.Background(), acctID, nil)
	if err != nil {
		t.Fatalf("autoMatch: %v", err)
	}
	if matched != 0 {
		t.Errorf("matched = %d, want 0 when the write failed", matched)
	}
}

// --- manual match / unmatch ---------------------------------------------

// CORRECTNESS: manual match and unmatch must be exact inverses of each other
// on both fields they touch, or an operator cannot undo a mistake.
func TestManualMatchUnmatch_RoundTrip(t *testing.T) {
	repo := newFakeRepo()
	txnID := uuid.New()
	repo.txns = []BankTransaction{{ID: txnID, Amount: 5000, Status: TransactionStatusUnmatched}}
	svc := newSvc(repo, nil)

	entryID := uuid.New()
	if err := svc.ManualMatch(context.Background(), ManualMatchRequest{BankTransactionID: txnID, JournalEntryID: entryID}); err != nil {
		t.Fatalf("ManualMatch: %v", err)
	}
	if repo.txns[0].Status != TransactionStatusMatched {
		t.Fatalf("Status = %q, want MATCHED", repo.txns[0].Status)
	}
	if repo.txns[0].MatchedJournalEntryID == nil || *repo.txns[0].MatchedJournalEntryID != entryID {
		t.Fatalf("MatchedJournalEntryID = %v, want %s", repo.txns[0].MatchedJournalEntryID, entryID)
	}

	if err := svc.ManualUnmatch(context.Background(), txnID); err != nil {
		t.Fatalf("ManualUnmatch: %v", err)
	}
	if repo.txns[0].Status != TransactionStatusUnmatched {
		t.Errorf("Status = %q, want UNMATCHED after unmatch", repo.txns[0].Status)
	}
	if repo.txns[0].MatchedJournalEntryID != nil {
		t.Errorf("MatchedJournalEntryID = %v, want nil after unmatch: a stale link is a phantom clearing",
			repo.txns[0].MatchedJournalEntryID)
	}
}

// CORRECTNESS: a failing write must surface as an error, not a silent no-op.
func TestManualMatch_PropagatesErrors(t *testing.T) {
	t.Run("transaction not found", func(t *testing.T) {
		repo := newFakeRepo()
		repo.getTxnErr = errors.New("nope")
		svc := newSvc(repo, nil)
		if err := svc.ManualMatch(context.Background(), ManualMatchRequest{BankTransactionID: uuid.New(), JournalEntryID: uuid.New()}); err == nil {
			t.Fatal("want error when the transaction cannot be loaded")
		}
	})

	t.Run("update fails", func(t *testing.T) {
		repo := newFakeRepo()
		id := uuid.New()
		repo.txns = []BankTransaction{{ID: id, Status: TransactionStatusUnmatched}}
		repo.updateTxnErr = errors.New("write failed")
		svc := newSvc(repo, nil)
		if err := svc.ManualMatch(context.Background(), ManualMatchRequest{BankTransactionID: id, JournalEntryID: uuid.New()}); err == nil {
			t.Fatal("want error when the update fails")
		}
	})
}

// --- sessions ------------------------------------------------------------

// CORRECTNESS: the statement balance the operator types in dollars must become
// the exact same amount in cents.
func TestCreateSession_StatementBalanceConversion(t *testing.T) {
	tests := []struct {
		name    string
		dollars float64
		want    int64
	}{
		{"whole dollars", 1000, 100000},
		{"two decimals", 1234.56, 123456},
		{"float-repr trap", 8.20, 820},
		{"zero", 0, 0},
		{"one cent", 0.01, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			svc := newSvc(repo, nil)
			sess, err := svc.CreateSession(context.Background(), CreateSessionRequest{
				BankAccountID:    uuid.New(),
				PeriodStart:      "2026-03-01",
				PeriodEnd:        "2026-03-31",
				StatementBalance: tc.dollars,
			})
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if sess.StatementBalance != tc.want {
				t.Errorf("StatementBalance = %d cents, want %d cents (from %.2f dollars)", sess.StatementBalance, tc.want, tc.dollars)
			}
		})
	}
}

// REGRESSION (this was a documented, pinned defect). An overdrawn account has a negative statement
// balance, and the dollars->cents conversion used to add +0.5 before a
// truncating int64 conversion. Truncation goes toward zero, so for negatives
// the +0.5 rounded the WRONG way and the session opened one cent light.
//
// Both this conversion and ImportCSV's now go through money.DollarsToCents,
// which rounds half away from zero in both directions.
func TestCreateSession_NegativeStatementBalanceLosesACent(t *testing.T) {
	tests := []struct {
		dollars float64
		want    int64
	}{
		{-100.00, -10000},
		{-1234.56, -123456},
		{-0.01, -1},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%.2f", tc.dollars), func(t *testing.T) {
			repo := newFakeRepo()
			svc := newSvc(repo, nil)
			sess, err := svc.CreateSession(context.Background(), CreateSessionRequest{
				BankAccountID:    uuid.New(),
				PeriodStart:      "2026-03-01",
				PeriodEnd:        "2026-03-31",
				StatementBalance: tc.dollars,
			})
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if sess.StatementBalance != tc.want {
				t.Errorf("StatementBalance = %d cents, want %d cents (from %.2f dollars)", sess.StatementBalance, tc.want, tc.dollars)
			}
		})
	}
}

// CORRECTNESS: a period the service cannot parse must be rejected before any
// row is written, not stored as a zero-time window that silently reconciles
// nothing.
func TestCreateSession_RejectsUnparseablePeriods(t *testing.T) {
	tests := []struct {
		name  string
		start string
		end   string
	}{
		{"bad start", "03/01/2026", "2026-03-31"},
		{"bad end", "2026-03-01", "31-03-2026"},
		{"empty start", "", "2026-03-31"},
		{"empty end", "2026-03-01", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			svc := newSvc(repo, nil)
			if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{
				BankAccountID: uuid.New(),
				PeriodStart:   tc.start,
				PeriodEnd:     tc.end,
			}); err == nil {
				t.Fatal("want an error for an unparseable period")
			}
			if len(repo.sessions) != 0 {
				t.Errorf("wrote %d sessions despite the validation failure", len(repo.sessions))
			}
		})
	}
}

// CORRECTNESS: a new session starts IN_PROGRESS with the period it was asked
// for. A session that opened COMPLETED could never be reconciled.
func TestCreateSession_InitialState(t *testing.T) {
	repo := newFakeRepo()
	svc := newSvc(repo, nil)

	sess, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		BankAccountID:    uuid.New(),
		PeriodStart:      "2026-03-01",
		PeriodEnd:        "2026-03-31",
		StatementBalance: 100,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Status != SessionStatusInProgress {
		t.Errorf("Status = %q, want IN_PROGRESS", sess.Status)
	}
	if sess.PeriodStart.Format("2006-01-02") != "2026-03-01" || sess.PeriodEnd.Format("2006-01-02") != "2026-03-31" {
		t.Errorf("period = %s..%s, want 2026-03-01..2026-03-31", sess.PeriodStart, sess.PeriodEnd)
	}
	if sess.CompletedAt != nil || sess.CompletedBy != nil {
		t.Error("a brand-new session must not be marked completed")
	}
}

// --- summary arithmetic --------------------------------------------------

// CORRECTNESS: this is the arithmetic that tells an accountant whether the
// books tie out. Cleared/outstanding are partitioned by status, EXCLUDED lines
// count toward neither, and a fully-reconciled account differs by zero.
func TestRecalculateSummary(t *testing.T) {
	tests := []struct {
		name             string
		statement        int64
		glBalance        int64
		txns             []BankTransaction
		wantClearedCount int
		wantClearedTotal int64
		wantOutCount     int
		wantOutTotal     int64
		wantDifference   int64
	}{
		{
			name:           "empty session differs by statement minus GL",
			statement:      500000,
			glBalance:      500000,
			wantDifference: 0,
		},
		{
			name:      "everything cleared: difference is statement minus GL",
			statement: 500000,
			glBalance: 500000,
			txns: []BankTransaction{
				{Amount: 25000, Status: TransactionStatusMatched},
				{Amount: -10000, Status: TransactionStatusMatched},
			},
			wantClearedCount: 2,
			wantClearedTotal: 15000,
			wantDifference:   0,
		},
		{
			name:      "an outstanding deposit is subtracted from the difference",
			statement: 500000,
			glBalance: 475000,
			txns: []BankTransaction{
				{Amount: 25000, Status: TransactionStatusUnmatched},
			},
			wantOutCount:   1,
			wantOutTotal:   25000,
			wantDifference: 0, // 500000 - 475000 - 25000
		},
		{
			name:      "an outstanding withdrawal adds back",
			statement: 400000,
			glBalance: 450000,
			txns: []BankTransaction{
				{Amount: -50000, Status: TransactionStatusUnmatched},
			},
			wantOutCount:   1,
			wantOutTotal:   -50000,
			wantDifference: 0, // 400000 - 450000 - (-50000)
		},
		{
			name:      "EXCLUDED lines are in neither bucket",
			statement: 100000,
			glBalance: 100000,
			txns: []BankTransaction{
				{Amount: 999999, Status: TransactionStatusExcluded},
				{Amount: 5000, Status: TransactionStatusMatched},
			},
			wantClearedCount: 1,
			wantClearedTotal: 5000,
			wantDifference:   0,
		},
		{
			name:      "a real discrepancy survives to the difference",
			statement: 500000,
			glBalance: 499900,
			txns: []BankTransaction{
				{Amount: 25000, Status: TransactionStatusMatched},
			},
			wantClearedCount: 1,
			wantClearedTotal: 25000,
			wantDifference:   100, // one dollar out of balance
		},
		{
			name:      "one cent out of balance is still reported",
			statement: 1,
			glBalance: 0,
			txns: []BankTransaction{
				{Amount: 100, Status: TransactionStatusMatched},
			},
			wantClearedCount: 1,
			wantClearedTotal: 100,
			wantDifference:   1,
		},
	}

	svc := newSvc(newFakeRepo(), nil)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess := &ReconciliationSession{
				StatementBalance: tc.statement,
				GLBalance:        tc.glBalance,
				Transactions:     tc.txns,
			}
			svc.recalculateSummary(sess)

			if sess.ClearedCount != tc.wantClearedCount {
				t.Errorf("ClearedCount = %d, want %d", sess.ClearedCount, tc.wantClearedCount)
			}
			if sess.ClearedTotal != tc.wantClearedTotal {
				t.Errorf("ClearedTotal = %d, want %d", sess.ClearedTotal, tc.wantClearedTotal)
			}
			if sess.OutstandingCount != tc.wantOutCount {
				t.Errorf("OutstandingCount = %d, want %d", sess.OutstandingCount, tc.wantOutCount)
			}
			if sess.OutstandingTotal != tc.wantOutTotal {
				t.Errorf("OutstandingTotal = %d, want %d", sess.OutstandingTotal, tc.wantOutTotal)
			}
			if sess.Difference != tc.wantDifference {
				t.Errorf("Difference = %d, want %d", sess.Difference, tc.wantDifference)
			}
		})
	}
}

// CORRECTNESS: GetSession must recompute the summary from the transactions it
// just loaded rather than trusting stale denormalized totals on the row.
func TestGetSession_RecomputesSummaryFromLoadedTransactions(t *testing.T) {
	repo := newFakeRepo()
	sessID := uuid.New()
	acctID := uuid.New()
	repo.sessions[sessID] = &ReconciliationSession{
		ID:               sessID,
		BankAccountID:    acctID,
		StatementBalance: 100000,
		GLBalance:        90000,
		// Deliberately wrong stored totals — GetSession must overwrite them.
		ClearedCount:     99,
		ClearedTotal:     99999,
		OutstandingCount: 99,
		OutstandingTotal: 99999,
		Difference:       -12345,
	}
	repo.txns = []BankTransaction{
		{ID: uuid.New(), BankAccountID: acctID, Amount: 10000, Status: TransactionStatusUnmatched},
		{ID: uuid.New(), BankAccountID: acctID, Amount: 4000, Status: TransactionStatusMatched},
	}
	svc := newSvc(repo, nil)

	got, err := svc.GetSession(context.Background(), sessID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(got.Transactions) != 2 {
		t.Fatalf("loaded %d transactions, want 2", len(got.Transactions))
	}
	if got.ClearedCount != 1 || got.ClearedTotal != 4000 {
		t.Errorf("cleared = %d/%d cents, want 1/4000 — stale totals were not recomputed", got.ClearedCount, got.ClearedTotal)
	}
	if got.OutstandingCount != 1 || got.OutstandingTotal != 10000 {
		t.Errorf("outstanding = %d/%d cents, want 1/10000", got.OutstandingCount, got.OutstandingTotal)
	}
	if want := int64(100000 - 90000 - 10000); got.Difference != want {
		t.Errorf("Difference = %d, want %d", got.Difference, want)
	}
}

// CORRECTNESS: completing a reconciliation is an audit event. It must stamp
// who and when, and must be refused a second time so a closed period cannot be
// silently reopened-and-reclosed by a repeated request.
func TestCompleteSession(t *testing.T) {
	userID := uuid.New()
	ctxWithUser := context.WithValue(context.Background(), middleware.UserContextKey,
		&middleware.UserClaims{RegisteredClaims: jwtSubject(userID.String())})

	t.Run("stamps status, time and actor", func(t *testing.T) {
		repo := newFakeRepo()
		sessID := uuid.New()
		repo.sessions[sessID] = &ReconciliationSession{ID: sessID, Status: SessionStatusInProgress}
		svc := newSvc(repo, nil)

		before := time.Now()
		got, err := svc.CompleteSession(ctxWithUser, sessID)
		if err != nil {
			t.Fatalf("CompleteSession: %v", err)
		}
		if got.Status != SessionStatusCompleted {
			t.Errorf("Status = %q, want COMPLETED", got.Status)
		}
		if got.CompletedAt == nil || got.CompletedAt.Before(before) {
			t.Errorf("CompletedAt = %v, want a timestamp at or after %v", got.CompletedAt, before)
		}
		if got.CompletedBy == nil || *got.CompletedBy != userID {
			t.Errorf("CompletedBy = %v, want %s from the auth context", got.CompletedBy, userID)
		}
		if len(repo.sessionsUpdated) != 1 {
			t.Errorf("persisted %d updates, want 1", len(repo.sessionsUpdated))
		}
	})

	t.Run("refuses to complete twice", func(t *testing.T) {
		repo := newFakeRepo()
		sessID := uuid.New()
		repo.sessions[sessID] = &ReconciliationSession{ID: sessID, Status: SessionStatusCompleted}
		svc := newSvc(repo, nil)

		if _, err := svc.CompleteSession(ctxWithUser, sessID); err == nil {
			t.Fatal("want an error when completing an already-completed session")
		}
		if len(repo.sessionsUpdated) != 0 {
			t.Error("an already-completed session was written again")
		}
	})

	t.Run("unauthenticated completion leaves the actor unset rather than guessing", func(t *testing.T) {
		repo := newFakeRepo()
		sessID := uuid.New()
		repo.sessions[sessID] = &ReconciliationSession{ID: sessID, Status: SessionStatusInProgress}
		svc := newSvc(repo, nil)

		got, err := svc.CompleteSession(context.Background(), sessID)
		if err != nil {
			t.Fatalf("CompleteSession: %v", err)
		}
		if got.CompletedBy != nil {
			t.Errorf("CompletedBy = %v, want nil with no claims on the context", got.CompletedBy)
		}
	})

	t.Run("a failed write is reported, not swallowed", func(t *testing.T) {
		repo := newFakeRepo()
		sessID := uuid.New()
		repo.sessions[sessID] = &ReconciliationSession{ID: sessID, Status: SessionStatusInProgress}
		repo.updateSessErr = errors.New("write failed")
		svc := newSvc(repo, nil)

		if _, err := svc.CompleteSession(ctxWithUser, sessID); err == nil {
			t.Fatal("want an error when the session update fails")
		}
	})
}

// --- bank accounts -------------------------------------------------------

// CORRECTNESS: a newly created bank account is active and keeps its GL link;
// without the link, nothing it imports can ever be reconciled to the ledger.
func TestCreateBankAccount(t *testing.T) {
	repo := newFakeRepo()
	svc := newSvc(repo, nil)
	glAcct := uuid.New()

	acct, err := svc.CreateBankAccount(context.Background(), CreateBankAccountRequest{
		Name:          "Operating",
		AccountNumber: "123456789",
		RoutingNumber: "021000021",
		GLAccountID:   glAcct,
	})
	if err != nil {
		t.Fatalf("CreateBankAccount: %v", err)
	}
	if !acct.IsActive {
		t.Error("a new bank account must be active")
	}
	if acct.GLAccountID != glAcct {
		t.Errorf("GLAccountID = %s, want %s", acct.GLAccountID, glAcct)
	}
	if len(repo.accounts) != 1 {
		t.Fatalf("persisted %d accounts, want 1", len(repo.accounts))
	}

	repo2 := newFakeRepo()
	repo2.createAcctErr = errors.New("insert failed")
	if _, err := newSvc(repo2, nil).CreateBankAccount(context.Background(), CreateBankAccountRequest{Name: "X"}); err == nil {
		t.Fatal("want an error when the insert fails")
	}
}

// jwtSubject builds the minimal RegisteredClaims CompleteSession reads.
func jwtSubject(sub string) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{Subject: sub}
}

// compile-time guard: the fake really does satisfy the production interface.
var _ Repository = (*fakeRepo)(nil)
