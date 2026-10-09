// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { router } from '../../lib/router.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { InvoiceService } from '../../services/InvoiceService.ts';
import { CreditMemoService } from '../../services/CreditMemoService.ts';
import { CustomerService } from '../../services/CustomerService.ts';
import { ApiError, apiErrorMessage, fieldErrorMap } from '../../services/apiError.ts';
import type { Invoice, InvoiceLine } from '../../types/invoice.ts';
import {
    type CreditMemo,
    type CreditMemoLineRequest,
    type CreditMemoRequest,
    type CreditReasonCode,
    CREDIT_REASON_CODES,
    formatReasonCode,
} from '../../types/creditMemo.ts';
import { formatCents, formatPrice4 } from '../../lib/utils.ts';
import { dollarsToTenThousandths, extensionCents, tenThousandthsToInput } from '../../lib/money.ts';
import {
    creditQuantityFromInput,
    creditQuantityToInput,
    quantityToScaled,
    remainingCreditable,
} from '../../lib/credit-display.ts';

/** One invoice line the user may return: what they typed and whether the goods go back on hand. */
interface ReturnRow {
    line: InvoiceLine;
    qtyInput: string;
    restock: boolean;
    /** What is still creditable: billed less the credits the page knows of. */
    remaining: string;
}

/** A free line: a price adjustment or a fee, charge code ADJUST. */
interface FreeRow {
    description: string;
    qtyInput: string;
    priceInput: string;
}

/** The invoice lines a credit memo can name: sold goods and charges, never a note or a kit component. */
export function creditableLines(invoice: Invoice): InvoiceLine[] {
    return invoice.lines
        .filter(l => l.line_type === 'product' || l.line_type === 'kit' || l.line_type === 'charge')
        .sort((a, b) => a.position - b.position);
}

/**
 * The request the form's rows make. Returns the problems per row key instead
 * when an input is invalid, so the page shows them beside the inputs.
 */
export function buildCreditMemoRequest(
    base: { invoiceId?: string; customerId?: string; reasonCode: CreditReasonCode; reason: string },
    returns: ReturnRow[],
    frees: FreeRow[],
): { request: CreditMemoRequest; errors: Record<string, string> } {
    const errors: Record<string, string> = {};
    const lines: CreditMemoLineRequest[] = [];
    returns.forEach(r => {
        if (r.qtyInput.trim() === '') return;
        const qty = creditQuantityFromInput(r.qtyInput);
        if (qty === null) {
            errors[`return:${r.line.id}`] = 'Enter a quantity greater than zero';
            return;
        }
        const asked = quantityToScaled(creditQuantityToInput(qty)) ?? 0;
        const left = quantityToScaled(r.remaining) ?? 0;
        if (asked > left) {
            errors[`return:${r.line.id}`] = `Only ${r.remaining} remain to credit`;
            return;
        }
        const req: CreditMemoLineRequest = { invoice_line_id: r.line.id, quantity: qty };
        if (r.line.line_type === 'product' && r.line.product_id !== null) req.restock = r.restock;
        lines.push(req);
    });
    frees.forEach((f, i) => {
        if (f.description.trim() === '' && f.priceInput.trim() === '') return;
        const qty = creditQuantityFromInput(f.qtyInput);
        const price = dollarsToTenThousandths(f.priceInput);
        if (f.description.trim() === '') errors[`free:${i}`] = 'Describe the adjustment';
        else if (qty === null) errors[`free:${i}`] = 'Enter a quantity greater than zero';
        else if (price === null || price <= 0) errors[`free:${i}`] = 'Enter a price greater than zero';
        else lines.push({ line_type: 'charge', charge_code: 'ADJUST', quantity: qty, unit_price_ten_thousandths: price, description: f.description.trim() });
    });
    if (lines.length === 0 && Object.keys(errors).length === 0) errors.lines = 'Add at least one line to credit';
    if (base.reason.trim() === '') errors.reason = 'Give a reason';
    const request: CreditMemoRequest = { reason_code: base.reasonCode, reason: base.reason.trim(), lines };
    if (base.invoiceId) request.invoice_id = base.invoiceId;
    else if (base.customerId) request.customer_id = base.customerId;
    else errors.customer = 'Choose a customer';
    return { request, errors };
}

