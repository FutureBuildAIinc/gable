// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The one date module (G1 walk note N15). Every date shown to a person goes
 * through here, in one style: the dealer's locale with the month as a short
 * word ("Oct 14, 2026" in en-US, "14 Oct 2026" in en-GB), so no reader can
 * mistake a month first form for a day first one. The dealer's locale is read
 * once at start (initDates, before the pages render): the desk route for the
 * desk's pages, the portal's public config for the portal's, and on any
 * failure en-US, quietly.
 *
 * formatDay is a business day (YYYY-MM-DD, no time zone) built from its
 * parts, so a machine west of UTC never shows the day before. formatDate and
 * formatDateTime are a moment, in the person's own time zone; the time form
 * adds hour and minute.
 *
 * The guard (dates.guard.test.ts) keeps every other file off the native
 * inputs and the locale formatters.
 */

import { fetchWithAuth } from '../services/fetchClient.ts';

const API_URL = import.meta.env.VITE_API_URL || '';

const DEFAULT_LOCALE = 'en-US';

let dealerLocale = DEFAULT_LOCALE;

/** The locale the formats run in. */
export function getDealerLocale(): string {
    return dealerLocale;
}

/** A tag Intl refuses (or an empty one) never leaves the default. */
function acceptable(tag: unknown): tag is string {
    if (typeof tag !== 'string' || tag === '') return false;
    try {
        new Intl.DateTimeFormat(tag);
        return true;
    } catch {
        return false;
    }
}

/**
 * Read the dealer's locale once, before the pages render. The portal's pages
 * take the public config; every other page takes the desk route. Any failure
 * leaves the en-US default, quietly.
 */
export async function initDates(): Promise<void> {
    const portal = window.location.pathname.startsWith('/portal');
    const url = portal
        ? `${API_URL}/api/portal/v1/config`
        : `${API_URL}/api/v1/dealer-profile/locale`;
    try {
        const res = await fetchWithAuth(url, portal ? { credentials: 'include' } : {});
        if (!res.ok) return;
        const body: unknown = await res.json();
        const tag = (body as { locale?: unknown } | null)?.locale;
        if (acceptable(tag)) dealerLocale = tag;
    } catch {
        // quiet: the en-US default stands
    }
}

/** Tests drive the locale through the same door initDates uses. */
export function setDealerLocale(tag: string): void {
    dealerLocale = acceptable(tag) ? tag : DEFAULT_LOCALE;
}

function dayFormatter(): Intl.DateTimeFormat {
    return new Intl.DateTimeFormat(dealerLocale, { year: 'numeric', month: 'short', day: 'numeric' });
}

function momentFormatter(): Intl.DateTimeFormat {
    return new Intl.DateTimeFormat(dealerLocale, { year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
}

// A business date rendered in the dealer's locale. A plain day (YYYY-MM-DD,
// no time zone) is built from its parts so a west-of-UTC machine never shows
// the day before; a business day the server sends as a midnight UTC timestamp
// (a DATE column in a Go time.Time) is formatted from its day part; any other
// value that parses is a moment, formatted as that moment's day in the
// person's own time zone, so machine text never reaches the screen; anything
// that does not parse at all is returned as it came.
const PLAIN_DAY = /^(\d{4})-(\d{2})-(\d{2})$/;
const MIDNIGHT_UTC = /^(\d{4})-(\d{2})-(\d{2})T00:00:00(\.\d+)?Z$/;

export function formatDay(day: string | null | undefined): string {
    if (!day) return '—';
    const m = PLAIN_DAY.exec(day) || MIDNIGHT_UTC.exec(day);
    if (m) {
        return dayFormatter().format(new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3])));
    }
    const d = new Date(day);
    if (isNaN(d.getTime())) return day;
    return dayFormatter().format(d);
}

// A moment (a Date, or the ISO text the API sends) rendered in the dealer's
// locale, in the person's own time zone. An unreadable moment is returned as
// it came.
export function formatDate(value: string | number | Date | null | undefined): string {
    if (value === null || value === undefined || value === '') return '—';
    const d = value instanceof Date ? value : new Date(value);
    if (isNaN(d.getTime())) return String(value);
    return dayFormatter().format(d);
}

// A moment with its time, hour and minute added to the date's style.
export function formatDateTime(value: string | number | Date | null | undefined): string {
    if (value === null || value === undefined || value === '') return '—';
    const d = value instanceof Date ? value : new Date(value);
    if (isNaN(d.getTime())) return String(value);
    return momentFormatter().format(d);
}

// A moment's clock time alone, hour and minute in the person's own time zone
// (an ETA, a "checked at"): the same two units formatDateTime adds.
export function formatTime(value: string | number | Date | null | undefined): string {
    if (value === null || value === undefined || value === '') return '—';
    const d = value instanceof Date ? value : new Date(value);
    if (isNaN(d.getTime())) return String(value);
    return new Intl.DateTimeFormat(dealerLocale, { hour: 'numeric', minute: '2-digit' }).format(d);
}

