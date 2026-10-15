// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../../lib/icons';
import { Radar, Play, X, AlertTriangle } from 'lucide';
import { formatCents } from '../../../lib/utils';
import { fetchWithAuth } from '../../../services/fetchClient';
import { apiErrorMessage, ApiError } from '../../../services/apiError';
import { ProductService, matchProducts } from '../../../services/product.service';
import { marketCheckService } from '../../../services/MarketCheckService';
import type { MarketCheckResult, MarketCheckRow, MarketCheckSettings } from '../../../services/MarketCheckService';
import type { Product } from '../../../types/product';

/** The most products one check takes (the server's bound). */
const MAX_PICK = 12;
/** On first open the pick is the yard's top lumber sellers. */
const API_URL = import.meta.env.VITE_API_URL || '';
const DEFAULT_PICK = 6;
const COMPETITOR = 'Home Depot';

interface Picked {
  id: string;
  sku: string;
  description: string;
}

/**
 * The catalogue's dimensional lumber by the trade's definition from the board fields: nominal thickness 2,
 * width 4 to 12 inches, length 8 to 16 feet, not random length (so 2x4 to 2x12, 8 to 16 feet; engineered
 * wood such as LVL is not). The most available first. The product list carries no category.
 */
export function dimensionalLumber(catalog: Product[]): Picked[] {
  const num = (v: unknown) => (v == null ? NaN : Number.parseFloat(String(v)));
  return catalog
    .filter((p) => {
      const t = num(p.board_thickness_in);
      const w = num(p.board_width_in);
      const l = num(p.board_length_ft);
      return !p.random_length && t === 2 && w >= 4 && w <= 12 && l >= 8 && l <= 16;
    })
    .sort((a, b) => Number.parseFloat(b.available) - Number.parseFloat(a.available))
    .slice(0, DEFAULT_PICK)
    .map((p) => ({ id: p.id, sku: p.sku, description: p.description }));
}

/** "1 is below", "2 are below": the verb follows the count. */
function countPhrase(n: number, what: string): string {
  return `${n} ${n === 1 ? 'is' : 'are'} ${what}`;
}

/** The summary line over a finished check. */
export function summarize(res: MarketCheckResult): string {
  const total = res.rows.length;
  const count = (p: string) => res.rows.filter((r) => r.status === 'matched' && r.position === p).length;
  const above = count('above');
  const below = count('below');
  const inLine = count('in_line');
  const lost = res.rows.filter((r) => r.status !== 'matched').length;
  return (
    `${above} of ${total} ${above === 1 ? 'is' : 'are'} above ${COMPETITOR} by more than ${res.band_pct} percent, ` +
    `${countPhrase(below, 'below')}, ${countPhrase(inLine, 'in line')}; ${lost} could not be matched`
  );
}

/** The suggested price's reason in words. */
export function reasonWords(row: MarketCheckRow, bandPct: string, floorPct: string): string {
  switch (row.reason) {
    case 'lower_to_band':
      return `Within ${bandPct} percent of ${COMPETITOR}`;
    case 'raise_to_band':
      return `Raise to within ${bandPct} percent`;
    case 'held_at_floor':
      return `Held at the ${floorPct} percent margin floor`;
    case 'keep':
      return 'Keep';
    default:
      return row.note;
  }
}

const POSITION_WORDS: Record<string, string> = { above: 'Above', below: 'Below', in_line: 'In line' };

function signedPct(n: number): string {
  return `${n > 0 ? '+' : ''}${n.toFixed(1)}%`;
}
function signedMoney(cents: number): string {
  return `${cents > 0 ? '+' : cents < 0 ? '-' : ''}${formatCents(Math.abs(cents))}`;
}

@customElement('gable-market-check')
export class GableMarketCheck extends LitElement {
  createRenderRoot() { return this; }

