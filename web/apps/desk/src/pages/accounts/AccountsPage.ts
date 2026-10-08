// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { router } from '../../lib/router.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { formatCents } from '../../lib/utils.ts';
import { CustomerService } from '../../services/CustomerService.ts';
import { apiErrorMessage, fieldErrorMap } from '../../services/apiError.ts';
import { CUSTOMER_TIERS } from '../../types/customer.ts';
import type { Customer, CustomerRequest, CustomerTier, PaymentTermsRecord } from '../../types/customer.ts';
import { INPUT_CLASS } from './formFields.ts';
import { Search, DollarSign, Building2, User, Plus } from 'lucide';
import './CustomerForm.ts';

type CreditFilter = 'all' | 'credit' | 'cash';

/**
 * The credit/cash split is on the loaded rows, because the server has no credit filter:
 * credit means the limit is null (no limit) or above zero; cash means the limit is exactly 0
 * (no credit). Search, tier and active are server side filters of the cursor list.
 */
export function matchesCreditFilter(filter: CreditFilter, creditLimitCents: number | null): boolean {
    if (filter === 'credit') return creditLimitCents === null || creditLimitCents > 0;
    if (filter === 'cash') return creditLimitCents === 0;
    return true;
}

@customElement('gable-accounts-page')
export class GableAccountsPage extends LitElement {
    createRenderRoot() { return this; }

    private static readonly PAGE_SIZE = 50;

    @state() private customers: Customer[] = [];
    @state() private nextCursor: string | null = null;
    @state() private total: number | null = null;
    @state() private loading = true;
    @state() private loadingMore = false;
    @state() private error: string | null = null;
    @state() private filter: CreditFilter = 'all';
    @state() private search = '';
    @state() private tier: CustomerTier | '' = '';
    @state() private status: 'all' | 'active' | 'inactive' = 'all';

    @state() private showCreate = false;
    @state() private terms: PaymentTermsRecord[] = [];
    @state() private creating = false;
    @state() private createErrors: Record<string, string> = {};
    @state() private createMessage = '';

    private searchTimer: ReturnType<typeof setTimeout> | null = null;
    private requestSeq = 0;

    connectedCallback() {
        super.connectedCallback();
        void this.loadCustomers();
    }

    disconnectedCallback() {
        super.disconnectedCallback();
        if (this.searchTimer) clearTimeout(this.searchTimer);
    }

    private get serverParams() {
        return {
            q: this.search,
            tier: this.tier || undefined,
            isActive: this.status === 'all' ? undefined : this.status === 'active',
            limit: GableAccountsPage.PAGE_SIZE,
        };
    }

    /** Loads the first page for the current search and filters; a slower earlier answer is dropped. */
    private async loadCustomers() {
        const seq = ++this.requestSeq;
        try {
            this.error = null;
            const page = await CustomerService.listCustomers({ ...this.serverParams, includeTotal: true });
            if (seq !== this.requestSeq) return;
            this.customers = page.items;
            this.nextCursor = page.next_cursor;
            this.total = page.total ?? null;
        } catch (error) {
            if (seq !== this.requestSeq) return;
            console.error('Failed to load customers:', error);
            this.error = apiErrorMessage(error, 'Failed to load customers');
        } finally {
            if (seq === this.requestSeq) this.loading = false;
        }
    }

    private async loadMore() {
        if (!this.nextCursor || this.loadingMore) return;
        const seq = this.requestSeq;
        this.loadingMore = true;
        try {
            const page = await CustomerService.listCustomers({ ...this.serverParams, cursor: this.nextCursor });
            if (seq !== this.requestSeq) return;
            this.customers = [...this.customers, ...page.items];
            this.nextCursor = page.next_cursor;
        } catch (error) {
            ToastService.show(`Failed to load more: ${apiErrorMessage(error)}`, 'error');
        } finally {
            this.loadingMore = false;
        }
    }

    private onSearchInput(value: string) {
        this.search = value;
        if (this.searchTimer) clearTimeout(this.searchTimer);
        this.searchTimer = setTimeout(() => void this.loadCustomers(), 300);
    }

    private onServerFilterChange() {
        void this.loadCustomers();
    }

