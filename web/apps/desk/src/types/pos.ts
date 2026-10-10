// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter module on the wire contract (ADR 0005 section 14.2 C2-5): one
// status field in lowercase, every amount an integer of cents, prices at
// scale 4, quantities decimal strings, and human readable POS- and RTN-
// numbers. Money stays integers until the display helper.

export type SaleStatus = 'open' | 'held' | 'completed' | 'voided';
export type TenderMethod = 'cash' | 'check' | 'card' | 'account';
export type RefundMethod = 'cash' | 'card' | 'account';
export type TillSessionStatus = 'open' | 'closed';
export type LineType = 'product' | 'kit' | 'component' | 'charge' | 'text';
export type PriceSource = 'price_list' | 'quote' | 'override' | 'manual' | 'none';

/** One line of a counter sale: the shared salesdoc shape (ADR 0005 2.2). */
export interface SaleLine {
    id: string;
    position: number;
    line_type: LineType;
    parent_line_id: string | null;
    product_id: string | null;
    charge_code_id: string | null;
    charge_code: string | null;
    sku: string | null;
    description: string;
    quantity: string | null;
    uom: string | null;
    price_uom: string | null;
    uom_qty: string | null;
    price_uom_qty: string | null;
    unit_price_ten_thousandths: number | null;
    priced_unit_price_ten_thousandths: number | null;
    price_source: PriceSource;
    override_reason: string | null;
    discount_percent: string | null;
    discount_cents: number | null;
    discount_reason: string | null;
    price_adjusted_by: string | null;
    line_total_cents: number | null;
    taxable: boolean;
    revenue_account_code: string | null;
    is_special_order: boolean;
    vendor_id: string | null;
    special_order_unit_cost_ten_thousandths: number | null;
    created_at: string;
}

/** One payment taken at the counter, stored net of change. */
export interface Tender {
    id: string;
    sale_id: string;
    method: TenderMethod;
    amount_cents: number;
    payment_id: string | null;
    reference: string | null;
    card_last4: string | null;
    card_brand: string | null;
    gateway_tx_id: string | null;
    auth_code: string | null;
    created_at: string;
}

export interface Sale {
    id: string;
    number: string;
    revision: number;
    branch_id: string;
    register_id: string;
    cashier_id: string;
    customer_id: string | null;
    currency: string;
    subtotal_cents: number;
    tax_cents: number;
    total_cents: number;
    change_cents: number;
    till_session_id: string | null;
    status: SaleStatus;
    invoice_id: string | null;
    completed_at: string | null;
    created_at: string;
    lines: SaleLine[];
    tenders: Tender[];
}

export interface SaleSummary {
    id: string;
    number: string;
    revision: number;
    branch_id: string;
    register_id: string;
    cashier_id: string;
    customer_id: string | null;
    currency: string;
    total_cents: number;
    status: SaleStatus;
    invoice_id: string | null;
    completed_at: string | null;
    created_at: string;
    item_count: number;
}

export interface SalePage {
    items: SaleSummary[];
    next_cursor: string | null;
    limit: number;
    total?: number;
}

// --- Returns ---

export interface ReturnLine {
    id: string;
    position: number;
    product_id: string | null;
    description: string;
    quantity: string | null;
    uom: string | null;
    unit_price_ten_thousandths: number | null;
    line_total_cents: number | null;
    restock: boolean;
}

export interface CounterReturn {
    id: string;
    number: string;
    revision: number;
    branch_id: string | null;
    register_id: string;
    till_session_id: string | null;
    original_sale_id: string | null;
    customer_id: string | null;
    cashier_id: string;
    currency: string;
    subtotal_cents: number;
    tax_cents: number;
    total_cents: number;
    refund_method: RefundMethod;
    reason: string;
    credit_memo_id: string | null;
    created_at: string;
    lines: ReturnLine[];
}

// --- Till sessions (drawer lifecycle) ---

export interface TillSession {
    id: string;
    register_id: string;
    branch_id: string | null;
    cashier_id: string;
    status: TillSessionStatus;
    opening_float_cents: number;
    opened_at: string;
    closed_at: string | null;
    expected_by_method: Record<string, number>;
    counted_by_method: Record<string, number>;
    over_short_cents: number | null;
    gl_entry_id: string | null;
    notes: string;
}

export interface TillReport {
    session: TillSession;
    sale_count: number;
    sales_total_cents: number;
    tax_total_cents: number;
    change_cents: number;
    tendered_by_method: Record<string, number>;
    expected_by_method: Record<string, number>;
}

// --- Typeahead and offline catalog ---

export interface QuickSearchResult {
    product_id: string;
    sku: string;
    description: string;
    unit_price_cents: number;
    uom: string;
    in_stock: string;
}
