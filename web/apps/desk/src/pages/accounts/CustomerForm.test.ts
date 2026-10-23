// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect, afterEach } from 'vitest';
import './CustomerForm.ts';
import type { GableCustomerForm } from './CustomerForm.ts';
import type { Customer, CustomerRequest } from '../../types/customer.ts';

// The Job required toggle sits beside Purchase order required and rides the
// form's whole header request, so a PUT never clears it by leaving it out.

const toggle = (el: GableCustomerForm, labelText: string): HTMLInputElement | null => {
    for (const label of Array.from(el.querySelectorAll('label'))) {
        if (label.textContent?.trim() === labelText) return label.querySelector('input[type="checkbox"]');
    }
    return null;
};

const customer = (over: Partial<Customer> = {}): Customer => ({
    id: 'c1', account_number: 'A-100', name: 'Smith Builders', email: null, phone: null, address: null,
    is_active: true, primary_branch_id: 'b', branch_ids: ['b'], price_profile_id: null, salesperson_id: null,
    salesperson_name: null, credit_limit_cents: null, balance_cents: 0, currency: null, effective_currency: 'USD',
    payment_terms_id: 't1', payment_terms: { id: 't1', code: 'NET30', name: 'Net 30' },
    po_required: false, job_required: false, named_buyer_required: false, check_limit_cents: null,
    revision: 1, created_at: '', updated_at: '',
    ...over,
} as unknown as Customer);

async function mount(c: Customer | null): Promise<GableCustomerForm> {
    const el = document.createElement('gable-customer-form') as GableCustomerForm;
    document.body.appendChild(el);
    el.customer = c;
    el.terms = [{ id: 't1', code: 'NET30', name: 'Net 30' }] as never;
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    await el.updateComplete;
    return el;
}

const submitOf = (el: GableCustomerForm): Promise<CustomerRequest> =>
    new Promise((resolve) => {
        el.addEventListener('customer-submit', (e: Event) => resolve((e as CustomEvent<CustomerRequest>).detail), { once: true });
        el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    });

describe('GableCustomerForm job required', () => {
    afterEach(() => { document.body.innerHTML = ''; });

    it('shows the Job required toggle seeded from the customer, beside the PO toggle', async () => {
        const el = await mount(customer({ po_required: true, job_required: true }));
        const po = toggle(el, 'Purchase order required');
        const job = toggle(el, 'Job required');
        expect(po?.checked).toBe(true);
        expect(job?.checked).toBe(true);
    });

    it('carries the toggled value in the whole header request', async () => {
        const el = await mount(customer({ job_required: false }));
        const job = toggle(el, 'Job required');
        job!.checked = true;
        job!.dispatchEvent(new Event('change', { bubbles: true }));
        await el.updateComplete;
        const got = await submitOf(el);
        expect(got.job_required).toBe(true);
        expect(got.po_required).toBe(false);
    });

    it('defaults the toggle off on create and sends it', async () => {
        const el = await mount(null);
        const job = toggle(el, 'Job required');
        expect(job?.checked).toBe(false);
        job!.checked = true;
        job!.dispatchEvent(new Event('change', { bubbles: true }));
        await el.updateComplete;
        const got = await submitOf(el);
        expect(got.job_required).toBe(true);
    });
});
