// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Financial statement assembly.
//
// The statements are the one place in the ERP where an arithmetic slip is
// self-evidently wrong rather than merely wrong: a balance sheet that does not
// balance is visibly broken on screen. These tests therefore assert the
// accounting identities themselves, not just field-by-field arithmetic:
//
//	debits = credits            (enforced on every fixture by ledger())
//	assets = liabilities + equity
//	P&L net income = balance sheet retained earnings, over the same window
//
// Assembly is a pure function over []AccountActivity, so all of this runs
// without Postgres.

// posting is one journal line: an account and a signed amount in cents,
// positive for a debit and negative for a credit.
type posting struct {
	code    string
	typ     string
	subtype string
	name    string
	amount  int64
}

func dr(code, typ, name string, cents int64) posting {
	return posting{code: code, typ: typ, name: name, amount: cents}
}

func cr(code, typ, name string, cents int64) posting {
	return posting{code: code, typ: typ, name: name, amount: -cents}
}

// cogs is a debit to an expense account marked subtype COGS, which is what
// puts it above the gross profit line.
func cogs(code, name string, cents int64) posting {
	return posting{code: code, typ: AccountTypeExpense, subtype: COGSSubtype, name: name, amount: cents}
}

// ledger aggregates journal lines into the per-account activity that
// GetAccountActivity returns, and fails the test if the lines do not balance.
//
// That guard matters: it keeps "debits = credits" a property of the fixture
// rather than of the code under test, so the identity assertions below are
// testing assembly instead of quietly assuming their own conclusion.
func ledger(t *testing.T, postings ...posting) []AccountActivity {
	t.Helper()

	var totalDebit, totalCredit int64
	order := []string{}
	byCode := map[string]*AccountActivity{}

	for _, p := range postings {
		acct, ok := byCode[p.code]
		if !ok {
			acct = &AccountActivity{
				AccountID:      uuid.New(),
				AccountCode:    p.code,
				AccountName:    p.name,
				AccountType:    p.typ,
				AccountSubtype: p.subtype,
			}
			byCode[p.code] = acct
			order = append(order, p.code)
		}
		if p.amount >= 0 {
			acct.Debit += p.amount
			totalDebit += p.amount
		} else {
			acct.Credit += -p.amount
			totalCredit += -p.amount
		}
	}

	if totalDebit != totalCredit {
		t.Fatalf("fixture is not a valid ledger: debits %d != credits %d", totalDebit, totalCredit)
	}

	out := make([]AccountActivity, 0, len(order))
	for _, code := range order {
		out = append(out, *byCode[code])
	}
	return out
}

// split partitions activity into the balance-sheet half and the income-statement
// half, the way the two GetAccountActivity calls in GetBalanceSheet do.
func split(rows []AccountActivity) (position, earnings []AccountActivity) {
	for _, r := range rows {
		switch r.AccountType {
		case AccountTypeAsset, AccountTypeLiability, AccountTypeEquity:
			position = append(position, r)
		default:
			earnings = append(earnings, r)
		}
	}
	return position, earnings
}

func TestSignedBalance(t *testing.T) {
	tests := []struct {
		name  string
		row   AccountActivity
		want  int64
		notes string
	}{
		{
			name: "asset with its natural debit balance",
			row:  AccountActivity{AccountType: AccountTypeAsset, Debit: 150_000, Credit: 20_000},
			want: 130_000,
		},
		{
			name:  "asset carrying a credit balance reports negative",
			row:   AccountActivity{AccountType: AccountTypeAsset, Debit: 20_000, Credit: 120_000},
			want:  -100_000,
			notes: "an overdrawn bank or a contra-asset must not be flipped positive",
		},
		{
			name: "liability with its natural credit balance",
			row:  AccountActivity{AccountType: AccountTypeLiability, Debit: 10_000, Credit: 75_000},
			want: 65_000,
		},
		{
			name: "equity with its natural credit balance",
			row:  AccountActivity{AccountType: AccountTypeEquity, Debit: 0, Credit: 500_000},
			want: 500_000,
		},
		{
			name: "revenue with its natural credit balance",
			row:  AccountActivity{AccountType: AccountTypeRevenue, Debit: 5_000, Credit: 305_000},
			want: 300_000,
		},
		{
			name:  "revenue net of returns can go negative",
			row:   AccountActivity{AccountType: AccountTypeRevenue, Debit: 90_000, Credit: 40_000},
			want:  -50_000,
			notes: "a period with more refunds than sales is a real, reportable state",
		},
		{
			name: "expense with its natural debit balance",
			row:  AccountActivity{AccountType: AccountTypeExpense, Debit: 45_000, Credit: 5_000},
			want: 40_000,
		},
		{
			name: "account with no activity is zero, not absent",
			row:  AccountActivity{AccountType: AccountTypeAsset},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := signedBalance(tt.row); got != tt.want {
				t.Errorf("signedBalance() = %d, want %d (%s)", got, tt.want, tt.notes)
			}
		})
	}
}

