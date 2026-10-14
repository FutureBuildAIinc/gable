// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../lib/icons.ts';
import { router } from '../lib/router.ts';
import { ToastService } from '../lib/toast-service.ts';
import { QuoteService, QuoteApiError, quoteErrorMessage } from '../services/QuoteService.ts';
import { ProductService } from '../services/product.service.ts';
import { CustomerService } from '../services/CustomerService.ts';
import { LocationService } from '../services/LocationService.ts';
import { deliveryService } from '../services/deliveryService.ts';
import type { Customer } from '../types/customer.ts';
import type { Product } from '../types/product.ts';
import { QUOTE_UOM_CODES } from '../types/quote.ts';
import type { QuoteRequest, QuoteLineRequest, QuoteDeliveryType } from '../types/quote.ts';
import { formatCents, formatPrice4 } from '../lib/utils.ts';
import {
    dollarsToCents,
    dollarsToTenThousandths,
    tenThousandthsToInput,
    extensionCents,
    floatDollarsToTenThousandths,
    normalizeQuantity,
    numberToQuantity,
    tenThousandthsToDecimal,
    centsToInput,
} from '../lib/money.ts';
import type { QuoteLineEscalator } from '../types/pricing.ts';
import type { ParseResponse, ParsedItem } from '../types/parsing.ts';
import type { Vehicle } from '../types/delivery.ts';
import { Save, Send, Printer, Calculator, AlertCircle, TrendingUp, Truck, Package, Minus, Plus, Trash2, Loader2 } from 'lucide';

// Side-effect imports: register child custom elements
import './quotes/QuoteList.ts';
import '../components/customers/CustomerSelect.ts';
import '../components/quotes/MaterialIntake.ts';
import '../components/quotes/LineItemEditor.ts';
import '../components/quotes/EscalatorToggle.ts';

/**
 * One editor line. Quantity is a decimal string and the unit price an integer in
 * ten thousandths of a dollar, exactly as the quote wire carries them; nothing
 * here is a float. The optional fields are kept from a loaded quote so a full
 * replace (PUT) does not drop a portal customer's note or a price unit pair.
 */
interface LineWithEscalator {
    id?: string;
    product_id: string | null;
    sku: string;
    description: string;
    customer_note?: string | null;
    quantity: string;
    uom: string;
    price_uom?: string;
    uom_qty?: string;
    price_uom_qty?: string;
    unit_price_ten_thousandths: number;
    escalator: QuoteLineEscalator;
}

