// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

// The manager's large cash worklist (CP-17b): the queue of the cash receipts
// the dealer owes a filing on. A manager reads each row, sees how long is
// left, records the filing they made in BSA E-Filing, and issues the
// customer's statement when the filing is done. The words are the house's
// voice: a crossing past its deadline says so, a walk-in who would not say
// who they were says that, and a Canadian crossing says it is the dealer's
// own record and not a FINTRAC report.
//
// The queue never carries the payer's taxpayer identification number. The
// Form 8300 data set is the one surface that does, and it is behind the
// filer's own read.

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { LargeCashService } from '../../services/LargeCashService';
import type { LargeCashForm8300, LargeCashWorklistRow } from '../../types/largecash';
import { deadlineWords, actionWords } from './largeCashWords';

const SURFACE = 'background:rgb(var(--gb-till-surface));border:1px solid rgb(var(--gb-till-edge))';
const INK = 'color:rgb(var(--gb-till-text))';
const MUTED = 'color:rgb(var(--gb-till-muted))';
const FIELD = 'background:rgb(var(--gb-till-field));border:1px solid rgb(var(--gb-till-edge));color:rgb(var(--gb-till-text))';

@customElement('gable-large-cash-worklist')
export class LargeCashWorklist extends LitElement {
    createRenderRoot() { return this; }

    @state() private _rows: LargeCashWorklistRow[] | null = null;
    @state() private _error: string | null = null;
    @state() private _note: string | null = null;
    @state() private _busy = false;
    /** The BSA E-Filing acknowledgement number the manager typed, per crossing. */
    @state() private _acks: Record<string, string> = {};
    /** The Form 8300 data set, once the filer asked for it. */
    @state() private _forms: Record<string, LargeCashForm8300 | null> = {};

    connectedCallback() {
        super.connectedCallback();
        void this._load();
    }

    private async _load() {
        try {
            this._busy = true;
            this._error = null;
            const list = await LargeCashService.listWorklist();
            this._rows = list.items ?? [];
        } catch (err: unknown) {
            this._error = err instanceof Error ? err.message : 'Failed to read the large cash worklist';
        } finally {
            this._busy = false;
        }
    }

    /** The manager's record of the Form 8300 they filed for this crossing. */
    private async _recordFiling(row: LargeCashWorklistRow) {
        const acknowledgement = (this._acks[row.event_id] ?? '').trim();
        if (acknowledgement === '') {
            this._error = 'Type the BSA E-Filing acknowledgement number the filing gave you';
            return;
        }
        try {
            this._busy = true;
            this._error = null;
            await LargeCashService.recordFiling(row.event_id, acknowledgement);
            this._note = `Recorded: the Form 8300 for ${row.payer_name} is filed.`;
            await this._load();
        } catch (err: unknown) {
            this._error = err instanceof Error ? err.message : 'Failed to record the large cash filing';
        } finally {
            this._busy = false;
        }
    }

    /** The customer's statement for this payer's year. */
    private async _issueStatement(row: LargeCashWorklistRow) {
        try {
            this._busy = true;
            this._error = null;
            await LargeCashService.issueStatement(row.event_id);
            this._note = `Issued: the customer statement for ${row.payer_name} is out.`;
            await this._load();
        } catch (err: unknown) {
            this._error = err instanceof Error ? err.message : 'Failed to issue the customer statement';
        } finally {
            this._busy = false;
        }
    }

    /** The data set the filer types into BSA E-Filing. */
    private async _showForm(row: LargeCashWorklistRow) {
        try {
            this._busy = true;
            this._error = null;
            const form = await LargeCashService.form8300(row.event_id);
            this._forms = { ...this._forms, [row.event_id]: form };
        } catch (err: unknown) {
            this._error = err instanceof Error ? err.message : 'Failed to read the Form 8300 data set';
        } finally {
            this._busy = false;
        }
    }

    render() {
        const rows = this._rows;
        return html`
      <div class="mx-auto max-w-4xl p-8 pb-20" data-testid="large-cash-worklist">
        <div>
          <h1 class="text-xl font-bold" style=${INK}>Large cash worklist</h1>
          <p class="mt-1 text-sm" style=${MUTED}>
            Cash receipts over the threshold the dealer owes a filing on. Work them from the top:
            the nearest deadline first.
          </p>
        </div>
        ${this._error ? html`
          <div class="mt-4 rounded-lg p-3 text-sm" role="alert" data-testid="large-cash-error"
            style="border:1px solid rgb(var(--gb-safety-red));color:rgb(var(--gb-safety-red))">${this._error}</div>` : nothing}
        ${this._note ? html`
          <div class="mt-4 rounded-lg p-3 text-sm font-semibold" role="status" data-testid="large-cash-note"
            style="border:1px solid rgb(var(--gb-till-ok));color:rgb(var(--gb-till-ok))">${this._note}</div>` : nothing}
        ${rows === null ? html`
          <p class="mt-6 text-sm" style=${MUTED} data-testid="large-cash-loading">Reading the queue...</p>` : rows.length === 0 ? html`
          <p class="mt-6 text-sm" style=${MUTED} data-testid="large-cash-empty">No large cash crossings. The queue is empty.</p>` : html`
          <ul class="mt-6 space-y-3" data-testid="large-cash-list">
            ${rows.map((row) => this._row(row))}
          </ul>`}
      </div>`;
    }

