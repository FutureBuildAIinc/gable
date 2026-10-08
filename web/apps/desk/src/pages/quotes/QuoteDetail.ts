// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { router } from '../../lib/router.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { FileText, Download, ArrowLeft, ShoppingCart, Send, Check, X, Sparkles, Eye, Map, Package, AlertTriangle, ShieldAlert, Truck, TrendingUp } from 'lucide';
import { QuoteService, QuoteApiError, quoteErrorMessage, orderRequestFromQuotePayload } from '../../services/QuoteService.ts';
import type { Quote, QuoteStatus, ParseMapItem } from '../../types/quote.ts';
import { formatCents, formatPrice4 } from '../../lib/utils.ts';
import { extensionCents, dollarsToCents } from '../../lib/money.ts';
import { OrderService } from '../../services/OrderService.ts';
import '../../components/quotes/exposure-banner.ts';

type Tab = 'details' | 'original' | 'mapping';

@customElement('gable-quote-detail')
export class GableQuoteDetail extends LitElement {
    createRenderRoot() { return this; }

    @property({ attribute: 'route-id' }) routeId = '';

    @state() private quote: Quote | null = null;
    @state() private loading = true;
    @state() private processing = false;
    @state() private activeTab: Tab = 'details';
    /** Lumber-index margin erosion, reported by <gable-exposure-banner>. */
    @state() private exposureDollars = 0;

    private stateColors: Record<string, string> = {
        draft: 'bg-zinc-500/20 text-zinc-400 border-zinc-500/30',
        sent: 'bg-blue-500/20 text-blue-400 border-blue-500/30',
        accepted: 'bg-emerald-500/20 text-emerald-400 border-emerald-500/30',
        rejected: 'bg-red-500/20 text-red-400 border-red-500/30',
        expired: 'bg-amber-500/20 text-amber-400 border-amber-500/30',
    };

