// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The quote expiry rule as the desk shows it (QE-01-a): a quote past its Valid
 * until cannot be sent, accepted or converted until it is revised with a new
 * date. The words are the server's refusal, so the page and the 409 agree.
 */

import { formatDate } from '../../lib/dates.ts';

interface Expirable {
    status: string;
    expires_at?: string | null;
}

/** True when the quote is in the EXPIRED state, or is still open but past its Valid until. */
export function isQuoteExpired(quote: Expirable, now: number = Date.now()): boolean {
    if (quote.status === 'expired') return true;
    if (quote.status !== 'draft' && quote.status !== 'sent') return false;
    return !!quote.expires_at && new Date(quote.expires_at).getTime() <= now;
}

/** The ruling's words for an expired quote. */
export function quoteExpiredWords(quote: Expirable): string {
    const day = quote.expires_at ? formatDate(quote.expires_at) : 'its valid until date';
    return `This quote expired on ${day}: revise it with a new valid until date to send or convert it`;
}
