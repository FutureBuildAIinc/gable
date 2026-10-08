// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// Invoice is where line prices become money owed, so these tests pin the
// subtotal/tax arithmetic exactly. Each test says whether it is a CORRECTNESS
// test (asserts what must be true) or a CHARACTERIZATION test (pins current,
// possibly wrong behaviour so a refactor cannot change it silently).
//
// CreateInvoice's persistence step runs inside database.DB.RunInTx, so tests
// drive it through testutil.TxContext. No SQL runs — the repository is a fake.

// --- fakes -------------------------------------------------------------------

type fakeRepo struct {
	created []Invoice

	createErr  error
	branchRate float64
	branchOK   bool
	branchSeen []*uuid.UUID

	openBalance int64
	existsOrder bool

	memos    []CreditMemo
	memoErr  error
	updateCM error
}

func (f *fakeRepo) CreateInvoice(_ context.Context, inv *Invoice) error {
	if f.createErr != nil {
		return f.createErr
	}
	if inv.ID == uuid.Nil {
		inv.ID = uuid.New()
	}
	f.created = append(f.created, *inv)
	return nil
}
func (f *fakeRepo) GetInvoice(context.Context, uuid.UUID) (*Invoice, error) { return nil, nil }
func (f *fakeRepo) ListInvoices(context.Context) ([]Invoice, error)         { return nil, nil }
func (f *fakeRepo) ListInvoicesPaginated(context.Context, int, int) ([]Invoice, int, error) {
	return nil, 0, nil
}
func (f *fakeRepo) UpdateInvoice(context.Context, *Invoice) error { return nil }
func (f *fakeRepo) ExistsInvoiceForOrder(context.Context, uuid.UUID) (bool, error) {
	return f.existsOrder, nil
}
func (f *fakeRepo) SumOpenBalanceCents(context.Context, uuid.UUID) (int64, error) {
	return f.openBalance, nil
}
func (f *fakeRepo) GetBranchTaxRate(_ context.Context, branchID *uuid.UUID) (float64, bool) {
	f.branchSeen = append(f.branchSeen, branchID)
	return f.branchRate, f.branchOK
}
func (f *fakeRepo) CreateCreditMemo(_ context.Context, cm *CreditMemo) error {
	if f.memoErr != nil {
		return f.memoErr
	}
	if cm.ID == uuid.Nil {
		cm.ID = uuid.New()
	}
	f.memos = append(f.memos, *cm)
	return nil
}
func (f *fakeRepo) ListCreditMemos(context.Context, uuid.UUID) ([]CreditMemo, error) {
	return f.memos, nil
}
func (f *fakeRepo) UpdateCreditMemo(context.Context, *CreditMemo) error { return f.updateCM }

// fakeAccount records AR subledger postings.
type fakeAccount struct {
	posts []accountPost
	err   error
}

type accountPost struct {
	customerID uuid.UUID
	txnType    account.TransactionType
	amount     int64
	refID      *uuid.UUID
	desc       string
}

func (f *fakeAccount) PostTransaction(_ context.Context, customerID uuid.UUID, t account.TransactionType, amount int64, ref *uuid.UUID, desc string) (*account.CustomerTransaction, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.posts = append(f.posts, accountPost{customerID, t, amount, ref, desc})
	return &account.CustomerTransaction{CustomerID: customerID, Type: t, Amount: amount}, nil
}
func (f *fakeAccount) GetAccountSummary(context.Context, uuid.UUID) (*account.AccountSummary, error) {
	return nil, nil
}
func (f *fakeAccount) GetTransactions(context.Context, uuid.UUID) ([]account.CustomerTransaction, error) {
	return nil, nil
}

func txCtx() context.Context { return testutil.TxContext(context.Background()) }

func line(priceEach int64, qty float64) InvoiceLine {
	return InvoiceLine{ProductID: uuid.New(), PriceEach: priceEach, Quantity: qty}
}

// --- subtotal accumulation ---------------------------------------------------

