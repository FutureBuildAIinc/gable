// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../../lib/icons.ts';
import { Save, Loader2, Trash2, Plus, Boxes } from 'lucide';
import { ProductService } from '../../../services/product.service.ts';
import { ApiError, apiErrorMessage } from '../../../services/apiError.ts';
import type { Product, KitComponent } from '../../../types/product.ts';
import { ToastService } from '../../../lib/toast-service.ts';
import { normalizeQuantity } from '../../../lib/money.ts';
import '../../../components/products/ProductSelect.ts';

/**
 * <gable-product-kit-tab> — the editor for a kit's definition (ADR 0005
 * section 2.6): what the kit holds, per one kit, in each part's own stocking
 * unit.
 *
 * The kit route replaces the whole list in one write, so the tab keeps the
 * list the dealer is editing and sends it whole. A product with a non empty
 * list is a kit; an empty list clears the definition and the product is a
 * plain product again.
 *
 * The refusals are the kit route's own, shown as sentences the dealer can act
 * on: a kit holds one level, so a kit cannot be a part of another kit and
 * cannot be a part of itself.
 */

/** One part of the kit as the tab is editing it. */
interface KitPart {
    component_product_id: string;
    sku: string;
    description: string;
    uom: string;
    /** Per one kit, in the part's stocking unit; a decimal string. */
    quantity: string;
}

/**
 * The kit route's refusal in plain words. The wire names the field and the
 * reason; the dealer needs the sentence.
 */
export function kitRefusalMessage(err: unknown): string {
    if (err instanceof ApiError) {
        for (const detail of err.details) {
            const text = detail.message;
            if (text.includes('cannot contain itself')) {
                return 'A kit cannot be a part of itself. Pick a different product for this part.';
            }
            if (text.includes('itself a kit')) {
                return 'A kit holds plain products only: it cannot hold another kit. Pick a plain product for this part.';
            }
            if (text.includes('lists a component once')) {
                return 'This kit already holds that product. Give that one part a quantity instead of listing it twice.';
            }
            if (text.includes('greater than zero')) {
                return 'A part needs a quantity of more than zero.';
            }
            if (text.includes('no such product')) {
                return 'That product is not in the catalogue any more.';
            }
        }
    }
    return apiErrorMessage(err, 'The kit could not be saved');
}

