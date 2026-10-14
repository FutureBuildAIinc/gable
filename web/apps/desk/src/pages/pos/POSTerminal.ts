// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state, query } from 'lit/decorators.js';
import { posService, dollarsToCents, type TenderIn } from '../../services/POSService';
import type { Sale, QuickSearchResult, SaleLine, TillSession, TillReport, TenderMethod, RefundMethod, CounterCustomer, CounterRegister } from '../../types/pos';
import { branchContext } from '../../services/BranchContext';
import { chargeRefusal, standingOf, money } from './standing';
import { formatPrice4 } from '../../lib/utils';
import { isCountedUnit, normalizeQuantity } from '../../lib/money';
import { payPlan, panelProgress, type PanelState } from './panels';
import { authCustody } from '@gable/auth';
import '../../components/BarcodeScanner.ts';

const REGISTER_STORAGE_PREFIX = 'gable.pos.register.';
// The sale each register last opened, so a visit that left it empty reuses it.
const SALE_STORAGE_PREFIX = 'gable.pos.sale.';
const fmt = (cents: number) => `$${(cents / 100).toFixed(2)}`;
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
  @state() private _pendingTenders: TenderIn[] = [];
  @state() private _tenderMethod: TenderMethod = 'cash';
  @state() private _tenderAmount = '';
  @state() private _loading = false;
  @state() private _error: string | null = null;
  @state() private _success: string | null = null;
  @state() private _isScanning = false;
  // The quantity the next added result takes, and the highlighted result.
  @state() private _qtyText = '1';
  @state() private _highlight = 0;

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

  // The register the counter works on: the current yard's, chosen from the
  // registers the caller may use. registersLoaded stays false until the first
  // answer, so the screen never claims a yard has none before it asked.
  @state() private _registers: CounterRegister[] = [];
  @state() private _register: CounterRegister | null = null;
  @state() private _registersLoaded = false;
  @state() private _registersFailed = false;
  /** Counts register loads; an answer to an older one is dropped. */
  private _registerSeq = 0;
  /** The yard the counter last settled on, to go back to when a change is declined. */
  private _yardId: string | null = null;
  private _revertingYard = false;

  @query('#pos-search-input') private _searchInput!: HTMLInputElement;

  private _newTxTimer: ReturnType<typeof setTimeout> | null = null;
  private _errorTimer: ReturnType<typeof setTimeout> | null = null;
  private _searchDebounce: ReturnType<typeof setTimeout> | null = null;
  /** Counts searches; an answer to an older one is dropped. */
  private _searchSeq = 0;
  /** True from a keystroke until the results on screen answer the typed query. */
  private _resultsStale = false;
  private _custDebounce: ReturnType<typeof setTimeout> | null = null;

  connectedCallback() {
    super.connectedCallback();
    window.addEventListener('gable:branch-changed', this._onYardChanged);
    this._yardId = branchContext.currentId;
    void this._loadRegisters();
  }

  /**
   * A yard change moves the counter to that yard's register. An open sale
   * with lines stays open on its own register, so the cashier is asked first;
   * declining puts the yard back.
   */
  private _onYardChanged = () => {
    if (this._revertingYard) return;
    if (this._saleHasLines && !window.confirm(this._leaveSaleQuestion())) {
      this._revertingYard = true;
      try {
        branchContext.setCurrent(this._yardId);
      } finally {
        this._revertingYard = false;
      }
      return;
    }
    this._yardId = branchContext.currentId;
    void this._loadRegisters();
  };

  private get _saleHasLines(): boolean {
    const sale = this._sale;
    return sale !== null && sale.status === 'open' && sale.lines.some((l) => l.parent_line_id === null);
  }

  private _leaveSaleQuestion(): string {
    const sale = this._sale;
    return `Sale ${sale?.number ?? ''} has lines and stays open on ${this._registerId}. Leave it and move on?`;
  }

  /* ---- The register: the current yard's ---- */

  private get _registerId(): string {
    return this._register?.id ?? '';
  }

  private _storageKey(): string {
    return REGISTER_STORAGE_PREFIX + (branchContext.currentId ?? 'all');
  }

  /**
   * Lists the registers and takes the current yard's. The list is the
   * server's: the request carries the yard (X-Branch-Id), so the answer is that
   * yard's registers, the user's grants with no yard, or every register for an
   * administrator with none. The counter takes the one this yard last used,
   * else the first by name. Then the till and a fresh sale follow.
   */
  private async _loadRegisters() {
    const seq = ++this._registerSeq;
    let all: CounterRegister[] = [];
    try {
      all = await posService.listRegisters();
    } catch (err: unknown) {
      if (seq !== this._registerSeq) return;
      // A failed list is not an empty one: say it failed and offer a retry.
      this._error = err instanceof Error ? err.message : 'Failed to list the registers';
      this._registers = [];
      this._register = null;
      this._till = null;
      this._sale = null;
      this._registersFailed = true;
      return;
    }
    if (seq !== this._registerSeq) return;
    // The server's order is yard then name; keep it when the list mixes yards.
    const here = [...all].sort((a, b) =>
      a.branch_name.localeCompare(b.branch_name) || a.branch_code.localeCompare(b.branch_code)
      || a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
    this._registers = here;
    this._registersFailed = false;
    this._registersLoaded = true;
    const remembered = localStorage.getItem(this._storageKey());
    this._register = here.find((r) => r.id === remembered) ?? here[0] ?? null;
    await this._openRegister();
  }

  /** Reads the till of the chosen register and starts a sale on it. */
  private async _openRegister() {
    this._till = null;
    this._sale = null;
    if (!this._register) return;
    const reg = this._registerId;
    await this._loadTill();
    // a later change of register or yard owns the screen now
    if (reg !== this._registerId) return;
    await this._startNewSale();
  }

  /** False when the cashier declined to leave the open sale. */
  private _chooseRegister(id: string): boolean {
    const next = this._registers.find((r) => r.id === id);
    if (!next || next.id === this._registerId) return true;
    if (this._saleHasLines && !window.confirm(this._leaveSaleQuestion())) return false;
    localStorage.setItem(this._storageKey(), next.id);
    this._register = next;
    void this._openRegister();
    return true;
  }

  /* ---- Till (drawer) lifecycle ---- */

  private async _loadTill() {
    const reg = this._registerId;
    let till: TillSession | null = null;
    try {
      till = await posService.currentTill(reg);
    } catch {
      till = null;
    }
    // a late answer for a register no longer selected is dropped
    if (reg !== this._registerId) return;
    this._till = till;
  }

  private async _openTill() {
    const cents = dollarsToCents(this._openingFloat);
    if (isNaN(cents) || cents < 0) {
      this._error = 'Enter a valid opening float';
      return;
    }
    try {
      this._tillBusy = true;
      this._till = await posService.openTill(this._registerId, cents);
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
    window.removeEventListener('gable:branch-changed', this._onYardChanged);
    this._registerSeq++;
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

  /** Arrows move the highlighted result; Enter adds it with the typed quantity. */
  private _resultKey(e: KeyboardEvent) {
    const n = this._searchResults.length;
    if (n === 0) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      this._highlight = Math.min(this._highlight + 1, n - 1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      this._highlight = Math.max(this._highlight - 1, 0);
    } else if (e.key === 'Enter') {
      e.preventDefault();
      void this._addItem(this._searchResults[Math.min(this._highlight, n - 1)]);
    }
  }

  /**
   * Enter adds only a result of the query now in the box. While the typed text is
   * ahead of the results on screen (the search delay), the search runs at once and
   * the exact SKU, else the top result of that search, is added.
   */
  private _searchKey(e: KeyboardEvent) {
    if (e.key === 'Enter' && this._resultsStale && this._searchQuery.trim().length >= 2) {
      e.preventDefault();
      void this._enterSearch();
      return;
    }
    this._resultKey(e);
  }

  private async _enterSearch() {
    const query = this._searchQuery;
    if (this._searchDebounce) clearTimeout(this._searchDebounce);
    try {
      const results = await this._runSearch(query);
      if (results === null || results.length === 0) return;
      const exact = results.find((r) => r.sku.toLowerCase() === query.trim().toLowerCase());
      await this._addItem(exact ?? results[0]);
    } catch {
      this._error = 'Search failed';
    }
  }

  /** Runs a search; null when a newer query was typed before it answered. */
  private async _runSearch(query: string): Promise<QuickSearchResult[] | null> {
    const seq = ++this._searchSeq;
    const results = await posService.searchProducts(query);
    if (seq !== this._searchSeq) return null;
    this._searchResults = results;
    this._resultsStale = false;
    return results;
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
      this._searchSeq++;
      this._searchResults = [];
      this._resultsStale = false;
      return;
    }

    this._searchDebounce = setTimeout(async () => {
      try {
        await this._runSearch(this._searchQuery);
      } catch {
        this._searchResults = [];
        this._resultsStale = false;
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
        <div id="pos-customer" class="rounded-lg border border-white/10 bg-deep-space p-3" data-testid="pos-customer">
          <div class="flex items-start justify-between gap-2">
            <div>
              <div class="text-sm font-semibold text-white" data-testid="pos-customer-name">${c.name}</div>
              <div class="font-mono text-xs text-slate-400">${c.account_number}</div>
            </div>
            <button
              @click=${() => this._clearCustomer()}
              ?disabled=${hasLines || this._loading}
              title=${hasLines ? 'The customer is named before the first line: start a new sale to change it' : 'Back to a walk-in sale'}
              class="rounded border border-white/20 px-3 py-1.5 text-xs font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:border-white/10 disabled:text-slate-400 disabled:hover:bg-transparent"
            >Change</button>
          </div>
          <div class="mt-2 font-mono text-xs font-semibold ${toneClass(st.tone)}" data-testid="pos-customer-standing">${st.text}</div>
          ${c.standing === 'good' || c.standing === 'over_limit' ? html`
            <div class="mt-1 font-mono text-xs text-slate-400">Open ${money(c.open_receivable_cents)}</div>` : nothing}
          <div class="mt-3 grid grid-cols-2 items-start gap-2">
            <div class="${c.jobs.length > 0 ? '' : 'col-span-2'}">
              <label class="block text-xs text-slate-400" for="pos-po">
                Purchase order${c.po_required ? html` <span class="text-safety-red">(required for a charge)</span>` : ''}
              </label>
              <input id="pos-po" type="text" maxlength="100" .value=${this._poText}
                @input=${(e: Event) => { this._poText = (e.target as HTMLInputElement).value; }}
                @change=${() => this._saveAccountRefs()}
                class="mt-1 w-full rounded border border-white/10 bg-slate-steel px-2 py-2 font-mono text-sm text-white outline-none"
                placeholder="PO number" />
            </div>
            ${c.jobs.length > 0 ? html`
              <div>
                <label class="block text-xs text-slate-400" for="pos-job">Job</label>
                <select id="pos-job" .value=${this._jobId}
                  @change=${(e: Event) => { this._jobId = (e.target as HTMLSelectElement).value; void this._saveAccountRefs(); }}
                  class="mt-1 w-full rounded border border-white/10 bg-slate-steel px-2 py-2 text-sm text-white outline-none">
                  <option value="">No job</option>
                  ${c.jobs.map((j) => html`<option value=${j.id} ?selected=${j.id === this._jobId}>${j.name}</option>`)}
                </select>
              </div>` : nothing}
          </div>
        </div>`;
    }
    return html`
      <div id="pos-customer" class="rounded-lg border border-white/10 bg-deep-space p-3" data-testid="pos-customer">
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
        <div class="mt-1 text-xs text-slate-400">Name the customer before the first line</div>
        ${this._custResults.length > 0 ? html`
          <div class="mt-2 rounded border border-white/10" data-testid="pos-customer-results">
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
    // A completion's own timer must not start a second sale over this one.
    if (this._newTxTimer) {
      clearTimeout(this._newTxTimer);
      this._newTxTimer = null;
    }
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
      const reg = this._registerId;
      const sale = await this._emptyOrNewSale(reg);
      // a late answer for a register no longer selected is dropped
      if (reg !== this._registerId) return;
      this._sale = sale;
      this._tenderAmount = '';
      this._tenderMethod = 'cash';
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

  /**
   * Each visit to the counter used to open a new empty sale, so abandoned empty
   * sales piled up on the register. The sale this register last opened is
   * reused while it is still open, empty, a walk-in and on the till session now
   * open; otherwise a new one is started and remembered.
   */
  private async _emptyOrNewSale(reg: string): Promise<Sale> {
    const key = SALE_STORAGE_PREFIX + reg;
    const remembered = localStorage.getItem(key);
    if (remembered) {
      try {
        const s = await posService.getSale(remembered);
        const empty = s.status === 'open' && s.register_id === reg && s.customer_id === null
          && !s.lines.some((l) => l.parent_line_id === null)
          && (s.till_session_id ?? null) === (this._till?.id ?? null);
        if (empty) return s;
      } catch {
        // gone or refused: a new sale is opened below
      }
    }
    const sale = await posService.startSale(reg);
    localStorage.setItem(key, sale.id);
    return sale;
  }

  private async _addItem(product: QuickSearchResult, quantity = this._qtyText) {
    if (!this._sale) return;
    const qty = normalizeQuantity(quantity);
    if (qty === null) {
      this._error = 'Enter a quantity above zero';
      return;
    }
    if (qty.includes('.') && isCountedUnit(product.uom)) {
      this._error = `${product.uom} is counted in whole units`;
      return;
    }
    try {
      this._error = null;
      const updated = await posService.addItem(this._sale.id, product.product_id, qty, product.uom);
      this._sale = updated;
      this._searchQuery = '';
      this._searchSeq++;
      this._searchResults = [];
      this._resultsStale = false;
      this._highlight = 0;
      this._qtyText = '1';
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

  /** What is still owed once the added tenders are taken (never negative). */
  private get _owedCents(): number {
    return Math.max((this._sale?.total_cents ?? 0) - this._tenderedSoFar, 0);
  }

  /**
   * The tender being entered: the chosen method and the typed amount, which
   * is what is still owed while the field is empty. Null when it does not
   * parse to a positive amount.
   */
  private get _entry(): TenderIn | null {
    const text = this._tenderAmount.trim();
    const cents = text === '' ? this._owedCents : dollarsToCents(text);
    if (isNaN(cents) || cents <= 0) return null;
    return { method: this._tenderMethod, amount_cents: cents };
  }

  private _chooseMethod(method: TenderMethod) {
    if (method === 'account') {
      const refusal = this._accountRefusal;
      if (refusal) {
        this._error = refusal;
        return;
      }
    }
    this._tenderMethod = method;
    this._tenderAmount = '';
  }

  /** Shows a typed tender amount with two decimals once the cashier leaves the field. */
  private _formatTenderAmount() {
    const text = this._tenderAmount.trim();
    if (text === '') return;
    const cents = dollarsToCents(text);
    if (cents !== null && !isNaN(cents) && cents > 0) this._tenderAmount = (cents / 100).toFixed(2);
  }

  /** Add the entered tender to the added list; a split names several. */
  private _addPendingTender() {
    const entry = this._entry;
    if (!entry) {
      this._error = 'Invalid tender amount';
      return;
    }
    this._pendingTenders = [...this._pendingTenders, entry];
    this._tenderAmount = '';
    // The next tender starts on a method the sale can still take.
    if (this._tenderMethod === 'account') this._tenderMethod = 'cash';
  }

  /** Pay: the added tenders plus the entry when they do not cover the total. */
  private async _pay() {
    if (!this._sale) return;
    const plan = payPlan(this._sale.total_cents, this._pendingTenders, this._entry);
    if (!plan.ok) {
      this._error = plan.owedCents > 0 ? `Still owed ${fmt(plan.owedCents)}` : 'Nothing to pay';
      return;
    }
    // Every account tender on its way out is rechecked here, whether it was just
    // entered or added to the list earlier: the customer's standing or the PO may
    // have changed since it was chosen.
    if (plan.tenders.some((t) => t.method === 'account')) {
      const refusal = this._accountRefusal;
      if (refusal) {
        this._error = refusal;
        return;
      }
    }
    await this._completeSale(plan.tenders);
  }

  /**
   * Sends the tenders it is given and clears the added list only when the sale
   * completes, so a refused Pay leaves no stale tender behind.
   */
  private async _completeSale(tenders: TenderIn[]) {
    if (!this._sale || tenders.length === 0) return;
    try {
      this._loading = true;
      this._error = null;
      const completed = await posService.completeSale(this._sale.id, this._sale.revision, tenders);
      this._sale = completed;
      this._completed = completed;
      const change = completed.change_cents || 0;
      this._success = change > 0
        ? `Sale ${completed.number} complete: ${fmt(completed.total_cents)} \u00b7 CHANGE DUE ${fmt(change)}`
        : `Sale ${completed.number} complete: ${fmt(completed.total_cents)}`;
      this._pendingTenders = [];
      this._tenderAmount = '';
      if (tenders.some((t) => t.method === 'account')) void this._refreshStanding();

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

  /** The charge moved the account's credit and open balance: read them again. */
  private async _refreshStanding() {
    const named = this._customer;
    if (!named) return;
    try {
      const found = await posService.findCustomers(named.account_number);
      const fresh = found.find((c) => c.id === named.id);
      if (fresh && this._customer?.id === named.id) this._customer = fresh;
    } catch {
      // the figures on screen stay; the next lookup reads them afresh
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
        register_id: this._registerId,
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

  private _progressChip(label: string, n: number, state: PanelState) {
    const tone = state === 'done'
      ? 'border-gable-green/40 text-gable-green'
      : state === 'current' ? 'border-white/30 text-white' : 'border-white/10 text-slate-400';
    return html`
      <span data-testid="progress-${label.toLowerCase()}" data-state=${state}
        class="flex items-center gap-2 rounded-full border px-3 py-1 text-xs font-semibold ${tone}">
        <span class="font-mono">${n}</span>${label}
      </span>`;
  }

  private _renderHeader(sale: Sale | null) {
    const cashier = authCustody.displayName ?? 'Cashier';
    const progress = panelProgress({
      hasLines: (sale?.lines.length ?? 0) > 0,
      customerNamed: this._customer !== null,
      tendered: this._pendingTenders.length > 0,
    });
    return html`
      <header class="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-white/10 bg-slate-steel px-6 py-3" data-testid="pos-header">
        <div class="flex flex-wrap items-center gap-3">
          <h1 class="text-lg font-bold text-white">Counter</h1>
          <span class="rounded-full bg-white/10 px-2 py-0.5 font-mono text-xs text-slate-200" data-testid="pos-register">${this._registerId}</span>
          ${this._registers.length > 1 ? html`
            <label class="sr-only" for="pos-register-picker">Register</label>
            <select id="pos-register-picker" data-testid="pos-register-picker"
              @change=${(e: Event) => {
                const picker = e.target as HTMLSelectElement;
                if (!this._chooseRegister(picker.value)) picker.value = this._registerId;
              }}
              class="rounded border border-white/20 bg-deep-space px-2 py-0.5 text-xs text-slate-200">
              ${this._registers.map((r) => html`<option value=${r.id} ?selected=${r.id === this._registerId}>${r.branch_code} ${r.name}</option>`)}
            </select>` : nothing}
          ${this._till ? html`
            <span title="Drawer open" class="rounded-full border border-gable-green/40 px-2 py-0.5 font-mono text-xs font-semibold text-gable-green" data-testid="pos-till-state">
              Till open &middot; float ${fmt(this._till.opening_float_cents)}
            </span>` : html`
            <span class="rounded-full border border-safety-red/40 px-2 py-0.5 text-xs font-semibold text-safety-red" data-testid="pos-till-state">No till</span>`}
          <span class="text-xs text-slate-400" data-testid="pos-cashier">Cashier <span class="text-slate-200">${cashier}</span></span>
          ${sale ? html`<span class="font-mono text-xs text-slate-400">${sale.number} &middot; ${sale.status}</span>` : nothing}
        </div>
        <nav class="order-last flex w-full items-center gap-2 xl:order-none xl:w-auto" aria-label="Sale progress">
          ${this._progressChip('Customer', 1, progress.customer)}
          ${this._progressChip('Payment', 2, progress.payment)}
          ${this._progressChip('Summary', 3, progress.summary)}
        </nav>
        <div class="flex gap-2">
          ${!this._register ? nothing : this._till ? html`
            <button @click=${() => this._beginCloseTill()} ?disabled=${this._tillBusy}
              title="Count the drawer and close the till with its Z report"
              class="rounded border border-white/20 px-4 py-1.5 text-sm text-slate-200">Close Till</button>` : html`
            <button @click=${() => { this._showTillOpen = true; }}
              class="rounded bg-gable-green px-4 py-1.5 text-sm font-semibold text-deep-space">Open Till</button>`}
          ${this._register ? html`
          <button @click=${() => this._startNewSale()}
            class="rounded border border-white/20 px-4 py-1.5 text-sm text-slate-200">New Sale</button>` : nothing}
        </div>
      </header>`;
  }

  private _renderItems(productLines: SaleLine[]) {
    const th = 'border-b border-white/10 px-3 py-2 text-xs font-semibold uppercase tracking-wide text-slate-400';
    return html`
      <section class="flex min-h-[22rem] flex-1 flex-col border-white/10 xl:min-h-0 xl:border-r" data-testid="pos-items">
        <div class="relative border-b border-white/10 p-4">
          <div class="flex gap-2">
            <input id="pos-search-input" type="text" autocomplete="off"
              placeholder="Search by SKU or description"
              .value=${this._searchQuery}
              @input=${(e: Event) => { this._searchQuery = (e.target as HTMLInputElement).value; this._highlight = 0; this._resultsStale = true; }}
              @keydown=${(e: KeyboardEvent) => this._searchKey(e)}
              class="flex-1 rounded-lg border border-white/10 bg-deep-space px-4 py-3 text-base text-white outline-none focus:border-gable-green"
              aria-label="Search product by SKU or description" />
            <label class="sr-only" for="pos-qty">Quantity</label>
            <input id="pos-qty" type="text" inputmode="decimal" autocomplete="off"
              .value=${this._qtyText}
              @input=${(e: Event) => { this._qtyText = (e.target as HTMLInputElement).value; }}
              @keydown=${(e: KeyboardEvent) => this._searchKey(e)}
              class="w-20 rounded-lg border border-white/10 bg-deep-space px-3 py-3 text-right font-mono text-base text-white outline-none focus:border-gable-green"
              title="Quantity to add" aria-label="Quantity" />
            <button @click=${() => { this._isScanning = true; }}
              class="rounded-lg border border-white/20 px-4 text-sm font-semibold text-slate-200">Scan</button>
          </div>
          ${this._isScanning ? html`
            <gable-barcode-scanner
              @scan=${(e: CustomEvent) => { this._isScanning = false; this._handleScan(e.detail); }}
              @close=${() => { this._isScanning = false; }}
            ></gable-barcode-scanner>` : nothing}
          ${this._searchResults.length > 0 ? html`
            <div class="absolute left-4 right-4 top-full z-10 max-h-72 overflow-y-auto rounded-lg border border-white/10 bg-slate-steel shadow-2xl" data-testid="pos-results">
              ${this._searchResults.map((r, i) => html`
                <button @click=${() => this._addItem(r)}
                  aria-current=${i === this._highlight ? 'true' : 'false'}
                  class="flex w-full items-center gap-3 border-b border-white/10 px-4 py-2 text-left text-sm text-white last:border-b-0 hover:bg-white/5 ${i === this._highlight ? 'bg-white/10' : ''}">
                  <span class="min-w-[100px] font-mono text-xs text-blueprint-blue">${r.sku}</span>
                  <span class="flex-1 text-slate-200">${r.description}</span>
                  <span class="font-mono font-semibold text-gable-green">${fmt(r.unit_price_cents)}/${r.uom}</span>
                  <span class="font-mono text-xs text-slate-400">${r.in_stock} on hand</span>
                </button>`)}
            </div>` : nothing}
        </div>
        <div class="min-h-0 flex-1 overflow-y-auto px-4 py-2">
          ${productLines.length === 0 ? html`
            <div class="flex h-full min-h-[120px] items-center justify-center text-sm text-slate-400" data-testid="pos-empty">
              Search and add products to start a sale
            </div>` : html`
            <table class="w-full border-collapse" aria-label="Ticket lines" data-testid="pos-lines">
              <thead><tr>
                <th class="${th} text-left">Item</th>
                <th class="${th} text-center">Qty</th>
                <th class="${th} text-right">Price</th>
                <th class="${th} text-right">Total</th>
                <th class="${th} w-14"></th>
              </tr></thead>
              <tbody>
                ${productLines.map((item) => html`
                  <tr class="border-b border-white/5">
                    <td class="px-3 py-3 text-sm text-white">${item.description}</td>
                    <td class="px-3 py-3 text-center font-mono text-sm text-slate-200">${item.quantity} ${item.uom}</td>
                    <td class="px-3 py-3 text-right font-mono text-sm text-slate-200">
                      ${item.unit_price_ten_thousandths !== null ? formatPrice4(item.unit_price_ten_thousandths) : '-'}</td>
                    <td class="px-3 py-3 text-right font-mono text-sm font-semibold text-white">
                      ${item.line_total_cents !== null ? fmt(item.line_total_cents) : '-'}</td>
                    <td class="px-3 py-3">
                      <button @click=${() => this._removeItem(item.id)} title="Remove"
                        aria-label="Remove ${item.description}"
                        ?disabled=${this._sale?.status !== 'open'}
                        class="inline-flex h-11 w-11 items-center justify-center rounded text-lg text-safety-red hover:bg-safety-red/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-safety-red disabled:cursor-not-allowed disabled:text-slate-400 disabled:opacity-40 disabled:hover:bg-transparent">×</button>
                    </td>
                  </tr>`)}
              </tbody>
            </table>`}
        </div>
      </section>`;
  }

  private _renderPaymentPanel(sale: Sale | null, hasLines: boolean) {
    const open = sale?.status === 'open';
    const total = sale?.total_cents ?? 0;
    const owed = this._owedCents;
    const entry = this._entry;
    const isCash = this._tenderMethod === 'cash';
    const change = isCash && entry !== null && this._tenderedSoFar < total
      ? Math.max(this._tenderedSoFar + entry.amount_cents - total, 0) : 0;
    return html`
      <section class="rounded-lg border border-white/10 bg-deep-space p-3" data-testid="panel-payment">
        <div class="text-xs font-semibold uppercase tracking-wide text-slate-400">Payment</div>
        ${this._pendingTenders.length > 0 ? html`
          <div class="mt-2 border-b border-dashed border-white/10 pb-2" data-testid="pos-added-tenders">
            ${this._pendingTenders.map((t) => html`
              <div class="flex justify-between py-0.5 text-sm text-slate-200">
                <span class="capitalize">${t.method}</span><span class="font-mono">${fmt(t.amount_cents)}</span>
              </div>`)}
            <div class="flex justify-between pt-1 text-sm font-bold ${owed > 0 ? 'text-amber-400' : 'text-gable-green'}">
              <span>${owed > 0 ? 'Still owed' : 'Covered'}</span><span class="font-mono">${fmt(owed)}</span>
            </div>
          </div>` : nothing}
        <div class="mt-2 grid grid-cols-2 gap-2" role="group" aria-label="Tender">
          ${METHODS.map((m) => {
            const refused = m === 'account' ? this._accountRefusal : null;
            const chosen = this._tenderMethod === m;
            return html`
              <button data-testid="tender-${m}" @click=${() => this._chooseMethod(m)}
                ?disabled=${!hasLines || !open || refused !== null}
                title=${m === 'account' ? (refused ?? 'Charge the sale to the customer’s account') : ''}
                aria-pressed=${chosen ? 'true' : 'false'}
                class="rounded-lg border px-3 py-3 text-sm font-semibold ${m === 'account' ? '' : 'capitalize'} disabled:cursor-not-allowed disabled:opacity-40 ${chosen ? 'border-gable-green text-gable-green' : 'border-white/10 text-white'}">
                ${m === 'account' ? 'Charge to account' : m}
              </button>`;
          })}
        </div>
        <label class="mt-3 block text-xs text-slate-400" for="pos-tender-amount">
          ${isCash ? 'Cash tendered' : html`<span class="capitalize">${this._tenderMethod}</span> amount`}
        </label>
        <input id="pos-tender-amount" type="text" inputmode="decimal" autocomplete="off"
          placeholder=${(owed / 100).toFixed(2)} .value=${this._tenderAmount}
          ?disabled=${!hasLines || !open}
          @input=${(e: Event) => { this._tenderAmount = (e.target as HTMLInputElement).value; }}
          @blur=${() => this._formatTenderAmount()}
          class="mt-1 w-full rounded border border-white/10 bg-slate-steel px-3 py-2 text-right font-mono text-xl font-bold text-white outline-none focus:border-gable-green"
          aria-label="Tender amount" />
        ${isCash && change > 0 ? html`
          <div class="mt-2 flex justify-between text-sm font-bold text-gable-green" data-testid="pos-change">
            <span>Change due</span><span class="font-mono">${fmt(change)}</span>
          </div>` : nothing}
        <div class="mt-3 flex gap-2">
          <button data-testid="pos-add-tender" @click=${() => this._addPendingTender()}
            ?disabled=${!hasLines || !open || entry === null || owed <= 0}
            class="flex-1 rounded border border-white/20 px-3 py-2 text-xs text-slate-200 disabled:opacity-40">Add as split tender</button>
          ${this._pendingTenders.length > 0 ? html`
            <button @click=${() => { this._pendingTenders = []; }}
              class="rounded border border-white/10 px-3 py-2 text-xs text-slate-400">Clear tenders</button>` : nothing}
        </div>
      </section>`;
  }

  private _renderSummaryPanel(sale: Sale | null, hasLines: boolean) {
    const owed = this._owedCents;
    const total = sale?.total_cents ?? 0;
    const open = sale?.status === 'open';
    const plan = payPlan(total, this._pendingTenders, this._entry);
    const canPay = this._till !== null && hasLines && open && !this._loading && plan.ok;
    const label = this._loading ? 'Processing...'
      : this._till === null ? `Pay ${fmt(total)}`
      : plan.ok ? `Pay ${fmt(total)}`
      : hasLines ? `Still owed ${fmt(plan.owedCents)}` : `Pay ${fmt(total)}`;
    return html`
      <section class="flex items-center gap-3 rounded-lg border border-white/10 bg-deep-space p-3 xl:block" data-testid="panel-summary">
        <div class="hidden text-xs font-semibold uppercase tracking-wide text-slate-400 xl:block">Summary</div>
        <dl class="text-sm xl:mt-2">
          <div class="hidden justify-between py-1 text-slate-400 xl:flex"><dt>Lines</dt>
            <dd class="font-mono" data-testid="summary-lines">${(sale?.lines ?? []).filter((l) => l.parent_line_id === null).length}</dd></div>
          <div class="hidden justify-between py-1 text-slate-400 xl:flex"><dt>Subtotal</dt>
            <dd class="font-mono" data-testid="summary-subtotal">${fmt(sale?.subtotal_cents ?? 0)}</dd></div>
          <div class="hidden justify-between py-1 text-slate-400 xl:flex"><dt>Tax</dt>
            <dd class="font-mono" data-testid="summary-tax">${fmt(sale?.tax_cents ?? 0)}</dd></div>
          <div class="mt-1 flex items-baseline justify-between border-t border-white/10 pt-2 font-bold text-white"><dt>Total</dt>
            <dd class="font-mono text-2xl text-gable-green" data-testid="summary-total">${fmt(total)}</dd></div>
          ${this._pendingTenders.length > 0 ? html`
            <div class="flex justify-between py-1 text-slate-400"><dt>Still owed</dt>
              <dd class="font-mono">${fmt(owed)}</dd></div>` : nothing}
        </dl>
        <button data-testid="pos-pay" @click=${() => this._pay()} ?disabled=${!canPay}
          class="min-w-0 flex-1 rounded-lg px-4 py-4 text-base font-bold xl:mt-3 xl:w-full ${canPay ? 'bg-gable-green text-deep-space' : 'cursor-not-allowed border border-white/10 bg-white/5 text-slate-400'}">
          ${label}
        </button>
        <div class="flex gap-2 xl:mt-3">
          <button data-testid="pos-void" @click=${() => this._voidSale()} ?disabled=${!this._completed}
            class="flex-1 rounded border border-white/30 px-3 py-2 text-xs font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:border-white/10 disabled:text-slate-400 disabled:hover:bg-transparent">Void</button>
          <button @click=${() => this._beginReturn()} ?disabled=${!this._completed}
            class="flex-1 rounded border border-white/30 px-3 py-2 text-xs font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:border-white/10 disabled:text-slate-400 disabled:hover:bg-transparent">Return</button>
        </div>
      </section>`;
  }

  render() {
    const sale = this._sale;
    const lines: SaleLine[] = sale?.lines ?? [];
    const productLines = lines.filter((l) => l.parent_line_id === null);
    const hasLines = productLines.length > 0;

    return html`
      <div class="flex h-screen flex-col bg-deep-space font-sans text-white" data-testid="pos-terminal">
        ${this._renderHeader(sale)}
        ${this._renderTillOpenModal()}
        ${this._renderTillCloseModal()}
        ${this._renderReturnModal()}

        ${this._error ? html`
          <div class="flex items-center justify-between border-b border-safety-red/40 bg-safety-red/10 px-4 py-2 text-sm text-safety-red" role="alert">
            ${this._error}
            <button @click=${() => { this._error = null; }} class="text-lg" aria-label="Dismiss error">×</button>
          </div>` : nothing}
        ${this._success ? html`
          <div class="border-b border-gable-green/30 bg-gable-green/10 px-4 py-2 text-center text-sm font-semibold text-gable-green" role="status">
            ${this._success}
          </div>` : nothing}

        ${this._registersFailed ? html`
          <div class="flex flex-1 flex-col items-center justify-center gap-3 p-8">
            <p class="max-w-md text-center text-base text-slate-200" data-testid="pos-registers-failed" role="status">
              The registers could not be loaded, so the counter cannot take a sale yet.
            </p>
            <button @click=${() => { void this._loadRegisters(); }}
              class="rounded border border-white/20 px-4 py-2 text-sm font-semibold text-slate-200">Try again</button>
          </div>` : this._registersLoaded && !this._register ? html`
          <div class="flex flex-1 items-center justify-center p-8">
            <p class="max-w-md text-center text-base text-slate-200" data-testid="pos-no-register" role="status">
              This yard has no register, so the counter cannot take a sale or open a till here.
              Ask an administrator to add a register for this yard, or switch to a yard that has one.
            </p>
          </div>` : html`
        <div class="flex min-h-0 flex-1 flex-col xl:flex-row">
          ${this._renderItems(productLines)}
          <div class="flex max-h-[45%] min-h-0 flex-col overflow-y-auto border-t border-white/10 bg-slate-steel xl:max-h-none xl:w-[420px] xl:border-t-0"
            data-testid="pos-panels">
            <div class="p-4">
              <div class="grid grid-cols-1 items-start gap-3 md:grid-cols-2 xl:grid-cols-1">
                <section data-testid="panel-customer">${this._renderCustomerPanel()}</section>
                ${this._renderPaymentPanel(sale, hasLines)}
              </div>
            </div>
            <div class="sticky bottom-0 mt-auto shrink-0 border-t border-white/10 bg-slate-steel p-4" data-testid="pos-panel-foot">
              ${this._renderSummaryPanel(sale, hasLines)}
            </div>
          </div>
        </div>`}
      </div>
    `;
  }
}

export default POSTerminal;
