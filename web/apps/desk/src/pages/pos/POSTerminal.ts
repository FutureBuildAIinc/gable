// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state, query } from 'lit/decorators.js';
import { posService, dollarsToCents, type TenderIn } from '../../services/POSService';
import type { Sale, QuickSearchResult, SaleLine, TillSession, TillReport, TenderMethod, RefundMethod, CounterCustomer } from '../../types/pos';
import { chargeRefusal, standingOf, money } from './standing';
import '../../components/BarcodeScanner.ts';

const REGISTER_ID = 'REG-01';
const fmt = (cents: number) => `$${(cents / 100).toFixed(2)}`;
/** A scale 4 price to dollars, for the display only. */
const fmtPrice = (tenThousandths: number) => `$${(tenThousandths / 10000).toFixed(2)}`;
const METHODS: TenderMethod[] = ['cash', 'check', 'card', 'account'];

/**
 * POSTerminal -- Full-screen retail counter sales interface on the wire
 * contract: cents tenders (a sale can split them across methods), a void of
 * the completed sale while the drawer is open, and a return that posts a
 * credit memo and refunds out of the drawer.
 */
@customElement('gable-pos-terminal')
export class POSTerminal extends LitElement {
  createRenderRoot() { return this; }

  @state() private _sale: Sale | null = null;
  @state() private _searchQuery = '';
  @state() private _searchResults: QuickSearchResult[] = [];
  @state() private _showTender = false;
  @state() private _pendingTenders: TenderIn[] = [];
  @state() private _tenderMethod: TenderMethod = 'cash';
  @state() private _tenderAmount = '';
  @state() private _loading = false;
  @state() private _error: string | null = null;
  @state() private _success: string | null = null;
  @state() private _isScanning = false;

  // The sale's customer: the lookup, the account's standing and what a charge
  // to the account carries (the purchase order and the job).
  @state() private _customer: CounterCustomer | null = null;
  @state() private _custQuery = '';
  @state() private _custResults: CounterCustomer[] = [];
  @state() private _poText = '';
  @state() private _jobId = '';

  // The completed sale offered for a return or a void.
  @state() private _completed: Sale | null = null;
  @state() private _showReturn = false;
  @state() private _returnReason = 'wrong material';
  @state() private _returnMethod: RefundMethod = 'cash';
  @state() private _returnQty: Record<string, string> = {};

  // Till (drawer) state
  @state() private _till: TillSession | null = null;
  @state() private _showTillOpen = false;
  @state() private _openingFloat = '';
  @state() private _showTillClose = false;
  @state() private _tillReport: TillReport | null = null;
  @state() private _counts: Record<string, string> = {};
  @state() private _closeResult: TillReport | null = null;
  @state() private _tillBusy = false;

  @query('#pos-search-input') private _searchInput!: HTMLInputElement;

  private _newTxTimer: ReturnType<typeof setTimeout> | null = null;
  private _errorTimer: ReturnType<typeof setTimeout> | null = null;
  private _searchDebounce: ReturnType<typeof setTimeout> | null = null;
  private _custDebounce: ReturnType<typeof setTimeout> | null = null;

  connectedCallback() {
    super.connectedCallback();
    void this._loadTill();
    this._startNewSale();
  }

  /* ---- Till (drawer) lifecycle ---- */

  private async _loadTill() {
    try {
      this._till = await posService.currentTill(REGISTER_ID);
    } catch {
      this._till = null;
    }
  }

  private async _openTill() {
    const cents = dollarsToCents(this._openingFloat);
    if (isNaN(cents) || cents < 0) {
      this._error = 'Enter a valid opening float';
      return;
    }
    try {
      this._tillBusy = true;
      this._till = await posService.openTill(REGISTER_ID, cents);
      this._showTillOpen = false;
      this._openingFloat = '';
      this._success = `Till opened with ${fmt(this._till.opening_float_cents)} float`;
      this._errorTimer = setTimeout(() => { this._success = null; }, 2500);
      // Re-start the sale so it attaches to the new session.
      void this._startNewSale();
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to open till';
    } finally {
      this._tillBusy = false;
    }
  }

  private async _beginCloseTill() {
    if (!this._till) return;
    try {
      this._tillBusy = true;
      this._tillReport = await posService.tillReport(this._till.id);
      // Seed blind-count inputs for every method the drawer expects (+ cash).
      const methods = new Set<string>(['cash', ...Object.keys(this._tillReport.expected_by_method || {})]);
      const seed: Record<string, string> = {};
      methods.forEach((m) => { seed[m] = ''; });
      this._counts = seed;
      this._closeResult = null;
      this._showTillClose = true;
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to load till report';
    } finally {
      this._tillBusy = false;
    }
  }

  private async _confirmCloseTill() {
    if (!this._till) return;
    const counted: Record<string, number> = {};
    for (const [method, val] of Object.entries(this._counts)) {
      const cents = dollarsToCents(val);
      if (!isNaN(cents)) counted[method] = cents;
    }
    try {
      this._tillBusy = true;
      this._closeResult = await posService.closeTill(this._till.id, counted, '');
      this._till = null; // session closed
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to close till';
    } finally {
      this._tillBusy = false;
    }
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    if (this._newTxTimer) clearTimeout(this._newTxTimer);
    if (this._errorTimer) clearTimeout(this._errorTimer);
    if (this._searchDebounce) clearTimeout(this._searchDebounce);
    if (this._custDebounce) clearTimeout(this._custDebounce);
  }