  @state() private _settings: MarketCheckSettings | null = null;
  @state() private _loading = true;
  @state() private _loadError: string | null = null;
  @state() private _picked: Picked[] = [];
  @state() private _query = '';
  @state() private _catalog: Product[] = [];
  @state() private _running = false;
  @state() private _result: MarketCheckResult | null = null;
  @state() private _runError: string | null = null;
  /** Which source filled the first pick, said plainly on the page. */
  @state() private _pickSource = '';

  connectedCallback() {
    super.connectedCallback();
    void this._load();
  }

  private async _load() {
    this._loading = true;
    this._loadError = null;
    try {
      this._settings = await marketCheckService.getSettings();
      if (this._settings.configured) {
        // The product list answers no category and no search, so the pick and the
        // search both work over the catalogue the page loads once.
        this._catalog = await ProductService.listAllProducts();
        const tiles = await this._topSellers();
        const boards = dimensionalLumber(this._catalog);
        let chosen: Picked[];
        if (tiles.length > 0) {
          chosen = tiles;
          this._pickSource = 'the yard top sellers in Lumber';
        } else if (boards.length > 0) {
          chosen = boards;
          this._pickSource = 'the dimensional lumber (2x4 to 2x12, 8 to 16 feet) with the most on hand';
        } else {
          chosen = this._catalog.slice(0, DEFAULT_PICK).map((p) => ({ id: p.id, sku: p.sku, description: p.description }));
          this._pickSource = 'the first products in the catalogue';
        }
        this._picked = chosen;
      }
    } catch (err) {
      this._loadError = apiErrorMessage(err, 'Could not load the Market check');
    } finally {
      this._loading = false;
    }
  }

  /**
   * The yard's top sellers in Lumber from the counter's tiles read, when the route exists and answers some;
   * any failure (a server without the route answers 404) is an empty list and the caller falls back.
   */
  private async _topSellers(): Promise<Picked[]> {
    try {
      const response = await fetchWithAuth(`${API_URL}/api/v1/pos/tiles?category=Lumber&limit=${DEFAULT_PICK}`);
      if (!response.ok) return [];
      const body = (await response.json()) as { items?: { product_id: string; sku: string; description: string }[] };
      return (body.items ?? []).slice(0, DEFAULT_PICK).map((t) => ({ id: t.product_id, sku: t.sku, description: t.description }));
    } catch {
      return [];
    }
  }

  private _add(p: Product) {
    if (this._picked.length >= MAX_PICK || this._picked.some((x) => x.id === p.id)) return;
    this._picked = [...this._picked, { id: p.id, sku: p.sku, description: p.description }];
    this._query = '';
  }

  private _remove(id: string) {
    this._picked = this._picked.filter((p) => p.id !== id);
  }

  private async _run() {
    if (this._picked.length === 0 || this._running) return;
    this._running = true;
    this._runError = null;
    this._result = null;
    try {
      this._result = await marketCheckService.run(this._picked.map((p) => p.id));
    } catch (err) {
      if (err instanceof ApiError && err.code === 'not_configured') {
        await this._load();
      } else {
        this._runError = apiErrorMessage(err, 'The market check could not run');
      }
    } finally {
      this._running = false;
    }
  }

  private _header() {
    const s = this._settings;
    const flow = this._result?.flow_name ?? s?.flow_name ?? '';
    const label = this._result?.store_label ?? s?.store_label ?? '';
    return html`
      <div>
        <h1 class="text-3xl font-bold text-white tracking-tight flex items-center gap-3">
          ${icon(Radar, 32, 'w-8 h-8 text-gable-green')}
          Market check
        </h1>
        ${label ? html`<p class="text-slate-300 mt-1" data-testid="store-label">${label}</p>` : nothing}
        ${s?.configured
          ? html`<p class="text-xs text-slate-500 mt-1" data-testid="powered-by">Powered by FB Connect: ${flow}</p>`
          : nothing}
      </div>
    `;
  }

