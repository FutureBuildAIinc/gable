// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's account standing, in words: what the cashier reads beside a
// customer, and why the charge to the account is or is not offered. Pure
// functions of the wire figures (integer cents), so the words are tested
// without the screen.

import type { CounterCustomer, CounterStanding, WayForward, TenderMethod } from '../../types/pos';

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

/** The words for a charge refused only for want of the job. */
export const JOB_REFUSAL = 'This account requires a job on every charge sale: pick the job';

/** The words when the requirement cannot be met at the counter at all. */
export const JOB_NONE_ON_FILE = 'This account requires a job and has none on file: add one on the account page';

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
    approved = false,
    jobId = '',
): string | null {
    if (!customer) return 'Name a customer to charge the sale to an account';
    // a manager's approval of this sale waives the credit words, never the PO
    if (!approved) {
        if (customer.standing === 'no_credit') return 'On hold: this account has no credit';
        if (customer.standing === 'over_limit') return standingOf(customer).text;
        if (customer.standing === 'good' && customer.available_credit_cents !== null
            && totalCents > customer.available_credit_cents) {
            return `This sale is ${money(totalCents - customer.available_credit_cents)} over the available credit`;
        }
    }
    if (customer.po_required && purchaseOrder.trim() === '') {
        return PO_REFUSAL;
    }
    // the job rule is the account tender's too, with its own words when the
    // account holds no job to pick
    if (customer.job_required && jobId === '') {
        return customer.jobs.length === 0 ? JOB_NONE_ON_FILE : JOB_REFUSAL;
    }
    return null;
}

// --- Can this sale go on account? The server's verdict, in words and actions ---

/** The verdict line the counter shows beside the account: whether the sale can go on account, and why. */
export function verdictLine(s: CounterStanding): Standing {
    const v = s.on_account;
    if (v.verdict === 'blocked') return { tone: 'stop', text: v.message };
    if (v.reason === 'manager_override') return { tone: 'warn', text: v.message };
    return { tone: 'ok', text: 'This sale can go on the account' };
}

/** What the cashier may do about a blocked verdict: only the ways that are available. */
export function waysForward(s: CounterStanding | null): WayForward[] {
    if (s === null || s.on_account.verdict !== 'blocked') return [];
    return s.on_account.ways_forward.filter((w) => w.available);
}

/** The label of a way forward as the button says it. */
export function wayLabel(w: WayForward): string {
    switch (w.kind) {
        case 'pay_down':
            return w.amount_cents !== undefined ? `Take ${money(w.amount_cents)} on the account` : 'Take a payment on the account';
        case 'manager_override':
            return 'Ask a manager to approve';
        default:
            return 'Pay another way';
    }
}

/** Why a sale on account is refused for want of a named buyer or past the buyer's cap, or null. */
export function buyerRefusal(s: CounterStanding | null, buyerId: string, accountCents: number): string | null {
    if (s === null) return null;
    if (buyerId === '') {
        return s.named_buyer_required
            ? `${s.name} asks for a named buyer: pick one from the account's roster`
            : null;
    }
    const buyer = s.buyers.find((b) => b.id === buyerId);
    if (!buyer) return `That buyer is not on ${s.name}'s roster: pick one from the list`;
    if (buyer.cap_cents !== null && accountCents > buyer.cap_cents) {
        return `${buyer.name} may put at most ${money(buyer.cap_cents)} on the account in one sale`;
    }
    return null;
}

/** Why a check of amountCents is refused for the customer's check limit, or null. */
export function checkRefusal(s: CounterStanding | null, limitFallbackCents: number | null, amountCents: number): string | null {
    const limit = s !== null && s.check_limit_cents !== null ? s.check_limit_cents : limitFallbackCents;
    if (limit === null || amountCents <= limit) return null;
    return `A check of ${money(amountCents)} is over the check limit of ${money(limit)}: ask a manager to approve it`;
}

/** The store credit a sale may take: what the account holds, up to what is owed. */
export function storeCreditOffer(s: CounterStanding | null, owedCents: number): number {
    if (s === null || s.open_credit_memo_cents <= 0 || owedCents <= 0) return 0;
    return Math.min(s.open_credit_memo_cents, owedCents);
}

/** The tender buttons the counter shows: the four it always had, and store credit while the account holds any. */
export function tenderMethods(s: CounterStanding | null): TenderMethod[] {
    const base: TenderMethod[] = ['cash', 'check', 'card', 'account'];
    return s !== null && s.open_credit_memo_cents > 0 ? [...base, 'store_credit'] : base;
}

/** The ID kinds a check or charge may record, as the select lists them. */
export const idRefusal = (checked: boolean, type: string): string | null =>
    checked && type === '' ? 'Pick the kind of ID that was checked' : null;
