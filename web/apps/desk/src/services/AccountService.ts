// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { AccountSummary, CustomerTransactionPage } from '../types/account';
import type { Payment } from '../types/payment';
import { walkCursor } from '../lib/cursorWalk';
import { fetchWithAuth } from './fetchClient';

const API_BASE_URL = import.meta.env.VITE_API_URL || '';

export const AccountService = {
    getAccountSummary: async (customerId: string): Promise<AccountSummary> => {
        const response = await fetchWithAuth(`${API_BASE_URL}/api/v1/accounts/${customerId}`);
        if (!response.ok) {
            throw new Error('Failed to fetch account summary');
        }
        return response.json();
    },

    // The subledger, newest first, in the list envelope; the cursor walks it.
    getTransactions: async (customerId: string, cursor?: string): Promise<CustomerTransactionPage> => {
        const qs = cursor ? `?cursor=${encodeURIComponent(cursor)}` : '';
        const response = await fetchWithAuth(`${API_BASE_URL}/api/v1/accounts/${customerId}/transactions${qs}`);
        if (!response.ok) {
            throw new Error('Failed to fetch transactions');
        }
        return response.json();
    },

    // The account's posted payments that still hold unapplied cash (the 2200
    // deposits waiting to be applied or refunded).
    // Every page of them: cash past the first page can still be applied or voided.
    getUnappliedPayments: (customerId: string): Promise<Payment[]> => walkCursor(async (cursor) => {
        const qs = cursor ? `&cursor=${encodeURIComponent(cursor)}` : '';
        const response = await fetchWithAuth(`${API_BASE_URL}/api/v1/payments?customer_id=${customerId}&unapplied=true&status=posted&limit=50${qs}`);
        if (!response.ok) {
            throw new Error('Failed to fetch unapplied payments');
        }
        return await response.json() as { items: Payment[]; next_cursor: string | null };
    }),

    // The open invoices the unapplied cash can be applied to.
    // Every page of them: a customer with more than one page of open invoices
    // can still apply cash to the last.
    getOpenInvoices: (customerId: string): Promise<{ id: string; number: string; open_cents: number }[]> => walkCursor(async (cursor) => {
        const qs = cursor ? `&cursor=${encodeURIComponent(cursor)}` : '';
        const response = await fetchWithAuth(`${API_BASE_URL}/api/v1/invoices?customer_id=${customerId}&status=unpaid,partial&limit=50${qs}`);
        if (!response.ok) {
            throw new Error('Failed to fetch open invoices');
        }
        return await response.json() as { items: { id: string; number: string; open_cents: number }[]; next_cursor: string | null };
    }),
};

export type { CustomerTransactionPage };