  private _renderNotConfigured() {
    return html`
      <div class="bg-slate-steel border border-white/10 rounded-lg px-6 py-5 max-w-2xl" data-testid="not-configured">
        <p class="text-white font-medium">Market check is off.</p>
        <p class="text-sm text-slate-400 mt-1">
          An admin enters the FB Connect address and the flow's webhook id in
          <a href="/admin?tab=market-check" class="text-blueprint-blue underline">Tech Admin</a>.
        </p>
        ${this._settings?.notice ? html`<p class="text-sm text-amber-400 mt-2">${this._settings.notice}</p>` : nothing}
      </div>
    `;
  }

  private _renderPick() {
    const matches = this._query.trim()
      ? matchProducts(this._catalog, this._query)
          .filter((p) => !this._picked.some((x) => x.id === p.id))
          .slice(0, 8)
      : [];
    return html`
      <div class="bg-slate-steel border border-white/10 rounded-lg p-4 space-y-3">
        <div class="flex flex-wrap gap-2" data-testid="chips">
          ${this._picked.map(
            (p) => html`
              <span class="inline-flex items-center gap-2 bg-deep-space border border-white/10 rounded px-2 py-1 text-xs text-white">
                <span class="font-mono">${p.sku}</span>
                <button type="button" aria-label="Remove ${p.sku}" class="text-slate-400 hover:text-white"
                  @click=${() => this._remove(p.id)}>${icon(X, 12)}</button>
              </span>
            `,
          )}
          ${this._picked.length === 0 ? html`<span class="text-sm text-slate-500">Pick up to ${MAX_PICK} products to check.</span>` : nothing}
        </div>
        <div class="relative max-w-md">
          <input type="text" .value=${this._query} placeholder="Add a product by SKU or description"
            ?disabled=${this._picked.length >= MAX_PICK}
            @input=${(e: Event) => { this._query = (e.target as HTMLInputElement).value; }}
            class="w-full bg-deep-space border border-white/10 rounded px-3 py-2 text-white text-sm placeholder-slate-500 focus:outline-none focus:border-gable-green" />
          ${matches.length > 0 ? html`
            <ul class="absolute z-10 mt-1 w-full bg-slate-steel border border-white/10 rounded shadow-lg" data-testid="matches">
              ${matches.map((p) => html`
                <li><button type="button" class="w-full text-left px-3 py-2 text-sm text-white hover:bg-white/5"
                  @click=${() => this._add(p)}><span class="font-mono">${p.sku}</span> <span class="text-slate-400">${p.description}</span></button></li>
              `)}
            </ul>` : nothing}
        </div>
        <div class="flex items-center gap-3">
          <button type="button" data-testid="run" @click=${() => this._run()}
            ?disabled=${this._picked.length === 0 || this._running}
            class="inline-flex items-center gap-2 bg-gable-green text-black font-semibold px-4 py-2 rounded disabled:opacity-50">
            ${icon(Play, 14)} Run check
          </button>
          ${this._pickSource ? html`<span class="text-xs text-slate-500">First pick: ${this._pickSource}.</span>` : nothing}
        </div>
      </div>
    `;
  }

  private _renderRunning() {
    return html`
      <div class="space-y-3" data-testid="running">
        <p class="text-slate-300 text-sm">Reading live prices through FB Connect</p>
        ${this._picked.map(() => html`<div class="h-10 rounded bg-white/5 animate-pulse"></div>`)}
      </div>
    `;
  }