  updated(changed: Map<string, unknown>) {
    if (changed.has('_searchQuery')) {
      this._debounceSearch();
    }
    if (changed.has('_custQuery')) {
      this._debounceCustomers();
    }
  }

  /* ---- Barcode scanning ---- */

  private async _handleScan(barcode: string) {
    try {
      const results = await posService.searchProducts(barcode);
      if (results && results.length > 0) {
        const exactMatch = results.find(r => r.sku === barcode || r.product_id === barcode) || results[0];
        await this._addItem(exactMatch);
      } else {
        this._error = `Product not found for barcode: ${barcode}`;
        this._errorTimer = setTimeout(() => { this._error = null; }, 3000);
      }
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Error scanning barcode';
    }
  }

  /* ---- Product search with debounce ---- */

  private _debounceSearch() {
    if (this._searchDebounce) clearTimeout(this._searchDebounce);

    if (this._searchQuery.length < 2) {
      this._searchResults = [];
      return;
    }

    this._searchDebounce = setTimeout(async () => {
      try {
        this._searchResults = await posService.searchProducts(this._searchQuery);
      } catch {
        this._searchResults = [];
      }
    }, 200);
  }

  /* ---- The sale's customer ---- */

  private _debounceCustomers() {
    if (this._custDebounce) clearTimeout(this._custDebounce);
    if (this._custQuery.trim().length < 2) {
      this._custResults = [];
      return;
    }
    this._custDebounce = setTimeout(async () => {
      try {
        this._custResults = await posService.findCustomers(this._custQuery);
      } catch {
        this._custResults = [];
      }
    }, 200);
  }

  /** Names the customer on the open sale; the lines then price for them. */
  private async _chooseCustomer(c: CounterCustomer) {
    if (!this._sale) return;
    try {
      this._error = null;
      this._sale = await posService.attachCustomer(this._sale.id, { customer_id: c.id });
      this._customer = c;
      this._custQuery = '';
      this._custResults = [];
      this._poText = '';
      this._jobId = '';
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to name the customer';
    }
  }

  /** Back to a walk-in sale (only before the first line). */
  private async _clearCustomer() {
    if (!this._sale) return;
    try {
      this._error = null;
      this._sale = await posService.attachCustomer(this._sale.id, { customer_id: null });
      this._customer = null;
      this._poText = '';
      this._jobId = '';
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to clear the customer';
    }
  }

  /** Saves the purchase order and the job the charge will carry. */
  private async _saveAccountRefs() {
    if (!this._sale || !this._customer) return;
    try {
      this._error = null;
      this._sale = await posService.attachCustomer(this._sale.id, {
        customer_id: this._customer.id,
        customer_po: this._poText.trim() || null,
        job_id: this._jobId || null,
      });
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to save the purchase order';
    }
  }

  /** Why the charge to the account is not offered, or null when it is. */
  private get _accountRefusal(): string | null {
    // What is left to charge once the cash, check or card tenders are taken.
    const owed = (this._sale?.total_cents ?? 0) - this._tenderedSoFar;
    return chargeRefusal(this._customer, Math.max(owed, 0), this._poText);
  }

