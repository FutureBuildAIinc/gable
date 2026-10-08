// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The order on the wire contract (ADR 0001; ADR 0005 sections 2 and 5): a
// lowercase status, integer cents money, decimal string quantities and the
// conversion pair, the scaled unit price, and the five line types of the
// shared sales line shape.

export type OrderStatus = 'draft' | 'on_hold' | 'confirmed' | 'backordered' | 'fulfilled' | 'cancelled';
export type OrderStatusColor = 'default' | 'info' | 'success' | 'warning' | 'error';
export type OrderDeliveryType = 'pickup' | 'delivery';
export type OrderTaxSource = 'exempt' | 'provider' | 'ship_to_rate' | 'branch_rate' | 'legacy';
export type SalesLineType = 'product' | 'kit' | 'component' | 'charge' | 'text';
export type SalesPriceSource = 'price_list' | 'quote' | 'override' | 'manual' | 'none';

export interface OrderSummary {
    id: string;
    number: string;
    branch_id: string;
    customer_id: string;
    customer_name: string;
    quote_id: string | null;
    job_id: string | null;
    status: OrderStatus;
    revision: number;
    currency: string;

    delivery_type: OrderDeliveryType;
    ship_to_id: string | null;
    customer_po: string | null;
    ordered_by_contact_id: string | null;
    salesperson_id: string | null;
    salesperson_name: string | null;
    scheduled_delivery_date: string | null;

    subtotal_cents: number;
    tax_cents: number;
    tax_rate_percent: string | null;
    tax_exempt: boolean;
    tax_source: OrderTaxSource;
    total_cents: number;

    total_cost_cents: number;
    total_margin_cents: number;
    margin_percent: string | null;
    total_commission_cents: number;

    hold_reason: 'credit_limit' | 'manual' | null;
    hold_note: string | null;
    confirmed_at: string | null;
    created_at: string;
    updated_at: string;

    invoice_ids: string[];
}

export interface OrderLine {
    id: string;
    position: number;
    line_type: SalesLineType;
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
    price_source: SalesPriceSource;
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

    quote_line_id: string | null;
    quantity_allocated: string;
    quantity_backordered: string;
    quantity_fulfilled: string;

    created_at: string;
}

export interface Order extends OrderSummary {
    ship_to: {
        id: string;
        code: string;
        name: string;
        line1: string;
        line2: string | null;
        city: string;
        region: string;
        postal_code: string;
        country: string | null;
        phone: string | null;
        delivery_instructions: string | null;
    } | null;
    lines: OrderLine[];
}

export interface OrderPage {
    items: OrderSummary[];
    next_cursor: string | null;
    limit: number;
    total?: number;
}

export interface CreateOrderLineRequest {
    id?: string;
    line_type?: 'product' | 'charge' | 'text';
    product_id?: string;
    charge_code?: string;
    sku?: string;
    description?: string;
    quantity?: string;
    uom?: string;
    price_uom?: string;
    uom_qty?: string;
    price_uom_qty?: string;
    unit_price_ten_thousandths?: number;
    override_reason?: string;
    discount_percent?: string;
    discount_cents?: number;
    discount_reason?: string;
    taxable?: boolean;
    is_special_order?: boolean;
    vendor_id?: string;
    special_order_unit_cost_ten_thousandths?: number;
}

export interface CreateOrderRequest {
    customer_id: string;
    quote_id?: string;
    job_id?: string;
    delivery_type: OrderDeliveryType;
    ship_to_id?: string;
    customer_po?: string;
    ordered_by_contact_id?: string;
    salesperson_id?: string;
    scheduled_delivery_date?: string;
    revision?: number;
    lines: CreateOrderLineRequest[];
}

export const getStatusColor = (status: OrderStatus): OrderStatusColor => {
    switch (status) {
        case 'draft': return 'default';
        case 'confirmed': return 'info';
        case 'backordered': return 'info';
        case 'fulfilled': return 'success';
        case 'on_hold': return 'warning';
        case 'cancelled': return 'error';
        default: return 'default';
    }
};

export const formatOrderStatus = (status: OrderStatus): string =>
    status.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase());