    private _row(row: LargeCashWorklistRow) {
        const words = actionWords(row);
        const form = this._forms[row.event_id] ?? null;
        return html`
        <li class="rounded-xl p-4" style=${SURFACE} data-testid="large-cash-row" data-event-id=${row.event_id}>
          <div class="flex flex-wrap items-baseline justify-between gap-2">
            <div class="text-sm font-semibold" style=${INK}>
              ${row.payer_name} &middot; $${(row.total_cents / 100).toFixed(2)}
            </div>
            <div class="font-mono text-xs" style=${MUTED}>
              ${row.branch_name}${row.contributed_branch_ids.length > 1
                ? html` &middot; ${row.contributed_branch_ids.length} branches in the total`
                : nothing}
            </div>
          </div>
          <div class="mt-1 font-mono text-xs" style=${MUTED}>
            ${row.currency} ${row.total_cents} cents &middot; ${row.transaction_count} cash transaction${row.transaction_count === 1 ? '' : 's'}
            &middot; threshold ${row.threshold_cents} cents
          </div>
          <p class="mt-2 text-sm" style=${row.past_deadline ? 'color:rgb(var(--gb-till-bad))' : MUTED}
            data-testid="large-cash-deadline">${deadlineWords(row)}</p>
          ${row.note ? html`
            <p class="mt-2 text-sm" style="color:rgb(var(--gb-till-warn))" data-testid="large-cash-note">${row.note}</p>` : nothing}
          <p class="mt-2 text-sm" style=${MUTED} data-testid="large-cash-action">${words.say}</p>
          ${words.canFile ? html`
            <div class="mt-3 grid grid-cols-1 gap-2 min-[700px]:grid-cols-2">
              <div>
                <label class="block text-xs" style=${MUTED} for=${`lc-ack-${row.event_id}`}>BSA E-Filing acknowledgement number</label>
                <input id=${`lc-ack-${row.event_id}`} type="text" .value=${this._acks[row.event_id] ?? ''}
                  @input=${(e: Event) => { this._acks = { ...this._acks, [row.event_id]: (e.target as HTMLInputElement).value }; }}
                  class="mt-1 w-full rounded px-3 py-2 text-sm outline-none" style=${FIELD} />
              </div>
            </div>
            <div class="mt-3 flex flex-wrap gap-2">
              <button data-testid="large-cash-file" @click=${() => this._recordFiling(row)} ?disabled=${this._busy}
                class="min-h-11 rounded px-4 py-2 text-sm font-bold"
                style="background:rgb(var(--gb-till-accent));color:rgb(var(--gb-plate));${this._busy ? 'opacity:0.4' : ''}">${words.button}</button>
            </div>` : nothing}
          ${words.canStatement ? html`
            <div class="mt-3 flex flex-wrap gap-2">
              <button data-testid="large-cash-statement" @click=${() => this._issueStatement(row)} ?disabled=${this._busy}
                class="min-h-11 rounded px-4 py-2 text-sm font-bold"
                style="background:rgb(var(--gb-till-accent));color:rgb(var(--gb-plate));${this._busy ? 'opacity:0.4' : ''}">Issue the customer statement</button>
            </div>` : nothing}
          <div class="mt-3 flex flex-wrap gap-2">
            <button data-testid="large-cash-form" @click=${() => this._showForm(row)} ?disabled=${this._busy}
              class="min-h-11 rounded border px-4 py-2 text-sm font-semibold"
              style="border-color:rgb(var(--gb-till-edge));${MUTED};${this._busy ? 'opacity:0.4' : ''}">Read the Form 8300 data set</button>
          </div>
          ${form ? html`
            <div class="mt-3 rounded-lg p-3 text-sm" style=${FIELD} data-testid="large-cash-form-data">
              <div class="font-semibold" style=${INK}>${form.form} for ${form.payer.name}</div>
              <div class="mt-1" style=${MUTED}>${form.payer.address}</div>
              <div class="mt-1" style=${MUTED}>Taxpayer identification number ${form.payer.taxpayer_id}</div>
              <div class="mt-1" style=${MUTED}>${form.currency} ${form.amount_cents} cents, received ${form.first_date} to ${form.last_date}</div>
              <div class="mt-1" style=${MUTED}>Filed for ${form.filer.branch_name}. Due ${form.filing_deadline}.</div>
              <div class="mt-2" style=${INK}>${form.note}</div>
            </div>` : nothing}
          <p class="mt-3 text-xs" style=${MUTED} data-testid="large-cash-adviser">${row.adviser_note}</p>
        </li>`;
    }
}

export default LargeCashWorklist;
