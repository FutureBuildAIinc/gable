// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import '../ui/gable-select';
import { customElement, property, state } from 'lit/decorators.js';
import type { CreatePaymentRequest, PaymentMethod } from '../../types/payment';

// The methods the payments route records today (a card charge is the
// gateway's own flow through Run Payments).
const DEPOSIT_METHODS: PaymentMethod[] = ['cash', 'check', 'ach', 'other'];
const METHOD_LABELS: Record<string, string> = { cash: 'Cash', check: 'Check', ach: 'ACH', other: 'Other' };

/**
 * The deposit dialog on an order (CP-07): money in dollars shown, integer
 * cents posted, against the order's customer and branch with the order named
 * and no applications, so it is held unapplied until the order bills. A
 * deposit past the order's balance due after deposits is refused here in the
 * same words the server refuses it.
 */
@customElement('gable-deposit-modal')
export class GableDepositModal extends LitElement {
  createRenderRoot() { return this; }

  @property({ type: Boolean, attribute: 'is-open' }) isOpen = false;
  @property({ type: String, attribute: 'order-id' }) orderId = '';
  @property({ type: String, attribute: 'customer-id' }) customerId = '';
  @property({ type: String, attribute: 'branch-id' }) branchId = '';
  /** What the customer still owes on the order: the most a deposit may take. */
  @property({ type: Number, attribute: 'balance-due-cents' }) balanceDueCents = 0;

  @state() private _amountDollars = 0;
  @state() private _method: PaymentMethod = 'cash';
  @state() private _reference = '';
  @state() private _notes = '';
  @state() private _error = '';

  updated(changed: Map<string, unknown>) {
    if (changed.has('isOpen') && this.isOpen) {
      this._amountDollars = 0;
      this._method = 'cash';
      this._reference = '';
      this._notes = '';
      this._error = '';
    }
  }

  private _amountCents(): number {
    return Math.round((Number(this._amountDollars) || 0) * 100);
  }

  private async _handleSubmit(e: Event) {
    e.preventDefault();
    const amountCents = this._amountCents();
    if (amountCents < 1) {
      this._error = 'Enter the amount of the deposit, one cent or more.';
      return;
    }
    if (amountCents > this.balanceDueCents) {
      this._error = 'The deposit is larger than the order\'s balance due after deposits.';
      return;
    }
    const deposit: CreatePaymentRequest = {
      customer_id: this.customerId,
      amount_cents: amountCents,
      method: this._method,
      order_id: this.orderId,
    };
    if (this.branchId) deposit.branch_id = this.branchId;
    if (this._reference.trim()) deposit.reference = this._reference.trim();
    if (this._notes.trim()) deposit.notes = this._notes.trim();
    this.dispatchEvent(new CustomEvent('save', { detail: deposit, bubbles: true, composed: true }));
    this._close();
  }

  private _close() {
    this.dispatchEvent(new CustomEvent('close', { bubbles: true, composed: true }));
  }

  render() {
    if (!this.isOpen) return nothing;

    return html`
      <div class="fixed inset-0 z-50 flex items-center justify-center bg-backdrop/80 backdrop-blur-sm" role="dialog" aria-modal="true" aria-labelledby="deposit-modal-title" data-testid="deposit-dialog">
        <div class="w-full max-w-md bg-zinc-900 border border-zinc-700 rounded-lg shadow-2xl p-6">
          <div class="mb-6">
            <h2 id="deposit-modal-title" class="text-xl font-bold text-zinc-100">Take deposit</h2>
            <p class="text-zinc-400 text-sm mt-1">Held on the order until it is invoiced</p>
          </div>

          ${this._error ? html`
            <div class="mb-4 p-3 bg-red-900/30 border border-red-800 text-red-200 rounded text-sm" role="alert" data-testid="deposit-error">${this._error}</div>
          ` : nothing}

          <form @submit=${this._handleSubmit} class="space-y-4">
            <div>
              <label class="block text-sm font-medium text-zinc-400 mb-1">Amount</label>
              <input type="number" required min="0.01" step="0.01" name="deposit-amount"
                .value=${String(this._amountDollars)}
                @input=${(e: InputEvent) => { this._amountDollars = parseFloat((e.target as HTMLInputElement).value) || 0; this._error = ''; }}
                class="w-full bg-zinc-950 border border-zinc-700 rounded px-3 py-2 text-zinc-100 focus:outline-none focus:ring-2 focus:ring-green-500 focus:border-transparent font-mono"
                />
              <p class="mt-1 text-xs text-zinc-500">Balance due after deposits: <span class="font-mono" data-testid="deposit-balance-due">${(this.balanceDueCents / 100).toFixed(2)}</span></p>
            </div>

            <div>
              <label class="block text-sm font-medium text-zinc-400 mb-1">Method</label>
              <gable-select
                .value=${this._method}
                .options=${DEPOSIT_METHODS.map(m => ({ value: m, label: METHOD_LABELS[m] }))}
                @change=${(e: Event) => this._method = (e.target as HTMLSelectElement).value as PaymentMethod}
              ></gable-select>
            </div>

            <div>
              <label class="block text-sm font-medium text-zinc-400 mb-1">Reference (Check # / Transaction ID)</label>
              <input type="text"
                .value=${this._reference}
                @input=${(e: InputEvent) => this._reference = (e.target as HTMLInputElement).value}
                class="w-full bg-zinc-950 border border-zinc-700 rounded px-3 py-2 text-zinc-100 focus:outline-none focus:ring-2 focus:ring-green-500 focus:border-transparent"
                placeholder="Optional"
                />
            </div>

            <div>
              <label class="block text-sm font-medium text-zinc-400 mb-1">Notes</label>
              <textarea
                .value=${this._notes}
                @input=${(e: InputEvent) => this._notes = (e.target as HTMLTextAreaElement).value}
                class="w-full bg-zinc-950 border border-zinc-700 rounded px-3 py-2 text-zinc-100 focus:outline-none focus:ring-2 focus:ring-green-500 focus:border-transparent"
                rows="2"
                placeholder="Optional"
              ></textarea>
            </div>

            <div class="mt-8 flex justify-end gap-3">
              <button type="button" @click=${this._close} class="px-4 py-2 text-sm text-zinc-300 hover:text-white transition-colors">Cancel</button>
              <button type="submit" class="px-4 py-2 bg-green-600 hover:bg-green-500 text-plate rounded text-sm font-medium transition-colors" data-testid="deposit-submit">
                Take deposit
              </button>
            </div>
          </form>
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-deposit-modal': GableDepositModal;
  }
}
