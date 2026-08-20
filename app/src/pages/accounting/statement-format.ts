// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { formatCents } from '../../lib/utils';

/**
 * Render a signed cent amount in the accounting convention: negatives in
 * parentheses rather than with a minus sign, e.g. -10000 -> "($100.00)".
 *
 * This is a sign-presentation wrapper, NOT another money formatter. Every
 * digit it emits comes from the shared `formatCents()` in lib/utils.ts, so the
 * currency symbol, decimal places and thousands separators stay identical to
 * the rest of the ERP. CLAUDE.md requires ERP pages to render money through
 * `formatCents()`; the accounting statements additionally need the
 * parenthesised-negative convention that a balance sheet is read in, and this
 * is the one place that convention is defined.
 *
 * See pages/accounting/erp-money-formatting.test.ts for the five pre-existing
 * private `_formatCents` copies this deliberately does not become a sixth of.
 */
export function formatStatementCents(cents: number): string {
    if (cents < 0) return `(${formatCents(-cents)})`;
    return formatCents(cents);
}

/** Tailwind colour classes for a signed statement amount. */
export function statementAmountClasses(cents: number): string {
    if (cents < 0) return 'text-rose-400';
    if (cents > 0) return 'text-emerald-400';
    return 'text-zinc-400';
}
