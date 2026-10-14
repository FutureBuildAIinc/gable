// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's Panels layout, as pure functions of the sale's figures: what
// Pay will send and which of the three panels is the sale's current step. The
// tenders it plans are exactly the ones the Add tender and Complete steps
// sent before; only the number of clicks changes. Integer cents throughout.

import type { TenderIn } from '../../services/POSService';

export type PayPlan =
    | { ok: true; tenders: TenderIn[]; changeCents: number }
    | { ok: false; owedCents: number };

const sum = (tenders: TenderIn[]) => tenders.reduce((s, t) => s + t.amount_cents, 0);

/**
 * What Pay sends: the tenders already added, plus the tender being entered
 * when the added ones do not yet cover the total. Refuses (with what is still
 * owed) when the tenders do not cover the total.
 */
export function payPlan(totalCents: number, added: TenderIn[], entry: TenderIn | null): PayPlan {
    let tenders = added;
    if (sum(tenders) < totalCents && entry !== null && entry.amount_cents > 0) {
        tenders = [...added, entry];
    }
    const tendered = sum(tenders);
    if (totalCents <= 0 || tenders.length === 0 || tendered < totalCents) {
        return { ok: false, owedCents: Math.max(totalCents - tendered, 0) };
    }
    return { ok: true, tenders, changeCents: tendered - totalCents };
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
 * done, the current one is lit, the rest wait. A completed sale has walked all
 * three and shows Summary as the last lit step.
 */
export function panelProgress(step: PanelKey, completed: boolean): PanelProgress {
    if (completed) return { customer: 'done', payment: 'done', summary: 'current' };
    const at = ORDER.indexOf(step);
    const stateOf = (k: PanelKey): PanelState => {
        const i = ORDER.indexOf(k);
        return i < at ? 'done' : i === at ? 'current' : 'upcoming';
    };
    return { customer: stateOf('customer'), payment: stateOf('payment'), summary: stateOf('summary') };
}