  private _renderCustomerPanel() {
    const sale = this._sale;
    const hasLines = (sale?.lines.length ?? 0) > 0;
    const c = this._customer;
    const toneClass = (tone: string) =>
      tone === 'stop' ? 'text-safety-red' : tone === 'warn' ? 'text-amber-400' : 'text-gable-green';
    if (c) {
      const st = standingOf(c);
      return html`
        <div id="pos-customer" class="mb-4 rounded-lg border border-white/10 bg-deep-space p-3" data-testid="pos-customer">
          <div class="flex items-start justify-between gap-2">
            <div>
              <div class="text-sm font-semibold text-white" data-testid="pos-customer-name">${c.name}</div>
              <div class="font-mono text-xs text-slate-400">${c.account_number}</div>
            </div>
            <button
              @click=${() => this._clearCustomer()}
              ?disabled=${hasLines || this._loading}
              title=${hasLines ? 'The customer is named before the first line: start a new sale to change it' : 'Back to a walk-in sale'}
              class="rounded border border-white/10 px-2 py-1 text-xs text-slate-300 disabled:cursor-not-allowed disabled:opacity-40"
            >Change</button>
          </div>
          <div class="mt-2 font-mono text-xs font-semibold ${toneClass(st.tone)}" data-testid="pos-customer-standing">${st.text}</div>
          ${c.standing === 'good' || c.standing === 'over_limit' ? html`
            <div class="mt-1 font-mono text-xs text-slate-400">Open ${money(c.open_receivable_cents)}</div>` : nothing}
          <label class="mt-3 block text-xs text-slate-400" for="pos-po">
            Purchase order${c.po_required ? html` <span class="text-safety-red">(required for a charge)</span>` : ''}
          </label>
          <input id="pos-po" type="text" maxlength="100" .value=${this._poText}
            @input=${(e: Event) => { this._poText = (e.target as HTMLInputElement).value; }}
            @change=${() => this._saveAccountRefs()}
            class="mt-1 w-full rounded border border-white/10 bg-slate-steel px-2 py-2 font-mono text-sm text-white outline-none"
            placeholder="PO number" />
          ${c.jobs.length > 0 ? html`
            <label class="mt-3 block text-xs text-slate-400" for="pos-job">Job</label>
            <select id="pos-job" .value=${this._jobId}
              @change=${(e: Event) => { this._jobId = (e.target as HTMLSelectElement).value; void this._saveAccountRefs(); }}
              class="mt-1 w-full rounded border border-white/10 bg-slate-steel px-2 py-2 text-sm text-white outline-none">
              <option value="">No job</option>
              ${c.jobs.map((j) => html`<option value=${j.id} ?selected=${j.id === this._jobId}>${j.name}</option>`)}
            </select>` : nothing}
        </div>`;
    }
    return html`
      <div id="pos-customer" class="mb-4 rounded-lg border border-white/10 bg-deep-space p-3" data-testid="pos-customer">
        <div class="flex items-center justify-between">
          <span class="text-xs font-semibold uppercase tracking-wide text-slate-400">Customer</span>
          <span class="text-xs text-slate-400">Walk-in sale</span>
        </div>
        <input id="pos-customer-search" type="text" autocomplete="off"
          placeholder="Find by name or account number"
          aria-label="Find a customer by name or account number"
          .value=${this._custQuery}
          ?disabled=${hasLines}
          @input=${(e: Event) => { this._custQuery = (e.target as HTMLInputElement).value; }}
          class="mt-2 w-full rounded border border-white/10 bg-slate-steel px-2 py-2 text-sm text-white outline-none disabled:opacity-40" />
        ${hasLines ? html`<div class="mt-1 text-xs text-slate-400">Name the customer before the first line.</div>` : nothing}
        ${this._custResults.length > 0 ? html`
          <div class="mt-2 max-h-56 overflow-y-auto rounded border border-white/10" data-testid="pos-customer-results">
            ${this._custResults.map((r) => {
              const st = standingOf(r);
              return html`
                <button @click=${() => this._chooseCustomer(r)}
                  class="block w-full border-b border-white/10 bg-slate-steel px-3 py-2 text-left last:border-b-0 hover:bg-white/5">
                  <span class="text-sm text-white">${r.name}</span>
                  <span class="ml-2 font-mono text-xs text-slate-400">${r.account_number}</span>
                  <span class="mt-0.5 block text-xs ${toneClass(st.tone)}">${st.text}</span>
                </button>`;
            })}
          </div>` : nothing}
      </div>`;
  }

  /* ---- Sale management ---- */

  private async _startNewSale() {
    try {
      this._loading = true;
      this._error = null;
      this._success = null;
      this._pendingTenders = [];
      this._customer = null;
      this._custQuery = '';
      this._custResults = [];
      this._poText = '';
      this._jobId = '';
      // The last completed sale stays offerable for a void or a return
      // until the next completion replaces it.
      const sale = await posService.startSale(REGISTER_ID);
      this._sale = sale;
      this._showTender = false;
      // Focus the search input after render
      this.updateComplete.then(() => {
        this._searchInput?.focus();
      });
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to start the sale';
    } finally {
      this._loading = false;
    }
  }

  private async _addItem(product: QuickSearchResult) {
    if (!this._sale) return;
    try {
      const updated = await posService.addItem(this._sale.id, product.product_id, '1', product.uom);
      this._sale = updated;
      this._searchQuery = '';
      this._searchResults = [];
      this._searchInput?.focus();
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to add item';
    }
  }

  private async _removeItem(itemId: string) {
    if (!this._sale) return;
    try {
      this._sale = await posService.removeItem(this._sale.id, itemId);
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to remove item';
    }
  }

  /* ---- Tenders (a split tender accumulates before it completes) ---- */

  private get _tenderedSoFar(): number {
    return this._pendingTenders.reduce((sum, t) => sum + t.amount_cents, 0);
  }

  private _handleTender(method: TenderMethod) {
    if (!this._sale) return;
    if (method === 'account') {
      const refusal = this._accountRefusal;
      if (refusal) {
        this._error = refusal;
        return;
      }
    }
    this._tenderMethod = method;
    const remaining = this._sale.total_cents - this._tenderedSoFar;
    this._tenderAmount = (Math.max(remaining, 0) / 100).toFixed(2);
    this._showTender = true;
  }

  /** Add the entered tender to the pending list; a split names several. */
  private _addPendingTender() {
    const cents = dollarsToCents(this._tenderAmount);
    if (isNaN(cents) || cents <= 0) {
      this._error = 'Invalid tender amount';
      return;
    }
    this._pendingTenders = [...this._pendingTenders, { method: this._tenderMethod, amount_cents: cents }];
    this._showTender = false;
    this._tenderAmount = '';
  }

  private async _completeSale() {
    if (!this._sale || this._pendingTenders.length === 0) return;
    try {
      this._loading = true;
      this._error = null;
      const completed = await posService.completeSale(this._sale.id, this._sale.revision, this._pendingTenders);
      this._sale = completed;
      this._completed = completed;
      const change = completed.change_cents || 0;
      this._success = change > 0
        ? `Sale ${completed.number} complete - ${fmt(completed.total_cents)} \u00b7 CHANGE DUE ${fmt(change)}`
        : `Sale ${completed.number} complete - ${fmt(completed.total_cents)}`;
      this._pendingTenders = [];
      this._showTender = false;

      // Auto-start the next sale after 2 seconds
      this._newTxTimer = setTimeout(() => {
        this._startNewSale();
      }, 2000);
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to complete sale';
    } finally {
      this._loading = false;
    }
  }