func TestAssembleProfitAndLoss(t *testing.T) {
	tests := []struct {
		name          string
		postings      []posting
		wantRevenue   int64
		wantCOGS      int64
		wantGross     int64
		wantExpenses  int64
		wantNetIncome int64
		wantCounts    [3]int // revenue, cogs, expenses
	}{
		{
			name: "a profitable month",
			postings: []posting{
				dr("1020", AccountTypeAsset, "Accounts Receivable", 1_000_000),
				cr("4010", AccountTypeRevenue, "Sales Revenue", 1_000_000),
				cogs("5010", "Cost of Goods Sold", 600_000),
				cr("1300", AccountTypeAsset, "Inventory", 600_000),
				dr("6010", AccountTypeExpense, "Rent", 150_000),
				cr("1010", AccountTypeAsset, "Cash", 150_000),
			},
			wantRevenue:   1_000_000,
			wantCOGS:      600_000,
			wantGross:     400_000,
			wantExpenses:  150_000,
			wantNetIncome: 250_000,
			wantCounts:    [3]int{1, 1, 1},
		},
		{
			name: "a loss-making month reports negative net income",
			postings: []posting{
				dr("1020", AccountTypeAsset, "Accounts Receivable", 100_000),
				cr("4010", AccountTypeRevenue, "Sales Revenue", 100_000),
				cogs("5010", "Cost of Goods Sold", 80_000),
				cr("1300", AccountTypeAsset, "Inventory", 80_000),
				dr("6010", AccountTypeExpense, "Rent", 150_000),
				cr("1010", AccountTypeAsset, "Cash", 150_000),
			},
			wantRevenue:   100_000,
			wantCOGS:      80_000,
			wantGross:     20_000,
			wantExpenses:  150_000,
			wantNetIncome: -130_000,
			wantCounts:    [3]int{1, 1, 1},
		},
		{
			name:          "an empty ledger reports zeroes, not absent sections",
			postings:      nil,
			wantRevenue:   0,
			wantCOGS:      0,
			wantGross:     0,
			wantExpenses:  0,
			wantNetIncome: 0,
			wantCounts:    [3]int{0, 0, 0},
		},
		{
			name: "subtype COGS goes above the gross profit line, everything else below",
			postings: []posting{
				dr("1020", AccountTypeAsset, "Accounts Receivable", 900_000),
				cr("4010", AccountTypeRevenue, "Sales Revenue", 900_000),
				cogs("5010", "COGS - Lumber", 300_000),
				cogs("5015", "COGS - Freight In", 50_000),
				dr("5100", AccountTypeExpense, "Wages", 200_000),
				cr("1010", AccountTypeAsset, "Cash", 550_000),
			},
			wantRevenue:   900_000,
			wantCOGS:      350_000,
			wantGross:     550_000,
			wantExpenses:  200_000,
			wantNetIncome: 350_000,
			wantCounts:    [3]int{1, 2, 1},
		},
		{
			// Regression guard for the classification rule this port arrived
			// with: "expense accounts whose code starts 50 are COGS". Against
			// this repo's actual seeded chart of accounts that rule is wrong
			// twice over — 5020 "Operating Expenses" (migration 025) and 5030
			// "Cash Over/Short" (migration 076) are both subtype 'Operating'.
			// Under the prefix rule this fixture reports gross profit of
			// $0.00 and operating expenses of $0.00; both are nonsense, and
			// gross margin is the number a P&L exists to show.
			name: "the seeded 50xx operating accounts are not cost of goods sold",
			postings: []posting{
				dr("1020", AccountTypeAsset, "Accounts Receivable", 1_000_000),
				cr("4010", AccountTypeRevenue, "Sales Revenue", 1_000_000),
				cogs("5010", "Cost of Goods Sold", 400_000),
				{code: "5020", typ: AccountTypeExpense, subtype: "Operating", name: "Operating Expenses", amount: 150_000},
				{code: "5030", typ: AccountTypeExpense, subtype: "Operating", name: "Cash Over/Short", amount: 50_000},
				cr("1010", AccountTypeAsset, "Cash", 600_000),
			},
			wantRevenue:   1_000_000,
			wantCOGS:      400_000,
			wantGross:     600_000,
			wantExpenses:  200_000, // 150,000 + 50,000, both operating
			wantNetIncome: 400_000,
			wantCounts:    [3]int{1, 1, 2},
		},
		{
			name: "an expense with no subtype defaults to operating, not COGS",
			postings: []posting{
				dr("1020", AccountTypeAsset, "Accounts Receivable", 500_000),
				cr("4010", AccountTypeRevenue, "Sales Revenue", 500_000),
				// Deliberately code 5099 with an empty subtype: a
				// user-created account that never had one set.
				dr("5099", AccountTypeExpense, "Uncategorised Expense", 100_000),
				cr("1010", AccountTypeAsset, "Cash", 100_000),
			},
			wantRevenue:   500_000,
			wantCOGS:      0,
			wantGross:     500_000,
			wantExpenses:  100_000,
			wantNetIncome: 400_000,
			wantCounts:    [3]int{1, 0, 1},
		},
		{
			name: "returns exceeding sales produce negative revenue",
			postings: []posting{
				dr("4010", AccountTypeRevenue, "Sales Revenue", 500_000),
				cr("1010", AccountTypeAsset, "Cash", 500_000),
				dr("1010", AccountTypeAsset, "Cash", 200_000),
				cr("4010", AccountTypeRevenue, "Sales Revenue", 200_000),
			},
			wantRevenue:   -300_000,
			wantCOGS:      0,
			wantGross:     -300_000,
			wantExpenses:  0,
			wantNetIncome: -300_000,
			wantCounts:    [3]int{1, 0, 0},
		},
		{
			name: "a nine-figure ledger stays exact in int64 cents",
			postings: []posting{
				dr("1020", AccountTypeAsset, "Accounts Receivable", 123_456_789_01),
				cr("4010", AccountTypeRevenue, "Sales Revenue", 123_456_789_01),
				cogs("5010", "Cost of Goods Sold", 98_765_432_10),
				cr("1300", AccountTypeAsset, "Inventory", 98_765_432_10),
			},
			wantRevenue:   123_456_789_01,
			wantCOGS:      98_765_432_10,
			wantGross:     24_691_356_91,
			wantExpenses:  0,
			wantNetIncome: 24_691_356_91,
			wantCounts:    [3]int{1, 1, 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, earnings := split(ledger(t, tt.postings...))
			got := assembleProfitAndLoss("2026-08-01", "2026-08-31", earnings)

			if got.TotalRevenue != tt.wantRevenue {
				t.Errorf("TotalRevenue = %d, want %d", got.TotalRevenue, tt.wantRevenue)
			}
			if got.TotalCOGS != tt.wantCOGS {
				t.Errorf("TotalCOGS = %d, want %d", got.TotalCOGS, tt.wantCOGS)
			}
			if got.GrossProfit != tt.wantGross {
				t.Errorf("GrossProfit = %d, want %d", got.GrossProfit, tt.wantGross)
			}
			if got.TotalExpenses != tt.wantExpenses {
				t.Errorf("TotalExpenses = %d, want %d", got.TotalExpenses, tt.wantExpenses)
			}
			if got.NetIncome != tt.wantNetIncome {
				t.Errorf("NetIncome = %d, want %d", got.NetIncome, tt.wantNetIncome)
			}

			// Internal consistency: the subtotals must agree with the lines
			// they claim to summarise, and with each other.
			if sum := sumItems(got.Revenue); sum != got.TotalRevenue {
				t.Errorf("revenue lines sum to %d but TotalRevenue = %d", sum, got.TotalRevenue)
			}
			if sum := sumItems(got.COGS); sum != got.TotalCOGS {
				t.Errorf("COGS lines sum to %d but TotalCOGS = %d", sum, got.TotalCOGS)
			}
			if sum := sumItems(got.Expenses); sum != got.TotalExpenses {
				t.Errorf("expense lines sum to %d but TotalExpenses = %d", sum, got.TotalExpenses)
			}
			if got.GrossProfit != got.TotalRevenue-got.TotalCOGS {
				t.Errorf("GrossProfit %d != revenue %d - COGS %d", got.GrossProfit, got.TotalRevenue, got.TotalCOGS)
			}
			if got.NetIncome != got.GrossProfit-got.TotalExpenses {
				t.Errorf("NetIncome %d != gross %d - expenses %d", got.NetIncome, got.GrossProfit, got.TotalExpenses)
			}

			counts := [3]int{len(got.Revenue), len(got.COGS), len(got.Expenses)}
			if counts != tt.wantCounts {
				t.Errorf("section line counts = %v, want %v", counts, tt.wantCounts)
			}

			// Sections are always present, never nil, so the JSON carries
			// `[]` rather than `null` and the UI can map over it.
			if got.Revenue == nil || got.COGS == nil || got.Expenses == nil {
				t.Error("statement sections must serialise as [] rather than null")
			}
			if got.StartDate != "2026-08-01" || got.EndDate != "2026-08-31" {
				t.Errorf("window = %s..%s, want 2026-08-01..2026-08-31", got.StartDate, got.EndDate)
			}
		})
	}
}