// CORRECTNESS: the subtotal is the sum of the line extensions, in cents, and
// multi-line invoices must accumulate without losing rows.
func TestCreateInvoice_SubtotalAccumulation(t *testing.T) {
	tests := []struct {
		name  string
		lines []InvoiceLine
		want  int64
	}{
		{"single whole-unit line", []InvoiceLine{line(1299, 1)}, 1299},
		{"quantity multiplies the unit price", []InvoiceLine{line(1299, 4)}, 5196},
		{"three lines accumulate", []InvoiceLine{line(1000, 2), line(250, 4), line(99, 1)}, 3099},
		{"fractional board feet", []InvoiceLine{line(100, 12.5)}, 1250},
		{"many small lines do not drift", []InvoiceLine{
			line(1, 1), line(1, 1), line(1, 1), line(1, 1), line(1, 1),
			line(1, 1), line(1, 1), line(1, 1), line(1, 1), line(1, 1),
		}, 10},
		// CHARACTERIZATION: a zero-quantity line contributes nothing and is
		// neither rejected nor dropped. CreateInvoice performs no line-level
		// validation at all.
		{"zero quantity contributes zero", []InvoiceLine{line(5000, 0), line(2500, 2)}, 5000},
		{"zero price contributes zero", []InvoiceLine{line(0, 99), line(2500, 2)}, 5000},
		// CHARACTERIZATION: negative quantities are accepted and reduce the
		// subtotal. There is no guard against a credit-shaped line arriving on
		// a regular invoice.
		{"negative quantity subtracts", []InvoiceLine{line(1000, 5), line(1000, -2)}, 3000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{branchRate: 0, branchOK: false}
			svc := NewService(repo, nil, nil, nil)

			inv := &Invoice{CustomerID: uuid.New(), Lines: tc.lines}
			if err := svc.CreateInvoice(txCtx(), inv); err != nil {
				t.Fatalf("CreateInvoice: %v", err)
			}
			if inv.Subtotal != tc.want {
				t.Errorf("Subtotal = %d, want %d", inv.Subtotal, tc.want)
			}
		})
	}
}

// CORRECTNESS: a pre-computed subtotal is trusted and the lines are not
// re-summed. POS relies on this to carry its exemption-aware numbers through.
func TestCreateInvoice_PrecomputedSubtotalIsPreserved(t *testing.T) {
	repo := &fakeRepo{branchRate: 0.10, branchOK: true}
	svc := NewService(repo, nil, nil, nil)

	inv := &Invoice{
		CustomerID: uuid.New(),
		Subtotal:   9999, // deliberately disagrees with the lines
		Lines:      []InvoiceLine{line(100, 1)},
	}
	if err := svc.CreateInvoice(txCtx(), inv); err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}
	if inv.Subtotal != 9999 {
		t.Errorf("Subtotal = %d, want the caller's 9999 to be preserved", inv.Subtotal)
	}
}

// REGRESSION (this was a documented, pinned defect) — the line extension rounds, never truncates.
//
// invoice/service.go used to compute
//
//	subtotal += int64(float64(line.PriceEach) * line.Quantity)
//
// int64() truncates toward zero, so any fractional cent was dropped, and a
// binary float that lands a hair BELOW an exact integer lost a whole cent:
// 100 cents x 8.29 is exactly 829 in decimal but 828.99999999999989 in float64,
// which truncates to 828.
//
// The same computation in order/service.go:91 uses math.Round, so an order and
// the invoice generated from it disagreed on the same line. Both now round.
func TestCreateInvoice_LineExtensionRounding(t *testing.T) {
	tests := []struct {
		name      string
		priceEach int64
		qty       float64
		want      int64 // correctly rounded cents
	}{
		{"float representation lands just under an exact cent", 100, 8.29, 829},
		{"fractional cent rounds up", 2999, 1.1, 3299},
		{"fractional cent rounds up (2)", 123, 4.56, 561},
		{"fractional cent rounds up (3)", 489, 2.1, 1027},
		{"fractional cent rounds up (4)", 1699, 2.3, 3908},
		{"fractional cent rounds down", 595, 1.15, 684},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{}
			svc := NewService(repo, nil, nil, nil)
			inv := &Invoice{CustomerID: uuid.New(), Lines: []InvoiceLine{line(tc.priceEach, tc.qty)}}
			if err := svc.CreateInvoice(txCtx(), inv); err != nil {
				t.Fatalf("CreateInvoice: %v", err)
			}
			if inv.Subtotal != tc.want {
				t.Errorf("Subtotal = %d, want %d (%d cents x %v)", inv.Subtotal, tc.want, tc.priceEach, tc.qty)
			}
		})
	}
}

