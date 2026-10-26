// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The manager's open refund intents (CP-06): voids, returns and paybacks that
// are waiting on the terminal's answer or on finance. A manager pays back a
// failed void's held credit, or resolves a pending claim by recording what the
// terminal actually did.

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { posService } from '../../services/POSService';
import type { RefundIntent, RefundIntentAct, RefundIntentState, TenderMethod } from '../../types/pos';
import { formatCents } from '../../lib/utils';

const ageWords = (createdAt: string): string => {
  const ms = Date.now() - new Date(createdAt).getTime();
  const minutes = Math.floor(ms / 60000);
  if (minutes < 1) return 'waiting a moment';
  if (minutes < 60) return `waiting ${minutes} minute${minutes === 1 ? '' : 's'}`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `waiting ${hours} hour${hours === 1 ? '' : 's'}`;
  const days = Math.floor(hours / 24);
  return `waiting ${days} day${days === 1 ? '' : 's'}`;
};

const tenderWords = (m: TenderMethod): string => {
  if (m === 'store_credit') return 'Store credit';
  if (m === 'account') return 'Charge to account';
  return m.charAt(0).toUpperCase() + m.slice(1);
};

const actWords = (act: RefundIntentAct): string => {
  if (act === 'void') return 'Void refund';
  if (act === 'return') return 'Return refund';
  return 'Payback';
};

const stateWords = (state: RefundIntentState, heldCreditCents: number): string => {
  if (state === 'FAILED') {
    return heldCreditCents > 0
      ? 'Refund declined - the credit is held'
      : 'Refund declined';
  }
  return "Waiting on the terminal's answer";
};

@customElement('gable-refund-intents')
export class RefundIntents extends LitElement {
  createRenderRoot() { return this; }

  @state() private _items: RefundIntent[] | null = null;
  @state() private _error: string | null = null;
  @state() private _note: string | null = null;
  @state() private _busy = false;
  @state() private _paybackReasons: Record<string, string> = {};
  @state() private _resolveOutcomes: Record<string, 'refund' | 'no_refund'> = {};
  @state() private _resolveRefs: Record<string, string> = {};
  @state() private _resolveReasons: Record<string, string> = {};
  @state() private _openForm: Record<string, 'payback' | 'resolve' | null> = {};

  connectedCallback() {
    super.connectedCallback();
    void this._load();
  }

  private async _load() {
    try {
      this._busy = true;
      this._error = null;
      this._items = await posService.listRefundIntents();
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to read the refund intents';
    } finally {
      this._busy = false;
    }
  }

  private async _payback(item: RefundIntent) {
    const reason = (this._paybackReasons[item.id] ?? '').trim();
    if (reason === '') {
      this._error = 'A reason is required to pay the credit back';
      return;
    }
    try {
      this._busy = true;
      this._error = null;
      const result = await posService.paybackRefundIntent(item.id, reason);
      this._note = `Paid back ${formatCents(result.amount_cents)} to the ${tenderWords(item.tender_method).toLowerCase()} tender. The terminal's refund id is ${result.refund_tx_id.slice(0, 8)}.`;
      this._openForm = { ...this._openForm, [item.id]: null };
      await this._load();
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to pay back the refund intent';
    } finally {
      this._busy = false;
    }
  }

  private async _resolve(item: RefundIntent) {
    const outcome = this._resolveOutcomes[item.id] ?? 'refund';
    const reason = (this._resolveReasons[item.id] ?? '').trim();
    if (reason === '') {
      this._error = 'A reason is required to resolve the claim';
      return;
    }
    const refundTxId = (this._resolveRefs[item.id] ?? '').trim();
    if (outcome === 'refund' && refundTxId === '') {
      this._error = 'The terminal\'s refund id is required when the terminal refunded it';
      return;
    }
    if (outcome === 'no_refund' && refundTxId === '') {
      this._error = 'A reconciliation reference is required when no refund went out';
      return;
    }
    try {
      this._busy = true;
      this._error = null;
      await posService.resolveRefundIntent(item.id, {
        outcome,
        ...(outcome === 'refund' ? { refund_tx_id: refundTxId } : { reconciliation_reference: refundTxId }),
        reason,
      });
      this._note = outcome === 'refund'
        ? `Recorded: the terminal refunded ${formatCents(item.amount_cents)}.`
        : 'Recorded: no refund went out for this claim.';
      this._openForm = { ...this._openForm, [item.id]: null };
      await this._load();
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to resolve the refund intent';
    } finally {
      this._busy = false;
    }
  }

