// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { router } from '../../lib/router.ts';
import { ToastService } from '../../lib/toast-service.ts';
import type { ProductDetail as ProductDetailType, PIMContent } from '../../types/pim.ts';
import { PIMService } from '../../services/PIMService.ts';
import { apiErrorMessage } from '../../services/apiError.ts';
import { formatPrice4 } from '../../lib/utils.ts';
import { ArrowLeft, Loader2, Package, FileText, Image, Megaphone, Search, Warehouse, Box } from 'lucide';
import './tabs/ProductOverviewTab.ts';
import './tabs/ProductContentTab.ts';
import './tabs/ProductMediaTab.ts';
import './tabs/ProductCollateralTab.ts';
import './tabs/ProductSEOTab.ts';
import './tabs/ProductStockTab.ts';
import './tabs/ProductGeometryTab.ts';
import '../../components/inventory/ProductMarginModal.ts';

type TabId = 'overview' | 'content' | 'media' | 'collateral' | 'seo' | 'stock' | 'geometry';

interface TabDef {
    id: TabId;
    label: string;
    iconData: typeof Package;
}

const TABS: TabDef[] = [
    { id: 'overview', label: 'Overview', iconData: Package },
    { id: 'content', label: 'PIM / Content', iconData: FileText },
    { id: 'media', label: 'Media', iconData: Image },
    { id: 'collateral', label: 'Collateral', iconData: Megaphone },
    { id: 'seo', label: 'SEO', iconData: Search },
    { id: 'stock', label: 'Stock & Locations', iconData: Warehouse },
    { id: 'geometry', label: 'Geometry', iconData: Box },
];

@customElement('gable-product-detail')
export class GableProductDetail extends LitElement {
    createRenderRoot() { return this; }

    @property({ attribute: 'route-id' }) routeId = '';

    /** The PIM aggregate as loaded; detail.product carries the revision every write of this page names. */
    @state() private detail: ProductDetailType | null = null;
    @state() private loading = true;
    @state() private isMarginModalOpen = false;
    @state() private activeTab: TabId = 'overview';

