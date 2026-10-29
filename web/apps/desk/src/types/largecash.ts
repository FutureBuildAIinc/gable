// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

// The large cash worklist on the wire (CP-17b): the manager's queue of the
// crossings the dealer owes a filing on, the filing itself, the Form 8300
// data set the filer types into BSA E-Filing, and the customer statement for
// the year's large cash receipts.
//
// The worklist row never carries the payer's taxpayer identification number.
// The Form 8300 export is the one surface that does.

/** How a crossing stands with the IRS: nothing owed, a live duty, done, or voluntary. */
export type LargeCashFilingState = 'not_reportable' | 'reportable' | 'filed' | 'voluntary';

/** What the manager does next with a row. */
export type LargeCashAction =
    | 'capture_payer_details'
    | 'file'
    | 'chase_filing'
    | 'issue_statement'
    | 'manager_review'
    | 'dealer_record';

/** One crossing on the manager's worklist. */
export interface LargeCashWorklistRow {
    event_id: string;
    payer_key: string;
    payer_name: string;
    customer_id?: string | null;
    customer_account?: string;
    branch_id: string;
    branch_name: string;
    branch_address: string;
    contributed_branch_ids: string[];
    currency: string;
    total_cents: number;
    rule_family: string;
    threshold_cents: number;
    transaction_count: number;
    tender_count: number;
    first_date?: string | null;
    last_date?: string | null;
    event_state: string;
    filing_state: LargeCashFilingState;
    filing_deadline?: string | null;
    past_deadline: boolean;
    filed_date?: string | null;
    acknowledgement_no?: string;
    statement_state: string;
    statement_issued_date?: string | null;
    payer_details_captured: boolean;
    severity_score: number;
    recommended_action: LargeCashAction;
    note?: string;
    adviser_note: string;
}

/** The manager's queue. */
export interface LargeCashWorklist {
    items: LargeCashWorklistRow[];
    count: number;
    generated_at: string;
}

/** The filing state of one crossing after a manager recorded it. */
export interface LargeCashFiling {
    event_id: string;
    state: string;
    filed_date?: string | null;
    acknowledgement_no?: string;
    recorded_by: string;
    recorded_at: string;
    past_deadline: boolean;
}

/** The data set the filer types into BSA E-Filing. */
export interface LargeCashForm8300 {
    event_id: string;
    form: string;
    currency: string;
    amount_cents: number;
    transaction_count: number;
    tender_count: number;
    first_date: string;
    last_date: string;
    received_at: string;
    filing_deadline: string;
    payer: {
        name: string;
        address: string;
        taxpayer_id: string;
        date_of_birth?: string;
        occupation?: string;
    };
    third_party_payer?: { name: string } | null;
    filer: { branch_id: string; branch_name: string; address: string };
    note: string;
    adviser_note: string;
    taxpayer_id_hint?: string | null;
}

/** One customer's statement for the year's large cash receipts. */
export interface LargeCashStatement {
    payer_key: string;
    year: number;
    payer_name: string;
    business_address: string;
    contact_name: string;
    telephone: string;
    aggregate_cents: number;
    currency: string;
    crossing_count: number;
    first_date?: string | null;
    last_date?: string | null;
    state: string;
    issued_date?: string | null;
    issued_by?: string;
    due_date: string;
    note: string;
    adviser_note: string;
}

/** The year's statements, one per payer. */
export interface LargeCashStatements {
    year: number;
    items: LargeCashStatement[];
    count: number;
}
