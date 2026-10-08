// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The customer wire (ADR 0001 and ADR 0005 section 7): cursor lists, quoted If-Match on
 * every write, integer cents, and the one error envelope. Every failure is thrown as an
 * ApiError, so a page can show the message, the field details and the stale revision.
 */
import type {
    Contact,
    ContactPage,
    ContactRequest,
    ContactUpdateRequest,
    Customer,
    CustomerPage,
    CustomerRequest,
    CustomerUpdateRequest,
    CustomerTier,
    EscalationPolicy,
    EscalationPolicyRequest,
    PaymentTermsRecordPage,
    PriceLevelPage,
    ShipTo,
    ShipToPage,
    ShipToRequest,
} from '../types/customer';
import { fetchWithAuth } from './fetchClient';
import { ifMatch, parseApiError } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

export interface ListCustomersParams {
    /** Case blind match on name, account number and email (at most 100 characters). */
    q?: string;
    tier?: CustomerTier;
    isActive?: boolean;
    salespersonId?: string;
    limit?: number;
    cursor?: string | null;
    /** Ask for the total row count (include=total). */
    includeTotal?: boolean;
}

export interface ListChildParams {
    isActive?: boolean | null;
    limit?: number;
    cursor?: string | null;
    includeTotal?: boolean;
}

function toQuery(entries: [string, string | undefined | null][]): string {
    const q = new URLSearchParams();
    for (const [k, v] of entries) if (v !== undefined && v !== null && v !== '') q.set(k, v);
    const s = q.toString();
    return s ? `?${s}` : '';
}

export function buildCustomerQuery(params: ListCustomersParams = {}): string {
    return toQuery([
        ['q', params.q?.trim()],
        ['tier', params.tier],
        ['is_active', params.isActive === undefined ? undefined : String(params.isActive)],
        ['salesperson_id', params.salespersonId],
        ['limit', params.limit ? String(params.limit) : undefined],
        ['cursor', params.cursor],
        ['include', params.includeTotal ? 'total' : undefined],
    ]);
}

export function buildChildQuery(params: ListChildParams = {}): string {
    return toQuery([
        ['is_active', params.isActive === undefined || params.isActive === null ? undefined : String(params.isActive)],
        ['limit', params.limit ? String(params.limit) : undefined],
        ['cursor', params.cursor],
        ['include', params.includeTotal ? 'total' : undefined],
    ]);
}

/**
 * Parses a credit limit typed in dollars into integer cents with string arithmetic, never a float.
 * Empty (or blank) means no limit and gives null; "0" gives 0 (no credit). A leading "$" and comma
 * thousands separators are tolerated; a sign, a third fraction digit, letters, or more than
 * 13 whole digits are refused.
 */
export function parseCreditLimitCents(text: string): { ok: true; cents: number | null } | { ok: false; message: string } {
    const t = text.trim().replace(/^\$/, '').replace(/,/g, '').trim();
    if (t === '') return { ok: true, cents: null };
    if (t.startsWith('-')) return { ok: false, message: 'A credit limit cannot be negative' };
    const m = /^(\d*)(?:\.(\d{0,2}))?$/.exec(t);
    if (!m || (m[1] === '' && !m[2])) {
        return { ok: false, message: /^\d*\.\d{3,}$/.test(t) ? 'Use at most two decimal places' : 'Enter an amount in dollars, for example 5000 or 5000.50' };
    }
    const whole = m[1] === '' ? '0' : m[1].replace(/^0+(?=\d)/, '');
    if (whole.length > 13) return { ok: false, message: 'That credit limit is too large' };
    const frac = (m[2] ?? '').padEnd(2, '0');
    return { ok: true, cents: Number(whole) * 100 + Number(frac) };
}

/** The text a credit limit input shows for a stored value: null is empty, 500050 is "5000.50". Integer arithmetic only. */
export function creditLimitInputText(cents: number | null | undefined): string {
    if (cents === null || cents === undefined) return '';
    const whole = Math.trunc(cents / 100);
    const frac = String(cents % 100).padStart(2, '0');
    return `${whole}.${frac}`;
}

/** The whole header of a loaded customer as a PUT body (a field left out takes its default, so send all of them). */
export function customerRequestFromCustomer(c: Customer): CustomerUpdateRequest {
    return {
        account_number: c.account_number,
        name: c.name,
        email: c.email,
        phone: c.phone,
        address: c.address,
        tier: c.tier,
        is_active: c.is_active,
        price_level_id: c.price_level_id,
        salesperson_id: c.salesperson_id,
        credit_limit_cents: c.credit_limit_cents,
        currency: c.currency,
        payment_terms_id: c.payment_terms_id,
        po_required: c.po_required,
    };
}

export function shipToRequestFromShipTo(s: ShipTo): ShipToRequest {
    return {
        code: s.code,
        name: s.name,
        line1: s.line1,
        line2: s.line2,
        city: s.city,
        region: s.region,
        postal_code: s.postal_code,
        country: s.country,
        phone: s.phone,
        delivery_instructions: s.delivery_instructions,
        tax_rate_percent: s.tax_rate_percent,
        is_default: s.is_default,
        is_active: s.is_active,
    };
}

export function contactRequestFromContact(c: Contact): ContactUpdateRequest {
    return {
        first_name: c.first_name,
        last_name: c.last_name,
        title: c.title,
        email: c.email,
        phone: c.phone,
        role: c.role,
        is_primary: c.is_primary,
        is_active: c.is_active,
        can_place_orders: c.can_place_orders,
        order_limit_cents: c.order_limit_cents,
    };
}