  private _renderRow(r: MarketCheckRow, res: MarketCheckResult) {
    const c = r.competitor;
    const head = html`
      <td class="px-3 py-2"><div class="text-white text-sm">${r.description}</div><div class="font-mono text-xs text-slate-500">${r.sku}</div></td>
      <td class="px-3 py-2 font-mono text-white text-right">${r.our_price_cents > 0 ? formatCents(r.our_price_cents) : ''}</td>
    `;
    if (r.status !== 'matched' || !c) {
      return html`<tr class="border-t border-white/5" data-status=${r.status}>
        ${head}
        <td colspan="4" class="px-3 py-2 text-sm ${r.status === 'error' ? 'text-red-400' : 'text-slate-400'}">
          ${r.status === 'unmatched' ? 'No match: ' : ''}${r.note}
        </td>
      </tr>`;
    }
    const diffCents = r.our_price_cents - c.price_cents;
    return html`<tr class="border-t border-white/5" data-status="matched">
      ${head}
      <td class="px-3 py-2">
        <div class="text-sm">
          ${c.link
            ? html`<a href=${c.link} target="_blank" rel="noopener noreferrer" class="text-blueprint-blue underline">${c.title}</a>`
            : html`<span class="text-white">${c.title}</span>`}
        </div>
        <div class="font-mono text-white text-sm">${formatCents(c.price_cents)}
          <span class="text-xs text-slate-500" title="Match confidence">${c.confidence}%</span></div>
        ${c.unit_note ? html`<div class="text-xs text-slate-500">${c.unit_note}</div>` : nothing}
      </td>
      <td class="px-3 py-2 font-mono text-sm text-white">
        ${r.diff_pct != null ? signedPct(r.diff_pct) : ''} <span class="text-slate-400">${signedMoney(diffCents)}</span>
      </td>
      <td class="px-3 py-2 text-sm text-white">${POSITION_WORDS[r.position] ?? ''}</td>
      <td class="px-3 py-2">
        ${r.suggested_price_cents != null
          ? html`<div class="font-mono text-white text-sm">${formatCents(r.suggested_price_cents)}</div>
                 <div class="text-xs text-slate-400">${reasonWords(r, res.band_pct, res.margin_floor_pct)}</div>`
          : html`<div class="text-xs text-slate-400">${r.note}</div>`}
      </td>
    </tr>`;
  }

  private _renderResult(res: MarketCheckResult) {
    const allFailed = res.rows.length > 0 && res.rows.every((r) => r.status === 'error');
    const someFailed = !allFailed && res.rows.some((r) => r.status === 'error');
    return html`
      <div class="space-y-3" data-testid="result">
        ${allFailed
          ? html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400 flex items-center gap-2" data-testid="all-failed">
              ${icon(AlertTriangle, 14)} None of the picked products could be read. Check the FB Connect settings in Tech Admin and try again.</div>`
          : html`<p class="text-white text-sm" data-testid="summary">${summarize(res)}</p>`}
        ${someFailed
          ? html`<p class="text-xs text-amber-400" data-testid="partial">Some products could not be read; the rest stand.</p>`
          : nothing}
        ${res.store_seen ? html`<p class="text-xs text-slate-500">Store read: ${res.store_seen}</p>` : nothing}
        <div class="bg-slate-steel border border-white/10 rounded-lg overflow-x-auto">
          <table class="w-full text-left">
            <thead class="text-xs uppercase tracking-wider text-slate-400">
              <tr>
                <th class="px-3 py-2">Product</th>
                <th class="px-3 py-2 text-right">Our price</th>
                <th class="px-3 py-2">${COMPETITOR}</th>
                <th class="px-3 py-2">Difference</th>
                <th class="px-3 py-2">Position</th>
                <th class="px-3 py-2">Suggested</th>
              </tr>
            </thead>
            <tbody>${res.rows.map((r) => this._renderRow(r, res))}</tbody>
          </table>
        </div>
        <p class="text-xs text-slate-500">${COMPETITOR} prices are US list prices; no exchange is applied.</p>
      </div>
    `;
  }

  render() {
    let body;
    if (this._loading) {
      body = html`<p class="text-slate-400" data-testid="loading">Loading Market check</p>`;
    } else if (this._loadError) {
      body = html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400">${this._loadError}</div>`;
    } else if (!this._settings?.configured) {
      body = this._renderNotConfigured();
    } else {
      body = html`
        ${this._renderPick()}
        ${this._runError ? html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400" data-testid="run-error">${this._runError}</div>` : nothing}
        ${this._running ? this._renderRunning() : this._result ? this._renderResult(this._result) : nothing}
      `;
    }
    return html`
      <div class="min-h-screen bg-deep-space p-8 space-y-6">
        ${this._header()}
        ${body}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-market-check': GableMarketCheck;
  }
}
