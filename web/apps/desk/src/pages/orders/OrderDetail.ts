// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { OrderService } from '../../services/OrderService.ts';
import { InvoiceService } from '../../services/InvoiceService.ts';
import { SalesTeamService } from '../../services/SalesTeamService.ts';
import { type Order, type OrderStatus, formatOrderStatus, getStatusColor } from '../../types/order.ts';
import type { SalesPerson } from '../../types/salesteam.ts';
import { Check, Printer, User, DollarSign, Mail, Phone, XCircle, LockOpen } from 'lucide';
import { formatCents, formatPrice4 } from '../../lib/utils.ts';

const API_URL = import.meta.env.VITE_API_URL || '';

@customElement('gable-order-detail')
export class GableOrderDetail extends LitElement {
    createRenderRoot() { return this; }

    @property({ attribute: 'route-id' }) routeId = '';

    @state() private order: Order | null = null;
    @state() private salesperson: SalesPerson | null = null;
    @state() private invoiceNumbers: Record<string, string> = {};
    @state() private loading = true;
    @state() private error = false;
    @state() private processing = false;
    @state() private pickedUpBy = '';
    @state() private fulfilOpen = false;
    @state() private pickupError = '';

    connectedCallback() {
        super.connectedCallback();
        if (this.routeId) this.loadOrder(this.routeId);
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('routeId') && changed.get('routeId') !== undefined && this.routeId) {
            this.loading = true;
            this.loadOrder(this.routeId);
        }
    }

    private async loadOrder(orderId: string) {
        try {
            const data = await OrderService.getOrder(orderId);
            this.order = data;
            this.loadInvoiceNumbers(data.id, data.invoice_ids);
            if (data.salesperson_id) {
                try {
                    const sp = await SalesTeamService.getSalesPerson(data.salesperson_id);
                    this.salesperson = sp;
                } catch {
                    // Salesperson lookup failed, not critical
                }
            }
        } catch (err) {
            console.error(err);
            this.error = true;
            ToastService.show('Failed to load order details', 'error');
        } finally {
            this.loading = false;
        }
    }

    /** The numbers of the invoices this order billed, for the links; the id stands in until they arrive. */
    private async loadInvoiceNumbers(orderId: string, invoiceIds: string[]) {
        if (invoiceIds.length === 0) return;
        try {
            const page = await InvoiceService.listInvoices({ orderId, limit: 100 });
            const numbers: Record<string, string> = {};
            for (const inv of Array.isArray(page?.items) ? page.items : []) numbers[inv.id] = inv.number;
            this.invoiceNumbers = numbers;
        } catch {
            // the number is a convenience: the link works by id
        }
    }

    /**
     * The confirm is the transition (ADR 0005 5.2). An over-the-limit
     * customer lands the order on the credit hold: the answer is the held
     * order, never an error, so the banner is the tell.
     */
    private async handleConfirm() {
        if (!this.order) return;
        if (!confirm('Confirm this order?')) return;
        this.processing = true;
        try {
            const updated = await OrderService.confirmOrder(this.order.id, this.order.revision);
            this.order = updated;
            if (updated.status === 'on_hold') {
                ToastService.show('The customer is over their credit limit: the order is on hold', 'error');
            } else {
                ToastService.show('Order confirmed', 'success');
            }
        } catch (error) {
            ToastService.show('Failed to confirm order: ' + (error instanceof Error ? error.message : error), 'error');
        } finally {
            this.processing = false;
        }
    }

    /** Releasing a hold skips the credit check (roles admin, owner, finance). */
    private async handleRelease() {
        if (!this.order) return;
        this.processing = true;
        try {
            this.order = await OrderService.releaseHold(this.order.id, this.order.revision);
            ToastService.show('Hold released', 'success');
        } catch (error) {
            ToastService.show('Failed to release hold: ' + (error instanceof Error ? error.message : error), 'error');
        } finally {
            this.processing = false;
        }
    }

    /**
     * The fulfilment is the money moment (ADR 0005 5.6): it bills everything
     * allocated, moves the stock and posts the invoice. A will-call order names
     * who collected it.
     */
    private async handleFulfil() {
        if (!this.order) return;
        const willCall = this.order.delivery_type === 'pickup';
        if (willCall && !this.fulfilOpen) {
            // a will-call order opens the pickup form under the header first
            this.fulfilOpen = true;
            return;
        }
        if (willCall && !this.pickedUpBy.trim()) {
            // inline beside the field, so it is gone the moment the name is given
            this.pickupError = 'Enter the name of the person collecting this order';
            return;
        }
        this.pickupError = '';
        this.processing = true;
        try {
            const { order, invoiceId } = await OrderService.fulfil(this.order.id, this.order.revision, {
                pickedUpBy: willCall ? this.pickedUpBy.trim() : undefined,
            });
            this.order = order;
            this.fulfilOpen = false;
            this.pickedUpBy = '';
            ToastService.show(invoiceId ? 'Order fulfilled and invoiced' : 'Order fulfilled', 'success');
        } catch (error) {
            ToastService.show('Failed to fulfil order: ' + (error instanceof Error ? error.message : error), 'error');
        } finally {
            this.processing = false;
        }
    }

    private closeFulfil() {
        this.fulfilOpen = false;
        this.pickedUpBy = '';
        this.pickupError = '';
    }

    /** The retry for a back order: allocates whatever stock has arrived. */
    private async handleAllocate() {
        if (!this.order) return;
        this.processing = true;
        try {
            this.order = await OrderService.allocate(this.order.id, this.order.revision);
            ToastService.show(this.order.status === 'backordered' ? 'Still on back order' : 'Back order released', 'success');
        } catch (error) {
            ToastService.show('Failed to allocate order: ' + (error instanceof Error ? error.message : error), 'error');
        } finally {
            this.processing = false;
        }
    }

    private async handleCancel() {
        if (!this.order) return;
        const reason = prompt('Cancel this order. Reason:');
        if (!reason) return;
        this.processing = true;
        try {
            this.order = await OrderService.cancelOrder(this.order.id, this.order.revision, reason);
            ToastService.show('Order cancelled', 'success');
        } catch (error) {
            ToastService.show('Failed to cancel order: ' + (error instanceof Error ? error.message : error), 'error');
        } finally {
            this.processing = false;
        }
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
            return html`<div class="text-white">Loading order details...</div>`;
        }
        if (this.error || !this.order) {
            return html`<div class="text-white">Failed to load order details.</div>`;
        }

        const order = this.order;
        const marginPct = order.margin_percent !== null ? Number(order.margin_percent) : null;
        const marginColor = marginPct === null ? 'text-zinc-400' :
            marginPct >= 20 ? 'text-emerald-400' : marginPct >= 10 ? 'text-amber-400' : 'text-red-400';

        return html`
            <div class="space-y-6 max-w-5xl mx-auto">
                <!-- Header -->
                <div class="flex items-center justify-between pb-6 border-b border-white/10">
                    <div class="min-w-0">
                        <div class="flex items-center gap-4 mb-2">
                            <h1 class="text-3xl font-bold font-mono text-white">${order.number}</h1>
                            <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium border border-transparent ${this.getStatusBadgeClass(order.status)}">
                                ${formatOrderStatus(order.status)}
                            </span>
                        </div>
                        <p class="text-muted-foreground">
                            Created on ${new Date(order.created_at).toLocaleString()}
                            · ${order.currency}
                            · ${order.delivery_type === 'pickup' ? 'Pickup' : 'Delivery'}
                            ${order.tax_rate_percent !== null ? html` · tax ${order.tax_rate_percent}%` : html` · tax by provider`}
                        </p>
                    </div>
                    <div class="flex gap-3 shrink-0">
                        ${order.status === 'draft' ? html`
                            <button
                                @click=${() => this.handleConfirm()}
                                ?disabled=${this.processing}
                                class="bg-gable-green text-black font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-gable-green/90 transition-colors flex items-center gap-2"
                            >
                                ${this.processing ? 'Processing...' : html`${icon(Check, 18)} Confirm Order`}
                            </button>
                        ` : nothing}
                        ${order.status === 'on_hold' ? html`
                            <button
                                @click=${() => this.handleRelease()}
                                ?disabled=${this.processing}
                                class="bg-amber-500 text-black font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-amber-600 transition-colors flex items-center gap-2"
                            >
                                ${this.processing ? 'Processing...' : html`${icon(LockOpen, 18)} Release Hold`}
                            </button>
                        ` : nothing}
                        ${(order.status === 'confirmed' || order.status === 'backordered') ? html`
                            <button
                                @click=${() => this.handleFulfil()}
                                ?disabled=${this.processing}
                                class="bg-gable-green text-black font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-gable-green/90 transition-colors flex items-center gap-2"
                            >
                                ${this.processing ? 'Processing...' : html`${icon(Check, 18)} Fulfil Order`}
                            </button>
                        ` : nothing}
                        ${order.status === 'backordered' ? html`
                            <button
                                @click=${() => this.handleAllocate()}
                                ?disabled=${this.processing}
                                class="bg-amber-500 text-black font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-amber-600 transition-colors flex items-center gap-2"
                            >
                                Allocate Stock
                            </button>
                        ` : nothing}
                        ${(order.status === 'draft' || order.status === 'confirmed' || order.status === 'backordered') ? html`
                            <button
                                @click=${() => this.handleCancel()}
                                ?disabled=${this.processing}
                                class="bg-red-500/20 text-red-400 font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-red-500/30 transition-colors flex items-center gap-2"
                            >
                                ${icon(XCircle, 18)} Cancel
                            </button>
                        ` : nothing}
                        ${(order.status === 'confirmed' || order.status === 'backordered' || order.status === 'fulfilled') ? html`
                            <button
                                @click=${() => window.open(`${API_URL}/api/v1/documents/print/pickticket/${order.id}`, '_blank')}
                                class="bg-white/10 text-white font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-white/20 transition-colors flex items-center gap-2"
                            >
                                ${icon(Printer, 18)} Pick Ticket
                            </button>
                        ` : nothing}
                    </div>
                </div>

                ${this.fulfilOpen && order.delivery_type === 'pickup' && (order.status === 'confirmed' || order.status === 'backordered') ? html`
                    <div class="rounded-lg border border-white/10 bg-black/30 p-4 flex items-start gap-3" data-testid="fulfil-pickup">
                        <div class="flex-1">
                            <input
                                type="text"
                                aria-label="Picked up by"
                                placeholder="Picked up by"
                                maxlength="200"
                                .value=${this.pickedUpBy}
                                @input=${(e: Event) => { this.pickedUpBy = (e.target as HTMLInputElement).value; this.pickupError = ''; }}
                                @keydown=${(e: KeyboardEvent) => { if (e.key === 'Enter') this.handleFulfil(); }}
                                class="bg-black/30 border border-white/10 rounded px-3 py-2 text-white text-sm w-full max-w-sm"
                            />
                            ${this.pickupError ? html`<p class="mt-2 text-sm text-red-400" role="alert">${this.pickupError}</p>` : nothing}
                        </div>
                        <button
                            @click=${() => this.handleFulfil()}
                            ?disabled=${this.processing}
                            class="bg-gable-green text-black font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-gable-green/90 transition-colors flex items-center gap-2"
                        >
                            ${this.processing ? 'Processing...' : html`${icon(Check, 18)} Confirm`}
                        </button>
                        <button
                            @click=${() => this.closeFulfil()}
                            ?disabled=${this.processing}
                            class="bg-white/10 text-white font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-white/20 transition-colors"
                        >
                            Cancel
                        </button>
                    </div>
                ` : nothing}

                ${order.hold_reason ? html`
                    <div class="rounded-lg border border-amber-500/40 bg-amber-500/10 p-4 text-sm text-amber-300">
                        <span class="font-semibold">On hold:</span>
                        ${order.hold_reason === 'credit_limit'
                            ? 'the customer is over their credit limit'
                            : 'held manually'}
                        ${order.hold_note ? html` — ${order.hold_note}` : nothing}
                    </div>
                ` : nothing}

                <div class="grid grid-cols-3 gap-6">
                    <!-- Main Content: Lines -->
                    <div class="col-span-2 space-y-6">
                        <div class="bg-slate-steel rounded-lg border border-white/10 overflow-hidden">
                            <div class="px-6 py-4 border-b border-white/10">
                                <h2 class="font-semibold text-white">Line Items</h2>
                            </div>
                            <table class="w-full text-left text-sm" aria-label="Order line items">
                                <thead class="bg-white/5">
                                    <tr>
                                        <th class="p-4 text-muted-foreground font-medium">Item</th>
                                        <th class="p-4 text-muted-foreground font-medium text-right">Qty</th>
                                        <th class="p-4 text-muted-foreground font-medium text-right">Stock</th>
                                        <th class="p-4 text-muted-foreground font-medium text-right">Price</th>
                                        <th class="p-4 text-muted-foreground font-medium text-right">Total</th>
                                    </tr>
                                </thead>
                                <tbody class="divide-y divide-white/5">
                                    ${order.lines?.filter(l => l.line_type !== 'component').map(line => html`
                                        <tr>
                                            <td class="p-4 text-white">
                                                <div class="font-mono text-sm">
                                                    ${line.sku || line.charge_code || (line.line_type === 'text' ? 'Note' : line.description.slice(0, 24))}
                                                    <span class="ml-2 text-[10px] uppercase tracking-wide text-zinc-500">${line.line_type}</span>
                                                    ${line.price_source === 'override' ? html`<span class="ml-2 text-[10px] uppercase tracking-wide text-amber-400">override</span>` : ''}
                                                    ${line.discount_percent !== null || line.discount_cents !== null ? html`<span class="ml-2 text-[10px] uppercase tracking-wide text-blue-400">discount</span>` : ''}
                                                </div>
                                                <div class="text-xs text-muted-foreground">${line.description}</div>
                                            </td>
                                            <td class="p-4 text-white font-mono text-right">
                                                ${line.quantity ?? '—'}
                                                ${line.uom ? html`<span class="text-xs text-zinc-500 ml-1">${line.uom}</span>` : ''}
                                                ${line.price_uom && line.uom && line.price_uom !== line.uom
                                                    ? html`<div class="text-[10px] text-zinc-500">per ${line.price_uom}</div>` : ''}
                                            </td>
                                            <td class="p-4 font-mono text-right text-xs text-zinc-500" data-testid="line-stock">
                                                ${line.line_type === 'product' && line.product_id !== null ? html`
                                                    <div class="text-zinc-300">${line.quantity_allocated} allocated</div>
                                                    ${line.quantity_backordered !== '0' ? html`<div class="text-amber-400">${line.quantity_backordered} back ordered</div>` : nothing}
                                                    ${line.quantity_fulfilled !== '0' ? html`<div class="text-gable-green">${line.quantity_fulfilled} shipped</div>` : nothing}
                                                ` : '—'}
                                            </td>
                                            <td class="p-4 text-white font-mono text-right">
                                                ${line.unit_price_ten_thousandths !== null
                                                    ? html`${formatPrice4(line.unit_price_ten_thousandths)}`
                                                    : '—'}
                                            </td>
                                            <td class="p-4 text-gable-green font-mono text-right font-medium">
                                                ${line.line_total_cents !== null ? formatCents(line.line_total_cents) : '—'}
                                            </td>
                                        </tr>
                                    `)}
                                </tbody>
                                <tfoot class="bg-white/5">
                                    <tr>
                                        <td colspan="4" class="p-4 text-right font-bold text-white uppercase">Subtotal</td>
                                        <td class="p-4 text-right font-bold text-white font-mono">${formatCents(order.subtotal_cents)}</td>
                                    </tr>
                                    <tr>
                                        <td colspan="4" class="p-4 text-right text-zinc-400">Tax</td>
                                        <td class="p-4 text-right text-zinc-400 font-mono">${formatCents(order.tax_cents)}</td>
                                    </tr>
                                    <tr>
                                        <td colspan="4" class="p-4 text-right font-bold text-white uppercase">Total</td>
                                        <td class="p-4 text-right font-bold text-gable-green font-mono text-lg">${formatCents(order.total_cents)}</td>
                                    </tr>
                                </tfoot>
                            </table>
                        </div>
                    </div>

                    <!-- Sidebar -->
                    <div class="space-y-6">
                        <!-- Customer Details -->
                        <div class="bg-slate-steel rounded-lg border border-white/10 p-6">
                            <h3 class="font-semibold text-white mb-4">Customer Details</h3>
                            <div class="space-y-2 text-sm">
                                ${order.customer_name ? html`<p class="text-white font-medium text-base">${order.customer_name}</p>` : nothing}
                                <p class="text-muted-foreground">Account: <span class="text-white font-mono">${order.customer_id.slice(0, 8)}</span></p>
                                ${order.customer_po ? html`<p class="text-muted-foreground">PO: <span class="text-white font-mono">${order.customer_po}</span></p>` : nothing}
                                ${order.ship_to ? html`
                                    <p class="text-muted-foreground pt-2">
                                        ${order.ship_to.line1}<br>
                                        ${order.ship_to.city}, ${order.ship_to.region} ${order.ship_to.postal_code}
                                    </p>
                                ` : nothing}
                            </div>
                        </div>

                        <!-- Salesperson Card -->
                        <div class="bg-slate-steel rounded-lg border border-white/10 p-6">
                            <h3 class="font-semibold text-white mb-4 flex items-center gap-2">
                                ${icon(User, 16, 'text-blue-400')} Salesperson
                            </h3>
                            ${this.salesperson ? html`
                                <div class="space-y-3 text-sm">
                                    <p class="text-white font-medium text-base">${this.salesperson.name}</p>
                                    <p class="text-muted-foreground">
                                        <span class="px-2 py-0.5 rounded text-xs font-medium bg-blue-500/10 text-blue-400">${this.salesperson.role}</span>
                                    </p>
                                    <div class="space-y-1.5 pt-1">
                                        <p class="text-zinc-400 flex items-center gap-2">
                                            ${icon(Mail, 14)} ${this.salesperson.email}
                                        </p>
                                        <p class="text-zinc-400 flex items-center gap-2">
                                            ${icon(Phone, 14)} ${this.salesperson.phone}
                                        </p>
                                    </div>
                                </div>
                            ` : html`
                                <p class="text-sm text-zinc-500">No salesperson assigned</p>
                            `}
                        </div>

                        <!-- Margin & Commission Card -->
                        <div class="bg-slate-steel rounded-lg border border-white/10 p-6">
                            <h3 class="font-semibold text-white mb-4 flex items-center gap-2">
                                ${icon(DollarSign, 16, 'text-emerald-400')} Margin & Commission
                            </h3>
                            <div class="space-y-3 text-sm">
                                <div class="flex justify-between">
                                    <span class="text-zinc-400">Revenue</span>
                                    <span class="text-white font-mono font-medium">${formatCents(order.subtotal_cents)}</span>
                                </div>
                                <div class="flex justify-between">
                                    <span class="text-zinc-400">Cost</span>
                                    <span class="text-white font-mono">${formatCents(order.total_cost_cents)}</span>
                                </div>
                                <div class="border-t border-white/10 pt-3 flex justify-between">
                                    <span class="text-zinc-400">Margin</span>
                                    <span class="font-mono font-bold ${marginColor}">
                                        ${formatCents(order.total_margin_cents)}${marginPct !== null ? ` (${marginPct.toFixed(1)}%)` : ''}
                                    </span>
                                </div>
                                <div class="flex justify-between">
                                    <span class="text-zinc-400">Commission</span>
                                    <span class="text-white font-mono">${formatCents(order.total_commission_cents)}</span>
                                </div>
                            </div>
                        </div>

                        <!-- Invoices -->
                        <div class="bg-slate-steel rounded-lg border border-white/10 p-6">
                            <h3 class="font-semibold text-white mb-4">Invoices</h3>
                            ${order.invoice_ids.length > 0 ? html`
                                <div class="space-y-2">
                                    ${order.invoice_ids.map(id => html`
                                        <a href="/invoices/${id}" class="block font-mono text-sm text-gable-green hover:underline">${this.invoiceNumbers[id] ?? id.slice(0, 8)}</a>
                                    `)}
                                </div>
                            ` : html`
                                <p class="text-sm text-zinc-500">None yet</p>
                            `}
                        </div>
                    </div>
                </div>
            </div>
        `;
    }
}
