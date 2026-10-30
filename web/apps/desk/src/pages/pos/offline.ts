// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's offline mode in plain words and pure functions (ADR 0011
// sections 4 and 5.3): what the banner and the strip say, which tenders the
// register takes with the connection gone, how an open server cart becomes
// the local cart, what holds the till close, and the offline receipt. Nothing
// here computes a figure of a sale: every amount comes from the core's answer
// and is only formatted.

import { formatCents } from '../../lib/utils';
import type { Sale } from '../../types/pos';
import type {
    OfflineCartLine,
    OfflineCartPreview,
    OfflineQueue,
    OfflineSignedSale,
    OfflineSnapshot,
    OfflineSyncReport,
} from '../../types/offline';

export const OFFLINE_BANNER = 'Offline: sales are kept on this register and sent when the connection returns';
export const OFFLINE_PRICES = 'Offline prices: promotions, quantity breaks and job prices are not applied';
export const NEEDS_CONNECTION = 'needs the connection';
export const LEVEL_PRICE = 'Level price: this account\'s own price is not on this register';
export const NO_RELOAD = 'Do not reload while offline';
export const RESULT_UNKNOWN = 'Result unknown: this sale may already be complete. Wait for the connection';
export const UNKNOWN_HOLDS_CLOSE = 'A sale\'s result is unknown: reconnect to resolve it first';
export const QUEUE_HOLDS_CLOSE = 'Sales are still waiting to be sent: the till closes once the register has sent them';
export const RE_ENROL = 'This register must be enrolled again before it can sell offline';
export const BROWSER_TAB = 'This counter runs in a browser tab, so it sells online only: reconnect to finish the sale.';
export const SAME_GOODS_QUESTION = 'Was this the same customer and goods?';
export const RECEIPT_PRICES_NOTE = 'prices and tax as known on this register: promotions, quantity breaks and job prices not applied offline';
export const DIFFERENT_CUSTOMER = 'Different customer';

/** The acts the counter cannot take offline, with the same words. */
export const ONLINE_ONLY_ACTS = ['Returns', 'Voids', 'Deposits', 'Manager overrides', 'Till close', 'Movements'] as const;

/** The tenders the counter shows offline, and why one is greyed. */
export type OfflineMethod = 'cash' | 'check' | 'account' | 'card' | 'store_credit';

export interface TenderAvailability {
    method: OfflineMethod;
    enabled: boolean;
    words: string;
}

/**
 * Which tender the register takes offline: card and store credit never (ADR
 * 0011 section 1), the rest only where the branch's policy allows it, and the
 * account only for a cached account with room left.
 */
export function tenderAvailability(
    snapshot: OfflineSnapshot,
    method: OfflineMethod,
    accountRoomCents: number | null,
): TenderAvailability {
    if (method === 'card' || method === 'store_credit') {
        return { method, enabled: false, words: NEEDS_CONNECTION };
    }
    const allowed = snapshot.policy.allowed_tenders.some((m) => m.toLowerCase() === method);
    if (!allowed) return { method, enabled: false, words: 'not taken offline at this branch' };
    if (method === 'account') {
        if (accountRoomCents === null) return { method, enabled: false, words: 'cached accounts only' };
        if (accountRoomCents <= 0) return { method, enabled: false, words: 'no room left on this account offline' };
    }
    return { method, enabled: true, words: '' };
}

/** Whether a line can be carried into the offline cart, and why not. */
export function lineRefusal(line: Sale['lines'][number], snapshot: OfflineSnapshot): string | null {
    if (line.line_type === 'text') return null;
    if (line.line_type === 'charge') return `${line.description}: charge lines do not sell offline`;
    if (line.line_type === 'kit' || line.line_type === 'component') return `${line.description}: kits do not sell offline`;
    const product = snapshot.products.find((p) => p.id === line.product_id);
    if (!product) return `${line.description}: this product is not on the register's offline list`;
    if (product.is_kit) return `${line.description}: kits do not sell offline`;
    return null;
}

export interface CarriedCart {
    lines: OfflineCartLine[];
    refused: string[];
    replacesSaleId: string | null;
}

