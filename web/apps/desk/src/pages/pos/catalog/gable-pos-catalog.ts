// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's Items card: a search field with a quantity and Add, category
// chips and a grid of product tiles. It reads the catalogue for the caller's
// yard and emits one `pos-catalog-add` event per add; it never touches the sale.

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import '../../../components/BarcodeScanner.ts';
import { posService } from '../../../services/POSService';
import type { CatalogCategory, CatalogItem } from '../../../services/POSService';

/** What an add carries: the fields the sale needs and a thumbnail. */
export interface CatalogProduct {
    product_id: string;
    sku: string;
    description: string;
    uom: string;
    unit_price_cents: number;
    in_stock: string;
    image_url: string | null;
}

export interface CatalogAddDetail {
    product: CatalogProduct;
    /** The quantity field's value as typed, a positive decimal string. */
    qty: string;
    /** The search text the add was made under, so the page can say what was submitted. */
    query: string;
}

const DEBOUNCE_MS = 250;
const POSITIVE_QTY = /^\d+(\.\d+)?$/;

function price(cents: number, uom: string): string {
    const sign = cents < 0 ? '-' : '';
    const abs = Math.abs(cents);
    return `${sign}$${Math.floor(abs / 100).toLocaleString('en-US')}.${String(abs % 100).padStart(2, '0')} / ${uom}`;
}

@customElement('gable-pos-catalog')
export class GablePosCatalog extends LitElement {
    createRenderRoot() { return this; }

    /** The till is closed or no sale is open: every add is blocked. */
    @property({ type: Boolean }) disabled = false;
    /** Any change re-reads the grid (the counter sets it when the yard changes). */
    @property({ attribute: 'yard-key' }) yardKey = '';
    /** One line the page wants beside the card's heading (empty for none). */
    @property() note = '';

    @state() private _categories: CatalogCategory[] = [];
    @state() private _items: CatalogItem[] = [];
    @state() private _category = '';
    @state() private _query = '';
    @state() private _qty = '1';
    @state() private _loading = true;
    @state() private _failed = false;
    @state() private _brokenImages = new Set<string>();
    @state() private _highlight = 0;
    @state() private _touched = false;
    @state() private _notice = '';
    @state() private _scanning = false;
    @state() private _chipsOverflow = false;
    @state() private _chipsAtEnd = false;

    private _seq = 0;
    private _lastKey = '';
    private _timer: ReturnType<typeof setTimeout> | null = null;
    private _pending: Promise<void> = Promise.resolve();
    private _resizer: ResizeObserver | null = null;

    connectedCallback(): void {
        super.connectedCallback();
        void this._read();
    }

    disconnectedCallback(): void {
        super.disconnectedCallback();
        this._clearTimer();
        this._resizer?.disconnect();
        this._resizer = null;
    }

    protected firstUpdated(): void {
        if (typeof ResizeObserver === 'undefined') return;
        const row = this.querySelector('[data-testid="catalog-chips"]');
        if (!row) return;
        this._resizer = new ResizeObserver(() => this._measureChips());
        this._resizer.observe(row);
    }

    private _measureChips(): void {
        const row = this.querySelector<HTMLElement>('[data-testid="catalog-chips"]');
        if (!row) return;
        const over = row.scrollWidth > row.clientWidth + 1;
        const atEnd = row.scrollLeft + row.clientWidth >= row.scrollWidth - 1;
        if (over !== this._chipsOverflow) this._chipsOverflow = over;
        if (atEnd !== this._chipsAtEnd) this._chipsAtEnd = atEnd;
    }

    /** A typed search looks across every category; the chip returns when it is cleared. */
    private get _searching(): boolean {
        return this._query.trim().length >= 2;
    }

    private get _activeCategory(): string {
        return this._searching ? '' : this._category;
    }

    protected willUpdate(changed: Map<string, unknown>): void {
        // The old yard's tiles must not stay clickable while the new yard's first read is pending.
        if (changed.has('yardKey') && changed.get('yardKey') !== undefined) {
            this._items = [];
            this._failed = false;
            // The first value arrives with the first read; only a later change re-reads.
            this._category = '';
            this._clearTimer();
            void this._read();
        }
    }

    protected updated(): void {
        // Measured after this update has completed, so the result never schedules one inside it.
        queueMicrotask(() => this._measureChips());
    }

    private _clearTimer(): void {
        if (this._timer !== null) clearTimeout(this._timer);
        this._timer = null;
    }

