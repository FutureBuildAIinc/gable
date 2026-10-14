// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { ClipboardCheck, MapPin, ChevronRight, Check, AlertTriangle, Loader2, ScanLine } from 'lucide';
import type { Product } from '../../types/product';
import { InventoryService } from '../../services/InventoryService';
import { LocationService } from '../../services/LocationService';
import { ProductService } from '../../services/product.service';
import type { Location } from '../../types/location';
import '../../components/BarcodeScanner.ts';


/** One location that holds (or was found to hold) the product, with its own count. */
interface CountLocation {
    location_id: string;
    name: string;
    expected: number;
    counted: string;
}

/** A product counted by location: the yard's own row and each zone or bin, never the yard's sum. */
interface CountItem {
    product: Product;
    locations: CountLocation[];
    /** The locations a found pallet may be added from, loaded on first use. */
    picking: boolean;
}

const ZONES = [
    { code: 'MAIN-A', label: 'Lumber Storage A' },
    { code: 'MAIN-B', label: 'Sheet Goods B' },
    { code: 'MAIN-C', label: 'Hardware C' },
    { code: 'MAIN-D', label: 'Roofing & Insulation D' },
    { code: 'SAT1-A', label: 'Treated Lumber (Satellite)' },
    { code: 'SAT1-B', label: 'Millwork (Satellite)' },
];

@customElement('gable-cycle-count')
export class CycleCount extends LitElement {
    createRenderRoot() { return this; }

    @state() private selectedZone: string | null = null;
    @state() private items: CountItem[] = [];
    @state() private loading = false;
    @state() private submitting = false;
    @state() private submitted = false;
    @state() private isScanning = false;
    private _yardLocations: Location[] | null = null;
    @state() private yardLocations: Location[] = [];

    updated(changed: Map<string, unknown>) {
        if (changed.has('selectedZone') && this.selectedZone) {
            this._loadZoneItems();
        }
    }

    private async _loadZoneItems() {
        if (!this.selectedZone) return;
        this.loading = true;
        this.submitted = false;
        try {
            // Zones slice the catalog, so every page through the cursor is needed (capped at 2000 products).
            const data: Product[] = await ProductService.listAllProducts();
            const zoneIndex = ZONES.findIndex(z => z.code === this.selectedZone);
            const subset = data.slice(zoneIndex * 7, zoneIndex * 7 + 7);
            // A count is per location: the levels rows of each product are the yard's own row and
            // every zone or bin that holds it, each with its own expected figure.
            this.items = await Promise.all(subset.map(async (p): Promise<CountItem> => {
                const rows = await InventoryService.getInventoryByProduct(p.id);
                return {
                    product: p,
                    picking: false,
                    locations: rows
                        .filter(r => r.location_id)
                        .map(r => ({
                            location_id: r.location_id as string,
                            name: r.location_name || (r.location_id as string),
                            // The inventory adjust route counts in numbers, so the quantity string is read once, here.
                            expected: Number(r.quantity),
                            counted: '',
                        })),
                };
            }));
        } catch {
            this.items = [];
        } finally {
            this.loading = false;
        }
    }

    private _handleScan(barcode: string) {
        const index = this.items.findIndex(i =>
            i.product.id === barcode ||
            i.product.sku.toLowerCase() === barcode.toLowerCase()
        );

        if (index >= 0) {
            const item = this.items[index];
            if (item.locations.length === 0) {
                ToastService.show(`${item.product.description} has no location here: add the one you found it in`, 'error');
                return;
            }
            // A scan adds one to the first location listed; the person moves it with the count field.
            const first = item.locations[0];
            const currentCount = parseFloat(first.counted);
            const newCount = isNaN(currentCount) ? 1 : currentCount + 1;
            this._setCount(index, 0, newCount.toString());
            ToastService.show(`Scanned: ${item.product.description} (+1)`, 'success');
        } else {
            ToastService.show(`Item not found in current zone: ${barcode}`, 'error');
        }
    }

    private _setCount(idx: number, locIdx: number, value: string) {
        const next = [...this.items];
        const locations = [...next[idx].locations];
        locations[locIdx] = { ...locations[locIdx], counted: value };
        next[idx] = { ...next[idx], locations };
        this.items = next;
    }

    /** A found pallet: add a location the product was not listed in, to count it there. */
    private async _togglePicking(idx: number) {
        if (this._yardLocations === null) {
            try {
                this._yardLocations = await LocationService.listAllLocations();
            } catch {
                this._yardLocations = [];
            }
            this.yardLocations = this._yardLocations;
        }
        const next = [...this.items];
        next[idx] = { ...next[idx], picking: !next[idx].picking };
        this.items = next;
    }

