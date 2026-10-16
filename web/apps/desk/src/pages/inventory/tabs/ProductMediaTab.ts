// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import '../../../components/ui/gable-select.ts';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../../lib/icons.ts';
import { Sparkles, Trash2, Star, Loader2, Image as ImageIcon, AlertTriangle } from 'lucide';
import { PIMService } from '../../../services/PIMService.ts';
import { ToastService } from '../../../lib/toast-service.ts';
import type { PIMMedia } from '../../../types/pim.ts';
import { techAdminService } from '../../../services/TechAdminService';
import { imageWords, type AIProvider } from '../../../lib/aiWords.ts';
import { renderAiNotice } from '../../../components/inventory/PimAiNotice.ts';

const STYLES = ['', 'photographic', 'digital-art', 'cinematic', '3d-model', 'isometric'];

@customElement('gable-product-media-tab')
export class GableProductMediaTab extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: String }) productId = '';
    @property({ attribute: false }) media: PIMMedia[] = [];

    @state() private _imageStyle = '';
    @state() private prompt = '';
    @state() private generating = false;
    @state() private error: string | null = null;
    // The chosen AI provider decides what the button makes: a photo with
    // OpenRouter, an illustration drawn by Claude with Anthropic.
    @state() private provider: AIProvider | null = null;

    connectedCallback() {
        super.connectedCallback();
        techAdminService.getAISettings()
            .then(s => { this.provider = s.provider; })
            .catch(() => { this.provider = null; });
    }

    private async _handleGenerate() {
        this.generating = true;
        this.error = null;
        try {
            // Returns 202 immediately with a placeholder row (status "generating").
            await PIMService.generateImage(this.productId, { style: this._imageStyle, prompt: this.prompt });
            // The image is produced in the background — poll until the row settles.
            await this._pollUntilSettled();
        } catch (err: unknown) {
            const msg = err instanceof Error ? err.message : imageWords(this.provider).failed;
            this.error = msg;
        } finally {
            this.generating = false;
            // Final authoritative sync with the parent (re-fetches product detail).
            this.dispatchEvent(new CustomEvent('media-update', { bubbles: true, composed: true }));
        }
    }

    // _pollUntilSettled refreshes the media list until no row is still "generating"
    // (or a safety deadline), driving the gallery render off the latest server state.
    private async _pollUntilSettled() {
        const deadline = Date.now() + 120_000;
        while (Date.now() < deadline) {
            const media = await PIMService.listMedia(this.productId);
            this.media = media;
            if (!media.some(m => m.status === 'generating')) {
                if (media.some(m => m.status === 'failed')) {
                    this.error = imageWords(this.provider).failed;
                }
                return;
            }
            await new Promise(r => setTimeout(r, 2500));
        }
    }

    /** The refusal notice stands only while the newest attempt is the refused one. */
    private _newestRefused(): boolean {
        const settled = this.media.filter(m => m.status !== 'generating');
        if (settled.length === 0) return false;
        // Compare the moments: timestamps that fall on a whole second omit the fraction,
        // and their text sorts after a later moment that carries one.
        const at = (m: PIMMedia) => Date.parse(m.created_at) || 0;
        const newest = settled.reduce((a, b) => (at(b) > at(a) ? b : a));
        return newest.status === 'refused';
    }

    private async _handleDelete(mediaId: string) {
        try {
            await PIMService.deleteMedia(this.productId, mediaId);
            this.dispatchEvent(new CustomEvent('media-update', { bubbles: true, composed: true }));
        } catch (err) {
            console.error('Delete failed:', err);
            ToastService.show('Failed to delete media', 'error');
        }
    }

    private async _handleSetPrimary(mediaId: string) {
        try {
            await PIMService.setPrimaryMedia(this.productId, mediaId);
            this.dispatchEvent(new CustomEvent('media-update', { bubbles: true, composed: true }));
        } catch (err) {
            console.error('Set primary failed:', err);
            ToastService.show('Failed to set primary media', 'error');
        }
    }

    render() {
        const words = imageWords(this.provider);
        return html`
            <div class="space-y-6">
                ${renderAiNotice(this._newestRefused() ? words.refused : null)}
                <!-- Generation Controls -->
                <div class="bg-zinc-900 border border-white/10 rounded-xl p-5">
                    <h3 class="text-sm font-medium text-zinc-300 flex items-center gap-2 mb-4">
                        ${icon(Sparkles, 16, 'w-4 h-4 text-amber-400')}
                        ${words.heading}
                    </h3>
                    <div class="space-y-3">
                        <div class="flex flex-wrap items-end gap-4">
                            <div>
                                <label class="block text-xs text-zinc-500 mb-1">Style</label>
                                <gable-select
                                    .value=${this._imageStyle}
                                    .options=${STYLES.map(s => ({ value: s, label: s ? s.charAt(0).toUpperCase() + s.slice(1).replace('-', ' ') : 'Auto' }))}
                                    @change=${(e: Event) => this._imageStyle = (e.target as HTMLSelectElement).value}
                                ></gable-select>
                            </div>
                            <button
                                @click=${this._handleGenerate}
                                ?disabled=${this.generating}
                                class="flex items-center gap-2 px-4 py-2 bg-amber-500/20 text-amber-400 border border-amber-500/30 rounded-lg hover:bg-amber-500/30 transition-colors disabled:opacity-50"
                            >
                                ${this.generating ? icon(Loader2, 16, 'w-4 h-4 animate-spin') : icon(Sparkles, 16, 'w-4 h-4')}
                                ${this.generating ? words.busy : words.button}
                            </button>
                        </div>
                        <div>
                            <label class="block text-xs text-zinc-500 mb-1">Custom Prompt (optional)</label>
                            <input
                                type="text"
                                .value=${this.prompt}
                                @input=${(e: InputEvent) => this.prompt = (e.target as HTMLInputElement).value}
                                placeholder="Leave empty for auto-generated prompt based on product data..."
                                class="w-full bg-zinc-800 border border-white/10 rounded-lg px-3 py-2 text-sm text-white placeholder-zinc-600 focus:outline-none focus:border-gable-green/50"
                            />
                        </div>
                        ${this.error ? html`
                            <div class="flex items-start gap-2 p-3 bg-rose-500/10 border border-rose-500/20 rounded-lg text-sm text-rose-400">
                                ${icon(AlertTriangle, 16, 'w-4 h-4 mt-0.5 shrink-0')}
                                <span>${this.error}</span>
                            </div>
                        ` : nothing}
                    </div>
                </div>

                <!-- Gallery -->
                ${this.media.length === 0
                    ? html`
                        <div class="bg-zinc-900 border border-white/10 rounded-xl p-12 text-center">
                            ${icon(ImageIcon, 48, 'w-12 h-12 text-zinc-600 mx-auto mb-3')}
                            <p class="text-zinc-500 text-sm">${words.empty}</p>
                        </div>
                    `
                    : html`
                        <div class="grid grid-cols-2 md:grid-cols-3 gap-4">
                            ${this.media.map(m => html`
                                <div class="relative group bg-zinc-900 border rounded-xl overflow-hidden ${m.is_primary ? 'border-gable-green/50 ring-1 ring-gable-green/30' : 'border-white/10'}">
                                    <div class="aspect-square flex items-center justify-center">
                                        ${m.status === 'generating'
                                            ? html`<div class="flex flex-col items-center gap-2 text-zinc-500">
                                                ${icon(Loader2, 28, 'w-7 h-7 animate-spin text-amber-400')}
                                                <span class="text-xs">Generating…</span>
                                            </div>`
                                            : m.status === 'failed' || m.status === 'refused'
                                            ? html`<div class="flex flex-col items-center gap-2 text-rose-400">
                                                ${icon(AlertTriangle, 28, 'w-7 h-7')}
                                                <span class="text-xs">${m.status === 'refused' ? 'Key refused' : 'Generation failed'}</span>
                                            </div>`
                                            : html`<img src="${m.url}" alt="${m.alt_text}" class="w-full h-full object-cover" />`
                                        }
                                    </div>
                                    ${m.is_primary ? html`
                                        <div class="absolute top-2 left-2 px-2 py-0.5 bg-gable-green/80 text-black text-xs font-bold rounded">Primary</div>
                                    ` : nothing}
                                    <div class="absolute top-2 right-2 px-2 py-0.5 bg-chip-ground/60 text-zinc-300 text-xs rounded">${m.media_type}</div>
                                    <!-- Hover Actions (settled rows only) -->
                                    ${m.status !== 'generating' ? html`
                                    <div class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-backdrop/80 to-transparent p-3 opacity-0 group-hover:opacity-100 transition-opacity">
                                        <div class="flex items-center justify-end gap-2">
                                            ${!m.is_primary && m.status !== 'failed' && m.status !== 'refused' ? html`
                                                <button @click=${() => this._handleSetPrimary(m.id)} class="p-1.5 rounded-md bg-photo-btn/10 hover:bg-photo-btn/20 text-amber-400" title="Set as Primary">
                                                    ${icon(Star, 16, 'w-4 h-4')}
                                                </button>
                                            ` : nothing}
                                            <button @click=${() => this._handleDelete(m.id)} class="p-1.5 rounded-md bg-photo-btn/10 hover:bg-rose-500/30 text-rose-400" title="Delete">
                                                ${icon(Trash2, 16, 'w-4 h-4')}
                                            </button>
                                        </div>
                                    </div>
                                    ` : nothing}
                                </div>
                            `)}
                        </div>
                    `
                }
            </div>
        `;
    }
}