// --- tax ---------------------------------------------------------------------

// CORRECTNESS: the rate comes from the invoice's branch when the branch has
// one, and falls back to DefaultTaxRate otherwise. The branch ID is passed
// through, and a nil branch means "resolve the default branch".
func TestCreateInvoice_TaxRateResolution(t *testing.T) {
	branchID := uuid.New()

	t.Run("branch rate wins", func(t *testing.T) {
		repo := &fakeRepo{branchRate: 0.12, branchOK: true}
		svc := NewService(repo, nil, nil, nil)

		inv := &Invoice{CustomerID: uuid.New(), BranchID: branchID, Lines: []InvoiceLine{line(10000, 1)}}
		if err := svc.CreateInvoice(txCtx(), inv); err != nil {
			t.Fatalf("CreateInvoice: %v", err)
		}
		if inv.TaxRate != 0.12 {
			t.Errorf("TaxRate = %v, want the branch rate 0.12", inv.TaxRate)
		}
		if inv.TaxAmount != 1200 {
			t.Errorf("TaxAmount = %d, want 1200", inv.TaxAmount)
		}
		if inv.TotalAmount != 11200 {
			t.Errorf("TotalAmount = %d, want subtotal+tax = 11200", inv.TotalAmount)
		}
		if len(repo.branchSeen) != 1 || repo.branchSeen[0] == nil || *repo.branchSeen[0] != branchID {
			t.Errorf("branch lookup got %v, want the invoice's branch %s", repo.branchSeen, branchID)
		}
	})

	t.Run("no branch rate falls back to DefaultTaxRate", func(t *testing.T) {
		repo := &fakeRepo{branchOK: false}
		svc := NewService(repo, nil, nil, nil)

		inv := &Invoice{CustomerID: uuid.New(), Lines: []InvoiceLine{line(100000, 1)}}
		if err := svc.CreateInvoice(txCtx(), inv); err != nil {
			t.Fatalf("CreateInvoice: %v", err)
		}
		if inv.TaxRate != DefaultTaxRate {
			t.Errorf("TaxRate = %v, want DefaultTaxRate %v", inv.TaxRate, DefaultTaxRate)
		}
		if inv.TaxAmount != 8250 {
			t.Errorf("TaxAmount = %d, want 8250 (100000 x 0.0825)", inv.TaxAmount)
		}
		if len(repo.branchSeen) != 1 || repo.branchSeen[0] != nil {
			t.Errorf("branch lookup got %v, want a nil branch so the repo resolves the default", repo.branchSeen)
		}
	})

	t.Run("caller-supplied rate is not overridden", func(t *testing.T) {
		repo := &fakeRepo{branchRate: 0.12, branchOK: true}
		svc := NewService(repo, nil, nil, nil)

		inv := &Invoice{CustomerID: uuid.New(), TaxRate: 0.05, Lines: []InvoiceLine{line(10000, 1)}}
		if err := svc.CreateInvoice(txCtx(), inv); err != nil {
			t.Fatalf("CreateInvoice: %v", err)
		}
		if inv.TaxRate != 0.05 {
			t.Errorf("TaxRate = %v, want the caller's 0.05", inv.TaxRate)
		}
		if inv.TaxAmount != 500 {
			t.Errorf("TaxAmount = %d, want 500", inv.TaxAmount)
		}
		if len(repo.branchSeen) != 0 {
			t.Error("branch rate must not be looked up when the caller set a rate")
		}
	})
}

// CORRECTNESS: the tax-inclusive identity. Whenever the service computes tax,
// TotalAmount must equal Subtotal + TaxAmount exactly — no rounding may be
// applied twice or dropped between the two.
func TestCreateInvoice_TotalEqualsSubtotalPlusTax(t *testing.T) {
	rates := []float64{0, 0.05, 0.0825, 0.12, 0.13, 0.9999}
	subtotals := []int64{1, 99, 100, 12345, 999999, 100000000}

	for _, rate := range rates {
		for _, sub := range subtotals {
			repo := &fakeRepo{}
			svc := NewService(repo, nil, nil, nil)
			inv := &Invoice{
				CustomerID: uuid.New(),
				Subtotal:   sub,
				TaxRate:    rate,
				Lines:      []InvoiceLine{line(1, 1)},
			}
			if err := svc.CreateInvoice(txCtx(), inv); err != nil {
				t.Fatalf("CreateInvoice: %v", err)
			}
			if inv.TotalAmount != inv.Subtotal+inv.TaxAmount {
				t.Errorf("rate=%v subtotal=%d: TotalAmount %d != Subtotal %d + TaxAmount %d",
					rate, sub, inv.TotalAmount, inv.Subtotal, inv.TaxAmount)
			}
			if inv.TaxAmount < 0 {
				t.Errorf("rate=%v subtotal=%d: negative tax %d", rate, sub, inv.TaxAmount)
			}
		}
	}
}

