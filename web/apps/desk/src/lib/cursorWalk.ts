// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The most rows walkCursor collects, so a broken cursor cannot grow a list forever. */
export const WALK_CURSOR_CAP = 2000;

/** The most pages walkCursor requests, so a broken cursor cannot loop forever. */
export const WALK_CURSOR_MAX_PAGES = 100;

interface CursorPage<T> {
    items: T[];
    next_cursor?: string | null;
}

/**
 * Thrown when a list has more pages than the walk will read. The screen that
 * asked shows its message as an error, so a total shown beside a cut list is
 * never presented as complete.
 */
export class WalkTruncatedError extends Error {
    constructor(message: string) {
        super(message);
        this.name = 'WalkTruncatedError';
    }
}

/**
 * Reads every page a cursor list offers, in order, until the last page. A
 * screen that shows a list beside totals, or offers each row as a choice, must
 * not stop at the first page. The walk is bounded three ways: it stops on an
 * empty page and on a cursor equal to the one it just sent (a server that
 * answers the same page again), and it throws WalkTruncatedError when more
 * pages remain after maxPages requests or cap rows, rather than hand back a cut
 * list as if it were whole.
 */
export async function walkCursor<T>(
    readPage: (cursor?: string) => Promise<CursorPage<T>>,
    cap: number = WALK_CURSOR_CAP,
    maxPages: number = WALK_CURSOR_MAX_PAGES,
): Promise<T[]> {
    const all: T[] = [];
    let cursor: string | undefined;
    for (let pages = 0; pages < maxPages; pages++) {
        const page = await readPage(cursor);
        all.push(...page.items);
        if (!page.next_cursor || page.items.length === 0 || page.next_cursor === cursor) return all;
        if (all.length >= cap) break;
        cursor = page.next_cursor;
    }
    throw new WalkTruncatedError(`This list has more than ${all.length} rows; narrow the filter to see all of it.`);
}
