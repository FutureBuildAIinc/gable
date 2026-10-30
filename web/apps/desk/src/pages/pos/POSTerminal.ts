// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import '../../components/ui/gable-select';
import { customElement, state } from 'lit/decorators.js';
import { posService, dollarsToCents, type TenderIn } from '../../services/POSService';
import type { PayerCaptureIn } from '../../services/POSService.ts';
import '../../pages/pos/PayerDialog.ts';
import { apiErrorMessage, ApiError } from '../../services/apiError';
import type {
  Sale, QuickSearchResult, SaleLine, TillSession, TillReport, TillMovement, TenderMethod, ExchangeTenderMethod, RefundMethod, CounterCustomer, CounterRegister,
  CounterStanding, ReturnReason, ReturnReasonCode, ExchangeFigures, ReturnFigures, IdType, SaleSummary, CardSurcharge,
} from '../../types/pos';
import { ID_TYPES } from '../../types/pos';
import { branchContext } from '../../services/BranchContext';
import { CustomerService } from '../../services/CustomerService';
import type { ShipTo } from '../../types/customer';
import {
  chargeRefusal, standingOf, money, PO_REFUSAL, JOB_REFUSAL, verdictLine, waysForward, wayLabel, buyerRefusal, checkRefusal,
  storeCreditOffer, tenderMethods, idRefusal, idRequired, idRequiredWords, holdStanding, cardRefusal, type CheckIDRule,
} from './standing';
import { differenceOf, exchangeTotals, reasonLabel, returnRefusal } from './exchange';
import { signatureRefusal, sealedWords } from './signatureLogic';
import './SignaturePad';
import type { SignaturePad } from './SignaturePad';
import './ScalePanel';
import { weightBadgeText } from './scale';
import { formatPrice4, formatCents } from '../../lib/utils';
import { isCountedUnit, normalizeQuantity } from '../../lib/money';
import { payPlan, panelProgress, type PanelState, type PanelKey } from './panels';
import { roundCashFor, type CashRoundingMode } from '../../lib/cashRounding';
import './PosOrderDeposit';
import { intentIdOfRefusal, cardOverWords, cardWaitWords, CARD_DECLINED_WORDS, CARD_CANCEL_WORDS } from './terminal';
import { receiptEmailAnswer, type ReceiptTone } from './receiptEmail';
import { cardFeeNotice } from './cardFee';
import { EmailDeliveryService } from '../../services/EmailDeliveryService';

/**
 * The money guard at the yard: the field shell's own offline words, so a
 * handheld sale is refused the way the shell's banner refuses, not with a
 * generic failure. Offline selling is the work shell's alone (ADR 0011).
 */
const YARD_OFFLINE_REFUSAL =
  'You are offline; the shell and its apps need the connection, and money never queues.';
import type { TerminalIntent } from '../../types/pos';
import { chordFromEvent, resolveHotkey, legendKeys, type HotkeyAction } from './hotkeys';
import { confirmDialog, promptDialog } from '../../components/ui/app-dialog';
import '../../components/ui/gable-date-field';
import './StepUpDialog';
import { authCustody } from '@gable/auth';
import './catalog/gable-pos-catalog';
import './TillMovementDialog.ts';
import './TillReportView.ts';
import './TillZReports.ts';
import './TintLineDialog.ts';
import { tintSummary } from './tint.ts';
import { kindLabel } from './movements.ts';
import './OfflineCounter';
import { offlineCounter, OfflineCounterError } from '../../services/OfflineCounterService';
import type { OfflineQueue, OfflineSignedSale } from '../../types/offline';
import {
  OFFLINE_BANNER, RESULT_UNKNOWN, SAME_GOODS_QUESTION, bannerFigures, carryCart, isLostAnswer,
  NEEDS_CONNECTION, NO_SESSION_ASK, queueDrained, snapshotRefusalWords, syncWords, tillCloseWait, unknownCardKey, unknownCartKey, type CarriedCart,
} from './offline';
import { POS_SW_WARNING_ATTR } from '../../lib/pos-sw';

