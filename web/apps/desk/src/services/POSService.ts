// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter module's desk service on the wire contract (ADR 0005 14.2
// C2-5): tenders and floats in cents, the line wrapper, lowercase methods,
// and the revision preconditions the complete and the void carry. Money is
// parsed to cents with string arithmetic, never parseFloat.

import type {
    Sale,
    SalePage,
    SaleSummary,
    QuickSearchResult,
    TenderMethod,
    CounterReturn,
    RefundMethod,
    TillSession,
    TillReport,
    TillMovement,
    MovementRequest,
    BranchDay,
    DepositCandidate,
    DayClose,
    CounterCustomer,
    CounterRegister,
    CounterStanding,
    CounterOverride,
    SealedSignature,
    ReturnReason,
    ReturnReasonCode,
    Exchange,
    ExchangeFigures,
    IdType,
    TerminalIntent,
    TerminalLostItem,
    TerminalSweepResult,
    StepUpManager,
    StepUpApproval,
} from '../types/pos.ts';
import { fetchWithAuth } from './fetchClient';
import { parseApiError, ifMatch } from './apiError';
import { openAuthedPdf } from '../lib/authedPdf';

const API_URL = import.meta.env.VITE_API_URL || '';

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (response.ok) return response;
    throw await parseApiError(response, fallback);
}

const JSON_HEADERS = { 'Content-Type': 'application/json' };

/** One category chip of the counter's catalogue read. */
export interface CatalogCategory {
    name: string;
    count: number;
}

/** One tile of the counter's catalogue read (GET /api/v1/pos/tiles). */
export interface CatalogItem {
    product_id: string;
    sku: string;
    description: string;
    uom: string;
    unit_price_cents: number;
    /** The yard's on hand, a decimal string. */
    in_stock: string;
    category: string;
    image_url: string | null;
}

export interface CatalogTiles {
    categories: CatalogCategory[];
    items: CatalogItem[];
    /** The product images setting; image_url is null while it is off. */
    images: boolean;
}

export interface TilesQuery {
    category?: string;
    q?: string;
    limit?: number;
}

/** Dollars typed by a cashier to integer cents; an empty or bad parse is NaN. */
export function dollarsToCents(text: string): number {
    const t = text.trim();
    if (!/^-?\d+(\.\d{1,2})?$/.test(t)) return NaN;
    const [whole, fraction = ''] = t.split('.');
    const sign = whole.startsWith('-') ? -1 : 1;
    const w = whole.replace('-', '');
    const cents = Number(w) * 100 + Number(fraction.padEnd(2, '0') || '0');
    return sign * cents;
}

export interface TenderIn {
    method: TenderMethod;
    amount_cents: number;
    reference?: string;
    token_id?: string;
    /** The cashier looked at the payer's ID (a check or a charge); the number is never taken. */
    id_checked?: boolean;
    id_type?: IdType;
}

/** One returned line of a return or an exchange. */
export interface ReturnLineIn {
    line_id: string;
    quantity: string;
    restock?: boolean;
    reason_code?: ReturnReasonCode;
}

/** The request of an exchange, and of its preview (which takes no tenders). */
export interface ExchangeIn {
    register_id?: string;
    original_sale_id?: string;
    customer_id?: string;
    refund_method?: 'cash' | 'account';
    reason?: string;
    returned_lines: ReturnLineIn[];
    sale_lines: Array<{ product_id: string; quantity: string }>;
    tenders?: TenderIn[];
}