  render() {
    const items = this._items;
    return html`
      <div class="mx-auto max-w-4xl p-8 pb-20" data-testid="refund-intents">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 class="text-xl font-bold text-white">Refund intents</h1>
            <p class="mt-1 text-sm text-slate-400">Open refund claims waiting on the terminal or on finance.</p>
          </div>
          <button data-testid="refund-intents-reload" @click=${() => void this._load()} ?disabled=${this._busy}
            class="min-h-11 rounded border border-white/20 px-4 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40">Reload</button>
        </div>
        ${this._error ? html`
          <div class="mt-4 rounded-lg border border-safety-red/40 bg-safety-red/10 p-3 text-sm text-safety-red" role="alert" data-testid="refund-intents-error">${this._error}</div>` : nothing}
        ${this._note ? html`
          <div class="mt-4 rounded-lg border border-gable-green/30 bg-gable-green/10 p-3 text-sm font-semibold text-gable-green" role="status" data-testid="refund-intents-note">${this._note}</div>` : nothing}
        ${items === null ? html`
          <p class="mt-6 text-sm text-slate-400" data-testid="refund-intents-loading">Reading the queue...</p>` : items.length === 0 ? html`
          <p class="mt-6 text-sm text-slate-400" data-testid="refund-intents-empty">No open refund intents. The queue is empty.</p>` : html`
          <ul class="mt-6 space-y-3" data-testid="refund-intents-list">
            ${items.map((item) => html`
              <li class="rounded-xl border border-white/10 bg-slate-steel p-4" data-testid="refund-intent-item" data-item-id=${item.id}>
                <div class="flex flex-wrap items-baseline justify-between gap-2">
                  <div class="text-sm font-semibold text-white">
                    ${actWords(item.act)} for sale ${item.sale_number}
                  </div>
                  <div class="font-mono text-xs text-slate-400" data-testid="refund-intent-amount">${formatCents(item.amount_cents)}</div>
                </div>
                <div class="mt-1 text-sm text-slate-300" data-testid="refund-intent-tender">${tenderWords(item.tender_method)} tender</div>
                <div class="mt-1 text-xs text-slate-400" data-testid="refund-intent-state">${stateWords(item.state, item.held_credit_cents)}</div>
                <div class="mt-1 text-xs text-slate-500" data-testid="refund-intent-age">${ageWords(item.created_at)}</div>
                ${item.state === 'FAILED' ? html`
                  <div class="mt-3 flex flex-wrap gap-2">
                    ${this._openForm[item.id] === 'payback' ? html`
                      <div class="w-full">
                        <label class="block text-xs text-slate-400" for=${`payback-reason-${item.id}`}>Reason</label>
                        <input id=${`payback-reason-${item.id}`} type="text" .value=${this._paybackReasons[item.id] ?? ''}
                          @input=${(e: Event) => { this._paybackReasons = { ...this._paybackReasons, [item.id]: (e.target as HTMLInputElement).value }; }}
                          class="mt-1 w-full rounded border border-white/10 bg-deep-space px-3 py-2 text-sm text-white outline-none focus:border-gable-green" />
                        <div class="mt-2 flex gap-2">
                          <button data-testid="refund-intent-payback-submit" @click=${() => void this._payback(item)} ?disabled=${this._busy}
                            class="min-h-11 rounded bg-gable-green px-4 py-2 text-sm font-bold text-deep-space disabled:cursor-not-allowed disabled:opacity-40">Pay back</button>
                          <button data-testid="refund-intent-payback-cancel" @click=${() => { this._openForm = { ...this._openForm, [item.id]: null }; }}
                            class="min-h-11 rounded border border-white/20 px-4 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5">Cancel</button>
                        </div>
                      </div>` : html`
                      <button data-testid="refund-intent-payback" @click=${() => { this._openForm = { ...this._openForm, [item.id]: 'payback' }; }} ?disabled=${this._busy}
                        class="min-h-11 rounded bg-gable-green px-4 py-2 text-sm font-bold text-deep-space disabled:cursor-not-allowed disabled:opacity-40">Pay back</button>`}
                  </div>` : nothing}
                ${item.state === 'PENDING' ? html`
                  <div class="mt-3 flex flex-wrap gap-2">
                    ${this._openForm[item.id] === 'resolve' ? html`
                      <div class="w-full space-y-3">
                        <div>
                          <label class="block text-xs text-slate-400">Outcome</label>
                          <div class="mt-1 flex gap-4">
                            <label class="flex items-center gap-2 text-sm text-slate-200 cursor-pointer">
                              <input type="radio" name=${`resolve-outcome-${item.id}`} .checked=${(this._resolveOutcomes[item.id] ?? 'refund') === 'refund'}
                                @change=${() => { this._resolveOutcomes = { ...this._resolveOutcomes, [item.id]: 'refund' }; }}
                                data-testid="refund-intent-resolve-refund" />
                              The terminal refunded it
                            </label>
                            <label class="flex items-center gap-2 text-sm text-slate-200 cursor-pointer">
                              <input type="radio" name=${`resolve-outcome-${item.id}`} .checked=${(this._resolveOutcomes[item.id] ?? 'refund') === 'no_refund'}
                                @change=${() => { this._resolveOutcomes = { ...this._resolveOutcomes, [item.id]: 'no_refund' }; }}
                                data-testid="refund-intent-resolve-none" />
                              No refund went out
                            </label>
                          </div>
                        </div>
                        <div>
                          <label class="block text-xs text-slate-400" for=${`resolve-ref-${item.id}`}>
                            ${(this._resolveOutcomes[item.id] ?? 'refund') === 'refund' ? "The terminal's refund id" : 'Reconciliation reference'}
                          </label>
                          <input id=${`resolve-ref-${item.id}`} type="text" .value=${this._resolveRefs[item.id] ?? ''}
                            @input=${(e: Event) => { this._resolveRefs = { ...this._resolveRefs, [item.id]: (e.target as HTMLInputElement).value }; }}
                            class="mt-1 w-full rounded border border-white/10 bg-deep-space px-3 py-2 text-sm text-white outline-none focus:border-gable-green" />
                        </div>
                        <div>
                          <label class="block text-xs text-slate-400" for=${`resolve-reason-${item.id}`}>Reason</label>
                          <input id=${`resolve-reason-${item.id}`} type="text" .value=${this._resolveReasons[item.id] ?? ''}
                            @input=${(e: Event) => { this._resolveReasons = { ...this._resolveReasons, [item.id]: (e.target as HTMLInputElement).value }; }}
                            class="mt-1 w-full rounded border border-white/10 bg-deep-space px-3 py-2 text-sm text-white outline-none focus:border-gable-green" />
                        </div>
                        <div class="flex gap-2">
                          <button data-testid="refund-intent-resolve-submit" @click=${() => void this._resolve(item)} ?disabled=${this._busy}
                            class="min-h-11 rounded bg-gable-green px-4 py-2 text-sm font-bold text-deep-space disabled:cursor-not-allowed disabled:opacity-40">Resolve</button>
                          <button data-testid="refund-intent-resolve-cancel" @click=${() => { this._openForm = { ...this._openForm, [item.id]: null }; }}
                            class="min-h-11 rounded border border-white/20 px-4 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5">Cancel</button>
                        </div>
                      </div>` : html`
                      <button data-testid="refund-intent-resolve" @click=${() => { this._openForm = { ...this._openForm, [item.id]: 'resolve' }; }} ?disabled=${this._busy}
                        class="min-h-11 rounded border border-white/20 px-4 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40">Resolve</button>`}
                  </div>` : nothing}
              </li>`)}
          </ul>`}
      </div>`;
  }
}

export default RefundIntents;