const REGISTER_STORAGE_PREFIX = 'gable.pos.register.';
// The sale each register last opened, so a visit that left it empty reuses it.
const SALE_STORAGE_PREFIX = 'gable.pos.sale.';
const fmt = (cents: number) => `$${(cents / 100).toFixed(2)}`;
const tenderTitle = (m: TenderMethod) => (m === 'account' ? 'Charge to account' : m === 'store_credit' ? 'Store credit' : m);
/** A line carries a quantity control when it has a quantity the route reprices. */
const hasQuantityControl = (l: SaleLine) => l.parent_line_id === null && ['product', 'kit', 'charge'].includes(l.line_type);
const methodLabel = (m: TenderMethod) => (m === 'account' ? 'Charge to account' : m === 'store_credit' ? 'Store credit' : m.charAt(0).toUpperCase() + m.slice(1));

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
  // Null until the cashier presses a tender: nothing is assumed, so Pay cannot
  // complete a sale on a method nobody chose.
  @state() private _tenderMethod: TenderMethod | null = null;
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
  // The delivered sale's answer (CP-10): the customer's ship to the goods go
  // to, so the tax comes from that zone instead of the branch.
  @state() private _shipTos: ShipTo[] = [];
  @state() private _shipToId = '';
  // Can this sale go on account? The server's read of the account (its verdict,
  // the buyer roster, the check limit, the store credit), and the buyer named.
  @state() private _standing: CounterStanding | null = null;
  /** The counter alerts the clerk has read and said so about on this sale. */
  @state() private _acknowledgedAlerts: string[] = [];
  // The counter's deposit on an order of the attached customer (CP-07).
  @state() private _depositOpen = false;
  @state() private _buyerId = '';
  // The check being entered: its number, the ID checked and its kind; and the
  // largest check a manager approved for this sale (null: none).
  @state() private _checkRef = '';
  @state() private _idChecked = false;
  // The large cash capture (CP-17a): the instrument kind of the check being
  // entered, the manager's suspected avoidance mark, the payer the counter
  // captured and the ask the preview made.
  @state() private _checkInstrument = 'personal_check';
  @state() private _suspectAvoidance = false;
  @state() private _payer: PayerCaptureIn | null = null;
  @state() private _payerAsk: {
    fields: string[];
    dealerThreshold: boolean;
    /** floor is true when the branch's walk-in identity floor is the only question: who paid, so their cash sales are counted together (CP-17a). */
    floor: boolean;
    /** thresholdCrossed is true when the rule's threshold is crossed: the payer's details are required and a decline does not answer it. */
    thresholdCrossed: boolean;
  } | null = null;
  @state() private _payerRefused = false;
  /** The walk-in declined to identify over the branch's floor: the sale goes ahead and the fact is recorded for the manager's worklist (CP-17a). */
  @state() private _payerDeclined = false;
  /** The counter's plain words when the branch's floor is off: the walk-in's cash is not aggregated (CP-17a). */
  @state() private _walkInNote = '';
  // The identified payer stays on the till session for the rest of it, so the
  // clerk is asked once (CP-17a). The identity is held in memory only: it is
  // never written to the browser's storage.
  private _sessionPayer: PayerCaptureIn | null = null;
  private _sessionTillId: string | null = null;
  // The payer capture was asked for by the exchange, not by a sale: the
  // capture that answers it rings the exchange (CP-17a).
  @state() private _payerForExchange = false;
  @state() private _idType: IdType | '' = '';
  @state() private _checkApprovedCents: number | null = null;

  // The manager step up waiting on the PIN dialog: the reason and cap are kept
  // here, the token completes them (CP-04).
  @state() private _stepUp: { kind: 'account' | 'check'; reason: string; capCents: number } | null = null;
  // The words a check over the limit was refused in (the server's, or the
  // screen's from the customer's own limit), with Ask a manager beside them.
  @state() private _checkOverWords: string | null = null;
  // The dealer's check ID rule (never by default; a read failure reads as
  // never: the server holds the rule itself at the completion).
  @state() private _idRule: CheckIDRule | null = null;
  /** The active branch's card fee rule (CP-15); null while none is known. */
  @state() private _cardFee: CardSurcharge | null = null;
  // The dealer's database wide check limit from the same read, so a walk-in
  // (no standing of its own) is warned at entry, not first at the server.
  @state() private _dealerCheckLimitCents: number | null = null;

  // The linear panels: the sale's current step and the one panel open (or none).
  @state() private _step: PanelKey = 'customer';
  @state() private _open: PanelKey | null = 'customer';
  // The picture each product came with from the catalogue, by product id.
  @state() private _images: Record<string, string> = {};
  @state() private _receiptNote: string | null = null;
  @state() private _receiptTone: ReceiptTone = 'plain';
  @state() private _receiptBusy = false;
  /** A blob url the receipt can be downloaded from when its tab was blocked. */
  @state() private _receiptDownload: string | null = null;

  // The card the terminal still holds: the intent's own record, read again
  // while the desk shows the waiting state. Null when no card leg waits.
  @state() private _cardIntent: TerminalIntent | null = null;
  /** The card leg being watched, kept while its read is in flight or failed (ADR 0011 5.3). */
  private _cardWaitId: string | null = null;
  @state() private _cardNote: string | null = null;
  @state() private _cardBusy = false;
  /** Counts card reads; an answer to an older one is dropped. */
  private _cardSeq = 0;
  private _cardTimer: ReturnType<typeof setTimeout> | null = null;

  // The completed sale offered for a return or a void.
  @state() private _completed: Sale | null = null;
  @state() private _showReturn = false;
  @state() private _returnReason = '';
  @state() private _returnMethod: RefundMethod = 'cash';
  @state() private _returnQty: Record<string, string> = {};
  // The closed reason list with each reason's fee, a reason per returned line,
  // and the exchange: the goods that replace what comes back.
  @state() private _reasons: ReturnReason[] = [];
  @state() private _returnReasonByLine: Record<string, ReturnReasonCode> = {};
  @state() private _exchangeOn = false;
  @state() private _exchangeLines: Array<{ product: QuickSearchResult; qty: string }> = [];
  @state() private _exchangeQuery = '';
  @state() private _exchangeResults: QuickSearchResult[] = [];
  @state() private _exchangeFigures: ExchangeFigures | null = null;
  // The figures of the plain return (its restocking fee and the money back),
  // stated by the server before the return is taken.
  @state() private _returnFigures: ReturnFigures | null = null;
  // True while the figures of the plain return are pending: the debounce and
  // the round trip after it. The taking waits for the statement.
  @state() private _returnBusy = false;
  // How many times the figures have been asked for: an answer from an older
  // preview never clears the wait of a newer one.
  private _returnAsked = 0;
  @state() private _exchangeBusy = false;
  @state() private _exchangeError: string | null = null;
  @state() private _exchangeTender: ExchangeTenderMethod = 'cash';
  @state() private _exchangeRefund: 'cash' | 'account' = 'cash';
  // The return search modal: finding a past sale to return.
  @state() private _showReturnSearch = false;
  @state() private _returnSearchQuery = '';
  @state() private _returnSearchDate = '';
  @state() private _returnSearchResults: SaleSummary[] = [];
  @state() private _returnSearchBusy = false;
  @state() private _returnSearchError: string | null = null;
  // Whether each returned line goes back to stock (true) or is damaged (false).
  @state() private _returnRestockByLine: Record<string, boolean> = {};
  // The signature pad on the completed sale.
  @state() private _signerName = '';
  @state() private _signing = false;
  @state() private _signatureNote: string | null = null;
  @state() private _sealed: { signer: string; seal: string } | null = null;
  private _exchangeDebounce: ReturnType<typeof setTimeout> | null = null;
  // The counter's look before Pay (CP-17a): the server's answer for the
  // tenders entered so far, so the plain words are on the screen before the
  // clerk presses Pay.
  private _lookDebounce: ReturnType<typeof setTimeout> | null = null;

  // Till (drawer) state
  @state() private _till: TillSession | null = null;
  @state() private _showTillOpen = false;
  @state() private _openingFloat = '';
  // The empty float's refusal, in words inside the Open Till dialog.
  @state() private _tillFloatError = '';
  @state() private _showTillClose = false;
  @state() private _tillReport: TillReport | null = null;
  @state() private _counts: Record<string, string> = {};
  @state() private _closeResult: TillReport | null = null;
  @state() private _tillBusy = false;
  // The drawer's cash movements dialog (CP-08).
  @state() private _showMovement = false;
  /** The Till menu on a handheld: the till's money controls live behind it below 900. */
  @state() private _tillMenu = false;
  /** The connection, for the money guard: a yard sale never queues offline. */
  @state() private _online = typeof navigator === 'undefined' ? true : navigator.onLine;

  private _onConnection = () => {
    this._online = navigator.onLine;
    if (!this._online) void this._goOffline();
    else void this._reconnect();
  };

  // The counter's offline mode (ADR 0011 slice C2): with the work shell's
  // core enrolled for this register and a till open, the counter keeps
  // selling when the connection drops. A browser tab has no core and keeps
  // the online only refusal.
  @state() private _offline = false;
  @state() private _offlineSeed: CarriedCart | null = null;
  /** The cart whose completion was sent and not answered (ADR 0011 5.3). */
  @state() private _unknownCart: string | null = null;
  /** The unknown cart's card leg the terminal had not answered when the connection dropped. */
  private _unknownCard: string | null = null;
  @state() private _offlineQueue: OfflineQueue | null = null;
  @state() private _offlineStrip: string | null = null;
  @state() private _offlineRoom: ReturnType<typeof bannerFigures> = null;
  /** The abandoned unknown cart whose default linked offline sale the clerk is asked about. */
  @state() private _sameGoods: { cartId: string; saleId: string; offlineRef: string } | null = null;
  private _completing = false;
  private _reconnecting = false;
  @state() private _needsSignIn = false;

  // The X report of the open drawer, and the register's stored Z reports (CP-08).
  @state() private _showXReport = false;
  @state() private _xReport: TillReport | null = null;
  @state() private _showZReports = false;
  // The scale panel overlay (CP-19).
  @state() private _showScale = false;

  // The tint line's dialog (row CP-16): the open sale line it was opened for.
  @state() private _tintLine: SaleLine | null = null;

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
    window.addEventListener('online', this._onConnection);
    window.addEventListener('offline', this._onConnection);
    this.addEventListener('pos-catalog-add', this._onCatalogAdd);
    document.addEventListener('keydown', this._onHotkey);
    this._yardId = branchContext.currentId;
    void this._loadRegisters();
    void this._loadIDRule();
  }

  /** Whether the work shell's core sells offline for the register on screen. */
  private async _offlineReady(): Promise<boolean> {
    if (!offlineCounter.available() || !this._registerId) return false;
    try {
      const enrolment = await offlineCounter.enrolment();
      return enrolment.register_id === this._registerId;
    } catch {
      return false;
    }
  }

  private async _refreshOfflineQueue() {
    try {
      const queue = await offlineCounter.queue();
      this._offlineQueue = queue;
      const snapshot = await offlineCounter.snapshot().catch(() => null);
      this._offlineRoom = bannerFigures(queue, snapshot);
    } catch {
      this._offlineQueue = null;
      this._offlineRoom = null;
    }
  }

  /** The unknown cart survives a reload: it is kept per register until it is read online. */
  private _restoreUnknownCart() {
    try {
      this._unknownCart = this._registerId ? localStorage.getItem(unknownCartKey(this._registerId)) : null;
      this._unknownCard = this._registerId && this._unknownCart ? localStorage.getItem(unknownCardKey(this._registerId)) : null;
    } catch {
      this._unknownCart = null;
      this._unknownCard = null;
    }
  }

  private _setUnknownCart(id: string | null, cardIntentId: string | null = null) {
    this._unknownCart = id;
    this._unknownCard = id ? cardIntentId : null;
    try {
      if (!this._registerId) return;
      if (id) localStorage.setItem(unknownCartKey(this._registerId), id);
      else localStorage.removeItem(unknownCartKey(this._registerId));
      if (this._unknownCard) localStorage.setItem(unknownCardKey(this._registerId), this._unknownCard);
      else localStorage.removeItem(unknownCardKey(this._registerId));
    } catch {
      // a refused store keeps the cart for this page only
    }
  }

  /** The card leg the terminal has not answered: waiting at the device, or its read in flight or failed. */
  private _cardInFlight(): string | null {
    if (this._cardIntent) return ['claimed', 'pending_at_device'].includes(this._cardIntent.state) ? this._cardIntent.id : null;
    return this._cardWaitId;
  }

  /**
   * The connection dropped (ADR 0011 5.3). With no completion in flight the
   * open cart's lines become the local cart and the offline sale names it;
   * a completion or a terminal call sent and not answered is the result
   * unknown case, whose cart is never carried.
   */
  private async _goOffline(): Promise<boolean> {
    if (this._offline) return true;
    if (this._till === null || !(await this._offlineReady())) return false;
    let seed: CarriedCart | null = null;
    const card = this._cardInFlight();
    if (card !== null && this._sale && this._unknownCart === null) this._setUnknownCart(this._sale.id, card);
    if (!this._completing && this._unknownCart === null && this._sale && this._sale.status !== 'completed') {
      try {
        const snapshot = await offlineCounter.snapshot();
        seed = carryCart(this._sale, snapshot, () => crypto.randomUUID());
      } catch {
        seed = null;
      }
    }
    this._offlineSeed = seed;
    this._offline = true;
    this._error = this._unknownCart ? RESULT_UNKNOWN : null;
    await this._refreshOfflineQueue();
    return true;
  }

  /** The strip's "Sign in this register": the shell's sign in, then the reconnect runs again. */
  private async _signInRegister() {
    try {
      await offlineCounter.signIn();
    } catch (err) {
      this._offlineStrip = err instanceof Error ? err.message : null;
      return;
    }
    this._offlineStrip = null;
    this._needsSignIn = false;
    await this._reconnect();
  }

  /**
   * The connection returned: the queue drains first, then the core takes new
   * prices, then the unknown cart is read and followed (ADR 0011 5.3, 6.2).
   * The same runs when the counter opens online, so a register that went
   * away offline sends what it kept.
   */
  private async _reconnect() {
    if (this._reconnecting) return;
    if (!(await this._offlineReady())) {
      this._offline = false;
      return;
    }
    this._reconnecting = true;
    this._needsSignIn = false;
    const wasOffline = this._offline;
    let drained = false;
    try {
      try {
        const report = await offlineCounter.sync();
        this._offlineQueue = report.queue;
        drained = queueDrained(report.queue);
        this._offlineStrip = report.sent > 0 ? syncWords(report) : this._offlineStrip;
      } catch (err) {
        this._offlineStrip = err instanceof Error ? err.message : null;
        if (err instanceof OfflineCounterError && err.code === 'no_session') this._needsSignIn = true;
      }
      try {
        await offlineCounter.refreshSnapshot();
      } catch (err) {
        if (err instanceof OfflineCounterError && err.code === 'no_session') {
          this._needsSignIn = true;
          this._offlineStrip = this._offlineStrip ?? err.message;
        } else if (err instanceof OfflineCounterError && err.code !== 'no_core') {
          this._offlineStrip = [this._offlineStrip, snapshotRefusalWords(err.code, err.message)].filter(Boolean).join('. ');
        }
      }
      // 5.3: the unknown cart is read only once the queue has drained; a
      // linked sale still waiting means the cart may yet be abandoned by it
      if (drained) await this._settleUnknownCart();
      await this._refreshOfflineQueue();
    } finally {
      this._reconnecting = false;
      if (wasOffline && navigator.onLine) {
        this._offline = false;
        this._offlineSeed = null;
        await this._loadTill();
        if (this._sale === null || this._sale.status !== 'open') void this._beginFresh();
      }
    }
  }

  /**
   * Reads the result unknown cart online and follows what it says (ADR 0011
   * 5.3): completed, it is shown complete; abandoned by a default linked
   * offline sale, the clerk is asked whether it was the same customer and
   * goods; still open, it returns to the screen.
   */
  private async _settleUnknownCart() {
    const id = this._unknownCart;
    if (!id) return;
    // the card leg first: while the terminal may still take the card the cart stays unknown
    const card = this._unknownCard;
    if (card) {
      try {
        const intent = await posService.getTerminalIntent(card);
        if (intent.state === 'claimed' || intent.state === 'pending_at_device') return;
      } catch {
        return;
      }
    }
    let sale: Sale;
    try {
      sale = await posService.getSale(id);
    } catch {
      return;
    }
    const status = sale.status as string;
    if (status === 'completed') {
      this._success = `The sale whose result was unknown completed online: ${sale.number}. A sale rung again for it offline is with a manager.`;
      this._setUnknownCart(null);
      this._error = null;
      return;
    }
    if (status === 'abandoned') {
      const linked = this._offlineQueue?.items.find((i) => i.sale.replaces_sale_id === id && i.state === 'synced');
      if (linked) this._sameGoods = { cartId: id, saleId: linked.sale.client_id, offlineRef: linked.sale.offline_ref };
      this._setUnknownCart(null);
      this._error = null;
      return;
    }
    if (status === 'open' || status === 'held') {
      this._sale = sale;
      this._setUnknownCart(null);
      this._error = null;
    }
  }

  private async _answerSameGoods(same: boolean) {
    const ask = this._sameGoods;
    if (!ask) return;
    try {
      await posService.answerReplacedCart(ask.saleId, same);
      this._sameGoods = null;
      this._success = same ? 'Recorded: the same customer and goods.' : 'Recorded for a manager: not the same customer and goods.';
    } catch (err) {
      this._error = apiErrorMessage(err, 'The answer could not be recorded');
    }
  }

  private _onOfflineSold = (e: Event) => {
    const sold = (e as CustomEvent<OfflineSignedSale>).detail;
    void sold;
    void this._refreshOfflineQueue();
  };

  /** Reads the dealer's check ID rule and check limit; a read failure leaves them off (the server still holds both). */
  private async _loadIDRule() {
    try {
      const s = await posService.settings();
      this._idRule = s.rules.check_id_required;
      this._dealerCheckLimitCents = s.rules.check_limit_cents;
      this._cardFee = s.rules.card_surcharge ?? null;
    } catch {
      this._idRule = null;
      this._dealerCheckLimitCents = null;
      this._cardFee = null;
    }
  }

  /**
   * A yard change moves the counter to that yard's register. An open sale
   * with lines stays open on its own register, so the cashier is asked first;
   * declining puts the yard back.
   */
  private _onYardChanged = () => {
    if (this._revertingYard) return;
    if (!this._saleHasLines) {
      this._yardId = branchContext.currentId;
      void this._loadRegisters();
      void this._loadIDRule();
      return;
    }
    const askedYard = branchContext.currentId;
    void confirmDialog({ title: this._leaveSaleQuestion(), confirmLabel: 'Leave sale', cancelLabel: 'Stay' }).then((leave) => {
      if (!leave) {
        // A newer yard change asked its own question in the meantime; leave it be.
        if (branchContext.currentId !== askedYard) return;
        this._revertingYard = true;
        try {
          branchContext.setCurrent(this._yardId);
        } finally {
          this._revertingYard = false;
        }
        return;
      }
      this._yardId = askedYard;
      void this._loadRegisters();
      void this._loadIDRule();
    });
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
      // Opened with no connection on an enrolled work shell register (a reload
      // offline, ADR 0011 5.2): the core's enrolment and snapshot name the
      // register and its till session, and the counter sells offline.
      if (!navigator.onLine && await this._openOfflineRegister()) return;
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
    this._restoreUnknownCart();
    if (offlineCounter.available()) void this._reconnect();
    await this._beginFresh();
  }

  /** The register and till the core's own copy names, for a counter opened offline. */
  private async _openOfflineRegister(): Promise<boolean> {
    if (!offlineCounter.available()) return false;
    try {
      const enrolment = await offlineCounter.enrolment();
      const snapshot = await offlineCounter.snapshot();
      this._registers = [{ id: enrolment.register_id, name: enrolment.register_id, branch_id: snapshot.branch_id, branch_code: '', branch_name: 'Offline' }];
      this._register = this._registers[0];
      this._registersLoaded = true;
      this._registersFailed = false;
      this._till = {
        id: snapshot.till_session.id, register_id: enrolment.register_id, branch_id: snapshot.branch_id,
        cashier_id: snapshot.till_session.cashier_id, status: 'open', opening_float_cents: snapshot.till_session.opening_float_cents,
        opened_at: snapshot.till_session.opened_at, closed_at: null, expected_by_method: {}, counted_by_method: {},
        over_short_cents: null, gl_entry_id: null, notes: '', closed_by: null,
      } as unknown as TillSession;
      this._restoreUnknownCart();
      this._offline = true;
      this._error = this._unknownCart ? RESULT_UNKNOWN : null;
      await this._refreshOfflineQueue();
      return true;
    } catch {
      return false;
    }
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
    const sub = s.kind === 'staff' ? s.sub : (s.claims.sub ?? '');
    const isId = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(sub);
    return { key: sub !== '' ? sub : 'anon', cashierId: isId ? sub.toLowerCase() : NIL };
  }

  /** False when the cashier declined to leave the open sale. */
  private async _chooseRegister(id: string): Promise<boolean> {
    const next = this._registers.find((r) => r.id === id);
    if (!next || next.id === this._registerId) return true;
    if (this._saleHasLines && !(await confirmDialog({ title: this._leaveSaleQuestion(), confirmLabel: 'Leave sale', cancelLabel: 'Stay' }))) return false;
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
    if (this._openingFloat.trim() === '') {
      // Nothing was counted: say what is missing in the dialog (the page's
      // error band sits under this overlay) and put the focus in the field.
      this._tillFloatError = 'Count the drawer and type the opening float first.';
      await this.updateComplete;
      (this.querySelector('[data-testid="till-float-amount"]') as HTMLInputElement | null)?.focus();
      return;
    }
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
      this._tillFloatError = '';
      this._success = `Till opened with ${fmt(this._till.opening_float_cents)} float`;
      this._errorTimer = setTimeout(() => { this._success = null; }, 2500);
      // Re-start the sale so it attaches to the new session.
      void this._beginFresh();
      // a register enrolled to sell offline takes its prices for the new session
      if (offlineCounter.available()) void this._reconnect();
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to open till';
    } finally {
      this._tillBusy = false;
    }
  }

  /** The X report of the open drawer: a mid-shift read, never a close. */
  private async _beginXReport() {
    if (!this._till) return;
    try {
      this._tillBusy = true;
      this._xReport = await posService.tillReport(this._till.id);
      this._showXReport = true;
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to load the X report';
    } finally {
      this._tillBusy = false;
    }
  }

  private async _beginCloseTill() {
    if (!this._till) return;
    // the till close waits for the queue and for a result unknown cart (ADR 0011 sections 4 and 5.3)
    if (offlineCounter.available()) await this._refreshOfflineQueue();
    const waits = tillCloseWait(this._offlineQueue, this._unknownCart);
    if (waits) {
      this._error = waits;
      return;
    }
    // The same question a register change asks: declining keeps till and sale.
    if (this._saleHasLines && !(await confirmDialog({ title: this._leaveSaleQuestion(), confirmLabel: 'Leave sale', cancelLabel: 'Stay' }))) return;
    try {
      this._tillBusy = true;
      this._tillReport = await posService.tillReport(this._till.id);
      // Seed blind-count inputs for every method the drawer expects (+ cash).
      // The report answers in storage spelling (CASH, CHECK); the close route
      // takes the wire's lowercase, so each method is one lowercase row.
      // account charges are not money in the drawer: never a row, never posted
      const methods = new Set<string>(['cash', ...Object.keys(this._tillReport.expected_by_method || {}).map((m) => m.toLowerCase()).filter((m) => m !== 'account')]);
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
    window.removeEventListener('online', this._onConnection);
    window.removeEventListener('offline', this._onConnection);
    this.removeEventListener('pos-catalog-add', this._onCatalogAdd);
    document.removeEventListener('keydown', this._onHotkey);
    this._registerSeq++;
    if (this._errorTimer) clearTimeout(this._errorTimer);
    if (this._custDebounce) clearTimeout(this._custDebounce);
    if (this._standingTimer) clearTimeout(this._standingTimer);
    if (this._exchangeDebounce) clearTimeout(this._exchangeDebounce);
    if (this._exchangeSearchTimer) clearTimeout(this._exchangeSearchTimer);
    // a counter that is off the page does not ask the server about the sale it
    // was about (CP-17a)
    if (this._lookDebounce) clearTimeout(this._lookDebounce);

    this._cardSeq++;
    if (this._cardTimer) clearTimeout(this._cardTimer);
  }

  updated(changed: Map<string, unknown>) {
    if (changed.has('_custQuery')) {
      this._debounceCustomers();
    }
    // the account's verdict is read at what is owed: when the sale, the added
    // tenders or the customer move, it is read again
    if (this._customer !== null && (changed.has('_sale') || changed.has('_pendingTenders') || changed.has('_customer'))) {
      this._scheduleStanding();
    }
  }

  /* ---- Can this sale go on account? ---- */

  private _standingTimer: ReturnType<typeof setTimeout> | null = null;
  private _standingSeq = 0;

  private _scheduleStanding() {
    if (this._standingTimer) clearTimeout(this._standingTimer);
    if (this._standing === null) {
      void this._syncStanding();
      return;
    }
    this._standingTimer = setTimeout(() => { void this._syncStanding(); }, 150);
  }

  /** What is still to be tendered, the amount a charge to the account would carry. */
  private get _owedForStanding(): number {
    return Math.max((this._sale?.total_cents ?? 0) - this._tenderedSoFar, 0);
  }

  /**
   * Reads the server's verdict for the named customer at what is owed, the
   * sale's own manager approval counted. A failed read leaves the lookup's
   * words in place: the server holds the same rules at the completion.
   */
  private async _syncStanding() {
    const c = this._customer;
    if (c === null) { this._standing = null; return; }
    const seq = ++this._standingSeq;
    try {
      const st = await posService.standing(c.id, this._owedForStanding, this._sale?.status === 'open' ? this._sale.id : undefined);
      if (seq !== this._standingSeq || this._customer?.id !== c.id) return;
      this._standing = st;
    } catch {
      // the lookup's words stand
    }
  }

  /** The actions a blocked verdict offers: pay down, a manager's approval, or another way to pay. */
  private async _takeWay(kind: 'pay_down' | 'manager_override' | 'switch_tender') {
    const c = this._customer;
    if (c === null) return;
    switch (kind) {
      case 'pay_down':
        // taking money on an account is the account's own page; the sale stays open here
        window.open(`/accounts/${c.id}`, '_blank');
        return;
      case 'manager_override':
        return this._askManager('account', this._sale?.total_cents ?? 0);
      default:
        this._chooseMethod('cash');
    }
  }

  /**
   * A manager approves this one sale (an account sale the verdict blocks, or a
   * check over the limit), with a reason and a cap. A manager signed in here
   * with the role approves as themselves (the claims path); anyone else asks
   * the manager to step up with their own counter PIN (CP-04), and the
   * approval the PIN mints is spent by this one sale's override, bound to the
   * kind and the cap the manager saw.
   */
  private async _askManager(kind: 'account' | 'check', capCents: number) {
    const sale = this._sale;
    if (!sale || sale.status !== 'open' || capCents <= 0) return;
    const reason = await promptDialog({
      title: kind === 'account' ? 'A manager approves this sale on account' : 'A manager approves this check',
      label: 'Reason', required: true, confirmLabel: 'Approve',
    });
    if (!reason) return;
    this._error = null;
    const roles = authCustody.roles ?? [];
    const sub = authCustody.subject;
    if (kind === 'account' || kind === 'check') {
      if ((roles.includes('admin') || roles.includes('owner')) && (!sub || sale.cashier_id !== sub)) {
        await this._recordOverride(sale, kind, reason, capCents);
        return;
      }
    }
    this._stepUp = { kind, reason, capCents };
  }

  /** Records the override, with a manager's approval token when the PIN minted one. */
  private async _recordOverride(sale: Sale, kind: 'account' | 'check', reason: string, capCents: number, approvalToken?: string, managerName?: string) {
    try {
      this._error = null;
      const approval = await posService.approveOverride(sale.id, kind, reason, capCents, approvalToken);
      if (kind === 'check') {
        this._checkApprovedCents = approval.cap_cents;
        this._checkOverWords = null;
      }
      await this._syncStanding();
      this._success = managerName ? `Approved by ${managerName}` : 'Approved for this sale';
      if (this._errorTimer) clearTimeout(this._errorTimer);
      this._errorTimer = setTimeout(() => { this._success = null; }, 2500);
    } catch (err: unknown) {
      this._error = apiErrorMessage(err, 'Failed to record the approval');
    }
  }

  /** The PIN dialog's approval completes the override the cashier asked for. */
  private async _onStepUpApproved(detail: { token: string; managerName: string }) {
    const ask = this._stepUp;
    const sale = this._sale;
    this._stepUp = null;
    if (!ask || !sale) return;
    await this._recordOverride(sale, ask.kind, ask.reason, ask.capCents, detail.token, detail.managerName);
  }

  /** The step up dialog, over everything, whatever panel is open. The
   * manager sees the kind and the cap they are approving. */
  private _renderStepUp() {
    if (!this._stepUp || !this._sale) return nothing;
    return html`
      <gable-step-up-dialog act="override" .kind=${this._stepUp.kind} .amountCents=${this._stepUp.capCents}
        target-id=${this._sale.id}
        heading=${this._stepUp.kind === 'account' ? 'A manager approves this sale on account' : 'A manager approves this check'}
        .detail=${this._stepUp.kind === 'account'
          ? `Approve ${fmt(this._stepUp.capCents)} on account for this sale`
          : `Approve a check of ${fmt(this._stepUp.capCents)} for this sale`}
        @step-up-cancel=${() => { this._stepUp = null; }}
        @step-up-approved=${(e: CustomEvent<{ token: string; managerName: string }>) => void this._onStepUpApproved(e.detail)}></gable-step-up-dialog>
    `;
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
      this._standing = null;
      this._acknowledgedAlerts = [];
      this._buyerId = '';
      this._resetTender();
      this._resetLargeCashCapture();
      this._custQuery = '';
      this._custResults = [];
      this._poText = '';
      this._jobId = '';
      this._shipToId = '';
      void this._loadShipTos(c.id);
      // An account customer keeps the panel open for the purchase order and
      // the job; Next: Payment moves on once there is a line.
      this._step = 'customer';
      this._open = 'customer';
      await this._syncStanding();
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

  /** Back to a walk-in sale (the tax follows the account now standing on it). */
  private async _clearCustomer() {
    if (!this._sale || this._sale.status !== 'open') return;
    try {
      this._error = null;
      this._sale = await posService.attachCustomer(this._sale.id, { customer_id: null });
      this._customer = null;
      this._standing = null;
      this._acknowledgedAlerts = [];
      this._buyerId = '';
      this._resetTender();
      this._resetLargeCashCapture();
      // the sale is a walk-in again: the identity the till session holds rides
      // it, so the walk-in's cash sales are counted together (CP-17a)
      this._restoreSessionPayer();
      this._poText = '';
      this._jobId = '';
      this._shipTos = [];
      this._shipToId = '';
      this._step = 'customer';
      this._open = 'customer';
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to clear the customer';
    }
  }

  /** Reads the customer's ship tos for the Deliver to picker (CP-10). */
  private async _loadShipTos(customerId: string) {
    try {
      const page = await CustomerService.listShipTos(customerId);
      this._shipTos = page.items ?? [];
    } catch {
      this._shipTos = [];
    }
  }

  /** Names the delivery: a ship to makes the sale a delivery, Pickup makes it a pickup again. */
  private async _chooseDelivery(shipToId: string) {
    if (!this._sale || !this._customer || this._sale.status !== 'open') return;
    const sent = this._sale;
    this._shipToId = shipToId;
    try {
      this._error = null;
      const answer = await posService.attachCustomer(sent.id, {
        customer_id: this._customer.id,
        delivery_type: shipToId ? 'delivery' : 'pickup',
        ship_to_id: shipToId || null,
      });
      const now = this._sale;
      if (!now || now.id !== answer.id || answer.revision < now.revision) return;
      this._sale = answer;
    } catch (err: unknown) {
      this._error = apiErrorMessage(err, 'Failed to name the delivery');
      this._shipToId = '';
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
        buyer_contact_id: this._buyerId || null,
      });
      // an answer for a sale that has moved on, or an older revision, is dropped
      const now = this._sale;
      if (!now || now.id !== answer.id || answer.revision < now.revision) return;
      this._sale = answer;
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to save the purchase order';
    }
  }

  /** Why Card is not offered, or null when it is: the register has no card terminal (CP-05 A1). */
  private get _cardRefusal(): string | null {
    return cardRefusal(this._register);
  }

  /** Why the charge to the account is not offered, or null when it is. */
  private get _accountRefusal(): string | null {
    // What is left to charge once the cash, check or card tenders are taken.
    const owed = Math.max((this._sale?.total_cents ?? 0) - this._tenderedSoFar, 0);
    const st = this._standing;
    // a manager's approval of this sale waives the credit words, never the purchase order
    const approved = st !== null && st.on_account.reason === 'manager_override';
    const base = chargeRefusal(this._customer, owed, this._poText, approved, this._jobId);
    if (base) return base;
    if (st !== null) {
      // the server's verdict (the same function the completion asks), then the roster
      if (st.on_account.verdict === 'blocked') return st.on_account.message;
      return buyerRefusal(st, this._buyerId, owed);
    }
    return null;
  }

  /** Whether the sale can go on the account, said plainly, and the ways forward when it cannot. */
  private _renderVerdict() {
    const st = this._standing;
    if (st === null) return nothing;
    const line = verdictLine(st);
    const tone = line.tone === 'stop' ? 'text-safety-red' : line.tone === 'warn' ? 'text-amber-400' : 'text-gable-green';
    const ways = waysForward(st);
    return html`
      <div class="mt-2" data-testid="pos-account-verdict" data-verdict=${st.on_account.verdict}>
        <div class="text-xs font-semibold ${tone}" data-testid="pos-verdict-line">${line.text}</div>
        ${ways.length > 0 ? html`
          <div class="mt-2 flex flex-wrap gap-2" role="group" aria-label="Ways forward" data-testid="pos-ways-forward">
            ${ways.map((w) => html`
              <button data-testid="pos-way-${w.kind}" title=${w.message} @click=${() => void this._takeWay(w.kind)}
                ?disabled=${this._sale?.status !== 'open'}
                class="min-h-11 rounded border border-white/20 px-3 py-2 text-xs font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40">${wayLabel(w)}</button>`)}
          </div>` : nothing}
      </div>`;
  }

  /** The buyer a sale on account names, from the account's roster; asked for when the account requires one. */
  private _renderBuyerPick(finished: boolean) {
    const st = this._standing;
    if (st === null || (st.buyers.length === 0 && !st.named_buyer_required)) return nothing;
    const needed = st.named_buyer_required && this._buyerId === '';
    return html`
      <div class="mt-3" data-testid="pos-buyer">
        <label class="block text-xs text-slate-400" for="pos-buyer-pick">
          Buyer${st.named_buyer_required ? html` <span class="text-safety-red">(required for a charge)</span>` : ''}
        </label>
        <div class="mt-1">
          <gable-select id="pos-buyer-pick" data-testid="pos-buyer-pick" .value=${this._buyerId} ?disabled=${finished}
            .options=${[{ value: '', label: st.buyers.length === 0 ? 'No buyers on the roster' : 'Pick a buyer' },
              ...st.buyers.map((b) => ({ value: b.id, label: b.cap_cents !== null ? `${b.name} (up to ${money(b.cap_cents)} a sale)` : b.name }))]}
            @change=${(e: Event) => { this._buyerId = (e.target as HTMLSelectElement).value; void this._saveAccountRefs(); }}
          ></gable-select>
        </div>
        ${needed ? html`<p class="mt-1 text-xs text-amber-400" data-testid="pos-buyer-needed">${st.name} asks for a named buyer on every charge</p>` : nothing}
      </div>`;
  }

  /**
   * The customer's alerts at the counter: a note is a quiet line, a warning is
   * a banner, and a warning that asks to be acknowledged says so until the
   * clerk has read it.
   */
  private _renderAlerts() {
    const alerts = this._standing?.alerts ?? [];
    if (alerts.length === 0) return nothing;
    return html`
      <div class="mt-2 space-y-1" data-testid="pos-alerts">
        ${alerts.map((a) => {
          const acknowledged = this._acknowledgedAlerts.includes(a.id);
          if (a.kind === 'warning') {
            return html`
              <div class="rounded border border-amber-500/30 bg-amber-500/10 px-2 py-1.5 text-xs text-amber-100" data-testid="pos-alert-warning" data-alert-id=${a.id}>
                <span class="font-semibold">Warning:</span> ${a.text}
                ${a.acknowledge_required
                  ? html`<div class="mt-1 ${acknowledged ? 'text-amber-400' : 'text-amber-400/90'}" data-testid="pos-alert-ack">
                      ${acknowledged ? 'Read and acknowledged' : 'The counter must read this before the sale is taken.'}
                    </div>`
                  : nothing}
              </div>`;
          }
          return html`
            <div class="rounded border border-white/10 bg-slate-steel/40 px-2 py-1.5 text-xs text-slate-300" data-testid="pos-alert-note" data-alert-id=${a.id}>
              <span class="font-semibold">Note:</span> ${a.text}
            </div>`;
        })}
      </div>`;
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
      // a hold record the AR core placed holds the account: the standing line
      // says so in its own words, the same blocked treatment the zero limit gets
      const held = holdStanding(this._standing);
      return html`
        <div id="pos-customer" class="rounded-lg bg-deep-space p-3" data-testid="pos-customer">
          <div class="flex items-start justify-between gap-2">
            <div>
              <div class="text-sm font-semibold text-white" data-testid="pos-customer-name">${c.name}</div>
              <div class="font-mono text-xs text-slate-400">${c.account_number}</div>
            </div>
            <button
              @click=${() => this._clearCustomer()}
              ?disabled=${finished || this._loading}
              title=${finished ? 'Start a new sale to change the customer' : 'Back to a walk-in sale'}
              class="rounded border border-white/20 px-3 py-1.5 text-xs font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:border-white/10 disabled:text-slate-400 disabled:opacity-40 disabled:hover:bg-transparent"
            >Walk-in sale</button>
          </div>
          <div class="mt-2 font-mono text-xs font-semibold ${toneClass(held !== null ? held.tone : st.tone)}" data-testid="pos-customer-standing">${held !== null ? held.text : st.text}</div>
          <div class="mt-2">
            <button type="button" data-testid="pos-deposit-on-order" ?disabled=${this._till === null}
              @click=${() => { this._depositOpen = true; }}
              class="rounded border border-white/20 px-3 py-1.5 text-xs font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:border-white/10 disabled:text-slate-400 disabled:opacity-40 disabled:hover:bg-transparent">
              Deposit on an order
            </button>
          </div>
          ${c.standing === 'good' || c.standing === 'over_limit' ? html`
            <div class="mt-1 font-mono text-xs text-slate-400">Open ${money(c.open_receivable_cents)}</div>` : nothing}
          ${this._renderVerdict()}
          ${this._renderAlerts()}
          ${this._renderBuyerPick(finished)}
          <div class="mt-3 grid grid-cols-1 sm:grid-cols-2 items-end gap-2">
            <div class="${c.jobs.length > 0 || c.job_required ? '' : 'sm:col-span-2'}">
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
            ${c.jobs.length > 0 || c.job_required ? html`
              <div>
                <label class="block text-xs text-slate-400" for="pos-job">Job${c.job_required ? html` <span class="text-safety-red">(required for a charge)</span>` : ''}</label>
                <div class="mt-1">
                  <gable-select id="pos-job" .value=${this._jobId} ?disabled=${finished || (c.job_required && c.jobs.length === 0)}
                    .options=${c.jobs.length === 0 ? [{ value: '', label: 'No jobs on file' }] : [{ value: '', label: 'No job' }, ...c.jobs.map((j) => ({ value: j.id, label: j.name }))]}
                    @change=${(e: Event) => { this._jobId = (e.target as HTMLSelectElement).value; void this._saveAccountRefs(); }}
                  ></gable-select>
                </div>
              </div>` : nothing}
          </div>
          ${this._shipTos.length > 0 ? html`
            <div class="mt-3">
              <label class="block text-xs text-slate-400" for="pos-deliver-to">Deliver to</label>
              <div class="mt-1">
                <gable-select id="pos-deliver-to" data-testid="pos-deliver-to" .value=${this._shipToId} ?disabled=${finished}
                  .options=${[
                    { value: '', label: 'Pickup at the counter (tax at the branch rate)' },
                    ...this._shipTos.map((sh) => ({
                      value: sh.id,
                      label: `Deliver to ${sh.name || sh.code} ${sh.city ?? ''} ${sh.postal_code ?? ''} (tax at that zone)`,
                    })),
                  ]}
                  @change=${(e: Event) => { void this._chooseDelivery((e.target as HTMLSelectElement).value); }}
                ></gable-select>
              </div>
            </div>` : nothing}
          ${c.job_required && c.jobs.length === 0 ? html`
            <p class="mt-1 text-xs text-slate-400">This account requires a job and has none on file: add one on the account page.</p>` : nothing}
          ${finished ? nothing : html`
            <div class="mt-3 flex items-center justify-between gap-3">
              <span class="text-xs text-slate-400">${hasLines ? '' : 'Add items to continue'}</span>
              <button data-testid="pos-next-payment" ?disabled=${!hasLines || this._till === null}
                @click=${() => { this._step = 'payment'; this._open = 'payment'; }}
                class="min-h-11 rounded border border-white/20 px-4 py-2 text-sm font-semibold text-white hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent">Next: Payment</button>
            </div>`}
          <gable-pos-order-deposit
            .isOpen=${this._depositOpen}
            .customerId=${c.id}
            .branchId=${this._sale?.branch_id ?? this._till?.branch_id ?? ''}
            .tillSessionId=${this._till?.id ?? ''}
            @close=${() => { this._depositOpen = false; }}
          ></gable-pos-order-deposit>
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
          ?disabled=${this._till === null}
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
      this._standing = null;
      this._acknowledgedAlerts = [];
      this._buyerId = '';
      this._custQuery = '';
      this._custResults = [];
      this._poText = '';
      this._jobId = '';
      this._sealed = null;
      this._signerName = '';
      this._signatureNote = null;
      this._receiptNote = null;
      this._receiptTone = 'plain';
      this._dropReceiptDownload();
      this._dismissCardWait();
      // A completed sale on screen stays offerable for a void or a return
      // until this new sale replaces it (New Sale, a product or a customer).
      const reg = this._registerId;
      const sale = await this._emptyOrNewSale(reg);
      // a late answer for a register no longer selected is dropped
      if (reg !== this._registerId) return;
      this._sale = sale;
      this._tenderAmount = '';
      this._tenderMethod = null;
      this._step = 'customer';
      this._open = 'customer';
      // the identity the till session holds rides the new sale, so the clerk
      // is asked once (CP-17a)
      this._restoreSessionPayer();
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
    if (!await confirmDialog({
      title: `Remove all ${ids.length} line${ids.length === 1 ? '' : 's'} from sale ${sale.number}?`,
      confirmLabel: 'Remove',
      tone: 'danger',
    })) return;
    for (const id of ids) {
      if (this._sale?.id !== sale.id) return;
      await this._removeItem(id);
      if (this._error) return;
    }
    // a tender chosen or added for the cleared lines is not the next lines' tender
    this._resetTender();
  }

  /* ---- Hotkeys (the roster and the chord rules are in ./hotkeys) ---- */

  /** The line a quantity key lands on: the last one touched, else the last on the sale. */
  @state() private _selectedLineId: string | null = null;

  /** True while a dialog of the app is open: no key acts behind it. */
  private get _dialogOpen(): boolean {
    return this._showTillOpen || this._showTillClose || this._showMovement || this._showReturn
      || document.querySelector('[role="dialog"], [aria-modal="true"]') !== null;
  }

  /** What Pay is live on: the order summary's own predicate. */
  private get _canPay(): boolean {
    const sale = this._sale;
    if (!sale || this._till === null || sale.status !== 'open' || this._loading) return false;
    // a quantity change on its way would leave the sale paid at the old quantity
    if (this._changing.size > 0) return false;
    if (!sale.lines.some((l) => l.parent_line_id === null)) return false;
    const plan = payPlan(sale.total_cents, this._pendingTenders, this._entry, this._roundingMode);
    return plan.ok && this._idHoldWords(plan.tenders) === null;
  }

  /** The sale's checks in total: the added check tenders plus the entry. */
  private _checksTotalCents(tenders: TenderIn[]): number {
    return tenders.filter((t) => t.method === 'check').reduce((n, t) => n + t.amount_cents, 0);
  }

  /**
   * Why the dealer's ID rule holds the plan's checks (why, or null when it
   * does not): the rule reads the sale's checks in total, and when it bites,
   * every check tender must record the ID was checked, so a split cannot
   * avoid it. A manager's approval covers the limit, never the ID.
   */
  private _idHoldWords(tenders: TenderIn[]): string | null {
    const total = this._checksTotalCents(tenders);
    for (const t of tenders) {
      if (t.method === 'check' && !t.id_checked) {
        const why = idRequiredWords(this._idRule, total);
        if (why) return why;
      }
    }
    return null;
  }

  /**
   * One ID check covers the sale's checks: when the rule bites on the checks'
   * total and the clerk ticks the ID with its kind on the entry, the checked
   * ID stamps onto every added check that has none, so a split added before
   * the total passed the figure is not trapped with no way to record the ID.
   */
  private _stampEntryIDOntoAddedChecks(entry: TenderIn) {
    if (!entry.id_checked || entry.id_type === undefined) return;
    const total = this._checksTotalCents([...this._pendingTenders, entry]);
    if (!idRequired(this._idRule, total)) return;
    this._pendingTenders = this._pendingTenders.map((t) =>
      t.method === 'check' && !t.id_checked ? { ...t, id_checked: true, id_type: entry.id_type } : t);
  }

  /**
   * An added check's ID state in words, when the rule reaches it: the ID
   * checked once, with its kind, or not checked yet. Other tenders, and checks
   * while the rule does not bite, say nothing.
   */
  private _addedCheckIDWords(t: TenderIn, entry: TenderIn | null, saleTotal: number): string {
    if (t.method !== 'check') return '';
    if (t.id_checked) return `ID checked: ${ID_TYPES.find((o) => o.value === t.id_type)?.label ?? ''}`;
    const planTenders = [...this._pendingTenders, ...(entry !== null && this._tenderedSoFar < saleTotal ? [entry] : [])];
    return this._idHoldWords(planTenders) !== null ? 'ID not checked yet' : '';
  }

  /** The same facts the controls disable on, so a dimmed key means a disabled control. */
  private _hotkeyLive(action: HotkeyAction): boolean {
    const sale = this._sale;
    const open = sale?.status === 'open';
    const hasLines = (sale?.lines ?? []).some((l) => l.parent_line_id === null);
    const till = this._till !== null;
    // the offline counter has none of these keys; the legend dims them as the tender buttons dim
    if (this._offline) return false;
    switch (action) {
      case 'search':
      case 'add':
        return till && open;
      case 'customer':
        // the account is named before or after the lines (CP-10: the tax follows it)
        return till && open && this._customer === null;
      case 'quantity':
        return open && hasLines;
      case 'next':
        return till && open && hasLines && (this._step === 'customer' || this._step === 'payment');
      case 'back':
        return open && (this._step === 'payment' || this._step === 'summary');
      case 'tender-cash':
      case 'tender-check':
        return till && open && hasLines && this._changing.size === 0;
      case 'tender-card':
        return till && open && hasLines && this._changing.size === 0 && this._cardRefusal === null;
      case 'tender-account':
        return till && open && hasLines && this._changing.size === 0 && this._accountRefusal === null;
      case 'pay':
        return this._canPay;
      case 'new-sale':
        return this._register !== null && till && this._changing.size === 0;
      case 'clear-sale':
        return open && hasLines && !this._loading && this._changing.size === 0;
      case 'void':
        return this._completed !== null && sale?.id === this._completed.id;
      default:
        return false;
    }
  }

  private _onHotkey = (e: KeyboardEvent) => {
    if (e.defaultPrevented || this._dialogOpen) return;
    const action = resolveHotkey(chordFromEvent(e));
    if (action === null || !this._hotkeyLive(action)) return;
    e.preventDefault();
    // a typed quantity not yet committed is committed first and the key is
    // not run over the old figure: the cashier presses it again
    const field = document.activeElement;
    if (field instanceof HTMLInputElement && field.dataset.lineId) {
      const item = this._sale?.lines.find((l) => l.id === field.dataset.lineId);
      if (item && field.value.trim() !== (item.quantity ?? '').trim()) {
        void this._changeQuantity(item, field.value, field).then((saved) => {
          if (!saved) return;
          this._success = 'Quantity saved. Press the key again.';
          if (this._errorTimer) clearTimeout(this._errorTimer);
          this._errorTimer = setTimeout(() => { this._success = null; }, 2500);
        });
        return;
      }
    }
    void this._runHotkey(action);
  };

  private async _runHotkey(action: HotkeyAction) {
    // the control may be on screen already; a panel that just opened is waited for
    const focus = async (selector: string) => {
      const here = this.querySelector(selector) as HTMLElement | null;
      if (here) { here.focus(); return; }
      await this.updateComplete;
      (this.querySelector(selector) as HTMLElement | null)?.focus();
    };
    switch (action) {
      case 'search':
        return focus('[data-testid="catalog-search"]');
      case 'add':
        return (this.querySelector('[data-testid="catalog-add"]') as HTMLButtonElement | null)?.click();
      case 'customer':
        this._step = 'customer';
        this._open = 'customer';
        return focus('#pos-customer-search');
      case 'quantity': {
        const lines = (this._sale?.lines ?? []).filter((l) => l.parent_line_id === null && hasQuantityControl(l));
        const target = lines.find((l) => l.id === this._selectedLineId) ?? lines[lines.length - 1];
        if (!target) return;
        const input = this.querySelector(`input[data-line-id="${target.id}"]`) as HTMLInputElement | null;
        input?.focus();
        input?.select();
        return;
      }
      case 'next':
        if (this._step === 'customer') { this._step = 'payment'; this._open = 'payment'; }
        else if (this._step === 'payment') { this._step = 'summary'; this._open = 'summary'; }
        return;
      case 'back':
        if (this._step === 'payment') { this._step = 'customer'; this._open = 'customer'; }
        else if (this._step === 'summary') { this._step = 'payment'; this._open = 'payment'; }
        return;
      case 'tender-cash': return this._chooseMethod('cash');
      case 'tender-check': return this._chooseMethod('check');
      case 'tender-card': return this._chooseMethod('card');
      case 'tender-account': return this._chooseMethod('account');
      case 'pay': return this._pay();
      case 'new-sale': return this._beginFresh();
      case 'clear-sale': return this._clearSale();
      case 'void': return this._voidSale();
      default: {
        // a new action in the roster must be given a handler here
        const unhandled: never = action;
        return unhandled;
      }
    }
  }

  /** The legend pinned above the counter: every key at every state, the unavailable ones dimmed. */
  private _renderHotkeyLegend() {
    return html`
      <div role="group" aria-label="Keyboard shortcuts" data-testid="pos-hotkey-legend"
        class="hidden min-[900px]:flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-white/10 bg-slate-steel px-4 py-1.5 text-xs text-slate-300">
        ${legendKeys((a) => this._hotkeyLive(a)).map((k) => html`
          <span data-action=${k.action} class="inline-flex items-center gap-1 whitespace-nowrap ${k.live ? '' : 'opacity-40'}">
            <kbd class="rounded border border-white/20 px-1.5 font-mono text-slate-200">${k.keycap}</kbd>${k.label}
          </span>`)}
      </div>`;
  }

  /** The lines whose quantity change is on its way, so one commit is sent once. */
  private _changing = new Set<string>();

  /**
   * Sets a line's quantity on the sale's revision. A change that cannot be
   * made says why and puts the number back.
   */
  private async _changeQuantity(item: SaleLine, text: string, input: HTMLInputElement | null): Promise<boolean> {
    const sale = this._sale;
    const restore = () => { if (input) input.value = item.quantity ?? ''; };
    if (!sale || sale.status !== 'open' || this._changing.has(item.id)) return false;
    const qty = normalizeQuantity(text);
    if (qty === null) {
      this._error = 'Enter a quantity above zero';
      restore();
      return false;
    }
    if (qty.includes('.') && isCountedUnit(item.uom ?? '')) {
      this._error = `${item.uom} is counted in whole units`;
      restore();
      return false;
    }
    if (Number(qty) === Number(item.quantity)) {
      restore();
      return false;
    }
    this._changing.add(item.id);
    this.requestUpdate();
    try {
      this._error = null;
      this._sale = await posService.changeQuantity(sale.id, item.id, qty, sale.revision);
      if (input) input.value = this._sale.lines.find((l) => l.id === item.id)?.quantity ?? qty;
      return true;
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to change the quantity';
      restore();
      return false;
    } finally {
      this._changing.delete(item.id);
      this.requestUpdate();
    }
  }

  /** The line's quantity one step either way, as the number a cashier would type. */
  private _stepQuantity(item: SaleLine, by: 1 | -1) {
    this._selectedLineId = item.id;
    const next = Math.round((Number(item.quantity) + by) * 10000) / 10000;
    if (!(next > 0)) return;
    void this._changeQuantity(item, String(next), null);
  }

  private _renderQuantityControl(item: SaleLine) {
    const busy = this._changing.has(item.id);
    const button = 'inline-flex h-11 w-11 shrink-0 items-center justify-center rounded border border-white/20 text-lg text-white hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-gable-green disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent';
    return html`
      <div class="flex shrink-0 items-center gap-1" data-testid="pos-line-quantity">
        <button type="button" aria-label="Decrease quantity of ${item.description}"
          ?disabled=${busy || !(Number(item.quantity) > 1)}
          @click=${() => this._stepQuantity(item, -1)} class=${button}>−</button>
        <input type="text" inputmode="decimal" autocomplete="off" aria-label="Quantity of ${item.description}"
          .value=${item.quantity} ?disabled=${busy} data-line-id=${item.id}
          @focus=${() => { this._selectedLineId = item.id; }}
          @keydown=${(e: KeyboardEvent) => {
            if (e.key === 'Enter') { e.preventDefault(); void this._changeQuantity(item, (e.target as HTMLInputElement).value, e.target as HTMLInputElement); }
          }}
          @change=${(e: Event) => void this._changeQuantity(item, (e.target as HTMLInputElement).value, e.target as HTMLInputElement)}
          class="h-11 w-16 rounded border border-white/10 bg-deep-space px-1 text-center font-mono text-sm text-white outline-none focus:border-gable-green disabled:opacity-60" />
        <button type="button" aria-label="Increase quantity of ${item.description}" ?disabled=${busy}
          @click=${() => this._stepQuantity(item, 1)} class=${button}>+</button>
      </div>`;
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
    const total = this._sale?.total_cents ?? 0;
    const pendingCash = this._pendingTenders
      .filter((t) => t.method === 'cash')
      .reduce((s, t) => s + t.amount_cents, 0);
    if (pendingCash > 0) {
      const otherLegs = this._pendingTenders
        .filter((t) => t.method !== 'cash')
        .reduce((s, t) => s + t.amount_cents, 0);
      const cashNeeded = roundCashFor(this._roundingMode, Math.max(total - otherLegs, 0)).rounded;
      if (pendingCash >= cashNeeded) {
        return 0;
      }
    }
    return Math.max(total - this._tenderedSoFar, 0);
  }

  /**
   * The branch's cash rounding rule (CP-18): the drawer keeps the rounded
   * cash, and the change falls out of the rounded figure. The rule off
   * changes nothing.
   */
  private get _roundingMode(): CashRoundingMode | null {
    const regBranchId = this._register?.branch_id;
    if (regBranchId) {
      const regBranch = branchContext.branches.find((b) => b.id === regBranchId);
      if (regBranch?.cash_rounding_mode) return regBranch.cash_rounding_mode;
    }
    return branchContext.current?.cash_rounding_mode ?? null;
  }

  /** The cash leg, before the branch's rule moves it. */
  private get _cashLegCents(): number {
    const total = this._sale?.total_cents ?? 0;
    const otherLegs = this._pendingTenders
      .filter((t) => t.method !== 'cash')
      .reduce((s, t) => s + t.amount_cents, 0);
    if (this._tenderMethod !== null && this._tenderMethod !== 'cash') {
      // The cashier is typing a card, a cheque or an account: whatever that
      // tender does not cover is still a cash leg. A card sale that covers
      // the total has no cash leg at all, and no rounding.
      const typed = dollarsToCents(this._tenderAmount.trim());
      const covered = Number.isFinite(typed) && typed > 0 ? typed : this._owedCents;
      return Math.max(total - otherLegs - covered, 0);
    }
    return Math.max(total - otherLegs, 0);
  }

  /** The cash the drawer keeps: the cash leg, rounded by the branch's rule. */
  private get _cashDueCents(): number {
    return roundCashFor(this._roundingMode, this._cashLegCents).rounded;
  }

  /** What the branch's rounding moves on this sale, in cents. */
  private get _roundingDiffCents(): number {
    return roundCashFor(this._roundingMode, this._cashLegCents).diff;
  }

  /**
   * The tender being entered: the chosen method and the typed amount, which
   * is what is still owed while the field is empty. Null when no method is
   * chosen or the amount does not parse to a positive number.
   */
  private get _entry(): TenderIn | null {
    if (this._tenderMethod === null) return null;
    const text = this._tenderAmount.trim();
    // store credit defaults to what the account holds, up to what is owed
    // Cash defaults to what the drawer keeps, which the branch's rounding
    // rule may move (CP-18); the other methods default to what is owed.
    const pendingCash = this._pendingTenders
      .filter((t) => t.method === 'cash')
      .reduce((s, t) => s + t.amount_cents, 0);
    const fallback = this._tenderMethod === 'store_credit'
      ? storeCreditOffer(this._standing, this._owedCents)
      : this._tenderMethod === 'cash'
        ? Math.max(this._cashDueCents - pendingCash, 0)
        : this._owedCents;
    const cents = text === '' ? fallback : dollarsToCents(text);
    if (isNaN(cents) || cents <= 0) return null;
    const entry: TenderIn = { method: this._tenderMethod, amount_cents: cents };
    // the check number rides as the reference; the ID checked and its kind (never a number) on a check or a charge
    if (this._tenderMethod === 'check' && this._checkRef.trim() !== '') entry.reference = this._checkRef.trim();
    // an ordinary check is the server's default instrument, so only a named kind rides the tender
    if (this._tenderMethod === 'check' && this._checkInstrument !== 'personal_check') entry.instrument = this._checkInstrument;
    if ((this._tenderMethod === 'check' || this._tenderMethod === 'account') && this._idChecked && this._idType !== '') {
      entry.id_checked = true;
      entry.id_type = this._idType;
    }
    return entry;
  }

  /** Why the entered tender cannot go yet (an ID without its kind, a check over the limit, more store credit than held), or null. */
  private _entryRefusal(entry: TenderIn | null): string | null {
    if (entry === null) return null;
    if (entry.method === 'check' || entry.method === 'account') {
      const why = idRefusal(this._idChecked, this._idType);
      if (why) return why;
    }
    if (entry.method === 'check') {
      const overLimit = checkRefusal(this._standing, this._dealerCheckLimitCents, entry.amount_cents);
      const approved = this._checkApprovedCents !== null && entry.amount_cents <= this._checkApprovedCents;
      if (overLimit && !approved) {
        // said beside Ask a manager as well as in the band
        this._checkOverWords = overLimit;
        return overLimit;
      }
      // the dealer's ID rule holds the check until the ID is checked; it
      // reads the sale's checks in total (the added ones plus this entry), so
      // a split cannot slip under the figure; a manager's check approval
      // covers the limit, never the ID
      if (!entry.id_checked) {
        const why = idRequiredWords(this._idRule, this._checksTotalCents([...this._pendingTenders, entry]));
        if (why) return why;
      }
    }
    if (entry.method === 'card') {
      const over = cardOverWords(entry.amount_cents, this._owedCents, fmt(this._owedCents));
      if (over) return over;
    }
    if (entry.method === 'store_credit') {
      const held = this._standing?.open_credit_memo_cents ?? 0;
      if (entry.amount_cents > held) return `The account holds ${money(held)} of store credit`;
    }
    return null;
  }

  /** Nothing chosen and nothing added: the next tender is chosen again. */
  private _resetTender() {
    this._tenderMethod = null;
    this._tenderAmount = '';
    this._pendingTenders = [];
    this._checkRef = '';
    this._idChecked = false;
    this._idType = '';
    this._checkInstrument = 'personal_check';
    this._checkOverWords = null;
    // the branch's plain words went with the tenders they were about
    this._walkInNote = '';
  }

  private _chooseMethod(method: TenderMethod) {
    if (this._till === null) return;
    if (method === 'account') {
      const refusal = this._accountRefusal;
      if (refusal) {
        this._error = refusal;
        return;
      }
    }
    if (method === 'card') {
      const refusal = this._cardRefusal;
      if (refusal) {
        this._error = refusal;
        return;
      }
    }
    if (method === 'store_credit' && storeCreditOffer(this._standing, this._owedCents) <= 0) return;
    this._tenderMethod = method;
    this._tenderAmount = '';
    // the counter's look at what this tender means for the branch's rule
    this._scheduleLargeCashLook();
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
    if (this._customer !== null && this._standing === null && (entry.method === 'check' || entry.method === 'store_credit')) {
      void this._syncStanding().then(() => {
        if (this._standing !== null) this._addPendingTender();
      });
      return;
    }
    const refusal = this._entryRefusal(entry);
    if (refusal) {
      this._error = refusal;
      return;
    }
    this._stampEntryIDOntoAddedChecks(entry);
    this._pendingTenders = [...this._pendingTenders, entry];
    this._tenderAmount = '';
    this._checkRef = '';
    this._idChecked = false;
    this._idType = '';
    this._checkInstrument = 'personal_check';
    // The next tender is chosen again, never assumed.
    this._tenderMethod = null;
    // the branch's plain words are about the tenders entered so far
    this._scheduleLargeCashLook();
  }

  /** Pay: the added tenders plus the entry when they do not cover the total. */
  private async _pay() {
    if (!this._sale) return;
    // The money guard at the yard: money never queues offline (the field
    // shell's own words). Offline selling is the work shell's alone, never
    // the field shell's (ADR 0011's first ruling), so Pay is refused here
    // before anything dials.
    if (!this._online) {
      // the work shell's core sells offline for an enrolled register; a
      // browser tab, or a register not enrolled, keeps the refusal
      if (offlineCounter.available()) {
        void this._goOffline().then((entered) => {
          if (!entered) this._error = YARD_OFFLINE_REFUSAL;
        });
        return;
      }
      this._error = YARD_OFFLINE_REFUSAL;
      return;
    }
    // a quantity change on its way would leave the sale paid at the old quantity
    if (this._changing.size > 0) return;
    // the entry is read once: the words below and the plan are about the same tender
    const entry = this._entry;
    // a tender still being entered is held to the same words as Add, unless the added ones already cover the sale
    if (entry !== null && this._tenderedSoFar < this._sale.total_cents) {
      const refusal = this._entryRefusal(entry);
      if (refusal) {
        this._error = refusal;
        return;
      }
    }
    if (entry !== null) this._stampEntryIDOntoAddedChecks(entry);
    const plan = payPlan(this._sale.total_cents, this._pendingTenders, entry, this._roundingMode);
    if (!plan.ok) {
      this._error = plan.owedCents > 0 ? `Still owed ${fmt(plan.owedCents)}` : 'Nothing to pay';
      return;
    }
    // the ID rule holds added checks the same way it holds the entry
    const idHold = this._idHoldWords(plan.tenders);
    if (idHold) {
      this._error = idHold;
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
    // The branch country's large cash rule (CP-17a): the server classifies
    // the plan's tenders against the sale it holds. A crossing asks for the
    // payer's details before Pay; a walk-in's cash sale over the branch's
    // identity floor asks who paid so their sales are counted together. The
    // identity the counter already holds rides the preview, so the preview
    // and the completion key the same person.
    try {
      const preview = await posService.largeCashPreview(
        this._sale.id, plan.tenders, this._suspectAvoidance, this._payer ?? undefined);
      this._walkInNote = preview.walk_in_note ?? '';
      // a recorded decline is the answer to the floor's ask: the sale goes
      // ahead and the counter is not asked again
      if (!this._payerDeclined && preview.asks_details && this._payer === null) {
        this._payerAsk = {
          fields: preview.fields,
          dealerThreshold: preview.dealer_threshold,
          // the floor is the only question when the rule's threshold is not
          // crossed: a crossing asks for the payer's details and no decline
          // answers it, so the counter says the crossing's words.
          floor: preview.asks_floor && !preview.threshold_crossed,
          thresholdCrossed: preview.threshold_crossed,
        };
        this._payerRefused = false;
        return;
      }
    } catch {
      // the preview is advisory: the completion's own capture is the authority
    }
    if (!(await this._acknowledgeCounterWarnings(this._sale?.customer_id))) return;
    await this._completeSale(plan.tenders);
  }

  /**
   * A warning that asks to be acknowledged is read at the counter first: the
   * clerk says so, and the acknowledgement goes on the sale with it. Every
   * such warning is read before the sale or the exchange is taken.
   */
  private async _acknowledgeCounterWarnings(customerId?: string | null): Promise<boolean> {
    if (customerId && (!this._standing || this._standing.customer_id !== customerId)) {
      try {
        this._standing = await posService.standing(customerId, 0);
      } catch {
        // if the standing read fails here, the server's own completion check still guards the sale
      }
    }
    for (const pending of this._standing?.alerts ?? []) {
      if (!pending.acknowledge_required || this._acknowledgedAlerts.includes(pending.id)) continue;
      const read = await confirmDialog({
        title: 'This customer has a warning',
        body: pending.text,
        confirmLabel: 'I have read this',
        cancelLabel: 'Not now',
      });
      if (!read) return false;
      this._acknowledgedAlerts = [...this._acknowledgedAlerts, pending.id];
    }
    return true;
  }

  /**
   * Sends the tenders it is given and clears the added list only when the sale
   * completes, so a refused Pay leaves no stale tender behind.
   */
  private async _completeSale(tenders: TenderIn[]) {
    if (!this._sale || tenders.length === 0) return;
    try {
      this._loading = true;
      this._completing = true;
      this._error = null;
      // A timer set earlier must not clear this completion's band.
      if (this._errorTimer) clearTimeout(this._errorTimer);
      // the capture rides along only when the counter holds one, and a
      // recorded decline rides with it (CP-17a)
      const extras = this._payer || this._suspectAvoidance || this._payerDeclined
        ? {
          payer: this._payer ?? undefined,
          suspected_avoidance: this._suspectAvoidance,
          payer_declined: this._payerDeclined,
        }
        : undefined;
      const completed = extras
        ? await posService.completeSale(this._sale.id, this._sale.revision, tenders, this._acknowledgedAlerts, extras)
        : await posService.completeSale(this._sale.id, this._sale.revision, tenders, this._acknowledgedAlerts);
      // the sale completed on the card: the waiting card has nothing left to say
      this._dismissCardWait();
      // The identified payer stays on the till session for the rest of it, so
      // the clerk is asked once (CP-17a).
      this._rememberSessionPayer();
      // the payer capture and the avoidance mark went with the sale that needed them
      this._resetLargeCashCapture();
      this._suspectAvoidance = false;
      this._sale = completed;
      this._completed = completed;
      // The Sale complete card says it (number, total, tenders, change): no band over the page.
      this._success = null;
      this._pendingTenders = [];
      this._tenderAmount = '';
      this._tenderMethod = null;
      this._step = 'summary';
      this._open = null;
      if (tenders.some((t) => t.method === 'account')) void this._refreshStanding();

      // The completed sale, its band and the change due stay until the cashier
      // moves on: New Sale, a product added or a customer named.
    } catch (err: unknown) {
      // The completion was sent and its answer lost (ADR 0011 5.3): the
      // server may have committed it, so the cart is result unknown. It keeps
      // its tender on screen, is never converted, and holds the till close
      // until it is read online.
      if (!(err instanceof ApiError) && isLostAnswer(err, navigator.onLine) && this._sale) {
        this._setUnknownCart(this._sale.id);
        this._error = RESULT_UNKNOWN;
        if (!navigator.onLine) void this._goOffline();
        return;
      }
      // a check over the limit the screen could not know of (a walk-in's, or the database wide limit) says so beside Ask a manager
      if (err instanceof ApiError && err.hasBlocker('check_over_limit')) this._checkOverWords = err.message;
      // A large cash crossing without the payer's details, or a walk-in's cash
      // sale over the branch's identity floor with no answer (CP-17a): the
      // capture is asked again in the dialog, in words, and the sale stays
      // open with its tenders still added.
      if (err instanceof ApiError && (err.hasBlocker('payer_details_required') || err.hasBlocker('walk_in_identity_required'))) {
        this._error = null;
        this._payerRefused = true;
        if (this._payerAsk === null) {
          const floor = err.hasBlocker('walk_in_identity_required');
          this._payerAsk = {
            fields: ['name', 'address', 'taxpayer_id', 'date_of_birth', 'occupation', 'id_type', 'id_number', 'third_party_payer'],
            dealerThreshold: false,
            floor,
            thresholdCrossed: !floor,
          };
        }
        return;
      }
      // a warning the office added after the standing on screen was read: the
      // server is the authority, so read the standing again and let the next
      // Pay offer the read dialog instead of looping on the same refusal
      if (err instanceof ApiError && err.hasBlocker('alert_unacknowledged')) void this._syncStanding();
      const words = err instanceof Error ? err.message : 'Failed to complete sale';
      // The terminal's own outcomes are said by the waiting card, in words,
      // not by the error band: a card the device still holds waits here, and
      // a decline is the terminal's own no with the sale still open.
      const waiting = intentIdOfRefusal(words);
      if (waiting !== null) {
        this._error = null;
        void this._watchCard(waiting);
        return;
      }
      if (/card was declined/i.test(words)) {
        this._error = CARD_DECLINED_WORDS;
        return;
      }
      this._error = words;
    } finally {
      this._loading = false;
      this._completing = false;
    }
  }

  /**
   * Forgets the large cash capture (CP-17a): the payer, the ask the preview
   * made and the check's instrument kind belong to the sale they were taken
   * for, so a finished, voided or new sale starts without them. The identity
   * the till session holds is not the sale's: it stays for the rest of the
   * session.
   */
  private _resetLargeCashCapture() {
    this._payer = null;
    this._payerAsk = null;
    this._payerRefused = false;
    this._payerDeclined = false;
    this._walkInNote = '';
    this._checkInstrument = 'personal_check';
  }

  /**
   * Asks the server what the branch's rule makes of the tenders entered so far,
   * so the counter's plain words are on the screen before Pay (CP-17a). The
   * look is advisory: the ask itself waits for the final plan at Pay, and the
   * completion's own capture is the authority.
   */
  private _scheduleLargeCashLook() {
    if (this._lookDebounce) clearTimeout(this._lookDebounce);
    this._lookDebounce = setTimeout(() => { void this._lookAtLargeCash(); }, 300);
  }

  private async _lookAtLargeCash() {
    if (!this._sale) return;
    const entry = this._entry;
    const tenders = [...this._pendingTenders];
    if (entry !== null) tenders.push(entry);
    if (tenders.length === 0) return;
    try {
      const preview = await posService.largeCashPreview(
        this._sale.id, tenders, this._suspectAvoidance, this._payer ?? undefined);
      this._walkInNote = preview.walk_in_note ?? '';
    } catch {
      // the look is advisory: the completion's own capture is the authority
    }
  }

  /**
   * Keeps the identified payer on the till session for the rest of it, so the
   * clerk is asked once (CP-17a). A till session that is not the one the
   * identity was captured on does not hold it.
   */
  private _rememberSessionPayer() {
    const tillId = this._till?.id ?? null;
    if (this._payer !== null && tillId !== null) {
      this._sessionPayer = this._payer;
      this._sessionTillId = tillId;
    }
  }

  /**
   * Puts the identity the till session holds on the sale, so a walk-in's cash
   * sale over the branch's floor is counted together with the one before it
   * without the clerk being asked again (CP-17a). A named customer is the
   * payer's own key, so the identity does not ride that sale.
   */
  private _restoreSessionPayer() {
    const tillId = this._till?.id ?? null;
    if (this._sessionPayer === null || this._sessionTillId === null || this._sessionTillId !== tillId) return;
    if (this._sale?.customer_id) return;
    this._payer = this._sessionPayer;
  }

  /** Reads the card leg's own record until the terminal answers or the waiting is dismissed. */
  private async _watchCard(id: string) {
    const seq = ++this._cardSeq;
    this._cardWaitId = id;
    this._cardIntent = null;
    this._cardNote = null;
    try {
      const intent = await posService.getTerminalIntent(id);
      if (seq !== this._cardSeq) return;
      this._cardIntent = intent;
    } catch (err: unknown) {
      if (seq !== this._cardSeq) return;
      this._cardNote = err instanceof Error ? err.message : 'Failed to read the card at the terminal';
      return;
    }
    if (this._cardIntent === null || !['claimed', 'pending_at_device'].includes(this._cardIntent.state)) {
      this._cardWaitId = null;
      return;
    }
    this._cardTimer = setTimeout(() => { void this._watchCard(id); }, 2000);
  }

  /** Drops the waiting card: the terminal may still answer, and the manager's queue holds the leg either way. */
  private _dismissCardWait() {
    this._cardSeq++;
    if (this._cardTimer) clearTimeout(this._cardTimer);
    this._cardWaitId = null;
    this._cardIntent = null;
    this._cardNote = null;
  }

  /** The desk's cancel, honest about the device: the server's refusal is said beside the card. */
  private async _cancelCard() {
    const intent = this._cardIntent;
    if (!intent || this._cardBusy) return;
    try {
      this._cardBusy = true;
      const fresh = await posService.cancelTerminalIntent(intent.id);
      this._cardIntent = fresh;
      if (fresh.state === 'lost') this._cardNote = 'The waiting stopped here. The terminal was never reached, so no money moved.';
    } catch (err: unknown) {
      this._cardNote = err instanceof Error ? err.message : 'Failed to cancel the card at the terminal';
    } finally {
      this._cardBusy = false;
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
    const reason = await promptDialog({ title: `Void sale ${sale.number}?`, label: 'Reason', required: true, confirmLabel: 'Void sale', tone: 'danger' });
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

  private _beginReturn(sale?: Sale) {
    const target = sale ?? this._completed ?? this._sale;
    if (!target || target.status !== 'completed') {
      this._error = 'Return needs a completed sale';
      return;
    }
    const productLines = target.lines.filter(l => l.line_type === 'product' && l.parent_line_id === null);
    const allReturned = productLines.length > 0 && productLines.every((l) => {
      const returned = Number(target.returned_quantities?.[l.id] ?? '0');
      return returned >= Number(l.quantity ?? '0');
    });
    if (allReturned) {
      this._error = 'Everything on this sale has already been returned';
      return;
    }
    this._completed = target;
    const qty: Record<string, string> = {};
    const restock: Record<string, boolean> = {};
    productLines.forEach((l) => {
      qty[l.id] = '0';
      // A tinted can does not go back into saleable stock (row CP-16): it is
      // re-tinted and put on the marked down rack. The server refuses the
      // restock of a tinted line, so the screen asks for the same thing.
      restock[l.id] = !l.tint;
    });
    this._returnQty = qty;
    this._returnRestockByLine = restock;
    this._returnReasonByLine = {};
    this._returnReason = '';
    this._exchangeOn = false;
    this._exchangeLines = [];
    this._exchangeFigures = null;
    this._returnFigures = null;
    this._exchangeError = null;
    this._exchangeQuery = '';
    this._exchangeResults = [];
    // the payer capture and the manager's mark belong to the exchange they
    // were taken for
    this._resetLargeCashCapture();
    this._payerForExchange = false;
    this._suspectAvoidance = false;
    this._showReturn = true;
    this._showReturnSearch = false;
    void this._loadReasons().then(() => { if (this._showReturn) this._scheduleDialogFigures(); });
  }

  /**
   * The closed list of reasons with each fee, read when the dialog opens: a
   * fee the dealer set since the last read shows in the reason's own words.
   */
  private async _loadReasons() {
    try {
      this._reasons = (await posService.returnReasons()) ?? [];
    } catch {
      // A failed read keeps the list the page had: the reason dropdown is
      // worth more than a fresh figure in its label, and the fee itself is
      // the server's, stated in the figures.
    }
  }

  /** Opens the search for a past sale to return. */
  private _openReturnSearch() {
    this._showReturnSearch = true;
    this._returnSearchQuery = '';
    this._returnSearchDate = '';
    this._returnSearchResults = [];
    this._returnSearchError = null;
  }

  /** Searches completed sales by number, invoice number, customer name, or date. */
  private async _searchReturns() {
    const q = this._returnSearchQuery.trim();
    const date = this._returnSearchDate.trim();
    if (q.length < 2 && !date) {
      this._returnSearchError = 'Type at least two characters or pick a date to search';
      return;
    }
    try {
      this._returnSearchBusy = true;
      this._returnSearchError = null;
      const page = await posService.searchSales(q, date || undefined);
      this._returnSearchResults = page.items;
    } catch (err: unknown) {
      this._returnSearchError = err instanceof Error ? err.message : 'Failed to search the sales';
    } finally {
      this._returnSearchBusy = false;
    }
  }

  /** Loads the picked sale and opens the return dialog on it. */
  private async _pickReturnSearchResult(summary: SaleSummary) {
    if (summary.status === 'voided') return;
    try {
      this._returnSearchBusy = true;
      this._returnSearchError = null;
      const full = await posService.getSale(summary.id);
      const productLines = full.lines.filter(l => l.line_type === 'product' && l.parent_line_id === null);
      const allReturned = productLines.length > 0 && productLines.every((l) => {
        const returned = Number(full.returned_quantities?.[l.id] ?? '0');
        return returned >= Number(l.quantity ?? '0');
      });
      if (allReturned) {
        this._returnSearchError = 'Everything on this sale has already been returned';
        return;
      }
      this._beginReturn(full);
    } catch (err: unknown) {
      this._returnSearchError = err instanceof Error ? err.message : 'Failed to read the sale';
    } finally {
      this._returnSearchBusy = false;
    }
  }

  /** How many of a line are still returnable, as a decimal string. */
  private _returnableQty(line: SaleLine): string {
    const total = Number(line.quantity ?? '0');
    const returned = Number(this._completed?.returned_quantities?.[line.id] ?? '0');
    return String(Math.max(0, total - returned));
  }

  /** The lines coming back, as the request names them: reason by line, restocked or not. */
  private _returnedLines() {
    const sale = this._completed;
    if (!sale) return [];
    return Object.entries(this._returnQty)
      .filter(([, q]) => Number(q) > 0)
      .map(([line_id, q]) => {
        const code = this._reasons.length > 0 ? this._returnReasonByLine[line_id] : undefined;
        return { line_id, quantity: q, restock: this._returnRestockByLine[line_id] ?? true, ...(code ? { reason_code: code } : {}) };
      });
  }

  private _returnProblem(): string | null {
    const sale = this._completed;
    if (!sale) return 'Return needs a completed sale';
    for (const l of sale.lines) {
      if (!Object.prototype.hasOwnProperty.call(this._returnQty, l.id)) continue;
      const max = Number(this._returnableQty(l));
      const asked = Number(this._returnQty[l.id] ?? '0');
      if (asked > max) return `${l.description}: only ${max} can be returned`;
    }
    const rows = Object.keys(this._returnQty).map((id) => ({
      quantity: this._returnQty[id] ?? '0',
      reason: this._reasons.length > 0 ? (this._returnReasonByLine[id] ?? '') : 'other',
    }));
    return returnRefusal(rows, this._returnReason, this._reasons.length > 0);
  }

  /** The body of a plain return, the same one the preview states and the confirm posts. */
  private _returnBody() {
    const sale = this._completed;
    if (!sale) return null;
    const lines = this._returnedLines();
    if (lines.length === 0) return null;
    return {
      register_id: this._registerId,
      original_sale_id: sale.id,
      customer_id: sale.customer_id ?? undefined,
      refund_method: this._returnMethod,
      reason: this._returnReason.trim() || undefined,
      lines,
    };
  }

  private async _confirmReturn() {
    const problem = this._returnProblem();
    if (problem) {
      this._error = problem;
      return;
    }
    const body = this._returnBody();
    if (body === null) return;
    try {
      this._loading = true;
      const ret = await posService.createReturn(body);
      this._showReturn = false;
      const fee = ret.restocking_fee_cents > 0 ? `, less a ${fmt(ret.restocking_fee_cents)} restocking fee` : '';
      this._success = `Return ${ret.number} complete - ${fmt(Math.abs(ret.total_cents))} refunded ${ret.refund_method}${fee}`;
      this._errorTimer = setTimeout(() => { this._success = null; }, 4000);
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to take the return';
    } finally {
      this._loading = false;
    }
  }

  /**
   * States the return's figures before it is taken: the fee each line's
   * reason withholds and the money back. Always the server's own preview,
   * never the reason list this page keeps: a fee set after the list was read
   * is charged by the return, so it is stated here too.
   */
  private async _previewReturn(asked: number) {
    if (asked !== this._returnAsked) return;
    const body = this._returnBody();
    if (body === null) {
      this._returnFigures = null;
      this._returnBusy = false;
      return;
    }
    try {
      const figures = await posService.previewReturn(body);
      if (asked !== this._returnAsked) return;
      this._returnFigures = figures ?? null;
    } catch {
      if (asked !== this._returnAsked) return;
      this._returnFigures = null;
    } finally {
      if (asked === this._returnAsked) this._returnBusy = false;
    }
  }

  /* ---- The exchange: goods back and goods out in one act ---- */

  private _exchangeBody(withTender: boolean) {
    const sale = this._completed;
    if (!sale || this._exchangeLines.length === 0) return null;
    const returned = this._returnedLines();
    if (returned.length === 0) return null;
    const f = this._exchangeFigures;
    const tenders = withTender && f !== null && f.difference_cents > 0
      ? [{ method: this._exchangeTender, amount_cents: f.difference_cents }] : undefined;
    const acknowledged = withTender && this._acknowledgedAlerts.length > 0
      ? this._acknowledgedAlerts : undefined;
    // the payer's capture and the manager's avoidance mark ride the exchange
    // that needs them, the way they ride a completion (CP-17a)
    const payer = withTender ? this._payer ?? undefined : undefined;
    const suspectedAvoidance = withTender && this._suspectAvoidance;
    const payerDeclined = withTender && this._payerDeclined;
    return {
      register_id: this._registerId,
      original_sale_id: sale.id,
      customer_id: sale.customer_id ?? undefined,
      refund_method: this._exchangeRefund,
      reason: this._returnReason.trim() || undefined,
      returned_lines: returned,
      sale_lines: this._exchangeLines.map((l) => ({ product_id: l.product.product_id, quantity: l.qty })),
      // the preview reads the tender method to say what the branch's large cash
      // rule needs of this exchange before it is rung
      tender_method: this._exchangeTender,
      ...(tenders ? { tenders } : {}),
      ...(acknowledged ? { acknowledged_alert_ids: acknowledged } : {}),
      ...(payer ? { payer } : {}),
      ...(suspectedAvoidance ? { suspected_avoidance: true } : {}),
      ...(payerDeclined ? { payer_declined: true } : {}),
    };
  }

  /**
   * The figures are the server's (the preview states them, it writes nothing):
   * the screen prices nothing. The exchange quotes its figures; the plain
   * return states its restocking fee.
   */
  private _scheduleDialogFigures() {
    if (this._exchangeDebounce) clearTimeout(this._exchangeDebounce);
    this._exchangeFigures = null;
    this._exchangeError = null;
    this._returnFigures = null;
    // The figures are pending from here: the debounce, and the round trip
    // after it. The return waits for the statement.
    this._returnBusy = !this._exchangeOn;
    const asked = this._returnAsked + 1;
    this._returnAsked = asked;
    this._exchangeDebounce = setTimeout(() => {
      if (this._exchangeOn) void this._previewExchange();
      else void this._previewReturn(asked);
    }, 300);
  }

  private async _previewExchange() {
    const body = this._exchangeBody(false);
    if (body === null) return;
    try {
      this._exchangeBusy = true;
      const figures = await posService.previewExchange(body);
      this._exchangeFigures = figures;
      this._exchangeError = null;
    } catch (err: unknown) {
      this._exchangeFigures = null;
      this._exchangeError = apiErrorMessage(err, 'Failed to price the exchange');
    } finally {
      this._exchangeBusy = false;
    }
  }

  private _debounceExchangeSearch() {
    if (this._exchangeSearchTimer) clearTimeout(this._exchangeSearchTimer);
    if (this._exchangeQuery.trim().length < 2) {
      this._exchangeResults = [];
      return;
    }
    this._exchangeSearchTimer = setTimeout(async () => {
      try {
        this._exchangeResults = (await posService.searchProducts(this._exchangeQuery.trim())) ?? [];
      } catch {
        this._exchangeResults = [];
      }
    }, 200);
  }
  private _exchangeSearchTimer: ReturnType<typeof setTimeout> | null = null;

  private _addExchangeLine(product: QuickSearchResult) {
    const at = this._exchangeLines.findIndex((l) => l.product.product_id === product.product_id);
    this._exchangeLines = at >= 0
      ? this._exchangeLines.map((l, i) => (i === at ? { ...l, qty: String(Number(l.qty) + 1) } : l))
      : [...this._exchangeLines, { product, qty: '1' }];
    this._exchangeQuery = '';
    this._exchangeResults = [];
    this._scheduleDialogFigures();
  }

  private async _confirmExchange() {
    const sale = this._completed;
    if (!sale) return;
    const problem = this._returnProblem();
    if (problem) {
      this._error = problem;
      return;
    }
    if (this._exchangeFigures === null) {
      await this._previewExchange();
      if (this._exchangeFigures === null) {
        this._error = this._exchangeError ?? 'The exchange has not been priced yet';
        return;
      }
    }
    // The branch country's large cash rule (CP-17a): the server says what
    // this exchange needs of the counter. A crossing asks for the payer's
    // details before the exchange is rung; a captured payer rides it.
    const largeCash = this._exchangeFigures.large_cash;
    if (!this._payerDeclined && largeCash?.asks_details && this._payer === null) {
      this._payerAsk = {
        fields: largeCash.fields,
        dealerThreshold: largeCash.dealer_threshold,
        floor: largeCash.asks_floor && !largeCash.threshold_crossed,
        thresholdCrossed: largeCash.threshold_crossed,
      };
      this._payerRefused = false;
      this._payerForExchange = true;
      return;
    }
    if (!(await this._acknowledgeCounterWarnings(sale.customer_id))) return;
    const body = this._exchangeBody(true);
    if (body === null) {
      this._error = 'Name the goods coming back and the goods that replace them';
      return;
    }
    try {
      this._loading = true;
      const done = await posService.createExchange(body);
      this._showReturn = false;
      this._exchangeOn = false;
      // the payer capture and the avoidance mark went with the exchange that
      // needed them
      this._resetLargeCashCapture();
      this._payerForExchange = false;
      this._suspectAvoidance = false;
      const d = differenceOf(done);
      this._success = `Exchange ${done.return.number} complete - ${d.text}`;
      this._errorTimer = setTimeout(() => { this._success = null; }, 5000);
      // the replacement sale is the sale on screen: its receipt and signature follow
      this._sale = done.sale;
      this._completed = done.sale;
      this._sealed = null;
    } catch (err: unknown) {
      if (err instanceof ApiError && err.hasBlocker('alert_unacknowledged')) void this._syncStanding();
      // A large cash crossing without the payer's details (CP-17a): the
      // capture is asked again in the dialog, in words, and the exchange is
      // not taken.
      if (err instanceof ApiError && (err.hasBlocker('payer_details_required') || err.hasBlocker('walk_in_identity_required'))) {
        this._error = null;
        this._payerRefused = true;
        this._payerForExchange = true;
        if (this._payerAsk === null) {
          const ask = this._exchangeFigures?.large_cash;
          const floor = err.hasBlocker('walk_in_identity_required');
          this._payerAsk = {
            fields: ask?.fields ?? ['name', 'address', 'taxpayer_id', 'date_of_birth', 'occupation', 'id_type', 'id_number', 'third_party_payer'],
            dealerThreshold: ask?.dealer_threshold ?? false,
            floor: floor || (ask?.asks_floor ?? false),
            thresholdCrossed: floor ? false : (ask?.threshold_crossed ?? true),
          };
        }
        return;
      }
      this._error = apiErrorMessage(err, 'Failed to take the exchange');
    } finally {
      this._loading = false;
    }
  }

  /* ---- Modals ---- */

  private _overlay(inner: unknown) {
    return html`
      <div style="position:fixed;inset:0;background:rgb(var(--gb-backdrop) / 0.6);display:flex;align-items:center;justify-content:center;z-index:50">
        <div style="width:440px;max-width:92vw;max-height:92vh;overflow-y:auto;background:rgb(var(--gb-till-surface));border:1px solid rgb(var(--gb-till-edge));border-radius:14px;padding:24px;color:rgb(var(--gb-till-text));font-family:'Outfit',-apple-system,sans-serif">
          ${inner}
        </div>
      </div>
    `;
  }

  private _renderTillOpenModal() {
    if (!this._showTillOpen) return nothing;
    return this._overlay(html`
      <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">Open Till</h2>
      <p style="margin:0 0 16px;font-size:13px;color:rgb(var(--gb-till-muted))">Count the starting cash in the drawer and enter the opening float.</p>
      <label style="display:block;font-size:12px;color:rgb(var(--gb-till-muted));margin-bottom:6px">Opening float ($)</label>
      <input type="number" step="0.01" .value=${this._openingFloat} data-testid="till-float-amount"
        @input=${(e: Event) => { this._openingFloat = (e.target as HTMLInputElement).value; this._tillFloatError = ''; }}
        class="placeholder:font-normal placeholder:text-[rgb(var(--gb-till-muted))]"
        style="width:100%;box-sizing:border-box;padding:14px;background:rgb(var(--gb-till-field));border:2px solid rgb(var(--gb-till-accent));border-radius:8px;color:rgb(var(--gb-till-text));font-size:22px;font-weight:700;text-align:center;outline:none" placeholder="Type the counted amount" />
      ${this._tillFloatError ? html`<p role="alert" data-testid="till-float-error" style="margin:8px 0 0;font-size:13px;color:rgb(var(--gb-till-bad))">${this._tillFloatError}</p>` : nothing}
      <div style="display:flex;gap:8px;margin-top:20px">
        <button @click=${() => { this._showTillOpen = false; this._tillFloatError = ''; }} style="flex:1;padding:12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-muted));font-size:14px;cursor:pointer">Cancel</button>
        <button @click=${() => this._openTill()} ?disabled=${this._tillBusy} style="flex:2;padding:12px;background:rgb(var(--gb-till-accent));border:none;border-radius:8px;color:rgb(var(--gb-plate));font-size:15px;font-weight:700;cursor:pointer">${this._tillBusy ? 'Opening\u2026' : 'Open Till'}</button>
      </div>
    `);
  }

  /** The tint line, in the counter's dialog frame (row CP-16). */
  private _renderTintModal() {
    const line = this._tintLine;
    if (!line || !this._sale) return nothing;
    return this._overlay(html`
      <gable-tint-line-dialog sale-id=${this._sale.id} line-id=${line.id} product-id=${line.product_id ?? ''}
        .quantity=${line.quantity ?? '1'}
        .uom=${line.uom ?? ''}
        .basePrice=${line.unit_price_ten_thousandths ?? 0}
        .customerId=${this._customer?.id ?? ''}
        @tint-cancel=${() => { this._tintLine = null; }}
        @tint-added=${(e: CustomEvent<Sale>) => { this._tintLine = null; this._sale = e.detail; }}></gable-tint-line-dialog>
    `);
  }

  /** The drawer's cash movements, in the till dialog frame (CP-08). */
  private _renderMovementModal() {
    if (!this._showMovement || !this._till) return nothing;
    return this._overlay(html`
      <gable-till-movement-dialog session-id=${this._till.id}
        @movement-cancel=${() => { this._showMovement = false; }}
        @movement-recorded=${(e: CustomEvent<TillMovement>) => this._onMovementRecorded(e.detail)}></gable-till-movement-dialog>
    `);
  }

  private _renderScaleModal() {
    if (!this._showScale) return nothing;
    return this._overlay(html`
      <gable-pos-scale-panel
        register-id=${this._registerId}
        .sale=${this._sale}
        @scale-sale=${(e: CustomEvent<Sale>) => { this._sale = e.detail; this._error = null; }}
        @scale-error=${(e: CustomEvent<string>) => { this._error = e.detail; }}
        @scale-close=${() => { this._showScale = false; }}>
      </gable-pos-scale-panel>
    `);
  }

  private _onMovementRecorded(m: TillMovement) {
    this._showMovement = false;
    const by = m.kind === 'pickup' && m.taken_by ? ` by ${m.taken_by}` : '';
    this._success = `${kindLabel(m.kind)} of ${fmt(m.amount_cents)} recorded${by}`;
    if (this._errorTimer) clearTimeout(this._errorTimer);
    this._errorTimer = setTimeout(() => { this._success = null; }, 2500);
  }

  /** The X report panel on the open drawer (CP-08). */
  private _renderXReportModal() {
    if (!this._showXReport) return nothing;
    return this._overlay(html`
      ${this._xReport ? html`
        <gable-till-report-view kind="x" .report=${this._xReport} branch-name=${this._register?.branch_name ?? ''}
          @report-close=${() => { this._showXReport = false; this._xReport = null; }}></gable-till-report-view>` : nothing}
    `);
  }

  /** The register's stored Z reports (CP-08). */
  private _renderZReportsModal() {
    if (!this._showZReports || !this._register) return nothing;
    return this._overlay(html`
      <gable-till-z-reports register-id=${this._registerId}
        @z-close=${() => { this._showZReports = false; }}></gable-till-z-reports>
    `);
  }

  private _renderTillCloseModal() {
    if (!this._showTillClose) return nothing;
    const close = () => { this._showTillClose = false; this._tillReport = null; this._closeResult = null; };

    // Result view (Z-report): expected vs counted vs over/short.
    if (this._closeResult) {
      const r = this._closeResult;
      const os = r.session.over_short_cents ?? 0;
      const osColor = os === 0 ? 'rgb(var(--gb-till-ok))' : os < 0 ? 'rgb(var(--gb-till-bad))' : 'rgb(var(--gb-till-warn))';
      const osLabel = os === 0 ? 'BALANCED' : os < 0 ? `SHORT ${fmt(Math.abs(os))}` : `OVER ${fmt(os)}`;
      // A count over the branch's threshold is not closed yet: a manager signs it off
      const waiting = r.session.status === 'pending_signoff';
      return this._overlay(html`
        <h2 style="margin:0 0 12px;font-size:18px;font-weight:700">${waiting ? 'Count Posted - Waiting for Sign Off' : 'Z-Report - Till Closed'}</h2>
        ${waiting ? html`<p data-testid="till-waiting-signoff" style="margin:0 0 12px;font-size:13px;color:rgb(var(--gb-till-warn))">The count is over the limit of ${fmt(r.session.variance_threshold_cents ?? 0)}. A manager must sign it off before the drawer is closed and the variance is booked.</p>` : nothing}
        <div style="text-align:center;padding:16px;background:rgb(var(--gb-till-field));border:1px solid rgb(var(--gb-till-edge));border-radius:10px;margin-bottom:16px">
          <div style="font-size:12px;color:rgb(var(--gb-till-muted));text-transform:uppercase;letter-spacing:0.5px">Over / Short</div>
          <div style="font-size:32px;font-weight:800;color:${osColor}">${osLabel}</div>
        </div>
        <table style="width:100%;border-collapse:collapse;font-size:13px">
          <thead><tr style="color:rgb(var(--gb-till-muted));text-align:right">
            <th style="text-align:left;padding:4px 0">Method</th><th>Expected</th><th>Counted</th><th>\u0394</th>
          </tr></thead>
          <tbody>
            ${Object.keys(r.session.expected_by_method || {}).filter((m) => m.toLowerCase() !== 'account').map((m) => {
              const exp = r.session.expected_by_method?.[m] ?? 0;
              const cnt = r.session.counted_by_method?.[m] ?? 0;
              const d = cnt - exp;
              return html`<tr style="text-align:right;border-top:1px solid rgb(var(--gb-till-rule))">
                <td style="text-align:left;padding:6px 0">${methodLabel(m.toLowerCase() as TenderMethod)}</td>
                <td>${fmt(exp)}</td><td>${fmt(cnt)}</td>
                <td style="color:${d === 0 ? 'rgb(var(--gb-till-muted))' : d < 0 ? 'rgb(var(--gb-till-bad))' : 'rgb(var(--gb-till-warn))'}">${fmt(d)}</td>
              </tr>`;
            })}
          </tbody>
        </table>
        <div style="font-size:12px;color:rgb(var(--gb-till-muted));margin-top:14px">${r.sale_count} sales \u00b7 ${fmt(r.sales_total_cents)} rung \u00b7 ${fmt(r.tax_total_cents)} tax \u00b7 ${fmt(r.change_cents)} change given</div>
        <button @click=${close} style="width:100%;margin-top:18px;padding:12px;background:rgb(var(--gb-till-go));border:none;border-radius:8px;color:rgb(var(--gb-plate));font-size:15px;font-weight:700;cursor:pointer">Done</button>
      `);
    }

    // Count view (blind — expected figures hidden until close posts).
    const rep = this._tillReport;
    const methods = Object.keys(this._counts);
    return this._overlay(html`
      <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">Close Till - Blind Count</h2>
      <p style="margin:0 0 16px;font-size:13px;color:rgb(var(--gb-till-muted))">
        Count the drawer and enter actuals by tender. Expected totals stay hidden until you post - that's the blind count.
        ${rep ? html`<br>${rep.sale_count} sales rung this session.` : nothing}
      </p>
      ${methods.map((m) => html`
        <div style="display:flex;align-items:center;gap:10px;margin-bottom:10px">
          <label style="width:90px;font-size:13px;color:rgb(var(--gb-till-soft))">${methodLabel(m as TenderMethod)}</label>
          <input type="number" step="0.01" .value=${this._counts[m]} data-testid="till-count-${m}"
            @input=${(e: Event) => { this._counts = { ...this._counts, [m]: (e.target as HTMLInputElement).value }; }}
            class="placeholder:font-normal placeholder:text-[rgb(var(--gb-till-muted))]"
            style="flex:1;box-sizing:border-box;padding:10px;background:rgb(var(--gb-till-field));border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-text));font-size:16px;text-align:right;outline:none" placeholder="Enter the count" />
        </div>
      `)}
      <div style="display:flex;gap:8px;margin-top:20px">
        <button @click=${close} style="flex:1;padding:12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-muted));font-size:14px;cursor:pointer">Cancel</button>
        <button @click=${() => this._confirmCloseTill()} ?disabled=${this._tillBusy} style="flex:2;padding:12px;background:rgb(var(--gb-till-accent));border:none;border-radius:8px;color:rgb(var(--gb-plate));font-size:15px;font-weight:700;cursor:pointer">${this._tillBusy ? 'Posting\u2026' : 'Post Count & Close'}</button>
      </div>
    `);
  }

  private _renderReturnSearchModal() {
    if (!this._showReturnSearch) return nothing;
    const input = 'padding:8px;background:rgb(var(--gb-till-field));border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-text));font-size:14px;outline:none';
    return this._overlay(html`
      <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">Return a past sale</h2>
      <p style="margin:0 0 16px;font-size:13px;color:rgb(var(--gb-till-muted))">Find the sale by its number, its invoice number, or the customer's name.</p>
      <label style="display:block;font-size:12px;color:rgb(var(--gb-till-muted));margin-bottom:6px">Search</label>
      <input type="text" .value=${this._returnSearchQuery} data-testid="return-search-query"
        @input=${(e: Event) => { this._returnSearchQuery = (e.target as HTMLInputElement).value; }}
        @keydown=${(e: KeyboardEvent) => { if (e.key === 'Enter') void this._searchReturns(); }}
        placeholder="Sale number, invoice number, or customer"
        style="width:100%;box-sizing:border-box;padding:10px;${input};margin-bottom:12px" />
      <label style="display:block;font-size:12px;color:rgb(var(--gb-till-muted));margin-bottom:6px">Date (optional)</label>
      <gable-date-field .value=${this._returnSearchDate} data-testid="return-search-date"
        @input=${(e: Event) => { this._returnSearchDate = (e.target as HTMLInputElement).value; }}
        style="margin-bottom:12px"></gable-date-field>
      ${this._returnSearchError ? html`<p role="alert" style="margin:0 0 12px;font-size:13px;color:rgb(var(--gb-till-bad))" data-testid="return-search-error">${this._returnSearchError}</p>` : nothing}
      <button @click=${() => void this._searchReturns()} ?disabled=${this._returnSearchBusy}
        style="width:100%;padding:12px;background:rgb(var(--gb-till-accent));border:none;border-radius:8px;color:rgb(var(--gb-plate));font-size:15px;font-weight:700;cursor:pointer;margin-bottom:16px"
        data-testid="return-search-submit">${this._returnSearchBusy ? 'Searching\u2026' : 'Search'}</button>
      ${this._returnSearchResults.length > 0 ? html`
        <div style="max-height:340px;overflow-y:auto" data-testid="return-search-results">
          ${this._returnSearchResults.map((s) => html`
            <div style="padding:10px;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;margin-bottom:8px" data-testid="return-search-result">
              <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;margin-bottom:4px">
                <span class="font-mono font-semibold" style="color:rgb(var(--gb-till-text))" data-testid="return-search-result-number">${s.number}</span>
                <span style="font-size:12px;color:rgb(var(--gb-till-muted))" data-testid="return-search-result-status">${({ completed: 'Completed', open: 'Open', held: 'Held', voided: 'Voided' } as Record<string, string>)[s.status] ?? s.status}</span>
              </div>
              <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;font-size:13px;color:rgb(var(--gb-till-soft));margin-bottom:6px">
                <span data-testid="return-search-result-customer">${s.customer_name ?? 'Walk-in'}</span>
                <span class="font-mono" data-testid="return-search-result-total">${formatCents(s.total_cents)}</span>
              </div>
              <div style="font-size:12px;color:rgb(var(--gb-till-muted));margin-bottom:6px" data-testid="return-search-result-date">${s.business_day}</div>
              ${s.status === 'voided' ? html`
                <p style="margin:0;font-size:12px;color:rgb(var(--gb-till-bad))" data-testid="return-search-result-voided">Voided - cannot be returned</p>` : s.status !== 'completed' ? html`
                <p style="margin:0;font-size:12px;color:rgb(var(--gb-till-muted))" data-testid="return-search-result-not-completed">Not completed yet - cannot be returned</p>` : html`
                <button @click=${() => void this._pickReturnSearchResult(s)} ?disabled=${this._returnSearchBusy}
                  style="width:100%;padding:8px;background:rgb(var(--gb-till-go));border:none;border-radius:6px;color:rgb(var(--gb-plate));font-size:13px;font-weight:700;cursor:pointer"
                  data-testid="return-search-result-pick">Return this sale</button>`}
            </div>`)}
        </div>` : nothing}
      <div style="display:flex;gap:8px;margin-top:8px">
        <button @click=${() => { this._showReturnSearch = false; }} style="flex:1;padding:12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-muted));font-size:14px;cursor:pointer">Cancel</button>
      </div>
    `);
  }

  private _renderReturnModal() {
    if (!this._showReturn || !this._completed) return nothing;
    const sale = this._completed;
    const productLines = sale.lines.filter(l => l.line_type === 'product' && l.parent_line_id === null);
    const input = 'padding:8px;background:rgb(var(--gb-till-field));border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-text));font-size:14px;outline:none';
    const reasons = this._reasons;
    const note = reasons.find((r) => r.needs_note);
    const figures = this._exchangeFigures;
    const diff = figures !== null ? differenceOf(figures) : null;
    return this._overlay(html`
      <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">${this._exchangeOn ? 'Exchange' : 'Return'} - ${sale.number}</h2>
      <p style="margin:0 0 16px;font-size:13px;color:rgb(var(--gb-till-muted))">${this._exchangeOn
        ? 'Name the lines coming back and the goods that replace them. The credit is applied to the new sale, and only the difference is paid.'
        : 'Name the lines coming back. The return posts a credit memo, restocks what returns, and refunds out of the drawer.'}</p>
      ${productLines.map((l) => {
        const remaining = this._returnableQty(l);
        const alreadyReturned = Number(this._completed?.returned_quantities?.[l.id] ?? '0');
        return html`
        <div style="margin-bottom:10px" data-testid="return-line">
          <div style="display:flex;align-items:center;gap:10px">
            <span style="flex:1;font-size:13px;color:rgb(var(--gb-till-soft))">${l.description}</span>
            <span style="font-size:12px;color:rgb(var(--gb-till-muted))" data-testid="return-line-remaining">
              ${alreadyReturned > 0 ? html`${l.quantity} sold, ${alreadyReturned} already returned` : html`of ${l.quantity}`}
            </span>
            <input type="number" min="0" max=${remaining} step="0.25" .value=${this._returnQty[l.id] ?? '0'} data-testid="return-qty"
              @input=${(e: Event) => {
                const v = (e.target as HTMLInputElement).value;
                const max = Number(remaining);
                const n = Number(v);
                const clamped = v === '' ? '' : (n > max ? String(max) : v);
                this._returnQty = { ...this._returnQty, [l.id]: clamped };
                this._scheduleDialogFigures();
              }}
              style="width:80px;${input};text-align:right" />
          </div>
          <div style="display:flex;align-items:center;gap:12px;margin-top:6px">
            ${l.tint ? html`
            <span style="font-size:12px;color:rgb(var(--gb-till-muted))" data-testid="return-tint-rack">
              Tinted: re-tinted and put on the marked down rack, not back into stock
            </span>` : html`
            <label style="display:flex;align-items:center;gap:4px;font-size:12px;color:rgb(var(--gb-till-soft));cursor:pointer">
              <input type="radio" name=${`restock-${l.id}`} .checked=${this._returnRestockByLine[l.id] !== false}
                @change=${() => { this._returnRestockByLine = { ...this._returnRestockByLine, [l.id]: true }; }}
                data-testid="return-restock-back" />
              Back to stock
            </label>
            <label style="display:flex;align-items:center;gap:4px;font-size:12px;color:rgb(var(--gb-till-soft));cursor:pointer">
              <input type="radio" name=${`restock-${l.id}`} .checked=${this._returnRestockByLine[l.id] === false}
                @change=${() => { this._returnRestockByLine = { ...this._returnRestockByLine, [l.id]: false }; }}
                data-testid="return-restock-damaged" />
              Damaged, not restocked
            </label>`}
          </div>
          ${reasons.length > 0 && Number(this._returnQty[l.id] ?? '0') > 0 ? html`
            <div style="margin-top:6px">
              <gable-select data-testid="return-reason" .value=${this._returnReasonByLine[l.id] ?? ''}
                .options=${[{ value: '', label: 'Why is it coming back?' }, ...reasons.map((r) => ({ value: r.code, label: reasonLabel(r) }))]}
                @change=${(e: Event) => { this._returnReasonByLine = { ...this._returnReasonByLine, [l.id]: (e.target as HTMLSelectElement).value as ReturnReasonCode }; this._scheduleDialogFigures(); }}
              ></gable-select>
            </div>` : nothing}
        </div>
      `;})}
      ${this._exchangeOn ? this._renderExchangeBody(sale, input) : html`
        <div style="display:flex;align-items:center;gap:10px;margin:14px 0">
          <label style="font-size:13px;color:rgb(var(--gb-till-muted))">Refund</label>
          <div style="flex:1">
            <gable-select .value=${this._returnMethod}
              .options=${[
                { value: 'cash', label: 'Cash (out of the drawer)' },
                { value: 'card', label: 'Card (back on the card)' },
                { value: 'account', label: 'Account credit' },
              ]}
              @change=${(e: Event) => { this._returnMethod = (e.target as HTMLSelectElement).value as RefundMethod; }}
            ></gable-select>
          </div>
        </div>`}
      <input type="text" .value=${this._returnReason} data-testid="return-note"
        @input=${(e: Event) => { this._returnReason = (e.target as HTMLInputElement).value; this._scheduleDialogFigures(); }}
        style="width:100%;box-sizing:border-box;padding:10px;${input};margin-bottom:6px"
        placeholder=${reasons.length > 0 ? (note ? 'Note (needed when the reason is other)' : 'Note') : 'Reason'} />
      ${!this._exchangeOn && this._returnFigures !== null && this._returnFigures.restocking_fee_cents > 0 ? html`
        <p style="margin:0 0 6px;font-size:12px;color:rgb(var(--gb-till-muted))" data-testid="return-fee-note">A restocking fee is withheld from the refund of the lines whose reason carries one.</p>
        <div style="margin:0 0 12px;padding:10px;border:1px solid rgb(var(--gb-till-edge));border-radius:8px" data-testid="return-figures">
          <div style="display:flex;justify-content:space-between;font-size:13px;color:rgb(var(--gb-till-soft))">
            <span>Restocking fee</span><span class="font-mono" data-testid="return-figures-fee">${money(this._returnFigures.restocking_fee_cents)}</span>
          </div>
          ${this._returnFigures.lines.filter((fl) => fl.restocking_fee_cents > 0).map((fl) => {
            const line = sale.lines.find((l) => l.id === fl.line_id);
            return html`
            <div style="display:flex;justify-content:space-between;font-size:12px;color:rgb(var(--gb-till-muted))" data-testid="return-figures-line-fee">
              <span>${line?.description ?? 'A line'} (${this._reasons.find((r) => r.code === fl.reason_code)?.label ?? fl.reason_code})</span>
              <span class="font-mono">${money(fl.restocking_fee_cents)}</span>
            </div>`;
          })}
          <div style="display:flex;justify-content:space-between;margin-top:6px;font-size:15px;font-weight:700" data-testid="return-figures-refund-row">
            <span>Refund after the fee</span><span class="font-mono" data-testid="return-figures-refund">${money(this._returnFigures.refund_cents)}</span>
          </div>
          <p style="margin:6px 0 0;font-size:12px;color:rgb(var(--gb-till-muted))">Stated before the return is taken; the memo shows the fee as its own line.</p>
        </div>` : nothing}
      ${this._exchangeOn && diff !== null && figures !== null ? html`
        <div style="margin:12px 0;padding:10px;border:1px solid rgb(var(--gb-till-edge));border-radius:8px" data-testid="exchange-totals">
          ${exchangeTotals(figures).map((row) => html`
            <div style="display:flex;justify-content:space-between;font-size:13px;color:rgb(var(--gb-till-soft))"><span>${row.label}</span><span class="font-mono">${money(row.cents)}</span></div>`)}
          <div style="display:flex;justify-content:space-between;margin-top:6px;font-size:15px;font-weight:700" data-testid="exchange-difference"><span>${diff.text}</span></div>
        </div>` : nothing}
      ${this._exchangeOn && this._exchangeError ? html`<p role="alert" style="margin:8px 0;font-size:13px;color:rgb(var(--gb-till-bad))" data-testid="exchange-error">${this._exchangeError}</p>` : nothing}
      <div style="display:flex;gap:8px">
        <button @click=${() => { this._showReturn = false; this._exchangeOn = false; this._resetLargeCashCapture(); this._payerForExchange = false; this._suspectAvoidance = false; }} style="flex:1;padding:12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-muted));font-size:14px;cursor:pointer">Cancel</button>
        <button data-testid="return-exchange-toggle" @click=${() => { this._exchangeOn = !this._exchangeOn; if (!this._exchangeOn) this._suspectAvoidance = false; this._scheduleDialogFigures(); }} style="flex:1;padding:12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-soft));font-size:14px;cursor:pointer">${this._exchangeOn ? 'Just a return' : 'Exchange'}</button>
        ${this._exchangeOn ? html`
          <button data-testid="exchange-take" @click=${() => this._confirmExchange()} ?disabled=${this._loading || this._exchangeBusy || this._exchangeLines.length === 0} style="flex:2;padding:12px;background:rgb(var(--gb-till-go));border:none;border-radius:8px;color:rgb(var(--gb-plate));font-size:15px;font-weight:700;cursor:pointer">${this._loading ? 'Posting\u2026' : 'Take the Exchange'}</button>` : html`
          <button data-testid="return-take" @click=${() => this._confirmReturn()} ?disabled=${this._loading || this._returnBusy} style="flex:2;padding:12px;background:rgb(var(--gb-till-go));border:none;border-radius:8px;color:rgb(var(--gb-plate));font-size:15px;font-weight:700;cursor:pointer">${this._loading ? 'Posting\u2026' : 'Take the Return'}</button>`}
      </div>
    `);
  }

  /** The replacement goods, and how the difference is paid or paid back. */
  private _renderExchangeBody(sale: Sale, input: string) {
    const f = this._exchangeFigures;
    const kind = f !== null ? differenceOf(f).kind : null;
    return html`
      <div style="margin:12px 0" data-testid="exchange-body">
        <label style="display:block;font-size:12px;color:rgb(var(--gb-till-muted));margin-bottom:6px" for="exchange-search">Replacement goods</label>
        <input id="exchange-search" data-testid="exchange-search" type="text" autocomplete="off" .value=${this._exchangeQuery}
          placeholder="Find a product by name or SKU"
          @input=${(e: Event) => { this._exchangeQuery = (e.target as HTMLInputElement).value; this._debounceExchangeSearch(); }}
          style="width:100%;box-sizing:border-box;padding:10px;${input}" />
        ${this._exchangeResults.length > 0 ? html`
          <div style="margin-top:6px;border:1px solid rgb(var(--gb-till-edge));border-radius:8px" data-testid="exchange-results">
            ${this._exchangeResults.map((p) => html`
              <button @click=${() => this._addExchangeLine(p)}
                style="display:block;width:100%;text-align:left;padding:8px 10px;background:transparent;border:0;border-bottom:1px solid rgb(var(--gb-till-edge));color:rgb(var(--gb-till-soft));font-size:13px;cursor:pointer">
                ${p.description} <span class="font-mono" style="color:rgb(var(--gb-till-muted))">${p.sku}</span>
              </button>`)}
          </div>` : nothing}
        ${this._exchangeLines.map((l, i) => html`
          <div style="display:flex;align-items:center;gap:10px;margin-top:8px" data-testid="exchange-line">
            <span style="flex:1;font-size:13px;color:rgb(var(--gb-till-soft))">${l.product.description}</span>
            <input type="number" min="1" step="1" .value=${l.qty} data-testid="exchange-qty"
              @input=${(e: Event) => { this._exchangeLines = this._exchangeLines.map((x, j) => (j === i ? { ...x, qty: (e.target as HTMLInputElement).value } : x)); this._scheduleDialogFigures(); }}
              style="width:70px;${input};text-align:right" />
            <button aria-label="Remove ${l.product.description}" @click=${() => { this._exchangeLines = this._exchangeLines.filter((_, j) => j !== i); this._scheduleDialogFigures(); }}
              style="background:transparent;border:0;color:rgb(var(--gb-till-bad));font-size:18px;cursor:pointer">\u00d7</button>
          </div>`)}
        ${kind === 'pays' ? html`
          <div style="display:flex;align-items:center;gap:10px;margin-top:12px">
            <label style="font-size:13px;color:rgb(var(--gb-till-muted))">Customer pays by</label>
            <div style="flex:1">
              <gable-select data-testid="exchange-tender" .value=${this._exchangeTender}
                .options=${[{ value: 'cash', label: 'Cash' }, { value: 'check', label: 'Check' }, { value: 'card', label: 'Card' }]}
                @change=${(e: Event) => { this._exchangeTender = (e.target as HTMLSelectElement).value as ExchangeTenderMethod; }}
              ></gable-select>
            </div>
          </div>
          <label style="display:flex;align-items:flex-start;gap:8px;margin-top:10px;font-size:13px;color:rgb(var(--gb-till-muted))" data-testid="exchange-avoidance-label">
            <input type="checkbox" data-testid="exchange-avoidance" .checked=${this._suspectAvoidance}
              @change=${(e: Event) => { this._suspectAvoidance = (e.target as HTMLInputElement).checked; }} />
            <span>The manager marked this payment as suspected avoidance. A marked payment is counted as cash whatever was sold.</span>
          </label>` : nothing}
        ${kind === 'refund' ? html`
          <div style="display:flex;align-items:center;gap:10px;margin-top:12px">
            <label style="font-size:13px;color:rgb(var(--gb-till-muted))">Pay back</label>
            <div style="flex:1">
              <gable-select data-testid="exchange-refund" .value=${this._exchangeRefund}
                .options=${[{ value: 'cash', label: 'Cash (out of the drawer)' },
                  ...(sale.customer_id !== null ? [{ value: 'account', label: 'Store credit on the account' }] : [])]}
                @change=${(e: Event) => { this._exchangeRefund = (e.target as HTMLSelectElement).value as 'cash' | 'account'; }}
              ></gable-select>
            </div>
          </div>` : nothing}
      </div>`;
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
                  <gable-select id="pos-register-picker" data-testid="pos-register-picker"
                    .value=${this._registerId}
                    .options=${this._registers.map((r) => ({ value: r.id, label: `${r.branch_code} ${r.name}` }))}
                    @change=${(e: Event) => {
                      const picker = e.target as HTMLSelectElement;
                      void this._chooseRegister(picker.value).then((chosen) => {
                        if (!chosen) picker.value = this._registerId;
                      });
                    }}
                  ></gable-select>` : nothing}
              </p>` : nothing}
          </div>
          <nav class="hidden flex-wrap items-center gap-2 min-[900px]:flex" aria-label="Sale progress">
            ${this._progressChip('Customer', 1, progress.customer)}
            ${this._progressChip('Payment', 2, progress.payment)}
            ${this._progressChip('Summary', 3, progress.summary)}
          </nav>
        </div>
        <div class="relative flex w-full min-w-0 flex-wrap items-center justify-end gap-x-2 gap-y-2 min-[900px]:static min-[900px]:w-auto min-[900px]:flex-nowrap">
          ${this._till ? html`
            <span title="Drawer open with a float of ${fmt(this._till.opening_float_cents)}" class="whitespace-nowrap rounded-full border border-gable-green/40 px-2 py-0.5 font-mono text-xs font-semibold text-gable-green" data-testid="pos-till-state">Till open</span>` : html`
            <span class="whitespace-nowrap rounded-full border border-safety-red/40 px-2 py-0.5 text-xs font-semibold text-safety-red" data-testid="pos-till-state">No till</span>`}
          <span class="hidden max-w-[10rem] shrink-0 truncate text-xs text-slate-200 min-[900px]:inline" title=${cashier} data-testid="pos-cashier">${cashier}</span>
          ${!this._register ? nothing : this._till ? html`
            ${this._renderTillMenu()}
            <button @click=${() => { this._showScale = true; }} data-testid="pos-scale-button"
              title="Weigh a truck in and out, or enter a weight by hand"
              class="min-h-11 whitespace-nowrap rounded border border-white/20 px-3 py-2 text-xs text-slate-200 hover:bg-white/5">Scale</button>
            <button @click=${() => this._beginFresh()} ?disabled=${this._offline} title=${this._offline ? NEEDS_CONNECTION : 'Start a new sale'}
              class="min-h-11 whitespace-nowrap rounded border border-white/20 px-3 py-2 text-xs text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40">New Sale</button>` : html`
            <button id="pos-open-till" @click=${() => { this._showTillOpen = true; }}
              class="min-h-11 whitespace-nowrap rounded bg-gable-green px-3 py-2 text-xs font-semibold text-deep-space">Open Till</button>
            <button @click=${() => { this._showZReports = true; }} data-testid="pos-z-reports"
              title="The frozen Z report of each past drawer of this register"
              class="min-h-11 whitespace-nowrap rounded border border-white/20 px-3 py-2 text-xs text-slate-200">Z Reports</button>`}
          ${this._register && this._till ? html`
          <button @click=${() => this._openReturnSearch()} data-testid="return-search-open" ?disabled=${this._offline}
            title=${this._offline ? NEEDS_CONNECTION : 'Find a past sale to return'}
            class="min-h-11 whitespace-nowrap rounded border border-white/20 px-3 py-2 text-xs text-slate-200 disabled:cursor-not-allowed disabled:opacity-40">Return a past sale</button>` : nothing}
        </div>
      </header>`;
  }

  /**
   * The till's money controls: a Till menu button below 900 (a drawer and a
   * till close are money movements, kept off the thumb's first row on a
   * handheld), the same controls inline at 900 and wider.
   */
  private _renderTillMenu() {
    const item = 'flex min-h-11 w-full items-center whitespace-nowrap rounded border border-white/20 px-3 py-2 text-xs text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40';
    // offline, every act here needs the server; Close Till stays live only to say what holds it
    const off = this._offline;
    const closeHeld = off && tillCloseWait(this._offlineQueue, this._unknownCart) !== null;
    const needs = (title: string) => (off ? NEEDS_CONNECTION : title);
    return html`
      <div class="min-[900px]:contents" data-testid="pos-till-menu">
        <button type="button" data-testid="pos-till-menu-button" aria-label="Till" aria-haspopup="menu"
          title=${off ? 'Offline: drawer cash and X report need the connection; Close Till says what holds it' : 'Till'}
          aria-expanded=${this._tillMenu ? 'true' : 'false'} @click=${() => { this._tillMenu = !this._tillMenu; }}
          class="flex h-11 w-11 items-center justify-center rounded border border-white/20 text-slate-200 hover:bg-white/5 min-[900px]:hidden">
          <svg viewBox="0 0 24 24" class="h-5 w-5" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
            <rect x="3" y="6" width="18" height="13" rx="2"></rect><path d="M3 10h18"></path><path d="M7 15h4"></path>
          </svg>
        </button>
        ${this._tillMenu ? html`
          <div role="menu" aria-label="Till" data-testid="pos-till-menu-items"
            class="absolute right-0 top-full z-30 mt-1 flex w-44 flex-col gap-1 rounded border border-white/10 bg-slate-steel p-2 shadow-raised">
            <button role="menuitem" @click=${() => { this._tillMenu = false; this._showMovement = true; }} ?disabled=${this._tillBusy || off}
              data-testid="pos-drawer-cash" title=${needs("Safe drop, pickup, cash paid in or paid out")} class=${item}>Drawer Cash</button>
            <button role="menuitem" @click=${() => { this._tillMenu = false; this._beginXReport(); }} ?disabled=${this._tillBusy || off}
              data-testid="pos-x-report" title=${needs("A mid-shift read of the drawer, printable; not a close")} class=${item}>X Report</button>
            <button role="menuitem" @click=${() => { this._tillMenu = false; this._showZReports = true; }} ?disabled=${this._tillBusy || off}
              data-testid="pos-z-reports" title=${needs("The frozen Z report of each past drawer of this register")} class=${item}>Z Reports</button>
            <button role="menuitem" @click=${() => { this._tillMenu = false; this._beginCloseTill(); }} ?disabled=${this._tillBusy || (off && !closeHeld)}
              data-testid="pos-close-till" title=${off && !closeHeld ? NEEDS_CONNECTION : "Count the drawer and close the till with its Z report"} class=${item}>Close Till</button>
          </div>` : nothing}
        <div class="hidden items-center gap-2 min-[900px]:flex">
          <button @click=${() => { this._showMovement = true; }} ?disabled=${this._tillBusy || off} data-testid="pos-drawer-cash"
            title=${needs("Safe drop, pickup, cash paid in or paid out")}
            class="whitespace-nowrap rounded border border-white/20 px-3 py-1 text-xs text-slate-200 disabled:cursor-not-allowed disabled:opacity-40">Drawer Cash</button>
          <button @click=${() => this._beginXReport()} ?disabled=${this._tillBusy || off} data-testid="pos-x-report"
            title=${needs("A mid-shift read of the drawer, printable; not a close")}
            class="whitespace-nowrap rounded border border-white/20 px-3 py-1 text-xs text-slate-200 disabled:cursor-not-allowed disabled:opacity-40">X Report</button>
          <button @click=${() => { this._showZReports = true; }} ?disabled=${this._tillBusy || off} data-testid="pos-z-reports"
            title=${needs("The frozen Z report of each past drawer of this register")}
            class="whitespace-nowrap rounded border border-white/20 px-3 py-1 text-xs text-slate-200 disabled:cursor-not-allowed disabled:opacity-40">Z Reports</button>
          <button @click=${() => this._beginCloseTill()} ?disabled=${this._tillBusy || (off && !closeHeld)} data-testid="pos-close-till"
            title=${off && !closeHeld ? NEEDS_CONNECTION : "Count the drawer and close the till with its Z report"}
            class="whitespace-nowrap rounded border border-white/20 px-3 py-1 text-xs text-slate-200 disabled:cursor-not-allowed disabled:opacity-40">Close Till</button>
        </div>
      </div>`;
  }

  /** The Items card is the catalogue; it dims and locks with no till or no open sale. */
  private _renderItems() {
    return html`
      <div class="w-full min-w-0 min-[768px]:w-[52%] min-[768px]:shrink-0 xl:w-[56%] ${this._till === null ? 'opacity-60' : ''}" data-testid="pos-items">
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

/** The waiting card: the card leg the terminal still holds, its outcome in words, and the honest cancel. */
  private _renderCardWait() {
    const intent = this._cardIntent;
    if (!intent) return nothing;
    const words = cardWaitWords(intent);
    const waiting = intent.state === 'claimed' || intent.state === 'pending_at_device';
    return html`
      <section class="rounded-xl border ${waiting ? 'border-amber-400' : 'border-white/10'} bg-slate-steel p-4" data-testid="pos-card-wait" aria-live="polite">
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0">
            <h2 class="text-base font-semibold ${waiting ? 'text-amber-400' : 'text-white'}" data-testid="pos-card-wait-title">${words.title}</h2>
            <p class="mt-1 text-sm text-slate-300" data-testid="pos-card-wait-body">${words.body}</p>
          </div>
          <span class="shrink-0 font-mono text-sm font-semibold text-slate-200" data-testid="pos-card-wait-amount">${fmt(intent.amount_cents)}</span>
        </div>
        ${intent.state === 'lost' ? html`
          <a href="/pos/lost-answers" class="mt-3 inline-block text-sm font-semibold text-gable-green underline" data-testid="pos-card-wait-lost">The lost card answers</a>` : nothing}
        ${waiting ? html`
          <div class="mt-3 flex flex-wrap items-center gap-2">
            <button data-testid="pos-card-cancel" @click=${() => this._cancelCard()} ?disabled=${this._cardBusy}
              title=${CARD_CANCEL_WORDS}
              class="min-h-11 rounded border border-white/20 px-4 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40">Stop waiting</button>
            <span class="text-xs text-slate-400" data-testid="pos-card-cancel-words">${CARD_CANCEL_WORDS}</span>
          </div>` : html`
          <div class="mt-3">
            <button data-testid="pos-card-dismiss" @click=${() => this._dismissCardWait()}
              class="min-h-11 rounded border border-white/20 px-4 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5">Close</button>
          </div>`}
        ${this._cardNote ? html`
          <p class="mt-2 text-xs text-amber-400" role="status" data-testid="pos-card-note">${this._cardNote}</p>` : nothing}
      </section>`;
  }

  private _renderPaymentPanel(sale: Sale | null, hasLines: boolean, state: PanelState, isOpen: boolean) {
    const open = sale?.status === 'open';
    const total = sale?.total_cents ?? 0;
    const owed = this._owedCents;
    const entry = this._entry;
    const isCash = this._tenderMethod === 'cash';
    const payPlanNow = payPlan(total, this._pendingTenders, entry, this._roundingMode);
    // the change due is the plan's: the tendered amount above the total, given from the cash
    const change = payPlanNow.ok ? payPlanNow.changeCents : 0;
    const tendersOver = !payPlanNow.ok && payPlanNow.over !== undefined;
    // the card fee notice (CP-15): only while a card is the chosen way to pay and the branch charges one
    const cardFee = this._tenderMethod === 'card' ? cardFeeNotice(this._cardFee, entry?.amount_cents ?? owed) : null;
    const overWords = !payPlanNow.ok && payPlanNow.over === 'typed'
      ? `A card, check or account charge cannot be more than ${fmt(payPlanNow.limitCents ?? total)} here`
      : 'The added tenders are more than the sale now: clear them and choose again';
    const ready = this._till !== null && hasLines && open;
    // Why Charge to account is off, in words, once a customer is named; the way on when it is the PO.
    const accountWhy = this._customer !== null && open ? this._accountRefusal : null;
    const needsPO = accountWhy === PO_REFUSAL;
    // the job rule is the PO's mirror: the way back to the Customer panel
    // (where the Job select lives) belongs in the box for both; an account
    // with no job on file has none to pick, so its words carry no way back
    const needsJob = accountWhy === JOB_REFUSAL;
    return html`
      <section class="${this._panelClass(isOpen)} ${this._till === null ? 'opacity-50' : ''}" data-testid="panel-payment"
        aria-disabled=${this._till === null ? 'true' : 'false'}>
        ${this._panelHead('payment', 2, 'Payment', state, isOpen, state === 'done' ? this._paymentValue(sale) : '')}
        <div id="pos-panel-payment-body" data-testid="panel-payment-body" ?hidden=${!isOpen} class="px-4 pb-4">
          <div class="text-xs font-semibold uppercase tracking-wide text-slate-400">Amount due</div>
          <div class="font-mono text-4xl font-bold text-white" data-testid="pos-amount-due">${fmt(owed)}</div>
          ${this._roundingDiffCents !== 0 ? html`
            <div class="mt-1 flex justify-between text-sm text-amber-400" data-testid="pos-cash-due">
              <span>Cash due (rounded to the nearest 5 cents)</span><span class="font-mono">${fmt(this._cashDueCents)}</span>
            </div>` : nothing}
          ${this._pendingTenders.length > 0 ? html`
            <div class="mt-2 border-b border-dashed border-white/10 pb-2" data-testid="pos-added-tenders">
              ${this._pendingTenders.map((t) => {
                const idWords = this._addedCheckIDWords(t, entry, total);
                return html`
                <div class="flex justify-between py-0.5 text-sm text-slate-200">
                  <span class="capitalize">${t.method}${idWords !== '' ? html` <span class="text-xs ${t.id_checked ? 'text-gable-green' : 'text-amber-400'}" data-testid="pos-added-check-id">${idWords}</span>` : nothing}</span><span class="font-mono">${fmt(t.amount_cents)}</span>
                </div>`;
              })}
              <div class="flex justify-between pt-1 text-sm font-bold ${owed > 0 ? 'text-amber-400' : 'text-gable-green'}">
                <span>${owed > 0 ? 'Still owed' : 'Covered'}</span><span class="font-mono">${fmt(owed)}</span>
              </div>
            </div>` : nothing}
          <div class="mt-4 text-xs font-semibold uppercase tracking-wide text-slate-400">How is the customer paying?</div>
          <div class="mt-2 grid grid-cols-2 gap-2" role="group" aria-label="Tender">
            ${tenderMethods(this._standing).map((m) => {
              const refused = m === 'account' ? this._accountRefusal : m === 'card' ? this._cardRefusal : null;
              const chosen = this._tenderMethod === m && this._till !== null;
              const noCredit = m === 'store_credit' && storeCreditOffer(this._standing, owed) <= 0;
              return html`
                <button data-testid="tender-${m}" @click=${() => this._chooseMethod(m)}
                  ?disabled=${this._till === null || !hasLines || !open || refused !== null || noCredit}
                  title=${m === 'account' ? (refused ?? 'Charge the sale to the customer’s account')
                    : m === 'card' ? (refused ?? '')
                    : m === 'store_credit' ? `The account holds ${money(this._standing?.open_credit_memo_cents ?? 0)} of store credit` : ''}
                  aria-pressed=${chosen ? 'true' : 'false'}
                  class="min-h-14 rounded-lg border px-3 py-3 text-base font-semibold ${m === 'store_credit' ? 'col-span-2 min-[768px]:col-span-1 ' : ''}${m === 'account' || m === 'store_credit' ? '' : 'capitalize'} disabled:cursor-not-allowed ${chosen ? 'border-gable-green text-gable-green' : 'border-white/10 text-white disabled:opacity-40'}">
                  ${tenderTitle(m)}
                </button>`;
            })}
          </div>
          ${this._cardRefusal !== null && this._till !== null ? html`
            <p class="mt-2 text-xs text-slate-400" data-testid="pos-card-refusal">${this._cardRefusal}</p>` : nothing}
          ${accountWhy !== null ? html`
            <div class="mt-3 rounded-lg border border-amber-400/40 bg-amber-400/10 p-3 text-sm text-amber-400" data-testid="pos-account-refusal">
              <p>${accountWhy}</p>
              ${needsPO || needsJob ? html`
                <button data-testid="pos-refusal-customer" @click=${() => { this._step = 'customer'; this._open = 'customer'; }}
                  class="mt-2 min-h-11 rounded border border-white/20 px-3 py-2 text-sm font-semibold text-white hover:bg-white/5">${needsPO ? 'Enter the purchase order in Customer' : 'Pick the job in Customer'}</button>` : nothing}
              ${waysForward(this._standing).length > 0 ? html`
                <div class="mt-2 flex flex-wrap gap-2" data-testid="pos-refusal-ways">
                  ${waysForward(this._standing).map((w) => html`
                    <button data-testid="pos-refusal-way-${w.kind}" title=${w.message} @click=${() => void this._takeWay(w.kind)}
                      class="min-h-11 rounded border border-white/20 px-3 py-2 text-sm font-semibold text-white hover:bg-white/5">${wayLabel(w)}</button>`)}
                </div>` : nothing}
            </div>` : nothing}
          ${this._tenderMethod === 'check' || this._tenderMethod === 'account' ? this._renderCheckExtras() : nothing}
          <label class="mt-3 block text-xs text-slate-400" for="pos-tender-amount">
            ${this._tenderMethod === null ? 'Amount' : isCash ? 'Cash tendered' : html`<span class="capitalize">${this._tenderMethod}</span> amount`}
          </label>
          <input id="pos-tender-amount" type="text" inputmode="decimal" autocomplete="off"
            placeholder=${owed > 0 ? (owed / 100).toFixed(2) : ''} .value=${this._tenderAmount}
            ?disabled=${!hasLines || !open || this._tenderMethod === null}
            @input=${(e: Event) => { this._tenderAmount = (e.target as HTMLInputElement).value; this._scheduleLargeCashLook(); }}
            @blur=${() => this._formatTenderAmount()}
            class="mt-1 w-full rounded border border-white/10 bg-deep-space px-3 py-2 text-right font-mono text-xl font-bold text-white outline-none focus:border-gable-green"
            aria-label="Tender amount" />
          ${cardFee !== null ? html`
            <div class="mt-2 rounded-lg border border-white/10 bg-deep-space/60 p-3 text-sm text-slate-300" role="status" data-testid="pos-card-fee">
              <div data-testid="pos-card-fee-notice">${cardFee.fee}</div>
              <div class="mt-1 text-slate-400" data-testid="pos-card-fee-cash">${cardFee.cash}</div>
            </div>` : nothing}
          ${tendersOver ? html`
            <div class="mt-2 rounded-lg border border-amber-400/40 bg-amber-400/10 p-3 text-sm text-amber-400" role="status" data-testid="pos-tenders-over">
              ${overWords}
            </div>` : nothing}
          ${this._tenderMethod === 'card' && entry !== null && cardOverWords(entry.amount_cents, owed, fmt(owed)) ? html`
            <div class="mt-2 rounded-lg border border-amber-400/40 bg-amber-400/10 p-3 text-sm text-amber-400" role="status" data-testid="pos-card-over">
              ${cardOverWords(entry.amount_cents, owed, fmt(owed))}
            </div>` : nothing}
          ${this._walkInNote !== '' ? html`
            <div class="mt-2 rounded-lg border border-white/10 bg-deep-space/60 p-3 text-sm text-slate-300" role="status" data-testid="pos-walk-in-note">
              ${this._walkInNote}
            </div>` : nothing}
          ${change > 0 ? html`
            <div class="mt-2 flex justify-between text-sm font-bold text-gable-green" data-testid="pos-change">
              <span>Change due</span><span class="font-mono">${fmt(change)}</span>
            </div>` : nothing}
          <div class="mt-3 flex gap-2">
            <button data-testid="pos-add-tender" @click=${() => this._addPendingTender()}
              ?disabled=${!hasLines || !open || this._tenderMethod === null || entry === null || owed <= 0}
              class="min-h-11 flex-1 rounded border border-white/20 px-3 py-2 text-xs text-slate-200 disabled:opacity-40">Add as split tender</button>
            ${this._pendingTenders.length > 0 ? html`
              <button @click=${() => { this._resetTender(); }}
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

  /** The check number, the ID checked and its kind (never an ID number), and a check over the limit with its way forward. */
  private _renderCheckExtras() {
    const isCheck = this._tenderMethod === 'check';
    const limit = this._standing?.check_limit_cents ?? this._dealerCheckLimitCents;
    const amount = this._entry?.amount_cents ?? this._owedCents;
    // the rule reads the sale's checks in total: the added ones plus what is typed
    const checksTotal = isCheck ? this._checksTotalCents([...this._pendingTenders, { method: 'check', amount_cents: amount }]) : 0;
    const idNeeded = isCheck && !this._idChecked ? idRequiredWords(this._idRule, checksTotal) : null;
    return html`
      <div class="mt-3 rounded-lg border border-white/10 p-3" data-testid="pos-tender-extras">
        ${isCheck ? html`
          <label class="block text-xs text-slate-400" for="pos-check-ref">Check number</label>
          <input id="pos-check-ref" data-testid="pos-check-ref" type="text" autocomplete="off" maxlength="40" .value=${this._checkRef}
            @input=${(e: Event) => { this._checkRef = (e.target as HTMLInputElement).value; }}
            class="mt-1 w-full rounded border border-white/10 bg-deep-space px-2 py-2 font-mono text-sm text-white outline-none focus:border-gable-green" />
          ${limit !== null ? html`<p class="mt-1 text-xs text-slate-400" data-testid="pos-check-limit">Check limit ${money(limit)}</p>` : nothing}` : nothing}
        ${idNeeded !== null ? html`
          <div class="${isCheck ? 'mt-3 ' : ''}rounded border border-amber-400/40 bg-amber-400/10 p-2 text-sm text-amber-400" role="status" data-testid="pos-check-id-needed">
            <p>${idNeeded}</p>
          </div>` : nothing}
        <label class="${isCheck ? 'mt-3 ' : ''}flex items-center gap-2 text-sm text-slate-200" for="pos-id-checked">
          <input id="pos-id-checked" data-testid="pos-id-checked" type="checkbox" .checked=${this._idChecked}
            @change=${(e: Event) => { this._idChecked = (e.target as HTMLInputElement).checked; if (!this._idChecked) this._idType = ''; const entry = this._entry; if (entry) this._stampEntryIDOntoAddedChecks(entry); }} />
          ID checked
        </label>
        ${this._idChecked ? html`
          <div class="mt-2">
            <gable-select id="pos-id-type" data-testid="pos-id-type" .value=${this._idType}
              .options=${[{ value: '', label: 'Kind of ID' }, ...ID_TYPES]}
              @change=${(e: Event) => { this._idType = (e.target as HTMLSelectElement).value as IdType | ''; const entry = this._entry; if (entry) this._stampEntryIDOntoAddedChecks(entry); }}
            ></gable-select>
            <p class="mt-1 text-xs text-slate-400">Only the kind of ID is kept, never its number</p>
          </div>` : nothing}
        ${isCheck && this._checkOverWords !== null ? html`
          <div class="mt-3 rounded border border-amber-400/40 bg-amber-400/10 p-2 text-sm text-amber-400" role="status" data-testid="pos-check-over">
            <p>${this._checkOverWords}</p>
            <button data-testid="pos-check-ask-manager" @click=${() => void this._askManager('check', this._entry?.amount_cents ?? this._owedCents)}
              class="mt-2 min-h-11 rounded border border-white/20 px-3 py-2 text-sm font-semibold text-white hover:bg-white/5">Ask a manager to approve the check</button>
          </div>` : nothing}
      </div>`;
  }

  /** The name of the job a sale names, when the customer's roster holds it. */
  private _jobName(jobId: string | null): string | null {
    if (jobId === null || jobId === '') return null;
    return this._customer?.jobs.find((j) => j.id === jobId)?.name ?? null;
  }

  private _renderSummaryPanel(sale: Sale | null, state: PanelState, isOpen: boolean) {
    const count = (sale?.lines ?? []).filter((l) => l.parent_line_id === null).length;
    const tender = this._paymentValue(sale);
    const jobName = this._jobName(this._jobId || sale?.job_id || null);
    return html`
      <section class="${this._panelClass(isOpen)} ${this._till === null ? 'opacity-50' : ''}" data-testid="panel-summary">
        ${this._panelHead('summary', 3, 'Summary', state, isOpen, sale?.status === 'completed' ? fmt(sale.total_cents) : '')}
        <div id="pos-panel-summary-body" data-testid="panel-summary-body" ?hidden=${!isOpen} class="px-4 pb-4">
          <dl class="text-sm">
            <div class="flex justify-between py-1"><dt class="text-slate-400">Customer</dt>
              <dd class="text-white">${this._customer?.name ?? 'Walk-in sale'}</dd></div>
            ${sale?.customer_po ? html`
              <div class="flex justify-between py-1"><dt class="text-slate-400">Purchase order</dt>
                <dd class="font-mono text-white" data-testid="review-po">${sale.customer_po}</dd></div>` : nothing}
            ${jobName !== null ? html`
              <div class="flex justify-between py-1"><dt class="text-slate-400">Job</dt>
                <dd class="text-white" data-testid="review-job">${jobName}</dd></div>` : nothing}
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
    const plan = payPlan(total, this._pendingTenders, this._entry, this._roundingMode);
    const canPay = this._canPay;
    const idHold = plan.ok ? this._idHoldWords(plan.tenders) : null;
    const clearable = open && hasLines && !this._loading;
    const label = this._loading ? 'Processing...'
      : completed ? 'Paid in full'
      : this._till === null ? `Pay ${fmt(total)}`
      : plan.ok && idHold !== null ? "Check the customer's ID"
      : plan.ok ? `Pay ${fmt(total)}`
      : !plan.ok && plan.over === 'added' ? 'Clear the added tenders'
      : !plan.ok && plan.over === 'typed' ? `Type no more than ${fmt(plan.limitCents ?? total)}`
      : hasLines && open && this._tenderMethod === null ? 'Choose how the customer pays'
      : hasLines ? `Still owed ${fmt(plan.owedCents)}` : `Pay ${fmt(total)}`;
    const po = (sale?.customer_po ?? this._poText).trim();
    const jobName = this._jobName(this._jobId || sale?.job_id || null);
    return html`
      <section class="rounded-xl border border-white/10 bg-slate-steel p-4" data-testid="pos-order-summary">
        <h2 class="text-lg font-bold text-white">Order summary</h2>
        <p class="text-sm text-slate-400" data-testid="pos-order-customer">${this._customer?.name ?? 'Walk-in sale'}</p>
        ${po !== '' || jobName !== null ? html`
          <p class="mt-0.5 font-mono text-xs text-slate-400" data-testid="pos-order-refs">
            ${po !== '' ? html`PO ${po}` : nothing}${po !== '' && jobName !== null ? html` &middot; ` : nothing}${jobName !== null ? html`Job ${jobName}` : nothing}
          </p>` : nothing}
        ${productLines.length === 0 ? html`
          <p class="mt-3 text-sm text-slate-400" data-testid="pos-no-lines">No items yet</p>` : html`
          <ul class="mt-3 space-y-3" aria-label="Ticket lines" data-testid="pos-lines">
            ${productLines.map((item) => html`
              <li class="flex items-center gap-3">
                ${this._renderThumb(item)}
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm font-semibold text-white">${item.description}</div>
                  ${weightBadgeText(item) ? html`<div class="font-mono text-xs text-gable-green" data-testid="pos-line-weight-badge">${weightBadgeText(item)}</div>` : nothing}
                  <div class="font-mono text-xs text-slate-400">${item.quantity} ${item.uom} at ${item.unit_price_ten_thousandths !== null ? formatPrice4(item.unit_price_ten_thousandths) : '-'}</div>
                  ${item.tint ? html`
                    <div class="font-mono text-xs text-slate-400" data-testid="pos-line-tint">
                      ${item.tint.color_name}${item.tint.color_code ? ` (${item.tint.color_code})` : ''} on ${item.tint.base_code} ${item.tint.container_size_code}
                      ${tintSummary(item.tint.components)}
                    </div>` : nothing}
                </div>
                ${open && item.tint === null && hasQuantityControl(item) && item.product_id ? html`
                  <button type="button" data-testid="pos-line-tint-button" title="Tint" aria-label="Tint ${item.description}"
                    @click=${() => { this._tintLine = item; }}
                    class="inline-flex h-11 shrink-0 items-center justify-center rounded border border-white/20 px-3 text-xs font-semibold text-slate-200 hover:bg-white/5">Tint</button>` : nothing}
                ${open && hasQuantityControl(item) ? this._renderQuantityControl(item) : nothing}
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
          ${sale?.tax_exemption ? html`
          <div class="flex items-baseline justify-between gap-2 py-1 text-slate-400"><dt>Tax</dt>
            <dd class="text-right text-xs" data-testid="summary-tax-exempt">Tax exempt${sale.tax_exemption.certificate_number ? html` &middot; ${sale.tax_exemption.certificate_number}` : nothing}${sale.tax_exemption.expiry_day ? html` &middot; expires ${sale.tax_exemption.expiry_day}` : nothing}</dd></div>` : html`
          <div class="flex justify-between py-1 text-slate-400"><dt>Tax</dt>
            <dd class="font-mono" data-testid="summary-tax">${fmt(sale?.tax_cents ?? 0)}</dd></div>`}
          <div class="mt-1 flex items-baseline justify-between gap-4 border-t border-white/10 pt-2 font-bold text-white"><dt>Total</dt>
            <dd class="font-mono text-2xl text-white" data-testid="summary-total">${fmt(total)}</dd></div>
          ${completed ? html`
            ${(sale?.tenders ?? []).map((t) => html`
              <div class="flex justify-between py-1 text-slate-300"><dt>${t.method === 'account' ? 'Charged to account' : t.method === 'store_credit' ? 'Paid with store credit' : `Paid by ${t.method}`}</dt>
                <dd class="font-mono">${fmt(t.amount_cents)}</dd></div>
              ${(t.surcharge_cents ?? 0) > 0 ? html`
              <div class="flex justify-between py-1 text-slate-300" data-testid="summary-card-fee"><dt>Card fee</dt>
                <dd class="font-mono">${fmt(t.surcharge_cents)}</dd></div>` : nothing}`)}
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

  /** Sends the sale's receipt to the contact on the account, then follows the
   *  queued delivery the 202 names: the note says what actually happened
   *  (sent, not sent because email is not set up here, not sent because the
   *  mail server refused, or still sending), never success on the 202 alone.
   *  The server says in words if there is no contact to send to. */
  private async _emailReceipt(saleId: string) {
    if (this._receiptBusy) return;
    this._receiptBusy = true;
    try {
      this._error = null;
      const queued = await posService.emailReceipt(saleId);
      const invoiceId = this._sale?.invoice_id ?? null;
      const delivery = queued.delivery_id && invoiceId
        ? await EmailDeliveryService.follow(queued.delivery_id, invoiceId)
        : undefined;
      const answer = receiptEmailAnswer(delivery);
      this._receiptNote = answer.note;
      this._receiptTone = answer.tone;
    } catch (err: unknown) {
      this._receiptNote = err instanceof Error ? err.message : 'Failed to email the receipt';
      this._receiptTone = 'error';
    } finally {
      this._receiptBusy = false;
    }
  }

  private _dropReceiptDownload() {
    if (this._receiptDownload) URL.revokeObjectURL(this._receiptDownload);
    this._receiptDownload = null;
  }

  /**
   * Opens the sale's receipt document (the 80mm receipt when asked); a refusal
   * is said in words beside Print receipt. Each print counts against the sale,
   * so the sale is read again and the next print says Reprint.
   */
  private async _printReceipt(saleId: string, layout?: 'thermal80') {
    this._dropReceiptDownload();
    try {
      const shown = layout ? await posService.printReceipt(saleId, layout) : await posService.printReceipt(saleId);
      await this._refreshPrintCount(saleId);
      if (shown?.blocked) {
        this._receiptDownload = shown.url;
        this._receiptNote = 'Allow pop ups for this page to print the receipt, or download it';
        this._receiptTone = 'error';
        return;
      }
      this._receiptNote = null;
      this._receiptTone = 'plain';
    } catch (err: unknown) {
      this._receiptNote = err instanceof Error ? err.message : 'Failed to print the receipt';
      this._receiptTone = 'error';
    }
  }

  /** Reads the sale again for its print count, so the screen says Reprint after a print. */
  private async _refreshPrintCount(saleId: string) {
    try {
      const fresh = await posService.getSale(saleId);
      if (!fresh) return;
      if (this._completed?.id === saleId) this._completed = fresh;
      if (this._sale?.id === saleId) this._sale = fresh;
    } catch {
      // the count on screen stays until the next read
    }
  }

  /** Seals the signer's name and the drawn signature to the completed sale. */
  private async _sealSignature(sale: Sale) {
    const pad = this.querySelector('gable-signature-pad') as SignaturePad | null;
    const why = signatureRefusal(pad?.strokes ?? [], this._signerName);
    if (why) {
      this._signatureNote = why;
      return;
    }
    const png = pad?.png() ?? null;
    if (png === null) {
      this._signatureNote = 'The signature box gave no image: draw the signature again';
      return;
    }
    try {
      this._signing = true;
      this._signatureNote = null;
      const sealed = await posService.captureSignature(sale.id, this._signerName.trim(), png);
      this._sealed = { signer: sealed.signer_name, seal: sealed.seal };
    } catch (err: unknown) {
      this._signatureNote = apiErrorMessage(err, 'Failed to seal the signature');
    } finally {
      this._signing = false;
    }
  }

  /** The signature block on a completed sale: sealed once, never changed. */
  private _renderSignature(sale: Sale) {
    const account = sale.tenders.some((t) => t.method === 'account');
    if (this._sealed !== null) {
      return html`
        <div class="mt-4" data-testid="pos-signature">
          <div class="text-xs font-semibold uppercase tracking-wide text-slate-400">Signature</div>
          <p class="mt-2 text-sm text-gable-green" data-testid="pos-signature-sealed">${sealedWords(this._sealed.signer, this._sealed.seal)}</p>
        </div>`;
    }
    return html`
      <div class="mt-4" data-testid="pos-signature">
        <div class="text-xs font-semibold uppercase tracking-wide text-slate-400">Signature${account ? ' (account sign-off)' : ''}</div>
        <label class="mt-2 block text-xs text-slate-400" for="pos-signer-name">Signer</label>
        <input id="pos-signer-name" data-testid="pos-signer-name" type="text" autocomplete="off" maxlength="200" .value=${this._signerName}
          @input=${(e: Event) => { this._signerName = (e.target as HTMLInputElement).value; }}
          class="mt-1 w-full rounded border border-white/10 bg-deep-space px-2 py-2 text-sm text-white outline-none focus:border-gable-green" />
        <div class="mt-2"><gable-signature-pad></gable-signature-pad></div>
        <div class="mt-2 flex gap-2">
          <button data-testid="pos-signature-clear" @click=${() => (this.querySelector('gable-signature-pad') as SignaturePad | null)?.clear()}
            class="min-h-11 flex-1 rounded border border-white/20 px-3 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5">Clear</button>
          <button data-testid="pos-signature-seal" @click=${() => void this._sealSignature(sale)} ?disabled=${this._signing}
            class="min-h-11 flex-1 rounded border border-white/20 px-3 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40">Seal signature</button>
        </div>
        ${this._signatureNote ? html`<p class="mt-2 text-xs text-safety-red" data-testid="pos-signature-note" role="status">${this._signatureNote}</p>` : nothing}
      </div>`;
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
    const po = (sale.customer_po ?? '').trim();
    const jobName = this._jobName(sale.job_id || this._jobId);
    return html`
      <section class="rounded-xl border border-gable-green bg-slate-steel p-4" data-testid="pos-complete" aria-live="polite">
        <div class="flex items-center gap-3">
          <span class="flex h-12 w-12 shrink-0 items-center justify-center rounded-full bg-gable-green text-2xl font-bold text-deep-space" aria-hidden="true">✓</span>
          <div class="min-w-0">
            <h2 class="text-xl font-bold text-white">Sale complete</h2>
            <p class="font-mono text-sm text-slate-300">${sale.number} &middot; ${fmt(sale.total_cents)}${how !== '' ? html` &middot; ${how}` : nothing}${sale.change_cents > 0 ? html`, change ${fmt(sale.change_cents)}` : nothing}</p>
            ${po !== '' || jobName !== null ? html`
              <p class="mt-1 font-mono text-xs text-slate-400" data-testid="pos-complete-refs">
                ${po !== '' ? html`PO ${po}` : nothing}${po !== '' && jobName !== null ? html` &middot; ` : nothing}${jobName !== null ? html`Job ${jobName}` : nothing}
              </p>` : nothing}
          </div>
        </div>
        ${invoice ? html`
          <div class="mt-4">
            <div class="text-xs font-semibold uppercase tracking-wide text-slate-400">Receipt</div>
            <div class="mt-2 flex flex-wrap gap-2">
              <button data-testid="pos-receipt-print" @click=${() => this._printReceipt(sale.id)}
                class="min-h-11 flex-1 rounded border border-white/20 px-3 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5">${sale.print_count > 0 ? 'Reprint receipt' : 'Print receipt'}</button>
              <button data-testid="pos-receipt-print-80" @click=${() => this._printReceipt(sale.id, 'thermal80')}
                class="min-h-11 flex-1 rounded border border-white/20 px-3 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5">${sale.print_count > 0 ? 'Reprint 80mm' : 'Print 80mm'}</button>
              ${sale.customer_id !== null ? html`
                <button data-testid="pos-receipt-email" @click=${() => this._emailReceipt(sale.id)} ?disabled=${this._receiptBusy}
                  class="min-h-11 flex-1 rounded border border-white/20 px-3 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40">Email receipt</button>` : nothing}
            </div>
            ${sale.customer_id !== null ? html`<p class="mt-2 text-xs text-slate-400" data-testid="pos-receipt-contact">Email goes to the contact on the account</p>` : nothing}
            ${this._receiptNote ? html`<p class="mt-2 text-xs ${this._receiptTone === 'error' ? 'text-safety-red' : this._receiptTone === 'warn' ? 'text-amber-400' : 'text-slate-400'}" data-testid="pos-receipt-note" role="status">${this._receiptNote}</p>` : nothing}
            ${this._receiptDownload ? html`<a data-testid="pos-receipt-download" href=${this._receiptDownload} download="receipt-${sale.number}.pdf"
              class="mt-1 inline-block text-xs font-semibold text-gable-green underline">Download the receipt</a>` : nothing}
            ${sale.print_count > 0 ? html`<p class="mt-2 text-xs text-slate-400" data-testid="pos-print-count">Printed ${sale.print_count} time${sale.print_count === 1 ? '' : 's'}; the next is a reprint</p>` : nothing}
          </div>
          ${this._renderSignature(sale)}` : nothing}
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
    const tenderChosen = this._tenderMethod !== null || this._pendingTenders.length > 0;
    const progress = panelProgress(this._step, completed, tenderChosen);
    const openKey = completed || this._till === null ? null : this._open;
    const customerValue = this._customer?.name ?? 'Walk-in sale';

    return html`
      <div class="flex h-screen flex-col bg-deep-space font-sans text-white" data-testid="pos-terminal">
        ${this._renderHeader(sale, progress)}
        ${this._renderHotkeyLegend()}
        ${this._renderTillOpenModal()}
        ${this._renderTillCloseModal()}
        ${this._renderMovementModal()}
        ${this._renderStepUp()}
        ${this._renderXReportModal()}
        ${this._renderZReportsModal()}
        ${this._renderReturnSearchModal()}
        ${this._renderTintModal()}
        ${this._renderReturnModal()}
        ${this._renderScaleModal()}

        ${this._error ? html`
          <div class="flex items-center justify-between border-b border-safety-red/40 bg-safety-red/10 px-4 py-2 text-sm text-safety-red" role="alert">
            ${this._error}
            <button @click=${() => { this._error = null; }} class="text-lg" aria-label="Dismiss error">×</button>
          </div>` : nothing}
        ${this._success ? html`
          <div class="border-b border-gable-green/30 bg-gable-green/10 px-4 py-2 text-center text-sm font-semibold text-gable-green" role="status">
            ${this._success}
          </div>` : nothing}

        ${this._renderOfflineBand()}
        ${this._offline ? html`
          <gable-pos-offline class="flex min-h-0 flex-1 overflow-y-auto" data-testid="pos-offline-mode"
            .seed=${this._offlineSeed} .unknownCartId=${this._unknownCart}
            @offline-sold=${this._onOfflineSold}></gable-pos-offline>` : this._registersFailed ? html`
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
        <div class="flex min-h-0 flex-1 flex-col items-start gap-4 overflow-y-auto p-4 min-[768px]:flex-row" data-testid="pos-body">
          ${this._renderItems()}
          <div class="flex w-full min-w-0 shrink-0 flex-col gap-3 min-[768px]:w-auto min-[768px]:flex-1" data-testid="pos-panels">
            <section class="${this._panelClass(openKey === 'customer')} ${this._till === null ? 'opacity-50' : ''}" data-testid="panel-customer">
              ${this._panelHead('customer', 1, 'Customer', progress.customer, openKey === 'customer', customerValue)}
              <div id="pos-panel-customer-body" data-testid="panel-customer-body" ?hidden=${openKey !== 'customer'} class="px-4 pb-4">
                ${this._renderCustomerPanel()}
              </div>
            </section>
            ${this._renderCardWait()}
            ${this._renderPaymentPanel(sale, hasLines, progress.payment, openKey === 'payment')}
            ${this._renderSummaryPanel(sale, progress.summary, openKey === 'summary')}
            ${completed && sale ? this._renderSaleComplete(sale) : nothing}
            ${this._renderOrderSummary(sale, productLines, hasLines)}
          </div>
        </div>
        ${this._renderPayBar(sale, productLines)}`}
        ${this._payerAsk !== null ? html`
          <gable-payer-dialog
            .ask=${this._payerAsk}
            ?refused=${this._payerRefused}
            .prior=${this._payer}
            @payer-save=${(e: CustomEvent) => {
              this._payer = e.detail;
              this._payerAsk = null;
              this._payerRefused = false;
              this._payerDeclined = false;
              if (this._payerForExchange) {
                // the capture answers the exchange: ring it now
                this._payerForExchange = false;
                void this._confirmExchange();
                return;
              }
              void this._pay();
            }}
            @payer-decline=${() => {
              // the walk-in declined to identify over the branch's floor: the
              // sale goes ahead and the fact is recorded for a manager
              this._payerDeclined = true;
              this._payerAsk = null;
              this._payerRefused = false;
              if (this._payerForExchange) {
                this._payerForExchange = false;
                void this._confirmExchange();
                return;
              }
              void this._pay();
            }}
            @payer-cancel=${() => { this._payerAsk = null; this._payerRefused = false; this._payerForExchange = false; }}
          ></gable-payer-dialog>
        ` : nothing}
      </div>
    `;
  }

  /**
   * The offline band (ADR 0011 section 4): the banner with the waiting count,
   * total and the room left under the queue caps; the strip with what the
   * sync answered and why new prices could not be taken; the reload warning
   * where the webview refused the service worker; and the question about a
   * replaced cart once the connection returned.
   */
  private _renderOfflineBand() {
    const room = this._offlineRoom;
    const reloadWarning = typeof document !== 'undefined' ? document.documentElement.getAttribute(POS_SW_WARNING_ATTR) : null;
    return html`
      ${this._offline ? html`
        <div class="border-b border-amber-400/40 bg-amber-400/10 px-4 py-2 text-sm text-amber-400" role="status" data-testid="pos-offline-banner">
          <p class="font-semibold">${OFFLINE_BANNER}</p>
          ${room ? html`<p class="mt-0.5 text-xs" data-testid="pos-offline-banner-figures">
            Waiting: <span class="font-mono">${room.count}</span> ${room.count === 1 ? 'sale' : 'sales'}, <span class="font-mono">${formatCents(room.totalCents)}</span>.
            Room left: <span class="font-mono">${room.roomCount}</span> ${room.roomCount === 1 ? 'sale' : 'sales'}, <span class="font-mono">${formatCents(room.roomCents)}</span>.</p>` : nothing}
          ${reloadWarning ? html`<p class="mt-0.5 text-xs font-semibold" data-testid="pos-offline-reload-warning">${reloadWarning}</p>` : nothing}
        </div>` : nothing}
      ${this._offlineStrip ? html`
        <div class="flex items-center justify-between gap-3 border-b border-blueprint-blue/30 bg-blueprint-blue/10 px-4 py-2 text-sm text-blueprint-blue" role="status" data-testid="pos-offline-strip">
          <span>${this._offlineStrip}</span>
          ${this._needsSignIn ? html`<button type="button" data-testid="pos-offline-sign-in" class="min-h-11 rounded-lg bg-gable-green px-4 font-semibold text-deep-space" @click=${() => void this._signInRegister()}>${NO_SESSION_ASK}</button>` : nothing}
          <button type="button" class="min-h-11 px-2 text-lg" aria-label="Dismiss" @click=${() => { this._offlineStrip = null; }}>×</button>
        </div>` : nothing}
      ${this._sameGoods ? html`
        <div class="flex flex-wrap items-center gap-3 border-b border-white/10 bg-slate-steel px-4 py-3 text-sm" role="group" data-testid="pos-offline-same-goods">
          <span>Offline sale <span class="font-mono">${this._sameGoods.offlineRef}</span> replaced the sale whose result was unknown. ${SAME_GOODS_QUESTION}</span>
          <button type="button" data-testid="pos-offline-same-goods-yes" class="min-h-11 rounded-lg bg-gable-green px-4 font-semibold text-deep-space" @click=${() => this._answerSameGoods(true)}>Yes</button>
          <button type="button" data-testid="pos-offline-same-goods-no" class="min-h-11 rounded-lg border border-white/20 px-4 font-semibold" @click=${() => this._answerSameGoods(false)}>No</button>
        </div>` : nothing}`;
  }

  /**
   * The pinned pay bar, a phone affordance below 768 only: the line count
   * and the total (one tap to the payment step), Pay at thumb height. At 768
   * and wider the Pay button in the order summary card is the affordance.
   */
  private _renderPayBar(sale: Sale | null, productLines: SaleLine[]) {
    if (!this._register || this._offline) return nothing;
    const total = sale?.total_cents ?? 0;
    const canPay = this._canPay;
    const label = this._loading ? 'Processing...' : `Pay ${fmt(total)}`;
    return html`
      <div class="flex shrink-0 items-stretch gap-2 border-t border-white/10 bg-slate-steel p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] min-[768px]:hidden" data-testid="pos-pay-bar">
        <button type="button" data-testid="pos-pay-bar-summary"
          title="Open the payment step"
          @click=${() => {
            this._step = 'payment';
            this._open = 'payment';
            this.querySelector('[data-testid="pos-panels"]')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
          }}
          class="flex min-h-14 flex-1 flex-col items-start justify-center rounded-lg border border-white/10 px-3 py-1 text-left">
          <span class="text-xs text-slate-400" data-testid="pos-pay-bar-lines">${productLines.length} item${productLines.length === 1 ? '' : 's'}</span>
          <span class="font-mono text-lg font-bold text-white" data-testid="pos-pay-bar-total">${fmt(total)}</span>
        </button>
        <button type="button" data-testid="pos-pay-bar-button" @click=${() => this._pay()} ?disabled=${!canPay}
          class="min-h-14 w-[45%] shrink-0 rounded-lg px-4 py-4 text-base font-bold ${canPay ? 'bg-gable-green text-deep-space' : 'cursor-not-allowed border border-white/10 bg-white/5 text-slate-400 opacity-40'}">
          ${label}
        </button>
      </div>`;
  }
}

export default POSTerminal;