    private async openCreate() {
        this.showCreate = true;
        this.createErrors = {};
        this.createMessage = '';
        if (this.terms.length === 0) {
            try {
                this.terms = (await CustomerService.listPaymentTerms({ limit: 200 })).items;
            } catch (error) {
                this.createMessage = apiErrorMessage(error, 'Failed to load payment terms');
            }
        }
    }

    private async createCustomer(request: CustomerRequest) {
        this.creating = true;
        this.createErrors = {};
        this.createMessage = '';
        try {
            const created = await CustomerService.createCustomer(request);
            ToastService.show(`Customer ${created.account_number} created`, 'success');
            router.navigate(`/accounts/${created.id}`);
        } catch (error) {
            this.createErrors = fieldErrorMap(error);
            this.createMessage = apiErrorMessage(error, 'Failed to create customer');
        } finally {
            this.creating = false;
        }
    }

    private get visibleCustomers(): Customer[] {
        return this.customers.filter(c => matchesCreditFilter(this.filter, c.credit_limit_cents));
    }

    private renderFilterButton(filterValue: 'all' | 'credit' | 'cash', label: string, iconData?: typeof Building2) {
        const active = this.filter === filterValue;
        return html`
            <button
                @click=${() => { this.filter = filterValue; }}
                class="px-3 py-1.5 rounded-md text-sm font-medium transition-all flex items-center gap-2 ${
                    active
                        ? 'bg-zinc-700 text-white shadow-sm'
                        : 'text-zinc-400 hover:text-white hover:bg-white/5'
                }"
            >
                ${iconData ? icon(iconData, 14) : nothing}
                ${label}
            </button>
        `;
    }

    private renderBadge(active: boolean) {
        return html`
            <span class="px-2 py-0.5 rounded-full text-xs font-medium border ${
                active
                    ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                    : 'bg-red-500/10 text-red-400 border-red-500/20'
            }">
                ${active ? 'Active' : 'Inactive'}
            </span>
        `;
    }

