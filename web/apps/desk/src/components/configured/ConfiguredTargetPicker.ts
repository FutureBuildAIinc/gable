// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { QuoteService } from '../../services/QuoteService.ts';
import { OrderService } from '../../services/OrderService.ts';

/**
 * Where a configured item goes: the dealer picks the draft quote or the
 * editable order it belongs on, or starts a new one.
 *
 * The picker never writes the item itself. It says which document was picked
 * and the configurator that opened it hands the item over and walks the
 * dealer to that document, where the item lands on the line.
 */
export interface ConfiguredTarget {
    /** The document's id, or null when the dealer starts a new one. */
    id: string | null;
}

export const CONFIGURED_TARGET_PICKED = 'configured-target-picked';

@customElement('gable-configured-target-picker')
export class GableConfiguredTargetPicker extends LitElement {
    createRenderRoot() { return this; }

    /** Which document the item is headed for. */
    @property() target: 'quote' | 'order' = 'quote';

    @state() private drafts: { id: string; label: string; detail: string }[] = [];
    @state() private loading = false;
    @state() private failed = false;

    connectedCallback() {
        super.connectedCallback();
        void this.loadDrafts();
    }

    private async loadDrafts() {
        this.loading = true;
        this.failed = false;
        try {
            if (this.target === 'quote') {
                const page = await QuoteService.list({ status: 'DRAFT', limit: 50 });
                this.drafts = page.items.map((q) => ({
                    id: q.id,
                    label: q.number ?? 'Draft quote',
                    detail: q.customer_name ?? '',
                }));
            } else {
                const page = await OrderService.listOrders({ status: 'DRAFT', limit: 50 });
                this.drafts = page.items.map((o) => ({
                    id: o.id,
                    label: o.number ?? 'Draft order',
                    detail: o.customer_name ?? '',
                }));
            }
        } catch {
            this.failed = true;
            this.drafts = [];
        } finally {
            this.loading = false;
        }
    }

    private pick(id: string | null) {
        this.dispatchEvent(new CustomEvent<ConfiguredTarget>(CONFIGURED_TARGET_PICKED, {
            detail: { id },
            bubbles: true,
            composed: true,
        }));
    }

    render() {
        const isQuote = this.target === 'quote';
        return html`
            <div
                class="fixed inset-0 z-50 flex items-center justify-center bg-backdrop/80 backdrop-blur-sm p-4"
                data-testid="configured-target-picker"
                @click=${(e: Event) => { if (e.target === e.currentTarget) this.pick(null); }}
            >
                <div class="w-full max-w-md rounded-xl border border-white/10 bg-deep-space shadow-2xl">
                    <div class="flex items-center justify-between border-b border-white/10 px-4 py-3">
                        <h2 class="text-sm font-medium text-white">
                            ${isQuote ? 'Add the item to a quote' : 'Add the item to an order'}
                        </h2>
                        <button
                            @click=${() => this.pick(null)}
                            data-testid="close-target-picker"
                            class="rounded px-2 py-1 text-xs text-zinc-400 hover:bg-white/5 hover:text-white"
                        >
                            Close
                        </button>
                    </div>
                    <div class="max-h-[60vh] overflow-auto p-4">
                        ${this.loading
                            ? html`<p class="text-sm text-zinc-400">Loading...</p>`
                            : this.failed
                                ? html`<p class="text-sm text-red-400">The drafts could not be read.</p>`
                                : this.drafts.length === 0
                                    ? html`<p class="text-sm text-zinc-400" data-testid="no-drafts">
                                        No draft ${isQuote ? 'quote' : 'order'} yet.
                                    </p>`
                                    : html`<ul class="space-y-2">
                                        ${this.drafts.map((d) => html`
                                            <li>
                                                <button
                                                    @click=${() => this.pick(d.id)}
                                                    data-testid="target-draft"
                                                    class="w-full rounded-lg border border-white/10 bg-white/5 px-3 py-2 text-left hover:border-gable-green/40"
                                                >
                                                    <span class="block text-sm text-white">${d.label}</span>
                                                    ${d.detail ? html`<span class="block text-xs text-zinc-400">${d.detail}</span>` : nothing}
                                                </button>
                                            </li>
                                        `)}
                                    </ul>`}
                    </div>
                    <div class="border-t border-white/10 px-4 py-3">
                        <button
                            @click=${() => this.pick(null)}
                            data-testid="new-draft"
                            class="w-full rounded-lg bg-gradient-to-r from-gable-green to-emerald-400 px-4 py-2 text-sm font-bold text-black"
                        >
                            ${isQuote ? 'Create New Draft Quote' : 'Start from a quote'}
                        </button>
                        ${isQuote
                            ? nothing
                            : html`<p class="mt-2 text-center text-xs text-zinc-500">
                                An order is made by converting a quote.
                            </p>`}
                    </div>
                </div>
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-configured-target-picker': GableConfiguredTargetPicker;
    }
}
