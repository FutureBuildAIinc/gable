// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { type ClassValue, clsx } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
    return twMerge(clsx(inputs))
}

// Money values from the ERP API are int64 cents (see backend/internal/order/model.go
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
