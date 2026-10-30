// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's account standing, in words: what the cashier reads beside a
// customer, and why the charge to the account is or is not offered. Pure
// functions of the wire figures (integer cents), so the words are tested
// without the screen.

import type { CounterCustomer, CounterRegister, CounterStanding, WayForward, TenderMethod } from '../../types/pos';

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

/**
 * The standing line of an account the AR core holds: the same blocked
 * treatment the zero limit gets, in the hold's own words (the verdict's line
 * beneath it carries the hold's reason), or null when the account is not
 * held by a hold record. Keyed off the server's on_hold flag, not the
 * verdict's reason: a sale over the cap of a live override window is refused
 * with the same reason while the account itself is not held.
 */
export function holdStanding(s: CounterStanding | null): Standing | null {
    if (s === null || !s.on_hold || s.on_account.verdict !== 'blocked' || s.on_account.reason !== 'account_hold') return null;
    return { tone: 'stop', text: 'On credit hold' };
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

/** The server's own refusal of a card where no terminal is wired (CP-05 A1, card_terminal_not_wired). */
export const NO_CARD_TERMINAL = 'This register has no card terminal: take cash, check or the account';

/** Why Card is not offered at the register, or null when it is: a register with no card terminal takes no card. */
export function cardRefusal(register: CounterRegister | null): string | null {
    return register?.card_terminal === 'none' ? NO_CARD_TERMINAL : null;
}

/** The same refusal in a refund's words, beside the return dialog's Card (CP-05 round 3 P3-C). */
export const NO_CARD_TERMINAL_REFUND = 'This register has no card terminal: give it back in cash or as account credit';

/** Why a refund back on the card is not offered at the register, or null when it is. */
export function cardRefundRefusal(register: CounterRegister | null): string | null {
    return register?.card_terminal === 'none' ? NO_CARD_TERMINAL_REFUND : null;
}

/** The counter's plain words beside Card at a register whose card terminal is the simulator (CP-05 round 2). */
export const SIMULATED_CARD_TERMINAL = 'Simulated card terminal: no card is charged';

/** The note beside Card when the register's terminal is the simulator, or null: nothing real is charged there. */
export function cardSimulatorNote(register: CounterRegister | null): string | null {
    return register?.card_terminal === 'simulator' ? SIMULATED_CARD_TERMINAL : null;
}

/** The ID kinds a check or charge may record, as the select lists them. */
export const idRefusal = (checked: boolean, type: string): string | null =>
    checked && type === '' ? 'Pick the kind of ID that was checked' : null;

/** The dealer's check ID rule as the counter reads it (GET /api/v1/pos/settings). */
export interface CheckIDRule {
    mode: 'never' | 'every_check' | 'over_amount';
    over_cents: number | null;
}

/**
 * Does the rule ask for the customer's ID when the sale's checks come to
 * checksTotalCents? An over an amount rule reads the total, so splitting one
 * check into pieces under the figure cannot avoid it; the server holds the
 * same rule at the completion.
 */
export function idRequired(rule: CheckIDRule | null, checksTotalCents: number): boolean {
    if (rule === null || rule.mode === 'never') return false;
    if (rule.mode === 'every_check') return true;
    return rule.over_cents !== null && checksTotalCents > rule.over_cents;
}

/** Why the rule asks for the ID on the sale's checks in total, in words, or null when it does not. */
export function idRequiredWords(rule: CheckIDRule | null, checksTotalCents: number): string | null {
    if (!idRequired(rule, checksTotalCents)) return null;
    if (rule !== null && rule.mode === 'over_amount' && rule.over_cents !== null) {
        return `Checks over ${money(rule.over_cents)} in total need the customer's ID: check it and pick the ID type`;
    }
    return `This check needs the customer's ID: check it and pick the ID type`;
}
