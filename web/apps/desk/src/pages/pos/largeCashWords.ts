// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

// The large cash worklist's words (CP-17b): what a row says and what the
// manager may do with it. The words live here so they can be argued about
// without a browser, the way the counter's terminal.ts keeps its own.

import type { LargeCashStatement, LargeCashWorklistRow } from '../../types/largecash';

/** What a row says about its deadline. */
export function deadlineWords(row: LargeCashWorklistRow): string {
    if (!row.filing_deadline) {
        // Nothing is owed to the IRS for this row, so there is no deadline.
        return 'No filing deadline: nothing is owed to the IRS for this one.';
    }
    const deadline = new Date(row.filing_deadline);
    const day = deadline.toISOString().slice(0, 10);
    if (row.past_deadline) {
        return `The filing was due ${day} and is past. Chase it.`;
    }
    return `The filing is due ${day}.`;
}

/** What the manager may do with a row, and the button that does it. */
export function actionWords(row: LargeCashWorklistRow): { say: string; button: string; canFile: boolean; canStatement: boolean } {
    switch (row.recommended_action) {
        case 'file':
            return {
                say: 'File the Form 8300 for this crossing, then record the filing.',
                button: 'Record the filing',
                canFile: true,
                canStatement: false,
            };
        case 'chase_filing':
            return {
                say: 'The filing is past its deadline. Chase it, then record the filing.',
                button: 'Record the filing',
                canFile: true,
                canStatement: false,
            };
        case 'issue_statement':
            return {
                say: 'The filing is done. Issue the customer the statement for the year.',
                button: '',
                canFile: false,
                canStatement: true,
            };
        case 'capture_payer_details':
            return {
                say: "The payer's details are missing, so the form cannot be completed. The counter asks again at the till.",
                button: '',
                canFile: false,
                canStatement: false,
            };
        case 'manager_review':
            return {
                say: 'A manager looks at this one. The walk-in would not say who they were, so there is no filing to make.',
                button: '',
                canFile: false,
                canStatement: false,
            };
        case 'dealer_record':
            return {
                say: "This crossing is the dealer's own record. It is not a report, so there is no filing to make.",
                button: '',
                canFile: false,
                canStatement: false,
            };
        default:
            return {
                say: 'Nothing is owed on this one.',
                button: '',
                canFile: false,
                canStatement: false,
            };
    }
}

/** An amount in cents in the row's own currency, as the statement prints it. */
export function moneyWords(cents: number, currency: string): string {
    const code = currency && /^[A-Za-z]{3}$/.test(currency) ? currency.toUpperCase() : 'USD';
    return new Intl.NumberFormat('en-US', { style: 'currency', currency: code }).format(cents / 100);
}

/** The day a statement date falls on, as the statement prints it. */
function dayWords(iso?: string | null): string {
    return iso ? iso.slice(0, 10) : '';
}

/**
 * The customer statement, line by line, as the sheet prints it: the dealer's
 * information contact, the addressee, the year's aggregate and the sentence
 * that says the information was furnished to the IRS. It never says the
 * dealer noted anything suspicious.
 */
export function statementLines(s: LargeCashStatement): { label: string; value: string }[] {
    return [
        { label: 'From', value: s.business_name },
        { label: 'Address', value: s.business_address },
        { label: 'Contact', value: s.contact_name },
        { label: 'Telephone', value: s.telephone },
        { label: 'To', value: s.payer_name },
        { label: 'Payer address', value: s.payer_address },
        { label: `Cash received in ${s.year}`, value: moneyWords(s.aggregate_cents, s.currency) },
        { label: 'Receipts', value: `${s.crossing_count} large cash receipt${s.crossing_count === 1 ? '' : 's'}` },
        { label: 'Issued', value: s.state === 'issued' ? dayWords(s.issued_date) : 'Not issued yet' },
    ].filter((line) => line.value !== '');
}