func TestAssembleBalanceSheetBalances(t *testing.T) {
	tests := []struct {
		name             string
		postings         []posting
		wantAssets       int64
		wantLiabilities  int64
		wantEquity       int64
		wantRetained     int64
		explainRetained  string
		wantAssetLineOne int64
	}{
		{
			name: "opening capital contribution",
			postings: []posting{
				dr("1010", AccountTypeAsset, "Cash", 5_000_000),
				cr("3010", AccountTypeEquity, "Owner Capital", 5_000_000),
			},
			wantAssets:       5_000_000,
			wantLiabilities:  0,
			wantEquity:       5_000_000,
			wantRetained:     0,
			explainRetained:  "no revenue or expense yet",
			wantAssetLineOne: 5_000_000,
		},
		{
			name: "a trading period folds net income into equity",
			postings: []posting{
				dr("1010", AccountTypeAsset, "Cash", 5_000_000),
				cr("3010", AccountTypeEquity, "Owner Capital", 5_000_000),
				// Buy inventory on credit.
				dr("1300", AccountTypeAsset, "Inventory", 2_000_000),
				cr("2010", AccountTypeLiability, "Accounts Payable", 2_000_000),
				// Sell half of it for 1.5x.
				dr("1020", AccountTypeAsset, "Accounts Receivable", 1_500_000),
				cr("4010", AccountTypeRevenue, "Sales Revenue", 1_500_000),
				cogs("5010", "Cost of Goods Sold", 1_000_000),
				cr("1300", AccountTypeAsset, "Inventory", 1_000_000),
				// Pay rent.
				dr("6010", AccountTypeExpense, "Rent", 200_000),
				cr("1010", AccountTypeAsset, "Cash", 200_000),
			},
			wantAssets:      7_300_000, // cash 4,800,000 + AR 1,500,000 + inventory 1,000,000
			wantLiabilities: 2_000_000, // accounts payable
			wantEquity:      5_300_000, // capital 5,000,000 + retained 300,000
			wantRetained:    300_000,   // revenue 1,500,000 - COGS 1,000,000 - rent 200,000
			explainRetained: "revenue less all expenses, inception to date",
		},
		{
			name: "a contra-asset carrying a credit balance still balances",
			postings: []posting{
				dr("1010", AccountTypeAsset, "Cash", 1_000_000),
				cr("3010", AccountTypeEquity, "Owner Capital", 1_000_000),
				// Depreciation: the contra-asset account goes credit-side.
				dr("6020", AccountTypeExpense, "Depreciation", 100_000),
				cr("1590", AccountTypeAsset, "Accumulated Depreciation", 100_000),
			},
			wantAssets:      900_000, // 1,000,000 - 100,000
			wantLiabilities: 0,
			wantEquity:      900_000, // capital 1,000,000 + retained (100,000)
			wantRetained:    -100_000,
			explainRetained: "an expense with no revenue is a loss",
		},
		{
			name: "an overdrawn bank account reports as a negative asset",
			postings: []posting{
				dr("6010", AccountTypeExpense, "Rent", 250_000),
				cr("1010", AccountTypeAsset, "Cash", 250_000),
			},
			wantAssets:      -250_000,
			wantLiabilities: 0,
			wantEquity:      -250_000,
			wantRetained:    -250_000,
			explainRetained: "spending with no capital and no revenue",
		},
		{
			name:            "an empty ledger balances at zero",
			postings:        nil,
			wantAssets:      0,
			wantLiabilities: 0,
			wantEquity:      0,
			wantRetained:    0,
			explainRetained: "nothing posted",
		},
		{
			name: "a nine-figure ledger stays exact in int64 cents",
			postings: []posting{
				dr("1010", AccountTypeAsset, "Cash", 987_654_321_00),
				cr("3010", AccountTypeEquity, "Owner Capital", 987_654_321_00),
			},
			wantAssets:      987_654_321_00,
			wantLiabilities: 0,
			wantEquity:      987_654_321_00,
			wantRetained:    0,
			explainRetained: "no trading activity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			position, earnings := split(ledger(t, tt.postings...))
			got := assembleBalanceSheet("2026-08-31", position, earnings)

			// The identity. Everything else on this statement is detail.
			if got.TotalAssets != got.TotalLiabilities+got.TotalEquity {
				t.Errorf("balance sheet does not balance: assets %d != liabilities %d + equity %d (out by %d)",
					got.TotalAssets, got.TotalLiabilities, got.TotalEquity,
					got.TotalAssets-got.TotalLiabilities-got.TotalEquity)
			}

			if got.TotalAssets != tt.wantAssets {
				t.Errorf("TotalAssets = %d, want %d", got.TotalAssets, tt.wantAssets)
			}
			if got.TotalLiabilities != tt.wantLiabilities {
				t.Errorf("TotalLiabilities = %d, want %d", got.TotalLiabilities, tt.wantLiabilities)
			}
			if got.TotalEquity != tt.wantEquity {
				t.Errorf("TotalEquity = %d, want %d", got.TotalEquity, tt.wantEquity)
			}
			if got.RetainedEarnings != tt.wantRetained {
				t.Errorf("RetainedEarnings = %d, want %d (%s)", got.RetainedEarnings, tt.wantRetained, tt.explainRetained)
			}

			// Retained earnings is included in total equity exactly once:
			// the equity account lines plus retained earnings must be the
			// total the UI prints under them.
			if sum := sumItems(got.Equity) + got.RetainedEarnings; sum != got.TotalEquity {
				t.Errorf("equity lines %d + retained %d = %d, but TotalEquity = %d (double count or omission)",
					sumItems(got.Equity), got.RetainedEarnings, sum, got.TotalEquity)
			}
			if sum := sumItems(got.Assets); sum != got.TotalAssets {
				t.Errorf("asset lines sum to %d but TotalAssets = %d", sum, got.TotalAssets)
			}
			if sum := sumItems(got.Liabilities); sum != got.TotalLiabilities {
				t.Errorf("liability lines sum to %d but TotalLiabilities = %d", sum, got.TotalLiabilities)
			}

			if got.Assets == nil || got.Liabilities == nil || got.Equity == nil {
				t.Error("statement sections must serialise as [] rather than null")
			}
			if got.AsOfDate != "2026-08-31" {
				t.Errorf("AsOfDate = %s, want 2026-08-31", got.AsOfDate)
			}
		})
	}
}