export const posService = {
    async startSale(registerID: string, customerID?: string): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ register_id: registerID, customer_id: customerID || undefined }),
            }),
            'Failed to start the sale',
        );
        return response.json();
    },

    async getSale(id: string): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${id}`),
            'Failed to read the sale',
        );
        return response.json();
    },

    /**
     * Fetches the completed sale's own receipt through the authenticated client (a
     * plain new tab would carry no token) and shows it from a blob url in a tab
     * opened during the click. A blocked tab answers the url for a download link;
     * a refusal closes the tab and throws the server's words.
     */
    async printReceipt(saleId: string, layout?: 'thermal80'): Promise<{ url: string; blocked: boolean }> {
        const query = layout ? `?layout=${layout}` : '';
        return openAuthedPdf(`/api/v1/pos/transactions/${saleId}/receipt${query}`, 'Failed to read the receipt');
    },

    /** Seals the signer's typed name and drawn PNG (standard base64 or a data URL) to the completed sale. */
    async captureSignature(saleId: string, signerName: string, imagePngBase64: string): Promise<SealedSignature> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/signature`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ signer_name: signerName, image_png_base64: imagePngBase64 }),
            }),
            'Failed to seal the signature',
        );
        return response.json();
    },

    /** Sends the completed sale's receipt to the account's contact; a refusal carries the server's words. */
    async emailReceipt(saleId: string): Promise<void> {
        await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/receipt/email`, { method: 'POST' }),
            'Failed to email the receipt',
        );
    },

    async addItem(saleId: string, productID: string, quantity: string, uom?: string): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/items`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ line: { product_id: productID, quantity, uom } }),
            }),
            'Failed to add the item',
        );
        return response.json();
    },

    /** Sets a line's quantity on the sale's revision; the answer is the whole sale, the line repriced. */
    async changeQuantity(saleId: string, itemId: string, quantity: string, revision: number): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/items/${itemId}`, {
                method: 'PATCH',
                headers: JSON_HEADERS,
                body: JSON.stringify({ quantity, revision }),
            }),
            'Failed to change the quantity',
        );
        return response.json();
    },

    async removeItem(saleId: string, itemId: string): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/items/${itemId}`, { method: 'DELETE' }),
            'Failed to remove the item',
        );
        return response.json();
    },

    /** The money moment: the tenders in cents on the sale's revision. */
    async completeSale(saleId: string, revision: number, tenders: TenderIn[]): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/complete`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify({ tenders }),
            }),
            'Failed to complete the sale',
        );
        return response.json();
    },

    async voidSale(saleId: string, revision: number, reason: string): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/void`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify({ reason }),
            }),
            'Failed to void the sale',
        );
        return response.json();
    },

    /** The day's sales: one page, newest first (the page is the whole day). */
    async listSales(registerID?: string, date?: string): Promise<SalePage> {
        const params = new URLSearchParams();
        if (registerID) params.append('register_id', registerID);
        if (date) params.append('date', date);
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions?${params.toString()}`),
            'Failed to list the day\u2019s sales',
        );
        return response.json();
    },

    async todaySales(registerID?: string): Promise<SaleSummary[]> {
        return (await this.listSales(registerID)).items;
    },

    async searchProducts(query: string): Promise<QuickSearchResult[]> {
        if (query.length < 2) return [];
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/products/search?q=${encodeURIComponent(query)}`),
            'Failed to search products',
        );
        return response.json();
    },

    /** The counter's catalogue for the caller's yard: category chips and product tiles. */
    async tiles(query: TilesQuery = {}): Promise<CatalogTiles> {
        const params = new URLSearchParams();
        if (query.category) params.append('category', query.category);
        if (query.q) params.append('q', query.q);
        if (query.limit) params.append('limit', String(query.limit));
        const qs = params.toString();
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/tiles${qs ? `?${qs}` : ''}`),
            'Failed to read the catalogue',
        );
        return response.json();
    },

    // --- The counter's customer ---

    /** The registers the caller may use, by branch then name. */
    async listRegisters(): Promise<CounterRegister[]> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/registers`),
            'Failed to list the registers',
        );
        const page: { items: CounterRegister[] } = await response.json();
        return page.items;
    },

    /** Customers by name or account number, each with the account's standing. */
    async findCustomers(query: string): Promise<CounterCustomer[]> {
        if (query.trim().length < 2) return [];
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/customers?q=${encodeURIComponent(query.trim())}`),
            'Failed to look up customers',
        );
        return response.json();
    },

    /**
     * Can a sale of amountCents go on this account? The verdict, its reason in
     * words and the ways forward; the sale makes the read count the sale's
     * live manager override.
     */
    async standing(customerId: string, amountCents = 0, saleId?: string): Promise<CounterStanding> {
        const params = new URLSearchParams({ amount_cents: String(Math.max(0, Math.trunc(amountCents))) });
        if (saleId) params.append('sale_id', saleId);
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/customers/${customerId}/standing?${params.toString()}`),
            'Failed to read the account\u2019s standing',
        );
        return response.json();
    },

    /** A manager approves one sale (kind account or check), with a reason and a cap in cents. */
    async approveOverride(saleId: string, kind: 'account' | 'check', reason: string, capCents: number,
        approvalToken?: string): Promise<CounterOverride> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/overrides`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ kind, reason, cap_cents: capCents, ...(approvalToken ? { approval_token: approvalToken } : {}) }),
            }),
            'Failed to record the approval',
        );
        return response.json();
    },

    // --- The manager step up (CP-04) ---

    /** The managers the picker offers for one act: active, a PIN set, a role the act admits. */
    async stepUpManagers(act: 'override' | 'pickup' | 'signoff'): Promise<StepUpManager[]> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/step-up/managers?act=${act}`),
            'Failed to load the managers',
        );
        const page = (await response.json()) as { items?: StepUpManager[] };
        return Array.isArray(page.items) ? page.items : [];
    },

    /** A manager approves at the counter with their own PIN; one act spends the answer. */
    async stepUp(act: 'override' | 'pickup' | 'signoff', manager: string, pin: string, targetId: string,
        amountCents?: number): Promise<StepUpApproval> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/step-up`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({
                    act, manager, pin, target_id: targetId,
                    ...(amountCents !== undefined ? { amount_cents: amountCents } : {}),
                }),
            }),
            'Failed to ask the manager',
        );
        return response.json();
    },

    /** Names the sale's customer (before the first line), purchase order, job and buyer. */
    async attachCustomer(
        saleId: string,
        input: { customer_id: string | null; customer_po?: string | null; job_id?: string | null; buyer_contact_id?: string | null },
    ): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/customer`, {
                method: 'PUT',
                headers: JSON_HEADERS,
                body: JSON.stringify(input),
            }),
            'Failed to name the customer',
        );
        return response.json();
    },

    // --- Returns ---

    async createReturn(input: {
        register_id?: string;
        original_sale_id?: string;
        customer_id?: string;
        refund_method: RefundMethod;
        reason?: string;
        lines: ReturnLineIn[];
    }): Promise<CounterReturn> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/returns`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(input),
            }),
            'Failed to take the return',
        );
        return response.json();
    },

    /** The closed list of return reasons, each with the restocking fee its returns carry. */
    async returnReasons(): Promise<ReturnReason[]> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/return-reasons`),
            'Failed to read the return reasons',
        );
        const page: { items: ReturnReason[] } = await response.json();
        return page.items;
    },

    // --- The exchange: goods back and goods out in one act ---

    /** What an exchange comes to, stated by the server; nothing is written. */
    async previewExchange(input: ExchangeIn): Promise<ExchangeFigures> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/exchanges/preview`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(input),
            }),
            'Failed to price the exchange',
        );
        return response.json();
    },

    async createExchange(input: ExchangeIn): Promise<Exchange> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/exchanges`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(input),
            }),
            'Failed to take the exchange',
        );
        return response.json();
    },

    // --- Till sessions (drawer lifecycle) ---

    /** The register's open session, or null when none is open. */
    async currentTill(registerID: string): Promise<TillSession | null> {
        const response = await fetchWithAuth(
            `${API_URL}/api/v1/pos/till/current?register_id=${encodeURIComponent(registerID)}`,
        );
        if (response.status === 404) return null;
        if (!response.ok) throw await parseApiError(response, 'Failed to look up the till');
        return response.json();
    },

    async openTill(registerID: string, openingFloatCents: number): Promise<TillSession> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/till/open`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ register_id: registerID, opening_float_cents: openingFloatCents }),
            }),
            'Failed to open the till',
        );
        return response.json();
    },

    async tillReport(sessionID: string): Promise<TillReport> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/till/${sessionID}/report`),
            'Failed to load the till report',
        );
        return response.json();
    },

    async closeTill(sessionID: string, countedByMethod: Record<string, number>, notes: string): Promise<TillReport> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/till/${sessionID}/close`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ counted_by_method: countedByMethod, notes }),
            }),
            'Failed to close the till',
        );
        return response.json();
    },

    /* ---- The card terminal's intents (CP-05) ---- */

    /** One card leg's own record: the desk's waiting state reads it while the terminal has the card. */
    async getTerminalIntent(id: string): Promise<TerminalIntent> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/terminal-intents/${id}`),
            'Failed to read the card terminal intent',
        );
        return response.json();
    },

    /**
     * The desk's cancel of a waiting card leg, honest about the device: the
     * server refuses it when the terminal has the leg, and the button's own
     * words say the charge may still go through.
     */
    async cancelTerminalIntent(id: string): Promise<TerminalIntent> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/terminal-intents/${id}/cancel`, { method: 'POST' }),
            'Failed to cancel the card terminal intent',
        );
        return response.json();
    },

    /** The manager's own trigger of the sweep act; the worker runs it too. */
    async sweepTerminalIntents(): Promise<TerminalSweepResult> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/terminal-intents/sweep`, { method: 'POST' }),
            'Failed to sweep the card terminal intents',
        );
        return response.json();
    },

    /** The manager's queue of card legs the sweep lost: who resolves it, what amount, the device reference. */
    async listTerminalLostItems(): Promise<TerminalLostItem[]> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/terminal-lost-items`),
            'Failed to read the lost card answers',
        );
        const body = await response.json();
        return body.items ?? [];
    },

    /** The manager's settlement of one lost answer: what the terminal's own record said. */
    async resolveTerminalLostItem(id: string, outcome: 'authorized' | 'declined', reference: string, reason: string): Promise<TerminalLostItem> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/terminal-lost-items/${id}/resolve`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ outcome, reference, reason }),
            }),
            'Failed to resolve the lost card answer',
        );
        return response.json();
    },

    // --- Till movements, variance sign off and the branch day (CP-08, CP-09) ---

    /** A safe drop, pickup, paid in or paid out against the open drawer. */
    async recordMovement(sessionID: string, input: MovementRequest): Promise<TillMovement> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/till/${sessionID}/movements`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(input),
            }),
            'Failed to record the movement',
        );
        return response.json();
    },

    /** The counted drawers waiting for a manager (finance and owner only). */
    async pendingSignoffs(): Promise<TillSession[]> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/till/signoffs`),
            'Failed to load the drawers waiting for sign off',
        );
        const page = (await response.json()) as { items?: TillSession[] };
        return Array.isArray(page.items) ? page.items : [];
    },

    async signOffTill(sessionID: string, reason: string, approvalToken?: string): Promise<TillReport> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/till/${sessionID}/signoff`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ reason, ...(approvalToken ? { approval_token: approvalToken } : {}) }),
            }),
            'Failed to sign off the till',
        );
        return response.json();
    },

    /** The branch's end of day summary; no date is the branch's today. */
    async branchDay(branchID: string, date?: string): Promise<BranchDay> {
        const params = new URLSearchParams({ branch_id: branchID });
        if (date) params.set('date', date);
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/end-of-day?${params.toString()}`),
            'Failed to load the branch day',
        );
        return response.json();
    },

    async closeBranchDay(branchID: string, date: string, depositSlipRef: string, bankAccountID?: string): Promise<BranchDay> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/end-of-day/close`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({
                    branch_id: branchID,
                    date,
                    deposit_slip_ref: depositSlipRef,
                    ...(bankAccountID ? { bank_account_id: bankAccountID } : {}),
                }),
            }),
            'Failed to close the branch day',
        );
        return response.json();
    },

    async reopenBranchDay(closeID: string, reason: string): Promise<BranchDay> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/end-of-day/${closeID}/reopen`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ reason }),
            }),
            'Failed to reopen the branch day',
        );
        return response.json();
    },

    async depositCandidates(closeID: string): Promise<DepositCandidate[]> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/end-of-day/${closeID}/deposit-candidates`),
            'Failed to load the bank statement lines',
        );
        const page = (await response.json()) as { items?: DepositCandidate[] };
        return Array.isArray(page.items) ? page.items : [];
    },

    async matchDeposit(closeID: string, bankTransactionID: string): Promise<DayClose> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/end-of-day/${closeID}/deposit-match`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ bank_transaction_id: bankTransactionID }),
            }),
            'Failed to match the deposit',
        );
        return response.json();
    },
};
