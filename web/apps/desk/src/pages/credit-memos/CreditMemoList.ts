// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { router } from '../../lib/router.ts';
import { ArrowRight } from 'lucide';
import { CreditMemoService } from '../../services/CreditMemoService.ts';
import { CustomerService } from '../../services/CustomerService.ts';
import { apiErrorMessage } from '../../services/apiError.ts';
import {
    type CreditMemoSummary,
    type CreditMemoStatus,
    CREDIT_MEMO_STATUSES,
    creditMemoLabel,
    formatCreditMemoStatus,
    formatReasonCode,
    getCreditMemoStatusColor,
} from '../../types/creditMemo.ts';
import { chipClass } from '../../components/invoices/chips.ts';
import { onBranchChanged } from '../../lib/branch-listener.ts';
import { formatCents, formatDay } from '../../lib/utils.ts';
import { creditTextClass } from '../../lib/credit-display.ts';

const PAGE_SIZE = 50;

@customElement('gable-credit-memo-list')
export class GableCreditMemoList extends LitElement {
    createRenderRoot() { return this; }

    @state() private memos: CreditMemoSummary[] = [];
    @state() private loading = true;
    @state() private refreshing = false;
    @state() private error: string | null = null;
    @state() private status: CreditMemoStatus | 'all' = 'all';
    @state() private customerId = '';
    @state() private invoiceId = '';
    @state() private customers: { id: string; name: string }[] = [];
    @state() private nextCursor: string | null = null;
    @state() private total: number | undefined;
    private _unsubBranch: (() => void) | null = null;

