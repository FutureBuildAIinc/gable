// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// These tests cover the AR subledger: the single writer for customers.balance_due.
// Every assertion here is a CORRECTNESS test (what must be true of a ledger)
// unless a comment says otherwise.
//
// PostTransaction's whole body runs inside database.DB.RunInTx, so the tests
// drive it through testutil.TxContext, which makes RunInTx take its
// "already in a transaction" branch. No SQL is executed: the repository is a
// fake. See testutil/tx.go for why that seam is needed.

// --- fake repository ---------------------------------------------------------

type fakeRepo struct {
	balance     int64
	creditLimit int64

	balanceErr error
	limitErr   error
	createErr  error
	updateErr  error

	created  []CustomerTransaction
	balances []int64 // every value handed to UpdateCustomerBalance, in order
	txns     []CustomerTransaction
	txnsErr  error
}

func (f *fakeRepo) CreateTransaction(_ context.Context, txn *CustomerTransaction) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, *txn)
	return nil
}

func (f *fakeRepo) GetTransactionsByCustomerID(context.Context, uuid.UUID) ([]CustomerTransaction, error) {
	return f.txns, f.txnsErr
}

func (f *fakeRepo) GetBalance(context.Context, uuid.UUID) (int64, error) {
	if f.balanceErr != nil {
		return 0, f.balanceErr
	}
	return f.balance, nil
}

func (f *fakeRepo) GetCreditLimit(context.Context, uuid.UUID) (int64, error) {
	if f.limitErr != nil {
		return 0, f.limitErr
	}
	return f.creditLimit, nil
}

func (f *fakeRepo) UpdateCustomerBalance(_ context.Context, _ uuid.UUID, newBalance int64) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.balances = append(f.balances, newBalance)
	f.balance = newBalance // so a sequence of posts compounds like the real column
	return nil
}

