// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { router } from '../../lib/router.ts';
import { ArrowRight } from 'lucide';
import { OrderService } from '../../services/OrderService.ts';
import { type OrderSummary, type OrderStatus, formatOrderStatus, getStatusColor } from '../../types/order.ts';
import { onBranchChanged } from '../../lib/branch-listener.ts';
import { formatCents } from '../../lib/utils.ts';

const STATUS_FILTERS: (OrderStatus | 'all')[] = ['all', 'draft', 'on_hold', 'confirmed', 'backordered', 'fulfilled', 'cancelled'];

@customElement('gable-order-list')
export class GableOrderList extends LitElement {
    createRenderRoot() { return this; }

    @state() private orders: OrderSummary[] = [];
    @state() private loading = true;
    @state() private error: string | null = null;
    @state() private status: OrderStatus | 'all' = 'all';
    @state() private nextCursor: string | null = null;
    private _unsubBranch: (() => void) | null = null;

    connectedCallback() {
        super.connectedCallback();
        this.loadOrders();
        this._unsubBranch = onBranchChanged(() => {
            this.loading = true;
            this.loadOrders();
        });
    }

    disconnectedCallback() {
        super.disconnectedCallback();
        if (this._unsubBranch) {
            this._unsubBranch();
            this._unsubBranch = null;
        }
    }

    private async loadOrders() {
        try {
            this.error = null;
            const page = await OrderService.listOrders({
                status: this.status === 'all' ? undefined : this.status,
                limit: 50,
            });
            this.orders = page.items;
            this.nextCursor = page.next_cursor;
        } catch (err) {
            console.error(err);
            this.error = err instanceof Error ? err.message : 'Failed to load orders';
        } finally {
            this.loading = false;
        }
    }

    private async loadMore() {
        if (!this.nextCursor) return;
        const page = await OrderService.listOrders({
            status: this.status === 'all' ? undefined : this.status,
            limit: 50,
            cursor: this.nextCursor,
        });
        this.orders = [...this.orders, ...page.items];
        this.nextCursor = page.next_cursor;
    }

    private setStatus(status: OrderStatus | 'all') {
        if (this.status === status) return;
        this.status = status;
        this.loading = true;
        this.loadOrders();
    }

    private getStatusBadgeClass(status: OrderStatus): string {
        const color = getStatusColor(status);
        let bg = 'bg-white/10 text-white';
        if (color === 'info') bg = 'bg-blue-500/20 text-blue-400 border-blue-500/50';
        if (color === 'success') bg = 'bg-gable-green/20 text-gable-green border-gable-green/50';
        if (color === 'warning') bg = 'bg-amber-500/20 text-amber-400 border-amber-500/50';
        if (color === 'error') bg = 'bg-red-500/20 text-red-400 border-red-500/50';
        return bg;
    }

    render() {
        if (this.loading) {
            return html`<div class="text-white">Loading orders...</div>`;
        }

        if (this.error) {
            return html`
                <div class="flex flex-col items-center justify-center min-h-[400px] p-8">
                    <p class="text-rose-400 text-lg font-semibold mb-2">Failed to load</p>
                    <p class="text-gray-400 text-sm mb-4">${this.error}</p>
                    <button
                        @click=${() => { this.error = null; this.loadOrders(); }}
                        class="px-4 py-2 bg-[#00FFA3] text-[#0A0B10] rounded font-medium hover:opacity-90"
                    >
                        Retry
                    </button>
                </div>
            `;
        }

        return html`
            <div class="space-y-4">
                <div class="flex items-center justify-between">
                    <h1 class="text-2xl font-bold text-white">Orders</h1>
                </div>
                <div class="flex gap-1 mb-2 border-b border-white/10 flex-wrap">
                    ${STATUS_FILTERS.map(s => html`
                        <button
                            @click=${() => this.setStatus(s)}
                            class="px-4 py-2 text-sm font-medium transition-colors border-b-2 ${
                                this.status === s ? 'text-gable-green border-gable-green' : 'text-zinc-400 border-transparent hover:text-white'
                            }"
                        >
                            ${s === 'all' ? 'All' : formatOrderStatus(s)}
                        </button>
                    `)}
                </div>
                ${this.orders.length === 0 ? html`
                    <div class="text-zinc-400 py-16 text-center">No orders${this.status !== 'all' ? ` in ${formatOrderStatus(this.status as OrderStatus)}` : ''}.</div>
                ` : html`
                    <div class="rounded-lg border border-white/10 overflow-hidden">
                        <table class="w-full text-left text-sm" aria-label="Orders">
                            <thead class="bg-white/5">
                                <tr>
                                    <th class="p-4 text-zinc-400 font-medium">Number</th>
                                    <th class="p-4 text-zinc-400 font-medium">Customer</th>
                                    <th class="p-4 text-zinc-400 font-medium">Status</th>
                                    <th class="p-4 text-zinc-400 font-medium text-right">Total</th>
                                    <th class="p-4 text-zinc-400 font-medium text-right">Created</th>
                                    <th class="p-4"></th>
                                </tr>
                            </thead>
                            <tbody class="divide-y divide-white/5">
                                ${this.orders.map(order => html`
                                    <tr class="hover:bg-white/5 cursor-pointer" @click=${() => router.navigate(`/orders/${order.id}`)}>
                                        <td class="p-4 font-mono text-white">${order.number}</td>
                                        <td class="p-4 text-white">${order.customer_name || order.customer_id.slice(0, 8)}</td>
                                        <td class="p-4">
                                            <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium border border-transparent ${this.getStatusBadgeClass(order.status)}">
                                                ${formatOrderStatus(order.status)}
                                            </span>
                                            ${order.hold_reason ? html`<span class="ml-2 text-xs text-amber-400">${order.hold_reason === 'credit_limit' ? 'credit hold' : 'held'}</span>` : ''}
                                        </td>
                                        <td class="p-4 text-gable-green font-mono text-right">${formatCents(order.total_cents)}</td>
                                        <td class="p-4 text-zinc-400 text-right">${new Date(order.created_at).toLocaleDateString()}</td>
                                        <td class="p-4 text-right">${icon(ArrowRight, 16, 'text-zinc-500')}</td>
                                    </tr>
                                `)}
                            </tbody>
                        </table>
                    </div>
                    ${this.nextCursor ? html`
                        <div class="flex justify-center pt-4">
                            <button @click=${() => this.loadMore()} class="px-4 py-2 bg-white/10 text-white rounded hover:bg-white/20 text-sm">
                                Load more
                            </button>
                        </div>
                    ` : ''}
                `}
            </div>
        `;
    }
}