    private _readKey(): string {
        const term = this._query.trim();
        return JSON.stringify([this._activeCategory, term.length >= 2 ? term : '']);
    }

    private _read(): Promise<void> {
        const seq = ++this._seq;
        const term = this._query.trim();
        this._lastKey = this._readKey();
        this._loading = true;
        this._pending = (async () => {
            try {
                const res = await posService.tiles({
                    category: this._activeCategory || undefined,
                    q: term.length >= 2 ? term : undefined,
                });
                if (seq !== this._seq) return;
                this._categories = res.categories ?? [];
                this._items = res.items ?? [];
                this._highlight = 0;
                this._touched = false;
                this._failed = false;
            } catch {
                if (seq !== this._seq) return;
                this._failed = true;
            } finally {
                if (seq === this._seq) this._loading = false;
            }
        })();
        return this._pending;
    }

    private _chip(name: string): void {
        if (name === this._activeCategory) return;
        this._category = name;
        this._query = '';
        this._notice = '';
        this._clearTimer();
        void this._read();
    }

    private _onSearch(e: Event): void {
        this._query = (e.target as HTMLInputElement).value;
        this._notice = '';
        this._clearTimer();
        this._timer = setTimeout(() => {
            this._timer = null;
            // One typed character asks for nothing new: the read is the same as the last.
            if (this._readKey() !== this._lastKey) void this._read();
        }, DEBOUNCE_MS);
    }

    private _onQty(e: Event): void {
        this._qty = (e.target as HTMLInputElement).value;
    }

    private get _qtyValid(): boolean {
        return POSITIVE_QTY.test(this._qty.trim()) && Number(this._qty) > 0;
    }

    private _emit(item: CatalogItem): void {
        if (this.disabled || !this._qtyValid) return;
        const detail: CatalogAddDetail = {
            product: {
                product_id: item.product_id,
                sku: item.sku,
                description: item.description,
                uom: item.uom,
                unit_price_cents: item.unit_price_cents,
                in_stock: item.in_stock,
                image_url: item.image_url,
            },
            qty: this._qty.trim(),
            query: this._query,
        };
        this.dispatchEvent(new CustomEvent<CatalogAddDetail>('pos-catalog-add', { detail, bubbles: true, composed: true }));
    }

    /** Enter or the Add button: the exact SKU among the results, else the first. */
    private async _addFromSearch(): Promise<void> {
        if (this.disabled) return;
        if (!this._searching) {
            if (this._timer !== null) {
                this._clearTimer();
                if (this._readKey() !== this._lastKey) void this._read();
            }
            // A tile chosen with the arrow keys is added without a search.
            const chosen = this._touched ? this._items[this._highlight] : undefined;
            if (chosen) this._emit(chosen);
            else this._notice = 'Type at least two characters';
            return;
        }
        if (this._timer !== null) {
            this._clearTimer();
            await this._read();
        } else {
            await this._pending;
        }
        const term = this._query.trim();
        if (this._failed) return;
        const exact = this._items.find((i) => i.sku.toLowerCase() === term.toLowerCase());
        const pick = exact ?? this._items[Math.min(this._highlight, this._items.length - 1)];
        if (!pick) return;
        this._emit(pick);
    }

    /**
     * The page calls this once it has accepted an add: the quantity returns to 1
     * and the search is cleared. A refused add leaves both as typed. Given what
     * the add submitted, only a field that still holds it is reset, so something
     * typed while the add was in flight is kept.
     */
    resetQuantity(sent?: { qty?: string; query?: string }): void {
        this._notice = '';
        if (sent?.qty === undefined || this._qty.trim() === sent.qty) this._qty = '1';
        if (this._query !== '' && (sent?.query === undefined || this._query === sent.query)) {
            this._clearTimer();
            this._query = '';
            void this._read();
        }
    }

    /** A scanned code is looked up across the whole catalogue and added only on an exact match. */
    private async _onScan(code: string): Promise<void> {
        this._scanning = false;
        if (this.disabled) return;
        const barcode = String(code).trim();
        try {
            const res = await posService.tiles({ q: barcode });
            const lower = barcode.toLowerCase();
            const hit = (res.items ?? []).find(
                (i) => i.sku.toLowerCase() === lower || i.product_id === barcode,
            );
            if (!hit) {
                this._notice = `Product not found for barcode: ${barcode}`;
                return;
            }
            this._notice = '';
            this._emit(hit);
        } catch {
            this._notice = 'Error scanning barcode';
        }
    }