/** The local calendar day of a stored instant, so a Valid until set to a day shows that day west of UTC. */
function localDay(iso: string): string {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso.slice(0, 10);
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

const defaultEscalator = (): QuoteLineEscalator => ({
    enabled: false,
    escalation_type: 'PERCENTAGE',
    escalation_rate: 5,
    effective_date: new Date().toISOString().split('T')[0],
    target_date: new Date(Date.now() + 90 * 24 * 60 * 60 * 1000).toISOString().split('T')[0],
});

@customElement('gable-quote-builder')
export class GableQuoteBuilder extends LitElement {
    createRenderRoot() { return this; }

    @property({ attribute: 'route-id' }) routeId = '';

    @state() private customer: Customer | null = null;
    @state() private products: Product[] = [];
    @state() private lines: LineWithEscalator[] = [];
    @state() private loading = false;
    @state() private initialLoading = false;

    // Delivery state
    @state() private deliveryType: QuoteDeliveryType = 'pickup';
    @state() private freightCents = 0;
    @state() private freightText = '';
    @state() private selectedVehicleId: string | undefined;
    @state() private vehicles: Vehicle[] = [];

    // AI Parsing state
    @state() private aiSource = false;
    @state() private lastParseResult: ParseResponse | null = null;

    // Header and terms
    @state() private quoteNumber: string | null = null;
    @state() private quoteStatus = 'draft';
    @state() private validUntil = '';
    @state() private yardName: string | null = null;

    // Save state: the loaded quote's revision (If-Match) and server side problems.
    @state() private revision: number | null = null;
    @state() private formError: string | null = null;
    @state() private lineErrors: Record<number, string> = {};
    private jobId: string | null = null;
    private expiresAt: string | null = null;

    private get isEditing() { return !!this.routeId; }

    connectedCallback() {
        super.connectedCallback();
        if (this.routeId) this.initialLoading = true;
        this.loadProducts();
        this.loadVehicles();
        if (this.routeId) this.loadExistingQuote(this.routeId);
    }

    private async loadProducts() {
        try {
            // The picker filters on the client, so it needs the catalog: every page through the cursor,
            // capped at LIST_ALL_PRODUCTS_CAP products (2000).
            this.products = await ProductService.listAllProducts();
        } catch (err) {
            console.error('Failed to load products', err);
        }
    }

    private async loadVehicles() {
        try {
            const data = await deliveryService.listVehicles();
            this.vehicles = data || [];
        } catch (err) {
            console.error('Failed to load vehicles', err);
        }
    }

    private async loadExistingQuote(editId: string) {
        try {
            const quote = await QuoteService.get(editId);
            if (quote.status !== 'draft') {
                ToastService.show('Only draft quotes can be edited', 'error');
                router.navigate(`/quotes/${editId}`);
                return;
            }
            try {
                const c = await CustomerService.getCustomer(quote.customer_id);
                this.customer = c;
            } catch { /* customer might not load, that's ok */ }

            this.revision = quote.revision;
            this.quoteNumber = quote.number;
            this.quoteStatus = quote.status;
            this.validUntil = quote.expires_at ? localDay(quote.expires_at) : '';
            this.jobId = quote.job_id;
            this.expiresAt = quote.expires_at;
            this.formError = null;
            this.lineErrors = {};
            this.lines = (quote.lines ?? []).map(l => ({
                id: l.id,
                product_id: l.product_id,
                sku: l.sku,
                description: l.description,
                customer_note: l.customer_note,
                quantity: l.quantity,
                uom: l.uom,
                price_uom: l.price_uom,
                uom_qty: l.uom_qty,
                price_uom_qty: l.price_uom_qty,
                unit_price_ten_thousandths: l.unit_price_ten_thousandths,
                escalator: defaultEscalator(),
            }));
            this.aiSource = quote.source === 'ai';
            this.deliveryType = quote.delivery_type;
            this.freightCents = quote.freight_cents;
            this.freightText = quote.freight_cents > 0 ? centsToInput(quote.freight_cents) : '';
            this.selectedVehicleId = quote.vehicle_id ?? undefined;
            LocationService.getBranch(quote.branch_id)
                .then(b => { this.yardName = b.name; })
                .catch(() => { this.yardName = null; });
        } catch (err) {
            console.error('Failed to load quote for editing', err);
            ToastService.show(`Failed to load quote: ${quoteErrorMessage(err, 'Unknown error')}`, 'error');
            router.navigate('/quotes');
        } finally {
            this.initialLoading = false;
        }
    }

    private handleAddLine(product: Product, quantity: string, uom: string, unitPriceTenThousandths: number) {
        this.lines = [...this.lines, {
            product_id: product.id,
            sku: product.sku,
            description: product.description,
            uom: uom || product.stock_uom,
            quantity,
            unit_price_ten_thousandths: unitPriceTenThousandths,
            escalator: defaultEscalator(),
        }];
    }

    private handleUomInput(idx: number, value: string) {
        const updated = [...this.lines];
        updated[idx] = { ...updated[idx], uom: value.trim().toUpperCase() };
        this.lines = updated;
    }

    private handleEscalatorChange(idx: number, escalator: QuoteLineEscalator) {
        const updated = [...this.lines];
        updated[idx] = { ...updated[idx], escalator };
        this.lines = updated;
    }

    private handleParseComplete(result: ParseResponse) {
        this.lastParseResult = result;
    }

    private handleQuantity(idx: number, value: string) {
        const q = normalizeQuantity(value);
        const updated = [...this.lines];
        updated[idx] = { ...updated[idx], quantity: q ?? value };
        this.lines = updated;
    }

    private stepQuantity(idx: number, delta: number) {
        const current = Number(this.lines[idx].quantity);
        const next = numberToQuantity(Math.max(1, (Number.isFinite(current) ? current : 0) + delta));
        if (next !== null) this.handleQuantity(idx, next);
    }

    private handlePrice(idx: number, value: string) {
        const tt = dollarsToTenThousandths(value);
        if (tt === null) return;
        const updated = [...this.lines];
        updated[idx] = { ...updated[idx], unit_price_ten_thousandths: tt };
        this.lines = updated;
    }

    private removeLine(idx: number) {
        this.lines = this.lines.filter((_, i) => i !== idx);
        const errs: Record<number, string> = {};
        for (const [k, v] of Object.entries(this.lineErrors)) {
            const i = Number(k);
            if (i < idx) errs[i] = v; else if (i > idx) errs[i - 1] = v;
        }
        this.lineErrors = errs;
    }

    private handleAcceptParsed(parsedItems: ParsedItem[]) {
        // A sample list is not the user's file: it never joins a quote, by any path.
        if (this.lastParseResult?.synthetic) return;
        // The parsing API still answers in float dollars and a float quantity;
        // both are converted here, once, through the integer helpers.
        const newLines: LineWithEscalator[] = parsedItems.map(item => ({
            product_id: item.matched_product?.product_id || null,
            sku: item.matched_product?.sku || 'SPECIAL-ORDER',
            description: item.matched_product?.description || item.raw_text,
            quantity: numberToQuantity(item.quantity) ?? '1',
            uom: (item.matched_product?.uom || item.uom || '').toUpperCase(),
            unit_price_ten_thousandths: floatDollarsToTenThousandths(item.matched_product?.base_price || 0),
            escalator: defaultEscalator(),
        }));
        this.lines = [...this.lines, ...newLines];
        this.aiSource = true;
        ToastService.show(`${parsedItems.length} items added from material list`, 'success');
    }

    /** Index of every line that has no unit of measure; the server rejects such a line. */
    private get linesMissingUom(): number[] {
        return this.lines.flatMap((l, i) => (l.uom.trim() === '' ? [i] : []));
    }

    private get freightInvalid() {
        return this.deliveryType === 'delivery' && this.freightText.trim() !== '' && dollarsToCents(this.freightText) === null;
    }

    private get saveBlockedReason(): string | null {
        if (this.linesMissingUom.length > 0) return 'Every line needs a unit of measure before the quote can be saved.';
        if (this.lines.some(l => normalizeQuantity(l.quantity) === null)) return 'Every line needs a quantity above zero.';
        if (this.freightInvalid) return 'Freight must be a dollar amount such as 125.00.';
        return null;
    }

    private buildLine(l: LineWithEscalator): QuoteLineRequest {
        const line: QuoteLineRequest = {
            quantity: l.quantity,
            uom: l.uom as QuoteLineRequest['uom'],
            unit_price_ten_thousandths: l.unit_price_ten_thousandths,
            sku: l.sku,
            description: l.description,
        };
        if (l.id) line.id = l.id;
        if (l.product_id) line.product_id = l.product_id;
        if (l.customer_note) line.customer_note = l.customer_note;
        if (l.price_uom && l.price_uom !== l.uom) {
            line.price_uom = l.price_uom;
            line.uom_qty = l.uom_qty;
            line.price_uom_qty = l.price_uom_qty;
        }
        return line;
    }

    /** Maps server problems such as lines[0].uom onto the line they name. */
    private showSaveError(err: unknown) {
        const lineErrors: Record<number, string> = {};
        const general: string[] = [];
        if (err instanceof QuoteApiError && err.code === 'validation_failed') {
            for (const d of err.details) {
                const m = d.field ? /^lines\[(\d+)\]\.?(.*)$/.exec(d.field) : null;
                if (m) {
                    const idx = Number(m[1]);
                    const text = m[2] ? `${m[2]}: ${d.message}` : d.message;
                    lineErrors[idx] = lineErrors[idx] ? `${lineErrors[idx]}; ${text}` : text;
                } else {
                    general.push(d.field ? `${d.field}: ${d.message}` : d.message);
                }
            }
            this.lineErrors = lineErrors;
            this.formError = Object.keys(lineErrors).length > 0 && general.length === 0
                ? 'Some lines need attention.'
                : [err.message, ...general].join(' ');
            ToastService.show(this.formError, 'error');
            return;
        }
        this.formError = quoteErrorMessage(err, 'Failed to save quote');
        ToastService.show(this.formError, 'error');
    }

    /** Saves the draft; with `send` it then marks the quote sent at the revision the save returned. */
    private async handleSave(send = false) {
        if (!this.customer) return;
        if (this.saveBlockedReason) {
            this.formError = this.saveBlockedReason;
            return;
        }
        this.loading = true;
        this.formError = null;
        this.lineErrors = {};
        try {
            const delivery = this.deliveryType === 'delivery';
            // A create also carries the fields fixed at create (source, the
            // original upload, the parse map); an edit sends only the header
            // fields and lines the server applies, and refuses the rest.
            const payload: QuoteRequest = {
                customer_id: this.customer.id,
                delivery_type: this.deliveryType,
                freight_cents: delivery ? this.freightCents : 0,
                lines: this.lines.map(l => this.buildLine(l)),
            };
            if (delivery && this.selectedVehicleId) payload.vehicle_id = this.selectedVehicleId;
            if (this.isEditing) payload.job_id = this.jobId;
            if (this.validUntil !== (this.expiresAt ? localDay(this.expiresAt) : '')) {
                // A changed date is valid through the end of that day.
                payload.expires_at = this.validUntil ? new Date(`${this.validUntil}T23:59:59`).toISOString() : null;
            } else if (this.isEditing) {
                payload.expires_at = this.expiresAt;
            }

            if (!this.isEditing) payload.source = this.aiSource ? 'ai' : 'manual';

            // Attach AI parse data if available (a create only)
            if (!this.isEditing && this.aiSource && this.lastParseResult && !this.lastParseResult.synthetic) {
                payload.parse_map = this.lastParseResult.items as unknown as QuoteRequest['parse_map'];
                if (this.lastParseResult.source_image) {
                    const [header, data] = this.lastParseResult.source_image.split(',');
                    const contentType = header?.match(/data:([^;]+)/)?.[1] || 'application/octet-stream';
                    payload.original_file = data;
                    payload.original_content_type = contentType;
                    payload.original_filename = 'material-list-upload';
                }
            }

            let quote;
            if (this.isEditing && this.routeId) {
                if (this.revision === null) throw new Error('The quote has not finished loading.');
                quote = await QuoteService.update(this.routeId, payload, this.revision);
                ToastService.show('Quote updated', 'success');
            } else {
                quote = await QuoteService.create(payload);
                ToastService.show('Draft quote created', 'success');
            }
            if (send) {
                try {
                    await QuoteService.transition(quote.id, 'sent', quote.revision);
                    ToastService.show('Quote sent', 'success');
                } catch (sendErr) {
                    ToastService.show(`Saved as a draft, but not sent: ${quoteErrorMessage(sendErr, 'Failed to send quote')}`, 'error');
                }
            }
            router.navigate(`/quotes/${quote.id}`);
        } catch (err) {
            console.error(err);
            if (err instanceof QuoteApiError && err.isStaleRevision && this.routeId) {
                ToastService.show('This quote changed elsewhere. Reloaded.', 'error');
                this.initialLoading = true;
                await this.loadExistingQuote(this.routeId);
            } else {
                this.showSaveError(err);
            }
        } finally {
            this.loading = false;
        }
    }

    private lineTotalCents(line: LineWithEscalator): number {
        return extensionCents(line.quantity, line.unit_price_ten_thousandths, line.uom_qty, line.price_uom_qty);
    }

    private get subtotalCents() {
        return this.lines.reduce((sum, line) => sum + this.lineTotalCents(line), 0);
    }

    private get effectiveFreightCents() {
        return this.deliveryType === 'delivery' ? this.freightCents : 0;
    }

    private get totalCents() {
        return this.subtotalCents + this.effectiveFreightCents;
    }

    /** The escalator service answers in float dollars; its price is converted to ten thousandths once. */
    private escalatedPriceTT(line: LineWithEscalator): number {
        return line.escalator.enabled && line.escalator.result
            ? floatDollarsToTenThousandths(line.escalator.result.future_price)
            : line.unit_price_ten_thousandths;
    }

    private get escalatedTotalCents() {
        return this.lines.reduce((sum, line) => sum + extensionCents(line.quantity, this.escalatedPriceTT(line), line.uom_qty, line.price_uom_qty), 0);
    }

    private get hasEscalators() {
        return this.lines.some(l => l.escalator.enabled && l.escalator.result);
    }

    private get hasStaleLines() {
        return this.lines.some(l => l.escalator.result?.is_stale);
    }

    private get isOverLimit() {
        // A null limit is no limit; a limit of zero is no credit, so any order over it is over.
        const limit = this.customer?.credit_limit_cents;
        return this.customer && limit !== null && limit !== undefined ? this.customer.balance_cents + this.totalCents > limit : false;
    }

    private renderCustomerCard() {
        const c = this.customer;
        if (!c) {
            return html`
                <gable-customer-select
                    @customer-select=${(e: CustomEvent) => { this.customer = e.detail; }}
                    .selectedCustomerId=${undefined}
                ></gable-customer-select>
            `;
        }
        const limit = c.credit_limit_cents;
        const available = limit === null || limit === undefined ? null : limit - c.balance_cents;
        return html`
            <div data-testid="customer-card" class="rounded-lg border border-white/10 bg-black/20 p-4">
                <div class="flex items-start justify-between gap-3">
                    <div class="min-w-0">
                        <div class="text-white font-medium truncate">${c.name}</div>
                        <div class="font-mono text-xs text-zinc-500">${c.account_number}</div>
                    </div>
                    <button type="button" data-testid="change-customer" @click=${() => { this.customer = null; }}
                        class="text-xs text-blueprint-blue hover:text-sky-300 shrink-0">Change</button>
                </div>
                <dl class="mt-4 grid grid-cols-3 gap-3 text-xs">
                    <div>
                        <dt class="text-zinc-500">Terms</dt>
                        <dd class="mt-0.5 text-zinc-200">${c.payment_terms?.name ?? 'Not set'}</dd>
                    </div>
                    <div>
                        <dt class="text-zinc-500">Open balance</dt>
                        <dd class="mt-0.5 font-mono ${limit !== null && limit !== undefined && c.balance_cents > limit ? 'text-rose-400 font-bold' : 'text-zinc-200'}">${formatCents(c.balance_cents)}</dd>
                    </div>
                    <div>
                        <dt class="text-zinc-500">Credit available</dt>
                        <dd class="mt-0.5 font-mono ${available !== null && available < 0 ? 'text-rose-400 font-bold' : 'text-emerald-400'}">${available === null ? 'No limit' : formatCents(available)}</dd>
                    </div>
                </dl>
                <div class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-zinc-400">
                    <span>Price level <span class="text-gable-green">${c.price_level?.name || 'Retail'}</span></span>
                    ${c.salesperson_name ? html`<span>Salesperson <span class="text-zinc-200">${c.salesperson_name}</span></span>` : nothing}
                    ${c.po_required ? html`<span class="text-amber-400">Customer PO required</span>` : nothing}
                </div>
                ${this.isOverLimit ? html`
                    <div class="mt-3 flex items-start gap-3 bg-rose-500/10 border border-rose-500/20 text-rose-400 text-xs p-3 rounded-lg">
                        ${icon(AlertCircle, 16, 'w-4 h-4 shrink-0 mt-0.5')}
                        <p>This quote exceeds the customer's credit limit. Approval will be required.</p>
                    </div>
                ` : nothing}
            </div>
        `;
    }

    private renderLineRow(line: LineWithEscalator, idx: number) {
        const err = this.lineErrors[idx];
        return html`
            <tr data-testid="quote-line" class="group hover:bg-white/5 transition-colors align-top">
                <td class="px-4 py-3">
                    <div class="font-mono text-white group-hover:text-gable-green transition-colors">${line.sku}</div>
                    <div class="text-zinc-400 text-xs">${line.description}</div>
                    ${err ? html`<div class="text-[10px] text-rose-400 mt-1">${err}</div>` : nothing}
                    <gable-escalator-toggle
                        .basePrice=${Number(tenThousandthsToDecimal(line.unit_price_ten_thousandths))}
                        .escalator=${line.escalator}
                        @escalator-change=${(e: CustomEvent<QuoteLineEscalator>) => this.handleEscalatorChange(idx, e.detail)}
                    ></gable-escalator-toggle>
                </td>
                <td class="px-2 py-3">
                    <div class="inline-flex items-center rounded-md border border-white/10 bg-deep-space">
                        <button type="button" data-testid="line-qty-down" aria-label="Decrease quantity" @click=${() => this.stepQuantity(idx, -1)}
                            class="px-2 py-1.5 text-zinc-400 hover:text-white">${icon(Minus, 12)}</button>
                        <input data-testid="line-qty" aria-label="Quantity" type="text" inputmode="decimal"
                            class="w-16 bg-transparent text-center text-white font-mono text-sm outline-none"
                            .value=${line.quantity}
                            @change=${(e: Event) => this.handleQuantity(idx, (e.target as HTMLInputElement).value)} />
                        <button type="button" data-testid="line-qty-up" aria-label="Increase quantity" @click=${() => this.stepQuantity(idx, 1)}
                            class="px-2 py-1.5 text-zinc-400 hover:text-white">${icon(Plus, 12)}</button>
                    </div>
                </td>
                <td class="px-2 py-3">
                    ${line.uom === '' || err ? html`
                        <select aria-label="Unit of measure"
                            class="w-20 bg-deep-space border border-rose-500/40 rounded px-1 py-1 text-white font-mono text-xs outline-none"
                            .value=${line.uom}
                            @change=${(e: Event) => this.handleUomInput(idx, (e.target as HTMLSelectElement).value)}>
                            <option value="" ?selected=${line.uom === ''}>Unit...</option>
                            ${QUOTE_UOM_CODES.map(code => html`<option value=${code} ?selected=${line.uom === code}>${code}</option>`)}
                        </select>
                        ${line.uom === '' ? html`<div class="text-[10px] text-rose-400 mt-1">Unit of measure required</div>` : nothing}
                    ` : html`<span class="font-mono text-xs text-zinc-400">${line.uom}</span>`}
                </td>
                <td class="px-2 py-3 text-right">
                    <input aria-label="Unit price" type="text" inputmode="decimal"
                        class="w-24 bg-deep-space border border-white/10 rounded px-2 py-1.5 text-right text-zinc-200 font-mono text-sm outline-none focus:border-gable-green"
                        .value=${tenThousandthsToInput(line.unit_price_ten_thousandths)}
                        @change=${(e: Event) => this.handlePrice(idx, (e.target as HTMLInputElement).value)} />
                    ${line.escalator.result ? html`
                        <div class="text-xs text-emerald-400 mt-1 font-mono">→ ${formatPrice4(floatDollarsToTenThousandths(line.escalator.result.future_price))}</div>
                    ` : nothing}
                </td>
                <td class="px-3 py-3 text-right font-mono font-bold text-emerald-400">
                    ${formatCents(this.lineTotalCents(line))}
                    ${line.escalator.result ? html`
                        <div class="text-xs text-emerald-300/70 mt-1">→ ${formatCents(extensionCents(line.quantity, floatDollarsToTenThousandths(line.escalator.result.future_price), line.uom_qty, line.price_uom_qty))}</div>
                    ` : nothing}
                </td>
                <td class="px-2 py-3 text-right">
                    <button type="button" data-testid="line-remove" aria-label="Remove line" @click=${() => this.removeLine(idx)}
                        class="p-1.5 rounded text-zinc-500 hover:text-rose-400 hover:bg-white/5">${icon(Trash2, 14)}</button>
                </td>
            </tr>
        `;
    }

    render() {
        if (this.initialLoading) {
            return html`
                <div>
                    <gable-quote-view-tabs active="new"></gable-quote-view-tabs>
                    <div class="text-slate-400 p-12 text-center">Loading quote...</div>
                </div>
            `;
        }

        const blocked = !this.customer || this.lines.length === 0 || this.loading || this.saveBlockedReason !== null;
        const secondary = 'inline-flex items-center justify-center rounded-lg text-sm font-medium border border-white/10 text-zinc-200 hover:bg-white/5 px-3 py-2 disabled:opacity-50';
        const card = 'bg-slate-steel/50 backdrop-blur border border-white/10 rounded-xl';

        return html`
            <div>
                ${!this.isEditing ? html`<gable-quote-view-tabs active="new"></gable-quote-view-tabs>` : nothing}

                <header class="flex flex-col md:flex-row md:items-center justify-between gap-4 mb-6">
                    <div class="flex items-center gap-3 flex-wrap">
                        <h1 class="text-2xl font-semibold text-white">${this.isEditing ? 'Edit quote' : 'New quote'}</h1>
                        <span data-testid="quote-number" class="font-mono text-sm text-zinc-300">${this.quoteNumber ?? 'Number assigned on save'}</span>
                        <span class="px-2 py-0.5 rounded-full text-[10px] font-bold uppercase tracking-wider bg-white/5 text-zinc-300 border border-white/10">${this.quoteStatus}</span>
                        ${this.yardName ? html`<span data-testid="quote-yard" class="text-xs text-zinc-300">${this.yardName}</span>` : nothing}
                        <span class="font-mono text-xs text-zinc-500">${new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>
                    </div>
                    <div class="flex items-center gap-2">
                        <button type="button" class=${secondary} @click=${() => window.print()}>${icon(Printer, 16, 'mr-2')} Print</button>
                        <button type="button" data-testid="save-draft" class=${secondary} ?disabled=${blocked}
                            @click=${() => this.handleSave(false)}>
                            ${icon(Save, 16, 'mr-2')} ${this.isEditing ? 'Save changes' : 'Save draft'}
                        </button>
                        <button type="button" data-primary="true" ?disabled=${blocked} @click=${() => this.handleSave(true)}
                            class="inline-flex items-center justify-center rounded-lg text-sm font-bold bg-gable-green text-deep-space hover:bg-gable-green/90 px-5 py-2 shadow-glow disabled:opacity-50">
                            ${this.loading ? html`<span class="animate-spin mr-2 inline-flex">${icon(Loader2, 16)}</span>` : icon(Send, 16, 'mr-2')}
                            Send quote
                        </button>
                    </div>
                </header>

                ${this.saveBlockedReason || this.formError ? html`
                    <div role="alert" class="flex items-start gap-3 bg-rose-500/10 border border-rose-500/20 text-rose-400 text-sm p-3 rounded-lg mb-6">
                        ${icon(AlertCircle, 16, 'w-4 h-4 shrink-0 mt-0.5')}
                        <p>${this.lines.length > 0 && this.saveBlockedReason ? this.saveBlockedReason : this.formError}</p>
                    </div>
                ` : nothing}

                <div class="grid grid-cols-1 xl:grid-cols-12 gap-6">
                    <!-- Products -->
                    <section data-pane="products" aria-label="Products" class="xl:col-span-8 space-y-4">
                        <div class="${card} p-5">
                            <h2 class="text-lg font-medium text-white mb-4">Products</h2>
                            <gable-line-item-editor
                                .products=${this.products}
                                .customerId=${this.customer?.id}
                                @add-line=${(e: CustomEvent) => this.handleAddLine(e.detail.product, e.detail.quantity, e.detail.uom, e.detail.unitPriceTenThousandths)}
                            ></gable-line-item-editor>

                            <div class="rounded-lg overflow-x-auto border border-white/5 bg-black/20">
                                <table class="w-full text-sm text-left">
                                    <thead class="bg-white/5 text-zinc-400 uppercase tracking-wider text-xs font-semibold">
                                        <tr>
                                            <th class="px-4 py-3">Item</th>
                                            <th class="px-2 py-3">Qty</th>
                                            <th class="px-2 py-3">Unit</th>
                                            <th class="px-2 py-3 text-right">Unit price</th>
                                            <th class="px-3 py-3 text-right">Line total</th>
                                            <th class="px-2 py-3"><span class="sr-only">Remove</span></th>
                                        </tr>
                                    </thead>
                                    <tbody class="divide-y divide-white/5">
                                        ${this.lines.length === 0 ? html`
                                            <tr><td colspan="6" class="px-6 py-10 text-center text-zinc-500 italic">No items yet. Search a product above or read a material list.</td></tr>
                                        ` : nothing}
                                        ${this.lines.map((line, idx) => this.renderLineRow(line, idx))}
                                    </tbody>
                                </table>
                            </div>

                            <div class="mt-5">
                            <gable-material-intake
                                ?disabled=${this.loading}
                                @parse-complete=${(e: CustomEvent) => this.handleParseComplete(e.detail)}
                                @accept-lines=${(e: CustomEvent) => this.handleAcceptParsed(e.detail)}
                            ></gable-material-intake>
                            </div>
                        </div>
                    </section>

                    <!-- Customer and terms -->
                    <section data-pane="customer" aria-label="Customer and terms" class="xl:col-span-4 space-y-4">
                        <div class="${card} p-5">
                            <h2 class="text-lg font-medium text-white mb-4">Customer</h2>
                            ${this.renderCustomerCard()}
                        </div>

                        <div class="${card} p-5">
                            <h2 class="text-lg font-medium text-white mb-4">Terms</h2>
                            <label class="block text-xs text-zinc-500 mb-1.5" for="quote-valid-until">Valid until</label>
                            <input id="quote-valid-until" type="date" .value=${this.validUntil}
                                @input=${(e: Event) => { this.validUntil = (e.target as HTMLInputElement).value; }}
                                class="w-full bg-white/5 border border-white/10 rounded-lg px-3 py-2 text-sm text-white font-mono focus:outline-none focus:ring-1 focus:ring-gable-green/50" />

                            <div class="flex gap-1 bg-white/5 rounded-lg p-1 border border-white/10 mt-5 mb-4">
                                <button type="button"
                                    @click=${() => { this.deliveryType = 'pickup'; this.freightCents = 0; this.freightText = ''; this.selectedVehicleId = undefined; }}
                                    class="flex-1 flex items-center justify-center gap-2 px-3 py-2 rounded-md text-sm font-medium transition-all ${this.deliveryType === 'pickup' ? 'bg-gable-green/10 text-gable-green border border-gable-green/20' : 'text-zinc-400 hover:text-white'}">
                                    ${icon(Package, 14)} Pickup
                                </button>
                                <button type="button" @click=${() => { this.deliveryType = 'delivery'; }}
                                    class="flex-1 flex items-center justify-center gap-2 px-3 py-2 rounded-md text-sm font-medium transition-all ${this.deliveryType === 'delivery' ? 'bg-blueprint-blue/10 text-blueprint-blue border border-blueprint-blue/20' : 'text-zinc-400 hover:text-white'}">
                                    ${icon(Truck, 14)} Delivery
                                </button>
                            </div>

                            ${this.deliveryType === 'delivery' ? html`
                                <div class="space-y-4">
                                    <div>
                                        <label class="block text-xs text-zinc-500 mb-1.5">Assign truck</label>
                                        <select .value=${this.selectedVehicleId || ''}
                                            @change=${(e: Event) => { this.selectedVehicleId = (e.target as HTMLSelectElement).value || undefined; }}
                                            class="w-full bg-white/5 border border-white/10 rounded-lg px-3 py-2 text-sm text-white focus:outline-none focus:ring-1 focus:ring-blueprint-blue/50">
                                            <option value="">Select a truck...</option>
                                            ${this.vehicles.map(v => html`<option value="${v.id}">${v.name} — ${v.vehicle_type.replace(/_/g, ' ')} (${v.license_plate})</option>`)}
                                        </select>
                                        ${this.vehicles.length === 0 ? html`<p class="text-xs text-zinc-500 mt-1">No vehicles in fleet. Add vehicles in Fleet Management.</p>` : nothing}
                                    </div>
                                    <div>
                                        <label class="block text-xs text-zinc-500 mb-1.5">Freight charge ($)</label>
                                        <input type="number" min="0" step="0.01" placeholder="0.00" .value=${this.freightText}
                                            @input=${(e: Event) => {
                                                this.freightText = (e.target as HTMLInputElement).value;
                                                this.freightCents = dollarsToCents(this.freightText) ?? 0;
                                            }}
                                            class="w-full bg-white/5 border border-white/10 rounded-lg px-3 py-2 text-sm text-white font-mono focus:outline-none focus:ring-1 focus:ring-blueprint-blue/50" />
                                    </div>
                                </div>
                            ` : nothing}
                        </div>

                        <div class="${card} p-5 border-gable-green/20">
                            <h2 class="text-lg font-medium text-white mb-4 flex items-center gap-2">${icon(Calculator, 20, 'w-5 h-5 text-gable-green')} Totals</h2>
                            <dl class="space-y-2 text-sm">
                                <div class="flex justify-between"><dt class="text-zinc-400">Products</dt><dd class="font-mono text-zinc-200">${formatCents(this.subtotalCents)}</dd></div>
                                ${this.effectiveFreightCents > 0 ? html`
                                    <div class="flex justify-between"><dt class="text-zinc-400 flex items-center gap-1.5">${icon(Truck, 14, 'w-3.5 h-3.5 text-blueprint-blue')} Freight</dt><dd class="font-mono text-blueprint-blue">${formatCents(this.effectiveFreightCents)}</dd></div>
                                ` : nothing}
                                <div class="flex justify-between"><dt class="text-zinc-400">Tax</dt><dd class="text-xs text-zinc-500">Calculated at invoicing</dd></div>
                                <div class="flex justify-between items-baseline border-t border-white/10 pt-3"><dt class="text-zinc-200 font-medium">Quote total</dt><dd data-testid="quote-total" class="font-mono text-2xl font-bold text-white">${formatCents(this.totalCents)}</dd></div>
                            </dl>
                            ${this.hasEscalators ? html`
                                <div class="mt-3 pt-3 border-t border-white/5 flex items-baseline justify-between">
                                    <span class="text-zinc-400 flex items-center gap-1.5 text-sm">${icon(TrendingUp, 14, 'w-3.5 h-3.5 text-gable-green')} Escalated total</span>
                                    <span class="text-xl font-mono font-bold text-emerald-400">${formatCents(this.escalatedTotalCents)}</span>
                                </div>
                            ` : nothing}
                            ${this.hasStaleLines ? html`
                                <div class="mt-3 flex items-center gap-2 bg-amber-500/10 border border-amber-500/20 text-amber-400 text-xs p-2.5 rounded-lg">
                                    ${icon(AlertCircle, 14, 'w-3.5 h-3.5 shrink-0')} Some lines have stale pricing
                                </div>
                            ` : nothing}
                        </div>
                    </section>
                </div>
            </div>
        `;
    }
}
