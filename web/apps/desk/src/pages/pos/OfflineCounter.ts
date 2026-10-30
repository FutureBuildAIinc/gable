// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import '../../components/ui/gable-select';
import { formatCents } from '../../lib/utils';
import { ID_TYPES } from '../../types/pos';
import { dollarsToCents } from '../../services/POSService';
import { offlineCounter, OfflineCounterError } from '../../services/OfflineCounterService';
import type {
    OfflineAccount,
    OfflineCartLine,
    OfflineCartPreview,
    OfflineProduct,
    OfflineSaleRequest,
    OfflineSignedSale,
    OfflineSnapshot,
    OfflineTender,
} from '../../types/offline';
import {
    DIFFERENT_CUSTOMER,
    NEEDS_CONNECTION,
    OFFLINE_PRICES,
    ONLINE_ONLY_ACTS,
    RE_ENROL,
    lineNote,
    receiptLines,
    soldAt,
    tenderAvailability,
    tenderWords,
    type CarriedCart,
    type OfflineMethod,
} from './offline';

const METHODS: Array<{ key: OfflineMethod; label: string }> = [
    { key: 'cash', label: 'Cash' },
    { key: 'check', label: 'Check' },
    { key: 'account', label: 'Account' },
    { key: 'card', label: 'Card' },
    { key: 'store_credit', label: 'Store credit' },
];

const newId = () => (globalThis.crypto?.randomUUID ? globalThis.crypto.randomUUID() : `${Date.now()}-${Math.random()}`);

/**
 * The counter's offline mode (ADR 0011 slice C2): the page holds the cart's
 * lines, units, quantities, customer and tenders, and the work shell's core
 * answers every figure (5.1, 5.3): it prices the cart as the clerk builds it,
 * and at the tender it derives the totals, the tax, the caps and the change,
 * assigns the sequence and the chain link, stores and signs the sale. The
 * page shows what the core answers, so the figures on the screen are the
 * figures the sale is signed with.
 */
@customElement('gable-pos-offline')
export class OfflineCounter extends LitElement {
    createRenderRoot() { return this; }

    /** The open server cart carried in when the connection dropped (ADR 0011 5.3). */
    @property({ attribute: false }) seed: CarriedCart | null = null;

    /** The result unknown cart every new offline sale names by default (ADR 0011 5.3). */
    @property({ attribute: false }) unknownCartId: string | null = null;

    @state() private _snapshot: OfflineSnapshot | null = null;
    @state() private _refusal: string | null = null;
    @state() private _lines: OfflineCartLine[] = [];
    @state() private _replaces: string | null = null;
    @state() private _carriedWords: string[] = [];
    @state() private _query = '';
    @state() private _accountQuery = '';
    @state() private _account: OfflineAccount | null = null;
    @state() private _po = '';
    @state() private _method: OfflineMethod = 'cash';
    @state() private _amount = '';
    @state() private _checkRef = '';
    @state() private _idChecked = false;
    @state() private _idType = '';
    @state() private _differentCustomer = false;
    @state() private _preview: OfflineCartPreview | null = null;
    @state() private _lineError: string | null = null;
    @state() private _error: string | null = null;
    @state() private _busy = false;
    @state() private _sold: OfflineSignedSale | null = null;

    private _priceSeq = 0;
    private _seeded = false;