    /** Tiles across in the first row of the grid as laid out (one when it cannot be measured). */
    private _columns(): number {
        const tiles = [...this.querySelectorAll<HTMLElement>('[data-testid="catalog-tile"]')];
        if (tiles.length === 0) return 1;
        const top = tiles[0].offsetTop;
        return Math.max(1, tiles.filter((t) => t.offsetTop === top).length);
    }

    private _moveHighlight(key: string): boolean {
        const n = this._items.length;
        if (n === 0) return false;
        const cols = this._columns();
        const at = this._highlight;
        const next: Record<string, number> = {
            ArrowLeft: at - 1,
            ArrowRight: at + 1,
            ArrowUp: at - cols,
            ArrowDown: at + cols,
            Home: 0,
            End: n - 1,
        };
        if (!(key in next)) return false;
        this._highlight = Math.max(0, Math.min(next[key], n - 1));
        this._touched = true;
        return true;
    }

    /**
     * Enter adds; the arrow keys, Home and End move the highlight. In the search
     * field only Up and Down move it, so the caret keys keep their meaning.
     */
    private _onKey(e: KeyboardEvent, inSearch: boolean): void {
        if (e.key === 'Enter') {
            e.preventDefault();
            void this._addFromSearch();
            return;
        }
        if (inSearch && e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
        if (this._moveHighlight(e.key)) e.preventDefault();
    }

    private _picture(item: CatalogItem) {
        const shown = item.image_url && !this._brokenImages.has(item.product_id);
        return html`<div data-testid="catalog-picture" class="flex h-[72px] w-[72px] shrink-0 items-center justify-center overflow-hidden rounded-md border border-white/10 bg-white">
            ${shown
                ? html`<img src=${item.image_url!} alt="" loading="lazy" class="h-full w-full object-cover"
                      @error=${() => { this._brokenImages = new Set(this._brokenImages).add(item.product_id); }} />`
                : html`<svg data-testid="catalog-no-picture" viewBox="0 0 24 24" fill="none" aria-label="No picture"
                      class="h-8 w-8 text-slate-500">
                      <rect x="2" y="14" width="20" height="5" rx="1" stroke="currentColor" stroke-width="1.5" />
                      <rect x="4" y="9" width="16" height="5" rx="1" stroke="currentColor" stroke-width="1.5" />
                      <rect x="7" y="4" width="10" height="5" rx="1" stroke="currentColor" stroke-width="1.5" />
                  </svg>`}
        </div>`;
    }

    private _tile(item: CatalogItem, on: boolean) {
        return html`<li data-testid="catalog-tile" aria-current=${on ? 'true' : 'false'}
            class=${`flex flex-col gap-2 rounded-lg border p-3 ${on ? 'border-gable-green/60 bg-white/5' : 'border-white/10 bg-deep-space'}`}>
            ${this._picture(item)}
            <div class="line-clamp-2 min-h-[2.5rem] text-sm font-semibold text-white">${item.description}</div>
            <div class="font-mono text-xs text-slate-300">${item.sku} · ${item.in_stock} on hand</div>
            <div class="mt-auto flex items-center justify-between gap-2">
                <span class="font-mono text-sm font-semibold text-white">${price(item.unit_price_cents, item.uom)}</span>
                <button type="button" data-testid="catalog-tile-add" ?disabled=${this.disabled}
                    aria-label=${`Add ${item.description}`}
                    @click=${() => this._emit(item)}
                    class="rounded border border-white/20 px-3 py-1.5 text-xs font-semibold text-white hover:border-gable-green hover:text-gable-green disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:border-white/20 disabled:hover:text-white">Add</button>
            </div>
        </li>`;
    }

    render() {
        const chips = [{ name: '', label: 'All' }, ...this._categories.map((c) => ({ name: c.name, label: c.name }))];
        return html`<section class="rounded-lg border border-white/10 bg-slate-steel p-4" data-testid="pos-catalog" aria-labelledby="pos-catalog-title">
            <div class="flex min-h-7 items-baseline justify-between gap-3">
                <h2 id="pos-catalog-title" class="text-lg font-bold text-white">Items</h2>
                ${this.note !== '' ? html`<span class="text-xs text-slate-400" data-testid="catalog-note">${this.note}</span>` : nothing}
            </div>
            <div class="mt-3 flex gap-2">
                <input data-testid="catalog-search" type="text" autocomplete="off" placeholder="Search by name, SKU or scan"
                    aria-label="Search by name, SKU or scan" .value=${this._query}
                    ?disabled=${this.disabled}
                    @input=${this._onSearch} @keydown=${(e: KeyboardEvent) => this._onKey(e, true)}
                    class="min-w-0 flex-1 rounded border border-white/10 bg-deep-space px-3 py-2 text-sm text-white outline-none focus:border-gable-green disabled:cursor-not-allowed disabled:opacity-40" />
                <input data-testid="catalog-qty" type="text" inputmode="decimal" autocomplete="off" aria-label="Quantity"
                    .value=${this._qty} ?disabled=${this.disabled}
                    aria-invalid=${this._qtyValid ? 'false' : 'true'}
                    @input=${this._onQty} @keydown=${(e: KeyboardEvent) => this._onKey(e, false)}
                    class="w-16 rounded border border-white/10 bg-deep-space px-2 py-2 text-center font-mono text-sm text-white outline-none focus:border-gable-green disabled:cursor-not-allowed disabled:opacity-40 aria-[invalid=true]:border-safety-red" />
                <button type="button" data-testid="catalog-scan"
                    @click=${() => { this._scanning = true; }}
                    class="rounded border border-white/20 px-3 py-2 text-sm font-semibold text-white hover:border-gable-green hover:text-gable-green">Scan</button>
                <button type="button" data-testid="catalog-add" ?disabled=${this.disabled}
                    @click=${() => void this._addFromSearch()}
                    class="rounded border border-white/20 px-4 py-2 text-sm font-semibold text-white hover:border-gable-green hover:text-gable-green disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:border-white/20 disabled:hover:text-white">Add</button>
            </div>
            ${this.disabled
                ? html`<p class="mt-2 text-xs text-slate-400" data-testid="catalog-closed">Open the till to start a sale</p>`
                : nothing}
            ${this._notice
                ? html`<p class="mt-2 text-xs text-slate-300" role="status" data-testid="catalog-notice">${this._notice}</p>`
                : nothing}
            ${this._scanning
                ? html`<gable-barcode-scanner
                      @scan=${(e: CustomEvent) => { void this._onScan(e.detail); }}
                      @close=${() => { this._scanning = false; }}></gable-barcode-scanner>`
                : nothing}
            <div class="relative mt-3">
            <div data-testid="catalog-chips" role="group" aria-label="Categories" @scroll=${() => this._measureChips()}
                class="flex flex-nowrap gap-2 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
                ${chips.map((c) => {
                    const on = c.name === this._activeCategory;
                    return html`<button type="button" data-testid="catalog-chip" aria-pressed=${on ? 'true' : 'false'}
                        @click=${() => this._chip(c.name)}
                        class=${`shrink-0 whitespace-nowrap rounded-full border px-3 py-1 text-xs font-semibold outline-none focus-visible:ring-1 focus-visible:ring-white/60 ${on
                            ? 'border-gable-green bg-gable-green text-deep-space'
                            : 'border-white/20 text-white hover:bg-white/10'}`}>${c.label}</button>`;
                })}
            </div>
            ${this._chipsOverflow && !this._chipsAtEnd
                ? html`<span data-testid="catalog-chips-fade" aria-hidden="true"
                      class="pointer-events-none absolute inset-y-0 right-0 w-12 bg-gradient-to-l from-slate-steel to-transparent"></span>`
                : nothing}
            </div>
            ${this._failed
                ? html`<p class="mt-4 flex items-center gap-3 text-sm text-slate-400" role="status">
                      <span>Products could not be loaded</span>
                      <button type="button" data-testid="catalog-retry" @click=${() => void this._read()}
                          class="rounded border border-white/20 px-2 py-1 text-xs font-semibold text-white hover:border-gable-green hover:text-gable-green">Retry</button>
                  </p>`
                : this._loading && this._items.length === 0
                    ? html`<p class="mt-4 text-sm text-slate-400" role="status">Loading products</p>`
                : this._items.length === 0
                    ? html`<p class="mt-4 text-sm text-slate-400">No products match</p>`
                    : html`<ul data-testid="catalog-grid"
                          @keydown=${(e: KeyboardEvent) => { if (e.key !== 'Enter') this._onKey(e, false); }} class=${`mt-4 grid grid-cols-[repeat(auto-fill,minmax(12.5rem,1fr))] gap-3 ${this.disabled ? 'opacity-50' : ''}`}
                          aria-busy=${this._loading ? 'true' : 'false'}>
                          ${this._items.map((i, n) => this._tile(i, !this.disabled && n === this._highlight))}
                      </ul>`}
        </section>`;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-pos-catalog': GablePosCatalog;
    }
}
