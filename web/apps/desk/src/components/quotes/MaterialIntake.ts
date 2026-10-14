// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons';
import { Upload, Check, X, AlertTriangle, Loader2, Package, ShieldAlert, ListChecks } from 'lucide';
import type { MatchedProduct, ParseResponse, ParsedItem } from '../../types/parsing';
import { ParsingService } from '../../services/parsing.service';

/** A line at or above this confidence is accepted by "Accept confident lines". */
const CONFIDENT = 0.9;

/**
 * The material list intake that lives inside the quote builder's Products
 * pane: paste a list or upload a file, review each parsed line beside its
 * original text, then accept, swap to an alternative or reject it.
 *
 * Events: `parse-complete` (the ParseResponse, for the quote's parse map) and
 * `accept-lines` (the accepted ParsedItem[], swaps applied).
 */
@customElement('gable-material-intake')
export class GableMaterialIntake extends LitElement {
  createRenderRoot() { return this; }

  @property({ type: Boolean }) disabled = false;

  @state() private _pasted = '';
  @state() private _busy = false;
  @state() private _error: string | null = null;
  @state() private _result: ParseResponse | null = null;
  @state() private _items: ParsedItem[] = [];
  @state() private _open: Set<number> = new Set();
  @state() private _expanded = false;

  private async _run(task: () => Promise<ParseResponse>) {
    this._busy = true;
    this._error = null;
    try {
      const result = await task();
      this._result = result;
      this._items = [...result.items];
      this._open = new Set();
      this._expanded = true;
      this.dispatchEvent(new CustomEvent('parse-complete', { detail: result, bubbles: true, composed: true }));
    } catch (err) {
      this._error = err instanceof Error ? err.message : 'Failed to parse the material list';
    } finally {
      this._busy = false;
    }
  }

  private _parsePasted() {
    if (this._pasted.trim() === '') return;
    void this._run(() => ParsingService.uploadPastedList(this._pasted));
  }

  private _onFile(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;
    void this._run(() => ParsingService.uploadMaterialList(file)).finally(() => { input.value = ''; });
  }

  private _emitAccept(accepted: ParsedItem[]) {
    // A sample list is not the caller's file: it never joins a quote.
    if (this._result?.synthetic) return;
    if (accepted.length === 0) return;
    this.dispatchEvent(new CustomEvent('accept-lines', { detail: accepted, bubbles: true, composed: true }));
  }

  private _accept(index: number) {
    if (this._result?.synthetic) return;
    this._emitAccept([this._items[index]]);
    this._items = this._items.filter((_, i) => i !== index);
    this._open = new Set();
  }

  private _acceptConfident() {
    if (this._result?.synthetic) return;
    const keep: ParsedItem[] = [];
    const accepted: ParsedItem[] = [];
    for (const item of this._items) {
      if (item.matched_product && !item.is_special_order && item.confidence >= CONFIDENT) accepted.push(item);
      else keep.push(item);
    }
    this._emitAccept(accepted);
    this._items = keep;
    this._open = new Set();
  }

  private _reject(index: number) {
    this._items = this._items.filter((_, i) => i !== index);
    this._open = new Set();
  }

  private _swap(index: number, alt: MatchedProduct) {
    if (this._result?.synthetic) return;
    const item = { ...this._items[index] };
    const rest = (item.alternatives ?? []).filter((a) => a.product_id !== alt.product_id);
    if (item.matched_product) rest.push(item.matched_product);
    item.matched_product = alt;
    item.alternatives = rest;
    item.is_special_order = false;
    const next = [...this._items];
    next[index] = item;
    this._items = next;
    this._open = new Set();
  }

  private _toggleAlts(index: number) {
    const next = new Set(this._open);
    if (next.has(index)) next.delete(index); else next.add(index);
    this._open = next;
  }

  private _badge(item: ParsedItem) {
    if (item.is_special_order || !item.matched_product) {
      return html`<span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-bold uppercase tracking-wider bg-rose-500/15 text-rose-400 border border-rose-500/20">${icon(AlertTriangle, 10)} Special order</span>`;
    }
    const pct = Math.round(item.confidence * 100);
    const tone = item.confidence >= CONFIDENT
      ? 'bg-emerald-500/15 text-emerald-400 border-emerald-500/20'
      : 'bg-amber-500/15 text-amber-400 border-amber-500/20';
    return html`<span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-bold font-mono tracking-wider border ${tone}">${icon(item.confidence >= CONFIDENT ? Check : AlertTriangle, 10)} ${pct}%</span>`;
  }