// TestNetIncomeAgreesWithRetainedEarnings is the cross-statement invariant: a
// P&L covering the whole life of the ledger must report exactly the retained
// earnings the balance sheet carries as of the same date. If these two drift,
// one of the statements is lying about the same underlying journal.
func TestNetIncomeAgreesWithRetainedEarnings(t *testing.T) {
	tests := []struct {
		name     string
		postings []posting
	}{
		{
			name: "profit",
			postings: []posting{
				dr("1010", AccountTypeAsset, "Cash", 2_000_000),
				cr("3010", AccountTypeEquity, "Owner Capital", 2_000_000),
				dr("1020", AccountTypeAsset, "Accounts Receivable", 750_000),
				cr("4010", AccountTypeRevenue, "Sales Revenue", 750_000),
				cogs("5010", "Cost of Goods Sold", 400_000),
				cr("1300", AccountTypeAsset, "Inventory", 400_000),
				dr("6010", AccountTypeExpense, "Rent", 125_000),
				cr("1010", AccountTypeAsset, "Cash", 125_000),
			},
		},
		{
			name: "loss",
			postings: []posting{
				dr("1010", AccountTypeAsset, "Cash", 500_000),
				cr("3010", AccountTypeEquity, "Owner Capital", 500_000),
				dr("6010", AccountTypeExpense, "Rent", 300_000),
				cr("1010", AccountTypeAsset, "Cash", 300_000),
			},
		},
		{
			name: "COGS only, no operating expense",
			postings: []posting{
				dr("1020", AccountTypeAsset, "Accounts Receivable", 90_000),
				cr("4010", AccountTypeRevenue, "Sales Revenue", 90_000),
				cogs("5010", "Cost of Goods Sold", 90_000),
				cr("1300", AccountTypeAsset, "Inventory", 90_000),
			},
		},
		{
			name: "odd cents that do not divide evenly",
			postings: []posting{
				dr("1020", AccountTypeAsset, "Accounts Receivable", 33_333),
				cr("4010", AccountTypeRevenue, "Sales Revenue", 33_333),
				cogs("5010", "Cost of Goods Sold", 11_111),
				cr("1300", AccountTypeAsset, "Inventory", 11_111),
				dr("6010", AccountTypeExpense, "Rent", 7),
				cr("1010", AccountTypeAsset, "Cash", 7),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := ledger(t, tt.postings...)
			position, earnings := split(rows)

			pl := assembleProfitAndLoss("2000-01-01", "2026-08-31", earnings)
			bs := assembleBalanceSheet("2026-08-31", position, earnings)

			if pl.NetIncome != bs.RetainedEarnings {
				t.Errorf("P&L net income %d != balance sheet retained earnings %d",
					pl.NetIncome, bs.RetainedEarnings)
			}
			if bs.TotalAssets != bs.TotalLiabilities+bs.TotalEquity {
				t.Errorf("balance sheet does not balance: %d != %d + %d",
					bs.TotalAssets, bs.TotalLiabilities, bs.TotalEquity)
			}
		})
	}
}