// REGRESSION (this was a documented, pinned defect) — tax rounds half up, it does not truncate.
//
// invoice/service.go used to compute
//
//	inv.TaxAmount = int64(float64(inv.Subtotal) * inv.TaxRate)
//
// $10.00 at 8.25% is exactly 82.5 cents. int64() truncated to 82, so the
// dealer under-collected. Truncation is systematically biased downward on
// every invoice whose tax has a fractional cent; standard practice is half-up.
func TestCreateInvoice_TaxHalfCentRounding(t *testing.T) {
	tests := []struct {
		subtotal int64
		rate     float64
		want     int64
	}{
		{1000, 0.0825, 83},  // exactly 82.5 -> half-up 83
		{600, 0.0825, 50},   // exactly 49.5 -> half-up 50
		{200, 0.0825, 17},   // exactly 16.5 -> half-up 17
		{1234, 0.0825, 102}, // 101.805 -> 102
		{333, 0.05, 17},     // 16.65 -> 17
	}

	for _, tc := range tests {
		repo := &fakeRepo{}
		svc := NewService(repo, nil, nil, nil)
		inv := &Invoice{
			CustomerID: uuid.New(),
			Subtotal:   tc.subtotal,
			TaxRate:    tc.rate,
			Lines:      []InvoiceLine{line(1, 1)},
		}
		if err := svc.CreateInvoice(txCtx(), inv); err != nil {
			t.Fatalf("CreateInvoice: %v", err)
		}
		if inv.TaxAmount != tc.want {
			t.Errorf("subtotal=%d rate=%v: TaxAmount = %d, want %d", tc.subtotal, tc.rate, inv.TaxAmount, tc.want)
		}
	}
}

// --- the pre-set-total escape hatch -----------------------------------------

// CORRECTNESS: a caller that pre-computed the whole invoice — subtotal AND
// tax-inclusive total — is honored exactly as supplied: rate, tax amount and
// total are left alone and the branch rate is never consulted. POS depends on
// this (it pre-computes exemption-aware tax, including the tax-inclusive
// summary line of a split tender).
//
// The escape hatch requires BOTH numbers. A total on its own is not evidence
// that anyone calculated tax — see
// TestCreateInvoice_OrderFulfilmentPathIsTaxed below.
func TestCreateInvoice_PresetTotalSuppressesTax(t *testing.T) {
	repo := &fakeRepo{branchRate: 0.12, branchOK: true}
	svc := NewService(repo, nil, nil, nil)

	inv := &Invoice{
		CustomerID:  uuid.New(),
		Subtotal:    10000,
		TaxAmount:   1200,
		TaxRate:     0.12,
		TotalAmount: 11200,
		Lines:       []InvoiceLine{line(10000, 1)},
	}
	if err := svc.CreateInvoice(txCtx(), inv); err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}
	if inv.TotalAmount != 11200 || inv.TaxAmount != 1200 || inv.TaxRate != 0.12 {
		t.Errorf("pre-computed totals were altered: subtotal=%d tax=%d rate=%v total=%d",
			inv.Subtotal, inv.TaxAmount, inv.TaxRate, inv.TotalAmount)
	}
	if len(repo.branchSeen) != 0 {
		t.Error("branch tax rate must not be consulted when the caller pre-computed the total")
	}
}

