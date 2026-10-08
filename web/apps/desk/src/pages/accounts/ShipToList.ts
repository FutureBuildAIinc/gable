// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { ToastService } from '../../lib/toast-service.ts';
import { CustomerService, shipToRequestFromShipTo } from '../../services/CustomerService.ts';
import { ApiError, apiErrorMessage, fieldErrorMap } from '../../services/apiError.ts';
import type { ShipTo, ShipToRequest } from '../../types/customer.ts';
import { BUTTON_PRIMARY, BUTTON_QUIET, checkbox, idPrefix, labeled, orNull, textInput } from './formFields.ts';

interface ShipToDraft {
    code: string;
    name: string;
    line1: string;
    line2: string;
    city: string;
    region: string;
    postal_code: string;
    country: string;
    phone: string;
    delivery_instructions: string;
    tax_rate_percent: string;
    is_default: boolean;
    is_active: boolean;
}

function emptyDraft(): ShipToDraft {
    return {
        code: '', name: '', line1: '', line2: '', city: '', region: '', postal_code: '',
        country: '', phone: '', delivery_instructions: '', tax_rate_percent: '', is_default: false, is_active: true,
    };
}

function draftFromShipTo(s: ShipTo): ShipToDraft {
    return {
        code: s.code, name: s.name, line1: s.line1, line2: s.line2 ?? '', city: s.city ?? '', region: s.region ?? '',
        postal_code: s.postal_code ?? '', country: s.country ?? '', phone: s.phone ?? '',
        delivery_instructions: s.delivery_instructions ?? '', tax_rate_percent: s.tax_rate_percent ?? '',
        is_default: s.is_default, is_active: s.is_active,
    };
}

