// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { ToastService } from '../../lib/toast-service.ts';
import { formatCents } from '../../lib/utils.ts';
import { CustomerService, creditLimitInputText, parseCreditLimitCents } from '../../services/CustomerService.ts';
import { ApiError, apiErrorMessage, fieldErrorMap } from '../../services/apiError.ts';
import { CONTACT_ROLES } from '../../types/customer.ts';
import type { Contact, ContactRequest, ContactRole } from '../../types/customer.ts';
import { BUTTON_PRIMARY, BUTTON_QUIET, INPUT_CLASS, checkbox, idPrefix, labeled, orNull, textInput } from './formFields.ts';

interface ContactDraft {
    first_name: string;
    last_name: string;
    title: string;
    email: string;
    phone: string;
    role: ContactRole | '';
    is_primary: boolean;
    is_active: boolean;
    can_place_orders: boolean;
    order_limit: string;
}

function emptyDraft(): ContactDraft {
    return {
        first_name: '', last_name: '', title: '', email: '', phone: '', role: 'Buyer',
        is_primary: false, is_active: true, can_place_orders: true, order_limit: '',
    };
}

function draftFromContact(c: Contact): ContactDraft {
    return {
        first_name: c.first_name, last_name: c.last_name, title: c.title ?? '', email: c.email ?? '', phone: c.phone ?? '',
        role: c.role ?? '', is_primary: c.is_primary, is_active: c.is_active, can_place_orders: c.can_place_orders,
        order_limit: creditLimitInputText(c.order_limit_cents),
    };
}