    private _addLocation(idx: number, loc: Location) {
        const next = [...this.items];
        const item = next[idx];
        if (!item.locations.some(l => l.location_id === loc.id)) {
            item.locations = [...item.locations, { location_id: loc.id, name: loc.path || loc.code, expected: 0, counted: '' }];
        }
        next[idx] = { ...item, picking: false };
        this.items = next;
    }

    private async _handleSubmit() {
        this.submitting = true;
        let failCount = 0;
        for (const item of this.items) {
            for (const loc of item.locations) {
                const counted = parseFloat(loc.counted);
                if (isNaN(counted) || counted === loc.expected) continue;
                // Each count is written to its own location, so the yard's total after the count is
                // the sum of the counts and never one figure forced onto one row.
                try {
                    await InventoryService.adjustStock({
                        product_id: item.product.id,
                        location_id: loc.location_id,
                        quantity: counted,
                        reason: `Cycle count - Zone ${this.selectedZone}`,
                        is_delta: false,
                    });
                } catch {
                    failCount++;
                    console.error('Failed to adjust stock');
                }
            }
        }
        if (failCount > 0) {
            ToastService.show(`${failCount} adjustment(s) failed to save`, 'error');
        }
        this.submitting = false;
        this.submitted = true;
    }

    private get _allLocations() { return this.items.flatMap(i => i.locations); }
    private get _countedCount() { return this._allLocations.filter(l => l.counted !== '').length; }
    private get _discrepancies() {
        return this._allLocations.filter(l => {
            const c = parseFloat(l.counted);
            return !isNaN(c) && c !== l.expected;
        }).length;
    }