// REGRESSION (this was a documented, pinned defect) — order fulfilment must produce a taxed invoice.
//
// order/service.go used to build the invoice with TotalAmount set to the
// order's PRE-TAX total, above the comment
//
//	"CreateInvoice recomputes subtotal/tax and sets the tax-inclusive TotalAmount"
//
// but any non-zero TotalAmount made CreateInvoice skip the tax block entirely.
// The invoice was stored with TaxRate 0, TaxAmount 0 and a pre-tax
// TotalAmount, and that untaxed figure is what got posted to the GL and the AR
// subledger by PostInvoiceToLedger. The delivery-completion path
// (cmd/server/main.go) builds the same invoice WITHOUT TotalAmount, so it was
// taxed correctly — the same order billed a different amount depending on
// which path invoiced it.
//
// Fixed on both sides: order fulfilment no longer passes the pre-tax total,
// and CreateInvoice now treats a total WITHOUT a caller-supplied subtotal as
// no evidence of a tax calculation and recomputes it. This test reproduces the
// old order-fulfilment construction, so it pins the invoice-side guard.
func TestCreateInvoice_OrderFulfilmentPathIsTaxed(t *testing.T) {
	repo := &fakeRepo{branchRate: 0.12, branchOK: true}
	svc := NewService(repo, nil, nil, nil)

	const orderTotalCents = 10000 // what order.CreateOrder computed, pre-tax

	// Exactly how order.FulfillOrder builds the invoice.
	inv := &Invoice{
		OrderID:     uuid.New(),
		CustomerID:  uuid.New(),
		BranchID:    uuid.New(),
		TotalAmount: orderTotalCents,
		Status:      InvoiceStatusUnpaid,
		Lines:       []InvoiceLine{line(10000, 1)},
	}
	if err := svc.CreateInvoice(txCtx(), inv); err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}

	if inv.TaxRate != 0.12 {
		t.Errorf("TaxRate = %v, want the branch rate 0.12", inv.TaxRate)
	}
	if inv.TaxAmount != 1200 {
		t.Errorf("TaxAmount = %d, want 1200", inv.TaxAmount)
	}
	if inv.TotalAmount != 11200 {
		t.Errorf("TotalAmount = %d, want the tax-inclusive 11200 — a fulfilled order must bill tax",
			inv.TotalAmount)
	}
}

// --- defaults and validation -------------------------------------------------

// CORRECTNESS: an invoice with no lines is meaningless and must be rejected
// before anything is written.
func TestCreateInvoice_RejectsEmptyLines(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo, nil, nil, nil)

	err := svc.CreateInvoice(txCtx(), &Invoice{CustomerID: uuid.New()})
	if err == nil {
		t.Fatal("want an error for an invoice with no lines")
	}
	if len(repo.created) != 0 {
		t.Error("nothing may be persisted for a rejected invoice")
	}
}

// CORRECTNESS: unset status and terms get safe defaults, and a due date is
// always stamped.
func TestCreateInvoice_Defaults(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo, nil, nil, nil)

	before := time.Now()
	inv := &Invoice{CustomerID: uuid.New(), Lines: []InvoiceLine{line(1000, 1)}}
	if err := svc.CreateInvoice(txCtx(), inv); err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}

	if inv.Status != InvoiceStatusUnpaid {
		t.Errorf("Status = %q, want %q", inv.Status, InvoiceStatusUnpaid)
	}
	if inv.PaymentTerms != TermsNet30 {
		t.Errorf("PaymentTerms = %q, want %q", inv.PaymentTerms, TermsNet30)
	}
	if inv.DueDate == nil {
		t.Fatal("DueDate must be set")
	}
	wantEarliest := before.AddDate(0, 0, 30)
	if inv.DueDate.Before(wantEarliest.Add(-time.Minute)) {
		t.Errorf("DueDate = %v, want roughly 30 days out (%v)", inv.DueDate, wantEarliest)
	}
}

// CORRECTNESS: a caller-supplied status and due date are respected.
func TestCreateInvoice_PreservesCallerStatusAndDueDate(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo, nil, nil, nil)

	due := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	inv := &Invoice{
		CustomerID:   uuid.New(),
		Status:       InvoiceStatusPartial,
		PaymentTerms: TermsCOD,
		DueDate:      &due,
		Lines:        []InvoiceLine{line(1000, 1)},
	}
	if err := svc.CreateInvoice(txCtx(), inv); err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}
	if inv.Status != InvoiceStatusPartial {
		t.Errorf("Status = %q, want it preserved", inv.Status)
	}
	if !inv.DueDate.Equal(due) {
		t.Errorf("DueDate = %v, want the caller's %v", inv.DueDate, due)
	}
}