@customElement('gable-product-kit-tab')
export class GableProductKitTab extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: String }) productId = '';

    /** The parts as the dealer is editing them. */
    @state() private parts: KitPart[] = [];
    /** The product picked for a new part, waiting for its quantity. */
    @state() private picked: Product | null = null;
    @state() private pickedQuantity = '1';
    @state() private loading = true;
    @state() private saving = false;
    /** The refusal the kit route gave, in plain words. */
    @state() private refusal: string | null = null;
    /** The product's revision; the write names it (If-Match) and takes the next one from the answer. */
    private revision: number | null = null;
    /** The product the definition on screen belongs to, so a re-render does not read it twice. */
    private loadedFor = '';

    connectedCallback() {
        super.connectedCallback();
        void this.loadKit();
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('productId') && changed.get('productId') !== undefined) {
            void this.loadKit();
        }
    }

    private async loadKit() {
        if (!this.productId) {
            this.loading = false;
            return;
        }
        if (this.loadedFor === this.productId) return;
        this.loadedFor = this.productId;
        this.loading = true;
        try {
            const list = await ProductService.getKitComponents(this.productId);
            this.revision = list.revision;
            this.parts = list.components.map((c: KitComponent) => ({
                component_product_id: c.component_product_id,
                sku: c.sku,
                description: c.description,
                uom: c.uom,
                quantity: c.quantity,
            }));
        } catch (err) {
            console.error('Failed to load the kit components:', err);
            ToastService.show(apiErrorMessage(err, 'Failed to load the kit components'), 'error');
        } finally {
            this.loading = false;
        }
    }

    private handlePick(product: Product) {
        this.refusal = null;
        if (product.id === this.productId) {
            this.picked = null;
            this.refusal = 'A kit cannot be a part of itself. Pick a different product for this part.';
            return;
        }
        // A kit holds one level (ADR 0005 section 2.6): a kit cannot be a part
        // of another kit. The route refuses it too; saying so here spares the
        // dealer a save that would not land.
        if (product.is_kit) {
            this.picked = null;
            this.refusal = 'A kit holds plain products only: it cannot hold another kit. Pick a plain product for this part.';
            return;
        }
        if (this.parts.some(p => p.component_product_id === product.id)) {
            this.picked = null;
            this.refusal = 'This kit already holds that product. Give that one part a quantity instead of listing it twice.';
            return;
        }
        this.picked = product;
    }

    private addPart() {
        if (!this.picked) return;
        const quantity = normalizeQuantity(this.pickedQuantity);
        if (quantity === null) {
            this.refusal = 'A part needs a quantity of more than zero.';
            return;
        }
        this.refusal = null;
        this.parts = [...this.parts, {
            component_product_id: this.picked.id,
            sku: this.picked.sku,
            description: this.picked.description,
            uom: this.picked.stock_uom,
            quantity,
        }];
        this.picked = null;
        this.pickedQuantity = '1';
    }

    private setQuantity(index: number, raw: string) {
        const quantity = normalizeQuantity(raw);
        if (quantity === null) {
            this.refusal = 'A part needs a quantity of more than zero.';
            return;
        }
        this.refusal = null;
        this.parts = this.parts.map((p, i) => (i === index ? { ...p, quantity } : p));
    }

    private removePart(index: number) {
        this.refusal = null;
        this.parts = this.parts.filter((_, i) => i !== index);
    }

    private async saveKit() {
        if (!this.productId || this.revision === null) return;
        this.saving = true;
        this.refusal = null;
        try {
            const saved = await ProductService.putKitComponents(
                this.productId,
                this.parts.map(p => ({ component_product_id: p.component_product_id, quantity: p.quantity })),
                this.revision,
            );
            this.revision = saved.revision;
            this.parts = saved.components.map((c: KitComponent) => ({
                component_product_id: c.component_product_id,
                sku: c.sku,
                description: c.description,
                uom: c.uom,
                quantity: c.quantity,
            }));
            ToastService.show(
                this.parts.length > 0 ? 'Kit saved' : 'Kit cleared: this product is a plain product again',
                'success',
            );
            this.dispatchEvent(new CustomEvent('kit-update', { bubbles: true, composed: true }));
        } catch (err) {
            console.error('Failed to save the kit:', err);
            this.refusal = kitRefusalMessage(err);
        } finally {
            this.saving = false;
        }
    }

    render() {
        if (this.loading) {
            return html`<div class="p-8 text-zinc-400 flex items-center gap-2">
                ${icon(Loader2, 16, 'w-4 h-4 animate-spin')} Loading the kit...
            </div>`;
        }

        return html`
            <div class="space-y-5">
                <div class="bg-zinc-900 border border-white/10 rounded-xl p-5">
                    <h3 class="text-sm font-medium text-zinc-300 flex items-center gap-2 mb-2">
                        ${icon(Boxes, 16, 'w-4 h-4 text-gable-green')}
                        Kit components
                    </h3>
                    <p class="text-xs text-zinc-500 mb-4">
                        What this product holds, per one kit, in each part's own stocking unit. A line
                        that names this product on a quote or an order becomes one kit line and one
                        line per part, and the dealer carries the cost of the parts.
                        ${this.parts.length === 0
                            ? 'This product is a plain product: give it a part to make it a kit.'
                            : ''}
                    </p>

                    ${this.parts.length === 0 ? html`
                        <p class="text-sm text-zinc-500 italic" data-testid="kit-empty">
                            No parts yet. This product is not a kit.
                        </p>
                    ` : html`
                        <table class="w-full text-sm text-left" data-testid="kit-parts">
                            <thead class="bg-white/5 text-zinc-400 uppercase tracking-wider text-xs font-semibold">
                                <tr>
                                    <th class="px-3 py-2">Part</th>
                                    <th class="px-3 py-2">Qty per kit</th>
                                    <th class="px-3 py-2"><span class="sr-only">Remove</span></th>
                                </tr>
                            </thead>
                            <tbody class="divide-y divide-white/5">
                                ${this.parts.map((part, index) => html`
                                    <tr data-testid="kit-part">
                                        <td class="px-3 py-2">
                                            <div class="font-mono text-white">${part.sku}</div>
                                            <div class="text-xs text-zinc-400">${part.description}</div>
                                        </td>
                                        <td class="px-3 py-2">
                                            <input
                                                type="text"
                                                inputmode="decimal"
                                                aria-label=${`Quantity of ${part.sku} per kit`}
                                                data-testid="kit-part-quantity"
                                                .value=${part.quantity}
                                                @change=${(e: Event) => this.setQuantity(index, (e.target as HTMLInputElement).value)}
                                                class="w-20 bg-deep-space border border-white/10 rounded px-2 py-1.5 text-right text-zinc-200 font-mono text-sm outline-none focus:border-gable-green"
                                            />
                                            <span class="text-xs text-zinc-500 ml-2">${part.uom}</span>
                                        </td>
                                        <td class="px-3 py-2 text-right">
                                            <button
                                                type="button"
                                                aria-label=${`Remove ${part.sku}`}
                                                data-testid="kit-part-remove"
                                                @click=${() => this.removePart(index)}
                                                class="p-1.5 rounded text-zinc-500 hover:text-rose-400 hover:bg-white/5"
                                            >${icon(Trash2, 14)}</button>
                                        </td>
                                    </tr>
                                `)}
                            </tbody>
                        </table>
                    `}
                </div>

                <div class="bg-zinc-900 border border-white/10 rounded-xl p-5">
                    <h4 class="text-sm font-medium text-zinc-300 mb-3">Add a part</h4>
                    <div class="grid grid-cols-1 md:grid-cols-3 gap-4 items-end">
                        <div class="md:col-span-2">
                            <gable-product-select
                                @product-select=${(e: CustomEvent<Product>) => this.handlePick(e.detail)}
                            ></gable-product-select>
                        </div>
                        <div>
                            <label class="block text-xs text-zinc-500 mb-1" for="kit-part-quantity-new">Qty per kit</label>
                            <input
                                id="kit-part-quantity-new"
                                type="text"
                                inputmode="decimal"
                                aria-label="Quantity per kit"
                                data-testid="kit-new-quantity"
                                .value=${this.pickedQuantity}
                                @input=${(e: Event) => { this.pickedQuantity = (e.target as HTMLInputElement).value; }}
                                class="w-full bg-deep-space border border-white/10 rounded px-2 py-1.5 text-right text-zinc-200 font-mono text-sm outline-none focus:border-gable-green"
                            />
                        </div>
                    </div>
                    <div class="mt-4 flex items-center gap-3">
                        <button
                            type="button"
                            data-testid="kit-add-part"
                            ?disabled=${!this.picked}
                            @click=${() => this.addPart()}
                            class="inline-flex items-center gap-2 px-4 py-2 rounded-lg border border-white/10 text-zinc-200 text-sm hover:bg-white/5 transition-colors disabled:opacity-50"
                        >
                            ${icon(Plus, 16, 'w-4 h-4')} Add part
                        </button>
                        <button
                            type="button"
                            data-testid="kit-save"
                            ?disabled=${this.saving}
                            @click=${() => void this.saveKit()}
                            class="inline-flex items-center gap-2 px-5 py-2 rounded-lg bg-gable-green/20 text-gable-green border border-gable-green/30 hover:bg-gable-green/30 transition-colors disabled:opacity-50"
                        >
                            ${this.saving ? icon(Loader2, 16, 'w-4 h-4 animate-spin') : icon(Save, 16, 'w-4 h-4')}
                            ${this.saving ? 'Saving...' : 'Save kit'}
                        </button>
                    </div>
                </div>

                ${this.refusal ? html`
                    <div role="alert" data-testid="kit-refusal" class="flex items-start gap-3 bg-rose-500/10 border border-rose-500/20 text-rose-400 text-sm p-3 rounded-lg">
                        ${icon(Boxes, 16, 'w-4 h-4 shrink-0 mt-0.5')}
                        <p>${this.refusal}</p>
                    </div>
                ` : nothing}
            </div>
        `;
    }
}