  private _line(item: ParsedItem, idx: number) {
    const alts = item.alternatives ?? [];
    const sample = this._result?.synthetic === true;
    return html`
      <li data-testid="intake-line" class="rounded-lg border border-white/10 bg-black/20 p-3">
        <div class="flex flex-wrap items-center gap-2 mb-2">
          ${this._badge(item)}
          <span class="text-xs text-zinc-500 font-mono truncate min-w-0 flex-1" title="Original line">${item.raw_text}</span>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          ${item.matched_product ? html`
            ${icon(Package, 16, 'text-zinc-500 shrink-0')}
            <div class="min-w-0 flex-1">
              <div class="font-mono text-white text-sm">${item.matched_product.sku}</div>
              <div class="text-xs text-zinc-400 truncate">${item.matched_product.description}</div>
            </div>
          ` : html`
            ${icon(ShieldAlert, 16, 'text-rose-400 shrink-0')}
            <div class="min-w-0 flex-1 text-sm text-rose-300">No catalog match: order it as a special item</div>
          `}
          <div class="font-mono text-sm text-white shrink-0">${item.quantity} <span class="text-zinc-500 text-[10px]">${item.uom}</span></div>
          ${sample ? nothing : html`<div class="flex items-center gap-1.5 shrink-0">
            <button type="button" data-testid="intake-accept" @click=${() => this._accept(idx)}
              class="inline-flex items-center gap-1 rounded-md border border-gable-green/30 bg-gable-green/10 text-gable-green text-xs font-medium px-2.5 py-1.5 hover:bg-gable-green/20">
              ${icon(Check, 12)} Accept
            </button>
            <button type="button" data-testid="intake-reject" aria-label="Reject this line" @click=${() => this._reject(idx)}
              class="inline-flex items-center rounded-md border border-white/10 text-zinc-400 hover:text-white px-2 py-1.5">
              ${icon(X, 12)}
            </button>
          </div>`}
        </div>
        ${alts.length > 0 && !sample ? html`
          <div class="mt-2">
            <button type="button" data-testid="intake-alts" class="text-xs text-blueprint-blue hover:text-sky-300" @click=${() => this._toggleAlts(idx)}>
              ${alts.length} alternative${alts.length === 1 ? '' : 's'}
            </button>
            ${this._open.has(idx) ? html`
              <div class="mt-2 space-y-1.5 pl-3 border-l-2 border-blueprint-blue/20">
                ${alts.map((alt) => html`
                  <button type="button" data-testid="intake-swap" @click=${() => this._swap(idx, alt)}
                    class="w-full text-left p-2 rounded-lg bg-white/5 hover:bg-white/10 flex items-center justify-between gap-3">
                    <span><span class="font-mono text-xs text-zinc-200">${alt.sku}</span><span class="text-xs text-zinc-500 ml-2">${alt.description}</span></span>
                  </button>
                `)}
              </div>
            ` : nothing}
          </div>
        ` : nothing}
      </li>
    `;
  }

  render() {
    const result = this._result;
    const confident = this._items.filter((i) => i.matched_product && !i.is_special_order && i.confidence >= CONFIDENT).length;
    return html`
      <details data-testid="intake-section" ?open=${this._expanded}
        @toggle=${(e: Event) => { this._expanded = (e.target as HTMLDetailsElement).open; }}
        class="rounded-xl border border-white/10 bg-slate-steel/40">
      <summary class="cursor-pointer select-none p-4 text-sm font-medium text-white flex items-center gap-2">
        ${icon(ListChecks, 16, 'text-gable-green')} From a material list
      </summary>
      <section data-testid="material-intake" class="px-4 pb-4">
        <textarea
          rows="3"
          spellcheck="false"
          aria-label="Paste a material list"
          placeholder="Paste a list, one item per line: 50 pcs - 2x4x8 stud"
          class="w-full bg-deep-space border border-white/10 rounded-lg px-3 py-2 text-sm text-white font-mono focus:outline-none focus:ring-1 focus:ring-gable-green/50"
          .value=${this._pasted}
          @input=${(e: Event) => { this._pasted = (e.target as HTMLTextAreaElement).value; }}
        ></textarea>
        <div class="mt-2 flex flex-wrap items-center gap-2">
          <button type="button" data-testid="intake-parse-pasted" @click=${this._parsePasted}
            ?disabled=${this.disabled || this._busy || this._pasted.trim() === ''}
            class="inline-flex items-center rounded-lg text-sm font-medium bg-gable-green/10 text-gable-green border border-gable-green/30 px-3 py-2 hover:bg-gable-green/20 disabled:opacity-50">
            ${this._busy ? html`<span class="animate-spin mr-2 inline-flex">${icon(Loader2, 14)}</span>Reading...` : 'Read pasted list'}
          </button>
          <label class="inline-flex items-center rounded-lg text-sm font-medium border border-dashed border-white/20 text-zinc-300 px-3 py-2 hover:border-gable-green/40 cursor-pointer">
            ${icon(Upload, 14, 'mr-2')} Upload a file
            <input type="file" class="hidden" accept="image/*,.pdf,.xlsx,.xls,.csv,.txt" aria-label="Upload material list file"
              ?disabled=${this.disabled || this._busy} @change=${this._onFile} />
          </label>
        </div>
        ${this._error ? html`<p role="alert" class="mt-2 text-xs text-rose-400">${this._error}</p>` : nothing}

        ${result ? html`
          ${result.synthetic ? html`
            <div data-testid="intake-sample-banner" class="mt-3 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3">
              <p class="text-sm font-semibold text-amber-300">No AI reader is set up on this server: these lines are a sample list</p>
              <p class="mt-1 text-xs text-amber-200/80">They were not read from your file, so they cannot be added to the quote. To read files and photos, an administrator turns AI on in Tech Admin, AI settings. A pasted text list is read without it.</p>
            </div>
          ` : nothing}
          <div class="mt-3 flex flex-wrap items-center justify-between gap-2">
            <p class="text-xs text-zinc-500">${this._items.length} line${this._items.length === 1 ? '' : 's'} to review</p>
            ${!result.synthetic && confident > 0 ? html`
              <button type="button" data-testid="intake-accept-confident" @click=${this._acceptConfident}
                class="inline-flex items-center rounded-lg text-xs font-medium border border-gable-green/30 bg-gable-green/10 text-gable-green px-3 py-1.5 hover:bg-gable-green/20">
                ${icon(Check, 12, 'mr-1.5')} Accept confident lines (${confident})
              </button>
            ` : nothing}
          </div>
          <ul class="mt-2 space-y-2">${this._items.map((item, idx) => this._line(item, idx))}</ul>
        ` : nothing}
      </section>
      </details>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-material-intake': GableMaterialIntake;
  }
}
