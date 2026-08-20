// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../../lib/icons.ts';
import { Save, Loader2, Box, Eraser } from 'lucide';
import { ProductService } from '../../../services/product.service.ts';
import type { ProductGeometry } from '../../../services/product.service.ts';
import { ToastService } from '../../../lib/toast-service.ts';

/**
 * <gable-product-geometry-tab> — the PIM editor for a product's canonical
 * parametric 3D geometry.
 *
 * NULLABILITY IS THE FEATURE. Every field is tri-state: a blank length input
 * and an "Unknown" stackable option mean "nobody has recorded this", which is
 * stored as SQL NULL and surfaced to AI_LM's Load Builder as JSON `null`. The
 * planner falls back to its own override table for null; a 0 would instead be
 * taken as a genuine zero-volume box, and `false` as a genuine "do not stack".
 * So: never coerce a blank input to 0, and never default the toggle. The
 * scary-looking `=== null` handling below is load-bearing.
 *
 * The preview is a scale drawing rather than a WebGL twin — the elevation view
 * is enough to catch a transposed length/width, and it keeps the ERP bundle
 * free of a 3D engine.
 */
@customElement('gable-product-geometry-tab')
export class GableProductGeometryTab extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: String }) productId = '';

    @state() private lengthIn: number | null = null;
    @state() private widthIn: number | null = null;
    @state() private heightIn: number | null = null;
    @state() private stackable: boolean | null = null;
    @state() private geometrySource: string | null = null;
    @state() private loading = true;
    @state() private saving = false;

    connectedCallback() {
        super.connectedCallback();
        this._loadGeometry();
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('productId') && changed.get('productId') !== undefined) {
            this.loading = true;
            this._loadGeometry();
        }
    }

    private async _loadGeometry() {
        if (!this.productId) {
            this.loading = false;
            return;
        }
        try {
            const product = await ProductService.getProduct(this.productId);
            this._apply({
                length_in: product.length_in ?? null,
                width_in: product.width_in ?? null,
                height_in: product.height_in ?? null,
                stackable: product.stackable ?? null,
                geometry_source: product.geometry_source ?? null,
            });
        } catch (err) {
            console.error('Failed to load product geometry:', err);
            ToastService.show('Failed to load product geometry', 'error');
        } finally {
            this.loading = false;
        }
    }

    private _apply(g: ProductGeometry) {
        this.lengthIn = g.length_in;
        this.widthIn = g.width_in;
        this.heightIn = g.height_in;
        this.stackable = g.stackable;
        this.geometrySource = g.geometry_source ?? null;
    }

    /**
     * Parses a dimension input. A blank field is `null` ("not recorded"), NOT
     * 0. A typed `0` is a real measurement and is kept as 0.
     */
    private _parse(raw: string): number | null {
        if (raw.trim() === '') return null;
        const n = Number(raw);
        return Number.isFinite(n) ? n : null;
    }

    private _hasDimensions(): boolean {
        return this.lengthIn !== null || this.widthIn !== null || this.heightIn !== null;
    }

    /** The exact payload sent to PATCH /api/v1/products/{id}/dimensions. */
    private _payload(): ProductGeometry {
        return {
            length_in: this.lengthIn,
            width_in: this.widthIn,
            height_in: this.heightIn,
            stackable: this.stackable,
            // Only an operator-entered triple has a provenance. When every
            // dimension is blank the source is cleared too, so the catalog
            // never claims 'parametric' geometry that does not exist.
            geometry_source: this._hasDimensions()
                ? (this.geometrySource || 'parametric')
                : null,
        };
    }

    private async _handleSave() {
        if (!this.productId) return;
        this.saving = true;
        try {
            const saved = await ProductService.updateDimensions(this.productId, this._payload());
            this._apply(saved);
            ToastService.show(
                this._hasDimensions() ? 'Dimensions saved' : 'Geometry cleared',
                'success',
            );
            this.dispatchEvent(new CustomEvent('dimensions-update', { bubbles: true, composed: true }));
        } catch (err) {
            console.error('Save dimensions failed:', err);
            ToastService.show('Failed to save dimensions', 'error');
        } finally {
            this.saving = false;
        }
    }

    private _handleClear() {
        this.lengthIn = null;
        this.widthIn = null;
        this.heightIn = null;
        this.stackable = null;
    }

    private _renderNumberInput(label: string, value: number | null, onChange: (v: number | null) => void) {
        return html`
            <div>
                <label class="block text-xs text-zinc-500 mb-1">${label} <span class="text-zinc-600">(in)</span></label>
                <input
                    type="number"
                    min="0"
                    step="0.25"
                    aria-label=${label}
                    .value=${value === null ? '' : String(value)}
                    @input=${(e: InputEvent) => onChange(this._parse((e.target as HTMLInputElement).value))}
                    class="w-full bg-zinc-800 border border-white/10 rounded-lg px-3 py-2 text-sm text-white font-mono placeholder-zinc-600 focus:outline-none focus:border-gable-green/50"
                    placeholder="—"
                />
                ${value === null ? html`<p class="text-[10px] text-zinc-600 mt-1">Not recorded</p>` : nothing}
            </div>
        `;
    }

    /**
     * A proportional elevation (length x height) drawing. Rendered only when
     * both axes are recorded and positive — a zero or missing dimension has
     * nothing meaningful to draw, and inventing a shape would hide exactly the
     * data gap this tab exists to close.
     */
    private _renderPreview() {
        const l = this.lengthIn;
        const h = this.heightIn;
        if (l === null || h === null || l <= 0 || h <= 0) {
            return html`
                <div
                    class="h-56 rounded-xl border border-dashed border-white/10 bg-black/20 flex items-center justify-center text-center px-6"
                    data-testid="geometry-preview-empty"
                >
                    <p class="text-xs text-zinc-500">
                        No scale preview: this SKU has no recorded length and height.
                        Downstream load planning treats it as unknown geometry.
                    </p>
                </div>
            `;
        }

        const boxW = (l / Math.max(l, h)) * 220;
        const boxH = (h / Math.max(l, h)) * 220;
        return html`
            <div class="h-56 rounded-xl border border-white/10 bg-black/20 flex items-center justify-center" data-testid="geometry-preview">
                <svg viewBox="0 0 260 260" class="h-52 w-52" role="img" aria-label="Scale elevation of the product">
                    <rect
                        x=${(260 - boxW) / 2}
                        y=${(260 - boxH) / 2}
                        width=${boxW}
                        height=${boxH}
                        rx="2"
                        fill="rgb(132 204 22 / 0.15)"
                        stroke="rgb(132 204 22 / 0.7)"
                        stroke-width="1.5"
                    />
                    <text x="130" y="248" text-anchor="middle" fill="#a1a1aa" font-size="11">${l}" long</text>
                    <text x="8" y="130" fill="#a1a1aa" font-size="11">${h}" high</text>
                </svg>
            </div>
        `;
    }

    render() {
        if (this.loading) {
            return html`<div class="p-8 text-zinc-400 flex items-center gap-2">
                ${icon(Loader2, 16, 'w-4 h-4 animate-spin')} Loading geometry...
            </div>`;
        }

        return html`
            <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
                <div class="space-y-5">
                    <div class="bg-zinc-900 border border-white/10 rounded-xl p-5">
                        <h3 class="text-sm font-medium text-zinc-300 flex items-center gap-2 mb-4">
                            ${icon(Box, 16, 'w-4 h-4 text-gable-green')}
                            Parametric Dimensions
                        </h3>
                        <p class="text-xs text-zinc-500 mb-4">
                            The PIM is the canonical source of per-product geometry. These dimensions are
                            published to the load-planning integration so each product can be packed as a
                            scaled digital twin. Leave a field blank when the measurement is genuinely
                            unknown — blank is recorded as "no data", which is treated differently from a
                            measured zero.
                        </p>
                        <div class="grid grid-cols-3 gap-3">
                            ${this._renderNumberInput('Length', this.lengthIn, (v) => this.lengthIn = v)}
                            ${this._renderNumberInput('Width', this.widthIn, (v) => this.widthIn = v)}
                            ${this._renderNumberInput('Height', this.heightIn, (v) => this.heightIn = v)}
                        </div>

                        <div class="mt-5">
                            <label class="block text-xs text-zinc-500 mb-1" for="geometry-stackable">Stackable in a load</label>
                            <select
                                id="geometry-stackable"
                                aria-label="Stackable in a load"
                                .value=${this.stackable === null ? '' : String(this.stackable)}
                                @change=${(e: Event) => {
                                    const v = (e.target as HTMLSelectElement).value;
                                    this.stackable = v === '' ? null : v === 'true';
                                }}
                                class="w-full bg-zinc-800 border border-white/10 rounded-lg px-3 py-2 text-sm text-white focus:outline-none focus:border-gable-green/50"
                            >
                                <option value="">Unknown — not recorded</option>
                                <option value="true">Yes, other cargo may stack on it</option>
                                <option value="false">No, keep the top clear</option>
                            </select>
                        </div>

                        <p class="text-[11px] text-zinc-600 mt-4">
                            Provenance:
                            <span class="font-mono text-zinc-400">${this.geometrySource ?? 'none recorded'}</span>
                        </p>
                    </div>

                    <div class="flex justify-between items-center">
                        <button
                            @click=${this._handleClear}
                            ?disabled=${this.saving}
                            class="flex items-center gap-2 px-4 py-2.5 text-zinc-400 border border-white/10 rounded-lg hover:bg-white/5 transition-colors disabled:opacity-50"
                        >
                            ${icon(Eraser, 16, 'w-4 h-4')}
                            Clear geometry
                        </button>
                        <button
                            @click=${this._handleSave}
                            ?disabled=${this.saving}
                            class="flex items-center gap-2 px-5 py-2.5 bg-gable-green/20 text-gable-green border border-gable-green/30 rounded-lg hover:bg-gable-green/30 transition-colors disabled:opacity-50"
                        >
                            ${this.saving ? icon(Loader2, 16, 'w-4 h-4 animate-spin') : icon(Save, 16, 'w-4 h-4')}
                            ${this.saving ? 'Saving...' : 'Save Dimensions'}
                        </button>
                    </div>
                </div>

                <div>
                    <h3 class="text-sm font-medium text-zinc-300 mb-3">Scale Preview</h3>
                    ${this._renderPreview()}
                </div>
            </div>
        `;
    }
}
