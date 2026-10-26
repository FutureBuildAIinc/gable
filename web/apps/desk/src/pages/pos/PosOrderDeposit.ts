// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import '../../components/ui/gable-select';
import '../../components/orders/DepositModal';
import { OrderService } from '../../services/OrderService';
import { paymentService } from '../../services/paymentService';
import { ToastService } from '../../lib/toast-service';
import { formatCents } from '../../lib/utils';
import type { Order, OrderSummary } from '../../types/order';
import type { CreatePaymentRequest } from '../../types/payment';

/**
 * The counter's deposit on an order (CP-07): with an account customer
 * attached at /pos, list their open orders and take a deposit on one through
 * the same payments route the order page uses (the order's customer, the
 * register's branch, the order named, no applications). Cash or check leaves
 * the drawer exactly as the order page's deposit does.
 */
@customElement('gable-pos-order-deposit')
export class GablePosOrderDeposit extends LitElement {
  createRenderRoot() { return this; }

  @property({ type: Boolean, attribute: 'is-open' }) isOpen = false;
  @property({ type: String, attribute: 'customer-id' }) customerId = '';
  @property({ type: String, attribute: 'branch-id' }) branchId = '';

  @state() private _orders: OrderSummary[] = [];
  @state() private _loading = false;
  @state() private _picked: Order | null = null;

  updated(changed: Map<string, unknown>) {
    if ((changed.has('isOpen') && this.isOpen) || (changed.has('customerId') && this.isOpen)) {
      this._picked = null;
      void this._loadOrders();
    }
  }

  private async _loadOrders() {
    if (!this.customerId) return;
    this._loading = true;
    try {
      const page = await OrderService.listOrders({
        customerId: this.customerId,
        status: ['draft', 'on_hold', 'confirmed', 'backordered'],
        limit: 50,
      });
      this._orders = page.items;
    } catch {
      ToastService.show('Failed to read the customer\'s orders', 'error');
      this._orders = [];
    } finally {
      this._loading = false;
    }
  }

  private async _pick(id: string) {
    this._picked = null;
    try {
      this._picked = await OrderService.getOrder(id);
    } catch {
      ToastService.show('Failed to read the order', 'error');
    }
  }

  private async _takeDeposit(deposit: CreatePaymentRequest) {
    try {
      await paymentService.createPayment(deposit);
      ToastService.show(`Deposit taken on ${this._picked?.number ?? 'the order'}`, 'success');
      // the order's balance shrank: the dialog closes and the list reloads
      // clean if the clerk takes another
      this._picked = null;
      this._close();
      void this._loadOrders();
    } catch (err) {
      ToastService.show(err instanceof Error ? err.message : 'Failed to take the deposit', 'error');
    }
  }

  private _close() {
    this.dispatchEvent(new CustomEvent('close', { bubbles: true, composed: true }));
  }

  render() {
    if (!this.isOpen) return nothing;
    return html`
      <div class="fixed inset-0 z-50 flex items-center justify-center bg-backdrop/80 backdrop-blur-sm" role="dialog" aria-modal="true" aria-labelledby="pos-deposit-title" data-testid="pos-deposit-dialog">
        <div class="w-full max-w-lg bg-zinc-900 border border-zinc-700 rounded-lg shadow-2xl p-6">
          ${this._picked ? html`
            <div class="mb-4">
              <h2 id="pos-deposit-title" class="text-xl font-bold text-zinc-100">Deposit on ${this._picked.number}</h2>
              <p class="text-zinc-400 text-sm mt-1">
                Total ${formatCents(this._picked.total_cents)} · balance due after deposits
                <span class="font-mono">${formatCents(this._picked.balance_due_after_deposits_cents)}</span>
              </p>
              <button type="button" @click=${() => { this._picked = null; }}
                class="mt-2 text-sm text-zinc-300 hover:text-white underline">Pick another order</button>
            </div>
            <gable-deposit-modal
              .isOpen=${true}
              .orderId=${this._picked.id}
              .customerId=${this.customerId}
              .branchId=${this.branchId || this._picked.branch_id}
              .balanceDueCents=${this._picked.balance_due_after_deposits_cents}
              @save=${(e: CustomEvent<CreatePaymentRequest>) => void this._takeDeposit(e.detail)}
              @close=${() => this._close()}
            ></gable-deposit-modal>
          ` : html`
            <div class="mb-4">
              <h2 id="pos-deposit-title" class="text-xl font-bold text-zinc-100">Deposit on an order</h2>
              <p class="text-zinc-400 text-sm mt-1">The customer's open orders</p>
            </div>
            ${this._loading ? html`<p class="text-sm text-zinc-400">Reading the orders...</p>` : this._orders.length === 0 ? html`
              <p class="text-sm text-zinc-500" data-testid="pos-deposit-no-orders">No open orders to take a deposit on</p>
            ` : html`
              <div class="rounded border border-white/10" data-testid="pos-deposit-orders">
                ${this._orders.map((o) => html`
                  <button type="button" data-testid="pos-deposit-order" data-order-id=${o.id}
                    @click=${() => void this._pick(o.id)}
                    class="block w-full border-b border-white/10 bg-slate-steel px-3 py-2 text-left last:border-b-0 hover:bg-white/5">
                    <span class="text-sm text-white font-mono">${o.number}</span>
                    <span class="ml-2 text-xs text-zinc-400">${o.status}</span>
                    <span class="float-right text-sm text-gable-green font-mono">${formatCents(o.total_cents)}</span>
                  </button>
                `)}
              </div>
            `}
            <div class="mt-6 flex justify-end">
              <button type="button" @click=${this._close} class="px-4 py-2 text-sm text-zinc-300 hover:text-white transition-colors">Close</button>
            </div>
          `}
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pos-order-deposit': GablePosOrderDeposit;
  }
}
