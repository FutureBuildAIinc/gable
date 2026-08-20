// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl

import (
	"time"

	"github.com/google/uuid"
)

// Account types
const (
	AccountTypeAsset     = "ASSET"
	AccountTypeLiability = "LIABILITY"
	AccountTypeEquity    = "EQUITY"
	AccountTypeRevenue   = "REVENUE"
	AccountTypeExpense   = "EXPENSE"
)

// Normal balance directions
const (
	NormalDebit  = "DEBIT"
	NormalCredit = "CREDIT"
)

// Journal entry sources
const (
	SourceManual     = "MANUAL"
	SourceInvoice    = "INVOICE"
	SourcePayment    = "PAYMENT"
	SourceAdjustment = "ADJUSTMENT"
	SourceClosing    = "CLOSING"
	SourceVendorInv  = "VENDOR_INVOICE"
	SourceVendorPmt  = "VENDOR_PAYMENT"
)

// Journal entry statuses
const (
	StatusDraft  = "DRAFT"
	StatusPosted = "POSTED"
	StatusVoid   = "VOID"
)

// Fiscal period statuses
const (
	PeriodOpen   = "OPEN"
	PeriodClosed = "CLOSED"
)

// GLAccount represents a single account in the Chart of Accounts.
type GLAccount struct {
	ID            uuid.UUID  `json:"id"`
	Code          string     `json:"code"`
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	Subtype       string     `json:"subtype"`
	ParentID      *uuid.UUID `json:"parent_id,omitempty"`
	NormalBalance string     `json:"normal_balance"`
	IsActive      bool       `json:"is_active"`
	Description   string     `json:"description"`
	Balance       int64      `json:"balance"` // Computed, in cents
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// JournalEntry represents a double-entry journal entry header.
type JournalEntry struct {
	ID              uuid.UUID     `json:"id"`
	EntryNumber     int           `json:"entry_number"`
	EntryDate       time.Time     `json:"entry_date"`
	Memo            string        `json:"memo"`
	Source          string        `json:"source"`
	SourceRefID     *uuid.UUID    `json:"source_ref_id,omitempty"`
	Status          string        `json:"status"`
	PostedBy        string        `json:"posted_by"`
	ReversesEntryID *uuid.UUID    `json:"reverses_entry_id,omitempty"` // set on a reversal entry
	TotalDebit      int64         `json:"total_debit"`                 // Computed, cents
	TotalCredit     int64         `json:"total_credit"`                // Computed, cents
	Lines           []JournalLine `json:"lines,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

// JournalLine represents a single debit or credit line in a journal entry.
type JournalLine struct {
	ID          uuid.UUID `json:"id"`
	EntryID     uuid.UUID `json:"journal_entry_id"`
	AccountID   uuid.UUID `json:"account_id"`
	AccountCode string    `json:"account_code,omitempty"`
	AccountName string    `json:"account_name,omitempty"`
	Description string    `json:"description"`
	Debit       int64     `json:"debit"`  // Cents
	Credit      int64     `json:"credit"` // Cents
}

// FiscalPeriod represents an accounting period that can be opened or closed.
type FiscalPeriod struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	StartDate time.Time  `json:"start_date"`
	EndDate   time.Time  `json:"end_date"`
	Status    string     `json:"status"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	ClosedBy  string     `json:"closed_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// TrialBalanceRow is a single row in the trial balance report.
type TrialBalanceRow struct {
	AccountID   uuid.UUID `json:"account_id"`
	AccountCode string    `json:"account_code"`
	AccountName string    `json:"account_name"`
	AccountType string    `json:"account_type"`
	Debit       int64     `json:"debit"`  // Cents
	Credit      int64     `json:"credit"` // Cents
}

// --- Financial Statements ---

// COGSSubtype is the gl_accounts.subtype that marks an EXPENSE account as cost
// of goods sold rather than operating expense. There is no COGS account *type*
// (migration 025 constrains type to the five classical ones), so the subtype is
// what separates the two halves of the income statement.
//
// Matching on subtype rather than on an account-code prefix is deliberate. The
// seeded chart of accounts makes a prefix rule wrong on its face: of the three
// 5xxx expense accounts, only 5010 is COGS — 5020 "Operating Expenses"
// (migration 025) and 5030 "Cash Over/Short" (migration 076) are both subtype
// 'Operating'. A "codes starting 50 are COGS" rule files two operating expense
// accounts above the gross profit line, understating gross margin and leaving
// the operating expense section near-empty.
//
// An expense account with no subtype set falls to operating expense, which is
// the conservative default: it sits below the gross profit line rather than
// flattering it.
const COGSSubtype = "COGS"

// AccountActivity is the posted debit/credit activity for a single account
// over a date window, in cents.
//
// Both figures are non-negative. They are sums of the `debit` and `credit`
// columns, which migration 025 constrains to be one-sided and positive
// (chk_debit_or_credit), so a "negative debit" cannot exist in the ledger.
// Which direction increases an account is a property of its type, applied
// during statement assembly — the ledger itself stores no signed amounts.
type AccountActivity struct {
	AccountID      uuid.UUID
	AccountCode    string
	AccountName    string
	AccountType    string
	AccountSubtype string
	Debit          int64 // Cents
	Credit         int64 // Cents
}

// AccountLineItem is one account's signed balance on a financial statement.
type AccountLineItem struct {
	AccountID   string `json:"account_id"`
	AccountCode string `json:"account_code"`
	AccountName string `json:"account_name"`
	Amount      int64  `json:"amount"` // Cents
}

// ProfitAndLossReport is an income statement covering [StartDate, EndDate].
type ProfitAndLossReport struct {
	StartDate     string            `json:"start_date"`
	EndDate       string            `json:"end_date"`
	Revenue       []AccountLineItem `json:"revenue"`
	COGS          []AccountLineItem `json:"cogs"`
	Expenses      []AccountLineItem `json:"expenses"`
	TotalRevenue  int64             `json:"total_revenue"`  // Cents
	TotalCOGS     int64             `json:"total_cogs"`     // Cents
	GrossProfit   int64             `json:"gross_profit"`   // Cents
	TotalExpenses int64             `json:"total_expenses"` // Cents, excludes COGS
	NetIncome     int64             `json:"net_income"`     // Cents
}

// BalanceSheetReport is a statement of financial position as of a date.
//
// RetainedEarnings is inception-to-date net income and is *already included*
// in TotalEquity; it is reported separately because it has no account of its
// own in the chart of accounts (nothing closes revenue and expense into an
// equity account, so it has to be derived on the fly). Rendering it as a line
// under the equity accounts and then showing TotalEquity is therefore correct
// and does not double-count.
type BalanceSheetReport struct {
	AsOfDate         string            `json:"as_of_date"`
	Assets           []AccountLineItem `json:"assets"`
	Liabilities      []AccountLineItem `json:"liabilities"`
	Equity           []AccountLineItem `json:"equity"`
	TotalAssets      int64             `json:"total_assets"`      // Cents
	TotalLiabilities int64             `json:"total_liabilities"` // Cents
	TotalEquity      int64             `json:"total_equity"`      // Cents, includes RetainedEarnings
	RetainedEarnings int64             `json:"retained_earnings"` // Cents
}