    async connectedCallback() {
        super.connectedCallback();
        try {
            this._snapshot = await offlineCounter.snapshot();
        } catch (err) {
            const code = err instanceof OfflineCounterError ? err.code : '';
            this._refusal = code === 're_enrol_required' || code === 'store_rolled_back'
                ? RE_ENROL
                : 'This register holds no offline prices for this till session, so it cannot sell until the connection returns.';
            return;
        }
        this._takeSeed();
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('seed')) this._takeSeed();
    }

    private _takeSeed() {
        if (this._seeded || !this.seed || !this._snapshot) return;
        this._seeded = true;
        this._lines = this.seed.lines;
        this._replaces = this.seed.replacesSaleId;
        this._carriedWords = this.seed.refused;
        void this._reprice();
    }

    /** The request the core prices and signs: the cart, never a figure. */
    private _request(withTenders: boolean): OfflineSaleRequest | null {
        const snap = this._snapshot;
        if (!snap || this._lines.length === 0) return null;
        const declared = this.unknownCartId !== null && this._differentCustomer;
        return {
            client_id: newId(),
            snapshot_id: snap.snapshot_id,
            till_session_id: snap.till_session.id,
            cashier_id: snap.till_session.cashier_id,
            customer_id: this._account?.customer_id ?? null,
            buyer_contact_id: null,
            customer_po: this._po.trim() || null,
            job_id: null,
            picked_up_by: null,
            business_date: snap.till_session.business_date,
            sold_at: soldAt(new Date()),
            lines: this._lines.map((l) => ({ id: l.id, product_id: l.product_id, quantity: l.quantity, uom: l.uom, ...(l.tally ? { tally: l.tally } : {}) })),
            tenders: withTenders ? this._tenders() : [],
            replaces_sale_id: declared ? null : (this.unknownCartId ?? this._replaces),
            different_customer: declared,
            unknown_cart_id: declared ? this.unknownCartId : null,
        };
    }

    /** The tender the clerk entered, in cents as typed; the core judges it. */
    private _tenders(): OfflineTender[] {
        const typed = this._amount.trim();
        const amount = typed === '' ? (this._preview?.total_cents ?? 0) : dollarsToCents(typed);
        if (!Number.isFinite(amount) || amount <= 0) return [];
        if (this._method !== 'cash' && this._method !== 'check' && this._method !== 'account') return [];
        return [{
            method: this._method,
            amount_cents: amount,
            reference: this._method === 'check' ? (this._checkRef.trim() || null) : null,
            id_checked: this._idChecked,
            id_type: this._idChecked && this._idType ? this._idType : null,
        }];
    }

    private async _reprice() {
        const req = this._request(true);
        const seq = ++this._priceSeq;
        if (!req) {
            this._preview = null;
            return;
        }
        try {
            const preview = await offlineCounter.price(req);
            if (seq !== this._priceSeq) return;
            this._preview = preview;
            this._lineError = null;
        } catch (err) {
            if (seq !== this._priceSeq) return;
            this._lineError = err instanceof Error ? err.message : 'The register could not price this cart.';
            if (err instanceof OfflineCounterError && err.code === 'kit_not_offline') {
                // the core refused the kit from its own flag: the line leaves the cart
                const snap = this._snapshot;
                this._lines = this._lines.filter((l) => !snap?.products.find((p) => p.id === l.product_id)?.is_kit);
                void this._reprice();
            }
        }
    }

    private _add(product: OfflineProduct) {
        if (product.is_kit) {
            this._lineError = `${product.description}: kits do not sell offline. Remove the kit or wait until the counter is online.`;
            return;
        }
        this._sold = null;
        this._lines = [...this._lines, { id: newId(), product_id: product.id, quantity: '1', uom: product.sale_uom }];
        this._query = '';
        void this._reprice();
    }

    private _setQuantity(id: string, quantity: string) {
        this._lines = this._lines.map((l) => (l.id === id ? { ...l, quantity } : l));
        void this._reprice();
    }

    private _remove(id: string) {
        this._lines = this._lines.filter((l) => l.id !== id);
        void this._reprice();
    }

    private _chooseAccount(account: OfflineAccount | null) {
        this._account = account;
        this._accountQuery = '';
        if (!account && this._method === 'account') this._method = 'cash';
        void this._reprice();
    }

    private async _complete() {
        const req = this._request(true);
        if (!req || req.tenders.length === 0) {
            this._error = 'Enter the tender first.';
            return;
        }
        this._busy = true;
        this._error = null;
        try {
            const signed = await offlineCounter.sign(req);
            this._sold = signed;
            this._replaces = null;
            this.dispatchEvent(new CustomEvent('offline-sold', { detail: signed, bubbles: true, composed: true }));
        } catch (err) {
            this._error = err instanceof Error ? err.message : 'The register could not keep this sale.';
        } finally {
            this._busy = false;
        }
    }

    private _newSale() {
        this._sold = null;
        this._lines = [];
        this._account = null;
        this._po = '';
        this._amount = '';
        this._checkRef = '';
        this._idChecked = false;
        this._idType = '';
        this._differentCustomer = false;
        this._preview = null;
        this._carriedWords = [];
    }

    private _product(id: string): OfflineProduct | undefined {
        return this._snapshot?.products.find((p) => p.id === id);
    }

    render() {
        if (this._refusal) {
            return html`<div class="flex flex-1 items-center justify-center p-8" data-testid="pos-offline-refused">
                <p class="max-w-md text-center text-base text-slate-200" role="status">${this._refusal}</p></div>`;
        }
        if (!this._snapshot) {
            return html`<p class="p-6 text-sm text-slate-400" data-testid="pos-offline-loading">Opening the register's offline prices.</p>`;
        }
        return html`
            <div class="flex min-h-0 w-full flex-1 flex-col items-start gap-4 p-4 min-[768px]:flex-row" data-testid="pos-offline">
                ${this._renderCart()}
                <div class="flex w-full min-w-0 flex-col gap-3 min-[768px]:flex-1" data-testid="pos-offline-panels">
                    ${this._sold ? this._renderReceipt(this._sold) : html`${this._renderCustomer()}${this._renderTender()}`}
                    ${this._renderOnlineOnly()}
                </div>
            </div>`;
    }

    private _renderCart() {
        const snap = this._snapshot as OfflineSnapshot;
        const q = this._query.trim().toLowerCase();
        const matches = q.length < 2 ? [] : snap.products.filter((p) =>
            p.sku.toLowerCase().includes(q) || p.description.toLowerCase().includes(q) || (p.barcode ?? '') === q).slice(0, 8);
        const priced = this._preview?.lines ?? [];
        return html`
            <section class="w-full min-w-0 rounded-xl border border-white/10 bg-slate-steel p-4 min-[768px]:w-[52%] min-[768px]:shrink-0" data-testid="pos-offline-cart">
                <p class="mb-3 rounded-lg border border-blueprint-blue/30 bg-blueprint-blue/10 px-3 py-2 text-sm text-blueprint-blue" data-testid="pos-offline-prices-note">${OFFLINE_PRICES}</p>
                <label class="mb-1 block text-xs text-slate-400" for="pos-offline-search">Add a product</label>
                <input id="pos-offline-search" data-testid="pos-offline-search" .value=${this._query}
                    @input=${(e: Event) => { this._query = (e.target as HTMLInputElement).value; }}
                    placeholder="SKU, description or barcode"
                    class="min-h-11 w-full rounded-lg border border-white/10 bg-deep-space px-3 text-sm text-white" />
                ${matches.length > 0 ? html`<ul class="mt-2 divide-y divide-white/5 rounded-lg border border-white/10" data-testid="pos-offline-results">
                    ${matches.map((p) => html`<li><button type="button" @click=${() => this._add(p)} data-testid="pos-offline-result"
                        class="flex min-h-11 w-full items-center justify-between gap-3 px-3 text-left text-sm">
                        <span class="min-w-0 truncate">${p.description}</span>
                        <span class="shrink-0 font-mono text-xs text-slate-400">${p.sku}${p.is_kit ? ' · kit' : ''}</span></button></li>`)}
                </ul>` : nothing}
                ${this._lineError ? html`<p class="mt-2 text-sm text-safety-red" role="alert" data-testid="pos-offline-line-error">${this._lineError}</p>` : nothing}
                ${this._carriedWords.map((w) => html`<p class="mt-2 text-sm text-amber-400" data-testid="pos-offline-carried-refusal">${w}</p>`)}
                <ul class="mt-3 flex flex-col gap-2" data-testid="pos-offline-lines">
                    ${this._lines.map((l) => {
                        const p = this._product(l.product_id);
                        const figure = priced.find((x) => x.id === l.id);
                        const note = figure ? lineNote(figure) : null;
                        return html`<li class="rounded-lg border border-white/10 px-3 py-2" data-testid="pos-offline-line">
                            <div class="flex items-center gap-3">
                                <span class="min-w-0 flex-1 truncate text-sm">${p?.description ?? l.product_id}</span>
                                <input type="text" inputmode="decimal" aria-label="Quantity" .value=${l.quantity} data-testid="pos-offline-qty"
                                    @change=${(e: Event) => this._setQuantity(l.id, (e.target as HTMLInputElement).value.trim())}
                                    class="h-11 w-20 rounded border border-white/10 bg-deep-space px-2 text-right font-mono text-sm" />
                                <span class="w-10 font-mono text-xs text-slate-400">${l.uom}</span>
                                <span class="w-24 text-right font-mono text-sm" data-testid="pos-offline-line-total">${figure ? formatCents(figure.line_total_cents) : ''}</span>
                                <button type="button" aria-label="Remove line" @click=${() => this._remove(l.id)} class="h-11 w-11 rounded text-slate-400">×</button>
                            </div>
                            ${note ? html`<p class="mt-1 text-xs text-amber-400" data-testid="pos-offline-level-price">${note}</p>` : nothing}
                        </li>`;
                    })}
                </ul>
                ${this._preview ? html`
                    <dl class="mt-4 grid grid-cols-2 gap-y-1 text-sm" data-testid="pos-offline-totals">
                        <dt class="text-slate-400">Subtotal</dt><dd class="text-right font-mono">${formatCents(this._preview.subtotal_cents)}</dd>
                        <dt class="text-slate-400">${this._preview.tax_exempt ? 'Tax exempt' : 'Tax'}</dt><dd class="text-right font-mono">${formatCents(this._preview.tax_cents)}</dd>
                        <dt class="font-semibold">Total</dt><dd class="text-right font-mono text-lg font-bold" data-testid="pos-offline-total">${formatCents(this._preview.total_cents)}</dd>
                    </dl>` : nothing}
            </section>`;
    }

    private _renderCustomer() {
        const snap = this._snapshot as OfflineSnapshot;
        const q = this._accountQuery.trim().toLowerCase();
        const matches = q.length < 2 ? [] : snap.accounts.filter((a) =>
            a.name.toLowerCase().includes(q) || a.account_number.toLowerCase().includes(q)).slice(0, 6);
        return html`
            <section class="rounded-xl border border-white/10 bg-slate-steel p-4" data-testid="pos-offline-customer">
                <h3 class="mb-2 text-base font-semibold">Customer</h3>
                <div class="flex items-center justify-between gap-3">
                    <span class="text-sm" data-testid="pos-offline-customer-name">${this._account?.name ?? 'Walk-in sale'}</span>
                    ${this._account ? html`<button type="button" class="min-h-11 text-sm text-slate-300" @click=${() => this._chooseAccount(null)}>Walk-in</button>` : nothing}
                </div>
                <input data-testid="pos-offline-account-search" .value=${this._accountQuery} aria-label="Find a cached account"
                    @input=${(e: Event) => { this._accountQuery = (e.target as HTMLInputElement).value; }}
                    placeholder="Cached accounts only: name or account number"
                    class="mt-2 min-h-11 w-full rounded-lg border border-white/10 bg-deep-space px-3 text-sm text-white" />
                ${matches.map((a) => html`<button type="button" data-testid="pos-offline-account" @click=${() => this._chooseAccount(a)}
                    class="mt-1 flex min-h-11 w-full items-center justify-between rounded px-2 text-left text-sm hover:bg-white/5">
                    <span>${a.name}</span><span class="font-mono text-xs text-slate-400">${a.account_number}</span></button>`)}
                ${this._account?.po_required ? html`<input data-testid="pos-offline-po" .value=${this._po} aria-label="Purchase order"
                    @input=${(e: Event) => { this._po = (e.target as HTMLInputElement).value; }} @change=${() => this._reprice()}
                    placeholder="Purchase order (this account requires one)"
                    class="mt-2 min-h-11 w-full rounded-lg border border-white/10 bg-deep-space px-3 text-sm text-white" />` : nothing}
                ${this.unknownCartId ? html`
                    <label class="mt-3 flex min-h-11 items-start gap-2 text-sm" data-testid="pos-offline-declare">
                        <input type="checkbox" class="mt-1 h-5 w-5" .checked=${this._differentCustomer}
                            @change=${(e: Event) => { this._differentCustomer = (e.target as HTMLInputElement).checked; void this._reprice(); }} />
                        <span><span class="font-semibold">${DIFFERENT_CUSTOMER}</span>: this sale is not the one whose result is unknown.
                        Without it, this sale is linked to that sale, so the same goods are never charged twice.</span>
                    </label>` : nothing}
            </section>`;
    }

    private _renderTender() {
        const snap = this._snapshot as OfflineSnapshot;
        const room = this._preview?.account_room_cents ?? null;
        const words = tenderWords(this._preview);
        const ready = this._preview !== null && !this._busy && this._lines.length > 0 && this._preview.tender_refusal === null;
        return html`
            <section class="rounded-xl border border-white/10 bg-slate-steel p-4" data-testid="pos-offline-tender">
                <h3 class="mb-2 text-base font-semibold">Payment</h3>
                <div class="grid grid-cols-2 gap-2 min-[480px]:grid-cols-3" role="radiogroup" aria-label="Tender">
                    ${METHODS.map((m) => {
                        const a = tenderAvailability(snap, m.key, this._account ? room : null);
                        const chosen = this._method === m.key;
                        return html`<button type="button" role="radio" aria-checked=${chosen ? 'true' : 'false'} ?disabled=${!a.enabled}
                            data-testid="pos-offline-method-${m.key}" title=${a.words}
                            @click=${() => { this._method = m.key; void this._reprice(); }}
                            class="flex min-h-14 flex-col items-center justify-center rounded-lg border px-2 text-sm ${chosen ? 'border-gable-green text-white' : 'border-white/10 text-slate-300'} disabled:cursor-not-allowed disabled:opacity-40">
                            <span class="font-semibold">${m.label}</span>
                            ${a.words ? html`<span class="text-xs">${a.words}</span>` : nothing}</button>`;
                    })}
                </div>
                ${this._method === 'account' && room !== null ? html`<p class="mt-2 text-xs text-slate-400" data-testid="pos-offline-room">Room left on this account offline: <span class="font-mono">${formatCents(room)}</span></p>` : nothing}
                <label class="mt-3 block text-xs text-slate-400" for="pos-offline-amount">Amount (blank for the total)</label>
                <input id="pos-offline-amount" data-testid="pos-offline-amount" inputmode="decimal" .value=${this._amount}
                    @input=${(e: Event) => { this._amount = (e.target as HTMLInputElement).value; }} @change=${() => this._reprice()}
                    class="min-h-11 w-full rounded-lg border border-white/10 bg-deep-space px-3 font-mono text-sm text-white" />
                ${this._method === 'check' ? html`
                    <input data-testid="pos-offline-check-ref" .value=${this._checkRef} aria-label="Check number" placeholder="Check number"
                        @input=${(e: Event) => { this._checkRef = (e.target as HTMLInputElement).value; }}
                        class="mt-2 min-h-11 w-full rounded-lg border border-white/10 bg-deep-space px-3 text-sm text-white" />` : nothing}
                ${this._method === 'check' || this._method === 'account' ? html`
                    <label class="mt-2 flex min-h-11 items-center gap-2 text-sm">
                        <input type="checkbox" class="h-5 w-5" data-testid="pos-offline-id-checked" .checked=${this._idChecked}
                            @change=${(e: Event) => { this._idChecked = (e.target as HTMLInputElement).checked; void this._reprice(); }} />ID checked</label>
                    ${this._idChecked ? html`<gable-select data-testid="pos-offline-id-type" aria-label="ID type" .value=${this._idType}
                        .options=${[{ value: '', label: 'Choose the ID' }, ...ID_TYPES.map((t) => ({ value: t.value, label: t.label }))]}
                        @change=${(e: Event) => { this._idType = (e.target as HTMLSelectElement).value; void this._reprice(); }}></gable-select>` : nothing}` : nothing}
                ${words ? html`<p class="mt-2 text-sm ${this._preview?.tender_refusal ? 'text-safety-red' : 'text-gable-green'}" data-testid="pos-offline-tender-words">${words}</p>` : nothing}
                ${this._error ? html`<p class="mt-2 text-sm text-safety-red" role="alert" data-testid="pos-offline-error">${this._error}</p>` : nothing}
                <button type="button" data-testid="pos-offline-complete" ?disabled=${!ready} @click=${() => this._complete()}
                    class="mt-4 min-h-14 w-full rounded-lg text-base font-bold ${ready ? 'bg-gable-green text-deep-space' : 'cursor-not-allowed border border-white/10 bg-white/5 text-slate-400 opacity-40'}">
                    ${this._busy ? 'Keeping the sale...' : `Complete offline sale${this._preview ? ` ${formatCents(this._preview.total_cents)}` : ''}`}
                </button>
            </section>`;
    }

    private _renderReceipt(sale: OfflineSignedSale) {
        const lines = receiptLines(sale, this._account?.name ?? null);
        return html`
            <section class="rounded-xl border border-gable-green bg-slate-steel p-4" data-testid="pos-offline-receipt">
                <h3 class="mb-2 text-base font-semibold text-gable-green">Offline sale kept on this register</h3>
                <pre class="whitespace-pre-wrap font-mono text-xs leading-5 text-slate-200" data-testid="pos-offline-receipt-text">${lines.join('\n')}</pre>
                <div class="mt-3 flex gap-2">
                    <button type="button" data-testid="pos-offline-print" @click=${() => window.print()}
                        class="min-h-11 flex-1 rounded-lg border border-white/20 text-sm font-semibold">Print receipt</button>
                    <button type="button" data-testid="pos-offline-new-sale" @click=${() => this._newSale()}
                        class="min-h-11 flex-1 rounded-lg bg-gable-green text-sm font-bold text-deep-space">New sale</button>
                </div>
            </section>`;
    }

    private _renderOnlineOnly() {
        return html`
            <p class="rounded-xl border border-white/10 px-4 py-3 text-xs text-slate-400" data-testid="pos-offline-online-only">
                ${ONLINE_ONLY_ACTS.join(', ')}: ${NEEDS_CONNECTION}.
            </p>`;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-pos-offline': OfflineCounter;
    }
}