/**
 * The open server cart becomes the local cart (ADR 0011 5.3, no completion
 * in flight): its product lines, units and quantities as the page already
 * read them, naming the cart in replaces_sale_id. Charge and kit lines stay
 * behind, said in words; text lines carry nothing.
 */
export function carryCart(sale: Sale | null, snapshot: OfflineSnapshot, newId: () => string): CarriedCart {
    if (!sale || (sale.status !== 'open' && sale.status !== 'held')) return { lines: [], refused: [], replacesSaleId: null };
    const lines: OfflineCartLine[] = [];
    const refused: string[] = [];
    for (const line of sale.lines.filter((l) => l.parent_line_id === null)) {
        if (line.line_type === 'text') continue;
        const why = lineRefusal(line, snapshot);
        if (why) {
            refused.push(why);
            continue;
        }
        lines.push({ id: newId(), product_id: line.product_id as string, quantity: line.quantity ?? '1', uom: line.uom ?? '' });
    }
    return { lines, refused, replacesSaleId: sale.id };
}

/** What the strip says once the sync ran by itself: "Sent 12, 1 needs a manager". */
export function syncWords(report: OfflineSyncReport): string {
    const sent = report.posted + report.resolved;
    const parts = [`Sent ${sent}`];
    const manager = report.open_exception_count;
    if (manager > 0) parts.push(`${manager} ${manager === 1 ? 'needs' : 'need'} a manager`);
    if (report.errors > 0) parts.push(`${report.errors} will be sent again`);
    return parts.join(', ');
}

/** The banner's figures: the waiting count and total, and the room left under the queue caps. */
export function bannerFigures(queue: OfflineQueue | null, snapshot: OfflineSnapshot | null): { count: number; totalCents: number; roomCount: number; roomCents: number } | null {
    if (!queue || !snapshot) return null;
    return {
        count: queue.waiting_count,
        totalCents: queue.waiting_total_cents,
        roomCount: Math.max(snapshot.policy.max_queue_count - queue.waiting_count, 0),
        roomCents: Math.max(snapshot.policy.max_queue_total_cents - queue.waiting_total_cents, 0),
    };
}

/** Whether a queued sale still waits on the server's answer (signed, sent or not yet sent). */
export function awaitsAnswer(item: OfflineQueue['items'][number]): boolean {
    return item.state === 'signing' || item.state === 'waiting' || item.state === 'pending';
}

/**
 * Whether the queue has drained (ADR 0011 5.3): the sync answered and no sale
 * it holds waits on an answer. Only then is the unknown cart read again.
 */
export function queueDrained(queue: OfflineQueue | null): boolean {
    return queue !== null && !queue.items.some(awaitsAnswer);
}

/**
 * Why the till close waits (ADR 0011 sections 4 and 5.3): a result unknown
 * cart first, then any sale the register has not had answered or that waits
 * on a manager. Null when the close may go on.
 */
export function tillCloseWait(queue: OfflineQueue | null, unknownCartId: string | null): string | null {
    if (unknownCartId) return UNKNOWN_HOLDS_CLOSE;
    if (!queue) return null;
    const waiting = queue.items.filter(awaitsAnswer).length;
    if (waiting > 0) return QUEUE_HOLDS_CLOSE;
    const review = queue.items.filter((i) => i.state === 'review').length;
    if (review > 0) return `${review === 1 ? 'A sale waits' : `${review} sales wait`} on a manager: the till closes once ${review === 1 ? 'it is' : 'they are'} resolved`;
    return null;
}

/** The strip's action when the core holds no session: the shell's own sign in, not the web's cookie one. */
export const NO_SESSION_ASK = 'Sign in this register';

/** The words for a snapshot the shell could not take, with the item that waits (ADR 0011 section 4). */
export function snapshotRefusalWords(code: string, message: string): string {
    if (code === 'queue_not_drained') return `${message} The register keeps selling on the prices it holds until the item is answered; a copy that never comes is settled by enrolling the register again.`;
    if (code === 're_enrol_required' || code === 'store_rolled_back') return RE_ENROL;
    if (code === 'server_unreachable') return message;
    return `The register could not take new offline prices: ${message}. It keeps selling on the prices it holds.`;
}

