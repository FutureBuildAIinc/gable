// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { router } from '../../lib/router.ts';
import { ArrowRight } from 'lucide';
import { InvoiceService } from '../../services/InvoiceService.ts';
import { CustomerService } from '../../services/CustomerService.ts';
import { apiErrorMessage } from '../../services/apiError.ts';
import {
    type InvoiceSummary,
    type InvoiceStatus,
    INVOICE_STATUSES,
    formatInvoiceStatus,
    getInvoiceStatusColor,
} from '../../types/invoice.ts';
import { chipClass, overdueBadge } from '../../components/invoices/chips.ts';
import { onBranchChanged } from '../../lib/branch-listener.ts';
import { formatCents, formatDay } from '../../lib/utils.ts';

const PAGE_SIZE = 50;

@customElement('gable-invoice-list')
export class GableInvoiceList extends LitElement {
    createRenderRoot() { return this; }

    @state() private invoices: InvoiceSummary[] = [];
    @state() private loading = true;
    @state() private refreshing = false;
    @state() private error: string | null = null;
    @state() private status: InvoiceStatus | 'all' = 'all';
    @state() private overdueOnly = false;
    @state() private customerId = '';
    @state() private customers: { id: string; name: string }[] = [];
    @state() private nextCursor: string | null = null;
    @state() private total: number | undefined;
    private _unsubBranch: (() => void) | null = null;

    connectedCallback() {
        super.connectedCallback();
        this.loadInvoices();
        this.loadCustomers();
        this._unsubBranch = onBranchChanged(() => {
            this.loading = true;
            this.loadInvoices();
        });
    }

    disconnectedCallback() {
        super.disconnectedCallback();
        if (this._unsubBranch) {
            this._unsubBranch();
            this._unsubBranch = null;
        }
    }

    private filterParams() {
        return {
            status: this.status === 'all' ? undefined : this.status,
            customerId: this.customerId || undefined,
            overdue: this.overdueOnly ? true : undefined,
            limit: PAGE_SIZE,
        };
    }

    private async loadCustomers() {
        try {
            const all = await CustomerService.listAllCustomers();
            this.customers = all.map(c => ({ id: c.id, name: c.name })).sort((a, b) => a.name.localeCompare(b.name));
        } catch (err) {
            // the customer filter is a convenience: the list works without it
            console.error('Failed to load customers for the filter', err);
        }
    }

    private async loadInvoices() {
        try {
            this.error = null;
            const page = await InvoiceService.listInvoices({ ...this.filterParams(), includeTotal: true });
            this.invoices = page.items;
            this.nextCursor = page.next_cursor;
            this.total = page.total;
        } catch (err) {
            console.error('Failed to load invoices:', err);
            this.error = apiErrorMessage(err, 'Failed to load invoices');
        } finally {
            this.loading = false;
            this.refreshing = false;
        }
    }

    private async loadMore() {
        if (!this.nextCursor) return;
        try {
            const page = await InvoiceService.listInvoices({ ...this.filterParams(), cursor: this.nextCursor });
            this.invoices = [...this.invoices, ...page.items];
            this.nextCursor = page.next_cursor;
        } catch (err) {
            this.error = apiErrorMessage(err, 'Failed to load more invoices');
        }
    }

    private refilter() {
        this.refreshing = true;
        this.loadInvoices();
    }

    private emptyText(): string {
        const parts: string[] = [];
        if (this.status !== 'all') parts.push(formatInvoiceStatus(this.status));
        if (this.overdueOnly) parts.push('overdue');
        if (this.customerId) parts.push('for this customer');
        return parts.length > 0 ? `No invoices match: ${parts.join(', ')}.` : 'No invoices have been billed yet. Fulfil an order to create one.';
    }

