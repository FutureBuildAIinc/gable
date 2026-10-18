// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type {
    GLAccount,
    JournalEntry,
    FiscalPeriod,
    TrialBalanceRow,
    ProfitAndLossReport,
    BalanceSheetReport,
    CreateAccountRequest,
    CreateJournalEntryRequest,
    GLDetailRow,
} from '../types/gl';
import { fetchWithAuth } from './fetchClient';

const API = import.meta.env.VITE_API_URL || '';

/** A refused or failed GL read, carrying the HTTP status the page words its message on. */
export class GLRequestError extends Error {
    constructor(message: string, readonly status: number) {
        super(message);
        this.name = 'GLRequestError';
    }
}

/**
 * The branch a GL read is scoped to. The desk stamps every call with the
 * selected yard's X-Branch-Id; a statement read for another yard names that
 * yard in the header too (the header bounds the branch_id it may name), and a
 * consolidated read sends the header empty, which the server reads as no
 * branch at all (it then needs every branch's reach).
 */
function scope(branchId?: string): { headers: Record<string, string> } {
    return { headers: { 'X-Branch-Id': branchId ?? '' } };
}

// --- Accounts ---

export async function fetchAccounts(): Promise<GLAccount[]> {
    const res = await fetchWithAuth(`${API}/api/v1/gl/accounts?limit=200`);
    if (!res.ok) throw new Error('Failed to fetch accounts');
    const page = await res.json();
    return page.items || [];
}

export async function createAccount(data: CreateAccountRequest): Promise<GLAccount> {
    const res = await fetchWithAuth(`${API}/api/v1/gl/accounts`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data),
    });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
}

export async function updateAccount(id: string, data: CreateAccountRequest, revision: number): Promise<GLAccount> {
    const res = await fetchWithAuth(`${API}/api/v1/gl/accounts/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'If-Match': `"${revision}"` },
        body: JSON.stringify(data),
    });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
}

// --- Journal Entries ---

export async function fetchJournalEntries(branchId?: string): Promise<JournalEntry[]> {
    const params = new URLSearchParams({ limit: '200' });
    if (branchId) params.set('branch_id', branchId);
    const res = await fetchWithAuth(`${API}/api/v1/gl/journal-entries?${params.toString()}`, scope(branchId));
    if (!res.ok) throw new Error('Failed to fetch journal entries');
    const page = await res.json();
    return page.items || [];
}

export async function fetchJournalEntry(id: string): Promise<JournalEntry> {
    const res = await fetchWithAuth(`${API}/api/v1/gl/journal-entries/${id}`);
    if (!res.ok) throw new Error('Failed to fetch journal entry');
    return res.json();
}

export async function createJournalEntry(data: CreateJournalEntryRequest): Promise<JournalEntry> {
    const res = await fetchWithAuth(`${API}/api/v1/gl/journal-entries`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data),
    });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
}

export async function postJournalEntry(id: string): Promise<void> {
    const res = await fetchWithAuth(`${API}/api/v1/gl/journal-entries/${id}/post`, { method: 'POST' });
    if (!res.ok) throw new Error(await res.text());
}

export async function voidJournalEntry(id: string): Promise<void> {
    const res = await fetchWithAuth(`${API}/api/v1/gl/journal-entries/${id}/void`, { method: 'POST' });
    if (!res.ok) throw new Error(await res.text());
}

// --- Trial Balance ---

export async function fetchTrialBalance(asOfDate?: string, branchId?: string, currency?: string): Promise<TrialBalanceRow[]> {
    const params = new URLSearchParams();
    if (asOfDate) params.set('as_of', asOfDate);
    if (branchId) params.set('branch_id', branchId);
    if (currency) params.set('currency', currency);
    const qs = params.toString() ? `?${params.toString()}` : '';
    const res = await fetchWithAuth(`${API}/api/v1/gl/trial-balance${qs}`, scope(branchId));
    if (!res.ok) throw new GLRequestError('Failed to fetch trial balance', res.status);
    return res.json();
}

/** The GL detail drill down of one account over a date window. */
export async function fetchGLDetail(accountId: string, from: string, to: string, branchId?: string, currency?: string): Promise<GLDetailRow[]> {
    const params = new URLSearchParams({ account_id: accountId, from, to });
    if (branchId) params.set('branch_id', branchId);
    if (currency) params.set('currency', currency);
    const res = await fetchWithAuth(`${API}/api/v1/gl/detail?${params.toString()}`, scope(branchId));
    if (!res.ok) throw new GLRequestError('Failed to fetch GL detail', res.status);
    return res.json();
}

// --- Financial Statements ---

/** Income statement over [startDate, endDate], both YYYY-MM-DD and inclusive. */
export async function fetchProfitAndLoss(startDate?: string, endDate?: string, branchId?: string, compareStart?: string, compareEnd?: string): Promise<ProfitAndLossReport> {
    const params = new URLSearchParams();
    if (startDate) params.set('start', startDate);
    if (endDate) params.set('end', endDate);
    if (branchId) params.set('branch_id', branchId);
    if (compareStart) params.set('compare_start', compareStart);
    if (compareEnd) params.set('compare_end', compareEnd);
    const qs = params.toString() ? `?${params.toString()}` : '';
    const res = await fetchWithAuth(`${API}/api/v1/gl/profit-and-loss${qs}`, scope(branchId));
    if (!res.ok) throw new GLRequestError('Failed to fetch profit and loss report', res.status);
    return res.json();
}

/** Statement of financial position as of asOfDate (YYYY-MM-DD, inclusive). */
export async function fetchBalanceSheet(asOfDate?: string, branchId?: string): Promise<BalanceSheetReport> {
    const params = new URLSearchParams();
    if (asOfDate) params.set('as_of', asOfDate);
    if (branchId) params.set('branch_id', branchId);
    const qs = params.toString() ? `?${params.toString()}` : '';
    const res = await fetchWithAuth(`${API}/api/v1/gl/balance-sheet${qs}`, scope(branchId));
    if (!res.ok) throw new GLRequestError('Failed to fetch balance sheet', res.status);
    return res.json();
}

// --- Fiscal Periods ---

export async function fetchFiscalPeriods(): Promise<FiscalPeriod[]> {
    const res = await fetchWithAuth(`${API}/api/v1/gl/fiscal-periods?limit=200`);
    if (!res.ok) throw new Error('Failed to fetch fiscal periods');
    const page = await res.json();
    return page.items || [];
}

export async function closeFiscalPeriod(id: string, revision: number): Promise<FiscalPeriod> {
    const res = await fetchWithAuth(`${API}/api/v1/gl/fiscal-periods/${id}/close`, {
        method: 'POST',
        headers: { 'If-Match': `"${revision}"` },
    });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
}

export async function reopenFiscalPeriod(id: string, revision: number, reason: string): Promise<FiscalPeriod> {
    const res = await fetchWithAuth(`${API}/api/v1/gl/fiscal-periods/${id}/reopen`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'If-Match': `"${revision}"` },
        body: JSON.stringify({ reason }),
    });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
}
