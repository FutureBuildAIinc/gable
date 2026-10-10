// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Accounts Payable types.
//
// The vendor invoice routes are on the wire contract (C4-1b, ADR 0008 7.4):
// money is int64 cents, the line unit price is int64 ten thousandths, the
// quantity is a decimal string, the status is lowercase, Gable's own number
// is number and the vendor's is vendor_invoice_number. The payment and aging
// routes keep their today shapes until their own conversion: the payment
// request still carries float dollars.

export type InvoiceStatus = 'pending' | 'approved' | 'partial' | 'paid' | 'voided';

export type PaymentMethod = 'CHECK' | 'ACH' | 'WIRE';

export interface VendorInvoice {
    id: string;
    number: string;               // Gable's own, AP-
    vendor_id: string;
    vendor_name: string;
    branch_id: string;
    vendor_invoice_number: string; // The vendor's own number
    currency: string;
    invoice_date: string;         // YYYY-MM-DD
    due_date: string;             // YYYY-MM-DD
    po_id: string | null;
    subtotal_cents: number;
    tax_cents: number;
    total_cents: number;
    amount_paid_cents: number;
    amount_open_cents: number;
    status: InvoiceStatus;
    approved_by: string | null;
    approved_at: string | null;
    notes: string;
    revision: number;
    gl_entry_id: string | null;
    created_at: string;
    lines?: VendorInvoiceLine[];
}

export interface VendorInvoiceLine {
    id: string;
    position: number;
    description: string;
    quantity: string;             // Decimal string
    unit_price_ten_thousandths: number;
    line_total_cents: number;
    gl_account_id: string | null;
    purchase_order_line_id: string | null;
    product_id: string | null;
    po_freight_charge_id: string | null;
    created_at: string;
}

export interface APPayment {
    id: string;
    vendor_id: string;
    vendor_name?: string;
    batch_id?: string;
    amount: number;         // Cents
    method: PaymentMethod;
    check_number?: string;
    reference?: string;
    payment_date: string;   // YYYY-MM-DD
    status: string;         // PENDING, COMPLETE, VOIDED
    created_at: string;
}

export interface CreateVendorInvoiceRequest {
    vendor_id: string;
    branch_id?: string;
    vendor_invoice_number: string;
    invoice_date: string;   // YYYY-MM-DD
    due_date: string;       // YYYY-MM-DD
    po_id?: string;
    currency?: string;
    tax_cents: number;
    notes?: string;
    lines: CreateVendorInvoiceLineReq[];
}

export interface CreateVendorInvoiceLineReq {
    description: string;
    quantity: string;       // Decimal string
    unit_price_ten_thousandths: number;
    gl_account_id: string;
    purchase_order_line_id?: string;
    product_id?: string;
}

export interface TransitionVendorInvoiceRequest {
    to: 'approved' | 'voided';
    revision: number;
    reason?: string;
}

export interface CreateAPPaymentRequest {
    vendor_id: string;
    amount: number;         // Dollars (sent as float64, until the payment routes convert)
    method: PaymentMethod;
    check_number?: string;
    reference?: string;
    payment_date: string;   // YYYY-MM-DD
    invoice_ids: string[];
}

export interface APAgingSummary {
    vendor_id: string;
    vendor_name: string;
    current: number;        // Cents
    past_30: number;        // Cents
    past_60: number;        // Cents
    past_90: number;        // Cents
    total: number;          // Cents
}
