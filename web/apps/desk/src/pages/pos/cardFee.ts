// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The card fee notice (CP-15), in words: what the clerk and the customer read
// before the card is presented. The fee itself is the card machine's figure
// (the processor decides who is surcharged and by how much); this is only the
// expected amount at the branch's rate, so the notice always says it is a
// notice and the stored figure on the sale is final. Pure functions of integer
// cents and basis points.

import { money } from './standing';
import type { CardSurcharge } from '../../types/pos';

/** The expected fee on a card amount: rate in basis points, rounded half up to the cent. */
export function expectedFeeCents(cardCents: number, rateBps: number): number {
    if (!Number.isFinite(cardCents) || cardCents <= 0 || rateBps <= 0) return 0;
    return Math.floor((cardCents * rateBps * 2 + 10000) / 20000);
}

/** The rate as percent text: 300 is "3", 250 is "2.5", 5 is "0.05". */
export function ratePercentText(rateBps: number): string {
    const whole = Math.floor(rateBps / 100);
    const frac = rateBps % 100;
    if (frac === 0) return `${whole}`;
    return `${whole}.${String(frac).padStart(2, '0').replace(/0$/, '')}`;
}

/**
 * The notice the Pay screen shows when the branch charges a card fee and a card
 * tender is chosen, or null when it does not: the rate, the expected fee on
 * the card amount, and the cash price (the amount without the fee).
 */
export function cardFeeNotice(rule: CardSurcharge | null | undefined, cardCents: number): { fee: string; cash: string } | null {
    if (!rule || !rule.enabled || rule.rate_bps <= 0 || cardCents <= 0) return null;
    const fee = expectedFeeCents(cardCents, rule.rate_bps);
    return {
        fee: `Card fee ${ratePercentText(rule.rate_bps)}%: about ${money(fee)} is added when the card is presented. The card machine shows the final figure.`,
        cash: `Cash price ${money(cardCents)}, no fee.`,
    };
}

/** The disclosure the settings screen shows beside the switch. */
export const CARD_FEE_DISCLOSURE =
    'The card fee is added by the card processor on credit cards only, never on debit, and only where the processor has enrolled this business. ' +
    'It is never more than 3 percent, the card brands’ cap, and some states and provinces limit it further. ' +
    'It is not part of the sale, is not taxed, and comes back with the card on a return.';