/** A line's own note on the strip: the level price, in the core's word. */
export function lineNote(line: { level_fallback: boolean }): string | null {
    return line.level_fallback ? LEVEL_PRICE : null;
}

const cents = formatCents;

/** A part's decimal rate ("0.050000") as the percent a tax line prints ("5"), without trailing zeros. */
export function ratePercent(rate: string): string {
    const [whole, frac = ''] = rate.trim().split('.');
    const digits = (whole.replace(/^0+/, '') + frac.padEnd(6, '0')).padStart(7, '0');
    const intPart = digits.slice(0, digits.length - 4).replace(/^0+(?=\d)/, '');
    const fracPart = digits.slice(digits.length - 4).replace(/0+$/, '');
    return fracPart ? `${intPart}.${fracPart}` : intPart;
}

/**
 * The offline receipt (ADR 0011 section 4): OFFLINE SALE, the offline
 * reference, each line's price charged with its level price mark, the prices
 * note, the totals and tenders as the core signed them, and no invoice number.
 */
export function receiptLines(sale: OfflineSignedSale, customerName: string | null): string[] {
    const out = ['OFFLINE SALE', `Reference ${sale.offline_ref}`, `Business day ${sale.business_date}`];
    out.push(customerName ? `Customer ${customerName}` : 'Walk-in sale');
    if (sale.different_customer) out.push('Declared: a different customer than the unknown sale');
    for (const l of sale.lines) {
        out.push(`${l.quantity} ${l.uom} ${l.sku} ${l.description}  ${cents(l.line_total_cents)}${l.level_fallback ? '  level price' : ''}`);
    }
    out.push(`Subtotal ${cents(sale.subtotal_cents)}`);
    // A Canadian sale prints each tax part as its own line (CP-14 A1: BC's PST
    // must stand apart from GST); a US sale keeps its one Tax line.
    if (!sale.tax_exempt && sale.tax_lines && sale.tax_lines.length > 0) {
        for (const t of sale.tax_lines) out.push(`${t.label} ${ratePercent(t.rate)}%: ${cents(t.cents)}`);
    } else {
        out.push(sale.tax_exempt ? 'Tax exempt' : `Tax ${cents(sale.tax_cents)}`);
    }
    out.push(`Total ${cents(sale.total_cents)}`);
    for (const t of sale.tenders) out.push(`${t.method.toUpperCase()} ${cents(t.amount_cents)}`);
    if (sale.change_cents > 0) out.push(`Change ${cents(sale.change_cents)}`);
    out.push(RECEIPT_PRICES_NOTE);
    return out;
}

/** A preview's tender line in words: the change, or why the tender is refused. */
export function tenderWords(preview: OfflineCartPreview | null): string | null {
    if (!preview) return null;
    if (preview.tender_refusal) return preview.tender_refusal.message;
    if (preview.change_cents !== null && preview.change_cents > 0) return `Change ${cents(preview.change_cents)}`;
    return null;
}

/** The register's local business date and time, as the core's request carries them. */
export function soldAt(now: Date): string {
    return now.toISOString().replace(/\.\d{3}Z$/, 'Z');
}

/** The register's unknown cart, kept across a reload offline (one per register). */
export function unknownCartKey(register: string): string {
    return `gable.pos.offline.unknown-cart.${register}`;
}

/** The card leg still open at the terminal for the register's unknown cart, kept beside it. */
export function unknownCardKey(register: string): string {
    return `gable.pos.offline.unknown-card.${register}`;
}

/**
 * Whether a completion's failure is the connection's (the answer was lost,
 * so the result is unknown) rather than the server's own refusal.
 */
export function isLostAnswer(err: unknown, online: boolean): boolean {
    if (!online) return true;
    if (err instanceof TypeError) return true;
    return err instanceof Error && /failed to fetch|network ?error|load failed/i.test(err.message);
}
