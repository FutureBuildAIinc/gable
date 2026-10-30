// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { openAuthedPdf } from '../../lib/authedPdf.ts';
import { WalkTruncatedError } from '../../lib/cursorWalk';
import { InvoiceService } from '../../services/InvoiceService.ts';
import { CreditMemoService } from '../../services/CreditMemoService.ts';
import { OrderService } from '../../services/OrderService.ts';
import { paymentService } from '../../services/paymentService.ts';
import { EmailDeliveryService, type EmailDelivery } from '../../services/EmailDeliveryService.ts';
import { emailOutcomeWords, emailDeliveryWords } from '../../lib/emailWords.ts';
import { ApiError, apiErrorMessage } from '../../services/apiError.ts';
import { type Invoice, formatInvoiceStatus, getInvoiceStatusColor } from '../../types/invoice.ts';
import {
    type CreditMemoSummary,
    creditMemoLabel,
    formatCreditMemoStatus,
    formatReasonCode,
    getCreditMemoStatusColor,
} from '../../types/creditMemo.ts';
import type { PaymentApplication, CreatePaymentRequest } from '../../types/payment.ts';
import { Download, CreditCard, Mail, RotateCcw, Ban } from 'lucide';
import { formatCents, formatDay } from '../../lib/utils.ts';
import { creditTextClass } from '../../lib/credit-display.ts';
import { formatDate, formatDateTime } from '../../lib/dates.ts';
import { chipClass, overdueBadge } from '../../components/invoices/chips.ts';
import { renderSalesLines } from '../../components/invoices/lines-table.ts';

// Side-effect imports: register child custom elements
import '../../components/invoices/PaymentModal.ts';
import '../../components/invoices/SalesDialog.ts';


/** Plain words for the blockers a void can be refused with. */
export function voidBlockerHint(err: unknown): string {
    if (!(err instanceof ApiError)) return apiErrorMessage(err, 'Failed to void invoice');
    if (err.hasBlocker('has_applications')) {
        return `${err.message}\nReverse the payment, or the applied credit memo, then void the invoice.`;
    }
    if (err.hasBlocker('has_credit_memos')) {
        return `${err.message}\nThe credit memos are listed on this page: void them, then void the invoice.`;
    }
    return err.displayMessage;
}

@customElement('gable-invoice-detail')
export class GableInvoiceDetail extends LitElement {
    createRenderRoot() { return this; }

    @property({ attribute: 'route-id' }) routeId = '';

    @state() private invoice: Invoice | null = null;
    @state() private orderNumber: string | null = null;
    @state() private applications: PaymentApplication[] = [];
    @state() private creditMemos: CreditMemoSummary[] = [];
    @state() private emailSends: EmailDelivery[] = [];
    @state() private loading = true;
    @state() private error = false;
    @state() private isPaymentModalOpen = false;
    @state() private voidOpen = false;
    @state() private voidBusy = false;
    @state() private voidError = '';

