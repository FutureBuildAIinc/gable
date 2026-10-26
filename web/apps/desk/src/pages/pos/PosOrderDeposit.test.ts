// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's deposit on an order (CP-07): with an account customer
// attached, the open orders are listed, one is picked, and the deposit rides
// the payments route with the order named and no applications.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Order, OrderSummary } from '../../types/order';

const orders = vi.hoisted(() => ({
  listOrders: vi.fn(),
  getOrder: vi.fn(),
}));
const payments = vi.hoisted(() => ({ createPayment: vi.fn() }));

vi.mock('../../services/OrderService', async (orig) => {
  const real = await orig<typeof import('../../services/OrderService')>();
  return { ...real, OrderService: orders };
});
vi.mock('../../services/paymentService', async (orig) => {
  const real = await orig<typeof import('../../services/paymentService')>();
  return { ...real, paymentService: payments };
});

import './PosOrderDeposit';
import type { GablePosOrderDeposit } from './PosOrderDeposit';
import { mount, q, update } from '../../test/dom';

const summary = (over: Partial<OrderSummary> = {}): OrderSummary => ({
  id: 'ord-1',
  number: 'SO-000042',
  branch_id: 'b-reg',
  customer_id: 'cu1',
  customer_name: 'Ridgeview Framing',
  quote_id: null,
  job_id: null,
  status: 'confirmed',
  revision: 2,
  currency: 'USD',
  delivery_type: 'pickup',
  ship_to_id: null,
  customer_po: null,
  ordered_by_contact_id: null,
  salesperson_id: null,
  salesperson_name: null,
  scheduled_delivery_date: null,
  subtotal_cents: 77_760,
  tax_cents: 6_902,
  tax_rate_percent: '8.875',
  tax_exempt: false,
  tax_source: 'branch_rate',
  total_cents: 84_662,
  total_cost_cents: 54_720,
  total_margin_cents: 23_040,
  margin_percent: '29.63',
  total_commission_cents: 1_152,
  hold_reason: null,
  hold_note: null,
  confirmed_at: null,
  created_at: '2026-08-11T14:00:00Z',
  updated_at: '2026-08-11T15:00:00Z',
  invoice_ids: [],
  deposit_unapplied_cents: 0,
  ...over,
});

const detail = (over: Partial<Order> = {}): Order => ({
  ...summary(),
  ship_to: null,
  lines: [],
  deposit_total_cents: 0,
  balance_due_after_deposits_cents: 84_662,
  deposits: [],
  ...over,
} as Order);

async function open() {
  const el = await mount<GablePosOrderDeposit>('gable-pos-order-deposit', {
    isOpen: true,
    customerId: 'cu1',
    branchId: 'b-reg',
  });
  await update(el, {});
  return el;
}

async function pickFirst(el: GablePosOrderDeposit) {
  q(el, '[data-testid="pos-deposit-order"]').dispatchEvent(new Event('click', { bubbles: true }));
  await update(el, {});
}

async function typeAmount(root: ParentNode, dollars: string) {
  const input = q<HTMLInputElement>(root, 'input[name="deposit-amount"]');
  input.value = dollars;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  const dialog = root.querySelector('gable-deposit-modal') as HTMLElement & { updateComplete: Promise<boolean> };
  await dialog.updateComplete;
}

function submitDeposit(root: ParentNode) {
  const dialog = root.querySelector('gable-deposit-modal') as HTMLElement;
  q<HTMLFormElement>(dialog, 'form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
}

describe('gable-pos-order-deposit - the counter action', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    orders.listOrders.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    orders.getOrder.mockResolvedValue(detail());
    payments.createPayment.mockResolvedValue({});
  });

  it('lists the customer\'s open orders when opened', async () => {
    const el = await open();
    expect(orders.listOrders).toHaveBeenCalledWith({ customerId: 'cu1', status: ['draft', 'on_hold', 'confirmed', 'backordered'], limit: 50 });
    expect(el.querySelectorAll('[data-testid="pos-deposit-order"]').length).toBe(1);
    expect(q(el, '[data-testid="pos-deposit-order"]').textContent).toContain('SO-000042');
    el.remove();
  });

  it('says plainly when the customer has no open orders', async () => {
    orders.listOrders.mockResolvedValue({ items: [], next_cursor: null, limit: 50 });
    const el = await open();
    expect(q(el, '[data-testid="pos-deposit-no-orders"]').textContent).toContain('No open orders');
    el.remove();
  });

  it('takes a deposit on the picked order through the payments route, the order named and no applications', async () => {
    const el = await open();
    await pickFirst(el);
    await typeAmount(el, '400');
    submitDeposit(el);
    await update(el, {});
    expect(payments.createPayment).toHaveBeenCalledTimes(1);
    expect(payments.createPayment.mock.calls[0][0]).toEqual({
      customer_id: 'cu1',
      branch_id: 'b-reg',
      amount_cents: 40_000,
      method: 'cash',
      order_id: 'ord-1',
    });
    el.remove();
  });

  it('refuses an order of another branch in words, and books the deposit in the order\'s own branch', async () => {
    // the picked order belongs to another branch: refused, nothing posted
    orders.getOrder.mockResolvedValue(detail({ branch_id: 'b-other' }));
    const el = await open();
    await pickFirst(el);
    await update(el, {});
    expect(q(el, '[data-testid="pos-deposit-refusal"]').textContent).toContain('another branch');
    expect(payments.createPayment).not.toHaveBeenCalled();
    el.remove();

    // and with no register branch in context, the deposit still rides the
    // order\'s own branch
    orders.getOrder.mockResolvedValue(detail());
    const el2 = await mount<GablePosOrderDeposit>('gable-pos-order-deposit', {
      isOpen: true,
      customerId: 'cu1',
      branchId: '',
    });
    await update(el2, {});
    await pickFirst(el2);
    await typeAmount(el2, '10');
    submitDeposit(el2);
    await update(el2, {});
    expect(payments.createPayment.mock.calls[0][0].branch_id).toBe('b-reg');
    el2.remove();
  });

  it('refuses a deposit past the order\'s balance due after deposits without calling the route', async () => {
    const el = await open();
    await pickFirst(el);
    await typeAmount(el, '846.63');
    submitDeposit(el);
    await update(el, {});
    expect(payments.createPayment).not.toHaveBeenCalled();
    el.remove();
  });
});
