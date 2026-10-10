// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The payments module on the wire contract (ADR 0005 section 9.4): integer
// cents everywhere, lowercase methods and statuses, and the list envelope.

export type PaymentMethod = 'cash' | 'check' | 'ach' | 'other' | 'card';
export type PaymentStatus = 'posted' | 'voided';

export interface Payment {
    id: string;
    number: string;
    customer_id: string;
    customer_name: string;
    branch_id: string;
    status: PaymentStatus;
    revision: number;
    currency: string;
    method: PaymentMethod;
    amount_cents: number;
    unapplied_cents: number;
    received_on: string;
    reference: string | null;
    notes: string | null;
    order_id: string | null;
    job_id: string | null;
    card_last4: string | null;
    card_brand: string | null;
    gateway_tx_id: string | null;
    void_reason: string | null;
    created_at: string;
    updated_at: string;
}

export interface PaymentPage {
    items: Payment[];
    next_cursor: string | null;
    limit: number;
}

export interface PaymentApplicationRequest {
    invoice_id: string;
    amount_cents: number;
    discount_cents?: number;
}

export interface CreatePaymentRequest {
    customer_id: string;
    amount_cents: number;
    method: PaymentMethod;
    reference?: string;
    notes?: string;
    applications?: PaymentApplicationRequest[];
}

export interface PaymentApplication {
    id: string;
    customer_id: string;
    currency: string;
    kind: 'payment' | 'credit_memo' | 'discount' | 'write_off';
    payment_id: string | null;
    credit_memo_id: string | null;
    invoice_id: string;
    amount_cents: number;
    applied_on: string;
    reversed_at: string | null;
    created_at: string;
}

export interface PaymentApplicationPage {
    items: PaymentApplication[];
    next_cursor: string | null;
    limit: number;
}

// Run Payments card payment flow: the intent answers the public key the
// tokenizer needs; the charge is taken with the token against the customer.
export interface PaymentIntentRequest {
    amount_cents: number;
}

export interface PaymentIntentResponse {
    public_key: string;
    amount_cents: number;
}

export interface ProcessCardPaymentRequest {
    customer_id: string;
    token_id: string;
    amount_cents: number;
    notes?: string;
    applications?: PaymentApplicationRequest[];
}

export interface RefundRequest {
    amount_cents: number;
    reason: string;
}

export interface Refund {
    id: string;
    payment_id: string | null;
    credit_memo_id: string | null;
    amount_cents: number;
    reason: string | null;
    status: string;
    method: string;
    refunded_on: string;
    created_at: string;
}

export interface VoidPaymentRequest {
    to: 'voided';
    reason: string;
}