/** Today as a business day, in the person's own time zone. */
export function todayDay(): string {
    const n = new Date();
    return `${n.getFullYear()}-${String(n.getMonth() + 1).padStart(2, '0')}-${String(n.getDate()).padStart(2, '0')}`;
}

/** A business day n days on from another (or from today), as YYYY-MM-DD. */
export function addDays(day: string, n: number): string {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
    const d = m ? new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3])) : new Date();
    d.setDate(d.getDate() + n);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

/**
 * Parse a typed day: the module's own style (the month as a short word, day
 * and four digit year in the locale's order) or YYYY-MM-DD. Answers the
 * business day, or null when the text is not a day.
 */
export function parseDay(text: string): string | null {
    const t = text.trim();
    const iso = /^(\d{4})-(\d{2})-(\d{2})$/.exec(t);
    if (iso) {
        return validDay(Number(iso[1]), Number(iso[2]), Number(iso[3]));
    }
    // The month as a word: the locale's short or long month names, matched
    // between non letters (a short name that prefixes a long one, or a name
    // that ends in a full stop, still matches).
    let month = 0;
    let rest = t;
    const names = monthNamesForParsing();
    for (let i = 0; i < 12 && !month; i++) {
        for (const name of names[i]) {
            const re = new RegExp(`(^|[^\\p{L}])${escapeRe(name)}(?=[^\\p{L}]|$)`, 'iu');
            if (re.test(rest)) {
                month = i + 1;
                rest = rest.replace(re, '$1 ');
                break;
            }
        }
    }
    if (!month) return null;
    const nums = rest.match(/\d+/g);
    if (!nums || nums.length !== 2) return null;
    const [a, b] = nums.map(Number);
    const year = a > 31 ? a : b > 31 ? b : -1;
    const day = a > 31 ? b : b > 31 ? a : -1;
    if (year < 1000 || day < 1) return null;
    return validDay(year, month, day);
}

function validDay(year: number, month: number, day: number): string | null {
    if (month < 1 || month > 12 || day < 1 || day > 31) return null;
    const d = new Date(year, month - 1, day);
    if (d.getFullYear() !== year || d.getMonth() !== month - 1 || d.getDate() !== day) return null;
    return `${year}-${String(month).padStart(2, '0')}-${String(day).padStart(2, '0')}`;
}

/** The locale's short month names, January first. */
export function monthNames(): string[] {
    const f = new Intl.DateTimeFormat(dealerLocale, { month: 'short' });
    const out: string[] = [];
    for (let i = 0; i < 12; i++) out.push(f.format(new Date(2026, i, 15)));
    return out;
}

/** Each month's accepted typed forms: the short and the long name. */
function monthNamesForParsing(): string[][] {
    const short = new Intl.DateTimeFormat(dealerLocale, { month: 'short' });
    const long = new Intl.DateTimeFormat(dealerLocale, { month: 'long' });
    const out: string[][] = [];
    for (let i = 0; i < 12; i++) {
        const d = new Date(2026, i, 15);
        out.push([short.format(d), long.format(d)]);
    }
    return out;
}

/** The locale's long month name for a month grid's heading (1 is January). */
export function monthName(month: number, year: number): string {
    return new Intl.DateTimeFormat(dealerLocale, { month: 'long', year: 'numeric' }).format(new Date(year, month - 1, 15));
}

/**
 * The day the locale starts its week on, in Date#getDay numbering (0 is
 * Sunday): the grid's first column.
 */
export function weekStart(): number {
    try {
        const loc = new Intl.Locale(dealerLocale) as unknown as {
            getWeekInfo?: () => { firstDay: number };
            weekInfo?: { firstDay: number };
        };
        // The week info came as the weekInfo getter first and the
        // getWeekInfo() method later; read whichever the runtime has. Its
        // firstDay is 1 for Monday through 7 for Sunday.
        const info = loc.getWeekInfo ? loc.getWeekInfo() : loc.weekInfo;
        if (info && info.firstDay >= 1 && info.firstDay <= 7) return info.firstDay % 7;
    } catch {
        // fall through to Sunday
    }
    return 0;
}

/** The locale's weekday names, narrow, starting on weekStart(). */
export function weekdayNames(): string[] {
    const f = new Intl.DateTimeFormat(dealerLocale, { weekday: 'narrow' });
    const start = weekStart();
    const out: string[] = [];
    // 2026-01-04 was a Sunday.
    for (let i = 0; i < 7; i++) out.push(f.format(new Date(2026, 0, 4 + ((start + i) % 7))));
    return out;
}

function escapeRe(s: string): string {
    return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