    render() {
        return html`
            <div class="flex flex-col space-y-4 p-4 max-w-md mx-auto">
                <h1 class="text-xl font-bold text-white tracking-tight flex items-center gap-2">
                    ${icon(ClipboardCheck, 20, 'text-amber-400')}
                    Cycle Count
                </h1>

                <!-- Zone Selection -->
                ${!this.selectedZone ? html`
                    <div class="space-y-2">
                        <p class="text-sm text-zinc-400">Select a zone to count:</p>
                        ${ZONES.map(z => html`
                            <div
                                class="rounded-2xl border border-white/[0.06] bg-surface-2/80 backdrop-blur-xl active:scale-[0.98] transition-all cursor-pointer border-white/5 hover:border-amber-400/30"
                                @click=${() => { this.selectedZone = z.code; }}
                            >
                                <div class="p-4 flex items-center justify-between">
                                    <div class="flex items-center gap-3">
                                        ${icon(MapPin, 16, 'text-amber-400')}
                                        <div>
                                            <div class="font-medium text-white text-sm">${z.label}</div>
                                            <div class="text-xs text-zinc-500 font-mono">${z.code}</div>
                                        </div>
                                    </div>
                                    ${icon(ChevronRight, 16, 'text-zinc-600')}
                                </div>
                            </div>
                        `)}
                    </div>
                ` : nothing}

                <!-- Count Interface -->
                ${this.selectedZone ? html`
                    <div class="flex items-center justify-between">
                        <button
                            @click=${() => { this.selectedZone = null; this.items = []; }}
                            aria-label="Change zone"
                            class="text-xs text-amber-400 hover:underline"
                        >
                            &larr; Change Zone
                        </button>

                        <div class="flex items-center gap-3">
                            <button
                                @click=${() => { this.isScanning = true; }}
                                aria-label="Scan item barcode"
                                class="flex items-center gap-1.5 text-xs bg-zinc-800 hover:bg-zinc-700 text-amber-400 py-1.5 px-3 rounded-lg border border-white/10 transition-colors"
                            >
                                ${icon(ScanLine, 14)} Scan Item
                            </button>
                            <span class="text-xs font-mono text-zinc-500">
                                ${ZONES.find(z => z.code === this.selectedZone)?.label}
                            </span>
                        </div>
                    </div>

                    ${this.isScanning ? html`
                        <gable-barcode-scanner
                            @scan=${(e: CustomEvent) => { this.isScanning = false; this._handleScan(e.detail); }}
                            @close=${() => { this.isScanning = false; }}
                        ></gable-barcode-scanner>
                    ` : nothing}

                    ${this.loading ? html`
                        <div class="flex justify-center py-12">
                            <div class="animate-spin rounded-full h-8 w-8 border-b-2 border-amber-400"></div>
                        </div>
                    ` : this.submitted ? html`
                        <div data-testid="count-done" class="text-center py-16 flex flex-col items-center gap-4">
                            <div class="w-16 h-16 rounded-full bg-emerald-500/10 flex items-center justify-center">
                                ${icon(Check, 32, 'text-emerald-400')}
                            </div>
                            <p class="text-white font-medium text-lg">Count Submitted</p>
                            <p class="text-zinc-500 text-sm">${this._discrepancies} discrepancies adjusted</p>
                            <button
                                @click=${() => { this.selectedZone = null; this.items = []; }}
                                class="mt-4 px-6 py-2 bg-white/5 border border-white/10 text-white rounded-lg text-sm hover:bg-white/10 transition-colors"
                            >
                                Count Another Zone
                            </button>
                        </div>
                    ` : html`
                        <!-- Progress -->
                        <div class="flex items-center gap-3 text-xs text-zinc-500">
                            <span>${this._countedCount}/${this._allLocations.length} locations counted</span>
                            ${this._discrepancies > 0 ? html`
                                <span class="flex items-center gap-1 text-amber-400">
                                    ${icon(AlertTriangle, 12)}
                                    ${this._discrepancies} discrepancies
                                </span>
                            ` : nothing}
                        </div>

                        <div class="space-y-2">
                            ${this.items.map((item, idx) => html`
                                <div class="rounded-2xl border border-white/[0.06] bg-surface-2/80 backdrop-blur-xl border-white/5">
                                    <div class="p-4 space-y-3">
                                        <div class="flex items-center gap-3">
                                            <div class="flex-1 min-w-0">
                                                <div class="font-medium text-white text-sm truncate">${item.product.description}</div>
                                                <div class="text-xs text-zinc-500 font-mono">${item.product.sku}</div>
                                            </div>
                                            <button
                                                @click=${() => this._togglePicking(idx)}
                                                class="text-xs text-amber-400 hover:underline shrink-0"
                                            >Found elsewhere</button>
                                        </div>
                                        ${item.locations.length === 0 ? html`
                                            <p class="text-xs text-zinc-500">No location holds this product here.</p>
                                        ` : nothing}
                                        ${item.locations.map((loc, locIdx) => {
                                            const counted = parseFloat(loc.counted);
                                            const hasDiscrep = !isNaN(counted) && counted !== loc.expected;
                                            return html`
                                                <div data-testid="count-location" class="flex items-center gap-3">
                                                    <div class="flex-1 min-w-0 text-xs font-mono text-zinc-400 truncate">${loc.name}</div>
                                                    <div class="text-right shrink-0 mr-2">
                                                        <div class="text-[10px] text-zinc-500 font-mono">Expected</div>
                                                        <div class="font-mono text-sm text-zinc-300">${loc.expected}</div>
                                                    </div>
                                                    <input
                                                        data-testid="count-input"
                                                        type="number"
                                                        inputmode="numeric"
                                                        aria-label="Count at ${loc.name}"
                                                        .value=${loc.counted}
                                                        @input=${(e: InputEvent) => this._setCount(idx, locIdx, (e.target as HTMLInputElement).value)}
                                                        placeholder="-"
                                                        class="w-20 text-center bg-black/20 border rounded-lg py-2 font-mono text-sm focus:outline-none transition-colors ${hasDiscrep
                                                            ? 'border-amber-400/50 text-amber-400 focus:border-amber-400'
                                                            : loc.counted !== ''
                                                                ? 'border-emerald-500/30 text-emerald-400'
                                                                : 'border-white/10 text-white focus:border-amber-400/50'
                                                        }"
                                                    />
                                                </div>
                                            `;
                                        })}
                                        ${item.picking ? html`
                                            <div class="rounded-lg border border-white/10 p-2 max-h-40 overflow-y-auto space-y-1">
                                                ${this.yardLocations.filter(l => !item.locations.some(x => x.location_id === l.id)).map(l => html`
                                                    <button
                                                        @click=${() => this._addLocation(idx, l)}
                                                        class="block w-full text-left text-xs font-mono text-zinc-300 hover:text-amber-400 py-1"
                                                    >${l.path || l.code}</button>
                                                `)}
                                            </div>
                                        ` : nothing}
                                    </div>
                                </div>
                            `)}
                        </div>

                        <!-- Submit -->
                        <div class="sticky bottom-20 pt-4">
                            <button
                                @click=${() => this._handleSubmit()}
                                ?disabled=${this._countedCount === 0 || this.submitting}
                                class="w-full py-4 rounded-xl font-bold text-lg font-mono uppercase tracking-wider transition-all ${this._countedCount > 0
                                    ? 'bg-amber-400 text-black hover:bg-amber-300 active:scale-[0.98] shadow-lg shadow-amber-400/20'
                                    : 'bg-white/5 text-zinc-600 border border-white/10 cursor-not-allowed'
                                }"
                            >
                                ${this.submitting ? html`
                                    <span class="flex items-center justify-center gap-2">
                                        ${icon(Loader2, 20, 'animate-spin')} Submitting...
                                    </span>
                                ` : `Submit Count (${this._countedCount} items)`}
                            </button>
                        </div>
                    `}
                ` : nothing}
            </div>
        `;
    }
}