/** The credit before tax the rows add up to, in cents, negative: each line is priced from its invoice line. */
export function estimateCreditCents(returns: ReturnRow[], frees: FreeRow[]): number {
    let total = 0;
    for (const r of returns) {
        const qty = creditQuantityFromInput(r.qtyInput);
        if (qty === null || r.line.unit_price_ten_thousandths === null) continue;
        total -= extensionCents(creditQuantityToInput(qty), r.line.unit_price_ten_thousandths, r.line.uom_qty ?? '1', r.line.price_uom_qty ?? '1');
    }
    for (const f of frees) {
        const qty = creditQuantityFromInput(f.qtyInput);
        const price = dollarsToTenThousandths(f.priceInput);
        if (qty === null || price === null) continue;
        total -= extensionCents(creditQuantityToInput(qty), price);
    }
    return total;
}

@customElement('gable-credit-memo-form')
export class GableCreditMemoForm extends LitElement {
    createRenderRoot() { return this; }

    /** Set on the edit route (/credit-memos/:id/edit); empty on /credit-memos/new. */
    @property({ attribute: 'route-id' }) routeId = '';

    @state() private invoice: Invoice | null = null;
    @state() private editing: CreditMemo | null = null;
    @state() private customers: { id: string; name: string }[] = [];
    @state() private customerId = '';
    @state() private returns: ReturnRow[] = [];
    @state() private frees: FreeRow[] = [];
    @state() private reasonCode: CreditReasonCode = 'return';
    @state() private reason = '';
    @state() private loading = true;
    @state() private loadError = '';
    @state() private saving = false;
    @state() private errors: Record<string, string> = {};
    @state() private formError = '';

    connectedCallback() {
        super.connectedCallback();
        this.init();
    }

    private async init() {
        try {
            if (this.routeId) {
                const memo = await CreditMemoService.getCreditMemo(this.routeId);
                if (memo.status !== 'draft') {
                    this.loadError = 'Only a draft credit memo can be edited.';
                    return;
                }
                this.editing = memo;
                this.reasonCode = memo.reason_code;
                this.reason = memo.reason;
                this.customerId = memo.customer_id;
                if (memo.invoice_id) await this.loadInvoice(memo.invoice_id, memo);
                else this.frees = memo.lines.map(l => this.freeFromLine(l));
            } else {
                const invoiceId = new URLSearchParams(window.location.search).get('invoice_id');
                if (invoiceId) await this.loadInvoice(invoiceId, null);
                else await this.loadCustomers();
            }
            if (this.frees.length === 0) this.frees = [{ description: '', qtyInput: '1', priceInput: '' }];
        } catch (err) {
            console.error(err);
            this.loadError = apiErrorMessage(err, 'Failed to load the credit memo form');
        } finally {
            this.loading = false;
        }
    }

    private freeFromLine(l: CreditMemo['lines'][number]): FreeRow {
        return {
            description: l.description,
            qtyInput: creditQuantityToInput(l.quantity) || '1',
            priceInput: l.unit_price_ten_thousandths !== null ? tenThousandthsToInput(l.unit_price_ten_thousandths) : '',
        };
    }

    private async loadCustomers() {
        const all = await CustomerService.listAllCustomers();
        this.customers = all.map(c => ({ id: c.id, name: c.name })).sort((a, b) => a.name.localeCompare(b.name));
    }

    /**
     * Loads the invoice and the credits already written against it, so each
     * line shows what is still creditable. The page knows the credit memos of
     * this invoice only; the server holds the final word (409 exceeds_billed).
     */
    private async loadInvoice(invoiceId: string, editing: CreditMemo | null) {
        const invoice = await InvoiceService.getInvoice(invoiceId);
        this.invoice = invoice;
        this.customerId = invoice.customer_id;
        const summaries = await CreditMemoService.allCreditMemos({ invoiceId });
        const credited = new Map<string, string[]>();
        for (const s of summaries) {
            if (s.status === 'void' || s.id === editing?.id) continue;
            const full = await CreditMemoService.getCreditMemo(s.id);
            for (const l of full.lines) {
                if (l.invoice_line_id && l.quantity) {
                    credited.set(l.invoice_line_id, [...(credited.get(l.invoice_line_id) ?? []), l.quantity]);
                }
            }
        }
        this.returns = creditableLines(invoice).map(line => {
            const existing = editing?.lines.find(l => l.invoice_line_id === line.id);
            return {
                line,
                qtyInput: existing ? creditQuantityToInput(existing.quantity) : '',
                restock: existing ? existing.restock : true,
                remaining: remainingCreditable(line.quantity, credited.get(line.id) ?? []) ?? '0',
            };
        });
        if (editing) this.frees = editing.lines.filter(l => !l.invoice_line_id).map(l => this.freeFromLine(l));
    }