// TestBalanceSheetExactAtNegativeCentBoundaries guards the rounding hazard the
// upstream implementation of these statements shipped with. That version
// scanned the DECIMAL dollar columns into float64 and converted with
// `int64(x*100.0 + 0.5)`. Because Go truncates float→int toward zero, that
// idiom rounds half *up* rather than half away from zero, so every negative
// balance came out one cent short: -$100.00 became -9999 cents. One
// contra-asset was enough to knock the sheet out of balance and light up the
// red "Unbalanced" banner.
//
// The conversion now happens in SQL as exact numeric, so no float is involved.
// These fixtures pin the boundary anyway: a contra account at an exact dollar,
// and a lopsided count of negative balances on each side, so a reintroduced
// off-by-one-cent would break the identity here rather than in production.
func TestBalanceSheetExactAtNegativeCentBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		postings   []posting
		wantAssets int64
	}{
		{
			name: "single contra-asset at an exact dollar",
			postings: []posting{
				dr("1010", AccountTypeAsset, "Cash", 1_000_000),
				cr("3010", AccountTypeEquity, "Owner Capital", 1_000_000),
				dr("6020", AccountTypeExpense, "Depreciation", 10_000),
				cr("1590", AccountTypeAsset, "Accumulated Depreciation", 10_000),
			},
			wantAssets: 990_000,
		},
		{
			name: "three negative balances on the asset side, one on the equity side",
			postings: []posting{
				dr("1010", AccountTypeAsset, "Cash", 5_000_000),
				cr("3010", AccountTypeEquity, "Owner Capital", 5_000_000),
				dr("6020", AccountTypeExpense, "Depreciation", 30_000),
				cr("1590", AccountTypeAsset, "Accum Depr - Buildings", 10_000),
				cr("1591", AccountTypeAsset, "Accum Depr - Vehicles", 10_000),
				cr("1592", AccountTypeAsset, "Accum Depr - Equipment", 10_000),
				// A treasury-stock style debit-balance equity account.
				dr("3020", AccountTypeEquity, "Owner Draws", 40_000),
				cr("1010", AccountTypeAsset, "Cash", 40_000),
			},
			wantAssets: 4_930_000, // 5,000,000 - 40,000 - 30,000
		},
		{
			name: "a one-cent contra balance",
			postings: []posting{
				dr("1010", AccountTypeAsset, "Cash", 100),
				cr("3010", AccountTypeEquity, "Owner Capital", 100),
				dr("6020", AccountTypeExpense, "Depreciation", 1),
				cr("1590", AccountTypeAsset, "Accumulated Depreciation", 1),
			},
			wantAssets: 99,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			position, earnings := split(ledger(t, tt.postings...))
			bs := assembleBalanceSheet("2026-08-31", position, earnings)

			if bs.TotalAssets != tt.wantAssets {
				t.Errorf("TotalAssets = %d, want %d", bs.TotalAssets, tt.wantAssets)
			}
			if diff := bs.TotalAssets - bs.TotalLiabilities - bs.TotalEquity; diff != 0 {
				t.Errorf("balance sheet out by %d cents", diff)
			}

			// And no individual line may be off by a cent from its activity.
			for _, item := range bs.Assets {
				for _, row := range position {
					if row.AccountCode == item.AccountCode && item.Amount != row.Debit-row.Credit {
						t.Errorf("account %s: line amount %d != debit %d - credit %d",
							item.AccountCode, item.Amount, row.Debit, row.Credit)
					}
				}
			}
		})
	}
}

