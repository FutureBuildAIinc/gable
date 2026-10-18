// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's exchange, in words: the goods that come back, the goods that
// replace them and what the difference asks of the cashier. Pure functions of
// the server's figures (integer cents), so the words are tested without the
// screen. The server states the figures (POST /pos/exchanges/preview); the
// screen never computes a price or a tax of its own.

import type { ExchangeFigures, ReturnReason } from '../../types/pos';
import { money } from './standing';

export type DifferenceKind = 'pays' | 'refund' | 'even';

export interface Difference {
    kind: DifferenceKind;
    /** What the customer pays (kind pays) or is paid back (kind refund), never negative. */
    cents: number;
    text: string;
}

/** What the difference between the replacement and the return asks of the cashier. */
export function differenceOf(f: ExchangeFigures): Difference {
    if (f.difference_cents > 0) {
        return { kind: 'pays', cents: f.difference_cents, text: `Customer pays ${money(f.difference_cents)}` };
    }
    if (f.difference_cents < 0) {
        return { kind: 'refund', cents: -f.difference_cents, text: `Pay the customer back ${money(-f.difference_cents)}` };
    }
    return { kind: 'even', cents: 0, text: 'An even exchange: nothing to pay or give back' };
}

/** The lines of the totals box: the return, any fee, the replacement and the difference. */
export function exchangeTotals(f: ExchangeFigures): Array<{ label: string; cents: number }> {
    const rows = [{ label: 'Returned goods', cents: f.returned_cents + f.restocking_fee_cents }];
    if (f.restocking_fee_cents > 0) rows.push({ label: 'Restocking fee', cents: -f.restocking_fee_cents });
    rows.push({ label: 'Replacement goods', cents: f.replacement_cents });
    return rows;
}

/** A reason as the select words it: its label, and its fee when it carries one. */
export function reasonLabel(r: ReturnReason): string {
    return r.restocking_fee_bps > 0 ? `${r.label} (${bpsWords(r.restocking_fee_bps)} restocking fee)` : r.label;
}

/** Basis points as a percentage in words, no float drift: 1500 is 15%, 125 is 1.25%. */
export function bpsWords(bps: number): string {
    const whole = Math.floor(bps / 100);
    const frac = bps % 100;
    if (frac === 0) return `${whole}%`;
    const f = frac.toString().padStart(2, '0').replace(/0+$/, '');
    return `${whole}.${f}%`;
}

/** Why a return cannot be taken yet, or null: a line named, and the note when a line says other. */
export function returnRefusal(
    lines: Array<{ quantity: string; reason: string }>,
    note: string,
    requireReason = false,
): string | null {
    const named = lines.filter((l) => Number(l.quantity) > 0);
    if (named.length === 0) return 'Name at least one line and quantity to return';
    if (requireReason && named.some((l) => l.reason === '')) return 'Pick a reason for each line coming back';
    if (named.some((l) => l.reason === 'other') && note.trim() === '') {
        return 'A line returned for another reason needs a note saying why';
    }
    return null;
}