// CORRECTNESS: a repository failure must surface, not be swallowed.
func TestCreateInvoice_RepositoryErrorPropagates(t *testing.T) {
	repo := &fakeRepo{createErr: errors.New("insert failed")}
	svc := NewService(repo, nil, nil, nil)

	err := svc.CreateInvoice(txCtx(), &Invoice{CustomerID: uuid.New(), Lines: []InvoiceLine{line(1000, 1)}})
	if err == nil {
		t.Fatal("want the repository error to propagate")
	}
}

// CORRECTNESS: payment terms map to the documented net periods; anything
// unrecognised defaults to 30 days rather than to "due immediately".
func TestCalcDueDate(t *testing.T) {
	from := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		terms string
		want  time.Time
	}{
		{TermsCOD, from},
		{TermsDueOnReceipt, from},
		{TermsNet30, from.AddDate(0, 0, 30)},
		{TermsNet60, from.AddDate(0, 0, 60)},
		{TermsNet90, from.AddDate(0, 0, 90)},
		{"NET45", from.AddDate(0, 0, 30)},
		{"", from.AddDate(0, 0, 30)},
	}

	for _, tc := range tests {
		t.Run(tc.terms, func(t *testing.T) {
			if got := calcDueDate(from, tc.terms); !got.Equal(tc.want) {
				t.Errorf("calcDueDate(%v, %q) = %v, want %v", from, tc.terms, got, tc.want)
			}
		})
	}
}

// --- credit memos ------------------------------------------------------------

// CORRECTNESS: a credit memo must carry a positive amount. A zero or negative
// memo would become a debit when applied (ApplyCreditMemoFull posts -Amount).
func TestCreateCreditMemo_RejectsNonPositiveAmounts(t *testing.T) {
	tests := []struct {
		name    string
		amount  int64
		wantErr bool
	}{
		{"positive is accepted", 1, false},
		{"zero is rejected", 0, true},
		{"negative is rejected", -500, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{}
			svc := NewService(repo, nil, nil, nil)

			cm, err := svc.CreateCreditMemo(context.Background(), uuid.New(), nil, tc.amount, "damaged goods")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error for amount %d", tc.amount)
				}
				if len(repo.memos) != 0 {
					t.Error("a rejected memo must not be persisted")
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateCreditMemo: %v", err)
			}
			if cm.Status != "PENDING" {
				t.Errorf("Status = %q, want PENDING — a new memo is not yet applied", cm.Status)
			}
			if cm.Amount != tc.amount {
				t.Errorf("Amount = %d, want %d", cm.Amount, tc.amount)
			}
			if cm.AppliedAt != nil {
				t.Error("AppliedAt must be nil on a pending memo")
			}
		})
	}
}

// --- ledger posting (double entry) ------------------------------------------

// CORRECTNESS: posting an invoice books a balanced GL entry (DR AR / CR Sales
// Revenue) for the invoice total, and debits the customer's AR subledger by the
// same amount. The GL and the subledger must never disagree.
func TestPostInvoiceToLedger_BalancedAndAgreeing(t *testing.T) {
	glRepo := newFakeGLRepo()
	acct := &fakeAccount{}
	svc := NewService(&fakeRepo{}, newGLService(glRepo), acct, nil)

	inv := &Invoice{ID: uuid.New(), CustomerID: uuid.New(), TotalAmount: 11200}
	if err := svc.PostInvoiceToLedger(context.Background(), inv); err != nil {
		t.Fatalf("PostInvoiceToLedger: %v", err)
	}

	if len(glRepo.entries) != 1 {
		t.Fatalf("wrote %d journal entries, want 1", len(glRepo.entries))
	}
	entry := glRepo.entries[0]

	assertBalanced(t, entry)
	assertLeg(t, glRepo, entry, arCode, revenueCode, inv.TotalAmount)

	if len(acct.posts) != 1 {
		t.Fatalf("made %d subledger postings, want 1", len(acct.posts))
	}
	post := acct.posts[0]
	if post.amount != inv.TotalAmount {
		t.Errorf("subledger amount = %d, want the invoice total %d", post.amount, inv.TotalAmount)
	}
	if post.customerID != inv.CustomerID {
		t.Errorf("subledger posted against %s, want %s", post.customerID, inv.CustomerID)
	}
	if post.txnType != account.TransactionTypeInvoice {
		t.Errorf("subledger type = %q, want %q", post.txnType, account.TransactionTypeInvoice)
	}
	if post.refID == nil || *post.refID != inv.ID {
		t.Errorf("subledger reference = %v, want the invoice ID %s", post.refID, inv.ID)
	}

	// The invariant the two ledgers share.
	var glDebitAR int64
	arID := glRepo.idFor(arCode)
	for _, l := range entry.Lines {
		if l.AccountID == arID {
			glDebitAR += l.Debit - l.Credit
		}
	}
	if glDebitAR != post.amount {
		t.Errorf("GL debited AR by %d but the subledger recorded %d", glDebitAR, post.amount)
	}
}

