// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import type { MillworkOption } from '../../types/millwork';
import { MillworkService } from '../../services/MillworkService';
import { ToastService } from '../../lib/toast-service.ts';

/**
 * The option catalog the dealer maintains.
 *
 * A configured item is priced by the server from this catalog, so what the
 * dealer changes here is what every configured door and every configured
 * board costs. The money is edited in dollars and sent as the integer cents
 * the wire carries; the write needs the revision the dealer read, so two
 * dealers editing the same option cannot silently overwrite each other.
 */

/** The categories the dealer maintains, in the order the page shows them. */
const CATEGORIES: { key: string; label: string }[] = [
    { key: 'door_type', label: 'Door type' },
    { key: 'material', label: 'Material' },
    { key: 'glass', label: 'Glass' },
    { key: 'jamb_depth', label: 'Jamb depth' },
    { key: 'handing', label: 'Handing' },
    { key: 'hinge_finish', label: 'Hinge finish' },
    { key: 'frame_type', label: 'Frame type' },
    { key: 'species', label: 'Species' },
    { key: 'grade', label: 'Grade' },
    { key: 'treatment', label: 'Treatment' },
    { key: 'dimensions', label: 'Dimensions' },
];

/** The money one row shows, in cents. */
interface OptionMoney {
    basePriceCents: number;
    baseCostCents: number;
    priceAdjustmentCents: number;
    costAdjustmentCents: number;
}

@customElement('gable-millwork-options')
export class MillworkOptions extends LitElement {
    createRenderRoot() { return this; }

    @state() private _category = 'door_type';
    @state() private _options: MillworkOption[] = [];
    @state() private _edits: Record<string, OptionMoney> = {};
    @state() private _loading = true;
    @state() private _saving = false;
    @state() private _error: string | null = null;

    connectedCallback() {
        super.connectedCallback();
        void this._load();
    }

    private async _load() {
        this._loading = true;
        this._error = null;
        try {
            this._options = await MillworkService.getOptionsByCategory(this._category);
            this._edits = {};
        } catch (err) {
            this._error = err instanceof Error ? err.message : 'Failed to load the option catalog';
        } finally {
            this._loading = false;
        }
    }

    private async _selectCategory(key: string) {
        this._category = key;
        await this._load();
    }

    /** The money the row shows: the edit the dealer typed, or the stored one. */
    private _money(option: MillworkOption): OptionMoney {
        const edited = this._edits[option.id];
        if (edited) return edited;
        return {
            basePriceCents: option.base_price_cents,
            baseCostCents: option.base_cost_cents,
            priceAdjustmentCents: option.price_adjustment_cents,
            costAdjustmentCents: option.cost_adjustment_cents,
        };
    }

    private _edit(option: MillworkOption, patch: Partial<OptionMoney>) {
        this._edits = { ...this._edits, [option.id]: { ...this._money(option), ...patch } };
    }

    /** The dollars the dealer typed, as the cents the wire carries. */
    private _toCents(value: string): number | null {
        const trimmed = value.trim();
        if (trimmed === '') return 0;
        const parsed = Number(trimmed);
        if (!Number.isFinite(parsed)) return null;
        return Math.round(parsed * 100);
    }

    private async _save(option: MillworkOption) {
        const money = this._money(option);
        this._saving = true;
        try {
            const updated = await MillworkService.updateOption(option.id, {
                category: option.category,
                name: option.name,
                price_adjustment_cents: money.priceAdjustmentCents,
                base_price_cents: money.basePriceCents,
                base_cost_cents: money.baseCostCents,
                cost_adjustment_cents: money.costAdjustmentCents,
                attributes: (option.attributes ?? undefined) as Record<string, unknown> | undefined,
                revision: option.revision,
            });
            this._options = this._options.map((o) => (o.id === updated.id ? updated : o));
            const { [option.id]: _gone, ...rest } = this._edits;
            this._edits = rest;
            ToastService.success(`${option.name} saved`);
        } catch (err) {
            ToastService.error(err instanceof Error ? err.message : 'Failed to save the option');
        } finally {
            this._saving = false;
        }
    }

    /** The cents as the dealer reads them: dollars with two decimals. */
    private _dollars(cents: number): string {
        return (cents / 100).toFixed(2);
    }

