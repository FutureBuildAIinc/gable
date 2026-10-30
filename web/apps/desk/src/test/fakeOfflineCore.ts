// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// A stand in for the work shell's Rust core in the desk's unit tests: it
// answers the named commands the counter calls with what the test sets, and
// records every call. Its figures are the test's own, deliberately unlike
// any the page could compute, so a test proves the page shows the core's.

import type {
    OfflineCartPreview,
    OfflineQueue,
    OfflineSaleRequest,
    OfflineSignedSale,
    OfflineSnapshot,
    OfflineSyncReport,
} from '../types/offline';

export function offlineSnapshot(): OfflineSnapshot {
    return {
        snapshot_id: 'snap-1', register_id: 'REG-01', branch_id: 'b-main', device_epoch: 2, issued_at: '2026-04-19T13:05:00Z',
        till_session: { id: 't', cashier_id: 'c', opened_at: '2026-04-19T13:00:00Z', business_date: '2026-04-19', opening_float_cents: 20000 },
        policy: {
            enabled: true, max_sale_cents: 250000, max_check_cents: 100000, default_check_limit_cents: 75000, account_cap_cents: 150000,
            max_queue_count: 5, max_queue_total_cents: 500000, allowed_tenders: ['cash', 'check', 'account'], require_id_for_tenders: ['check'],
        },
        tax: { branch_rate: '0.082500', provider_configured: false },
        walk_in_customer: { id: 'walk-in', currency: 'USD' },
        products: [
            { id: 'p-a', sku: 'SKU-a', barcode: '012345', description: 'Item a', category: 'Lumber', taxable: true, is_kit: false,
                random_length: false, board_thickness_in: null, board_width_in: null, stock_uom: 'EA', sale_uom: 'EA', price_uom: 'EA',
                in_stock: '40', unit_set: [{ uom: 'EA', unit_qty: '1', stock_qty: '1', sell: true, price: true }] },
            { id: 'p-kit', sku: 'KIT-1', barcode: null, description: 'Deck kit', category: 'Kits', taxable: true, is_kit: true,
                random_length: false, board_thickness_in: null, board_width_in: null, stock_uom: 'EA', sale_uom: 'EA', price_uom: 'EA',
                in_stock: '4', unit_set: [{ uom: 'EA', unit_qty: '1', stock_qty: '1', sell: true, price: true }] },
        ],
        accounts: [{
            customer_id: 'cust-1', account_number: 'A-100', name: 'Harbor Builders', currency: 'USD', tax_exempt: false, po_required: false,
            buyer_roster_required: false, credit_hold: false, credit_limit_cents: 500000, open_receivable_cents: 0, offline_room_cents: 150000,
            check_limit_cents: null, price_key: 'customer:cust-1', level_key: 'level:pro', jobs: [], buyers: [],
        }],
    };
}

export function emptyQueue(): OfflineQueue {
    return { register_id: 'REG-01', device_epoch: 2, last_seq: 0, confirmed_seq: 0, waiting_count: 0, waiting_total_cents: 0,
        pending_count: 0, review_count: 0, synced_count: 0, items: [] };
}

export interface FakeCore {
    calls: Array<{ command: string; args?: Record<string, unknown> }>;
    snapshot: OfflineSnapshot;
    queue: OfflineQueue;
    /** The core's price for a request; the default is the test's odd figure. */
    price: (req: OfflineSaleRequest) => OfflineCartPreview;
    sync: () => OfflineSyncReport;
    refuse: Partial<Record<string, { code: string; message: string }>>;
    invoke: (command: string, args?: Record<string, unknown>) => Promise<unknown>;
}

/** The odd total the stand in answers: 123.45 for any cart. */
export const CORE_TOTAL = 12345;