    private setReturn(i: number, patch: Partial<ReturnRow>) {
        this.returns = this.returns.map((r, j) => (j === i ? { ...r, ...patch } : r));
        this.errors = {};
    }

    private setFree(i: number, patch: Partial<FreeRow>) {
        this.frees = this.frees.map((f, j) => (j === i ? { ...f, ...patch } : f));
        this.errors = {};
    }

    private async save() {
        this.formError = '';
        const { request, errors } = buildCreditMemoRequest(
            { invoiceId: this.invoice?.id, customerId: this.customerId, reasonCode: this.reasonCode, reason: this.reason },
            this.returns,
            this.frees,
        );
        this.errors = errors;
        if (Object.keys(errors).length > 0) return;
        this.saving = true;
        try {
            const saved = this.editing
                ? await CreditMemoService.updateCreditMemo(this.editing.id, this.editing.revision, request)
                : await CreditMemoService.createCreditMemo(request);
            ToastService.show(this.editing ? 'Draft credit memo saved' : 'Draft credit memo created', 'success');
            router.navigate(`/credit-memos/${saved.id}`);
        } catch (err) {
            if (err instanceof ApiError && err.isStaleRevision && this.editing) {
                ToastService.show('The credit memo changed while you were editing. It has been reloaded.', 'error');
                this.loading = true;
                await this.init();
            } else {
                this.formError = apiErrorMessage(err, 'Failed to save the credit memo');
                this.errors = fieldErrorMap(err);
            }
        } finally {
            this.saving = false;
        }
    }

    private cancel() {
        router.navigate(this.editing ? `/credit-memos/${this.editing.id}` : this.invoice ? `/invoices/${this.invoice.id}` : '/credit-memos');
    }