  private async _voidSale() {
    const sale = this._completed ?? this._sale;
    if (!sale || sale.status !== 'completed') {
      this._error = 'Only a completed sale is voided, while its drawer is open';
      return;
    }
    const reason = window.prompt('Void reason?');
    if (!reason) return;
    try {
      const voided = await posService.voidSale(sale.id, sale.revision, reason);
      this._sale = voided;
      this._completed = null;
      this._success = `Sale ${voided.number} voided - the ledger and the stock are whole again`;
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to void the sale';
    }
  }

  /* ---- Returns ---- */

  private _beginReturn() {
    const sale = this._completed ?? this._sale;
    if (!sale || sale.status !== 'completed') {
      this._error = 'Return needs a completed sale';
      return;
    }
    this._completed = sale;
    const qty: Record<string, string> = {};
    sale.lines.filter(l => l.line_type === 'product' && l.parent_line_id === null).forEach(l => { qty[l.id] = '0'; });
    this._returnQty = qty;
    this._showReturn = true;
  }

  private async _confirmReturn() {
    const sale = this._completed;
    if (!sale) return;
    const lines = Object.entries(this._returnQty)
      .filter(([, q]) => Number(q) > 0)
      .map(([line_id, q]) => ({ line_id, quantity: q, restock: true }));
    if (lines.length === 0) {
      this._error = 'Name at least one line and quantity to return';
      return;
    }
    try {
      this._loading = true;
      const ret = await posService.createReturn({
        register_id: REGISTER_ID,
        original_sale_id: sale.id,
        customer_id: sale.customer_id ?? undefined,
        refund_method: this._returnMethod,
        reason: this._returnReason,
        lines,
      });
      this._showReturn = false;
      this._success = `Return ${ret.number} complete - ${fmt(ret.total_cents)} refunded ${ret.refund_method}`;
      this._errorTimer = setTimeout(() => { this._success = null; }, 4000);
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to take the return';
    } finally {
      this._loading = false;
    }
  }

  /* ---- Modals ---- */

  private _overlay(inner: unknown) {
    return html`
      <div style="position:fixed;inset:0;background:rgba(0,0,0,0.6);display:flex;align-items:center;justify-content:center;z-index:50">
        <div style="width:440px;max-width:92vw;background:#161b22;border:1px solid #30363d;border-radius:14px;padding:24px;color:#e6edf3;font-family:'Outfit',-apple-system,sans-serif">
          ${inner}
        </div>
      </div>
    `;
  }

  private _renderTillOpenModal() {
    if (!this._showTillOpen) return nothing;
    return this._overlay(html`
      <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">Open Till</h2>
      <p style="margin:0 0 16px;font-size:13px;color:#8b949e">Count the starting cash in the drawer and enter the opening float.</p>
      <label style="display:block;font-size:12px;color:#8b949e;margin-bottom:6px">Opening float ($)</label>
      <input type="number" step="0.01" .value=${this._openingFloat}
        @input=${(e: Event) => { this._openingFloat = (e.target as HTMLInputElement).value; }}
        style="width:100%;box-sizing:border-box;padding:14px;background:#0d1117;border:2px solid #8957e5;border-radius:8px;color:#e6edf3;font-size:22px;font-weight:700;text-align:center;outline:none" placeholder="200.00" />
      <div style="display:flex;gap:8px;margin-top:20px">
        <button @click=${() => { this._showTillOpen = false; }} style="flex:1;padding:12px;background:transparent;border:1px solid #30363d;border-radius:8px;color:#8b949e;font-size:14px;cursor:pointer">Cancel</button>
        <button @click=${() => this._openTill()} ?disabled=${this._tillBusy} style="flex:2;padding:12px;background:#8957e5;border:none;border-radius:8px;color:#fff;font-size:15px;font-weight:700;cursor:pointer">${this._tillBusy ? 'Opening\u2026' : 'Open Till'}</button>
      </div>
    `);
  }