func newTestService(repo Repository) Service {
	return NewService(repo, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func txCtx() context.Context { return testutil.TxContext(context.Background()) }

// --- PostTransaction ---------------------------------------------------------

// CORRECTNESS: a subledger posting must be balance_before + amount, and the
// value written back to customers.balance_due must equal the BalanceAfter
// recorded on the transaction row. If those two ever disagree the statement
// and the customer record drift apart.
func TestPostTransaction_BalanceArithmetic(t *testing.T) {
	custID := uuid.New()
	refID := uuid.New()

	tests := []struct {
		name    string
		opening int64
		txnType TransactionType
		amount  int64
		want    int64
	}{
		{"invoice debit increases what the customer owes", 0, TransactionTypeInvoice, 10825, 10825},
		{"invoice on an existing balance accumulates", 10825, TransactionTypeInvoice, 5000, 15825},
		{"payment credit is posted as a negative amount", 15825, TransactionTypePayment, -15825, 0},
		{"refund credit can drive the balance negative", 2500, TransactionTypeRefund, -4000, -1500},
		{"posting against a credit balance still adds", -1500, TransactionTypeInvoice, 1000, -500},
		{"zero-amount adjustment leaves the balance alone", 7777, TransactionTypeAdjustment, 0, 7777},
		{"one cent is not lost", 1, TransactionTypeInvoice, 1, 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{balance: tc.opening}
			svc := newTestService(repo)

			txn, err := svc.PostTransaction(txCtx(), custID, tc.txnType, tc.amount, &refID, "test")
			if err != nil {
				t.Fatalf("PostTransaction: %v", err)
			}

			if txn.BalanceAfter != tc.want {
				t.Errorf("BalanceAfter = %d, want %d", txn.BalanceAfter, tc.want)
			}
			if txn.Amount != tc.amount {
				t.Errorf("Amount = %d, want %d (the posted amount must be recorded verbatim)", txn.Amount, tc.amount)
			}
			if txn.Type != tc.txnType {
				t.Errorf("Type = %q, want %q", txn.Type, tc.txnType)
			}
			if txn.CustomerID != custID {
				t.Errorf("CustomerID = %s, want %s", txn.CustomerID, custID)
			}
			if txn.ReferenceID == nil || *txn.ReferenceID != refID {
				t.Errorf("ReferenceID = %v, want %s", txn.ReferenceID, refID)
			}
			if txn.ID == uuid.Nil {
				t.Error("transaction ID must be generated, got uuid.Nil")
			}
			if txn.CreatedAt.IsZero() {
				t.Error("CreatedAt must be stamped")
			}

			if len(repo.balances) != 1 {
				t.Fatalf("UpdateCustomerBalance called %d times, want 1", len(repo.balances))
			}
			if repo.balances[0] != tc.want {
				t.Errorf("wrote balance_due = %d, want %d", repo.balances[0], tc.want)
			}
			if len(repo.created) != 1 {
				t.Fatalf("CreateTransaction called %d times, want 1", len(repo.created))
			}
			if repo.created[0].BalanceAfter != repo.balances[0] {
				t.Errorf("subledger row BalanceAfter=%d disagrees with the customer balance written (%d)",
					repo.created[0].BalanceAfter, repo.balances[0])
			}
		})
	}
}

// CORRECTNESS: the ledger invariant. Replaying a sequence of postings, the
// final balance must equal the opening balance plus the sum of every amount,
// and each row's BalanceAfter must be the running total at that point.
func TestPostTransaction_LedgerInvariant(t *testing.T) {
	custID := uuid.New()
	repo := &fakeRepo{balance: 25000} // $250.00 opening
	svc := newTestService(repo)

	amounts := []struct {
		typ TransactionType
		amt int64
	}{
		{TransactionTypeInvoice, 108250},
		{TransactionTypePayment, -50000},
		{TransactionTypeInvoice, 1},
		{TransactionTypeRefund, -333},
		{TransactionTypeAdjustment, 7},
		{TransactionTypePayment, -83925},
	}

	var running int64 = 25000
	for i, a := range amounts {
		running += a.amt
		txn, err := svc.PostTransaction(txCtx(), custID, a.typ, a.amt, nil, "seq")
		if err != nil {
			t.Fatalf("posting %d: %v", i, err)
		}
		if txn.BalanceAfter != running {
			t.Fatalf("posting %d: BalanceAfter = %d, want running total %d", i, txn.BalanceAfter, running)
		}
	}

	var sum int64
	for _, a := range amounts {
		sum += a.amt
	}
	if got, want := repo.balance, int64(25000)+sum; got != want {
		t.Errorf("final balance_due = %d, want opening+sum(amounts) = %d", got, want)
	}
	if len(repo.created) != len(amounts) {
		t.Errorf("wrote %d subledger rows, want %d — every posting must leave an audit row",
			len(repo.created), len(amounts))
	}
}

// CORRECTNESS: a failure anywhere in the posting must not leave a half-applied
// ledger. Real isolation comes from the surrounding transaction, but the
// service must at minimum surface the error and not attempt later steps.
func TestPostTransaction_ErrorPropagation(t *testing.T) {
	custID := uuid.New()
	boom := errors.New("boom")

	t.Run("GetBalance failure aborts before any write", func(t *testing.T) {
		repo := &fakeRepo{balanceErr: boom}
		txn, err := newTestService(repo).PostTransaction(txCtx(), custID, TransactionTypeInvoice, 500, nil, "x")
		if err == nil {
			t.Fatal("want error when the current balance cannot be read")
		}
		if txn != nil {
			t.Error("no transaction should be returned on failure")
		}
		if len(repo.created) != 0 || len(repo.balances) != 0 {
			t.Error("nothing may be written when the opening balance is unknown")
		}
	})

	t.Run("CreateTransaction failure leaves balance_due untouched", func(t *testing.T) {
		repo := &fakeRepo{balance: 1000, createErr: boom}
		_, err := newTestService(repo).PostTransaction(txCtx(), custID, TransactionTypeInvoice, 500, nil, "x")
		if err == nil {
			t.Fatal("want error when the subledger row cannot be written")
		}
		if len(repo.balances) != 0 {
			t.Errorf("balance_due was updated (%v) even though the audit row failed", repo.balances)
		}
	})

	t.Run("UpdateCustomerBalance failure is surfaced", func(t *testing.T) {
		repo := &fakeRepo{balance: 1000, updateErr: boom}
		txn, err := newTestService(repo).PostTransaction(txCtx(), custID, TransactionTypeInvoice, 500, nil, "x")
		if err == nil {
			t.Fatal("want error when the customer balance cannot be updated")
		}
		if txn != nil {
			t.Error("no transaction should be returned on failure")
		}
	})
}

// --- GetAccountSummary -------------------------------------------------------

// The credit gate. CORRECTNESS for the arithmetic; the negative-available case
// is flagged as CHARACTERIZATION because "available credit" going negative is
// current behaviour that a UI may or may not be prepared for.
func TestGetAccountSummary_AvailableCredit(t *testing.T) {
	custID := uuid.New()

	tests := []struct {
		name        string
		balance     int64
		creditLimit int64
		wantAvail   int64
	}{
		{"unused limit", 0, 500000, 500000},
		{"partially used", 125000, 500000, 375000},
		{"exactly at the limit leaves zero", 500000, 500000, 0},
		{"one cent under the limit", 499999, 500000, 1},
		{"one cent over the limit", 500001, 500000, -1},
		// CHARACTERIZATION: an over-limit customer reports NEGATIVE available
		// credit rather than being clamped at zero. Consumers must not assume
		// this is unsigned.
		{"over limit reports negative available credit", 750000, 500000, -250000},
		{"no limit configured", 12345, 0, -12345},
		{"credit balance increases available credit above the limit", -10000, 500000, 510000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{balance: tc.balance, creditLimit: tc.creditLimit}
			sum, err := newTestService(repo).GetAccountSummary(context.Background(), custID)
			if err != nil {
				t.Fatalf("GetAccountSummary: %v", err)
			}
			if sum.AvailableCredit != tc.wantAvail {
				t.Errorf("AvailableCredit = %d, want %d", sum.AvailableCredit, tc.wantAvail)
			}
			if sum.BalanceDue != tc.balance {
				t.Errorf("BalanceDue = %d, want %d", sum.BalanceDue, tc.balance)
			}
			if sum.CreditLimit != tc.creditLimit {
				t.Errorf("CreditLimit = %d, want %d", sum.CreditLimit, tc.creditLimit)
			}
			if sum.CustomerID != custID {
				t.Errorf("CustomerID = %s, want %s", sum.CustomerID, custID)
			}
			// The identity the credit gate depends on.
			if sum.CreditLimit-sum.BalanceDue != sum.AvailableCredit {
				t.Errorf("available credit (%d) != limit (%d) - balance (%d)",
					sum.AvailableCredit, sum.CreditLimit, sum.BalanceDue)
			}
		})
	}
}

