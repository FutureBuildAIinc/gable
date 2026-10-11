// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import type { CreatePaymentRequest, PaymentMethod } from '../../types/payment';

// The counter methods the payment route takes (a card charge is a gateway
// flow, and ACCOUNT is no longer a method: an account charge is an open
// invoice).
const PAYMENT_METHODS: PaymentMethod[] = ['cash', 'check', 'ach', 'other'];
const METHOD_LABELS: Record<string, string> = { cash: 'Cash', check: 'Check', ach: 'ACH', other: 'Other' };

@customElement('gable-payment-modal')
export class GablePaymentModal extends LitElement {
  createRenderRoot() { return this; }

  @property({ type: Boolean, attribute: 'is-open' }) isOpen = false;
  @property({ type: String, attribute: 'invoice-id' }) invoiceId = '';
  @property({ type: String, attribute: 'customer-id' }) customerId = '';
  @property({ type: Number, attribute: 'amount-due' }) amountDue = 0;

  @state() private _amountDollars = 0;
  @state() private _method: PaymentMethod = 'cash';
  @state() private _reference = '';
  @state() private _notes = '';
  @state() private _isSubmitting = false;
  @state() private _error = '';

  updated(changed: Map<string, unknown>) {
    if (changed.has('amountDue')) {
      this._amountDollars = this.amountDue / 100;
    }
  }

  private async _handleSubmit(e: Event) {
    e.preventDefault();
    this._isSubmitting = true;
    this._error = '';

    try {
      const amountCents = Math.round(this._amountDollars * 100);
      // The payment is recorded against the customer and applied to this
      // invoice in the same act; cash left over stays unapplied in 2200.
      const payment: CreatePaymentRequest = {
        customer_id: this.customerId,
        amount_cents: amountCents,
        method: this._method,
        applications: [{ invoice_id: this.invoiceId, amount_cents: Math.min(amountCents, this.amountDue) }],
      };
      if (this._reference.trim()) payment.reference = this._reference.trim();
      if (this._notes.trim()) payment.notes = this._notes.trim();
      this.dispatchEvent(new CustomEvent('save', { detail: payment, bubbles: true, composed: true }));
      this._close();
      this._amountDollars = 0;
      this._method = 'cash';
      this._reference = '';
      this._notes = '';
    } catch (err) {
      this._error = err instanceof Error ? err.message : 'Failed to record payment';
    } finally {
      this._isSubmitting = false;
    }
  }

  private _close() {
    this.dispatchEvent(new CustomEvent('close', { bubbles: true, composed: true }));
  }

  render() {
    if (!this.isOpen) return nothing;

    return html`
      <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-sm" role="dialog" aria-modal="true" aria-labelledby="payment-modal-title">
        <div class="w-full max-w-md bg-zinc-900 border border-zinc-700 rounded-lg shadow-2xl p-6">
          <div class="mb-6">
            <h2 id="payment-modal-title" class="text-xl font-bold text-zinc-100">Record Payment</h2>
            <p class="text-zinc-400 text-sm mt-1">Apply payment to Invoice</p>
          </div>

          ${this._error ? html`
            <div class="mb-4 p-3 bg-red-900/30 border border-red-800 text-red-200 rounded text-sm">${this._error}</div>
          ` : nothing}

          <form @submit=${this._handleSubmit} class="space-y-4">
            <div>
              <label class="block text-sm font-medium text-zinc-400 mb-1">Amount</label>
              <input type="number" required min="0.01" step="0.01"
                .value=${String(this._amountDollars)}
                @input=${(e: InputEvent) => this._amountDollars = parseFloat((e.target as HTMLInputElement).value)}
                class="w-full bg-zinc-950 border border-zinc-700 rounded px-3 py-2 text-zinc-100 focus:outline-none focus:ring-2 focus:ring-green-500 focus:border-transparent font-mono"
                />
              ${this._amountDollars * 100 > this.amountDue ? html`
                <p class="mt-1 text-xs text-amber-400">Overpayment: the remainder is held as unapplied cash on the account.</p>
              ` : nothing}
            </div>

            <div>
              <label class="block text-sm font-medium text-zinc-400 mb-1">Method</label>
              <select
                .value=${this._method}
                @change=${(e: Event) => this._method = (e.target as HTMLSelectElement).value as PaymentMethod}
                class="w-full bg-zinc-950 border border-zinc-700 rounded px-3 py-2 text-zinc-100 focus:outline-none focus:ring-2 focus:ring-green-500 focus:border-transparent"
              >
                ${PAYMENT_METHODS.map(m => html`<option value=${m}>${METHOD_LABELS[m]}</option>`)}
              </select>
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
              <button type="submit" ?disabled=${this._isSubmitting} class="px-4 py-2 bg-green-600 hover:bg-green-500 text-white rounded text-sm font-medium transition-colors disabled:opacity-50">
                ${this._isSubmitting ? 'Processing...' : 'Record Payment'}
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
    'gable-payment-modal': GablePaymentModal;
  }
}