    render() {
        if (this.loading) {
            return html`<div class="text-white">Loading invoices...</div>`;
        }

        if (this.error && this.invoices.length === 0) {
            return html`
                <div class="flex flex-col items-center justify-center min-h-[400px] p-8">
                    <p class="text-rose-400 text-lg font-semibold mb-2">Failed to load</p>
                    <p class="text-gray-400 text-sm mb-4">${this.error}</p>
                    <button
                        @click=${() => { this.error = null; this.loading = true; this.loadInvoices(); }}
                        class="px-4 py-2 bg-[#00FFA3] text-[#0A0B10] rounded font-medium hover:opacity-90"
                    >
                        Retry
                    </button>
                </div>
            `;
        }

        return html`
            <div class="space-y-4">
                <div class="flex items-center justify-between flex-wrap gap-3">
                    <div>
                        <h1 class="text-2xl font-bold text-white">Invoices</h1>
                        ${this.total !== undefined ? html`<p class="text-sm text-zinc-400 mt-1" data-testid="invoice-total">${this.total} invoice${this.total === 1 ? '' : 's'}</p>` : nothing}
                    </div>
                    <a href="/credit-memos" class="text-sm text-blue-400 hover:underline">Credit memos</a>
                </div>

                <div class="flex flex-wrap items-center gap-4 pb-3 border-b border-white/10">
                    <label class="flex items-center gap-2 text-sm text-zinc-400">
                        Status
                        <select
                            aria-label="Status"
                            .value=${this.status}
                            @change=${(e: Event) => { this.status = (e.target as HTMLSelectElement).value as InvoiceStatus | 'all'; this.refilter(); }}
                            class="bg-black/30 border border-white/10 rounded px-3 py-1.5 text-white text-sm"
                        >
                            <option value="all">All</option>
                            ${INVOICE_STATUSES.map(s => html`<option value=${s} ?selected=${this.status === s}>${formatInvoiceStatus(s)}</option>`)}
                        </select>
                    </label>
                    <label class="flex items-center gap-2 text-sm text-zinc-400">
                        Customer
                        <select
                            aria-label="Customer"
                            .value=${this.customerId}
                            @change=${(e: Event) => { this.customerId = (e.target as HTMLSelectElement).value; this.refilter(); }}
                            class="bg-black/30 border border-white/10 rounded px-3 py-1.5 text-white text-sm max-w-[16rem]"
                        >
                            <option value="">All customers</option>
                            ${this.customers.map(c => html`<option value=${c.id} ?selected=${this.customerId === c.id}>${c.name}</option>`)}
                        </select>
                    </label>
                    <label class="flex items-center gap-2 text-sm text-zinc-300 cursor-pointer">
                        <input
                            type="checkbox"
                            aria-label="Overdue only"
                            .checked=${this.overdueOnly}
                            @change=${(e: Event) => { this.overdueOnly = (e.target as HTMLInputElement).checked; this.refilter(); }}
                            class="accent-[#00FFA3]"
                        />
                        Overdue only
                    </label>
                    ${this.refreshing ? html`<span class="text-xs text-zinc-500">Updating...</span>` : nothing}
                </div>

                ${this.error ? html`<div class="p-3 rounded border border-red-500/40 bg-red-500/10 text-sm text-red-300" role="alert">${this.error}</div>` : nothing}

                ${this.invoices.length === 0 ? html`
                    <div class="text-zinc-400 py-16 text-center" data-testid="invoice-empty">${this.emptyText()}</div>
                ` : html`
                    <div class="rounded-lg border border-white/10 overflow-hidden">
                        <div class="overflow-x-auto">
                            <table class="w-full text-left text-sm" aria-label="Invoices">
                                <thead class="bg-white/5">
                                    <tr>
                                        <th class="p-3 text-zinc-400 font-medium">Number</th>
                                        <th class="p-3 text-zinc-400 font-medium">Customer</th>
                                        <th class="p-3 text-zinc-400 font-medium">Order</th>
                                        <th class="p-3 text-zinc-400 font-medium">Job</th>
                                        <th class="p-3 text-zinc-400 font-medium">Invoice date</th>
                                        <th class="p-3 text-zinc-400 font-medium">Due</th>
                                        <th class="p-3 text-zinc-400 font-medium text-right">Total</th>
                                        <th class="p-3 text-zinc-400 font-medium text-right">Open</th>
                                        <th class="p-3 text-zinc-400 font-medium">Status</th>
                                        <th class="p-3"></th>
                                    </tr>
                                </thead>
                                <tbody class="divide-y divide-white/5">
                                    ${this.invoices.map(inv => html`
                                        <tr class="hover:bg-white/5 cursor-pointer" @click=${() => router.navigate(`/invoices/${inv.id}`)}>
                                            <td class="p-3 font-mono text-white whitespace-nowrap">${inv.number}</td>
                                            <td class="p-3 text-white">${inv.customer_name || inv.customer_id.slice(0, 8)}</td>
                                            <td class="p-3 font-mono text-xs">
                                                ${inv.order_id
                                                    ? html`<a href="/orders/${inv.order_id}" @click=${(e: Event) => e.stopPropagation()} class="text-blue-400 hover:underline">${inv.order_id.slice(0, 8)}</a>`
                                                    : html`<span class="text-zinc-500">counter</span>`}
                                            </td>
                                            <td class="p-3 font-mono text-xs text-zinc-400">${inv.job_id ? inv.job_id.slice(0, 8) : '—'}</td>
                                            <td class="p-3 text-zinc-400 whitespace-nowrap">${formatDay(inv.invoice_date)}</td>
                                            <td class="p-3 whitespace-nowrap ${inv.is_overdue ? 'text-red-400' : 'text-zinc-400'}">${formatDay(inv.due_date)}</td>
                                            <td class="p-3 text-white font-mono text-right">${formatCents(inv.total_cents)}</td>
                                            <td class="p-3 font-mono text-right ${inv.open_cents > 0 ? 'text-gable-green' : 'text-zinc-500'}">${formatCents(inv.open_cents)}</td>
                                            <td class="p-3 whitespace-nowrap">
                                                <span class=${chipClass(getInvoiceStatusColor(inv.status))}>${formatInvoiceStatus(inv.status)}</span>
                                                ${overdueBadge(inv.is_overdue)}
                                            </td>
                                            <td class="p-3 text-right">${icon(ArrowRight, 16, 'text-zinc-500')}</td>
                                        </tr>
                                    `)}
                                </tbody>
                            </table>
                        </div>
                    </div>
                    ${this.nextCursor ? html`
                        <div class="flex justify-center pt-4">
                            <button @click=${() => this.loadMore()} class="px-4 py-2 bg-white/10 text-white rounded hover:bg-white/20 text-sm">
                                Load more
                            </button>
                        </div>
                    ` : nothing}
                `}
            </div>
        `;
    }
}
