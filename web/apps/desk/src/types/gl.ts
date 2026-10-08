// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// General Ledger types

export interface GLAccount {
    id: string;
    code: string;
    name: string;
    type: 'ASSET' | 'LIABILITY' | 'EQUITY' | 'REVENUE' | 'EXPENSE';
    subtype: string;
    parent_id?: string;
    normal_balance: 'DEBIT' | 'CREDIT';
    is_active: boolean;
    description: string;
    balance: number; // Cents
    created_at: string;
    updated_at: string;
}

export interface JournalEntry {
    id: string;
    entry_number: number;
    entry_date: string;
    memo: string;
    source: 'MANUAL' | 'INVOICE' | 'PAYMENT' | 'ADJUSTMENT' | 'CLOSING';
    source_ref_id?: string;
    status: 'DRAFT' | 'POSTED' | 'VOID';
    posted_by: string;
    total_debit: number;  // Cents
    total_credit: number; // Cents
    lines?: JournalLine[];
    created_at: string;
    updated_at: string;
}

export interface JournalLine {
    id: string;
    journal_entry_id: string;
    account_id: string;
    account_code?: string;
    account_name?: string;
    description: string;
    debit: number;  // Cents
    credit: number; // Cents
}

export interface FiscalPeriod {
    id: string;
    name: string;
    start_date: string;
    end_date: string;
    status: 'OPEN' | 'CLOSED';
    closed_at?: string;
    closed_by?: string;
    created_at: string;
}

export interface TrialBalanceRow {
    account_id: string;
    account_code: string;
    account_name: string;
    account_type: string;
    debit: number;  // Cents
    credit: number; // Cents
}

export interface CreateAccountRequest {
    code: string;
    name: string;
    type: string;
    subtype: string;
    parent_id?: string;
    normal_balance: string;
    description: string;
}

// Financial statement types

/** One account's signed balance on a financial statement. */
export interface AccountLineItem {
    account_id: string;
    account_code: string;
    account_name: string;
    amount: number; // Cents
}

/** Income statement for the window [start_date, end_date]. */
export interface ProfitAndLossReport {
    start_date: string;
    end_date: string;
    revenue: AccountLineItem[];
    cogs: AccountLineItem[];
    expenses: AccountLineItem[];
    total_revenue: number;  // Cents
    total_cogs: number;     // Cents
    gross_profit: number;   // Cents
    total_expenses: number; // Cents, excludes COGS
    net_income: number;     // Cents
}

/**
 * Statement of financial position as of a date.
 *
 * `retained_earnings` is inception-to-date net income and is ALREADY included
 * in `total_equity`. It is sent separately only because it has no account of
 * its own in the chart of accounts. Render it as a line beneath the equity
 * accounts and then print `total_equity` — do not add the two together.
 */
export interface BalanceSheetReport {
    as_of_date: string;
    assets: AccountLineItem[];
    liabilities: AccountLineItem[];
    equity: AccountLineItem[];
    total_assets: number;      // Cents
    total_liabilities: number; // Cents
    total_equity: number;      // Cents, includes retained_earnings
    retained_earnings: number; // Cents
}

export interface CreateJournalEntryRequest {
    entry_date: string;
    memo: string;
    lines: {
        account_id: string;
        description: string;
        debit: number;
        credit: number;
    }[];
}
