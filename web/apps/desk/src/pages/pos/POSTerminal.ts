// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { posService, dollarsToCents, type TenderIn } from '../../services/POSService';
import type { Sale, QuickSearchResult, SaleLine, TillSession, TillReport, TenderMethod, RefundMethod, CounterCustomer, CounterRegister } from '../../types/pos';
import { branchContext } from '../../services/BranchContext';
import { chargeRefusal, standingOf, money, PO_REFUSAL } from './standing';
import { formatPrice4 } from '../../lib/utils';
import { isCountedUnit, normalizeQuantity } from '../../lib/money';
import { payPlan, panelProgress, type PanelState, type PanelKey } from './panels';
import { authCustody } from '@gable/auth';
import './catalog/gable-pos-catalog';

const REGISTER_STORAGE_PREFIX = 'gable.pos.register.';
// The sale each register last opened, so a visit that left it empty reuses it.
const SALE_STORAGE_PREFIX = 'gable.pos.sale.';
const fmt = (cents: number) => `$${(cents / 100).toFixed(2)}`;
const METHODS: TenderMethod[] = ['cash', 'check', 'card', 'account'];
const methodLabel = (m: TenderMethod) => (m === 'account' ? 'Charge to account' : m.charAt(0).toUpperCase() + m.slice(1));

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
  @state() private _pendingTenders: TenderIn[] = [];
  @state() private _tenderMethod: TenderMethod = 'cash';
  @state() private _tenderAmount = '';
  @state() private _loading = false;
  @state() private _error: string | null = null;
  @state() private _success: string | null = null;
  // The sale's customer: the lookup, the account's standing and what a charge
  // to the account carries (the purchase order and the job).
  @state() private _customer: CounterCustomer | null = null;
  @state() private _custQuery = '';
  @state() private _custResults: CounterCustomer[] = [];
  @state() private _poText = '';
  @state() private _jobId = '';

  // The linear panels: the sale's current step and the one panel open (or none).
  @state() private _step: PanelKey = 'customer';
  @state() private _open: PanelKey | null = 'customer';
  // The picture each product came with from the catalogue, by product id.
  @state() private _images: Record<string, string> = {};
  @state() private _receiptNote: string | null = null;
  @state() private _receiptRefused = false;
  @state() private _receiptBusy = false;
  /** A blob url the receipt can be downloaded from when its tab was blocked. */
  @state() private _receiptDownload: string | null = null;

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

  private _errorTimer: ReturnType<typeof setTimeout> | null = null;
  private _custDebounce: ReturnType<typeof setTimeout> | null = null;

  connectedCallback() {
    super.connectedCallback();
    window.addEventListener('gable:branch-changed', this._onYardChanged);
    this.addEventListener('pos-catalog-add', this._onCatalogAdd);
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
    // A sale takes its till session when it is created, so none is opened
    // until a till is: opening the till starts it (and reuses a remembered
    // empty one), so the till opening never leaves a second sale behind.
    if (this._till === null) return;
    await this._beginFresh();
  }

  /**
   * The signed in person: the key the remembered sale is filed under and the
   * cashier id the server stamps on a sale they start (the token's subject
   * when it is a uuid, else the nil uuid).
   */
  private _person(): { key: string; cashierId: string } {
    const NIL = '00000000-0000-0000-0000-000000000000';
    const s = authCustody.session;
    if (s === null) return { key: 'anon', cashierId: NIL };
    if (s.kind === 'dev') return { key: `dev-${s.name}`, cashierId: NIL };
    const sub = s.claims.sub ?? '';
    const isId = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(sub);
    return { key: sub !== '' ? sub : 'anon', cashierId: isId ? sub.toLowerCase() : NIL };
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
      void this._beginFresh();
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to open till';
    } finally {
      this._tillBusy = false;
    }
  }

  private async _beginCloseTill() {
    if (!this._till) return;
    // The same question a register change asks: declining keeps till and sale.
    if (this._saleHasLines && !window.confirm(this._leaveSaleQuestion())) return;
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
      this._sale = null;
      this._completed = null;
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to close till';
    } finally {
      this._tillBusy = false;
    }
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    window.removeEventListener('gable:branch-changed', this._onYardChanged);
    this.removeEventListener('pos-catalog-add', this._onCatalogAdd);
    this._registerSeq++;
    if (this._errorTimer) clearTimeout(this._errorTimer);
    if (this._custDebounce) clearTimeout(this._custDebounce);
  }

  updated(changed: Map<string, unknown>) {
    if (changed.has('_custQuery')) {
      this._debounceCustomers();
    }
  }

  /* ---- The sale's customer ---- */

  private _debounceCustomers() {
    if (this._custDebounce) clearTimeout(this._custDebounce);
    if (this._custQuery.trim().length < 2) {
      if (this._custResults.length > 0) this._custResults = [];
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
    if (!(await this._readySale())) return;
    if (!this._sale) return;
    try {
      this._error = null;
      this._sale = await posService.attachCustomer(this._sale.id, { customer_id: c.id });
      this._customer = c;
      this._custQuery = '';
      this._custResults = [];
      this._poText = '';
      this._jobId = '';
      // An account customer keeps the panel open for the purchase order and
      // the job; Next: Payment moves on once there is a line.
      this._step = 'customer';
      this._open = 'customer';
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to name the customer';
    }
  }

  /** A walk in sale is the choice not to name a customer. */
  private async _walkInSale() {
    if (!(await this._readySale())) return;
    this._customerSettled();
  }

  /** The customer is settled: Payment is the step, open once there is a line. */
  private _customerSettled() {
    this._step = 'payment';
    this._open = this._saleHasLines ? 'payment' : null;
  }

  private _togglePanel(key: PanelKey) {
    if (this._till === null) return;
    if (this._open === key) {
      this._open = null;
      return;
    }
    this._open = key;
    this._step = key;
  }

  /** Back to a walk-in sale (only before the first line). */
  private async _clearCustomer() {
    if (!this._sale || this._sale.status !== 'open') return;
    try {
      this._error = null;
      this._sale = await posService.attachCustomer(this._sale.id, { customer_id: null });
      this._customer = null;
      this._poText = '';
      this._jobId = '';
      this._step = 'customer';
      this._open = 'customer';
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to clear the customer';
    }
  }

  /** Saves the purchase order and the job the charge will carry. */
  private async _saveAccountRefs() {
    if (!this._sale || !this._customer || this._sale.status !== 'open') return;
    const sent = this._sale;
    try {
      this._error = null;
      const answer = await posService.attachCustomer(sent.id, {
        customer_id: this._customer.id,
        customer_po: this._poText.trim() || null,
        job_id: this._jobId || null,
      });
      // an answer for a sale that has moved on, or an older revision, is dropped
      const now = this._sale;
      if (!now || now.id !== answer.id || answer.revision < now.revision) return;
      this._sale = answer;
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
    // A completed sale's lines are no bar: naming a customer starts the next sale.
    const hasLines = sale?.status === 'open' && (sale?.lines.length ?? 0) > 0;
    const finished = sale !== null && sale.status !== 'open';
    const c = this._customer;
    const toneClass = (tone: string) =>
      tone === 'stop' ? 'text-safety-red' : tone === 'warn' ? 'text-amber-400' : 'text-gable-green';
    if (c) {
      const st = standingOf(c);
      return html`
        <div id="pos-customer" class="rounded-lg bg-deep-space p-3" data-testid="pos-customer">
          <div class="flex items-start justify-between gap-2">
            <div>
              <div class="text-sm font-semibold text-white" data-testid="pos-customer-name">${c.name}</div>
              <div class="font-mono text-xs text-slate-400">${c.account_number}</div>
            </div>
            <button
              @click=${() => this._clearCustomer()}
              ?disabled=${hasLines || finished || this._loading}
              title=${finished ? 'Start a new sale to change the customer' : hasLines ? 'The customer is named before the first line: start a new sale to change it' : 'Back to a walk-in sale'}
              class="rounded border border-white/20 px-3 py-1.5 text-xs font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:border-white/10 disabled:text-slate-400 disabled:opacity-40 disabled:hover:bg-transparent"
            >Walk-in sale</button>
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
                ?disabled=${finished}
                class="mt-1 w-full rounded border border-white/10 bg-slate-steel px-2 py-2 font-mono text-sm text-white outline-none disabled:cursor-not-allowed disabled:opacity-40"
                placeholder="PO number" />
            </div>
            ${c.jobs.length > 0 ? html`
              <div>
                <label class="block text-xs text-slate-400" for="pos-job">Job</label>
                <div class="relative mt-1">
                  <select id="pos-job" .value=${this._jobId} ?disabled=${finished}
                    @change=${(e: Event) => { this._jobId = (e.target as HTMLSelectElement).value; void this._saveAccountRefs(); }}
                    class="w-full appearance-none rounded border border-white/10 bg-slate-steel py-2 pl-2 pr-7 text-sm text-white outline-none disabled:cursor-not-allowed disabled:opacity-40">
                    <option value="">No job</option>
                    ${c.jobs.map((j) => html`<option value=${j.id} ?selected=${j.id === this._jobId}>${j.name}</option>`)}
                  </select>
                  <span class="pointer-events-none absolute inset-y-0 right-2 flex items-center text-xs text-slate-400" aria-hidden="true">&#9662;</span>
                </div>
              </div>` : nothing}
          </div>
          ${finished ? nothing : html`
            <div class="mt-3 flex items-center justify-between gap-3">
              <span class="text-xs text-slate-400">${hasLines ? '' : 'Add items to continue'}</span>
              <button data-testid="pos-next-payment" ?disabled=${!hasLines || this._till === null}
                @click=${() => { this._step = 'payment'; this._open = 'payment'; }}
                class="min-h-11 rounded border border-white/20 px-4 py-2 text-sm font-semibold text-white hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent">Next: Payment</button>
            </div>`}
        </div>`;
    }
    return html`
      <div id="pos-customer" class="rounded-lg bg-deep-space p-3" data-testid="pos-customer">
        <div class="flex items-center justify-between gap-2">
          <label for="pos-customer-search" class="text-xs font-semibold uppercase tracking-wide text-slate-400">Find the customer</label>
          <button data-testid="pos-walk-in" @click=${() => this._walkInSale()} ?disabled=${this._till === null}
            class="min-h-11 rounded border border-white/20 px-3 py-1.5 text-sm font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent">Walk-in sale</button>
        </div>
        <input id="pos-customer-search" type="text" autocomplete="off"
          placeholder="Find by name or account number"
          aria-label="Find a customer by name or account number"
          .value=${this._custQuery}
          ?disabled=${hasLines || this._till === null}
          @input=${(e: Event) => { this._custQuery = (e.target as HTMLInputElement).value; }}
          class="mt-2 w-full rounded border border-white/10 bg-slate-steel px-2 py-2 text-sm text-white outline-none disabled:cursor-not-allowed disabled:opacity-40" />
        ${this._till === null ? nothing : html`<div class="mt-1 text-xs text-slate-400">Name the customer, then add products</div>`}
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
    if (this._till === null) return;
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
      this._receiptNote = null;
      this._receiptRefused = false;
      this._dropReceiptDownload();
      // A completed sale on screen stays offerable for a void or a return
      // until this new sale replaces it (New Sale, a product or a customer).
      const reg = this._registerId;
      const sale = await this._emptyOrNewSale(reg);
      // a late answer for a register no longer selected is dropped
      if (reg !== this._registerId) return;
      this._sale = sale;
      this._tenderAmount = '';
      this._tenderMethod = 'cash';
      this._step = 'customer';
      this._open = 'customer';
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to start the sale';
    } finally {
      this._loading = false;
    }
  }

  /**
   * Each visit to the counter used to open a new empty sale, so abandoned empty
   * sales piled up on the register. The sale this person last opened on this
   * register is reused while it is still open, empty, a walk-in, theirs (the
   * server stamps the cashier at the create) and on the till session now open;
   * otherwise a new one is started and remembered. The counter opens a sale
   * only while a till is open, so a sale always carries its till session.
   */
  private async _emptyOrNewSale(reg: string): Promise<Sale> {
    const who = this._person();
    const key = `${SALE_STORAGE_PREFIX}${who.key}.${reg}`;
    const remembered = localStorage.getItem(key);
    if (remembered) {
      try {
        const s = await posService.getSale(remembered);
        const empty = s.status === 'open' && s.register_id === reg && s.customer_id === null
          && s.cashier_id === who.cashierId
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

  /**
   * Before a product is added or a customer named: with no till open nothing is
   * created, the cashier is told and the focus goes to Open Till; a completed
   * sale on screen gives way to a fresh one in this till session. True when an
   * open sale is there to act on.
   */
  private _fresh: Promise<void> | null = null;

  /** Every sale start goes through this one promise, so two never run at once. */
  private _beginFresh(): Promise<void> {
    // a start still in flight for a register no longer selected is not joined
    if (this._fresh && this._freshReg === this._registerId) return this._fresh;
    const flight: Promise<void> = this._startNewSale().finally(() => {
      if (this._fresh === flight) this._fresh = null;
    });
    this._fresh = flight;
    this._freshReg = this._registerId;
    return flight;
  }
  private _freshReg = '';

  private async _readySale(): Promise<boolean> {
    if (this._till === null) {
      this._needTill();
      return false;
    }
    // A start already in flight (New Sale, the till opening, a quick add)
    // is awaited whatever the sale on screen says, so no second sale starts.
    if (this._fresh) await this._fresh;
    const st = this._sale?.status;
    if (st === 'completed' || st === 'voided') {
      // Two quick adds share one fresh sale: the second waits on the first's start.
      await this._beginFresh();
    }
    return this._sale !== null && this._sale.status === 'open';
  }

  /** The counter's own message, and the focus on Open Till. */
  private _needTill() {
    this._error = 'Open the till to start a sale';
    void this.updateComplete.then(() => {
      (this.querySelector('#pos-open-till') as HTMLElement | null)?.focus();
    });
  }

  /** True once the line is on the sale; a refused add answers false. */
  private async _addItem(product: QuickSearchResult, quantity: string): Promise<boolean> {
    if (!(await this._readySale())) return false;
    if (!this._sale) return false;
    const qty = normalizeQuantity(quantity);
    if (qty === null) {
      this._error = 'Enter a quantity above zero';
      return false;
    }
    if (qty.includes('.') && isCountedUnit(product.uom)) {
      this._error = `${product.uom} is counted in whole units`;
      return false;
    }
    try {
      this._error = null;
      const updated = await posService.addItem(this._sale.id, product.product_id, qty, product.uom);
      this._sale = updated;
      if (this._step === 'payment' && this._open === null) this._open = 'payment';
      return true;
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to add item';
      return false;
    }
  }

  /** The catalogue's Add: the same add path, with its quantity; the card resets only on an accepted add. */
  private _onCatalogAdd = (e: Event) => {
    const detail = (e as CustomEvent<{ product?: QuickSearchResult & { image_url?: string | null }; qty?: string; query?: string }>).detail;
    const product = detail?.product;
    if (!product) return;
    if (product.image_url) this._images = { ...this._images, [product.product_id]: product.image_url };
    const sent = { qty: detail.qty, query: detail.query };
    void this._addItem(product, detail.qty ?? '1').then((accepted) => {
      if (!accepted) return;
      (this.querySelector('gable-pos-catalog') as (HTMLElement & { resetQuantity?: (s?: { qty?: string; query?: string }) => void }) | null)
        ?.resetQuantity?.(sent);
    });
  };

  /** Removes every line of the open sale through the line removal, after a confirm. */
  private async _clearSale() {
    const sale = this._sale;
    if (!sale || sale.status !== 'open') return;
    const ids = sale.lines.filter((l) => l.parent_line_id === null).map((l) => l.id);
    if (ids.length === 0) return;
    if (!window.confirm(`Remove all ${ids.length} line${ids.length === 1 ? '' : 's'} from sale ${sale.number}?`)) return;
    for (const id of ids) {
      if (this._sale?.id !== sale.id) return;
      await this._removeItem(id);
      if (this._error) return;
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
      // A timer set earlier must not clear this completion's band.
      if (this._errorTimer) clearTimeout(this._errorTimer);
      const completed = await posService.completeSale(this._sale.id, this._sale.revision, tenders);
      this._sale = completed;
      this._completed = completed;
      // The Sale complete card says it (number, total, tenders, change): no band over the page.
      this._success = null;
      this._pendingTenders = [];
      this._tenderAmount = '';
      this._step = 'summary';
      this._open = null;
      if (tenders.some((t) => t.method === 'account')) void this._refreshStanding();

      // The completed sale, its band and the change due stay until the cashier
      // moves on: New Sale, a product added or a customer named.
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
      : state === 'current' ? 'border-gable-green text-white' : 'border-white/10 text-slate-400';
    return html`
      <span data-testid="progress-${label.toLowerCase()}" data-state=${state}
        class="flex items-center gap-2 rounded-full border px-3 py-1 text-xs font-semibold ${tone}">
        <span class="font-mono">${state === 'done' ? '✓' : n}</span>${label}
      </span>`;
  }

  private _renderHeader(sale: Sale | null, progress: ReturnType<typeof panelProgress>) {
    const cashier = authCustody.displayName ?? 'Signed in';
    return html`
      <header class="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-white/10 bg-slate-steel px-4 py-2 min-[900px]:flex-nowrap" data-testid="pos-header">
        <div class="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-2 min-[900px]:flex-nowrap">
          <div class="shrink-0">
            <div class="flex items-baseline gap-2" data-testid="pos-title-row">
              <h1 class="text-xl font-bold text-white">New sale</h1>
              ${sale ? html`<span class="font-mono text-xs text-slate-400">${sale.number} &middot; ${sale.status}</span>` : nothing}
            </div>
            ${this._register ? html`
              <p class="flex flex-wrap items-center gap-x-2 text-xs text-slate-400" data-testid="pos-yard-register">
                <span>${this._register.branch_name}</span><span aria-hidden="true">&middot;</span>
                <span class="font-mono" data-testid="pos-register">${this._registerId}</span>
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
              </p>` : nothing}
          </div>
          <nav class="flex flex-wrap items-center gap-2" aria-label="Sale progress">
            ${this._progressChip('Customer', 1, progress.customer)}
            ${this._progressChip('Payment', 2, progress.payment)}
            ${this._progressChip('Summary', 3, progress.summary)}
          </nav>
        </div>
        <div class="flex shrink-0 flex-wrap items-center justify-end gap-x-2 gap-y-2 min-[900px]:flex-nowrap">
          ${this._till ? html`
            <span title="Drawer open with a float of ${fmt(this._till.opening_float_cents)}" class="rounded-full border border-gable-green/40 px-2 py-0.5 font-mono text-xs font-semibold text-gable-green" data-testid="pos-till-state">Till open</span>` : html`
            <span class="rounded-full border border-safety-red/40 px-2 py-0.5 text-xs font-semibold text-safety-red" data-testid="pos-till-state">No till</span>`}
          <span class="max-w-[8rem] truncate text-xs text-slate-200" title="Cashier" data-testid="pos-cashier">${cashier}</span>
          ${!this._register ? nothing : this._till ? html`
            <button @click=${() => this._beginCloseTill()} ?disabled=${this._tillBusy}
              title="Count the drawer and close the till with its Z report"
              class="whitespace-nowrap rounded border border-white/20 px-3 py-1 text-xs text-slate-200">Close Till</button>` : html`
            <button id="pos-open-till" @click=${() => { this._showTillOpen = true; }}
              class="whitespace-nowrap rounded bg-gable-green px-3 py-1 text-xs font-semibold text-deep-space">Open Till</button>`}
          ${this._register && this._till ? html`
          <button @click=${() => this._beginFresh()}
            class="whitespace-nowrap rounded border border-white/20 px-3 py-1 text-xs text-slate-200">New Sale</button>` : nothing}
        </div>
      </header>`;
  }

  /** The Items card is the catalogue; it dims and locks with no till or no open sale. */
  private _renderItems() {
    return html`
      <div class="w-full min-w-0 min-[900px]:w-[58%] min-[900px]:shrink-0 xl:w-[56%] ${this._till === null ? 'opacity-60' : ''}" data-testid="pos-items">
        <gable-pos-catalog ?disabled=${this._till === null} yard-key=${this._registerId}
          note=${this._sale?.status === 'completed' ? 'Add a product to start the next sale' : ''}></gable-pos-catalog>
      </div>`;
  }

  /** One linear panel's header: a disclosure button with its number or check, its value and Change. */
  private _panelHead(key: PanelKey, n: number, title: string, state: PanelState, isOpen: boolean, value: string) {
    const done = state === 'done';
    const finished = this._sale?.status === 'completed';
    return html`
      <h3 class="m-0">
        <button type="button" id="pos-panel-${key}-head" data-testid="panel-${key}-toggle"
          aria-expanded=${isOpen ? 'true' : 'false'} aria-controls="pos-panel-${key}-body"
          ?disabled=${this._till === null || finished}
          @click=${() => this._togglePanel(key)}
          class="flex min-h-11 w-full items-center gap-3 rounded-xl px-4 py-3 text-left disabled:cursor-not-allowed">
          <span class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border font-mono text-xs ${done ? 'border-gable-green text-gable-green' : isOpen ? 'border-gable-green text-white' : 'border-white/20 text-slate-400'}">${done ? '✓' : n}</span>
          <span class="text-base font-semibold ${isOpen || done ? 'text-white' : 'text-slate-400'}">${title}</span>
          ${!isOpen && value !== '' ? html`<span class="ml-auto min-w-0 truncate text-sm text-slate-300">${value}</span>` : nothing}
          ${!isOpen && done && !finished ? html`<span class="${value === '' ? 'ml-auto ' : ''}shrink-0 text-sm font-semibold text-gable-green">Change</span>` : nothing}
        </button>
      </h3>`;
  }

  private _panelClass(isOpen: boolean) {
    return `rounded-xl border bg-slate-steel ${isOpen ? 'border-gable-green' : 'border-white/10'}`;
  }

  /** What the tenders come to, in words: the added ones and the entry, or what the sale took. */
  private _paymentValue(sale: Sale | null): string {
    if (sale?.status === 'completed') {
      return sale.tenders.map((t) => `${methodLabel(t.method)} ${fmt(t.amount_cents)}`).join(' + ');
    }
    const parts = this._pendingTenders.map((t) => `${methodLabel(t.method)} ${fmt(t.amount_cents)}`);
    const entry = this._entry;
    if (entry !== null && this._tenderedSoFar < (sale?.total_cents ?? 0)) {
      parts.push(`${methodLabel(entry.method)} ${fmt(entry.amount_cents)}`);
    }
    return parts.join(' + ');
  }

  private _renderPaymentPanel(sale: Sale | null, hasLines: boolean, state: PanelState, isOpen: boolean) {
    const open = sale?.status === 'open';
    const total = sale?.total_cents ?? 0;
    const owed = this._owedCents;
    const entry = this._entry;
    const isCash = this._tenderMethod === 'cash';
    const change = isCash && entry !== null && this._tenderedSoFar < total
      ? Math.max(this._tenderedSoFar + entry.amount_cents - total, 0) : 0;
    const ready = this._till !== null && hasLines && open;
    // Why Charge to account is off, in words, once a customer is named; the way on when it is the PO.
    const accountWhy = this._customer !== null && open ? this._accountRefusal : null;
    const needsPO = accountWhy === PO_REFUSAL;
    return html`
      <section class="${this._panelClass(isOpen)} ${this._till === null ? 'opacity-50' : ''}" data-testid="panel-payment"
        aria-disabled=${this._till === null ? 'true' : 'false'}>
        ${this._panelHead('payment', 2, 'Payment', state, isOpen, state === 'done' ? this._paymentValue(sale) : '')}
        <div id="pos-panel-payment-body" data-testid="panel-payment-body" ?hidden=${!isOpen} class="px-4 pb-4">
          <div class="text-xs font-semibold uppercase tracking-wide text-slate-400">Amount due</div>
          <div class="font-mono text-4xl font-bold text-white" data-testid="pos-amount-due">${fmt(owed)}</div>
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
          <div class="mt-4 text-xs font-semibold uppercase tracking-wide text-slate-400">How is the customer paying?</div>
          <div class="mt-2 grid grid-cols-2 gap-2" role="group" aria-label="Tender">
            ${METHODS.map((m) => {
              const refused = m === 'account' ? this._accountRefusal : null;
              const chosen = this._tenderMethod === m && this._till !== null;
              return html`
                <button data-testid="tender-${m}" @click=${() => this._chooseMethod(m)}
                  ?disabled=${!hasLines || !open || refused !== null}
                  title=${m === 'account' ? (refused ?? 'Charge the sale to the customer’s account') : ''}
                  aria-pressed=${chosen ? 'true' : 'false'}
                  class="min-h-14 rounded-lg border px-3 py-3 text-base font-semibold ${m === 'account' ? '' : 'capitalize'} disabled:cursor-not-allowed ${chosen ? 'border-gable-green text-gable-green' : 'border-white/10 text-white disabled:opacity-40'}">
                  ${m === 'account' ? 'Charge to account' : m}
                </button>`;
            })}
          </div>
          ${accountWhy !== null ? html`
            <div class="mt-3 rounded-lg border border-amber-400/40 bg-amber-400/10 p-3 text-sm text-amber-300" data-testid="pos-account-refusal">
              <p>${accountWhy}</p>
              ${needsPO ? html`
                <button data-testid="pos-refusal-customer" @click=${() => { this._step = 'customer'; this._open = 'customer'; }}
                  class="mt-2 min-h-11 rounded border border-white/20 px-3 py-2 text-sm font-semibold text-white hover:bg-white/5">Enter the purchase order in Customer</button>` : nothing}
            </div>` : nothing}
          <label class="mt-3 block text-xs text-slate-400" for="pos-tender-amount">
            ${isCash ? 'Cash tendered' : html`<span class="capitalize">${this._tenderMethod}</span> amount`}
          </label>
          <input id="pos-tender-amount" type="text" inputmode="decimal" autocomplete="off"
            placeholder=${owed > 0 ? (owed / 100).toFixed(2) : ''} .value=${this._tenderAmount}
            ?disabled=${!hasLines || !open}
            @input=${(e: Event) => { this._tenderAmount = (e.target as HTMLInputElement).value; }}
            @blur=${() => this._formatTenderAmount()}
            class="mt-1 w-full rounded border border-white/10 bg-deep-space px-3 py-2 text-right font-mono text-xl font-bold text-white outline-none focus:border-gable-green"
            aria-label="Tender amount" />
          ${isCash && change > 0 ? html`
            <div class="mt-2 flex justify-between text-sm font-bold text-gable-green" data-testid="pos-change">
              <span>Change due</span><span class="font-mono">${fmt(change)}</span>
            </div>` : nothing}
          <div class="mt-3 flex gap-2">
            <button data-testid="pos-add-tender" @click=${() => this._addPendingTender()}
              ?disabled=${!hasLines || !open || entry === null || owed <= 0}
              class="min-h-11 flex-1 rounded border border-white/20 px-3 py-2 text-xs text-slate-200 disabled:opacity-40">Add as split tender</button>
            ${this._pendingTenders.length > 0 ? html`
              <button @click=${() => { this._pendingTenders = []; }}
                class="min-h-11 rounded border border-white/10 px-3 py-2 text-xs text-slate-400">Clear tenders</button>` : nothing}
          </div>
          <div class="mt-4 flex items-center justify-between gap-2">
            <button data-testid="pos-payment-back" @click=${() => { this._step = 'customer'; this._open = 'customer'; }}
              class="min-h-11 rounded px-3 py-2 text-sm font-semibold text-slate-300 hover:text-white">Back</button>
            <button data-testid="pos-next-summary" ?disabled=${!ready}
              @click=${() => { this._step = 'summary'; this._open = 'summary'; }}
              class="min-h-11 rounded border border-white/20 px-4 py-2 text-sm font-semibold text-white hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent">Next: Summary</button>
          </div>
        </div>
      </section>`;
  }

  private _renderSummaryPanel(sale: Sale | null, state: PanelState, isOpen: boolean) {
    const count = (sale?.lines ?? []).filter((l) => l.parent_line_id === null).length;
    const tender = this._paymentValue(sale);
    return html`
      <section class="${this._panelClass(isOpen)} ${this._till === null ? 'opacity-50' : ''}" data-testid="panel-summary">
        ${this._panelHead('summary', 3, 'Summary', state, isOpen, sale?.status === 'completed' ? fmt(sale.total_cents) : '')}
        <div id="pos-panel-summary-body" data-testid="panel-summary-body" ?hidden=${!isOpen} class="px-4 pb-4">
          <dl class="text-sm">
            <div class="flex justify-between py-1"><dt class="text-slate-400">Customer</dt>
              <dd class="text-white">${this._customer?.name ?? 'Walk-in sale'}</dd></div>
            <div class="flex justify-between py-1"><dt class="text-slate-400">Lines</dt>
              <dd class="font-mono text-white" data-testid="review-lines">${count}</dd></div>
            <div class="flex justify-between py-1"><dt class="text-slate-400">Tender</dt>
              <dd class="font-mono text-white">${tender === '' ? 'None yet' : tender}</dd></div>
            <div class="flex justify-between py-1 font-bold"><dt class="text-slate-200">Total</dt>
              <dd class="font-mono text-white" data-testid="review-total">${fmt(sale?.total_cents ?? 0)}</dd></div>
          </dl>
          <div class="mt-3">
            <button data-testid="pos-summary-back" @click=${() => { this._step = 'payment'; this._open = 'payment'; }}
              class="min-h-11 rounded px-3 py-2 text-sm font-semibold text-slate-300 hover:text-white">Back</button>
          </div>
        </div>
      </section>`;
  }

  private _renderThumb(line: SaleLine) {
    const src = line.product_id ? this._images[line.product_id] : undefined;
    return html`
      <span data-testid="pos-line-thumb" class="flex h-10 w-10 shrink-0 items-center justify-center overflow-hidden rounded-md border border-white/10 bg-deep-space">
        ${src ? html`<img src=${src} alt="" class="h-full w-full object-cover" />` : html`
          <svg viewBox="0 0 24 24" class="h-5 w-5 text-slate-500" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
            <rect x="3" y="4" width="18" height="16" rx="2"></rect><circle cx="9" cy="10" r="1.5"></circle>
            <path d="M21 16l-5-5-8 9"></path>
          </svg>`}
      </span>`;
  }

  private _renderOrderSummary(sale: Sale | null, productLines: SaleLine[], hasLines: boolean) {
    const owed = this._owedCents;
    const total = sale?.total_cents ?? 0;
    const open = sale?.status === 'open';
    const completed = sale?.status === 'completed';
    const plan = payPlan(total, this._pendingTenders, this._entry);
    const canPay = this._till !== null && hasLines && open && !this._loading && plan.ok;
    const clearable = open && hasLines && !this._loading;
    const label = this._loading ? 'Processing...'
      : completed ? 'Paid in full'
      : this._till === null ? `Pay ${fmt(total)}`
      : plan.ok ? `Pay ${fmt(total)}`
      : hasLines ? `Still owed ${fmt(plan.owedCents)}` : `Pay ${fmt(total)}`;
    return html`
      <section class="order-first rounded-xl border border-white/10 bg-slate-steel p-4 min-[900px]:order-none" data-testid="pos-order-summary">
        <h2 class="text-lg font-bold text-white">Order summary</h2>
        <p class="text-sm text-slate-400" data-testid="pos-order-customer">${this._customer?.name ?? 'Walk-in sale'}</p>
        ${productLines.length === 0 ? html`
          <p class="mt-3 text-sm text-slate-400" data-testid="pos-no-lines">No items yet</p>` : html`
          <ul class="mt-3 space-y-3" aria-label="Ticket lines" data-testid="pos-lines">
            ${productLines.map((item) => html`
              <li class="flex items-center gap-3">
                ${this._renderThumb(item)}
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm font-semibold text-white">${item.description}</div>
                  <div class="font-mono text-xs text-slate-400">${item.quantity} ${item.uom} at ${item.unit_price_ten_thousandths !== null ? formatPrice4(item.unit_price_ten_thousandths) : '-'}</div>
                </div>
                <div class="font-mono text-sm font-semibold text-white">${item.line_total_cents !== null ? fmt(item.line_total_cents) : '-'}</div>
                ${open ? html`
                <button @click=${() => this._removeItem(item.id)} title="Remove"
                  aria-label="Remove ${item.description}"
                  class="inline-flex h-11 w-11 shrink-0 items-center justify-center rounded text-lg text-safety-red hover:bg-safety-red/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-safety-red">×</button>` : nothing}
              </li>`)}
          </ul>`}
        <dl class="mt-4 border-t border-white/10 pt-3 text-sm">
          <div class="flex justify-between py-1 text-slate-400"><dt>Items</dt>
            <dd class="font-mono" data-testid="summary-lines">${productLines.length}</dd></div>
          <div class="flex justify-between py-1 text-slate-400"><dt>Subtotal</dt>
            <dd class="font-mono" data-testid="summary-subtotal">${fmt(sale?.subtotal_cents ?? 0)}</dd></div>
          <div class="flex justify-between py-1 text-slate-400"><dt>Tax</dt>
            <dd class="font-mono" data-testid="summary-tax">${fmt(sale?.tax_cents ?? 0)}</dd></div>
          <div class="mt-1 flex items-baseline justify-between gap-4 border-t border-white/10 pt-2 font-bold text-white"><dt>Total</dt>
            <dd class="font-mono text-2xl text-white" data-testid="summary-total">${fmt(total)}</dd></div>
          ${completed ? html`
            ${(sale?.tenders ?? []).map((t) => html`
              <div class="flex justify-between py-1 text-slate-300"><dt>${t.method === 'account' ? 'Charged to account' : `Paid by ${t.method}`}</dt>
                <dd class="font-mono">${fmt(t.amount_cents)}</dd></div>`)}
            ${(sale?.change_cents ?? 0) > 0 ? html`
              <div class="flex justify-between py-1 font-semibold text-gable-green"><dt>Change given</dt>
                <dd class="font-mono">${fmt(sale?.change_cents ?? 0)}</dd></div>` : nothing}` : this._pendingTenders.length > 0 ? html`
            <div class="flex justify-between py-1 text-slate-400"><dt>Still owed</dt>
              <dd class="font-mono">${fmt(owed)}</dd></div>` : nothing}
        </dl>
        <button data-testid="pos-pay" @click=${() => this._pay()} ?disabled=${!canPay}
          class="mt-4 min-h-14 w-full rounded-lg px-4 py-4 text-base font-bold ${canPay ? 'bg-gable-green text-deep-space' : 'cursor-not-allowed border border-white/10 bg-white/5 text-slate-400 opacity-40'}">
          ${label}
        </button>
        ${completed ? nothing : html`
        <button data-testid="pos-clear-sale" @click=${() => this._clearSale()} ?disabled=${!clearable}
          class="mt-2 min-h-11 w-full rounded border px-3 py-2 text-sm font-semibold ${clearable ? 'border-white/20 text-slate-200 hover:bg-white/5' : 'cursor-not-allowed border-white/10 text-slate-400 opacity-40'}">Clear sale</button>`}
      </section>`;
  }

  /** Sends the sale's receipt to the contact on the account; the server says in words if there is none. */
  private async _emailReceipt(saleId: string) {
    if (this._receiptBusy) return;
    this._receiptBusy = true;
    try {
      this._error = null;
      await posService.emailReceipt(saleId);
      this._receiptNote = 'Receipt sent to the contact on the account';
      this._receiptRefused = false;
    } catch (err: unknown) {
      this._receiptNote = err instanceof Error ? err.message : 'Failed to email the receipt';
      this._receiptRefused = true;
    } finally {
      this._receiptBusy = false;
    }
  }

  private _dropReceiptDownload() {
    if (this._receiptDownload) URL.revokeObjectURL(this._receiptDownload);
    this._receiptDownload = null;
  }

  /** Opens the sale's receipt document; a refusal is said in words beside Print receipt. */
  private async _printReceipt(saleId: string) {
    this._dropReceiptDownload();
    try {
      const shown = await posService.printReceipt(saleId);
      if (shown?.blocked) {
        this._receiptDownload = shown.url;
        this._receiptNote = 'Allow pop ups for this page to print the receipt, or download it';
        this._receiptRefused = true;
        return;
      }
      this._receiptNote = null;
      this._receiptRefused = false;
    } catch (err: unknown) {
      this._receiptNote = err instanceof Error ? err.message : 'Failed to print the receipt';
      this._receiptRefused = true;
    }
  }

  private _renderSaleComplete(sale: Sale) {
    // Void and Return act on the completed sale on screen only: an empty new
    // sale, whatever the one before it was, offers neither.
    const followOn = this._completed !== null && sale.id === this._completed.id;
    const follow = followOn
      ? 'border-white/10 text-slate-400 hover:bg-white/5 hover:text-white'
      : 'cursor-not-allowed border-white/10 text-slate-400 opacity-40';
    const how = sale.tenders.map((t) => `${methodLabel(t.method)} ${fmt(t.amount_cents)}`).join(', ');
    const invoice = sale.invoice_id;
    return html`
      <section class="rounded-xl border border-gable-green bg-slate-steel p-4" data-testid="pos-complete" aria-live="polite">
        <div class="flex items-center gap-3">
          <span class="flex h-12 w-12 shrink-0 items-center justify-center rounded-full bg-gable-green text-2xl font-bold text-deep-space" aria-hidden="true">✓</span>
          <div class="min-w-0">
            <h2 class="text-xl font-bold text-white">Sale complete</h2>
            <p class="font-mono text-sm text-slate-300">${sale.number} &middot; ${fmt(sale.total_cents)}${how !== '' ? html` &middot; ${how}` : nothing}${sale.change_cents > 0 ? html`, change ${fmt(sale.change_cents)}` : nothing}</p>
          </div>
        </div>
        ${invoice ? html`
          <div class="mt-4">
            <div class="text-xs font-semibold uppercase tracking-wide text-slate-400">Receipt</div>
            <div class="mt-2 flex flex-wrap gap-2">
              <button data-testid="pos-receipt-print" @click=${() => this._printReceipt(sale.id)}
                class="min-h-11 flex-1 rounded border border-white/20 px-3 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5">Print receipt</button>
              ${sale.customer_id !== null ? html`
                <button data-testid="pos-receipt-email" @click=${() => this._emailReceipt(sale.id)} ?disabled=${this._receiptBusy}
                  class="min-h-11 flex-1 rounded border border-white/20 px-3 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40">Email receipt</button>` : nothing}
            </div>
            ${sale.customer_id !== null ? html`<p class="mt-2 text-xs text-slate-400" data-testid="pos-receipt-contact">Email goes to the contact on the account</p>` : nothing}
            ${this._receiptNote ? html`<p class="mt-2 text-xs ${this._receiptRefused ? 'text-safety-red' : 'text-slate-400'}" data-testid="pos-receipt-note" role="status">${this._receiptNote}</p>` : nothing}
            ${this._receiptDownload ? html`<a data-testid="pos-receipt-download" href=${this._receiptDownload} download="receipt-${sale.number}.pdf"
              class="mt-1 inline-block text-xs font-semibold text-gable-green underline">Download the receipt</a>` : nothing}
          </div>` : nothing}
        <div class="mt-4 flex gap-2">
          <button data-testid="pos-void" @click=${() => this._voidSale()} ?disabled=${!followOn}
            class="min-h-9 flex-1 rounded border px-3 py-1.5 text-xs ${follow}">Void</button>
          <button @click=${() => this._beginReturn()} ?disabled=${!followOn}
            class="min-h-9 flex-1 rounded border px-3 py-1.5 text-xs ${follow}">Return</button>
        </div>
        <button @click=${() => this._beginFresh()} ?disabled=${this._till === null}
          class="mt-3 min-h-14 w-full rounded-lg bg-gable-green px-4 py-4 text-base font-bold text-deep-space disabled:cursor-not-allowed disabled:opacity-40">New sale</button>
      </section>`;
  }

  render() {
    const sale = this._sale;
    const lines: SaleLine[] = sale?.lines ?? [];
    const productLines = lines.filter((l) => l.parent_line_id === null);
    const hasLines = productLines.length > 0;
    const completed = sale?.status === 'completed';
    const progress = panelProgress(this._step, completed);
    const openKey = completed || this._till === null ? null : this._open;
    const customerValue = this._customer?.name ?? 'Walk-in sale';

    return html`
      <div class="flex h-screen flex-col bg-deep-space font-sans text-white" data-testid="pos-terminal">
        ${this._renderHeader(sale, progress)}
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
        <div class="flex min-h-0 flex-1 flex-col items-start gap-4 overflow-y-auto p-4 min-[900px]:flex-row" data-testid="pos-body">
          ${this._renderItems()}
          <div class="order-first flex w-full min-w-0 shrink-0 flex-col gap-3 min-[900px]:order-none min-[900px]:w-auto min-[900px]:flex-1" data-testid="pos-panels">
            <section class="${this._panelClass(openKey === 'customer')} ${this._till === null ? 'opacity-50' : ''}" data-testid="panel-customer">
              ${this._panelHead('customer', 1, 'Customer', progress.customer, openKey === 'customer', customerValue)}
              <div id="pos-panel-customer-body" data-testid="panel-customer-body" ?hidden=${openKey !== 'customer'} class="px-4 pb-4">
                ${this._renderCustomerPanel()}
              </div>
            </section>
            ${this._renderPaymentPanel(sale, hasLines, progress.payment, openKey === 'payment')}
            ${this._renderSummaryPanel(sale, progress.summary, openKey === 'summary')}
            ${completed && sale ? this._renderSaleComplete(sale) : nothing}
            ${this._renderOrderSummary(sale, productLines, hasLines)}
          </div>
        </div>`}
      </div>
    `;
  }
}

export default POSTerminal;