    connectedCallback() {
        super.connectedCallback();
        // an invoice link may carry its filter: /credit-memos?invoice_id=...
        this.invoiceId = new URLSearchParams(window.location.search).get('invoice_id') ?? '';
        this.loadMemos();
        this.loadCustomers();
        this._unsubBranch = onBranchChanged(() => {
            this.loading = true;
            this.loadMemos();
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
            invoiceId: this.invoiceId.trim() || undefined,
            limit: PAGE_SIZE,
        };
    }

    private async loadCustomers() {
        try {
            const all = await CustomerService.listAllCustomers();
            this.customers = all.map(c => ({ id: c.id, name: c.name })).sort((a, b) => a.name.localeCompare(b.name));
        } catch (err) {
            console.error('Failed to load customers for the filter', err);
        }
    }

    private async loadMemos() {
        try {
            this.error = null;
            const page = await CreditMemoService.listCreditMemos({ ...this.filterParams(), includeTotal: true });
            this.memos = page.items;
            this.nextCursor = page.next_cursor;
            this.total = page.total;
        } catch (err) {
            console.error('Failed to load credit memos:', err);
            this.error = apiErrorMessage(err, 'Failed to load credit memos');
        } finally {
            this.loading = false;
            this.refreshing = false;
        }
    }

    private async loadMore() {
        if (!this.nextCursor) return;
        try {
            const page = await CreditMemoService.listCreditMemos({ ...this.filterParams(), cursor: this.nextCursor });
            this.memos = [...this.memos, ...page.items];
            this.nextCursor = page.next_cursor;
        } catch (err) {
            this.error = apiErrorMessage(err, 'Failed to load more credit memos');
        }
    }

    private refilter() {
        this.refreshing = true;
        this.loadMemos();
    }

    render() {
        if (this.loading) {
            return html`<div class="text-white">Loading credit memos...</div>`;
        }

        if (this.error && this.memos.length === 0) {
            return html`
                <div class="flex flex-col items-center justify-center min-h-[400px] p-8">
                    <p class="text-rose-400 text-lg font-semibold mb-2">Failed to load</p>
                    <p class="text-gray-400 text-sm mb-4">${this.error}</p>
                    <button
                        @click=${() => { this.error = null; this.loading = true; this.loadMemos(); }}
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
                        <h1 class="text-2xl font-bold text-white">Credit Memos</h1>
                        ${this.total !== undefined ? html`<p class="text-sm text-zinc-400 mt-1" data-testid="credit-memo-total">${this.total} credit memo${this.total === 1 ? '' : 's'}</p>` : nothing}
                    </div>
                    <a href="/credit-memos/new" class="bg-gable-green text-black font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-gable-green/90 transition-colors">New credit memo</a>
                </div>

                <div class="flex flex-wrap items-center gap-4 pb-3 border-b border-white/10">
                    <label class="flex items-center gap-2 text-sm text-zinc-400">
                        Status
                        <select
                            aria-label="Status"
                            .value=${this.status}
                            @change=${(e: Event) => { this.status = (e.target as HTMLSelectElement).value as CreditMemoStatus | 'all'; this.refilter(); }}
                            class="bg-black/30 border border-white/10 rounded px-3 py-1.5 text-white text-sm"
                        >
                            <option value="all">All</option>
                            ${CREDIT_MEMO_STATUSES.map(s => html`<option value=${s} ?selected=${this.status === s}>${formatCreditMemoStatus(s)}</option>`)}
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
                    ${this.invoiceId ? html`
                        <span class="inline-flex items-center gap-2 text-sm text-zinc-300 bg-white/5 border border-white/10 rounded px-3 py-1.5">
                            Invoice <span class="font-mono">${this.invoiceId.slice(0, 8)}</span>
                            <button
                                aria-label="Clear invoice filter"
                                @click=${() => { this.invoiceId = ''; this.refilter(); }}
                                class="text-zinc-400 hover:text-white"
                            >x</button>
                        </span>
                    ` : nothing}
                    ${this.refreshing ? html`<span class="text-xs text-zinc-500">Updating...</span>` : nothing}
                </div>

                ${this.error ? html`<div class="p-3 rounded border border-red-500/40 bg-red-500/10 text-sm text-red-300" role="alert">${this.error}</div>` : nothing}

                ${this.memos.length === 0 ? html`
                    <div class="text-zinc-400 py-16 text-center" data-testid="credit-memo-empty">
                        ${this.status !== 'all' || this.customerId || this.invoiceId ? 'No credit memos match these filters.' : 'No credit memos yet. Start one from an invoice.'}
                    </div>
                ` : html`
                    <div class="rounded-lg border border-white/10 overflow-hidden">
                        <div class="overflow-x-auto">
                            <table class="w-full text-left text-sm" aria-label="Credit memos">
                                <thead class="bg-white/5">
                                    <tr>
                                        <th class="p-3 text-zinc-400 font-medium">Number</th>
                                        <th class="p-3 text-zinc-400 font-medium">Customer</th>
                                        <th class="p-3 text-zinc-400 font-medium">Invoice</th>
                                        <th class="p-3 text-zinc-400 font-medium">Reason</th>
                                        <th class="p-3 text-zinc-400 font-medium">Date</th>
                                        <th class="p-3 text-zinc-400 font-medium text-right">Total</th>
                                        <th class="p-3 text-zinc-400 font-medium">Status</th>
                                        <th class="p-3"></th>
                                    </tr>
                                </thead>
                                <tbody class="divide-y divide-white/5">
                                    ${this.memos.map(cm => html`
                                        <tr class="hover:bg-white/5 cursor-pointer" @click=${() => router.navigate(`/credit-memos/${cm.id}`)}>
                                            <td class="p-3 font-mono whitespace-nowrap ${cm.number ? 'text-white' : 'text-zinc-400 italic'}">${creditMemoLabel(cm)}</td>
                                            <td class="p-3 text-white">${cm.customer_name || cm.customer_id.slice(0, 8)}</td>
                                            <td class="p-3 font-mono text-xs">
                                                ${cm.invoice_id
                                                    ? html`<a href="/invoices/${cm.invoice_id}" @click=${(e: Event) => e.stopPropagation()} class="text-blue-400 hover:underline">${cm.invoice_id.slice(0, 8)}</a>`
                                                    : html`<span class="text-zinc-500">none</span>`}
                                            </td>
                                            <td class="p-3 text-zinc-300">${formatReasonCode(cm.reason_code)}</td>
                                            <td class="p-3 text-zinc-400 whitespace-nowrap">${formatDay(cm.memo_date)}</td>
                                            <td class="p-3 font-mono text-right ${creditTextClass(cm.total_cents)}">${formatCents(cm.total_cents)}</td>
                                            <td class="p-3 whitespace-nowrap"><span class=${chipClass(getCreditMemoStatusColor(cm.status))}>${formatCreditMemoStatus(cm.status)}</span></td>
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
