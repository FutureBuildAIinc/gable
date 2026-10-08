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
import { deliveryService } from '../services/deliveryService.ts';
import type { Customer } from '../types/customer.ts';
import type { Product } from '../types/product.ts';
import { QUOTE_UOM_CODES } from '../types/quote.ts';
import type { QuoteRequest, QuoteLineRequest, QuoteDeliveryType } from '../types/quote.ts';
import { formatCents, formatPrice4 } from '../lib/utils.ts';
import {
    dollarsToCents,
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
import { Save, FileText, Calculator, CreditCard, AlertCircle, TrendingUp, Truck, Package } from 'lucide';

// Side-effect imports: register child custom elements
import './quotes/QuoteList.ts';
import '../components/customers/CustomerSelect.ts';
import '../components/quotes/MaterialListUpload.ts';
import '../components/quotes/LineItemEditor.ts';
import '../components/quotes/EscalatorToggle.ts';
import '../components/quotes/ParsedResultsPanel.ts';

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
    @state() private parseResult: ParseResponse | null = null;
    @state() private showParsePanel = false;
    @state() private aiSource = false;
    @state() private lastParseResult: ParseResponse | null = null;

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
            const data = await ProductService.getProducts();
            this.products = data;
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
            uom: uom || product.uom_primary || '',
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
        this.parseResult = result;
        this.showParsePanel = true;
    }

    private handleAcceptParsed(parsedItems: ParsedItem[]) {
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
        this.lastParseResult = this.parseResult;
        this.showParsePanel = false;
        this.parseResult = null;
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

    private async handleSave() {
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
            if (this.isEditing) {
                payload.job_id = this.jobId;
                payload.expires_at = this.expiresAt;
            }

            if (!this.isEditing) payload.source = this.aiSource ? 'ai' : 'manual';

            // Attach AI parse data if available (a create only)
            if (!this.isEditing && this.aiSource && this.lastParseResult) {
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

    render() {
        if (this.initialLoading) {
            return html`
                <div>
                    <gable-quote-view-tabs active="new"></gable-quote-view-tabs>
                    <div class="text-slate-400 p-12 text-center">Loading quote...</div>
                </div>
            `;
        }

        return html`
            <div>
                ${!this.isEditing ? html`<gable-quote-view-tabs active="new"></gable-quote-view-tabs>` : nothing}

                <div class="flex flex-col md:flex-row md:items-center justify-between gap-4 mb-8">
                    <div>
                        <h1 class="text-display-large text-white flex items-center gap-3">
                            ${icon(FileText, 40, 'w-10 h-10 text-gable-green')}
                            ${this.isEditing ? 'Edit Quote' : 'New Quote'}
                        </h1>
                        <p class="text-zinc-500 mt-1 max-w-2xl text-lg">
                            ${this.isEditing ? 'Update this draft quote.' : 'Draft a new pricing proposal.'}
                        </p>
                    </div>
                    <button
                        @click=${() => this.handleSave()}
                        ?disabled=${!this.customer || this.lines.length === 0 || this.loading || this.saveBlockedReason !== null}
                        class="inline-flex items-center justify-center rounded-lg text-sm font-medium transition-colors bg-gable-green text-deep-space hover:bg-gable-green/90 px-4 py-2 shadow-glow disabled:opacity-50"
                    >
                        ${this.loading ? html`<span class="animate-spin mr-2">...</span>` : icon(Save, 16, 'w-4 h-4 mr-2')}
                        ${this.isEditing ? 'Save Changes' : 'Create Quote'}
                    </button>
                </div>

                ${this.saveBlockedReason || this.formError ? html`
                    <div role="alert" class="flex items-start gap-3 bg-rose-500/10 border border-rose-500/20 text-rose-400 text-sm p-3 rounded-lg mb-6">
                        ${icon(AlertCircle, 16, 'w-4 h-4 shrink-0 mt-0.5')}
                        <p>${this.lines.length > 0 && this.saveBlockedReason ? this.saveBlockedReason : this.formError}</p>
                    </div>
                ` : nothing}

                <div class="grid grid-cols-1 lg:grid-cols-12 gap-8">
                    <!-- Left Column: Customer & Details -->
                    <div class="lg:col-span-4 space-y-6">
                        <div class="bg-slate-steel/50 backdrop-blur border border-white/10 rounded-xl overflow-hidden">
                            <div class="p-6">
                                <h2 class="text-lg font-medium text-white mb-4 flex items-center gap-2">
                                    ${icon(CreditCard, 20, 'w-5 h-5 text-zinc-400')}
                                    Customer Details
                                </h2>
                                <gable-customer-select
                                    @customer-select=${(e: CustomEvent) => { this.customer = e.detail; }}
                                    .selectedCustomerId=${this.customer?.id}
                                ></gable-customer-select>

                                ${this.customer ? html`
                                    <div class="mt-6 space-y-4 text-sm border-t border-white/5 pt-6">
                                        <div class="flex justify-between items-center bg-white/5 p-3 rounded-lg">
                                            <span class="text-zinc-400">Account #</span>
                                            <span class="font-mono text-white font-bold">${this.customer.account_number}</span>
                                        </div>
                                        <div class="flex justify-between items-center">
                                            <span class="text-zinc-400">Price Level</span>
                                            <span class="text-gable-green font-medium px-2 py-0.5 rounded bg-gable-green/10 border border-gable-green/20">
                                                ${this.customer.price_level?.name || 'Retail'}
                                            </span>
                                        </div>
                                        <div class="space-y-2 pt-2">
                                            <div class="flex justify-between">
                                                <span class="text-zinc-400">Credit Limit</span>
                                                <span class="font-mono text-zinc-200">${this.customer.credit_limit_cents === null ? 'No limit' : formatCents(this.customer.credit_limit_cents)}</span>
                                            </div>
                                            <div class="flex justify-between">
                                                <span class="text-zinc-400">Balance Due</span>
                                                <span class="font-mono ${this.customer.credit_limit_cents !== null && this.customer.balance_cents > this.customer.credit_limit_cents ? 'text-rose-500 font-bold' : 'text-zinc-200'}">
                                                    ${formatCents(this.customer.balance_cents)}
                                                </span>
                                            </div>
                                            <div class="flex justify-between border-t border-white/5 pt-2">
                                                <span class="text-zinc-400">Available</span>
                                                <span class="font-mono font-bold ${this.customer.credit_limit_cents !== null && this.customer.credit_limit_cents - this.customer.balance_cents < 0 ? 'text-rose-500' : 'text-emerald-400'}">
                                                    ${this.customer.credit_limit_cents === null ? 'No limit' : formatCents(this.customer.credit_limit_cents - this.customer.balance_cents)}
                                                </span>
                                            </div>
                                        </div>
                                        ${this.isOverLimit ? html`
                                            <div class="flex items-start gap-3 bg-rose-500/10 border border-rose-500/20 text-rose-400 text-xs p-3 rounded-lg">
                                                ${icon(AlertCircle, 16, 'w-4 h-4 shrink-0 mt-0.5')}
                                                <p>This quote exceeds the customer's credit limit. Approval will be required.</p>
                                            </div>
                                        ` : nothing}
                                    </div>
                                ` : nothing}
                            </div>
                        </div>

                        <div class="bg-slate-steel/50 backdrop-blur border border-white/10 rounded-xl overflow-hidden">
                            <div class="p-6">
                                <h2 class="text-lg font-medium text-white mb-4 flex items-center gap-2">
                                    ${icon(Truck, 20, 'w-5 h-5 text-zinc-400')}
                                    Fulfillment
                                </h2>

                                <!-- Delivery Type Toggle -->
                                <div class="flex gap-1 bg-white/5 rounded-lg p-1 border border-white/10 mb-4">
                                    <button
                                        @click=${() => { this.deliveryType = 'pickup'; this.freightCents = 0; this.freightText = ''; this.selectedVehicleId = undefined; }}
                                        class="flex-1 flex items-center justify-center gap-2 px-3 py-2 rounded-md text-sm font-medium transition-all ${
                                            this.deliveryType === 'pickup'
                                                ? 'bg-gable-green/10 text-gable-green border border-gable-green/20'
                                                : 'text-zinc-400 hover:text-white'
                                        }"
                                    >
                                        ${icon(Package, 14)} Pickup
                                    </button>
                                    <button
                                        @click=${() => { this.deliveryType = 'delivery'; }}
                                        class="flex-1 flex items-center justify-center gap-2 px-3 py-2 rounded-md text-sm font-medium transition-all ${
                                            this.deliveryType === 'delivery'
                                                ? 'bg-blue-500/10 text-blue-400 border border-blue-500/20'
                                                : 'text-zinc-400 hover:text-white'
                                        }"
                                    >
                                        ${icon(Truck, 14)} Delivery
                                    </button>
                                </div>

                                ${this.deliveryType === 'delivery' ? html`
                                    <div class="space-y-4">
                                        <div>
                                            <label class="block text-xs text-zinc-500 mb-1.5">Assign Truck</label>
                                            <select
                                                .value=${this.selectedVehicleId || ''}
                                                @change=${(e: Event) => { this.selectedVehicleId = (e.target as HTMLSelectElement).value || undefined; }}
                                                class="w-full bg-white/5 border border-white/10 rounded-lg px-3 py-2 text-sm text-white focus:outline-none focus:ring-1 focus:ring-blue-500/50"
                                            >
                                                <option value="">Select a truck...</option>
                                                ${this.vehicles.map(v => html`
                                                    <option value="${v.id}">
                                                        ${v.name} \u2014 ${v.vehicle_type.replace(/_/g, ' ')} (${v.license_plate})
                                                    </option>
                                                `)}
                                            </select>
                                            ${this.vehicles.length === 0 ? html`
                                                <p class="text-xs text-zinc-500 mt-1">No vehicles in fleet. Add vehicles in Fleet Management.</p>
                                            ` : nothing}
                                        </div>
                                        <div>
                                            <label class="block text-xs text-zinc-500 mb-1.5">Freight Charge ($)</label>
                                            <input
                                                type="number"
                                                min="0"
                                                step="0.01"
                                                .value=${this.freightText}
                                                @input=${(e: Event) => {
                                                    this.freightText = (e.target as HTMLInputElement).value;
                                                    this.freightCents = dollarsToCents(this.freightText) ?? 0;
                                                }}
                                                placeholder="0.00"
                                                class="w-full bg-white/5 border border-white/10 rounded-lg px-3 py-2 text-sm text-white font-mono focus:outline-none focus:ring-1 focus:ring-blue-500/50"
                                            />
                                        </div>
                                    </div>
                                ` : nothing}
                            </div>
                        </div>

                        <div class="bg-slate-steel/50 backdrop-blur border border-white/10 rounded-xl overflow-hidden bg-gradient-to-br from-gable-green/5 to-emerald-900/5 border-gable-green/20">
                            <div class="p-6">
                                <h2 class="text-lg font-medium text-white mb-4 flex items-center gap-2">
                                    ${icon(Calculator, 20, 'w-5 h-5 text-gable-green')}
                                    Quote Summary
                                </h2>
                                <div class="flex items-baseline justify-between">
                                    <span class="text-zinc-400">Subtotal</span>
                                    <span class="font-mono font-bold ${this.effectiveFreightCents > 0 ? 'text-lg text-zinc-300' : 'text-2xl text-white'}">${formatCents(this.subtotalCents)}</span>
                                </div>

                                ${this.effectiveFreightCents > 0 ? html`
                                    <div class="flex items-baseline justify-between mt-2">
                                        <span class="text-zinc-400 flex items-center gap-1.5 text-sm">
                                            ${icon(Truck, 14, 'w-3.5 h-3.5 text-blue-400')}
                                            Freight
                                        </span>
                                        <span class="font-mono font-bold text-lg text-blue-400">${formatCents(this.effectiveFreightCents)}</span>
                                    </div>
                                ` : nothing}

                                ${this.effectiveFreightCents > 0 ? html`
                                    <div class="flex items-baseline justify-between mt-2 pt-2 border-t border-white/5">
                                        <span class="text-zinc-400 font-medium">Total</span>
                                        <span class="text-2xl font-mono font-bold text-white">${formatCents(this.totalCents)}</span>
                                    </div>
                                ` : nothing}

                                ${this.hasEscalators ? html`
                                    <div class="mt-3 pt-3 border-t border-white/5">
                                        <div class="flex items-baseline justify-between">
                                            <span class="text-zinc-400 flex items-center gap-1.5 text-sm">
                                                ${icon(TrendingUp, 14, 'w-3.5 h-3.5 text-gable-green')}
                                                Escalated Total
                                            </span>
                                            <span class="text-xl font-mono font-bold text-emerald-400">
                                                ${formatCents(this.escalatedTotalCents)}
                                            </span>
                                        </div>
                                        <div class="text-[10px] text-zinc-500 text-right mt-1">
                                            +${formatCents(this.escalatedTotalCents - this.subtotalCents)} from escalators
                                        </div>
                                    </div>
                                ` : nothing}

                                ${this.hasStaleLines ? html`
                                    <div class="mt-3 flex items-center gap-2 bg-amber-500/10 border border-amber-500/20 text-amber-400 text-xs p-2.5 rounded-lg">
                                        ${icon(AlertCircle, 14, 'w-3.5 h-3.5 shrink-0')}
                                        Some lines have stale pricing
                                    </div>
                                ` : nothing}

                                <div class="text-xs text-zinc-500 text-right mt-1">Tax calculated at invoicing</div>
                            </div>
                        </div>
                    </div>

                    <!-- Right Column: Lines -->
                    <div class="lg:col-span-8 space-y-6">
                        <div class="bg-slate-steel/50 backdrop-blur border border-white/10 rounded-xl overflow-hidden h-full">
                            <div class="p-6">
                                <div class="flex items-center justify-between mb-6">
                                    <h2 class="text-lg font-medium text-white">Line Items</h2>
                                    <gable-material-list-upload
                                        @parse-complete=${(e: CustomEvent) => this.handleParseComplete(e.detail)}
                                        ?disabled=${this.loading}
                                    ></gable-material-list-upload>
                                </div>

                                <gable-line-item-editor
                                    .products=${this.products}
                                    .customerId=${this.customer?.id}
                                    @add-line=${(e: CustomEvent) => this.handleAddLine(e.detail.product, e.detail.quantity, e.detail.uom, e.detail.unitPriceTenThousandths)}
                                ></gable-line-item-editor>

                                <!-- Lines Table -->
                                <div class="mt-8 rounded-lg overflow-hidden border border-white/5 bg-black/20">
                                    <table class="w-full text-sm text-left">
                                        <thead class="bg-white/5 text-zinc-400 uppercase tracking-wider text-xs font-semibold">
                                            <tr>
                                                <th class="px-6 py-4">SKU / Description</th>
                                                <th class="px-6 py-4 text-right">Qty</th>
                                                <th class="px-6 py-4 text-right">Unit Price</th>
                                                <th class="px-6 py-4 text-right">Total</th>
                                            </tr>
                                        </thead>
                                        <tbody class="divide-y divide-white/5">
                                            ${this.lines.length === 0 ? html`
                                                <tr>
                                                    <td colspan="4" class="px-6 py-12 text-center text-zinc-500 italic">
                                                        No items added yet. Start building the quote above.
                                                    </td>
                                                </tr>
                                            ` : nothing}
                                            ${this.lines.map((line, idx) => html`
                                                <tr class="group hover:bg-white/5 transition-colors">
                                                    <td class="px-6 py-4">
                                                        <div class="font-mono text-white mb-0.5 group-hover:text-gable-green transition-colors">${line.sku}</div>
                                                        <div class="text-zinc-400 text-xs">${line.description}</div>

                                                        <gable-escalator-toggle
                                                            .basePrice=${Number(tenThousandthsToDecimal(line.unit_price_ten_thousandths))}
                                                            .escalator=${line.escalator}
                                                            @escalator-change=${(e: CustomEvent<QuoteLineEscalator>) => this.handleEscalatorChange(idx, e.detail)}
                                                        ></gable-escalator-toggle>
                                                    </td>
                                                    <td class="px-6 py-4 text-right font-mono text-zinc-300 align-top">
                                                        ${line.quantity} <span class="text-zinc-600 text-[10px] ml-1">${line.uom}</span>
                                                        ${line.uom === '' || this.lineErrors[idx] ? html`
                                                            <select
                                                                aria-label="Unit of measure"
                                                                class="mt-1 w-24 bg-[#0A0B10] border border-rose-500/40 rounded px-2 py-1 text-white text-right font-mono text-xs outline-none"
                                                                .value=${line.uom}
                                                                @change=${(e: Event) => this.handleUomInput(idx, (e.target as HTMLSelectElement).value)}
                                                            >
                                                                <option value="" ?selected=${line.uom === ''}>Unit...</option>
                                                                ${QUOTE_UOM_CODES.map(code => html`<option value=${code} ?selected=${line.uom === code}>${code}</option>`)}
                                                            </select>
                                                            ${line.uom === '' ? html`<div class="text-[10px] text-rose-400 mt-1">Unit of measure required</div>` : nothing}
                                                        ` : nothing}
                                                        ${this.lineErrors[idx] ? html`<div class="text-[10px] text-rose-400 mt-1">${this.lineErrors[idx]}</div>` : nothing}
                                                    </td>
                                                    <td class="px-6 py-4 text-right font-mono text-zinc-300 align-top">
                                                        ${formatPrice4(line.unit_price_ten_thousandths)}
                                                        ${line.escalator.result ? html`
                                                            <div class="text-xs text-emerald-400 mt-1">
                                                                \u2192 ${formatPrice4(floatDollarsToTenThousandths(line.escalator.result.future_price))}
                                                            </div>
                                                        ` : nothing}
                                                    </td>
                                                    <td class="px-6 py-4 text-right font-mono font-bold text-emerald-400 align-top">
                                                        ${formatCents(this.lineTotalCents(line))}
                                                        ${line.escalator.result ? html`
                                                            <div class="text-xs text-emerald-300/70 mt-1">
                                                                \u2192 ${formatCents(extensionCents(line.quantity, floatDollarsToTenThousandths(line.escalator.result.future_price), line.uom_qty, line.price_uom_qty))}
                                                            </div>
                                                        ` : nothing}
                                                    </td>
                                                </tr>
                                            `)}
                                        </tbody>
                                        ${this.lines.length > 0 ? html`
                                            <tfoot class="bg-white/5 border-t border-white/10">
                                                <tr>
                                                    <td colspan="3" class="px-6 py-4 text-right font-medium text-zinc-400 uppercase tracking-wider text-xs">
                                                        ${this.effectiveFreightCents > 0 ? 'Lines Subtotal' : 'Total Amount'}
                                                    </td>
                                                    <td class="px-6 py-4 text-right font-mono text-xl font-bold text-gable-green">${formatCents(this.subtotalCents)}</td>
                                                </tr>
                                                ${this.effectiveFreightCents > 0 ? html`
                                                    <tr class="border-t border-white/5">
                                                        <td colspan="3" class="px-6 py-2 text-right text-zinc-400 text-xs">
                                                            <span class="flex items-center justify-end gap-1.5">
                                                                ${icon(Truck, 12, 'w-3 h-3 text-blue-400')} Freight
                                                            </span>
                                                        </td>
                                                        <td class="px-6 py-2 text-right font-mono text-sm text-blue-400">${formatCents(this.effectiveFreightCents)}</td>
                                                    </tr>
                                                    <tr class="border-t border-white/5">
                                                        <td colspan="3" class="px-6 py-4 text-right font-medium text-zinc-400 uppercase tracking-wider text-xs">Total Amount</td>
                                                        <td class="px-6 py-4 text-right font-mono text-xl font-bold text-gable-green">${formatCents(this.totalCents)}</td>
                                                    </tr>
                                                ` : nothing}
                                            </tfoot>
                                        ` : nothing}
                                    </table>
                                </div>
                            </div>
                        </div>
                    </div>
                </div>

                <!-- AI Parse Results Overlay -->
                ${this.showParsePanel && this.parseResult ? html`
                    <gable-parsed-results-panel
                        .result=${this.parseResult}
                        @accept=${(e: CustomEvent) => this.handleAcceptParsed(e.detail)}
                        @close=${() => {
                            this.showParsePanel = false;
                            this.parseResult = null;
                        }}
                    ></gable-parsed-results-panel>
                ` : nothing}
            </div>
        `;
    }
}