  private _renderTillCloseModal() {
    if (!this._showTillClose) return nothing;
    const close = () => { this._showTillClose = false; this._tillReport = null; this._closeResult = null; };

    // Result view (Z-report): expected vs counted vs over/short.
    if (this._closeResult) {
      const r = this._closeResult;
      const os = r.session.over_short_cents ?? 0;
      const osColor = os === 0 ? '#3fb950' : os < 0 ? '#f85149' : '#d29922';
      const osLabel = os === 0 ? 'BALANCED' : os < 0 ? `SHORT ${fmt(Math.abs(os))}` : `OVER ${fmt(os)}`;
      return this._overlay(html`
        <h2 style="margin:0 0 12px;font-size:18px;font-weight:700">Z-Report - Till Closed</h2>
        <div style="text-align:center;padding:16px;background:#0d1117;border:1px solid #30363d;border-radius:10px;margin-bottom:16px">
          <div style="font-size:12px;color:#8b949e;text-transform:uppercase;letter-spacing:0.5px">Over / Short</div>
          <div style="font-size:32px;font-weight:800;color:${osColor}">${osLabel}</div>
        </div>
        <table style="width:100%;border-collapse:collapse;font-size:13px">
          <thead><tr style="color:#8b949e;text-align:right">
            <th style="text-align:left;padding:4px 0">Method</th><th>Expected</th><th>Counted</th><th>\u0394</th>
          </tr></thead>
          <tbody>
            ${Object.keys(r.session.expected_by_method || {}).map((m) => {
              const exp = r.session.expected_by_method?.[m] ?? 0;
              const cnt = r.session.counted_by_method?.[m] ?? 0;
              const d = cnt - exp;
              return html`<tr style="text-align:right;border-top:1px solid #21262d">
                <td style="text-align:left;padding:6px 0">${m}</td>
                <td>${fmt(exp)}</td><td>${fmt(cnt)}</td>
                <td style="color:${d === 0 ? '#8b949e' : d < 0 ? '#f85149' : '#d29922'}">${fmt(d)}</td>
              </tr>`;
            })}
          </tbody>
        </table>
        <div style="font-size:12px;color:#8b949e;margin-top:14px">${r.sale_count} sales \u00b7 ${fmt(r.sales_total_cents)} rung \u00b7 ${fmt(r.tax_total_cents)} tax \u00b7 ${fmt(r.change_cents)} change given</div>
        <button @click=${close} style="width:100%;margin-top:18px;padding:12px;background:#238636;border:none;border-radius:8px;color:#fff;font-size:15px;font-weight:700;cursor:pointer">Done</button>
      `);
    }

    // Count view (blind — expected figures hidden until close posts).
    const rep = this._tillReport;
    const methods = Object.keys(this._counts);
    return this._overlay(html`
      <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">Close Till - Blind Count</h2>
      <p style="margin:0 0 16px;font-size:13px;color:#8b949e">
        Count the drawer and enter actuals by tender. Expected totals stay hidden until you post - that's the blind count.
        ${rep ? html`<br>${rep.sale_count} sales rung this session.` : nothing}
      </p>
      ${methods.map((m) => html`
        <div style="display:flex;align-items:center;gap:10px;margin-bottom:10px">
          <label style="width:90px;font-size:13px;color:#c9d1d9;text-transform:capitalize">${m}</label>
          <input type="number" step="0.01" .value=${this._counts[m]}
            @input=${(e: Event) => { this._counts = { ...this._counts, [m]: (e.target as HTMLInputElement).value }; }}
            style="flex:1;box-sizing:border-box;padding:10px;background:#0d1117;border:1px solid #30363d;border-radius:8px;color:#e6edf3;font-size:16px;text-align:right;outline:none" placeholder="0.00" />
        </div>
      `)}
      <div style="display:flex;gap:8px;margin-top:20px">
        <button @click=${close} style="flex:1;padding:12px;background:transparent;border:1px solid #30363d;border-radius:8px;color:#8b949e;font-size:14px;cursor:pointer">Cancel</button>
        <button @click=${() => this._confirmCloseTill()} ?disabled=${this._tillBusy} style="flex:2;padding:12px;background:#8957e5;border:none;border-radius:8px;color:#fff;font-size:15px;font-weight:700;cursor:pointer">${this._tillBusy ? 'Posting\u2026' : 'Post Count & Close'}</button>
      </div>
    `);
  }

  private _renderReturnModal() {
    if (!this._showReturn || !this._completed) return nothing;
    const sale = this._completed;
    const productLines = sale.lines.filter(l => l.line_type === 'product' && l.parent_line_id === null);
    return this._overlay(html`
      <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">Return - ${sale.number}</h2>
      <p style="margin:0 0 16px;font-size:13px;color:#8b949e">Name the lines coming back. The return posts a credit memo, restocks what returns, and refunds out of the drawer.</p>
      ${productLines.map((l) => html`
        <div style="display:flex;align-items:center;gap:10px;margin-bottom:8px">
          <span style="flex:1;font-size:13px;color:#c9d1d9">${l.description}</span>
          <span style="font-size:12px;color:#8b949e">of ${l.quantity}</span>
          <input type="number" min="0" step="0.25" .value=${this._returnQty[l.id] ?? '0'}
            @input=${(e: Event) => { this._returnQty = { ...this._returnQty, [l.id]: (e.target as HTMLInputElement).value }; }}
            style="width:80px;padding:8px;background:#0d1117;border:1px solid #30363d;border-radius:8px;color:#e6edf3;font-size:14px;text-align:right;outline:none" />
        </div>
      `)}
      <div style="display:flex;align-items:center;gap:10px;margin:14px 0">
        <label style="font-size:13px;color:#8b949e">Refund</label>
        <select .value=${this._returnMethod}
          @change=${(e: Event) => { this._returnMethod = (e.target as HTMLSelectElement).value as RefundMethod; }}
          style="flex:1;padding:10px;background:#0d1117;border:1px solid #30363d;border-radius:8px;color:#e6edf3;font-size:14px;outline:none">
          <option value="cash">Cash (out of the drawer)</option>
          <option value="card">Card (back on the card)</option>
          <option value="account">Account credit</option>
        </select>
      </div>
      <input type="text" .value=${this._returnReason}
        @input=${(e: Event) => { this._returnReason = (e.target as HTMLInputElement).value; }}
        style="width:100%;box-sizing:border-box;padding:10px;background:#0d1117;border:1px solid #30363d;border-radius:8px;color:#e6edf3;font-size:14px;outline:none;margin-bottom:16px" placeholder="Reason" />
      <div style="display:flex;gap:8px">
        <button @click=${() => { this._showReturn = false; }} style="flex:1;padding:12px;background:transparent;border:1px solid #30363d;border-radius:8px;color:#8b949e;font-size:14px;cursor:pointer">Cancel</button>
        <button @click=${() => this._confirmReturn()} ?disabled=${this._loading} style="flex:2;padding:12px;background:#238636;border:none;border-radius:8px;color:#fff;font-size:15px;font-weight:700;cursor:pointer">${this._loading ? 'Posting\u2026' : 'Take the Return'}</button>
      </div>
    `);
  }