export function fakeOfflineCore(): FakeCore {
    let seq = 0;
    const core: FakeCore = {
        calls: [],
        snapshot: offlineSnapshot(),
        queue: emptyQueue(),
        refuse: {},
        price: (req) => {
            const kit = req.lines.find((l) => core.snapshot.products.find((p) => p.id === l.product_id)?.is_kit);
            if (kit) throw { code: 'kit_not_offline', message: 'Kits do not sell offline: remove the kit line or wait until the counter is online.' };
            const tender = req.tenders[0];
            const card = tender && !['cash', 'check', 'account'].includes(tender.method);
            return {
                snapshot_id: 'snap-1', currency: 'USD', tax_rate: '0.082500', tax_exempt: false,
                lines: req.lines.map((l, i) => ({ id: l.id, position: i, product_id: l.product_id, sku: 'SKU-a', description: 'Item a',
                    quantity: l.quantity ?? '1', uom: l.uom, price_uom: l.uom, stock_uom: 'EA', stock_quantity: l.quantity ?? '1',
                    price_key: req.customer_id ? 'level:pro' : 'walkin', price_source: 'level', level_fallback: req.customer_id !== null,
                    unit_price_ten_thousandths: 0, line_total_cents: 11111, taxable: true })),
                subtotal_cents: 11111, tax_cents: 1234, total_cents: CORE_TOTAL,
                change_cents: tender && !card ? Math.max(tender.amount_cents - CORE_TOTAL, 0) : null,
                tender_refusal: card ? { code: 'tender_not_offline', message: 'tender method card is not allowed offline' } : null,
                account_room_cents: req.customer_id ? 150000 : null, queue_room_count: 5, queue_room_cents: 500000,
            };
        },
        sync: () => ({ sent: 0, posted: 0, refused: 0, held: 0, resolved: 0, errors: 0, confirmed_seq: 0, open_exception_count: 0, queue: core.queue }),
        invoke: async (command, args) => {
            core.calls.push({ command, args });
            const refusal = core.refuse[command];
            if (refusal) throw refusal;
            switch (command) {
                case 'offline_enrolment_status':
                    return { register_id: 'REG-01', device_epoch: 2, public_key_hex: 'ab', gable_url: 'https://gable.yard.example', last_seq: seq, confirmed_seq: 0, chain_head: 'x' };
                case 'load_offline_snapshot':
                    return core.snapshot;
                case 'refresh_offline_snapshot':
                    return { status: 'unchanged', snapshot_id: 'snap-1', issued_at: core.snapshot.issued_at };
                case 'price_offline_cart':
                    return core.price(args?.request as OfflineSaleRequest);
                case 'sign_and_queue_offline_sale': {
                    const req = args?.request as OfflineSaleRequest;
                    const priced = core.price(req);
                    seq += 1;
                    const sale: OfflineSignedSale = {
                        client_id: req.client_id, register_id: 'REG-01', device_epoch: 2, device_seq: seq,
                        offline_ref: `REG-01-2-${String(seq).padStart(6, '0')}`, snapshot_id: 'snap-1', till_session_id: 't',
                        customer_id: req.customer_id, business_date: req.business_date, sold_at: req.sold_at, currency: 'USD',
                        subtotal_cents: priced.subtotal_cents, tax_cents: priced.tax_cents, total_cents: priced.total_cents,
                        change_cents: priced.change_cents ?? 0, tax_rate: priced.tax_rate, tax_exempt: false, lines: priced.lines,
                        tenders: req.tenders.map((t) => ({ method: t.method, amount_cents: t.amount_cents, reference: t.reference ?? null })),
                        ...(req.replaces_sale_id ? { replaces_sale_id: req.replaces_sale_id } : {}),
                        ...(req.different_customer ? { different_customer: true, unknown_cart_id: req.unknown_cart_id ?? undefined } : {}),
                    };
                    core.queue = { ...core.queue, last_seq: seq, waiting_count: core.queue.waiting_count + 1,
                        waiting_total_cents: core.queue.waiting_total_cents + sale.total_cents,
                        items: [...core.queue.items, { device_seq: seq, state: 'waiting', reason_code: null, reason_message: null, invoice_id: null, invoice_number: null, sale }] };
                    return sale;
                }
                case 'offline_queue_status':
                    return core.queue;
                case 'sync_offline_queue':
                    return core.sync();
                default:
                    throw { code: 'unknown_command', message: command };
            }
        },
    };
    return core;
}
