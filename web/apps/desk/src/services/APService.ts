// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type {
    VendorInvoice,
    APPayment,
    APAgingSummary,
    CreateVendorInvoiceRequest,
    TransitionVendorInvoiceRequest,
    CreateAPPaymentRequest
} from '../types/ap';
import { fetchWithAuth } from './fetchClient';

const API = import.meta.env.VITE_API_URL || '';

export const APService = {
    async getAgingSummary(): Promise<APAgingSummary[]> {
        const res = await fetchWithAuth(`${API}/api/v1/ap/aging`);
        if (!res.ok) throw new Error('Failed to fetch AP aging summary');
        return res.json();
    },

    // The invoice list is the cursor envelope: walk every page so the page
    // keeps one flat list, newest first.
    async listVendorInvoices(vendorId?: string, status?: string): Promise<VendorInvoice[]> {
        const out: VendorInvoice[] = [];
        let cursor = '';
        for (;;) {
            const params = new URLSearchParams();
            params.set('limit', '100');
            if (vendorId) params.set('vendor_id', vendorId);
            if (status) params.set('status', status);
            if (cursor) params.set('cursor', cursor);
            const res = await fetchWithAuth(`${API}/api/v1/ap/invoices?${params.toString()}`);
            if (!res.ok) throw new Error('Failed to fetch vendor invoices');
            const page = await res.json();
            out.push(...(page.items ?? []));
            if (!page.next_cursor) return out;
            cursor = page.next_cursor;
        }
    },

    async getVendorInvoice(id: string): Promise<VendorInvoice> {
        const res = await fetchWithAuth(`${API}/api/v1/ap/invoices/${id}`);
        if (!res.ok) throw new Error('Failed to fetch vendor invoice details');
        return res.json();
    },

    async createVendorInvoice(req: CreateVendorInvoiceRequest): Promise<VendorInvoice> {
        const res = await fetchWithAuth(`${API}/api/v1/ap/invoices`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        });
        if (!res.ok) throw new Error(await res.text());
        return res.json();
    },

    async transitionVendorInvoice(id: string, req: TransitionVendorInvoiceRequest): Promise<VendorInvoice> {
        const res = await fetchWithAuth(`${API}/api/v1/ap/invoices/${id}/transitions`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        });
        if (!res.ok) throw new Error(await res.text());
        return res.json();
    },

    async payVendor(req: CreateAPPaymentRequest): Promise<APPayment> {
        const res = await fetchWithAuth(`${API}/api/v1/ap/payments`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        });
        if (!res.ok) throw new Error(await res.text());
        return res.json();
    },

    async listPayments(vendorId?: string): Promise<APPayment[]> {
        const params = new URLSearchParams();
        if (vendorId) params.set('vendor_id', vendorId);
        const qs = params.toString() ? `?${params.toString()}` : '';
        const res = await fetchWithAuth(`${API}/api/v1/ap/payments${qs}`);
        if (!res.ok) throw new Error('Failed to fetch AP payments');
        return res.json();
    }
};