    render() {
        if (this.loading) {
            return html`<div class="text-white p-8">Loading accounts...</div>`;
        }

        const visible = this.visibleCustomers;

        return html`
            <div class="space-y-6">
                <div class="flex flex-col md:flex-row md:items-center justify-between gap-4">
                    <div>
                        <h1 class="text-2xl font-bold bg-gradient-to-r from-white to-zinc-400 bg-clip-text text-transparent">Accounts</h1>
                        <p class="text-zinc-400 text-sm mt-1">Manage customer accounts, balances, and credit limits.${this.total !== null ? html` <span class="font-mono">${this.total}</span> in total.` : nothing}</p>
                    </div>

                    <div class="flex flex-wrap items-center gap-3">
                        <div class="relative">
                            <span class="absolute left-3 top-1/2 -translate-y-1/2 text-zinc-500">
                                ${icon(Search, 16)}
                            </span>
                            <input
                                type="text"
                                aria-label="Search accounts"
                                placeholder="Search accounts..."
                                maxlength="100"
                                .value=${this.search}
                                @input=${(e: Event) => this.onSearchInput((e.target as HTMLInputElement).value)}
                                class="bg-slate-steel/50 border border-white/5 rounded-full py-2 pl-10 pr-4 text-sm text-white focus:outline-none focus:ring-1 focus:ring-gable-green/50 w-64"
                            />
                        </div>

                        <select
                            aria-label="Tier"
                            .value=${this.tier}
                            @change=${(e: Event) => { this.tier = (e.target as HTMLSelectElement).value as CustomerTier | ''; this.onServerFilterChange(); }}
                            class="${INPUT_CLASS} !w-auto !rounded-lg"
                        >
                            <option value="">All tiers</option>
                            ${CUSTOMER_TIERS.map(t => html`<option value=${t}>${t.charAt(0).toUpperCase() + t.slice(1)}</option>`)}
                        </select>

                        <select
                            aria-label="Status"
                            .value=${this.status}
                            @change=${(e: Event) => { this.status = (e.target as HTMLSelectElement).value as 'all' | 'active' | 'inactive'; this.onServerFilterChange(); }}
                            class="${INPUT_CLASS} !w-auto !rounded-lg"
                        >
                            <option value="all">Active and inactive</option>
                            <option value="active">Active</option>
                            <option value="inactive">Inactive</option>
                        </select>

                        <div class="bg-slate-steel/50 p-1 rounded-lg flex items-center border border-white/5">
                            ${this.renderFilterButton('all', 'All')}
                            ${this.renderFilterButton('credit', 'Credit', Building2)}
                            ${this.renderFilterButton('cash', 'Cash', DollarSign)}
                        </div>

                        <button
                            @click=${() => void this.openCreate()}
                            class="inline-flex items-center gap-2 px-4 py-2 bg-gable-green text-black text-sm font-semibold rounded-md hover:shadow-glow transition-all"
                        >
                            ${icon(Plus, 14)} New customer
                        </button>
                    </div>
                </div>

                ${this.showCreate ? html`
                    <gable-customer-form
                        .terms=${this.terms}
                        .fieldErrors=${this.createErrors}
                        .formError=${this.createMessage}
                        ?submitting=${this.creating}
                        @customer-submit=${(e: CustomEvent<CustomerRequest>) => void this.createCustomer(e.detail)}
                        @customer-cancel=${() => { this.showCreate = false; }}
                    ></gable-customer-form>
                ` : nothing}

                ${this.error ? html`<div class="text-sm text-red-400" role="alert">${this.error}</div>` : nothing}

                <div class="grid grid-cols-1 gap-4">
                    <!-- Table Header -->
                    <div class="grid grid-cols-12 gap-4 px-6 py-3 text-xs font-medium text-zinc-500 uppercase tracking-wider border-b border-white/5">
                        <div class="col-span-4">Customer</div>
                        <div class="col-span-2">Account #</div>
                        <div class="col-span-2 text-right">Balance Due</div>
                        <div class="col-span-2 text-right">Credit Limit</div>
                        <div class="col-span-2 text-right">Status</div>
                    </div>

                    ${visible.map(customer => html`
                        <a href="/accounts/${customer.id}" class="block">
                            <div class="grid grid-cols-12 gap-4 px-6 py-4 items-center hover:bg-white/5 transition-colors border border-white/5 hover:border-gable-green/30 rounded-lg bg-slate-steel group">
                                <div class="col-span-4 flex items-center gap-3">
                                    <div class="w-10 h-10 rounded-full flex items-center justify-center text-xs font-bold shrink-0 ${
                                        customer.credit_limit_cents !== 0
                                            ? 'bg-blue-500/10 text-blue-400 border border-blue-500/20'
                                            : 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
                                    }">
                                        ${customer.credit_limit_cents !== 0 ? icon(Building2, 18) : icon(User, 18)}
                                    </div>
                                    <div>
                                        <div class="font-medium text-white group-hover:text-gable-green transition-colors">${customer.name}</div>
                                        <div class="text-xs text-zinc-500 truncate">${customer.email || 'No email'}</div>
                                    </div>
                                </div>
                                <div class="col-span-2 text-sm text-zinc-400 font-mono">${customer.account_number}</div>
                                <div class="col-span-2 text-right font-mono font-medium text-white">
                                    ${formatCents(customer.balance_cents)}
                                </div>
                                <div class="col-span-2 text-right font-mono text-zinc-400 text-sm">
                                    ${customer.credit_limit_cents === null
                                        ? html`<span class="text-zinc-500">No limit</span>`
                                        : formatCents(customer.credit_limit_cents)
                                    }
                                </div>
                                <div class="col-span-2 flex justify-end">
                                    ${this.renderBadge(customer.is_active)}
                                </div>
                            </div>
                        </a>
                    `)}

                    ${visible.length === 0 ? html`
                        <div class="text-center py-20 text-zinc-500">
                            No accounts found matching your filters.
                        </div>
                    ` : nothing}

                    ${this.nextCursor ? html`
                        <div class="flex justify-center">
                            <button
                                @click=${() => void this.loadMore()}
                                ?disabled=${this.loadingMore}
                                class="inline-flex items-center gap-2 px-4 py-2 border border-white/10 text-zinc-300 hover:text-white text-sm font-medium rounded-md transition-colors disabled:opacity-50"
                            >${this.loadingMore ? 'Loading...' : 'Load more'}</button>
                        </div>
                    ` : nothing}
                </div>
            </div>
        `;
    }
}