  /* ---- Render ---- */

  render() {
    const sale = this._sale;
    const totalDollars = sale ? (sale.total_cents / 100).toFixed(2) : '0.00';
    const subtotalDollars = sale ? (sale.subtotal_cents / 100).toFixed(2) : '0.00';
    const taxDollars = sale ? (sale.tax_cents / 100).toFixed(2) : '0.00';
    const lines: SaleLine[] = sale?.lines ?? [];
    const productLines = lines.filter(l => l.parent_line_id === null);
    const remaining = sale ? sale.total_cents - this._tenderedSoFar : 0;

    return html`
      <div style="display:flex;flex-direction:column;height:100vh;background:#0d1117;color:#e6edf3;font-family:'Outfit',-apple-system,sans-serif">
        <!-- Header -->
        <div style="display:flex;justify-content:space-between;align-items:center;padding:12px 24px;background:#161b22;border-bottom:1px solid #21262d">
          <div style="display:flex;align-items:center;gap:12px">
            <h1 style="font-size:18px;font-weight:700;margin:0;color:#e6edf3">POS Terminal</h1>
            <span style="font-size:11px;padding:2px 8px;background:#238636;border-radius:12px;color:#fff;font-weight:600">REG-01</span>
            ${this._till ? html`
              <span title="Drawer open" style="font-size:11px;padding:2px 8px;background:#8957e5;border-radius:12px;color:#fff;font-weight:600">
                \u25cf TILL OPEN \u00b7 float ${fmt(this._till.opening_float_cents)}
              </span>
            ` : html`
              <span style="font-size:11px;padding:2px 8px;background:#6e2f2f;border-radius:12px;color:#ffb4b4;font-weight:600">
                \u25cb NO TILL
              </span>
            `}
            ${sale ? html`
              <span style="font-size:11px;padding:2px 8px;background:#1f6feb;border-radius:12px;color:#fff;font-family:monospace">
                ${sale.number} \u00b7 ${sale.status}
              </span>
            ` : nothing}
          </div>
          <div style="display:flex;gap:8px">
            ${this._till ? html`
              <button @click=${() => this._beginCloseTill()} ?disabled=${this._tillBusy} style="padding:6px 16px;background:#21262d;border:1px solid #8957e5;border-radius:6px;color:#d2a8ff;font-size:13px;cursor:pointer">
                Close Till \u00b7 Z-Report
              </button>
            ` : html`
              <button @click=${() => { this._showTillOpen = true; }} style="padding:6px 16px;background:#8957e5;border:none;border-radius:6px;color:#fff;font-size:13px;font-weight:600;cursor:pointer">
                Open Till
              </button>
            `}
            <button @click=${() => this._startNewSale()} style="padding:6px 16px;background:#21262d;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:13px;cursor:pointer">
              New Sale
            </button>
          </div>
        </div>

        ${this._renderTillOpenModal()}
        ${this._renderTillCloseModal()}
        ${this._renderReturnModal()}

        <!-- Alerts -->
        ${this._error ? html`
          <div style="padding:10px 16px;background:#3d1114;border-bottom:1px solid #f8514940;color:#f85149;font-size:13px;display:flex;justify-content:space-between;align-items:center">
            ${this._error}
            <button @click=${() => { this._error = null; }} style="background:none;border:none;color:#f85149;font-size:16px;cursor:pointer" aria-label="Dismiss error">\u00d7</button>
          </div>
        ` : nothing}
        ${this._success ? html`
          <div style="padding:10px 16px;background:#0d2818;border-bottom:1px solid #2ea04340;color:#3fb950;font-size:14px;font-weight:600;text-align:center">
            ${this._success}
          </div>
        ` : nothing}

        <!-- Main Layout -->
        <div style="display:flex;flex:1;overflow:hidden">
          <!-- Left: Cart -->
          <div style="flex:1;display:flex;flex-direction:column;border-right:1px solid #21262d">
            <!-- Search Bar -->
            <div style="position:relative;padding:16px;border-bottom:1px solid #21262d">
              <div style="display:flex;gap:8px">
                <input
                  id="pos-search-input"
                  type="text"
                  placeholder="Search product by SKU or description..."
                  .value=${this._searchQuery}
                  @input=${(e: Event) => { this._searchQuery = (e.target as HTMLInputElement).value; }}
                  style="flex:1;padding:12px 16px;background:#0d1117;border:2px solid #30363d;border-radius:8px;color:#e6edf3;font-size:16px;outline:none;box-sizing:border-box"
                  aria-label="Search product by SKU or description"
                />
                <button
                  @click=${() => { this._isScanning = true; }}
                  style="background:#238636;border:none;border-radius:8px;color:#fff;padding:0 16px;cursor:pointer;font-weight:bold;display:flex;align-items:center;gap:8px"
                >
                  Scan
                </button>
              </div>
              ${this._isScanning ? html`
                <gable-barcode-scanner
                  @scan=${(e: CustomEvent) => { this._isScanning = false; this._handleScan(e.detail); }}
                  @close=${() => { this._isScanning = false; }}
                ></gable-barcode-scanner>
              ` : nothing}
              ${this._searchResults.length > 0 ? html`
                <div style="position:absolute;top:100%;left:16px;right:16px;background:#161b22;border:1px solid #30363d;border-radius:8px;z-index:10;max-height:300px;overflow-y:auto;box-shadow:0 8px 24px rgba(0,0,0,0.4)">
                  ${this._searchResults.map(result => html`
                    <button
                      @click=${() => this._addItem(result)}
                      style="display:flex;width:100%;padding:10px 14px;background:transparent;border:none;border-bottom:1px solid #21262d;color:#e6edf3;cursor:pointer;text-align:left;gap:12px;align-items:center;font-size:13px"
                    >
                      <span style="font-family:monospace;font-size:12px;color:#58a6ff;min-width:100px">${result.sku}</span>
                      <span style="flex:1;color:#c9d1d9">${result.description}</span>
                      <span style="font-weight:600;color:#3fb950">${fmt(result.unit_price_cents)}/${result.uom}</span>
                      <span style="font-size:11px;color:#8b949e">${result.in_stock} avail</span>
                    </button>
                  `)}
                </div>
              ` : nothing}
            </div>

            <!-- Lines -->
            <div style="flex:1;overflow-y:auto;padding:8px 16px">
              ${productLines.length === 0 ? html`
                <div style="display:flex;flex-direction:column;align-items:center;justify-content:center;height:100%;color:#484f58">
                  <div style="font-size:48px;margin-bottom:12px">&#x1f6d2;</div>
                  <p>Search and add products to start a sale</p>
                </div>
              ` : html`
                <table style="width:100%;border-collapse:collapse" aria-label="Cart items">
                  <thead>
                    <tr>
                      <th style="text-align:left;padding:8px 12px;font-size:11px;color:#8b949e;text-transform:uppercase;letter-spacing:0.5px;border-bottom:1px solid #21262d">Item</th>
                      <th style="text-align:center;padding:8px 12px;font-size:11px;color:#8b949e;text-transform:uppercase;letter-spacing:0.5px;border-bottom:1px solid #21262d">Qty</th>
                      <th style="text-align:right;padding:8px 12px;font-size:11px;color:#8b949e;text-transform:uppercase;letter-spacing:0.5px;border-bottom:1px solid #21262d">Price</th>
                      <th style="text-align:right;padding:8px 12px;font-size:11px;color:#8b949e;text-transform:uppercase;letter-spacing:0.5px;border-bottom:1px solid #21262d">Total</th>
                      <th style="padding:8px 12px;font-size:11px;color:#8b949e;text-transform:uppercase;letter-spacing:0.5px;border-bottom:1px solid #21262d;width:40px"></th>
                    </tr>
                  </thead>
                  <tbody>
                    ${productLines.map((item: SaleLine) => html`
                      <tr>
                        <td style="padding:10px 12px;font-size:14px;border-bottom:1px solid #161b22">${item.description}</td>
                        <td style="padding:10px 12px;font-size:14px;border-bottom:1px solid #161b22;text-align:center">
                          ${item.quantity} ${item.uom}
                        </td>
                        <td style="padding:10px 12px;font-size:14px;border-bottom:1px solid #161b22;text-align:right">
                          ${item.unit_price_ten_thousandths !== null ? fmtPrice(item.unit_price_ten_thousandths) : '-'}
                        </td>
                        <td style="padding:10px 12px;font-size:14px;border-bottom:1px solid #161b22;text-align:right;font-weight:600">
                          ${item.line_total_cents !== null ? fmt(item.line_total_cents) : '-'}
                        </td>
                        <td style="padding:10px 12px;font-size:14px;border-bottom:1px solid #161b22">
                          <button
                            @click=${() => this._removeItem(item.id)}
                            style="background:none;border:none;color:#f85149;font-size:18px;cursor:pointer;padding:2px 6px;border-radius:4px"
                            title="Remove"
                            aria-label="Remove ${item.description}"
                          >\u00d7</button>
                        </td>
                      </tr>
                    `)}
                  </tbody>
                </table>
              `}
            </div>
          </div>

          <!-- Right: Totals + Tenders -->
          <div style="width:360px;display:flex;flex-direction:column;background:#161b22;padding:24px;overflow-y:auto">
            ${this._renderCustomerPanel()}
            <div style="margin-bottom:24px">
              <div style="display:flex;justify-content:space-between;padding:6px 0;font-size:14px;color:#8b949e">
                <span>Subtotal</span>
                <span>$${subtotalDollars}</span>
              </div>
              <div style="display:flex;justify-content:space-between;padding:6px 0;font-size:14px;color:#8b949e">
                <span>Tax</span>
                <span>$${taxDollars}</span>
              </div>
              <div style="display:flex;justify-content:space-between;padding:12px 0;font-size:16px;font-weight:700;color:#e6edf3;border-top:1px solid #30363d;margin-top:8px">
                <span>TOTAL</span>
                <span style="font-size:28px;font-weight:800;color:#3fb950">$${totalDollars}</span>
              </div>
              ${this._pendingTenders.length > 0 ? html`
                <div style="margin-top:8px;padding:8px 0;border-top:1px dashed #30363d">
                  ${this._pendingTenders.map((t, i) => html`
                    <div key=${i} style="display:flex;justify-content:space-between;font-size:13px;color:#c9d1d9;padding:3px 0">
                      <span style="text-transform:capitalize">${t.method}</span>
                      <span>${fmt(t.amount_cents)}</span>
                    </div>
                  `)}
                  <div style="display:flex;justify-content:space-between;font-size:13px;font-weight:700;color:${remaining > 0 ? '#d29922' : '#3fb950'};padding-top:6px">
                    <span>${remaining > 0 ? 'Still owed' : 'Change'}</span>
                    <span>${fmt(Math.abs(remaining))}</span>
                  </div>
                </div>
              ` : nothing}
            </div>

            ${!this._showTender && this._pendingTenders.length === 0 ? html`
              <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;flex:1">
                ${METHODS.map(m => html`
                  <button @click=${() => this._handleTender(m)} style="padding:20px;background:#21262d;border:1px solid #30363d;border-radius:12px;color:#e6edf3;font-size:15px;font-weight:600;cursor:pointer;transition:all 0.15s;text-align:center;text-transform:capitalize" ?disabled=${productLines.length === 0 || sale?.status !== 'open' || (m === 'account' && this._accountRefusal !== null)} title=${m === 'account' ? (this._accountRefusal ?? 'Charge the sale to the customer\u2019s account') : ''}>
                    ${m === 'account' ? 'Charge to account' : m}
                  </button>
                `)}
              </div>
            ` : this._showTender ? html`
              <div style="display:flex;flex-direction:column;gap:12px">
                <div style="font-size:18px;font-weight:700;text-align:center;padding:8px;text-transform:capitalize">
                  ${this._tenderMethod}
                </div>
                <input
                  type="number"
                  .value=${this._tenderAmount}
                  @input=${(e: Event) => { this._tenderAmount = (e.target as HTMLInputElement).value; }}
                  style="padding:14px;background:#0d1117;border:2px solid #238636;border-radius:8px;color:#e6edf3;font-size:24px;font-weight:700;text-align:center;outline:none"
                  step="0.01"
                  aria-label="Tender amount"
                />
                <div style="display:flex;gap:8px">
                  <button
                    @click=${() => this._addPendingTender()}
                    style="flex:1;padding:14px;background:#1f6feb;border:none;border-radius:8px;color:#fff;font-size:15px;font-weight:700;cursor:pointer"
                  >Add Tender (split)</button>
                </div>
                <button
                  @click=${() => { this._showTender = false; }}
                  style="padding:10px;background:transparent;border:1px solid #30363d;border-radius:8px;color:#8b949e;font-size:14px;cursor:pointer"
                >
                  Cancel
                </button>
              </div>
            ` : html`
              <div style="display:flex;flex-direction:column;gap:12px;flex:1">
                <div style="font-size:14px;color:#8b949e;text-align:center">Tenders taken against $${totalDollars}</div>
                ${METHODS.map(m => html`
                  <button @click=${() => this._handleTender(m)} style="padding:12px;background:#21262d;border:1px solid #30363d;border-radius:8px;color:#e6edf3;font-size:14px;font-weight:600;cursor:pointer;text-transform:capitalize" ?disabled=${(remaining <= 0 && m !== 'cash') || (m === 'account' && this._accountRefusal !== null)} title=${m === 'account' ? (this._accountRefusal ?? '') : ''}>
                    Add ${m}
                  </button>
                `)}
                <button
                  @click=${() => this._completeSale()}
                  style="padding:16px;background:#238636;border:none;border-radius:8px;color:#fff;font-size:16px;font-weight:700;cursor:pointer;margin-top:8px"
                  ?disabled=${this._loading || remaining > 0}
                >
                  ${this._loading ? 'Processing...' : remaining > 0 ? `Still owed ${fmt(remaining)}` : `Complete Sale - $${totalDollars}`}
                </button>
                <button
                  @click=${() => { this._pendingTenders = []; }}
                  style="padding:10px;background:transparent;border:1px solid #30363d;border-radius:8px;color:#8b949e;font-size:14px;cursor:pointer"
                >
                  Clear tenders
                </button>
              </div>
            `}

            <!-- Quick Actions -->
            <div style="padding:16px 0;margin-top:auto;display:flex;gap:8px">
              <button
                @click=${() => this._voidSale()}
                style="flex:1;padding:10px;background:transparent;border:1px solid #f8514940;border-radius:8px;color:#f85149;font-size:13px;cursor:pointer"
                ?disabled=${!this._completed}
              >
                Void
              </button>
              <button
                @click=${() => this._beginReturn()}
                style="flex:1;padding:10px;background:transparent;border:1px solid #d2992240;border-radius:8px;color:#d29922;font-size:13px;cursor:pointer"
                ?disabled=${!this._completed}
              >
                Return
              </button>
            </div>
          </div>
        </div>
      </div>
    `;
  }
}

export default POSTerminal;