/** The ship-to addresses of one customer: list, add, edit, and deactivate (a ship-to is never deleted). */
@customElement('gable-ship-to-list')
export class GableShipToList extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: String }) customerId = '';

    @state() private shipTos: ShipTo[] = [];
    @state() private nextCursor: string | null = null;
    @state() private loading = true;
    @state() private error: string | null = null;
    /** null: no form; 'new': the add form; otherwise the ship-to being edited, with the revision it was loaded at. */
    @state() private editing: 'new' | ShipTo | null = null;
    @state() private draft: ShipToDraft = emptyDraft();
    @state() private saving = false;
    @state() private formError = '';
    @state() private fieldErrors: Record<string, string> = {};

    private readonly ids = idPrefix('shipto');
    private loadedFor = '';

    connectedCallback() {
        super.connectedCallback();
        if (this.customerId) void this.load();
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('customerId') && this.customerId && this.customerId !== this.loadedFor) void this.load();
    }

    private async load() {
        this.loadedFor = this.customerId;
        try {
            this.loading = true;
            const page = await CustomerService.listShipTos(this.customerId, { limit: 200 });
            this.shipTos = this.sorted(page.items);
            this.nextCursor = page.next_cursor;
            this.error = null;
        } catch (err) {
            this.error = apiErrorMessage(err, 'Failed to load ship-to addresses');
        } finally {
            this.loading = false;
        }
    }

    private async loadMore() {
        if (!this.nextCursor) return;
        try {
            const page = await CustomerService.listShipTos(this.customerId, { limit: 200, cursor: this.nextCursor });
            this.shipTos = this.sorted([...this.shipTos, ...page.items]);
            this.nextCursor = page.next_cursor;
        } catch (err) {
            ToastService.show(`Failed to load more: ${apiErrorMessage(err)}`, 'error');
        }
    }

    /** The default first, then the active ones, then the rest, each by code. */
    private sorted(items: ShipTo[]): ShipTo[] {
        return [...items].sort((a, b) =>
            Number(b.is_default) - Number(a.is_default) || Number(b.is_active) - Number(a.is_active) || a.code.localeCompare(b.code));
    }

    private openAdd() {
        this.editing = 'new';
        this.draft = emptyDraft();
        this.fieldErrors = {};
        this.formError = '';
    }

    private openEdit(s: ShipTo) {
        this.editing = s;
        this.draft = draftFromShipTo(s);
        this.fieldErrors = {};
        this.formError = '';
    }

    private set<K extends keyof ShipToDraft>(key: K, value: ShipToDraft[K]) {
        this.draft = { ...this.draft, [key]: value };
    }

    /** On create, is_default is left out unless ticked, so the customer's first active ship-to becomes the default. */
    private requestFromDraft(isNew: boolean): ShipToRequest {
        const d = this.draft;
        const request: ShipToRequest = {
            code: d.code.trim(),
            name: d.name.trim(),
            line1: d.line1.trim(),
            line2: orNull(d.line2),
            city: orNull(d.city),
            region: orNull(d.region),
            postal_code: orNull(d.postal_code),
            country: orNull(d.country.toUpperCase()),
            phone: orNull(d.phone),
            delivery_instructions: orNull(d.delivery_instructions),
            tax_rate_percent: orNull(d.tax_rate_percent),
            is_active: d.is_active,
        };
        if (!isNew || d.is_default) request.is_default = d.is_default;
        return request;
    }

    private async save(e: Event) {
        e.preventDefault();
        const target = this.editing;
        if (!target) return;
        this.saving = true;
        this.fieldErrors = {};
        this.formError = '';
        try {
            if (target === 'new') {
                await CustomerService.createShipTo(this.customerId, this.requestFromDraft(true));
                ToastService.show('Ship-to address added', 'success');
            } else {
                await CustomerService.updateShipTo(target.id, this.requestFromDraft(false), target.revision);
                ToastService.show('Ship-to address saved', 'success');
            }
            this.editing = null;
            await this.load();
        } catch (err) {
            if (err instanceof ApiError && err.isStaleRevision) {
                ToastService.show(`${err.message} The ship-to addresses were reloaded.`, 'error');
                this.editing = null;
                await this.load();
            } else {
                this.fieldErrors = fieldErrorMap(err);
                this.formError = apiErrorMessage(err, 'Failed to save ship-to address');
            }
        } finally {
            this.saving = false;
        }
    }

    /** Deactivates or reactivates through is_active, on the ship-to's own revision. */
    private async toggleActive(s: ShipTo) {
        try {
            await CustomerService.updateShipTo(s.id, { ...shipToRequestFromShipTo(s), is_active: !s.is_active, is_default: s.is_active ? false : s.is_default }, s.revision);
            ToastService.show(s.is_active ? 'Ship-to address deactivated' : 'Ship-to address reactivated', 'success');
        } catch (err) {
            if (err instanceof ApiError && err.isStaleRevision) {
                ToastService.show(`${err.message} The ship-to addresses were reloaded.`, 'error');
            } else {
                ToastService.show(apiErrorMessage(err, 'Failed to update ship-to address'), 'error');
            }
        }
        await this.load();
    }

    private renderForm() {
        const id = (n: string) => `${this.ids}-${n}`;
        const d = this.draft;
        const err = this.fieldErrors;
        const isNew = this.editing === 'new';
        return html`
            <form @submit=${(e: Event) => void this.save(e)} class="p-5 bg-slate-steel border border-white/10 rounded-2xl space-y-4" aria-label=${isNew ? 'Add ship-to address' : 'Edit ship-to address'}>
                <h3 class="text-white font-semibold">${isNew ? 'Add ship-to address' : 'Edit ship-to address'}</h3>
                ${this.formError ? html`<div class="text-sm text-red-400 whitespace-pre-line" role="alert">${this.formError}</div>` : nothing}
                <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                    ${labeled(id('code'), 'Code', textInput(id('code'), d.code, v => this.set('code', v), { required: true, mono: true, maxlength: 20 }), err.code, 'Unique for this customer, for example YARD1')}
                    ${labeled(id('name'), 'Ship-to name', textInput(id('name'), d.name, v => this.set('name', v), { required: true }), err.name)}
                    ${labeled(id('line1'), 'Address line 1', textInput(id('line1'), d.line1, v => this.set('line1', v), { required: true }), err.line1)}
                    ${labeled(id('line2'), 'Address line 2', textInput(id('line2'), d.line2, v => this.set('line2', v)), err.line2)}
                    ${labeled(id('city'), 'City', textInput(id('city'), d.city, v => this.set('city', v)), err.city)}
                    ${labeled(id('region'), 'Region', textInput(id('region'), d.region, v => this.set('region', v)), err.region)}
                    ${labeled(id('postal'), 'Postal code', textInput(id('postal'), d.postal_code, v => this.set('postal_code', v), { mono: true }), err.postal_code)}
                    ${labeled(id('country'), 'Country', textInput(id('country'), d.country, v => this.set('country', v), { maxlength: 2, mono: true, placeholder: 'US' }), err.country, 'Two capital letters')}
                    ${labeled(id('phone'), 'Phone', textInput(id('phone'), d.phone, v => this.set('phone', v)), err.phone)}
                    ${labeled(id('tax'), 'Tax rate percent', textInput(id('tax'), d.tax_rate_percent, v => this.set('tax_rate_percent', v), { mono: true, inputmode: 'decimal', placeholder: '8.875' }), err.tax_rate_percent, 'The rate a delivery here is taxed at; empty for none')}
                    <div class="md:col-span-2">
                        ${labeled(id('instr'), 'Delivery instructions', textInput(id('instr'), d.delivery_instructions, v => this.set('delivery_instructions', v)), err.delivery_instructions)}
                    </div>
                </div>
                <div class="flex flex-wrap items-center gap-6">
                    ${checkbox(id('default'), 'Make default', d.is_default, v => this.set('is_default', v))}
                    ${!isNew ? checkbox(id('active'), 'Active', d.is_active, v => this.set('is_active', v)) : nothing}
                    ${err.is_default ? html`<span class="text-xs text-red-400" role="alert">${err.is_default}</span>` : nothing}
                    <div class="ml-auto flex gap-2">
                        <button type="button" class=${BUTTON_QUIET} @click=${() => { this.editing = null; }}>Cancel</button>
                        <button type="submit" ?disabled=${this.saving} class=${BUTTON_PRIMARY}>${this.saving ? 'Saving...' : 'Save ship-to'}</button>
                    </div>
                </div>
            </form>
        `;
    }

    render() {
        if (this.loading && this.shipTos.length === 0) return html`<div class="text-zinc-400">Loading ship-to addresses...</div>`;
        if (this.error) return html`<div class="text-red-400" role="alert">${this.error}</div>`;

        return html`
            <div class="space-y-4">
                <div class="flex justify-between items-center">
                    <h3 class="text-lg font-semibold text-white">Ship-to addresses</h3>
                    <button class=${BUTTON_PRIMARY} @click=${() => this.openAdd()}>Add ship-to</button>
                </div>

                ${this.editing ? this.renderForm() : nothing}

                ${this.shipTos.length === 0
                    ? html`<div class="text-center py-8 text-zinc-500 border border-dashed border-white/10 rounded-lg">No ship-to addresses yet.</div>`
                    : html`
                        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                            ${this.shipTos.map(s => html`
                                <div class="p-4 bg-slate-steel border border-white/10 rounded-lg ${s.is_active ? '' : 'opacity-60'}" data-ship-to=${s.code}>
                                    <div class="flex items-center justify-between gap-2">
                                        <div class="flex items-center gap-2">
                                            <span class="font-mono text-sm text-white bg-white/5 px-2 py-0.5 rounded border border-white/5">${s.code}</span>
                                            <span class="text-white font-medium">${s.name}</span>
                                        </div>
                                        <div class="flex gap-2">
                                            ${s.is_default ? html`<span class="px-2 py-0.5 rounded-full text-xs font-medium border bg-gable-green/10 text-gable-green border-gable-green/20">Default</span>` : nothing}
                                            ${s.is_active ? nothing : html`<span class="px-2 py-0.5 rounded-full text-xs font-medium border bg-red-500/10 text-red-400 border-red-500/20">Inactive</span>`}
                                        </div>
                                    </div>
                                    <div class="mt-2 text-sm text-zinc-400">
                                        <div>${s.line1}</div>
                                        ${s.line2 ? html`<div>${s.line2}</div>` : nothing}
                                        <div>${[s.city, s.region, s.postal_code].filter(Boolean).join(', ')}${s.country ? html` <span class="font-mono">${s.country}</span>` : nothing}</div>
                                        ${s.phone ? html`<div class="font-mono">${s.phone}</div>` : nothing}
                                        ${s.tax_rate_percent !== null ? html`<div>Tax rate <span class="font-mono text-zinc-300">${s.tax_rate_percent}%</span></div>` : nothing}
                                        ${s.delivery_instructions ? html`<div class="text-zinc-500 italic mt-1">${s.delivery_instructions}</div>` : nothing}
                                    </div>
                                    <div class="mt-3 flex gap-2">
                                        <button class=${BUTTON_QUIET} @click=${() => this.openEdit(s)}>Edit</button>
                                        <button class=${BUTTON_QUIET} @click=${() => void this.toggleActive(s)}>${s.is_active ? 'Deactivate' : 'Reactivate'}</button>
                                    </div>
                                </div>
                            `)}
                        </div>
                    `}

                ${this.nextCursor ? html`<div class="flex justify-center"><button class=${BUTTON_QUIET} @click=${() => void this.loadMore()}>Load more</button></div>` : nothing}
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-ship-to-list': GableShipToList;
    }
}