// --- Service layer: argument plumbing and failure paths ---

func TestServiceGetBalanceSheetQueriesBothHalvesInceptionToDate(t *testing.T) {
	repo := &MockRepository{
		activity: ledger(t,
			dr("1010", AccountTypeAsset, "Cash", 300_000),
			cr("4010", AccountTypeRevenue, "Sales Revenue", 300_000),
		),
	}
	svc := NewService(repo, nil, nil)

	report, err := svc.GetBalanceSheet(context.Background(), "2026-08-31")
	if err != nil {
		t.Fatalf("GetBalanceSheet: %v", err)
	}

	if len(repo.activityCalls) != 2 {
		t.Fatalf("expected 2 repository calls (position, earnings), got %d", len(repo.activityCalls))
	}
	for i, call := range repo.activityCalls {
		if call.start != nil {
			t.Errorf("call %d: start = %v, want nil — a balance sheet is inception-to-date", i, *call.start)
		}
		want := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
		if !call.end.Equal(want) {
			t.Errorf("call %d: end = %v, want %v", i, call.end, want)
		}
	}
	if got := repo.activityCalls[0].types; len(got) != 3 {
		t.Errorf("position call asked for types %v, want the three permanent types", got)
	}
	if got := repo.activityCalls[1].types; len(got) != 2 {
		t.Errorf("earnings call asked for types %v, want revenue and expense", got)
	}

	if report.TotalAssets != 300_000 || report.RetainedEarnings != 300_000 {
		t.Errorf("assets %d / retained %d, want 300000 / 300000", report.TotalAssets, report.RetainedEarnings)
	}
	if report.TotalAssets != report.TotalLiabilities+report.TotalEquity {
		t.Error("service-assembled balance sheet does not balance")
	}
}

