// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { CreditMemoService } from '../../services/CreditMemoService.ts';
import { InvoiceService } from '../../services/InvoiceService.ts';
import { ApiError, apiErrorMessage } from '../../services/apiError.ts';
import {
    type CreditMemo,
    creditMemoLabel,
    formatCreditMemoStatus,
    formatReasonCode,
    getCreditMemoStatusColor,
} from '../../types/creditMemo.ts';
import { Check, Ban, Pencil } from 'lucide';
import { formatCents, formatDay } from '../../lib/utils.ts';
import { creditTextClass } from '../../lib/credit-display.ts';
import { chipClass } from '../../components/invoices/chips.ts';
import { renderSalesLines } from '../../components/invoices/lines-table.ts';

import '../../components/invoices/SalesDialog.ts';

type Pending = 'post' | 'void' | null;

@customElement('gable-credit-memo-detail')
export class GableCreditMemoDetail extends LitElement {
    createRenderRoot() { return this; }

    @property({ attribute: 'route-id' }) routeId = '';

    @state() private memo: CreditMemo | null = null;
    @state() private invoiceNumber: string | null = null;
    @state() private loading = true;
    @state() private error = false;
    @state() private pending: Pending = null;
    @state() private busy = false;
    @state() private dialogError = '';

    connectedCallback() {
        super.connectedCallback();
        if (this.routeId) this.load(this.routeId);
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('routeId') && changed.get('routeId') !== undefined && this.routeId) {
            this.loading = true;
            this.load(this.routeId);
        }
    }

    private async load(id: string) {
        try {
            this.memo = await CreditMemoService.getCreditMemo(id);
            this.error = false;
            this.invoiceNumber = null;
            if (this.memo.invoice_id) {
                try {
                    this.invoiceNumber = (await InvoiceService.getInvoice(this.memo.invoice_id)).number;
                } catch {
                    // the invoice number is a convenience: the link still works by id
                }
            }
        } catch (err) {
            console.error(err);
            this.error = true;
            ToastService.show('Failed to load credit memo', 'error');
        } finally {
            this.loading = false;
        }
    }

    private openDialog(kind: Pending) {
        this.dialogError = '';
        this.pending = kind;
    }

    private async confirm(reason: string) {
        if (!this.memo || !this.pending) return;
        const kind = this.pending;
        this.busy = true;
        this.dialogError = '';
        try {
            if (kind === 'post') {
                this.memo = await CreditMemoService.post(this.memo.id, this.memo.revision);
                ToastService.show(this.memo.number ? `Credit memo ${this.memo.number} posted` : 'Credit memo posted', 'success');
            } else {
                this.memo = await CreditMemoService.voidCreditMemo(this.memo.id, this.memo.revision, reason);
                ToastService.show('Credit memo voided', 'success');
            }
            this.pending = null;
        } catch (err) {
            if (err instanceof ApiError && err.isStaleRevision) {
                this.pending = null;
                ToastService.show('The credit memo changed while you were looking at it. It has been reloaded.', 'error');
                if (this.routeId) this.load(this.routeId);
            } else {
                this.dialogError = apiErrorMessage(err, 'The credit memo could not be changed');
            }
        } finally {
            this.busy = false;
        }
    }

    render() {
        if (this.loading) return html`<div class="text-white">Loading credit memo...</div>`;
        if (this.error || !this.memo) return html`<div class="text-white">Failed to load credit memo.</div>`;

        const cm = this.memo;
        const isDraft = cm.status === 'draft';
        const isVoid = cm.status === 'void';
        const canVoid = isDraft || cm.status === 'open';
        return html`
            <div class="space-y-6 max-w-6xl mx-auto pb-20">
                <div class="flex items-start justify-between gap-4 flex-wrap pb-6 border-b border-white/10">
                    <div class="min-w-0">
                        <div class="flex items-center gap-3 mb-2 flex-wrap">
                            <h1 class="text-3xl font-bold font-mono ${cm.number ? 'text-white' : 'text-zinc-400'}">${creditMemoLabel(cm)}</h1>
                            <span class=${chipClass(getCreditMemoStatusColor(cm.status))}>${formatCreditMemoStatus(cm.status)}</span>
                        </div>
                        <p class="text-muted-foreground text-sm">
                            Credit memo dated ${formatDay(cm.memo_date)} · ${cm.currency} · ${formatReasonCode(cm.reason_code)}
                        </p>
                    </div>
                    <div class="flex gap-3 flex-wrap">
                        ${isDraft ? html`
                            <button
                                @click=${() => this.openDialog('post')}
                                class="bg-gable-green text-black font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-gable-green/90 transition-colors flex items-center gap-2"
                            >${icon(Check, 18)} Post</button>
                            <a
                                href="/credit-memos/${cm.id}/edit"
                                class="bg-white/10 text-white font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-white/20 transition-colors flex items-center gap-2 border border-white/10"
                            >${icon(Pencil, 18)} Edit</a>
                        ` : nothing}
                        ${canVoid ? html`
                            <button
                                @click=${() => this.openDialog('void')}
                                class="bg-red-500/20 text-red-400 font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-red-500/30 transition-colors flex items-center gap-2"
                            >${icon(Ban, 18)} Void</button>
                        ` : nothing}
                    </div>
                </div>

                ${isDraft ? html`
                    <div class="rounded-lg border border-amber-500/40 bg-amber-500/10 p-4 text-sm text-amber-300" data-testid="draft-banner">
                        <span class="font-semibold">Draft.</span>
                        This credit memo has no number and has moved nothing yet. Post it to assign its number, restock the returned goods and post the credit to the ledger.
                    </div>
                ` : nothing}
                ${isVoid ? html`
                    <div class="rounded-lg border border-red-500/50 bg-red-500/10 p-4 text-red-300" data-testid="void-banner">
                        <div class="text-lg font-bold tracking-wide text-red-400">VOID</div>
                        <div class="text-sm mt-1">
                            This credit memo was voided${cm.voided_at ? html` on ${new Date(cm.voided_at).toLocaleString()}` : nothing}.
                            ${cm.void_reason ? html`<span class="block mt-1">Reason: <span class="text-white">${cm.void_reason}</span></span>` : nothing}
                        </div>
                    </div>
                ` : nothing}

                <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
                    <div class="lg:col-span-2 space-y-6 min-w-0">
                        <div class="bg-slate-steel rounded-lg border border-white/10 overflow-hidden ${isVoid ? 'opacity-70' : ''}">
                            <div class="px-6 py-4 border-b border-white/10">
                                <h2 class="font-semibold text-white">Credited lines</h2>
                            </div>
                            ${renderSalesLines(cm.lines, { ariaLabel: 'Credit memo lines', credit: true, showRestock: true })}
                            <div class="bg-white/5 px-6 py-4 space-y-2 text-sm" data-testid="credit-totals">
                                <div class="flex justify-between"><span class="font-bold text-white uppercase">Subtotal</span><span class="font-bold font-mono ${creditTextClass(cm.subtotal_cents) || 'text-white'}">${formatCents(cm.subtotal_cents)}</span></div>
                                <div class="flex justify-between text-zinc-400">
                                    <span>Tax ${cm.tax_rate_percent !== null ? `(${cm.tax_rate_percent}%)` : ''}</span>
                                    <span class="font-mono ${creditTextClass(cm.tax_cents)}">${formatCents(cm.tax_cents)}</span>
                                </div>
                                <div class="flex justify-between border-t border-white/10 pt-2"><span class="font-bold text-white uppercase">Total credit</span><span class="font-bold font-mono text-lg ${creditTextClass(cm.total_cents) || 'text-white'}" data-testid="credit-total">${formatCents(cm.total_cents)}</span></div>
                                <div class="flex justify-between"><span class="text-zinc-300">Still unapplied</span><span class="font-mono ${creditTextClass(cm.open_cents) || 'text-zinc-500'}">${formatCents(cm.open_cents)}</span></div>
                            </div>
                        </div>
                    </div>

                    <div class="space-y-6">
                        <div class="bg-slate-steel rounded-lg border border-white/10 p-6">
                            <h3 class="font-semibold text-white mb-4">Customer</h3>
                            <div class="space-y-2 text-sm">
                                <p class="text-white font-medium text-base"><a href="/accounts/${cm.customer_id}" class="hover:underline">${cm.customer_name || 'Customer'}</a></p>
                                <p class="text-muted-foreground">Invoice:
                                    ${cm.invoice_id
                                        ? html`<a href="/invoices/${cm.invoice_id}" class="font-mono text-blue-400 hover:underline">${this.invoiceNumber ?? cm.invoice_id.slice(0, 8)}</a>`
                                        : html`<span class="text-zinc-500">none</span>`}
                                </p>
                            </div>
                        </div>
                        <div class="bg-slate-steel rounded-lg border border-white/10 p-6">
                            <h3 class="font-semibold text-white mb-4">Reason</h3>
                            <p class="text-sm text-zinc-300">${cm.reason}</p>
                        </div>
                    </div>
                </div>

                <gable-sales-dialog
                    ?is-open=${this.pending !== null}
                    heading=${this.pending === 'post' ? 'Post this credit memo?' : 'Void this credit memo?'}
                    body=${this.pending === 'post'
                        ? 'Posting assigns the credit memo its number, puts the lines marked restock back on hand, and posts the credit to the ledger. After that it can no longer be edited, only voided.'
                        : 'Voiding cancels this credit memo and reverses what posting did: the stock it returned and its entry in the ledger. It cannot be undone.'}
                    confirm-label=${this.pending === 'post' ? 'Post credit memo' : 'Void credit memo'}
                    ?require-reason=${this.pending === 'void'}
                    ?danger=${this.pending === 'void'}
                    ?busy=${this.busy}
                    .error=${this.dialogError}
                    @close=${() => { this.pending = null; }}
                    @confirm=${(e: CustomEvent<{ reason: string }>) => this.confirm(e.detail.reason)}
                ></gable-sales-dialog>
            </div>
        `;
    }
}