    connectedCallback() {
        super.connectedCallback();
        if (this.routeId) this.loadQuote(this.routeId);
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('routeId') && changed.get('routeId') !== undefined && this.routeId) {
            this.loading = true;
            this.loadQuote(this.routeId);
        }
    }

    private async loadQuote(quoteId: string) {
        try {
            const data = await QuoteService.get(quoteId);
            this.quote = data;
        } catch (error) {
            console.error(error);
            ToastService.show(`Failed to load quote: ${quoteErrorMessage(error, 'Unknown error')}`, 'error');
        } finally {
            this.loading = false;
        }
    }

    /** A 409 stale_revision means another session moved the quote: say so and reload it. */
    private async handleStale(error: unknown): Promise<boolean> {
        if (error instanceof QuoteApiError && error.isStaleRevision && this.quote) {
            ToastService.show('This quote changed elsewhere. Reloaded.', 'error');
            await this.loadQuote(this.quote.id);
            return true;
        }
        return false;
    }

    private async handleStateChange(to: QuoteStatus) {
        if (!this.quote) return;
        this.processing = true;
        try {
            const updated = await QuoteService.transition(this.quote.id, to, this.quote.revision);
            this.quote = updated;
            ToastService.show(`Quote marked as ${to}`, 'success');
        } catch (error) {
            if (await this.handleStale(error)) return;
            ToastService.show(`Failed: ${quoteErrorMessage(error, 'Unknown error')}`, 'error');
        } finally {
            this.processing = false;
        }
    }

    private async handleConvert() {
        if (!this.quote) return;
        this.processing = true;
        try {
            const orderPayload = await QuoteService.convert(this.quote.id, this.quote.revision);
            const order = await OrderService.createOrder(orderRequestFromQuotePayload(orderPayload));
            ToastService.show('Quote converted to order', 'success');
            router.navigate(`/orders/${order.id}`);
        } catch (error) {
            if (await this.handleStale(error)) return;
            ToastService.show(`Failed: ${quoteErrorMessage(error, 'Unknown error')}`, 'error');
        } finally {
            this.processing = false;
        }
    }

    private renderSummaryCard(label: string, value: string, accent?: string) {
        return html`
            <div class="bg-slate-steel border border-white/10 rounded-lg p-4">
                <div class="text-xs text-zinc-500 uppercase tracking-wider mb-1">${label}</div>
                <div class="text-lg font-mono font-bold ${accent || 'text-white'}">${value}</div>
            </div>
        `;
    }

    private renderTimelineEntry(label: string, date: string) {
        return html`
            <div class="flex items-center gap-3">
                <div class="w-2 h-2 rounded-full bg-gable-green/60"></div>
                <span class="text-zinc-400 w-20">${label}</span>
                <span class="text-white font-mono text-xs">${new Date(date).toLocaleString()}</span>
            </div>
        `;
    }

    private renderConfidenceBadge(confidence: number, isSpecialOrder: boolean) {
        if (isSpecialOrder) {
            return html`
                <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-bold uppercase tracking-wider bg-rose-500/15 text-rose-400 border border-rose-500/20">
                    ${icon(AlertTriangle, 10, 'w-2.5 h-2.5')} Special
                </span>
            `;
        }
        const pct = Math.round(confidence * 100);
        if (confidence >= 0.9) {
            return html`
                <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-bold uppercase tracking-wider bg-emerald-500/15 text-emerald-400 border border-emerald-500/20">
                    ${icon(Check, 10, 'w-2.5 h-2.5')} ${pct}%
                </span>
            `;
        }
        return html`
            <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-bold uppercase tracking-wider bg-amber-500/15 text-amber-400 border border-amber-500/20">
                ${icon(AlertTriangle, 10, 'w-2.5 h-2.5')} ${pct}%
            </span>
        `;
    }

    private renderDetailsTab(quote: Quote) {
        const lines = quote.lines || [];
        // All money here is integer cents. The line cost is the quantity times
        // the unit cost (ten thousandths) with NO conversion pair: the cost is
        // per the product's own unit (its stocking unit, which is also the
        // sale unit), so the pair that converts the PRICE unit never applies
        // to it. 187.5 PCS stocked and costed per piece, priced per MBF.
        const lineCostCents = (l: Quote['lines'][number]) =>
            extensionCents(l.quantity, l.unit_cost_ten_thousandths);
        const totalRevenue = lines.reduce((s, l) => s + l.line_total_cents, 0);
        const baseCost = lines.reduce((s, l) => s + lineCostCents(l), 0);
        const baseMargin = totalRevenue - baseCost;
        const baseMarginPct = totalRevenue > 0 ? (baseMargin / totalRevenue) * 100 : 0;
        const hasCostData = lines.some(l => l.unit_cost_ten_thousandths > 0);

        // Index-driven margin erosion: the locked price stands, but the cost to
        // fulfil has risen by the exposure dollars, so realized margin drops.
        // The exposure route still answers in float dollars; it is converted to
        // cents once, here, through the integer parser.
        const erosion = this.exposureDollars > 0 ? (dollarsToCents(this.exposureDollars.toFixed(2)) ?? 0) : 0;
        const totalCost = baseCost + erosion;
        const projectedMargin = totalRevenue - totalCost;
        const marginPct = totalRevenue > 0 ? (projectedMargin / totalRevenue) * 100 : 0;

        return html`
            <div class="space-y-6">
                <!-- Projected Margin Card -->
                ${hasCostData ? html`
                    <div class="bg-slate-steel border border-white/10 rounded-xl p-5">
                        <div class="flex items-center gap-2 mb-4">
                            ${icon(TrendingUp, 20, 'w-5 h-5 text-gable-green')}
                            <h3 class="text-sm font-semibold text-white uppercase tracking-wider">Projected Margin</h3>
                        </div>
                        <div class="grid grid-cols-2 md:grid-cols-4 gap-6">
                            <div>
                                <div class="text-[11px] text-zinc-500 uppercase tracking-wider mb-1">Revenue</div>
                                <div class="text-xl font-mono font-bold text-white">${formatCents(totalRevenue)}</div>
                            </div>
                            <div>
                                <div class="text-[11px] text-zinc-500 uppercase tracking-wider mb-1">Est. Cost</div>
                                <div class="text-xl font-mono font-bold text-zinc-300">${formatCents(totalCost)}</div>
                                ${erosion > 0 ? html`
                                    <div class="text-[11px] font-mono text-rose-400 mt-1">+${formatCents(erosion)} index</div>
                                ` : nothing}
                            </div>
                            <div>
                                <div class="text-[11px] text-zinc-500 uppercase tracking-wider mb-1">Projected Margin</div>
                                <div class="text-xl font-mono font-bold ${projectedMargin >= 0 ? 'text-gable-green' : 'text-red-400'}">
                                    ${formatCents(projectedMargin)}
                                </div>
                                ${erosion > 0 ? html`
                                    <div class="text-[11px] font-mono text-zinc-500 mt-1 line-through">${formatCents(baseMargin)}</div>
                                ` : nothing}
                            </div>
                            <div>
                                <div class="text-[11px] text-zinc-500 uppercase tracking-wider mb-1">Margin %</div>
                                <div class="text-xl font-mono font-bold ${marginPct >= 20 ? 'text-gable-green' : marginPct >= 10 ? 'text-amber-400' : 'text-red-400'}">
                                    ${marginPct.toFixed(1)}%
                                    ${erosion > 0 ? html`<span class="text-[11px] text-zinc-500 line-through ml-1">${baseMarginPct.toFixed(1)}%</span>` : nothing}
                                </div>
                                <div class="mt-2 h-1.5 bg-white/5 rounded-full overflow-hidden">
                                    <div
                                        class="h-full rounded-full transition-all ${marginPct >= 20 ? 'bg-gable-green' : marginPct >= 10 ? 'bg-amber-400' : 'bg-red-400'}"
                                        style="width: ${Math.min(Math.max(marginPct, 0), 100)}%"
                                    ></div>
                                </div>
                            </div>
                        </div>
                    </div>
                ` : nothing}

                <!-- Summary Cards -->
                <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
                    ${this.renderSummaryCard('Total Amount', formatCents(quote.total_cents), 'text-gable-green')}
                    ${this.renderSummaryCard('Lines', String(lines.length))}
                    ${this.renderSummaryCard('Fulfillment', quote.delivery_type === 'delivery' ? 'Delivery' : 'Pickup', quote.delivery_type === 'delivery' ? 'text-blue-400' : undefined)}
                    ${this.renderSummaryCard('Source', quote.source === 'ai' ? 'AI Parsed' : 'Manual')}
                </div>

                <!-- Delivery Info -->
                ${quote.delivery_type === 'delivery' ? html`
                    <div class="bg-blue-500/5 border border-blue-500/20 rounded-lg p-4 flex items-center gap-4">
                        ${icon(Truck, 20, 'w-5 h-5 text-blue-400 shrink-0')}
                        <div class="flex-1 flex items-center gap-6 text-sm">
                            ${quote.vehicle_name ? html`
                                <div>
                                    <span class="text-zinc-500 mr-2">Truck:</span>
                                    <span class="text-white font-medium">${quote.vehicle_name}</span>
                                </div>
                            ` : nothing}
                            ${quote.freight_cents > 0 ? html`
                                <div>
                                    <span class="text-zinc-500 mr-2">Freight:</span>
                                    <span class="text-blue-400 font-mono font-medium">${formatCents(quote.freight_cents)}</span>
                                </div>
                            ` : nothing}
                        </div>
                    </div>
                ` : nothing}

                <!-- Timeline -->
                <div class="bg-slate-steel border border-white/10 rounded-lg p-6">
                    <h3 class="text-sm font-medium text-zinc-400 uppercase tracking-wider mb-4">Timeline</h3>
                    <div class="space-y-3 text-sm">
                        ${this.renderTimelineEntry('Created', quote.created_at)}
                        ${quote.sent_at ? this.renderTimelineEntry('Sent', quote.sent_at) : nothing}
                        ${quote.accepted_at ? this.renderTimelineEntry('Accepted', quote.accepted_at) : nothing}
                        ${quote.rejected_at ? this.renderTimelineEntry('Rejected', quote.rejected_at) : nothing}
                    </div>
                </div>

                <!-- Line Items -->
                <div class="bg-slate-steel border border-white/10 rounded-lg overflow-hidden">
                    <table class="w-full text-left text-sm" aria-label="Quote line items">
                        <thead>
                            <tr class="border-b border-white/10 bg-white/5">
                                <th class="p-4 font-medium text-muted-foreground">SKU</th>
                                <th class="p-4 font-medium text-muted-foreground">Description</th>
                                <th class="p-4 font-medium text-muted-foreground text-right">Qty</th>
                                <th class="p-4 font-medium text-muted-foreground text-right">Unit Price</th>
                                ${hasCostData ? html`<th class="p-4 font-medium text-muted-foreground text-right">Unit Cost</th>` : nothing}
                                <th class="p-4 font-medium text-muted-foreground text-right">Total</th>
                                ${hasCostData ? html`<th class="p-4 font-medium text-muted-foreground text-right">Margin</th>` : nothing}
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-white/5">
                            ${lines.map(line => {
                                const lineCost = lineCostCents(line);
                                const lineMargin = line.line_total_cents - lineCost;
                                const lineMarginPct = line.line_total_cents > 0 ? (lineMargin / line.line_total_cents) * 100 : 0;
                                return html`
                                    <tr class="hover:bg-white/5 transition-colors">
                                        <td class="p-4 font-mono text-white">${line.sku}</td>
                                        <td class="p-4 text-zinc-300">${line.description}</td>
                                        <td class="p-4 text-right font-mono text-zinc-300">
                                            ${line.quantity} <span class="text-zinc-600 text-xs">${line.uom}</span>
                                            ${line.price_uom && line.price_uom !== line.uom ? html`<div class="text-zinc-600 text-[10px]">priced per ${line.price_uom}</div>` : nothing}
                                        </td>
                                        <td class="p-4 text-right font-mono text-zinc-300">${formatPrice4(line.unit_price_ten_thousandths)}</td>
                                        ${hasCostData ? html`
                                            <td class="p-4 text-right font-mono text-zinc-500">
                                                ${line.unit_cost_ten_thousandths > 0 ? formatPrice4(line.unit_cost_ten_thousandths) : '\u2014'}
                                            </td>
                                        ` : nothing}
                                        <td class="p-4 text-right font-mono text-gable-green font-medium">${formatCents(line.line_total_cents)}</td>
                                        ${hasCostData ? html`
                                            <td class="p-4 text-right font-mono">
                                                ${line.unit_cost_ten_thousandths > 0 ? html`
                                                    <span class="${lineMarginPct >= 20 ? 'text-gable-green' : lineMarginPct >= 10 ? 'text-amber-400' : 'text-red-400'}">
                                                        ${lineMarginPct.toFixed(1)}%
                                                    </span>
                                                ` : html`<span class="text-zinc-600">\u2014</span>`}
                                            </td>
                                        ` : nothing}
                                    </tr>
                                `;
                            })}
                        </tbody>
                        ${lines.length > 0 ? html`
                            <tfoot class="bg-white/5 border-t border-white/10">
                                ${quote.freight_cents > 0 ? html`
                                    <tr>
                                        <td colspan="${hasCostData ? 6 : 4}" class="p-4 text-right font-medium text-zinc-400 uppercase tracking-wider text-xs">Lines Subtotal</td>
                                        <td class="p-4 text-right font-mono text-lg text-zinc-300">
                                            ${formatCents(totalRevenue)}
                                        </td>
                                    </tr>
                                    <tr class="border-t border-white/5">
                                        <td colspan="${hasCostData ? 6 : 4}" class="px-4 py-2 text-right text-zinc-400 text-xs">
                                            <span class="flex items-center justify-end gap-1.5">
                                                ${icon(Truck, 12, 'w-3 h-3 text-blue-400')} Freight
                                            </span>
                                        </td>
                                        <td class="px-4 py-2 text-right font-mono text-sm text-blue-400">${formatCents(quote.freight_cents)}</td>
                                    </tr>
                                ` : nothing}
                                <tr class="${quote.freight_cents > 0 ? 'border-t border-white/5' : ''}">
                                    <td colspan="${hasCostData ? 6 : 4}" class="p-4 text-right font-medium text-zinc-400 uppercase tracking-wider text-xs">Total</td>
                                    <td class="p-4 text-right font-mono text-xl font-bold text-gable-green">${formatCents(quote.total_cents)}</td>
                                </tr>
                            </tfoot>
                        ` : nothing}
                    </table>
                </div>
            </div>
        `;
    }

    private renderOriginalUploadTab(quote: Quote) {
        const fileUrl = QuoteService.getOriginalFileUrl(quote.id);
        const isImage = quote.original_content_type?.startsWith('image/');
        const isPdf = quote.original_content_type === 'application/pdf';

        return html`
            <div class="space-y-4">
                <div class="flex items-center justify-between">
                    <h3 class="text-sm font-medium text-zinc-400 uppercase tracking-wider">Original Material List Upload</h3>
                    <a href="${fileUrl}" download="${quote.original_filename || 'original-upload'}"
                        class="text-gable-green hover:text-gable-green/80 text-sm flex items-center gap-2 transition-colors">
                        ${icon(Download, 14)} Download Original
                    </a>
                </div>

                <div class="bg-slate-steel border border-white/10 rounded-lg overflow-hidden">
                    ${isImage ? html`
                        <img src="${fileUrl}" alt="Original material list" class="w-full max-h-[70vh] object-contain bg-black/30 p-4" />
                    ` : nothing}
                    ${isPdf ? html`
                        <iframe src="${fileUrl}" class="w-full h-[70vh]" title="Original PDF"></iframe>
                    ` : nothing}
                    ${!isImage && !isPdf ? html`
                        <div class="p-12 text-center text-zinc-500">
                            ${icon(FileText, 48, 'w-12 h-12 mx-auto mb-4 opacity-50')}
                            <p>Preview not available for ${quote.original_content_type}</p>
                            <a href="${fileUrl}" download class="text-gable-green hover:underline text-sm mt-2 inline-block">
                                Download to view
                            </a>
                        </div>
                    ` : nothing}
                </div>
            </div>
        `;
    }

    private renderMappingTab(parseMap: ParseMapItem[]) {
        return html`
            <div class="space-y-4">
                <div class="flex items-center justify-between">
                    <h3 class="text-sm font-medium text-zinc-400 uppercase tracking-wider">AI Extraction Mapping</h3>
                    <div class="text-xs text-zinc-500">
                        ${parseMap.length} items extracted &middot;
                        ${parseMap.filter(i => i.confidence >= 0.9 && !i.is_special_order).length} high confidence &middot;
                        ${parseMap.filter(i => i.is_special_order).length} special order
                    </div>
                </div>

                <div class="space-y-3">
                    ${parseMap.map(item => html`
                        <div class="bg-slate-steel border border-white/10 rounded-lg p-4">
                            <!-- Raw text source -->
                            <div class="flex items-center gap-3 mb-3 pb-3 border-b border-white/5">
                                <span class="text-[10px] font-bold uppercase tracking-wider text-zinc-600 bg-white/5 px-2 py-0.5 rounded">
                                    Raw Input
                                </span>
                                <span class="font-mono text-sm text-zinc-300">${item.raw_text}</span>
                                ${this.renderConfidenceBadge(item.confidence, item.is_special_order)}
                            </div>

                            <!-- Mapping arrow -->
                            <div class="flex items-start gap-4">
                                <div class="flex-1">
                                    ${item.matched_product ? html`
                                        <div class="flex items-center gap-3">
                                            ${icon(Package, 16, 'w-4 h-4 text-emerald-400 shrink-0')}
                                            <div>
                                                <div class="font-mono text-white text-sm">${item.matched_product.sku}</div>
                                                <div class="text-xs text-zinc-400">${item.matched_product.description}</div>
                                            </div>
                                            <div class="ml-auto text-right">
                                                <div class="font-mono text-sm text-white">
                                                    ${item.quantity} <span class="text-zinc-500 text-xs">${item.uom}</span>
                                                </div>
                                                <div class="font-mono text-xs text-emerald-400">$${item.matched_product.base_price.toFixed(2)}/ea</div>
                                            </div>
                                        </div>
                                    ` : html`
                                        <div class="flex items-center gap-3">
                                            ${icon(ShieldAlert, 16, 'w-4 h-4 text-rose-400 shrink-0')}
                                            <div>
                                                <div class="text-sm text-rose-300">No catalog match \u2014 Special Order</div>
                                                <div class="text-xs text-zinc-500">${item.raw_text}</div>
                                            </div>
                                            <div class="ml-auto font-mono text-sm text-white">
                                                ${item.quantity} <span class="text-zinc-500 text-xs">${item.uom}</span>
                                            </div>
                                        </div>
                                    `}
                                </div>
                            </div>

                            <!-- Alternatives considered -->
                            ${item.alternatives && item.alternatives.length > 0 ? html`
                                <div class="mt-3 pt-3 border-t border-white/5">
                                    <span class="text-[10px] font-bold uppercase tracking-wider text-zinc-600">
                                        Alternatives Considered
                                    </span>
                                    <div class="mt-2 space-y-1.5 pl-4 border-l-2 border-zinc-700/50">
                                        ${item.alternatives.map(alt => html`
                                            <div class="flex items-center justify-between text-xs text-zinc-500">
                                                <span><span class="font-mono text-zinc-400">${alt.sku}</span> \u2014 ${alt.description}</span>
                                                <span class="font-mono text-zinc-400">$${alt.base_price.toFixed(2)}</span>
                                            </div>
                                        `)}
                                    </div>
                                </div>
                            ` : nothing}
                        </div>
                    `)}
                </div>
            </div>
        `;
    }

    render() {
        if (this.loading || !this.quote) {
            return html`<div class="text-white p-8">Loading quote...</div>`;
        }

        const quote = this.quote;

        const tabs: { id: Tab; label: string; iconData: typeof FileText; show: boolean }[] = [
            { id: 'details', label: 'Details', iconData: FileText, show: true },
            { id: 'original', label: 'Original Upload', iconData: Eye, show: quote.source === 'ai' && !!quote.original_filename },
            { id: 'mapping', label: 'AI Mapping', iconData: Map, show: quote.source === 'ai' && !!(quote.parse_map?.length) },
        ];

        return html`
            <div class="space-y-6 max-w-6xl mx-auto">
                <!-- Header -->
                <div class="flex items-center justify-between pb-6 border-b border-white/10">
                    <div>
                        <button @click=${() => router.navigate('/quotes')} class="text-zinc-500 hover:text-white text-sm flex items-center gap-1 mb-3 transition-colors">
                            ${icon(ArrowLeft, 14)} Back to Quotes
                        </button>
                        <div class="flex items-center gap-4 mb-2">
                            <h1 class="text-3xl font-bold font-mono text-white">Quote ${quote.number}</h1>
                            <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium border uppercase ${this.stateColors[quote.status] || ''}">
                                ${quote.status}
                            </span>
                            ${quote.source === 'ai' ? html`
                                <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-bold uppercase tracking-wider bg-violet-500/15 text-violet-400 border border-violet-500/20">
                                    ${icon(Sparkles, 12, 'w-3 h-3')} AI Parsed
                                </span>
                            ` : nothing}
                        </div>
                        <p class="text-muted-foreground">
                            ${quote.customer_name || quote.customer_id.slice(0, 8)} &middot; Created ${new Date(quote.created_at).toLocaleDateString()}
                        </p>
                    </div>
                    <div class="flex gap-2">
                        ${quote.status === 'draft' ? html`
                            <button @click=${() => this.handleStateChange('sent')} ?disabled=${this.processing}
                                class="bg-blue-600 text-white px-4 py-2 rounded hover:bg-blue-500 transition-colors flex items-center gap-2 text-sm font-medium disabled:opacity-50">
                                ${icon(Send, 14)} Mark Sent
                            </button>
                        ` : nothing}
                        ${(quote.status === 'draft' || quote.status === 'sent') ? html`
                            <button @click=${() => this.handleStateChange('accepted')} ?disabled=${this.processing}
                                class="bg-emerald-600 text-white px-4 py-2 rounded hover:bg-emerald-500 transition-colors flex items-center gap-2 text-sm font-medium disabled:opacity-50">
                                ${icon(Check, 14)} Accept
                            </button>
                            <button @click=${() => this.handleStateChange('rejected')} ?disabled=${this.processing}
                                class="bg-red-600/80 text-white px-4 py-2 rounded hover:bg-red-500 transition-colors flex items-center gap-2 text-sm font-medium disabled:opacity-50">
                                ${icon(X, 14)} Reject
                            </button>
                        ` : nothing}
                        ${(quote.status === 'draft' || quote.status === 'sent') ? html`
                            <button @click=${() => this.handleConvert()} ?disabled=${this.processing}
                                class="bg-gable-green text-black px-4 py-2 rounded hover:bg-gable-green/90 transition-colors flex items-center gap-2 text-sm font-bold disabled:opacity-50">
                                ${icon(ShoppingCart, 14)} Convert to Order
                            </button>
                        ` : nothing}
                    </div>
                </div>

                <!-- Lumber index exposure / margin erosion (self-hides when not at risk) -->
                <gable-exposure-banner
                    .quoteId=${quote.id}
                    .shortId=${quote.number}
                    @exposure-loaded=${(e: Event) => { this.exposureDollars = (e as CustomEvent).detail?.exposureDollars ?? 0; }}>
                </gable-exposure-banner>

                <!-- Tabs -->
                <div class="flex gap-1 border-b border-white/10">
                    ${tabs.filter(t => t.show).map(tab => html`
                        <button
                            @click=${() => { this.activeTab = tab.id; }}
                            class="flex items-center gap-2 px-4 py-3 text-sm font-medium transition-colors border-b-2 -mb-px ${
                                this.activeTab === tab.id
                                    ? 'text-gable-green border-gable-green'
                                    : 'text-zinc-500 border-transparent hover:text-white hover:border-white/20'
                            }"
                        >
                            ${icon(tab.iconData, 16)}
                            ${tab.label}
                        </button>
                    `)}
                </div>

                <!-- Tab Content -->
                ${this.activeTab === 'details' ? this.renderDetailsTab(quote) : nothing}
                ${this.activeTab === 'original' ? this.renderOriginalUploadTab(quote) : nothing}
                ${this.activeTab === 'mapping' ? this.renderMappingTab((quote.parse_map ?? []) as unknown as ParseMapItem[]) : nothing}
            </div>
        `;
    }
}