// CORRECTNESS: a summary must never be fabricated from a partial read — an
// unreadable balance or limit has to fail rather than default to zero, which
// would silently hand a customer unlimited credit.
func TestGetAccountSummary_ErrorPropagation(t *testing.T) {
	boom := errors.New("boom")

	for _, tc := range []struct {
		name string
		repo *fakeRepo
	}{
		{"balance unreadable", &fakeRepo{balanceErr: boom, creditLimit: 100000}},
		{"credit limit unreadable", &fakeRepo{balance: 100, limitErr: boom}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sum, err := newTestService(tc.repo).GetAccountSummary(context.Background(), uuid.New())
			if err == nil {
				t.Fatal("want error")
			}
			if sum != nil {
				t.Errorf("want nil summary on error, got %+v", sum)
			}
		})
	}
}

// --- GetTransactions ---------------------------------------------------------

func TestGetTransactions(t *testing.T) {
	want := []CustomerTransaction{
		{ID: uuid.New(), Amount: 100, BalanceAfter: 100},
		{ID: uuid.New(), Amount: -40, BalanceAfter: 60},
	}
	repo := &fakeRepo{txns: want}

	got, err := newTestService(repo).GetTransactions(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("GetTransactions: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d transactions, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Amount != want[i].Amount {
			t.Errorf("transaction %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	repo.txnsErr = errors.New("boom")
	if _, err := newTestService(repo).GetTransactions(context.Background(), uuid.New()); err == nil {
		t.Error("want error to propagate")
	}
}