const JSON_HEADERS = { 'Content-Type': 'application/json' };

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (!response.ok) throw await parseApiError(response, fallback);
    return response;
}

/** The most customers listAllCustomers collects (pages of 200), so a huge dealer never hangs the omnibar. */
export const LIST_ALL_CUSTOMERS_CAP = 2000;

export const CustomerService = {
    async listCustomers(params: ListCustomersParams = {}): Promise<CustomerPage> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/customers${buildCustomerQuery(params)}`),
            'Failed to fetch customers',
        );
        return response.json();
    },

    /** Pages by cursor until the last page or LIST_ALL_CUSTOMERS_CAP customers, for pickers and the omnibar. */
    async listAllCustomers(): Promise<Customer[]> {
        const all: Customer[] = [];
        let cursor: string | null = null;
        while (all.length < LIST_ALL_CUSTOMERS_CAP) {
            const page: CustomerPage = await CustomerService.listCustomers({ limit: 200, cursor });
            all.push(...page.items);
            if (!page.next_cursor) break;
            cursor = page.next_cursor;
        }
        return all.slice(0, LIST_ALL_CUSTOMERS_CAP);
    },

    async getCustomer(id: string): Promise<Customer> {
        const response = await expectOk(await fetchWithAuth(`${API_URL}/api/v1/customers/${id}`), 'Failed to fetch customer');
        return response.json();
    },

    async createCustomer(request: CustomerRequest): Promise<Customer> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/customers`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(request),
            }),
            'Failed to create customer',
        );
        return response.json();
    },

    /** Replaces the header on the revision the page loaded. */
    async updateCustomer(id: string, request: CustomerUpdateRequest, revision: number): Promise<Customer> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/customers/${id}`, {
                method: 'PUT',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify(request),
            }),
            'Failed to update customer',
        );
        return response.json();
    },

    async updateSalesperson(id: string, salespersonId: string | null, revision: number): Promise<Customer> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/customers/${id}/salesperson`, {
                method: 'PATCH',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify({ salesperson_id: salespersonId }),
            }),
            'Failed to update salesperson',
        );
        return response.json();
    },

    async getEscalationPolicy(customerId: string): Promise<EscalationPolicy> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/customers/${customerId}/escalation-policy`),
            'Failed to fetch escalation policy',
        );
        return response.json();
    },

    /** The revision is the customer's, as the policy read carried it. */
    async setEscalationPolicy(customerId: string, request: EscalationPolicyRequest, revision: number): Promise<EscalationPolicy> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/customers/${customerId}/escalation-policy`, {
                method: 'PUT',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify(request),
            }),
            'Failed to update escalation policy',
        );
        return response.json();
    },

    // Ship-to addresses

    async listShipTos(customerId: string, params: ListChildParams = {}): Promise<ShipToPage> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/customers/${customerId}/ship-tos${buildChildQuery(params)}`),
            'Failed to fetch ship-to addresses',
        );
        return response.json();
    },

    async getShipTo(id: string): Promise<ShipTo> {
        const response = await expectOk(await fetchWithAuth(`${API_URL}/api/v1/ship-tos/${id}`), 'Failed to fetch ship-to address');
        return response.json();
    },

    async createShipTo(customerId: string, request: ShipToRequest): Promise<ShipTo> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/customers/${customerId}/ship-tos`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(request),
            }),
            'Failed to create ship-to address',
        );
        return response.json();
    },

    async updateShipTo(id: string, request: ShipToRequest, revision: number): Promise<ShipTo> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/ship-tos/${id}`, {
                method: 'PUT',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify(request),
            }),
            'Failed to update ship-to address',
        );
        return response.json();
    },

    // Contacts

    async listContacts(customerId: string, params: ListChildParams = {}): Promise<ContactPage> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/customers/${customerId}/contacts${buildChildQuery(params)}`),
            'Failed to fetch contacts',
        );
        return response.json();
    },

    async createContact(customerId: string, request: ContactRequest): Promise<Contact> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/customers/${customerId}/contacts`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(request),
            }),
            'Failed to create contact',
        );
        return response.json();
    },

    async updateContact(id: string, request: ContactUpdateRequest, revision: number): Promise<Contact> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/contacts/${id}`, {
                method: 'PUT',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify(request),
            }),
            'Failed to update contact',
        );
        return response.json();
    },

    /** A DELETE has no body: the revision travels in If-Match. */
    async deleteContact(id: string, revision: number): Promise<void> {
        await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/contacts/${id}`, {
                method: 'DELETE',
                headers: { 'If-Match': ifMatch(revision) },
            }),
            'Failed to delete contact',
        );
    },

    // Reference lists

    /** Active terms by default, because only active terms can be newly assigned; pass isActive: null for all of them. */
    async listPaymentTerms(params: ListChildParams = {}): Promise<PaymentTermsRecordPage> {
        const { isActive = true, ...rest } = params;
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/payment-terms${buildChildQuery({ ...rest, isActive })}`),
            'Failed to fetch payment terms',
        );
        return response.json();
    },

    async listPriceLevels(params: ListChildParams = {}): Promise<PriceLevelPage> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/price_levels${buildChildQuery({ ...params, isActive: undefined })}`),
            'Failed to fetch price levels',
        );
        return response.json();
    },
};
