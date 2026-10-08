// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { creditLimitInputText, customerRequestFromCustomer, parseCreditLimitCents } from '../../services/CustomerService.ts';
import { CUSTOMER_TIERS } from '../../types/customer.ts';
import type { Customer, CustomerRequest, CustomerTier, PaymentTermsRecord } from '../../types/customer.ts';
import { BUTTON_PRIMARY, BUTTON_QUIET, INPUT_CLASS, checkbox, idPrefix, labeled, orNull, textInput } from './formFields.ts';

/**
 * The customer header form, for create (no `customer`) and edit. It builds the whole header
 * request, because a PUT replaces the header and a field left out takes its default, and emits
 * it as `customer-submit`; the page makes the call and hands the server's field problems back
 * through `fieldErrors`.
 */
@customElement('gable-customer-form')
export class GableCustomerForm extends LitElement {
    createRenderRoot() { return this; }

    @property({ attribute: false }) customer: Customer | null = null;
    @property({ attribute: false }) terms: PaymentTermsRecord[] = [];
    @property({ attribute: false }) fieldErrors: Record<string, string> = {};
    @property({ type: Boolean }) submitting = false;
    @property({ type: String }) formError = '';

    @state() private name = '';
    @state() private accountNumber = '';
    @state() private email = '';
    @state() private phone = '';
    @state() private address = '';
    @state() private tier: CustomerTier = 'retail';
    @state() private termsId = '';
    @state() private creditLimit = '';
    @state() private poRequired = false;
    @state() private isActive = true;
    @state() private creditError = '';

    private readonly ids = idPrefix('cust');
    private seeded = false;

    willUpdate() {
        if (!this.seeded) {
            this.seeded = true;
            const c = this.customer;
            if (c) {
                this.name = c.name;
                this.accountNumber = c.account_number;
                this.email = c.email ?? '';
                this.phone = c.phone ?? '';
                this.address = c.address ?? '';
                this.tier = c.tier;
                this.termsId = c.payment_terms_id;
                this.creditLimit = creditLimitInputText(c.credit_limit_cents);
                this.poRequired = c.po_required;
                this.isActive = c.is_active;
            }
        }
        // Create: preselect the dealer's usual terms once they have loaded.
        if (!this.customer && this.termsId === '' && this.terms.length > 0) {
            this.termsId = (this.terms.find(t => t.code === 'NET30') ?? this.terms[0]).id;
        }
    }

    private submit(e: Event) {
        e.preventDefault();
        const limit = parseCreditLimitCents(this.creditLimit);
        if (!limit.ok) {
            this.creditError = limit.message;
            return;
        }
        this.creditError = '';
        // An edit starts from the loaded header, so fields the form does not show (currency,
        // price level, salesperson) go back unchanged.
        const base: CustomerRequest = this.customer
            ? customerRequestFromCustomer(this.customer)
            : { account_number: '', name: '' };
        const request: CustomerRequest = {
            ...base,
            account_number: this.accountNumber.trim(),
            name: this.name.trim(),
            email: orNull(this.email),
            phone: orNull(this.phone),
            address: orNull(this.address),
            tier: this.tier,
            credit_limit_cents: limit.cents,
            po_required: this.poRequired,
            is_active: this.isActive,
        };
        if (this.termsId) request.payment_terms_id = this.termsId;
        this.dispatchEvent(new CustomEvent<CustomerRequest>('customer-submit', { detail: request, bubbles: true }));
    }

    private cancel() {
        this.dispatchEvent(new CustomEvent('customer-cancel', { bubbles: true }));
    }

    /** The loaded customer's terms stay selectable even when they have since been deactivated. */
    private get termOptions(): { id: string; label: string }[] {
        const opts = this.terms.map(t => ({ id: t.id, label: `${t.code} - ${t.name}` }));
        const c = this.customer;
        if (c && !opts.some(o => o.id === c.payment_terms_id)) {
            opts.unshift({ id: c.payment_terms_id, label: `${c.payment_terms.code} - ${c.payment_terms.name}` });
        }
        return opts;
    }

    render() {
        const id = (n: string) => `${this.ids}-${n}`;
        const err = this.fieldErrors;
        const editing = this.customer !== null;
        return html`
            <form @submit=${(e: Event) => this.submit(e)} class="p-5 bg-slate-steel border border-white/10 rounded-2xl space-y-4" aria-label=${editing ? 'Edit customer' : 'New customer'}>
                <h2 class="text-white font-semibold">${editing ? 'Edit customer' : 'New customer'}</h2>
                ${this.formError ? html`<div class="text-sm text-red-400 whitespace-pre-line" role="alert">${this.formError}</div>` : nothing}
                <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                    ${labeled(id('name'), 'Name', textInput(id('name'), this.name, v => { this.name = v; }, { required: true }), err.name)}
                    ${labeled(id('acct'), 'Account number', textInput(id('acct'), this.accountNumber, v => { this.accountNumber = v; }, { required: true, mono: true }), err.account_number)}
                    ${labeled(id('email'), 'Email', textInput(id('email'), this.email, v => { this.email = v; }, { type: 'email' }), err.email)}
                    ${labeled(id('phone'), 'Phone', textInput(id('phone'), this.phone, v => { this.phone = v; }), err.phone)}
                    <div class="md:col-span-2">
                        ${labeled(id('address'), 'Billing address', textInput(id('address'), this.address, v => { this.address = v; }), err.address)}
                    </div>
                    ${labeled(
                        id('tier'),
                        'Tier',
                        html`<select id=${id('tier')} .value=${this.tier} @change=${(e: Event) => { this.tier = (e.target as HTMLSelectElement).value as CustomerTier; }} class=${INPUT_CLASS}>
                            ${CUSTOMER_TIERS.map(t => html`<option value=${t} ?selected=${t === this.tier}>${t.charAt(0).toUpperCase() + t.slice(1)}</option>`)}
                        </select>`,
                        err.tier,
                    )}
                    ${labeled(
                        id('terms'),
                        'Payment terms',
                        html`<select id=${id('terms')} .value=${this.termsId} @change=${(e: Event) => { this.termsId = (e.target as HTMLSelectElement).value; }} class=${INPUT_CLASS}>
                            ${this.termOptions.map(o => html`<option value=${o.id} ?selected=${o.id === this.termsId}>${o.label}</option>`)}
                        </select>`,
                        err.payment_terms_id,
                    )}
                    ${labeled(
                        id('limit'),
                        'Credit limit (dollars)',
                        textInput(id('limit'), this.creditLimit, v => { this.creditLimit = v; }, { mono: true, inputmode: 'decimal', placeholder: 'Empty means no limit' }),
                        this.creditError || err.credit_limit_cents,
                        'Leave empty for no limit. 0 means no credit (cash only).',
                    )}
                    <div class="flex flex-col justify-end gap-2 pb-1">
                        ${checkbox(id('po'), 'Purchase order required', this.poRequired, v => { this.poRequired = v; })}
                        ${editing ? checkbox(id('active'), 'Active', this.isActive, v => { this.isActive = v; }) : nothing}
                        ${err.po_required ? html`<p class="text-xs text-red-400" role="alert">${err.po_required}</p>` : nothing}
                    </div>
                </div>
                <div class="flex justify-end gap-2">
                    <button type="button" class=${BUTTON_QUIET} @click=${() => this.cancel()}>Cancel</button>
                    <button type="submit" ?disabled=${this.submitting} class=${BUTTON_PRIMARY}>
                        ${this.submitting ? 'Saving...' : editing ? 'Save changes' : 'Create customer'}
                    </button>
                </div>
            </form>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-customer-form': GableCustomerForm;
    }
}