// CORRECTNESS: if the GL leg fails, the AR subledger must not be posted —
// otherwise the customer owes money that no journal entry explains.
func TestPostInvoiceToLedger_GLFailureBlocksSubledger(t *testing.T) {
	glRepo := newFakeGLRepo()
	glRepo.accounts = nil // chart of accounts missing -> resolveAccountIDs fails
	acct := &fakeAccount{}
	svc := NewService(&fakeRepo{}, newGLService(glRepo), acct, nil)

	err := svc.PostInvoiceToLedger(context.Background(), &Invoice{ID: uuid.New(), CustomerID: uuid.New(), TotalAmount: 5000})
	if err == nil {
		t.Fatal("want an error when the GL posting fails")
	}
	if len(acct.posts) != 0 {
		t.Errorf("AR subledger was posted (%+v) despite the GL failing", acct.posts)
	}
}

// CORRECTNESS: a store-credit return is the mirror of an invoice — the GL entry
// reverses (DR Revenue / CR AR) and the subledger amount is NEGATIVE so the
// customer owes less.
func TestPostAccountReturnToLedger_MirrorsInvoice(t *testing.T) {
	glRepo := newFakeGLRepo()
	acct := &fakeAccount{}
	svc := NewService(&fakeRepo{}, newGLService(glRepo), acct, nil)

	custID, returnID := uuid.New(), uuid.New()
	glEntryID, err := svc.PostAccountReturnToLedger(context.Background(), custID, returnID, 2500)
	if err != nil {
		t.Fatalf("PostAccountReturnToLedger: %v", err)
	}
	if glEntryID == uuid.Nil {
		t.Error("want the GL entry ID returned so the return row can link to it")
	}

	if len(glRepo.entries) != 1 {
		t.Fatalf("wrote %d journal entries, want 1", len(glRepo.entries))
	}
	assertBalanced(t, glRepo.entries[0])
	assertLeg(t, glRepo, glRepo.entries[0], revenueCode, arCode, 2500)

	if len(acct.posts) != 1 {
		t.Fatalf("made %d subledger postings, want 1", len(acct.posts))
	}
	if acct.posts[0].amount != -2500 {
		t.Errorf("subledger amount = %d, want -2500 — store credit must reduce what the customer owes",
			acct.posts[0].amount)
	}
	if acct.posts[0].txnType != account.TransactionTypeRefund {
		t.Errorf("subledger type = %q, want %q", acct.posts[0].txnType, account.TransactionTypeRefund)
	}
}

// CORRECTNESS: with no GL wired (nil service) the money paths must degrade
// quietly rather than panic — POS calls these best-effort after commit.
func TestLedgerPosting_NilGLIsSafe(t *testing.T) {
	acct := &fakeAccount{}
	svc := NewService(&fakeRepo{}, nil, acct, nil)

	if err := svc.PostCashSaleToGL(context.Background(), uuid.New().String(), 1000); err != nil {
		t.Errorf("PostCashSaleToGL with no GL: %v", err)
	}
	id, err := svc.PostCashReturnToGL(context.Background(), uuid.New().String(), 1000)
	if err != nil || id != uuid.Nil {
		t.Errorf("PostCashReturnToGL with no GL = (%v, %v), want (uuid.Nil, nil)", id, err)
	}

	// The subledger leg still runs even without a GL.
	if _, err := svc.PostAccountReturnToLedger(context.Background(), uuid.New(), uuid.New(), 500); err != nil {
		t.Errorf("PostAccountReturnToLedger with no GL: %v", err)
	}
	if len(acct.posts) != 1 || acct.posts[0].amount != -500 {
		t.Errorf("subledger postings = %+v, want one posting of -500", acct.posts)
	}
}