    connectedCallback() {
        super.connectedCallback();
        this.loadProduct();
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('routeId') && changed.get('routeId') !== undefined) {
            this.loading = true;
            this.loadProduct();
        }
    }

    private async loadProduct() {
        if (!this.routeId) return;
        try {
            const data = await PIMService.getProductDetail(this.routeId);
            this.detail = data;
        } catch (err) {
            console.error('Failed to load product:', err);
            ToastService.show(apiErrorMessage(err, 'Failed to load product'), 'error');
        } finally {
            this.loading = false;
        }
    }

    private handleContentUpdate(content: PIMContent) {
        if (this.detail) {
            this.detail = { ...this.detail, content };
        }
    }

    private handleMediaUpdate() {
        this.loadProduct();
    }

    private handleCollateralUpdate() {
        this.loadProduct();
    }

    render() {
        if (this.loading) {
            return html`
                <div class="flex items-center justify-center h-96">
                    ${icon(Loader2, 32, 'w-8 h-8 text-zinc-500 animate-spin')}
                </div>
            `;
        }

        const detail = this.detail;
        if (!detail) {
            return html`
                <div class="flex flex-col items-center justify-center h-96 gap-4">
                    ${icon(Package, 64, 'w-16 h-16 text-zinc-600')}
                    <p class="text-zinc-500">Product not found</p>
                    <button @click=${() => router.navigate('/inventory')} class="text-gable-green hover:underline text-sm">
                        Back to Inventory
                    </button>
                </div>
            `;
        }

        const product = detail.product;
        return html`
            <div class="space-y-6">
                <!-- Header -->
                <div class="flex items-center gap-4">
                    <button
                        @click=${() => router.navigate('/inventory')}
                        class="p-2 rounded-lg hover:bg-white/5 text-zinc-400 hover:text-white transition-colors"
                        aria-label="Back to inventory"
                    >
                        ${icon(ArrowLeft, 20, 'w-5 h-5')}
                    </button>
                    <div class="flex-1 min-w-0">
                        <div class="flex items-center gap-3 mb-1">
                            <h1 class="text-xl font-bold text-white truncate">${product.description}</h1>
                            <span class="px-2 py-0.5 bg-white/5 border border-white/10 rounded text-xs font-mono text-zinc-400 shrink-0">
                                ${product.sku}
                            </span>
                        </div>
                        <div class="flex items-center gap-3 text-sm text-zinc-500">
                            ${product.vendor ? html`<span>${product.vendor}</span>` : nothing}
                            <span>${product.stock_uom}</span>
                            <span class="font-mono text-emerald-400">${formatPrice4(product.base_price_ten_thousandths)}</span>
                        </div>
                    </div>
                </div>

                <!-- Tabs -->
                <div class="border-b border-white/10">
                    <div class="flex gap-1 overflow-x-auto">
                        ${TABS.map(tab => html`
                            <button
                                @click=${() => { this.activeTab = tab.id; }}
                                class="flex items-center gap-2 px-4 py-2.5 text-sm font-medium whitespace-nowrap border-b-2 transition-colors ${
                                    this.activeTab === tab.id
                                        ? 'border-gable-green text-gable-green'
                                        : 'border-transparent text-zinc-400 hover:text-white hover:border-white/20'
                                }"
                            >
                                ${icon(tab.iconData, 16, 'w-4 h-4')}
                                ${tab.label}
                            </button>
                        `)}
                    </div>
                </div>

                <!-- Tab Content -->
                <div>
                    ${this.activeTab === 'overview' ? html`
                        <gable-product-overview-tab
                            .detail=${detail}
                            @open-margin-modal=${() => { this.isMarginModalOpen = true; }}
                        ></gable-product-overview-tab>
                    ` : nothing}
                    ${this.activeTab === 'content' ? html`
                        <gable-product-content-tab
                            .productId=${product.id}
                            .content=${detail.content}
                            @content-update=${(e: CustomEvent<PIMContent>) => this.handleContentUpdate(e.detail)}
                        ></gable-product-content-tab>
                    ` : nothing}
                    ${this.activeTab === 'media' ? html`
                        <gable-product-media-tab
                            .productId=${product.id}
                            .media=${detail.media}
                            @media-update=${() => this.handleMediaUpdate()}
                        ></gable-product-media-tab>
                    ` : nothing}
                    ${this.activeTab === 'collateral' ? html`
                        <gable-product-collateral-tab
                            .productId=${product.id}
                            .collateral=${detail.collateral}
                            @collateral-update=${() => this.handleCollateralUpdate()}
                        ></gable-product-collateral-tab>
                    ` : nothing}
                    ${this.activeTab === 'seo' ? html`
                        <gable-product-seo-tab
                            .productId=${product.id}
                            .content=${detail.content}
                            @content-update=${(e: CustomEvent<PIMContent>) => this.handleContentUpdate(e.detail)}
                        ></gable-product-seo-tab>
                    ` : nothing}
                    ${this.activeTab === 'stock' ? html`
                        <gable-product-stock-tab
                            .productId=${product.id}
                            .productDescription=${product.description}
                        ></gable-product-stock-tab>
                    ` : nothing}
                    ${this.activeTab === 'geometry' ? html`
                        <gable-product-geometry-tab
                            .productId=${product.id}
                            @dimensions-update=${() => { void this.loadProduct(); }}
                        ></gable-product-geometry-tab>
                    ` : nothing}
                </div>

                <gable-product-margin-modal
                    ?is-open=${this.isMarginModalOpen}
                    .product=${product}
                    @close=${() => { this.isMarginModalOpen = false; }}
                    @success=${() => { void this.loadProduct(); }}
                ></gable-product-margin-modal>
            </div>
        `;
    }
}
