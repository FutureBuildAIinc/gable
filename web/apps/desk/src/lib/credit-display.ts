// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Display and input helpers for credit memos (ADR 0005 section 6.3). A credit
// memo stores every total, quantity and extension NEGATIVE; storage is never
// flipped. The desk shows those values as credits (the minus sign in the safety
// red token) and lets a user type a positive "how many come back", which is
// negated into the wire's decimal string with string arithmetic, never a float.

import { formatCents } from './utils.ts';
import { parseScaled } from './money.ts';

const SCALE = 4;

/** A signed decimal string ("-2", "1.5") to an integer scaled by 10^4; null when it is not one. */
export function quantityToScaled(text: string | null | undefined): number | null {
    if (text === null || text === undefined) return null;
    const trimmed = text.trim();
    const negative = trimmed.startsWith('-');
    const scaled = parseScaled(negative ? trimmed.slice(1) : trimmed, SCALE);
    if (scaled === null) return null;
    return negative ? -scaled : scaled;
}

/** A scaled integer back to the wire's decimal string: 20000 -> "2", -15000 -> "-1.5". */
export function scaledToQuantity(scaled: number): string {
    const negative = scaled < 0;
    const abs = Math.abs(Math.trunc(scaled));
    const whole = Math.floor(abs / 10 ** SCALE);
    const frac = String(abs % 10 ** SCALE).padStart(SCALE, '0').replace(/0+$/, '');
    const body = frac === '' ? String(whole) : `${whole}.${frac}`;
    return negative && abs !== 0 ? `-${body}` : body;
}

/**
 * What a user types for "quantity returned" ("2", "1.5") to the wire's negative
 * decimal string ("-2", "-1.5"). Null unless the text is a positive decimal with
 * at most four fraction digits.
 */
export function creditQuantityFromInput(text: string): string | null {
    const scaled = parseScaled(text, SCALE);
    if (scaled === null || scaled <= 0) return null;
    return scaledToQuantity(-scaled);
}

/** A stored (negative) credit quantity to the positive text a user would type: "-2" -> "2". */
export function creditQuantityToInput(quantity: string | null | undefined): string {
    const scaled = quantityToScaled(quantity);
    if (scaled === null) return '';
    return scaledToQuantity(Math.abs(scaled));
}

/** The decimal strings added: "24" and "-2" give "22". Null when either is not a decimal. */
export function addQuantities(a: string, b: string): string | null {
    const x = quantityToScaled(a);
    const y = quantityToScaled(b);
    if (x === null || y === null) return null;
    return scaledToQuantity(x + y);
}

/**
 * How much of an invoice line can still be credited: what it billed less what
 * the credit memos the page knows of already credited (their quantities are
 * negative, so they are added). Never below zero. Null when the line carries no
 * quantity (a note line).
 */
export function remainingCreditable(billed: string | null, creditedNegatives: string[]): string | null {
    const b = quantityToScaled(billed);
    if (b === null) return null;
    let left = b;
    for (const c of creditedNegatives) {
        const n = quantityToScaled(c);
        if (n !== null) left += n;
    }
    return scaledToQuantity(Math.max(left, 0));
}

/** A credit in dollars with its minus sign: -4400 -> "-$44.00". Positive and zero amounts render as they are. */
export function formatCreditCents(cents: number | null | undefined): string {
    return formatCents(cents);
}

/** The text colour class for an amount: the safety red token for a credit, none otherwise. */
export function creditTextClass(cents: number | null | undefined): string {
    return typeof cents === 'number' && cents < 0 ? 'text-red-400' : '';
}

/** A line's quantity for display: the sign kept, a note line's missing quantity as a dash. */
export function formatQuantity(quantity: string | null | undefined): string {
    return quantity === null || quantity === undefined || quantity === '' ? '—' : quantity;
}
