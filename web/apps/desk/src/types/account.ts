// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The account reads of the AR core (ADR 0005 sections 9.3 and 10): integer
// cents, the eight subledger row types, the list envelope.

export type TransactionType = 'INVOICE' | 'PAYMENT' | 'ADJUSTMENT' | 'REFUND' | 'CREDIT_MEMO' | 'DISCOUNT' | 'WRITE_OFF' | 'REVERSAL';

export interface CustomerTransaction {
    id: string;
    customer_id: string;
    type: TransactionType;
    amount_cents: number;
    balance_after_cents: number;
    currency: string;
    source_kind: string | null;
    reference_id: string | null;
    description: string;
    created_at: string;
}

export interface CustomerTransactionPage {
    items: CustomerTransaction[];
    next_cursor: string | null;
    limit: number;
}

export interface AccountSummary {
    customer_id: string;
    currency: string;
    balance_cents: number;
    credit_limit_cents: number | null;
    available_credit_cents: number | null;
    unapplied_cents: number;
}

// The AR reads of section 10: aging by customer, job or ship-to, and the
// statement by customer. Integer cents; a group row's coarser fields are null.

export type AgingGroupBy = 'customer' | 'job' | 'ship_to';

export interface ArAgingItem {
    customer_id: string;
    customer_name: string;
    job_id: string | null;
    job_name: string | null;
    ship_to_id: string | null;
    ship_to_code: string | null;
    currency: string;
    current_cents: number;
    days_1_30_cents: number;
    days_31_60_cents: number;
    days_61_90_cents: number;
    over_90_cents: number;
    unapplied_cents: number;
    total_cents: number;
}

export interface ArAgingPage {
    items: ArAgingItem[];
    next_cursor: string | null;
    limit: number;
}

export interface ArAgingTotal {
    currency: string;
    current_cents: number;
    days_1_30_cents: number;
    days_31_60_cents: number;
    days_61_90_cents: number;
    over_90_cents: number;
    unapplied_cents: number;
    total_cents: number;
}

export interface ArAgingSummary {
    as_of: string;
    basis: string;
    totals: ArAgingTotal[];
}

export interface ArStatementLine {
    id: string;
    date: string;
    type: TransactionType;
    description: string;
    amount_cents: number;
    balance_after_cents: number;
    source_kind: string | null;
    reference_id: string | null;
    job_id: string | null;
}

export interface ArOpenDocument {
    kind: 'invoice' | 'credit_memo';
    id: string;
    number: string;
    date: string;
    due_date: string | null;
    job_id: string | null;
    total_cents: number;
    open_cents: number;
}

export interface ArStatementCurrency {
    currency: string;
    opening_balance_cents: number;
    lines: ArStatementLine[];
    closing_balance_cents: number;
    open_documents: ArOpenDocument[];
}

export interface ArStatement {
    customer_id: string;
    customer_name: string;
    from: string;
    to: string;
    job_id: string | null;
    currencies: ArStatementCurrency[];
}
