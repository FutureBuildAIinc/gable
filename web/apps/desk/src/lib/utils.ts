// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// cn MOVED to @gable/design-system when web/ became one workspace (the
// shared components and the front door use the same copy); it is re-exported
// here so the desk's `import { cn } from './utils'` keeps working.

export { cn } from '@gable/design-system';

// Money values from the ERP API are int64 cents (see core/internal/order/model.go
// and the "Money: stored in cents (integer) in application code" convention in
// CLAUDE.md). Use this helper everywhere on the ERP side that renders an amount
// to dollars. Returns a string with a leading "$" and 2 decimals (e.g. 738807 -> "$7,388.07").
// Portal-side amounts already arrive as dollars (float) — use the portal's own
// formatCurrency helper there, not this one.
//
// Formatting goes through Intl's `style: 'currency'` rather than concatenating a
// "$" in front of Number#toLocaleString(): the latter puts the minus sign inside
// the symbol ("$-73.88"), which is wrong everywhere the ERP shows a credit,
// refund or below-cost margin. `narrowSymbol` pins the symbol to "$" for every
// en-* locale rather than letting en-GB render "US$73.88".
const CURRENCY = new Intl.NumberFormat(undefined, {
    style: 'currency',
    currency: 'USD',
    currencyDisplay: 'narrowSymbol',
});

export function formatCents(cents: number | null | undefined): string {
    const n = typeof cents === 'number' && isFinite(cents) ? cents : 0;
    // `|| 0` folds -0 (and only -0, since every other falsy result is 0) back to
    // positive zero, so a zero balance never renders as "-$0.00".
    return CURRENCY.format(n / 100 || 0);
}

// A quote line's unit price arrives as an integer in ten thousandths of a dollar
// (55000 is 5.5000). This renders it with the fraction digits it needs and no
// more than four, but never fewer than two: 55000 -> "$5.50", 13725 -> "$1.3725".
// It is pure integer arithmetic, so a price never passes through a float.
export function formatPrice4(tenThousandths: number | null | undefined): string {
    const n = typeof tenThousandths === 'number' && Number.isFinite(tenThousandths) ? Math.trunc(tenThousandths) : 0;
    const negative = n < 0;
    const abs = Math.abs(n);
    const whole = Math.floor(abs / 10000);
    let frac = String(abs % 10000).padStart(4, '0');
    while (frac.length > 2 && frac.endsWith('0')) frac = frac.slice(0, -1);
    const wholeText = new Intl.NumberFormat('en-US').format(whole);
    return `${negative && abs !== 0 ? '-' : ''}$${wholeText}.${frac}`;
}

// A business date (YYYY-MM-DD, no time zone) rendered in the user's locale. Built
// from its parts so a west-of-UTC machine never shows the day before; anything
// that is not a plain date is returned as it came.
export function formatDay(day: string | null | undefined): string {
    if (!day) return '—';
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
    if (!m) return day;
    return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3])).toLocaleDateString();
}
