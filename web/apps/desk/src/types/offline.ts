// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The offline counter's shapes as the work shell's Rust core answers them
// (ADR 0011 sections 5.1 to 5.4, tauri/core/src/offline/model.rs). The page
// holds the cart's lines, units, quantities, customer and tenders; every
// figure in these shapes is the core's.

export interface OfflineError {
    code: string;
    message: string;
}

export interface OfflineEnrolment {
    register_id: string;
    device_epoch: number;
    public_key_hex: string;
    gable_url: string;
    last_seq: number;
    confirmed_seq: number;
    chain_head: string;
}

export interface OfflineUnitRow {
    uom: string;
    unit_qty: string;
    stock_qty: string;
    sell: boolean;
    price: boolean;
}

export interface OfflineProduct {
    id: string;
    sku: string;
    barcode: string | null;
    description: string;
    category: string;
    taxable: boolean;
    is_kit: boolean;
    random_length: boolean;
    board_thickness_in: string | null;
    board_width_in: string | null;
    stock_uom: string;
    sale_uom: string;
    price_uom: string;
    in_stock: string;
    unit_set: OfflineUnitRow[];
}

export interface OfflineAccount {
    customer_id: string;
    account_number: string;
    name: string;
    currency: string;
    tax_exempt: boolean;
    po_required: boolean;
    buyer_roster_required: boolean;
    credit_hold: boolean;
    credit_limit_cents: number;
    open_receivable_cents: number;
    offline_room_cents: number;
    check_limit_cents: number | null;
    price_key: string;
    level_key: string | null;
    jobs: Array<{ id: string; code: string; name: string }>;
    buyers: Array<{ contact_id: string; name: string; per_purchase_cap_cents: number | null }>;
}

export interface OfflinePolicy {
    enabled: boolean;
    max_sale_cents: number;
    max_check_cents: number;
    default_check_limit_cents: number | null;
    account_cap_cents: number;
    max_queue_count: number;
    max_queue_total_cents: number;
    allowed_tenders: string[];
    require_id_for_tenders: string[];
    cash_rounding_mode?: 'OFF' | 'NEAREST_5_CENTS';
}

export interface OfflineSnapshot {
    snapshot_id: string;
    register_id: string;
    branch_id: string;
    device_epoch: number;
    issued_at: string;
    till_session: { id: string; cashier_id: string; opened_at: string; business_date: string; opening_float_cents: number };
    policy: OfflinePolicy;
    /** A Canadian branch also carries its zone's parts (CP-14 A1); a US branch none. */
    tax: {
        branch_rate: string;
        provider_configured: boolean;
        zone_id?: string;
        data_source?: string;
        country?: string;
        province?: string;
        parts?: Array<{ name: string; rate: string; label: string }>;
    };
    walk_in_customer: { id: string; currency: string };
    products: OfflineProduct[];
    accounts: OfflineAccount[];
}

export interface OfflineTallyRow {
    pieces: number;
    length_ft: string;
}

/** One line as the page holds it: what the clerk rang, no figure. */
export interface OfflineCartLine {
    id: string;
    product_id: string;
    quantity: string;
    uom: string;
    tally?: { rows: OfflineTallyRow[] };
}

export interface OfflineTender {
    method: 'cash' | 'check' | 'account';
    amount_cents: number;
    reference?: string | null;
    id_checked: boolean;
    id_type?: string | null;
}

/** The request the page sends the core: the cart, never a figure. */
export interface OfflineSaleRequest {
    client_id: string;
    snapshot_id: string;
    till_session_id: string;
    cashier_id: string;
    customer_id: string | null;
    buyer_contact_id: string | null;
    customer_po: string | null;
    job_id: string | null;
    picked_up_by: string | null;
    business_date: string;
    sold_at: string;
    lines: Array<{ id: string; product_id: string; quantity?: string; uom: string; tally?: { rows: OfflineTallyRow[] } }>;
    tenders: OfflineTender[];
    replaces_sale_id?: string | null;
    different_customer?: boolean;
    unknown_cart_id?: string | null;
}

export interface OfflineSignedLine {
    id: string;
    position: number;
    product_id: string;
    sku: string;
    description: string;
    quantity: string;
    uom: string;
    price_uom: string;
    stock_uom: string;
    stock_quantity: string;
    price_key: string;
    price_source: string;
    level_fallback: boolean;
    unit_price_ten_thousandths: number;
    line_total_cents: number;
    taxable: boolean;
}

/** One tax part of a Canadian offline sale as the core priced it (CP-14 A1): printed as its own line. */
export interface OfflineTaxLine {
    name: string;
    rate: string;
    label: string;
    cents: number;
}

export interface OfflineCartPreview {
    snapshot_id: string;
    currency: string;
    tax_rate: string;
    tax_exempt: boolean;
    lines: OfflineSignedLine[];
    subtotal_cents: number;
    tax_cents: number;
    tax_lines?: OfflineTaxLine[];
    total_cents: number;
    change_cents: number | null;
    tender_refusal: OfflineError | null;
    account_room_cents: number | null;
    queue_room_count: number;
    queue_room_cents: number;
}

export interface OfflineSignedSale {
    client_id: string;
    register_id: string;
    device_epoch: number;
    device_seq: number;
    offline_ref: string;
    snapshot_id: string;
    till_session_id: string;
    customer_id: string | null;
    business_date: string;
    sold_at: string;
    currency: string;
    subtotal_cents: number;
    tax_cents: number;
    total_cents: number;
    change_cents: number;
    tax_rate: string;
    tax_exempt: boolean;
    tax_lines?: OfflineTaxLine[];
    lines: OfflineSignedLine[];
    tenders: Array<{ method: string; amount_cents: number; reference: string | null }>;
    replaces_sale_id?: string;
    different_customer?: boolean;
    unknown_cart_id?: string;
}

export type OfflineQueueState = 'signing' | 'waiting' | 'pending' | 'synced' | 'review';

export interface OfflineQueueItem {
    device_seq: number;
    state: OfflineQueueState;
    reason_code: string | null;
    reason_message: string | null;
    invoice_id: string | null;
    invoice_number: string | null;
    sale: OfflineSignedSale;
}

export interface OfflineQueue {
    register_id: string;
    device_epoch: number;
    last_seq: number;
    confirmed_seq: number;
    waiting_count: number;
    waiting_total_cents: number;
    pending_count: number;
    review_count: number;
    synced_count: number;
    items: OfflineQueueItem[];
}

export interface OfflineSyncReport {
    sent: number;
    posted: number;
    refused: number;
    held: number;
    resolved: number;
    errors: number;
    confirmed_seq: number;
    open_exception_count: number;
    queue: OfflineQueue;
}

export interface OfflineSnapshotRefresh {
    status: 'updated' | 'unchanged';
    snapshot_id: string;
    issued_at: string;
}
