// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The card terminal's own words, as pure functions of the intent's state
// (CP-05): what the waiting card says while the terminal has the card, what
// each outcome says in plain words, the cancel that never claims the charge
// was stopped, and the manager's words for a lost answer's kind. The page
// renders them; nothing here touches the network.

import type { TerminalIntent, TerminalLostItem } from '../../types/pos';

/** The intent id the waiting refusal names, or null when the refusal is not the terminal's. */
export function intentIdOfRefusal(message: string): string | null {
    const found = message.match(/\(intent ([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\)/i);
    return found !== null ? found[1] : null;
}

/** The refusal words when the terminal declined the card, in plain words. */
export const CARD_DECLINED_WORDS = 'The card was declined at the terminal. The sale is still open: take the card again or choose another tender.';

/** The cancel's own words: honest about the device, never claiming the charge was stopped. */
export const CARD_CANCEL_WORDS = 'Cancelling stops the waiting here. It does not stop the terminal: the device may still answer, and the money may still move.';

/** What the waiting card shows for the intent's state: a title and the body's words. */
export function cardWaitWords(intent: TerminalIntent): { title: string; body: string } {
    switch (intent.state) {
        case 'claimed':
            return { title: 'Card at the terminal', body: 'The card is being read. Wait for the terminal to answer.' };
        case 'pending_at_device':
            return { title: 'Card at the terminal', body: 'The terminal has the card and has not answered yet. Pay again once the terminal answers.' };
        case 'authorized':
            return { title: 'Card approved', body: 'The terminal approved the card after the wait ran out. Pay again with the same card tender: the approved card is used and is not charged twice.' };
        case 'declined':
            return { title: 'Card declined', body: CARD_DECLINED_WORDS };
        case 'lost':
            if (intent.provider_ref === '') {
                return { title: 'The terminal did not answer', body: 'The terminal was never reached for this card, so no money moved. Pay again to charge the card.' };
            }
            return { title: 'The terminal did not answer', body: 'The terminal never answered for this card. A manager settles it from the lost card answers, reading the terminal\'s own record.' };
    }
}

/** The manager's line about the device's own late word on an item, or null when it sent none. */
export function lateWords(item: TerminalLostItem): string | null {
    if (item.late_verdict === '') return null;
    const said = item.late_verdict === 'authorized' ? 'approved' : 'declined';
    const ref = item.late_provider_ref !== '' ? ` (reference ${item.late_provider_ref})` : '';
    return `The terminal answered late: it ${said} this${ref}.`;
}

/** The reference the manager must type: none when the item already carries the device's own. */
export function needsReference(item: TerminalLostItem, outcome: 'authorized' | 'declined'): boolean {
    return outcome === 'authorized' && item.provider_ref === '' && item.late_provider_ref === '';
}

/** The manager's word for a lost answer's kind. */
export function lostKindWords(kind: TerminalLostItem['kind']): string {
    switch (kind) {
        case 'sale': return 'a card charge';
        case 'refund': return 'a card refund';
        case 'void': return 'a card void';
    }
}

/**
 * The counter's words when a card is asked for more than is owed on it
 * (CP-03), or null when the amount fits. Change comes out of cash only, so the
 * card is held to the remainder; the server refuses the same case.
 */
export function cardOverWords(amountCents: number, owedCents: number, owedMoney: string): string | null {
  if (amountCents <= owedCents) return null;
  return `The card can be charged at most ${owedMoney}: the rest is cash change.`;
}