    render() {
        if (this.loading) return html`<div class="text-white">Loading...</div>`;
        if (this.loadError) {
            return html`
                <div class="max-w-xl mx-auto p-8 text-center">
                    <p class="text-rose-400 text-lg font-semibold mb-2">Cannot open the form</p>
                    <p class="text-gray-400 text-sm mb-4">${this.loadError}</p>
                    <a href="/credit-memos" class="text-blue-400 hover:underline">Back to credit memos</a>
                </div>
            `;
        }

        const estimate = estimateCreditCents(this.returns, this.frees);
        const title = this.editing ? 'Edit draft credit memo' : 'New credit memo';
        return html`
            <div class="space-y-6 max-w-5xl mx-auto pb-20">
                <div class="pb-6 border-b border-white/10">
                    <h1 class="text-3xl font-bold text-white">${title}</h1>
                    <p class="text-muted-foreground mt-1">
                        ${this.invoice
                            ? html`Against invoice <a href="/invoices/${this.invoice.id}" class="font-mono text-blue-400 hover:underline">${this.invoice.number}</a> for ${this.invoice.customer_name}`
                            : 'A credit with no invoice: adjustment lines only. To return goods, start from the invoice.'}
                    </p>
                </div>

                <div class="rounded-lg border border-blue-500/30 bg-blue-500/10 p-4 text-sm text-blue-200" data-testid="draft-explainer">
                    <span class="font-semibold">Saved as a draft first.</span>
                    Nothing moves until you post it. Posting assigns the credit memo number, puts the lines marked restock back on hand, and posts the credit to the ledger.
                    A draft can still be edited or voided.
                </div>

                ${this.formError ? html`<div class="p-3 rounded border border-red-500/40 bg-red-500/10 text-sm text-red-300 whitespace-pre-line" role="alert" data-testid="form-error">${this.formError}</div>` : nothing}

                ${!this.invoice ? html`
                    <div class="bg-slate-steel rounded-lg border border-white/10 p-6">
                        <label class="block text-xs uppercase tracking-wide text-zinc-500 mb-1" for="cm-customer">Customer</label>
                        <select
                            id="cm-customer"
                            .value=${this.customerId}
                            @change=${(e: Event) => { this.customerId = (e.target as HTMLSelectElement).value; this.errors = {}; }}
                            class="bg-black/30 border border-white/10 rounded px-3 py-2 text-white text-sm w-full max-w-md"
                        >
                            <option value="">Choose a customer</option>
                            ${this.customers.map(c => html`<option value=${c.id} ?selected=${this.customerId === c.id}>${c.name}</option>`)}
                        </select>
                        ${this.errors.customer ? html`<p class="mt-2 text-sm text-red-400" role="alert">${this.errors.customer}</p>` : nothing}
                    </div>
                ` : html`
                    <div class="bg-slate-steel rounded-lg border border-white/10 overflow-hidden">
                        <div class="px-6 py-4 border-b border-white/10">
                            <h2 class="font-semibold text-white">Goods and charges to credit</h2>
                            <p class="text-xs text-zinc-500 mt-1">Type the quantity coming back. A credit takes its price from the invoice line and cannot exceed what was billed.</p>
                        </div>
                        <div class="overflow-x-auto">
                            <table class="w-full text-left text-sm" aria-label="Invoice lines to credit">
                                <thead class="bg-white/5">
                                    <tr>
                                        <th class="p-3 text-muted-foreground font-medium">Item</th>
                                        <th class="p-3 text-muted-foreground font-medium text-right">Billed</th>
                                        <th class="p-3 text-muted-foreground font-medium text-right">Can credit</th>
                                        <th class="p-3 text-muted-foreground font-medium text-right">Unit price</th>
                                        <th class="p-3 text-muted-foreground font-medium text-right">Credit qty</th>
                                        <th class="p-3 text-muted-foreground font-medium text-center">Restock</th>
                                    </tr>
                                </thead>
                                <tbody class="divide-y divide-white/5">
                                    ${this.returns.map((r, i) => html`
                                        <tr>
                                            <td class="p-3 text-white">
                                                <div class="font-mono text-sm">${r.line.sku || r.line.charge_code || r.line.line_type}</div>
                                                <div class="text-xs text-muted-foreground">${r.line.description}</div>
                                            </td>
                                            <td class="p-3 font-mono text-right text-zinc-300">${r.line.quantity}${r.line.uom ? html` <span class="text-xs text-zinc-500">${r.line.uom}</span>` : nothing}</td>
                                            <td class="p-3 font-mono text-right ${r.remaining === '0' ? 'text-red-400' : 'text-zinc-300'}">${r.remaining}</td>
                                            <td class="p-3 font-mono text-right text-zinc-300 whitespace-nowrap">${r.line.unit_price_ten_thousandths !== null ? formatPrice4(r.line.unit_price_ten_thousandths) : '—'}</td>
                                            <td class="p-3 text-right">
                                                <input
                                                    type="text"
                                                    inputmode="decimal"
                                                    aria-label="Credit quantity for ${r.line.sku || r.line.description}"
                                                    placeholder="0"
                                                    ?disabled=${r.remaining === '0'}
                                                    .value=${r.qtyInput}
                                                    @input=${(e: Event) => this.setReturn(i, { qtyInput: (e.target as HTMLInputElement).value })}
                                                    class="bg-black/30 border ${this.errors[`return:${r.line.id}`] ? 'border-red-500' : 'border-white/10'} rounded px-3 py-1.5 text-white font-mono text-sm w-28 text-right disabled:opacity-40"
                                                />
                                                ${this.errors[`return:${r.line.id}`] ? html`<p class="mt-1 text-xs text-red-400 text-right" role="alert">${this.errors[`return:${r.line.id}`]}</p>` : nothing}
                                            </td>
                                            <td class="p-3 text-center">
                                                ${r.line.line_type === 'product' && r.line.product_id !== null ? html`
                                                    <input
                                                        type="checkbox"
                                                        aria-label="Restock ${r.line.sku || r.line.description}"
                                                        .checked=${r.restock}
                                                        @change=${(e: Event) => this.setReturn(i, { restock: (e.target as HTMLInputElement).checked })}
                                                        class="accent-[#00FFA3]"
                                                    />
                                                ` : html`<span class="text-zinc-600">-</span>`}
                                            </td>
                                        </tr>
                                    `)}
                                </tbody>
                            </table>
                        </div>
                    </div>
                `}

                <div class="bg-slate-steel rounded-lg border border-white/10 p-6 space-y-3">
                    <div class="flex items-center justify-between">
                        <div>
                            <h2 class="font-semibold text-white">Price adjustment or fee</h2>
                            <p class="text-xs text-zinc-500 mt-1">A free line, booked as charge ADJUST: a price correction, a restocking fee given back, a goodwill credit.</p>
                        </div>
                        <button
                            @click=${() => { this.frees = [...this.frees, { description: '', qtyInput: '1', priceInput: '' }]; }}
                            class="text-sm text-blue-400 hover:underline whitespace-nowrap"
                        >Add line</button>
                    </div>
                    ${this.frees.map((f, i) => html`
                        <div class="grid grid-cols-1 sm:grid-cols-[1fr_6rem_8rem_auto] gap-3 items-start">
                            <input
                                type="text"
                                aria-label="Adjustment description"
                                placeholder="Description"
                                maxlength="200"
                                .value=${f.description}
                                @input=${(e: Event) => this.setFree(i, { description: (e.target as HTMLInputElement).value })}
                                class="bg-black/30 border ${this.errors[`free:${i}`] ? 'border-red-500' : 'border-white/10'} rounded px-3 py-1.5 text-white text-sm"
                            />
                            <input
                                type="text"
                                inputmode="decimal"
                                aria-label="Adjustment quantity"
                                placeholder="Qty"
                                .value=${f.qtyInput}
                                @input=${(e: Event) => this.setFree(i, { qtyInput: (e.target as HTMLInputElement).value })}
                                class="bg-black/30 border border-white/10 rounded px-3 py-1.5 text-white font-mono text-sm text-right"
                            />
                            <input
                                type="text"
                                inputmode="decimal"
                                aria-label="Adjustment unit price"
                                placeholder="Unit price $"
                                .value=${f.priceInput}
                                @input=${(e: Event) => this.setFree(i, { priceInput: (e.target as HTMLInputElement).value })}
                                class="bg-black/30 border border-white/10 rounded px-3 py-1.5 text-white font-mono text-sm text-right"
                            />
                            <button
                                aria-label="Remove adjustment line"
                                @click=${() => { this.frees = this.frees.filter((_, j) => j !== i); }}
                                class="text-zinc-500 hover:text-white px-2 py-1"
                            >Remove</button>
                            ${this.errors[`free:${i}`] ? html`<p class="sm:col-span-4 text-xs text-red-400" role="alert">${this.errors[`free:${i}`]}</p>` : nothing}
                        </div>
                    `)}
                </div>

                <div class="bg-slate-steel rounded-lg border border-white/10 p-6 grid grid-cols-1 md:grid-cols-3 gap-4">
                    <div>
                        <label class="block text-xs uppercase tracking-wide text-zinc-500 mb-1" for="cm-reason-code">Reason code</label>
                        <select
                            id="cm-reason-code"
                            .value=${this.reasonCode}
                            @change=${(e: Event) => { this.reasonCode = (e.target as HTMLSelectElement).value as CreditReasonCode; }}
                            class="bg-black/30 border border-white/10 rounded px-3 py-2 text-white text-sm w-full"
                        >
                            ${CREDIT_REASON_CODES.map(c => html`<option value=${c} ?selected=${this.reasonCode === c}>${formatReasonCode(c)}</option>`)}
                        </select>
                    </div>
                    <div class="md:col-span-2">
                        <label class="block text-xs uppercase tracking-wide text-zinc-500 mb-1" for="cm-reason">Reason</label>
                        <textarea
                            id="cm-reason"
                            rows="2"
                            maxlength="500"
                            placeholder="What happened, in a sentence"
                            .value=${this.reason}
                            @input=${(e: Event) => { this.reason = (e.target as HTMLTextAreaElement).value; this.errors = {}; }}
                            class="bg-black/30 border ${this.errors.reason ? 'border-red-500' : 'border-white/10'} rounded px-3 py-2 text-white text-sm w-full"
                        ></textarea>
                        ${this.errors.reason ? html`<p class="mt-1 text-xs text-red-400" role="alert">${this.errors.reason}</p>` : nothing}
                    </div>
                </div>

                ${this.errors.lines ? html`<p class="text-sm text-red-400" role="alert">${this.errors.lines}</p>` : nothing}

                <div class="flex items-center justify-between gap-4 flex-wrap">
                    <div data-testid="credit-estimate">
                        <div class="text-xs uppercase tracking-wide text-zinc-500">Credit before tax (estimate)</div>
                        <div class="text-2xl font-bold font-mono ${estimate < 0 ? 'text-red-400' : 'text-zinc-400'}">${formatCents(estimate)}</div>
                        <div class="text-xs text-zinc-500">Tax and discounts are worked out by the server when the draft is saved.</div>
                    </div>
                    <div class="flex gap-3">
                        <button @click=${() => this.cancel()} ?disabled=${this.saving} class="bg-white/10 text-white font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-white/20 transition-colors">Cancel</button>
                        <button
                            @click=${() => this.save()}
                            ?disabled=${this.saving}
                            class="bg-gable-green text-black font-bold px-4 py-2 rounded whitespace-nowrap hover:bg-gable-green/90 transition-colors disabled:opacity-50"
                        >${this.saving ? 'Saving...' : this.editing ? 'Save draft' : 'Create draft'}</button>
                    </div>
                </div>
            </div>
        `;
    }
}
