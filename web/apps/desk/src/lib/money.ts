// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Integer-only money and quantity helpers for the quote screens. The quote wire
// carries cents (*_cents), unit prices in ten thousandths of a dollar and
// quantities as decimal strings (at most four fraction digits). Text a user types
// is split on "." and handled as digits, never multiplied as a float, so a price
// like 1.1 can never become 1.1000000000000001 on its way to the server.

const DECIMAL = /^(\d*)(?:\.(\d*))?$/;

/**
 * Parses an unsigned decimal string into an integer scaled by 10^scale.
 * Returns null for anything that is not a plain decimal, is empty, or carries
 * more fraction digits than the scale allows.
 */
export function parseScaled(text: string, scale: number): number | null {
    const trimmed = text.trim();
    const m = DECIMAL.exec(trimmed);
    if (!m) return null;
    const whole = m[1] ?? '';
    const frac = m[2] ?? '';
    if (whole === '' && frac === '') return null;
    if (frac.length > scale) return null;
    const digits = (whole === '' ? '0' : whole) + frac.padEnd(scale, '0');
    const n = Number(digits);
    return Number.isSafeInteger(n) ? n : null;
}

/** Dollars typed by a user ("12.5") to integer cents (1250); null when invalid. */
export function dollarsToCents(text: string): number | null {
    return parseScaled(text, 2);
}

/** Dollars typed by a user ("5.5") to ten thousandths (55000); null when invalid. */
export function dollarsToTenThousandths(text: string): number | null {
    return parseScaled(text, 4);
}

/**
 * Boundary for APIs that still answer in float dollars (the parsing and pricing
 * services). The float is rendered to four decimals as text and then parsed by
 * the integer path above; nothing is multiplied.
 */
export function floatDollarsToTenThousandths(value: number): number {
    if (!Number.isFinite(value) || value < 0) return 0;
    return parseScaled(value.toFixed(4), 4) ?? 0;
}

/** Ten thousandths to a plain decimal string for an input box: 55000 -> "5.5000". */
export function tenThousandthsToDecimal(tt: number): string {
    const n = Math.trunc(tt);
    const sign = n < 0 ? '-' : '';
    const abs = Math.abs(n);
    return `${sign}${Math.floor(abs / 10000)}.${String(abs % 10000).padStart(4, '0')}`;
}

/** Integer cents to an input box value: 12500 -> "125.00". */
export function centsToInput(cents: number): string {
    const n = Math.trunc(cents);
    const sign = n < 0 ? '-' : '';
    const abs = Math.abs(n);
    return `${sign}${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, '0')}`;
}

/** Ten thousandths to an input box value, at least two fraction digits: 55000 -> "5.50", 13725 -> "1.3725". */
export function tenThousandthsToInput(tt: number): string {
    let text = tenThousandthsToDecimal(tt);
    while (text.endsWith('0') && /\.\d{3,}$/.test(text)) text = text.slice(0, -1);
    return text;
}

/** Normalises a user quantity to the wire's decimal string ("10", "12.5"); null unless positive. */
export function normalizeQuantity(text: string): string | null {
    const scaled = parseScaled(text, 4);
    if (scaled === null || scaled <= 0) return null;
    return scaledToQuantity(scaled);
}

/** A JS number (a parsing API's float quantity) to a decimal string; null unless positive. */
export function numberToQuantity(value: number): string | null {
    if (!Number.isFinite(value) || value <= 0) return null;
    return normalizeQuantity(value.toFixed(4));
}

function scaledToQuantity(scaled: number): string {
    const whole = Math.floor(scaled / 10000);
    const frac = String(scaled % 10000).padStart(4, '0').replace(/0+$/, '');
    return frac === '' ? String(whole) : `${whole}.${frac}`;
}

/**
 * The line extension in cents: quantity x unit price (ten thousandths), rounded
 * once, half away from zero, the platform rule. Both factors are scaled integers
 * multiplied as BigInt (scale 10^8, divided down to 10^2).
 */
export function extensionCents(quantity: string, unitPriceTenThousandths: number): number {
    const q = parseScaled(quantity, 4);
    if (q === null || !Number.isFinite(unitPriceTenThousandths)) return 0;
    const product = BigInt(q) * BigInt(Math.trunc(unitPriceTenThousandths));
    const divisor = 1000000n;
    const negative = product < 0n;
    const abs = negative ? -product : product;
    let cents = abs / divisor;
    if ((abs % divisor) * 2n >= divisor) cents += 1n;
    return Number(negative ? -cents : cents);
}
