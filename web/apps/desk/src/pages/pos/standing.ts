// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's account standing, in words: what the cashier reads beside a
// customer, and why the charge to the account is or is not offered. Pure
// functions of the wire figures (integer cents), so the words are tested
// without the screen.

import type { CounterCustomer } from '../../types/pos';

export type Tone = 'ok' | 'warn' | 'stop';

export interface Standing {
    tone: Tone;
    text: string;
}

/** Whole dollars and cents from integer cents, no float arithmetic on the money. */
export function money(cents: number): string {
    const sign = cents < 0 ? '-' : '';
    const abs = Math.abs(cents);
    const whole = Math.floor(abs / 100).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
    return `${sign}$${whole}.${(abs % 100).toString().padStart(2, '0')}`;
}

/** The account's standing as the charge tender will read it. */
export function standingOf(c: CounterCustomer): Standing {
    switch (c.standing) {
        case 'no_credit':
            return { tone: 'stop', text: 'On hold: this account has no credit' };
        case 'over_limit':
            return {
                tone: 'stop',
                text: `Over the credit limit by ${money(Math.abs(c.available_credit_cents ?? 0))}`,
            };
        case 'no_limit':
            return { tone: 'ok', text: 'No credit limit on this account' };
        default:
            return {
                tone: 'ok',
                text: `Credit available ${money(c.available_credit_cents ?? 0)} of ${money(c.credit_limit_cents ?? 0)}`,
            };
    }
}

/** The words for a charge refused only for want of the purchase order. */
export const PO_REFUSAL = 'This account asks for a purchase order: enter it before charging';

/**
 * Why a charge of totalCents to this account is refused at the counter, in
 * words, or null when it is offered. The server holds the same rules (409
 * credit_limit, 409 po_required); this only keeps the cashier from ringing a
 * charge the account will refuse.
 */
export function chargeRefusal(
    customer: CounterCustomer | null,
    totalCents: number,
    purchaseOrder: string,
): string | null {
    if (!customer) return 'Name a customer to charge the sale to an account';
    if (customer.standing === 'no_credit') return 'On hold: this account has no credit';
    if (customer.standing === 'over_limit') return standingOf(customer).text;
    if (customer.standing === 'good' && customer.available_credit_cents !== null
        && totalCents > customer.available_credit_cents) {
        return `This sale is ${money(totalCents - customer.available_credit_cents)} over the available credit`;
    }
    if (customer.po_required && purchaseOrder.trim() === '') {
        return PO_REFUSAL;
    }
    return null;
}