    connectedCallback() {
        super.connectedCallback();
        if (this.routeId) this.loadAll(this.routeId);
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('routeId') && changed.get('routeId') !== undefined && this.routeId) {
            this.loading = true;
            this.loadAll(this.routeId);
        }
    }

    private loadAll(id: string) {
        this.loadInvoice(id);
        this.loadPayments(id);
        this.loadCreditMemos(id);
        this.loadEmailSends(id);
    }

    /** The invoice's email sends and what each delivery row says of them. */
    private async loadEmailSends(id: string) {
        try {
            this.emailSends = await EmailDeliveryService.listForDocument(id);
        } catch {
            // the sends list is a convenience beside the invoice; a refused
            // read (a sign in that may not list deliveries) leaves it empty
            this.emailSends = [];
        }
    }

    private async loadInvoice(id: string) {
        try {
            const data = await InvoiceService.getInvoice(id);
            this.invoice = data;
            this.error = false;
            this.orderNumber = null;
            if (data.order_id) {
                try {
                    this.orderNumber = (await OrderService.getOrder(data.order_id)).number;
                } catch {
                    // the order number is a convenience: the link still works by id
                }
            }
        } catch (err) {
            console.error(err);
            this.error = true;
            ToastService.show('Failed to load invoice', 'error');
        } finally {
            this.loading = false;
        }
    }

    private async loadPayments(id: string) {
        try {
            // the invoice's applications, newest first; a reversed one is
            // still listed with its reversal date
            this.applications = await paymentService.historyAll(id);
        } catch (error) {
            console.error('Failed to load payments', error);
            ToastService.show(error instanceof WalkTruncatedError ? error.message : 'Failed to load payments', 'error');
        }
    }

    private async loadCreditMemos(id: string) {
        try {
            this.creditMemos = (await CreditMemoService.listCreditMemos({ invoiceId: id, limit: 100 })).items;
        } catch (error) {
            console.error('Failed to load credit memos', error);
        }
    }

    private async handlePayment(input: CreatePaymentRequest) {
        await paymentService.createPayment(input);
        if (this.routeId) this.loadAll(this.routeId);
    }

    private async handlePrintInvoice(id: string) {
        try {
            const shown = await openAuthedPdf(`/api/v1/documents/print/invoice/${id}`, 'Failed to read the invoice document', 'invoice.pdf');
            if (shown.blocked) ToastService.show('Pop ups are blocked: the invoice was saved as a download', 'success');
        } catch (err) {
            ToastService.show(apiErrorMessage(err, 'Failed to print the invoice'), 'error');
        }
    }

    /** Queues the invoice's email, then follows the delivery the 202 names:
     * the toast says what actually happened, never success on the 202 alone. */
    private async handleEmailInvoice() {
        if (!this.invoice?.id) return;
        try {
            const queued = await InvoiceService.emailInvoice(this.invoice.id);
            const delivery = queued.delivery_id
                ? await EmailDeliveryService.follow(queued.delivery_id, this.invoice.id)
                : undefined;
            const words = emailOutcomeWords(delivery, 'invoice');
            const kind = delivery?.status === 'failed' ? 'error' : delivery?.status === 'sent' ? 'success' : 'info';
            ToastService.show(words, kind);
            this.loadEmailSends(this.invoice.id);
        } catch (err) {
            ToastService.show(apiErrorMessage(err, 'Failed to email invoice'), 'error');
        }
    }

    private openVoid() {
        this.voidError = '';
        this.voidOpen = true;
    }

    private async confirmVoid(reason: string) {
        if (!this.invoice) return;
        this.voidBusy = true;
        this.voidError = '';
        try {
            this.invoice = await InvoiceService.voidInvoice(this.invoice.id, this.invoice.revision, reason);
            this.voidOpen = false;
            ToastService.show('Invoice voided', 'success');
            // the credit memos listed beside it may have changed since the page loaded
            this.loadCreditMemos(this.invoice.id);
        } catch (err) {
            if (err instanceof ApiError && err.isStaleRevision) {
                // someone changed the invoice since it was loaded: show the current one
                this.voidOpen = false;
                ToastService.show('The invoice changed while you were looking at it. It has been reloaded.', 'error');
                if (this.routeId) this.loadAll(this.routeId);
            } else {
                this.voidError = voidBlockerHint(err);
            }
        } finally {
            this.voidBusy = false;
        }
    }

    render() {
        if (this.loading) return html`<div class="text-white">Loading invoice...</div>`;
        if (this.error || !this.invoice) return html`<div class="text-white">Failed to load invoice.</div>`;

        const inv = this.invoice;
        const isVoid = inv.status === 'void';
        const canPay = inv.status === 'unpaid' || inv.status === 'partial';
        const canVoid = inv.status === 'unpaid';
        const totalPaid = this.applications.filter(a => !a.reversed_at).reduce((sum, a) => sum + a.amount_cents, 0);

        return html`
            <div class="space-y-6 max-w-6xl mx-auto pb-20">
                <!-- Header -->
                <div class="flex items-start justify-between gap-4 flex-wrap pb-6 border-b border-white/10">
                    <div class="min-w-0">
                        <div class="flex items-center gap-3 mb-2 flex-wrap">
                            <h1 class="text-3xl font-bold font-mono text-white">${inv.number}</h1>
                            <span class=${chipClass(getInvoiceStatusColor(inv.status))}>${formatInvoiceStatus(inv.status)}</span>
                            ${overdueBadge(inv.is_overdue)}
                        </div>
                        <p class="text-muted-foreground text-sm">
                            Invoice date ${formatDay(inv.invoice_date)}
                            · ${inv.currency}
                            · ${inv.origin === 'pos' ? 'Counter sale' : inv.delivery_type === 'pickup' ? 'Pickup' : 'Delivery'}
                            ${inv.picked_up_by ? html` · picked up by ${inv.picked_up_by}` : nothing}
                        </p>
                    </div>
                    ${!isVoid ? html`
                        <div class="flex gap-3 flex-wrap">
                            <button
                                @click=${() => this.handlePrintInvoice(inv.id)}
                                class="bg-white/10 text-white hover:bg-white/20 px-4 py-2 rounded flex items-center gap-2 transition-colors border border-white/10 whitespace-nowrap"
                            >
                                ${icon(Download, 18)} Print PDF
                            </button>
                            <button
                                @click=${() => this.handleEmailInvoice()}
                                class="bg-white/10 text-white hover:bg-white/20 px-4 py-2 rounded flex items-center gap-2 transition-colors border border-white/10 whitespace-nowrap"
                            >
                                ${icon(Mail, 18)} Email
                            </button>
                            ${canPay ? html`
                                <button
                                    @click=${() => { this.isPaymentModalOpen = true; }}
                                    class="bg-gable-green text-black font-bold px-4 py-2 rounded flex items-center gap-2 hover:bg-gable-green/90 transition-colors whitespace-nowrap"
                                >
                                    ${icon(CreditCard, 18)} Pay
                                </button>
                            ` : nothing}
                            <a
                                href="/credit-memos/new?invoice_id=${inv.id}"
                                class="bg-white/10 text-white hover:bg-white/20 px-4 py-2 rounded flex items-center gap-2 transition-colors border border-white/10 whitespace-nowrap"
                            >
                                ${icon(RotateCcw, 18)} Create credit memo
                            </a>
                            ${canVoid ? html`
                                <button
                                    @click=${() => this.openVoid()}
                                    class="bg-red-500/20 text-red-400 font-bold px-4 py-2 rounded flex items-center gap-2 hover:bg-red-500/30 transition-colors whitespace-nowrap"
                                >
                                    ${icon(Ban, 18)} Void
                                </button>
                            ` : nothing}
                        </div>
                    ` : nothing}
                </div>

                ${isVoid ? html`
                    <div class="rounded-lg border border-red-500/50 bg-red-500/10 p-4 text-red-300" data-testid="void-banner">
                        <div class="text-lg font-bold tracking-wide text-red-400">VOID</div>
                        <div class="text-sm mt-1">
                            This invoice was voided${inv.voided_at ? html` on ${formatDateTime(inv.voided_at)}` : nothing}${inv.voided_by ? html` by ${inv.voided_by}` : nothing}.
                            ${inv.void_reason ? html`<span class="block mt-1">Reason: <span class="text-white">${inv.void_reason}</span></span>` : nothing}
                        </div>
                    </div>
                ` : nothing}

                <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
                    <!-- Main: lines, totals, history -->
                    <div class="lg:col-span-2 space-y-6 min-w-0">
                        <div class="bg-slate-steel rounded-lg border border-white/10 overflow-hidden ${isVoid ? 'opacity-70' : ''}">
                            <div class="px-6 py-4 border-b border-white/10">
                                <h2 class="font-semibold text-white">Line Items</h2>
                            </div>
                            ${renderSalesLines(inv.lines, { ariaLabel: 'Invoice line items' })}
                            <div class="bg-white/5 px-6 py-4 space-y-2 text-sm" data-testid="invoice-totals">
                                <div class="flex justify-between"><span class="font-bold text-white uppercase">Subtotal</span><span class="font-bold text-white font-mono">${formatCents(inv.subtotal_cents)}</span></div>
                                <div class="flex justify-between text-zinc-400">
                                    <span>Tax ${inv.tax_exempt ? '(exempt)' : inv.tax_rate_percent !== null ? `(${inv.tax_rate_percent}%, ${inv.tax_source.replace(/_/g, ' ')})` : `(${inv.tax_source.replace(/_/g, ' ')})`}</span>
                                    <span class="font-mono">${formatCents(inv.tax_cents)}</span>
                                </div>
                                <div class="flex justify-between border-t border-white/10 pt-2"><span class="font-bold text-white uppercase">Total</span><span class="font-bold text-gable-green font-mono text-lg">${formatCents(inv.total_cents)}</span></div>
                                <div class="flex justify-between"><span class="text-zinc-300">Open amount</span><span class="font-mono ${inv.open_cents > 0 ? 'text-amber-400' : 'text-zinc-500'}" data-testid="open-amount">${formatCents(inv.open_cents)}</span></div>
                            </div>
                        </div>

                        <!-- Credit memos against this invoice -->
                        <div class="bg-slate-steel rounded-lg border border-white/10 overflow-hidden">
                            <div class="px-6 py-4 border-b border-white/10 flex items-center justify-between">
                                <h2 class="font-semibold text-white">Credit memos</h2>
                                ${!isVoid ? html`<a href="/credit-memos/new?invoice_id=${inv.id}" class="text-sm text-blue-400 hover:underline">New credit memo</a>` : nothing}
                            </div>
                            ${this.creditMemos.length === 0 ? html`
                                <p class="px-6 py-4 text-sm text-zinc-500">No credit memos against this invoice.</p>
                            ` : html`
                                <div class="overflow-x-auto">
                                    <table class="w-full text-left text-sm" aria-label="Credit memos for this invoice">
                                        <thead class="bg-white/5">
                                            <tr>
                                                <th class="p-3 text-muted-foreground font-medium">Number</th>
                                                <th class="p-3 text-muted-foreground font-medium">Reason</th>
                                                <th class="p-3 text-muted-foreground font-medium text-right">Total</th>
                                                <th class="p-3 text-muted-foreground font-medium">Status</th>
                                            </tr>
                                        </thead>
                                        <tbody class="divide-y divide-white/5">
                                            ${this.creditMemos.map(cm => html`
                                                <tr>
                                                    <td class="p-3 font-mono"><a href="/credit-memos/${cm.id}" class="text-blue-400 hover:underline">${creditMemoLabel(cm)}</a></td>
                                                    <td class="p-3 text-zinc-300">${formatReasonCode(cm.reason_code)}</td>
                                                    <td class="p-3 font-mono text-right ${creditTextClass(cm.total_cents)}">${formatCents(cm.total_cents)}</td>
                                                    <td class="p-3"><span class=${chipClass(getCreditMemoStatusColor(cm.status))}>${formatCreditMemoStatus(cm.status)}</span></td>
                                                </tr>
                                            `)}
                                        </tbody>
                                    </table>
                                </div>
                            `}
                        </div>

                        <!-- Email sends of this invoice: what each delivery row says -->
                        <div class="bg-slate-steel rounded-lg border border-white/10 overflow-hidden" data-testid="invoice-email-sends">
                            <div class="px-6 py-4 border-b border-white/10 flex items-center justify-between">
                                <h2 class="font-semibold text-white">Email sends</h2>
                            </div>
                            ${this.emailSends.length === 0 ? html`
                                <p class="px-6 py-4 text-sm text-zinc-500">This invoice has not been emailed.</p>
                            ` : html`
                                <div class="overflow-x-auto">
                                    <table class="w-full text-left text-sm" aria-label="Email sends of this invoice">
                                        <thead class="bg-white/5">
                                            <tr>
                                                <th class="p-3 text-muted-foreground font-medium">Sent to</th>
                                                <th class="p-3 text-muted-foreground font-medium">When</th>
                                                <th class="p-3 text-muted-foreground font-medium">Outcome</th>
                                            </tr>
                                        </thead>
                                        <tbody class="divide-y divide-white/5">
                                            ${this.emailSends.map(send => html`
                                                <tr data-delivery-status=${send.status}>
                                                    <td class="p-3 text-zinc-300">${send.recipient}</td>
                                                    <td class="p-3 text-zinc-400">${formatDateTime(send.created_at)}</td>
                                                    <td class="p-3 text-zinc-300">${emailDeliveryWords(send.status)}</td>
                                                </tr>
                                            `)}
                                        </tbody>
                                    </table>
                                </div>
                            `}
                        </div>

                        ${this.applications.length > 0 ? html`
                            <div class="bg-slate-steel rounded-lg border border-white/10 overflow-hidden">
                                <div class="px-6 py-4 border-b border-white/10 flex justify-between items-center">
                                    <h2 class="font-semibold text-white">Payment History</h2>
                                    <span class="text-zinc-400 text-sm">Paid: <span class="text-green-400 font-mono">${formatCents(totalPaid)}</span></span>
                                </div>
                                <div class="overflow-x-auto">
                                    <table class="w-full text-left text-sm" aria-label="Payment history">
                                        <thead class="bg-white/5">
                                            <tr>
                                                <th class="p-3 text-muted-foreground font-medium">Date</th>
                                                <th class="p-3 text-muted-foreground font-medium">Kind</th>
                                                <th class="p-3 text-muted-foreground font-medium">Status</th>
                                                <th class="p-3 text-muted-foreground font-medium text-right">Amount</th>
                                            </tr>
                                        </thead>
                                        <tbody class="divide-y divide-white/5">
                                            ${this.applications.map(a => html`
                                                <tr>
                                                    <td class="p-3 text-zinc-300">${formatDateTime(a.created_at)}</td>
                                                    <td class="p-3 text-zinc-300 font-bold">${a.kind}</td>
                                                    <td class="p-3 ${a.reversed_at ? 'text-zinc-500' : 'text-zinc-300'}">${a.reversed_at ? `reversed ${formatDate(a.reversed_at)}` : 'live'}</td>
                                                    <td class="p-3 text-right text-white font-mono font-bold">${formatCents(a.amount_cents)}</td>
                                                </tr>
                                            `)}
                                        </tbody>
                                    </table>
                                </div>
                            </div>
                        ` : nothing}
                    </div>

                    <!-- Sidebar -->
                    <div class="space-y-6">
                        <div class="bg-slate-steel rounded-lg border border-white/10 p-6">
                            <h3 class="font-semibold text-white mb-4">Bill To</h3>
                            <div class="space-y-2 text-sm">
                                <p class="text-white font-medium text-base"><a href="/accounts/${inv.customer_id}" class="hover:underline">${inv.customer_name || 'Customer'}</a></p>
                                ${inv.ship_to ? html`
                                    <p class="text-muted-foreground pt-2" data-testid="ship-to">
                                        <span class="text-zinc-300">${inv.ship_to.name}</span><br>
                                        ${inv.ship_to.line1}${inv.ship_to.line2 ? html`<br>${inv.ship_to.line2}` : nothing}<br>
                                        ${inv.ship_to.city}, ${inv.ship_to.region} ${inv.ship_to.postal_code}
                                    </p>
                                ` : nothing}
                            </div>
                        </div>

                        <div class="bg-slate-steel rounded-lg border border-white/10 p-6">
                            <h3 class="font-semibold text-white mb-4">Terms and references</h3>
                            <dl class="space-y-2 text-sm">
                                <div class="flex justify-between gap-4"><dt class="text-zinc-400">Due date</dt><dd class="font-mono ${inv.is_overdue ? 'text-red-400' : 'text-zinc-200'}" data-testid="due-date">${formatDay(inv.due_date)}</dd></div>
                                ${inv.discount_percent !== null ? html`
                                    <div class="flex justify-between gap-4"><dt class="text-zinc-400">Early payment</dt><dd class="text-zinc-200 font-mono">${inv.discount_percent}%${inv.discount_due_date ? html` by ${formatDay(inv.discount_due_date)}` : nothing}</dd></div>
                                ` : nothing}
                                <div class="flex justify-between gap-4"><dt class="text-zinc-400">Order</dt><dd class="font-mono">${inv.order_id ? html`<a href="/orders/${inv.order_id}" class="text-blue-400 hover:underline">${this.orderNumber ?? inv.order_id.slice(0, 8)}</a>` : html`<span class="text-zinc-500">counter sale</span>`}</dd></div>
                                ${inv.job_id ? html`<div class="flex justify-between gap-4"><dt class="text-zinc-400">Job</dt><dd class="text-zinc-200 font-mono">${inv.job_id.slice(0, 8)}</dd></div>` : nothing}
                                <div class="flex justify-between gap-4"><dt class="text-zinc-400">Currency</dt><dd class="text-zinc-200 font-mono">${inv.currency}</dd></div>
                                <div class="flex justify-between gap-4"><dt class="text-zinc-400">Origin</dt><dd class="text-zinc-200">${inv.origin === 'pos' ? 'Counter' : 'Order'}</dd></div>
                                <div class="flex justify-between gap-4"><dt class="text-zinc-400">Delivery</dt><dd class="text-zinc-200">${inv.delivery_type === 'pickup' ? 'Pickup' : 'Delivery'}</dd></div>
                                ${inv.picked_up_by ? html`<div class="flex justify-between gap-4"><dt class="text-zinc-400">Picked up by</dt><dd class="text-zinc-200">${inv.picked_up_by}</dd></div>` : nothing}
                                ${inv.paid_at ? html`<div class="flex justify-between gap-4"><dt class="text-zinc-400">Paid</dt><dd class="text-zinc-200">${formatDate(inv.paid_at)}</dd></div>` : nothing}
                            </dl>
                        </div>
                    </div>
                </div>

                <gable-payment-modal
                    customer-id=${inv.customer_id}
                    ?is-open=${this.isPaymentModalOpen}
                    @close=${() => { this.isPaymentModalOpen = false; }}
                    @save=${(e: CustomEvent<CreatePaymentRequest>) => this.handlePayment(e.detail)}
                    .invoiceId=${inv.id}
                    .amountDue=${inv.open_cents > 0 ? inv.open_cents : 0}
                ></gable-payment-modal>

                <gable-sales-dialog
                    ?is-open=${this.voidOpen}
                    heading="Void invoice ${inv.number}"
                    body="Voiding cancels this invoice and reverses its entry in the ledger. It cannot be undone, and the number stays on record as void."
                    confirm-label="Void invoice"
                    require-reason
                    danger
                    ?busy=${this.voidBusy}
                    .error=${this.voidError}
                    @close=${() => { this.voidOpen = false; }}
                    @confirm=${(e: CustomEvent<{ reason: string }>) => this.confirmVoid(e.detail.reason)}
                ></gable-sales-dialog>
            </div>
        `;
    }
}