func TestServiceGetProfitAndLossBoundsTheWindow(t *testing.T) {
	repo := &MockRepository{
		activity: ledger(t,
			dr("1010", AccountTypeAsset, "Cash", 100_000),
			cr("4010", AccountTypeRevenue, "Sales Revenue", 100_000),
		),
	}
	svc := NewService(repo, nil, nil)

	if _, err := svc.GetProfitAndLoss(context.Background(), "2026-08-01", "2026-08-31"); err != nil {
		t.Fatalf("GetProfitAndLoss: %v", err)
	}

	if len(repo.activityCalls) != 1 {
		t.Fatalf("expected 1 repository call, got %d", len(repo.activityCalls))
	}
	call := repo.activityCalls[0]
	if call.start == nil {
		t.Fatal("start = nil, but an income statement is bounded below")
	}
	wantStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	if !call.start.Equal(wantStart) {
		t.Errorf("start = %v, want %v", *call.start, wantStart)
	}
	if !call.end.Equal(wantEnd) {
		t.Errorf("end = %v, want %v", call.end, wantEnd)
	}
	for _, typ := range call.types {
		if typ != AccountTypeRevenue && typ != AccountTypeExpense {
			t.Errorf("P&L asked for %s, which is not a temporary account type", typ)
		}
	}
}

