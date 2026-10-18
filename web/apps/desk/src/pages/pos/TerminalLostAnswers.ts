// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The manager's lost card answers (CP-05): the queue of card legs the sweep
// turned lost because the terminal may have moved money. A manager reads the
// terminal's own record and settles each item here (the terminal approved,
// or the terminal declined), and can run the sweep's own trigger. The words
// are terminal.ts's; the panel is the counter's own shape.

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { posService } from '../../services/POSService';
import type { TerminalLostItem, TerminalSweepResult } from '../../types/pos';
import { lostKindWords, lateWords, needsReference } from './terminal';

const fmt = (cents: number) => `$${(cents / 100).toFixed(2)}`;

@customElement('gable-terminal-lost-answers')
export class TerminalLostAnswers extends LitElement {
  createRenderRoot() { return this; }

  @state() private _items: TerminalLostItem[] | null = null;
  @state() private _error: string | null = null;
  @state() private _note: string | null = null;
  @state() private _busy = false;
  /** The reference the manager typed, per item: what they read off the terminal. */
  @state() private _references: Record<string, string> = {};
  @state() private _reasons: Record<string, string> = {};

  connectedCallback() {
    super.connectedCallback();
    void this._load();
  }

  private async _load() {
    try {
      this._busy = true;
      this._error = null;
      this._items = await posService.listTerminalLostItems();
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to read the lost card answers';
    } finally {
      this._busy = false;
    }
  }

  /** The manager's own trigger of the sweep act; the counts it closed and queued are said in words. */
  private async _sweep() {
    try {
      this._busy = true;
      this._error = null;
      const result: TerminalSweepResult = await posService.sweepTerminalIntents();
      this._note = `The sweep closed ${result.claimed_closed} claim${result.claimed_closed === 1 ? '' : 's'} that never reached a device and filed ${result.dispatched_lost} lost answer${result.dispatched_lost === 1 ? '' : 's'}.`;
      await this._load();
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to sweep the card terminal intents';
    } finally {
      this._busy = false;
    }
  }

  /** Records what the terminal's own record said: approved money is applied, declined money closed. */
  private async _resolve(item: TerminalLostItem, outcome: 'authorized' | 'declined') {
    const reference = (this._references[item.id] ?? '').trim();
    if (reference === '' && needsReference(item, outcome)) {
      this._error = 'Read the terminal\'s own record first and type its reference for this charge';
      return;
    }
    try {
      this._busy = true;
      this._error = null;
      await posService.resolveTerminalLostItem(item.id, outcome, reference, (this._reasons[item.id] ?? '').trim());
      this._note = outcome === 'authorized'
        ? `Recorded: the terminal approved ${fmt(item.amount_cents)} on ${item.register_id}. The money went where it belongs, or back to the card.`
        : `Recorded: the terminal declined ${fmt(item.amount_cents)} on ${item.register_id}. Nothing moved.`;
      await this._load();
    } catch (err: unknown) {
      this._error = err instanceof Error ? err.message : 'Failed to resolve the lost card answer';
    } finally {
      this._busy = false;
    }
  }

  render() {
    const items = this._items;
    return html`
      <div class="mx-auto max-w-4xl p-8 pb-20" data-testid="lost-answers">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 class="text-xl font-bold text-white">Lost card answers</h1>
            <p class="mt-1 text-sm text-slate-400">Card legs the terminal may have moved and never answered for. Read the terminal's own record, then record what it said.</p>
          </div>
          <button data-testid="lost-answers-sweep" @click=${() => this._sweep()} ?disabled=${this._busy}
            class="min-h-11 rounded border border-white/20 px-4 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40">Sweep the terminal intents now</button>
        </div>
        ${this._error ? html`
          <div class="mt-4 rounded-lg border border-safety-red/40 bg-safety-red/10 p-3 text-sm text-safety-red" role="alert" data-testid="lost-answers-error">${this._error}</div>` : nothing}
        ${this._note ? html`
          <div class="mt-4 rounded-lg border border-gable-green/30 bg-gable-green/10 p-3 text-sm font-semibold text-gable-green" role="status" data-testid="lost-answers-note">${this._note}</div>` : nothing}
        ${items === null ? html`
          <p class="mt-6 text-sm text-slate-400" data-testid="lost-answers-loading">Reading the queue...</p>` : items.length === 0 ? html`
          <p class="mt-6 text-sm text-slate-400" data-testid="lost-answers-empty">No lost card answers. The queue is empty.</p>` : html`
          <ul class="mt-6 space-y-3" data-testid="lost-answers-list">
            ${items.map((item) => html`
              <li class="rounded-xl border border-white/10 bg-slate-steel p-4" data-testid="lost-answer-item" data-item-id=${item.id}>
                <div class="flex flex-wrap items-baseline justify-between gap-2">
                  <div class="text-sm font-semibold text-white">
                    ${lostKindWords(item.kind)} of ${fmt(item.amount_cents)}
                  </div>
                  <div class="font-mono text-xs text-slate-400">register ${item.register_id}${item.provider_ref !== '' ? html` &middot; device reference ${item.provider_ref}` : nothing}</div>
                </div>
                <div class="mt-1 font-mono text-xs text-slate-400">transaction ${item.transaction_id} &middot; intent ${item.intent_id}</div>
                ${lateWords(item) !== null ? html`
                  <p class="mt-2 text-sm text-amber-300" data-testid="lost-answer-late">${lateWords(item)}</p>` : nothing}
                ${item.state === 'resolving' ? html`
                  <p class="mt-2 text-sm text-slate-300" data-testid="lost-answer-resolving">A manager is settling this one now. Read the queue again in a moment.</p>` : nothing}
                <div class="mt-3 grid grid-cols-1 gap-2 min-[700px]:grid-cols-2">
                  <div>
                    <label class="block text-xs text-slate-400" for=${`lost-ref-${item.id}`}>What the terminal's record said (reference)</label>
                    <input id=${`lost-ref-${item.id}`} type="text" .value=${this._references[item.id] ?? ''}
                      @input=${(e: Event) => { this._references = { ...this._references, [item.id]: (e.target as HTMLInputElement).value }; }}
                      class="mt-1 w-full rounded border border-white/10 bg-deep-space px-3 py-2 text-sm text-white outline-none focus:border-gable-green" />
                  </div>
                  <div>
                    <label class="block text-xs text-slate-400" for=${`lost-reason-${item.id}`}>Reason (kept with the record)</label>
                    <input id=${`lost-reason-${item.id}`} type="text" .value=${this._reasons[item.id] ?? ''}
                      @input=${(e: Event) => { this._reasons = { ...this._reasons, [item.id]: (e.target as HTMLInputElement).value }; }}
                      class="mt-1 w-full rounded border border-white/10 bg-deep-space px-3 py-2 text-sm text-white outline-none focus:border-gable-green" />
                </div>
                </div>
                <div class="mt-3 flex flex-wrap gap-2">
                  <button data-testid="lost-answer-authorized" @click=${() => this._resolve(item, 'authorized')} ?disabled=${this._busy || item.state === 'resolving'}
                    class="min-h-11 rounded bg-gable-green px-4 py-2 text-sm font-bold text-deep-space disabled:cursor-not-allowed disabled:opacity-40">The terminal approved</button>
                  <button data-testid="lost-answer-declined" @click=${() => this._resolve(item, 'declined')} ?disabled=${this._busy || item.state === 'resolving'}
                    class="min-h-11 rounded border border-white/20 px-4 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40">The terminal declined</button>
                </div>
              </li>`)}
          </ul>`}
      </div>`;
  }
}

export default TerminalLostAnswers;
