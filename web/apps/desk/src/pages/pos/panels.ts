// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's Panels layout, as pure functions of the sale's figures: what
// Pay will send and which of the three panels is the sale's current step. The
// tenders it plans are exactly the ones the Add tender and Complete steps
// sent before; only the number of clicks changes. Integer cents throughout.

import type { TenderIn } from '../../services/POSService';
import { CASH_ROUNDING_NEAREST_5, roundCashFor, type CashRoundingMode } from '../../lib/cashRounding';

export type PayPlan =
    | { ok: true; tenders: TenderIn[]; changeCents: number }
    | { ok: false; owedCents: number; over?: 'added' | 'typed'; typedMethod?: TenderIn['method']; limitCents?: number };

const sum = (tenders: TenderIn[]) => tenders.reduce((s, t) => s + t.amount_cents, 0);
const cashSum = (tenders: TenderIn[]) =>
    tenders.filter((t) => t.method === 'cash').reduce((s, t) => s + t.amount_cents, 0);

/**
 * What Pay sends: the tenders already added, plus the tender being entered
 * when the added ones do not yet cover the total. Refuses (with what is still
 * owed) when the tenders do not cover the total.
 *
 * The branch's cash rounding rule (CP-18) moves the cash leg only: the
 * drawer keeps the rounded cash and the change falls out of the rounded
 * figure, exactly as the server does it. The rule off changes nothing.
 */
export function payPlan(
    totalCents: number,
    added: TenderIn[],
    entry: TenderIn | null,
    rounding: CashRoundingMode | null | undefined = null,
): PayPlan {
    // One rule: card, check and account tenders together never exceed the
    // total, so the change never exceeds the cash tendered (the server's own
    // change_from_cash). Everything else pays, with change from the cash.
    const nonCash = (list: TenderIn[]) => sum(list.filter((t) => t.method !== 'cash'));
    const addedNonCash = nonCash(added);
    if (totalCents > 0 && addedNonCash > totalCents) {
        return { ok: false, owedCents: 0, over: 'added' };
    }
    const neededFor = (list: TenderIn[]) => {
        const other = nonCash(list);
        const cash = cashSum(list);
        if (cash > 0 && totalCents > other) {
            return other + roundCashFor(rounding, totalCents - other).rounded;
        }
        return totalCents;
    };
    let tenders = added;
    if (sum(added) < neededFor(added) && entry !== null && entry.amount_cents > 0) {
        if (entry.method !== 'cash' && addedNonCash + entry.amount_cents > totalCents) {
            // The limit a typed card, check or account tender must stay under:
            // the total less the other non cash tenders already added.
            return { ok: false, owedCents: 0, over: 'typed', typedMethod: entry.method, limitCents: totalCents - addedNonCash };
        }
        tenders = [...added, entry];
    }
    const tendered = sum(tenders);
    // The cash leg is what the total is less the legs that settle to the
    // cent, and the branch's rule rounds it: the drawer keeps the rounded
    // cash, and the change is what the cash tendered holds over it.
    const cashDue = roundCashFor(rounding, totalCents - nonCash(tenders)).rounded;
    const cashTendered = cashSum(tenders);
    const needed = neededFor(tenders);
    const change = cashTendered - cashDue;
    if (totalCents <= 0 || tenders.length === 0 || tendered < needed || change < 0) {
        const owed = Math.max(totalCents - tendered, 0);
        // With the rule on, the cash the drawer wants is the rounded figure,
        // so that is what the counter asks for; with the rule off the sale's
        // own total is what is still owed.
        const cashShort = Math.max(cashDue - cashTendered, 0);
        const rounded = rounding === CASH_ROUNDING_NEAREST_5;
        return { ok: false, owedCents: rounded ? cashShort : owed };
    }
    return { ok: true, tenders, changeCents: change };
}

export type PanelState = 'done' | 'current' | 'upcoming';

export type PanelKey = 'customer' | 'payment' | 'summary';

export interface PanelProgress {
    customer: PanelState;
    payment: PanelState;
    summary: PanelState;
}

const ORDER: PanelKey[] = ['customer', 'payment', 'summary'];

/**
 * The three panels as one linear walk: the panels before the current step are
 * done, the current one is lit, the rest wait. Payment is the exception: it
 * ticks only once a tender is chosen, so a Summary reached with no tender
 * ("Tender: None yet") does not show Payment as done. A completed sale has
 * walked all three, so all three are done.
 */
export function panelProgress(step: PanelKey, completed: boolean, tenderChosen: boolean): PanelProgress {
    if (completed) return { customer: 'done', payment: 'done', summary: 'done' };
    const at = ORDER.indexOf(step);
    const stateOf = (k: PanelKey): PanelState => {
        const i = ORDER.indexOf(k);
        if (k === 'payment' && i < at && !tenderChosen) return 'upcoming';
        return i < at ? 'done' : i === at ? 'current' : 'upcoming';
    };
    return { customer: stateOf('customer'), payment: stateOf('payment'), summary: stateOf('summary') };
}
