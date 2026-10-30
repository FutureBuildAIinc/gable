// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The follow up on a quote: the day to chase it and whether it is late.
 *
 * The quote card and the quote list read the same follow up and must mark it
 * the same way, so the reading lives here once. The queue the server builds
 * reads "today" in the quote's branch's calendar; the mark on the screen is
 * drawn in the person's own calendar, which is the same calendar for anyone
 * working in their own branch.
 */

import { todayDay } from './dates.ts';

/** The follow up's business day in the person's own calendar, or null when there is none. */
export function followUpDay(at: string | null | undefined): string | null {
    if (!at) return null;
    const d = new Date(at);
    if (isNaN(d.getTime())) return null;
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

/** True when the follow up's day is before today: the overdue mark. */
export function followUpOverdue(at: string | null | undefined): boolean {
    const day = followUpDay(at);
    if (!day) return false;
    return day < todayDay();
}
