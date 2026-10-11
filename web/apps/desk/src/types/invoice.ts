// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The invoice on the wire contract (ADR 0001; ADR 0005 section 6): every shape
// is derived from the generated client so a contract change is a compile error.
// A lowercase status, integer cents money, decimal string quantities, the scaled
// unit price, and an overdue flag the server computes (OVERDUE is not a status).
// The reporting shapes at the foot belong to the unconverted reporting module.

import type { components } from '@gable/api-client';

type Schemas = components['schemas'];

export type Invoice = Schemas['Invoice'];
export type InvoiceSummary = Schemas['InvoiceSummary'];
export type InvoiceLine = Schemas['InvoiceLine'];
export type InvoicePage = Schemas['InvoicePage'];
export type InvoiceStatus = Schemas['InvoiceStatus'];
export type InvoiceTransitionRequest = Schemas['InvoiceTransitionRequest'];

export const INVOICE_STATUSES: InvoiceStatus[] = ['unpaid', 'partial', 'paid', 'void', 'written_off'];

export type InvoiceStatusColor = 'default' | 'info' | 'success' | 'warning' | 'error';

export const getInvoiceStatusColor = (status: InvoiceStatus): InvoiceStatusColor => {
    switch (status) {
        case 'unpaid': return 'warning';
        case 'partial': return 'info';
        case 'paid': return 'success';
        case 'void': return 'error';
        case 'written_off': return 'default';
        default: return 'default';
    }
};

export const formatInvoiceStatus = (status: InvoiceStatus): string =>
    status.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase());

export interface ARAgingBucket {
    customer_id: string;
    customer_name: string;
    current: number;
    days_31_60: number;
    days_61_90: number;
    over_90: number;
    total: number;
}

export interface ARAgingReport {
    as_of_date: string;
    buckets: ARAgingBucket[];
    total_current: number;
    total_31_60: number;
    total_61_90: number;
    total_over_90: number;
    grand_total: number;
}

export interface StatementLine {
    date: string;
    type: string;
    description: string;
    debit: number;
    credit: number;
    balance: number;
}

export interface CustomerStatement {
    customer_id: string;
    customer_name: string;
    start_date: string;
    end_date: string;
    open_balance: number;
    close_balance: number;
    lines: StatementLine[];
}
