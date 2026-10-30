// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { formatDateTime } from '../../lib/dates.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { openAuthedPdf } from '../../lib/authedPdf.ts';
import { confirmDialog, promptDialog } from '../../components/ui/app-dialog.ts';
import '../../components/orders/DepositModal.ts';
import { paymentService } from '../../services/paymentService.ts';
import type { OrderDeposit } from '../../types/order.ts';
import { OrderService } from '../../services/OrderService.ts';
import { CustomerService } from '../../services/CustomerService.ts';
import { InvoiceService } from '../../services/InvoiceService.ts';
import { SalesTeamService } from '../../services/SalesTeamService.ts';
import { type Order, type OrderStatus, type CreateOrderLineRequest, formatOrderStatus, getStatusColor } from '../../types/order.ts';
import type { SalesPerson } from '../../types/salesteam.ts';
import type { Alert } from '../../types/customer.ts';
import { Check, Printer, User, DollarSign, Mail, Phone, XCircle, LockOpen, Wallet } from 'lucide';
import { formatCents, formatPrice4 } from '../../lib/utils.ts';
import { ConfiguredItemHandoff } from '../../lib/configured-item.ts';


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
    /** The customer's alerts that speak at the order desk, read only here. */
    @state() private alerts: Alert[] = [];
    @state() private depositOpen = false;

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
            void this.loadAlerts(data.customer_id);
            this.loadInvoiceNumbers(data.id, data.invoice_ids);
            await this.takePendingItem(data);
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

    /**
     * A configured item the configurator handed over lands on the draft order
     * the dealer picked: the line carries the server's price and the
     * configuration, and the order is saved with it.
     */
    private async takePendingItem(order: Order) {
        const pending = ConfiguredItemHandoff.peek();
        if (!pending || order.status !== 'draft') return;
        ConfiguredItemHandoff.take();
        const lines = order.lines.map((l) => this.toCreateLine(l));
        lines.push({
            product_id: pending.product_id,
            sku: pending.sku,
            description: pending.description,
            quantity: '1',
            uom: 'EA',
            unit_price_ten_thousandths: pending.unit_price_ten_thousandths,
            configuration_json: pending.configuration_json,
        });
        try {
            this.order = await OrderService.updateOrder(order.id, {
                customer_id: order.customer_id,
                delivery_type: order.delivery_type,
                revision: order.revision,
                lines,
            });
            ToastService.success('The configured item is on the order');
        } catch {
            ToastService.error('The configured item could not be added to the order');
        }
    }

    /** The order's own line, as the update request carries it. */
    private toCreateLine(l: Order['lines'][number]): CreateOrderLineRequest {
        return {
            id: l.id,
            line_type: l.line_type === 'kit' || l.line_type === 'component' ? 'product' : l.line_type,
            product_id: l.product_id ?? undefined,
            charge_code: l.charge_code ?? undefined,
            sku: l.sku ?? undefined,
            description: l.description,
            quantity: l.quantity ?? undefined,
            uom: l.uom ?? undefined,
            price_uom: l.price_uom ?? undefined,
            uom_qty: l.uom_qty ?? undefined,
            price_uom_qty: l.price_uom_qty ?? undefined,
            unit_price_ten_thousandths: l.unit_price_ten_thousandths ?? undefined,
            taxable: l.taxable,
        };
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

    private async handlePrintPickTicket(id: string) {
        try {
            const shown = await openAuthedPdf(`/api/v1/documents/print/pickticket/${id}`, 'Failed to read the pick ticket', 'pick-ticket.pdf');
            if (shown.blocked) ToastService.show('Pop ups are blocked: the pick ticket was saved as a download', 'success');
        } catch (err) {
            ToastService.show(err instanceof Error ? err.message : 'Failed to print the pick ticket', 'error');
        }
    }

    /**
     * The confirm is the transition (ADR 0005 5.2). An over-the-limit
     * customer lands the order on the credit hold: the answer is the held
     * order, never an error, so the banner is the tell.
     */
    private async handleConfirm() {
        if (!this.order) return;
        if (!await confirmDialog({ title: 'Confirm this order?' })) return;
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
        const reason = await promptDialog({ title: 'Cancel this order', label: 'Reason', required: true, confirmLabel: 'Cancel order', tone: 'danger' });
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

    /** A deposit goes on an order that can still be billed (CP-07). */
    private get canTakeDeposit(): boolean {
        return ['draft', 'on_hold', 'confirmed', 'backordered'].includes(this.order?.status ?? '');
    }

    private openDeposit() {
        this.depositOpen = true;
    }

    /**
     * The deposit rides the payments route with the order named and no
     * applications, so the cash is held unapplied until the order bills.
     * A refusal (past the balance due, a cancelled or fully invoiced order)
     * lands in the dialog's own words.
     */
    private async handleDeposit(detail: { amount_cents: number; method: string; reference?: string; notes?: string }) {
        if (!this.order) return;
        try {
            await paymentService.createPayment({
                customer_id: this.order.customer_id,
                branch_id: this.order.branch_id,
                amount_cents: detail.amount_cents,
                method: detail.method as 'cash' | 'check' | 'ach' | 'other',
                order_id: this.order.id,
                reference: detail.reference,
                notes: detail.notes,
            });
            ToastService.show('Deposit taken', 'success');
            this.depositOpen = false;
            this.loading = true;
            this.loadOrder(this.routeId);
        } catch (err) {
            ToastService.show(err instanceof Error ? err.message : 'Failed to take the deposit', 'error');
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

    /**
     * The customer's alerts that speak at the order desk: the order page only
     * reads them. The office leaves and ends them on the account page.
     */
    private async loadAlerts(customerId: string | null) {
        if (!customerId) {
            this.alerts = [];
            return;
        }
        try {
            const page = await CustomerService.listAlerts(customerId, { state: 'open', limit: 200 });
            this.alerts = page.items.filter((a) => a.show_at_orders);
        } catch {
            // The alerts are an addition to the order page: a failed read
            // leaves the page as it was.
            this.alerts = [];
        }
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
                            Created on ${formatDateTime(order.created_at)}
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
                        ${this.canTakeDeposit ? html`
                            <button
                                @click=${() => this.openDeposit()}
                                class="bg-white/10 text-white font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-white/20 transition-colors flex items-center gap-2"
                                data-testid="take-deposit"
                            >
                                ${icon(Wallet, 18)} Take deposit
                            </button>
                        ` : nothing}
                        ${(order.status === 'confirmed' || order.status === 'backordered' || order.status === 'fulfilled') ? html`
                            <button
                                @click=${() => this.handlePrintPickTicket(order.id)}
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

                <gable-deposit-modal
                    .isOpen=${this.depositOpen}
                    .orderId=${order.id}
                    .customerId=${order.customer_id}
                    .branchId=${order.branch_id}
                    .balanceDueCents=${order.balance_due_after_deposits_cents}
                    @save=${(e: CustomEvent) => this.handleDeposit(e.detail)}
                    @close=${() => { this.depositOpen = false; }}
                ></gable-deposit-modal>

                <div class="grid grid-cols-1 lg:grid-cols-3 gap-6" data-testid="order-columns">
                    <!-- Main Content: Lines -->
                    <div class="lg:col-span-2 min-w-0 space-y-6">
                        <div class="bg-slate-steel rounded-lg border border-white/10 overflow-x-auto">
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
                                <p class="text-white font-medium text-base"><a href="/accounts/${order.customer_id}" class="hover:underline">${order.customer_name || 'Customer'}</a></p>
                                ${order.customer_po ? html`<p class="text-muted-foreground">PO: <span class="text-white font-mono">${order.customer_po}</span></p>` : nothing}
                                ${order.ship_to ? html`
                                    <p class="text-muted-foreground pt-2">
                                        ${order.ship_to.line1}<br>
                                        ${order.ship_to.city}, ${order.ship_to.region} ${order.ship_to.postal_code}
                                    </p>
                                ` : nothing}
                            </div>
                        </div>

                        <!-- Customer Alerts: read only here, left and ended on the account page -->
                        ${this.alerts.length > 0 ? html`
                            <div class="bg-slate-steel rounded-lg border border-amber-500/20 p-6" data-testid="order-alerts">
                                <h3 class="font-semibold text-white mb-4">Alerts</h3>
                                <ul class="space-y-2 text-sm">
                                    ${this.alerts.map((a) => html`
                                        <li class="rounded border border-white/10 bg-slate-steel/40 px-3 py-2" data-testid="order-alert">
                                            <span class="font-semibold ${a.kind === 'warning' ? 'text-amber-400' : 'text-blue-400'}">${a.kind === 'warning' ? 'Warning:' : 'Note:'}</span>
                                            <span class="text-white">${a.text}</span>
                                        </li>
                                    `)}
                                </ul>
                            </div>
                        ` : nothing}

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

                        <!-- Deposits -->
                        <div class="bg-slate-steel rounded-lg border border-white/10 p-6" data-testid="order-deposits">
                            <h3 class="font-semibold text-white mb-4 flex items-center gap-2">
                                ${icon(Wallet, 16, 'text-gable-green')} Deposits
                            </h3>
                            ${(order.deposits?.length ?? 0) > 0 ? html`
                                <div class="space-y-3 text-sm">
                                    ${order.deposits.map((d: OrderDeposit) => html`
                                        <div class="border-b border-white/5 pb-3 last:border-0 last:pb-0" data-testid="order-deposit-row">
                                            <div class="flex justify-between items-baseline">
                                                <span class="text-white font-mono">${d.number}</span>
                                                <span class="text-gable-green font-mono font-medium">${formatCents(d.amount_cents)}</span>
                                            </div>
                                            <div class="text-xs text-zinc-500">
                                                ${d.received_on} · ${d.method}${d.reference ? html` · ${d.reference}` : nothing}
                                            </div>
                                            ${d.applications.length > 0 ? html`
                                                <div class="mt-1 text-xs text-zinc-400">
                                                    ${d.applications.map(a => html`
                                                        <div>
                                                            Applied ${formatCents(a.amount_cents)} to
                                                            <a href="/invoices/${a.invoice_id}" class="text-gable-green hover:underline font-mono">${this.invoiceNumbers[a.invoice_id] ?? a.invoice_id.slice(0, 8)}</a>
                                                        </div>
                                                    `)}
                                                </div>
                                            ` : nothing}
                                            ${d.unapplied_cents > 0 ? html`
                                                <div class="mt-1 text-xs text-blue-400">Still unapplied: <span class="font-mono">${formatCents(d.unapplied_cents)}</span></div>
                                            ` : nothing}
                                        </div>
                                    `)}
                                </div>
                                <div class="space-y-2 text-sm mt-4 pt-3 border-t border-white/10">
                                    <div class="flex justify-between">
                                        <span class="text-zinc-400">Total deposited</span>
                                        <span class="text-white font-mono font-medium" data-testid="deposit-total">${formatCents(order.deposit_total_cents)}</span>
                                    </div>
                                    <div class="flex justify-between">
                                        <span class="text-zinc-400">Unapplied</span>
                                        <span class="text-white font-mono" data-testid="deposit-unapplied">${formatCents(order.deposit_unapplied_cents)}</span>
                                    </div>
                                    <div class="flex justify-between">
                                        <span class="text-zinc-400">Balance due after deposits</span>
                                        <span class="text-white font-mono" data-testid="balance-after-deposits">${formatCents(order.balance_due_after_deposits_cents)}</span>
                                    </div>
                                </div>
                            ` : html`
                                <p class="text-sm text-zinc-500">No deposits on this order</p>
                            `}
                        </div>
                    </div>
                </div>
            </div>
        `;
    }
}