    render() {
        return html`
            <div class="min-h-full bg-deep-space text-pale p-6">
                <header class="mb-6">
                    <h1 class="text-2xl font-bold text-gable-green">Option catalog</h1>
                    <p class="mt-1 text-sm text-zinc-400">
                        A configured item is priced on the server from these options: the price is the
                        base of the option that names the item plus the adjustments of the options it
                        selects.
                    </p>
                </header>

                <nav class="mb-5 flex flex-wrap gap-2" aria-label="Option categories">
                    ${CATEGORIES.map(c => html`
                        <button
                            @click=${() => this._selectCategory(c.key)}
                            data-testid="option-category-${c.key}"
                            class="rounded-lg border px-3 py-1.5 text-xs font-medium ${c.key === this._category
                                ? 'border-gable-green/50 bg-gable-green/10 text-gable-green'
                                : 'border-white/10 text-zinc-400 hover:bg-white/5 hover:text-white'}"
                        >
                            ${c.label}
                        </button>
                    `)}
                </nav>

                ${this._error ? html`
                    <p class="rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-300">${this._error}</p>
                ` : nothing}

                ${this._loading ? html`
                    <p class="text-sm text-zinc-500">Loading the catalog...</p>
                ` : html`
                    <div class="overflow-x-auto rounded-xl border border-white/10">
                        <table class="w-full text-sm text-left">
                            <thead class="bg-white/5 text-zinc-400 uppercase tracking-wider text-xs font-semibold">
                                <tr>
                                    <th class="px-4 py-3">Option</th>
                                    <th class="px-3 py-3 text-right">Base price</th>
                                    <th class="px-3 py-3 text-right">Base cost</th>
                                    <th class="px-3 py-3 text-right">Price adjustment</th>
                                    <th class="px-3 py-3 text-right">Cost adjustment</th>
                                    <th class="px-3 py-3"></th>
                                </tr>
                            </thead>
                            <tbody class="divide-y divide-white/5">
                                ${this._options.length === 0 ? html`
                                    <tr><td colspan="6" class="px-6 py-8 text-center text-zinc-500 italic">No option in this category yet.</td></tr>
                                ` : nothing}
                                ${this._options.map(option => this._renderRow(option))}
                            </tbody>
                        </table>
                    </div>
                `}
            </div>
        `;
    }

    private _renderRow(option: MillworkOption) {
        const money = this._money(option);
        const edited = !!this._edits[option.id];
        return html`
            <tr data-testid="option-row" data-option-name=${option.name}>
                <td class="px-4 py-3 font-medium text-white">${option.name}</td>
                <td class="px-3 py-3 text-right">
                    <input
                        type="number" step="0.01" min="0"
                        .value=${this._dollars(money.basePriceCents)}
                        @input=${(e: Event) => this._edit(option, {
                            basePriceCents: this._toCents((e.target as HTMLInputElement).value) ?? 0,
                        })}
                        class="w-24 rounded border border-white/10 bg-black/30 px-2 py-1 text-right font-mono text-white"
                    />
                </td>
                <td class="px-3 py-3 text-right">
                    <input
                        type="number" step="0.01" min="0"
                        .value=${this._dollars(money.baseCostCents)}
                        @input=${(e: Event) => this._edit(option, {
                            baseCostCents: this._toCents((e.target as HTMLInputElement).value) ?? 0,
                        })}
                        class="w-24 rounded border border-white/10 bg-black/30 px-2 py-1 text-right font-mono text-white"
                    />
                </td>
                <td class="px-3 py-3 text-right">
                    <input
                        type="number" step="0.01"
                        .value=${this._dollars(money.priceAdjustmentCents)}
                        @input=${(e: Event) => this._edit(option, {
                            priceAdjustmentCents: this._toCents((e.target as HTMLInputElement).value) ?? 0,
                        })}
                        class="w-24 rounded border border-white/10 bg-black/30 px-2 py-1 text-right font-mono text-white"
                    />
                </td>
                <td class="px-3 py-3 text-right">
                    <input
                        type="number" step="0.01"
                        .value=${this._dollars(money.costAdjustmentCents)}
                        @input=${(e: Event) => this._edit(option, {
                            costAdjustmentCents: this._toCents((e.target as HTMLInputElement).value) ?? 0,
                        })}
                        class="w-24 rounded border border-white/10 bg-black/30 px-2 py-1 text-right font-mono text-white"
                    />
                </td>
                <td class="px-3 py-3 text-right">
                    <button
                        @click=${() => this._save(option)}
                        ?disabled=${!edited || this._saving}
                        data-testid="save-option"
                        class="rounded-lg border border-gable-green/40 px-3 py-1.5 text-xs font-medium text-gable-green disabled:opacity-40 ${edited ? 'bg-gable-green/10' : ''}"
                    >
                        Save
                    </button>
                </td>
            </tr>
        `;
    }
}