/** The contacts of one customer. Edits and deletes carry the contact's own revision as If-Match. */
@customElement('gable-contact-list')
export class GableContactList extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: String }) customerId = '';

    @state() private contacts: Contact[] = [];
    @state() private nextCursor: string | null = null;
    @state() private loading = true;
    @state() private error: string | null = null;
    @state() private editing: 'new' | Contact | null = null;
    @state() private draft: ContactDraft = emptyDraft();
    @state() private saving = false;
    @state() private formError = '';
    @state() private fieldErrors: Record<string, string> = {};

    private readonly ids = idPrefix('contact');
    private loadedFor = '';

    connectedCallback() {
        super.connectedCallback();
        if (this.customerId) void this.fetchContacts();
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('customerId') && this.customerId && this.customerId !== this.loadedFor) void this.fetchContacts();
    }

    /** The list is newest first; the desk shows the primary contact first, then by name. */
    private sorted(items: Contact[]): Contact[] {
        return [...items].sort((a, b) =>
            Number(b.is_primary) - Number(a.is_primary) ||
            `${a.last_name} ${a.first_name}`.localeCompare(`${b.last_name} ${b.first_name}`));
    }

    private async fetchContacts() {
        this.loadedFor = this.customerId;
        try {
            this.loading = true;
            const page = await CustomerService.listContacts(this.customerId, { limit: 200 });
            this.contacts = this.sorted(page.items);
            this.nextCursor = page.next_cursor;
            this.error = null;
        } catch (err) {
            this.error = apiErrorMessage(err, 'Failed to load contacts');
        } finally {
            this.loading = false;
        }
    }

    private async loadMore() {
        if (!this.nextCursor) return;
        try {
            const page = await CustomerService.listContacts(this.customerId, { limit: 200, cursor: this.nextCursor });
            this.contacts = this.sorted([...this.contacts, ...page.items]);
            this.nextCursor = page.next_cursor;
        } catch (err) {
            ToastService.show(`Failed to load more: ${apiErrorMessage(err)}`, 'error');
        }
    }

    private openAdd() {
        this.editing = 'new';
        this.draft = emptyDraft();
        this.fieldErrors = {};
        this.formError = '';
    }

    private openEdit(c: Contact) {
        this.editing = c;
        this.draft = draftFromContact(c);
        this.fieldErrors = {};
        this.formError = '';
    }

    private set<K extends keyof ContactDraft>(key: K, value: ContactDraft[K]) {
        this.draft = { ...this.draft, [key]: value };
    }

    private async save(e: Event) {
        e.preventDefault();
        const target = this.editing;
        if (!target) return;
        const d = this.draft;
        // The order limit is dollars typed by a person; empty is no limit of the contact's own.
        const limit = parseCreditLimitCents(d.order_limit);
        if (!limit.ok) {
            this.fieldErrors = { order_limit_cents: limit.message };
            return;
        }
        const request: ContactRequest = {
            first_name: d.first_name.trim(),
            last_name: d.last_name.trim(),
            title: orNull(d.title),
            email: orNull(d.email),
            phone: orNull(d.phone),
            role: d.role === '' ? null : d.role,
            is_primary: d.is_primary,
            is_active: d.is_active,
            can_place_orders: d.can_place_orders,
            order_limit_cents: limit.cents,
        };
        this.saving = true;
        this.fieldErrors = {};
        this.formError = '';
        try {
            if (target === 'new') {
                await CustomerService.createContact(this.customerId, request);
                ToastService.show('Contact added', 'success');
            } else {
                await CustomerService.updateContact(target.id, { ...request, can_place_orders: d.can_place_orders, order_limit_cents: limit.cents }, target.revision);
                ToastService.show('Contact saved', 'success');
            }
            this.editing = null;
            await this.fetchContacts();
        } catch (err) {
            if (err instanceof ApiError && err.isStaleRevision) {
                ToastService.show(`${err.message} The contacts were reloaded.`, 'error');
                this.editing = null;
                await this.fetchContacts();
            } else {
                this.fieldErrors = fieldErrorMap(err);
                this.formError = apiErrorMessage(err, 'Failed to save contact');
            }
        } finally {
            this.saving = false;
        }
    }

    private async handleDelete(c: Contact) {
        if (!confirm('Are you sure you want to delete this contact?')) return;
        try {
            await CustomerService.deleteContact(c.id, c.revision);
            ToastService.show('Contact deleted', 'success');
        } catch (err) {
            if (err instanceof ApiError && err.isStaleRevision) {
                ToastService.show(`${err.message} The contacts were reloaded.`, 'error');
            } else {
                ToastService.show(apiErrorMessage(err, 'Failed to delete contact'), 'error');
            }
        }
        await this.fetchContacts();
    }

    private renderForm() {
        const id = (n: string) => `${this.ids}-${n}`;
        const d = this.draft;
        const err = this.fieldErrors;
        const isNew = this.editing === 'new';
        return html`
            <form @submit=${(e: Event) => void this.save(e)} class="mb-6 p-5 bg-slate-steel border border-white/10 rounded-2xl space-y-4" aria-label=${isNew ? 'Add contact' : 'Edit contact'}>
                ${this.formError ? html`<div class="text-sm text-red-400 whitespace-pre-line" role="alert">${this.formError}</div>` : nothing}
                <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                    ${labeled(id('first'), 'First name', textInput(id('first'), d.first_name, v => this.set('first_name', v), { required: true }), err.first_name)}
                    ${labeled(id('last'), 'Last name', textInput(id('last'), d.last_name, v => this.set('last_name', v), { required: true }), err.last_name)}
                    ${labeled(
                        id('role'),
                        'Role',
                        html`<select id=${id('role')} .value=${d.role} @change=${(e: Event) => this.set('role', (e.target as HTMLSelectElement).value as ContactRole | '')} class=${INPUT_CLASS}>
                            <option value="" ?selected=${d.role === ''}>No role</option>
                            ${CONTACT_ROLES.map(r => html`<option value=${r} ?selected=${d.role === r}>${r === 'AP' ? 'Accounts Payable' : r}</option>`)}
                        </select>`,
                        err.role,
                    )}
                    ${labeled(id('title'), 'Title', textInput(id('title'), d.title, v => this.set('title', v)), err.title)}
                    ${labeled(id('email'), 'Email', textInput(id('email'), d.email, v => this.set('email', v), { type: 'email' }), err.email)}
                    ${labeled(id('phone'), 'Phone', textInput(id('phone'), d.phone, v => this.set('phone', v)), err.phone)}
                    ${labeled(
                        id('limit'),
                        'Order limit (dollars)',
                        textInput(id('limit'), d.order_limit, v => this.set('order_limit', v), { mono: true, inputmode: 'decimal', placeholder: 'Empty means no limit' }),
                        err.order_limit_cents,
                        'The largest order this contact may place. Empty for no limit of their own.',
                    )}
                </div>
                <div class="flex flex-wrap items-center gap-6">
                    ${checkbox(id('primary'), 'Primary contact', d.is_primary, v => this.set('is_primary', v))}
                    ${checkbox(id('orders'), 'Can place orders', d.can_place_orders, v => this.set('can_place_orders', v))}
                    ${!isNew ? checkbox(id('active'), 'Active', d.is_active, v => this.set('is_active', v)) : nothing}
                    <div class="ml-auto flex gap-2">
                        <button type="button" class=${BUTTON_QUIET} @click=${() => { this.editing = null; }}>Cancel</button>
                        <button type="submit" ?disabled=${this.saving} class=${BUTTON_PRIMARY}>${this.saving ? 'Saving...' : 'Save contact'}</button>
                    </div>
                </div>
            </form>
        `;
    }

    render() {
        if (this.loading && this.contacts.length === 0) return html`<div class="text-zinc-400">Loading contacts...</div>`;
        if (this.error) return html`<div class="text-red-400" role="alert">${this.error}</div>`;

        return html`
            <div>
                <div class="flex justify-between items-center mb-6">
                    <h3 class="text-lg font-semibold text-white">Contacts</h3>
                    <button class=${BUTTON_PRIMARY} @click=${() => (this.editing ? (this.editing = null) : this.openAdd())}>
                        ${this.editing ? 'Cancel' : 'Add Contact'}
                    </button>
                </div>

                ${this.editing ? this.renderForm() : nothing}

                ${this.contacts.length === 0
                    ? html`<div class="text-center py-8 text-zinc-500 border border-dashed border-white/10 rounded-lg">No contacts found for this account.</div>`
                    : html`
                        <div class="border border-white/5 rounded-lg overflow-hidden bg-slate-steel/20">
                            <table class="w-full text-sm">
                                <thead class="bg-white/5 text-zinc-400 font-medium border-b border-white/5">
                                    <tr>
                                        <th scope="col" class="px-4 py-3 text-left">Name</th>
                                        <th scope="col" class="px-4 py-3 text-left">Role</th>
                                        <th scope="col" class="px-4 py-3 text-left">Contact info</th>
                                        <th scope="col" class="px-4 py-3 text-left">Orders</th>
                                        <th scope="col" class="px-4 py-3 text-right"><span class="sr-only">Actions</span></th>
                                    </tr>
                                </thead>
                                <tbody class="divide-y divide-white/5">
                                    ${this.contacts.map(c => html`
                                        <tr class="${c.is_active ? '' : 'opacity-60'}">
                                            <td class="px-4 py-3 text-white font-medium">
                                                ${c.first_name} ${c.last_name}
                                                ${c.is_primary ? html`<span class="ml-2 px-2 py-0.5 rounded text-xs font-medium bg-blue-500/10 text-blue-400 border border-blue-500/20">Primary</span>` : nothing}
                                                ${c.is_active ? nothing : html`<span class="ml-2 px-2 py-0.5 rounded text-xs font-medium bg-red-500/10 text-red-400 border border-red-500/20">Inactive</span>`}
                                                ${c.title ? html`<div class="text-xs text-zinc-500 font-normal">${c.title}</div>` : nothing}
                                            </td>
                                            <td class="px-4 py-3 text-zinc-400">${c.role ?? html`<span class="text-zinc-600">None</span>`}</td>
                                            <td class="px-4 py-3 text-zinc-400">
                                                ${c.email ? html`<div class="truncate">${c.email}</div>` : nothing}
                                                ${c.phone ? html`<div class="font-mono">${c.phone}</div>` : nothing}
                                                ${!c.email && !c.phone ? html`<span class="text-zinc-600 italic">No contact info</span>` : nothing}
                                            </td>
                                            <td class="px-4 py-3 text-zinc-400">
                                                ${c.can_place_orders
                                                    ? html`<span class="text-emerald-400">Can place orders</span>
                                                        <div class="font-mono text-xs text-zinc-500">${c.order_limit_cents === null ? 'No limit' : `Up to ${formatCents(c.order_limit_cents)}`}</div>`
                                                    : html`<span class="text-zinc-500">Cannot place orders</span>`}
                                            </td>
                                            <td class="px-4 py-3 text-right whitespace-nowrap">
                                                <button class="text-zinc-300 hover:text-white mr-4" @click=${() => this.openEdit(c)}>Edit</button>
                                                <button class="text-red-400 hover:text-red-300" @click=${() => void this.handleDelete(c)}>Delete</button>
                                            </td>
                                        </tr>
                                    `)}
                                </tbody>
                            </table>
                        </div>
                    `}

                ${this.nextCursor ? html`<div class="flex justify-center mt-4"><button class=${BUTTON_QUIET} @click=${() => void this.loadMore()}>Load more</button></div>` : nothing}
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-contact-list': GableContactList;
    }
}
