// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The most rows walkCursor collects, so a broken cursor cannot loop forever. */
export const WALK_CURSOR_CAP = 2000;

interface CursorPage<T> {
    items: T[];
    next_cursor?: string | null;
}

/**
 * Reads every page a cursor list offers, in order, until the last page or
 * WALK_CURSOR_CAP rows. A screen that shows a list beside totals, or offers
 * each row as a choice, must not stop at the first page.
 */
export async function walkCursor<T>(readPage: (cursor?: string) => Promise<CursorPage<T>>, cap: number = WALK_CURSOR_CAP): Promise<T[]> {
    const all: T[] = [];
    let cursor: string | undefined;
    while (all.length < cap) {
        const page = await readPage(cursor);
        all.push(...page.items);
        if (!page.next_cursor) break;
        cursor = page.next_cursor;
    }
    return all.slice(0, cap);
}