func TestServiceStatementDateValidation(t *testing.T) {
	svc := NewService(&MockRepository{}, nil, nil)
	ctx := context.Background()

	t.Run("malformed P&L start", func(t *testing.T) {
		if _, err := svc.GetProfitAndLoss(ctx, "08/01/2026", "2026-08-31"); err == nil {
			t.Error("expected an error for a non-ISO start date")
		}
	})
	t.Run("malformed P&L end", func(t *testing.T) {
		if _, err := svc.GetProfitAndLoss(ctx, "2026-08-01", "not-a-date"); err == nil {
			t.Error("expected an error for a non-ISO end date")
		}
	})
	t.Run("inverted P&L window", func(t *testing.T) {
		if _, err := svc.GetProfitAndLoss(ctx, "2026-08-31", "2026-08-01"); err == nil {
			t.Error("expected an error when end precedes start")
		}
	})
	t.Run("malformed balance sheet date", func(t *testing.T) {
		if _, err := svc.GetBalanceSheet(ctx, "31-08-2026"); err == nil {
			t.Error("expected an error for a non-ISO as_of date")
		}
	})
	t.Run("a single-day window is valid", func(t *testing.T) {
		if _, err := svc.GetProfitAndLoss(ctx, "2026-08-31", "2026-08-31"); err != nil {
			t.Errorf("a one-day P&L should be allowed: %v", err)
		}
	})
}

func TestServiceStatementRepositoryFailurePropagates(t *testing.T) {
	sentinel := errors.New("connection refused")
	ctx := context.Background()

	t.Run("profit and loss", func(t *testing.T) {
		svc := NewService(&MockRepository{activityErr: sentinel}, nil, nil)
		_, err := svc.GetProfitAndLoss(ctx, "2026-08-01", "2026-08-31")
		if !errors.Is(err, sentinel) {
			t.Errorf("err = %v, want it to wrap %v — a failed query must not read as an empty statement", err, sentinel)
		}
	})

	t.Run("balance sheet", func(t *testing.T) {
		svc := NewService(&MockRepository{activityErr: sentinel}, nil, nil)
		_, err := svc.GetBalanceSheet(ctx, "2026-08-31")
		if !errors.Is(err, sentinel) {
			t.Errorf("err = %v, want it to wrap %v", err, sentinel)
		}
	})
}

func sumItems(items []AccountLineItem) int64 {
	var total int64
	for _, i := range items {
		total += i.Amount
	}
	return total
}
