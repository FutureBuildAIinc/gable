// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter when the connection drops (ADR 0011 slice C2, section 5.3),
// against a stand in for the work shell's core: the open cart becomes the
// local cart, a completion whose answer was lost is result unknown and holds
// the till close, the queue holds it too, and on reconnect the queue drains
// before new prices and the unknown cart is read and followed. A browser tab
// has no core and keeps the online only refusal.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Sale } from '../../types/pos';
import { setOfflineInvoke } from '../../services/OfflineCounterService';
import { fakeOfflineCore, type FakeCore } from '../../test/fakeOfflineCore';

const svc = vi.hoisted(() => ({
  listRegisters: vi.fn(),
  currentTill: vi.fn(),
  startSale: vi.fn(),
  completeSale: vi.fn(),
  searchProducts: vi.fn(),
  findCustomers: vi.fn(),
  attachCustomer: vi.fn(),
  addItem: vi.fn(),
  removeItem: vi.fn(),
  changeQuantity: vi.fn(),
  getSale: vi.fn(),
  openTill: vi.fn(),
  tillReport: vi.fn(),
  closeTill: vi.fn(),
  printReceipt: vi.fn(),
  emailReceipt: vi.fn(),
  getTerminalIntent: vi.fn(),
  cancelTerminalIntent: vi.fn(),
  answerReplacedCart: vi.fn(),
}));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './POSTerminal';
import type { POSTerminal } from './POSTerminal';
import { OFFLINE_BANNER, QUEUE_HOLDS_CLOSE, RESULT_UNKNOWN, SAME_GOODS_QUESTION, UNKNOWN_HOLDS_CLOSE } from './offline';

const REG_01 = { id: 'REG-01', name: 'Front till', branch_id: 'b-main', branch_code: 'MAIN', branch_name: 'main yard' };
const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement | null;

const line = (id: string, cents: number) => ({
  id, position: 1, line_type: 'product', parent_line_id: null, product_id: `p-${id}`,
  charge_code_id: null, charge_code: null, sku: `SKU-${id}`, description: `Item ${id}`,
  quantity: '3', uom: 'EA', unit_price_ten_thousandths: cents * 100, line_total_cents: cents,
});

function sale(status = 'open'): Sale {
  return {
    id: 's1', number: 'POS-000101', revision: 3, branch_id: 'b', register_id: 'REG-01', cashier_id: 'c',
    customer_id: null, currency: 'USD', subtotal_cents: 8000, tax_cents: 650, total_cents: 8650,
    change_cents: 0, till_session_id: 't', status, invoice_id: null, completed_at: null,
    created_at: '', customer_po: null, job_id: null, tenders: [],
    lines: [line('a', 8650)],
  } as unknown as Sale;
}

let core: FakeCore;
let online = true;

const settle = async (el: POSTerminal) => {
  for (let i = 0; i < 6; i++) {
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
  }
};

async function mount(): Promise<POSTerminal> {
  svc.listRegisters.mockResolvedValue([REG_01]);
  svc.currentTill.mockResolvedValue({ id: 't', register_id: 'REG-01', cashier_id: 'c', status: 'open', opening_float_cents: 20000 });
  svc.startSale.mockResolvedValue(sale());
  const el = document.createElement('gable-pos-terminal') as POSTerminal;
  document.body.appendChild(el);
  await settle(el);
  return el;
}

function goOffline() {
  online = false;
  window.dispatchEvent(new Event('offline'));
}

function goOnline() {
  online = true;
  window.dispatchEvent(new Event('online'));
}

describe('the counter when the connection drops', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    online = true;
    vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => online);
    core = fakeOfflineCore();
    setOfflineInvoke(core.invoke);
  });
  afterEach(() => {
    setOfflineInvoke(undefined);
    vi.restoreAllMocks();
    document.body.innerHTML = '';
  });

  it('carries the open cart into the offline mode, with the banner and the waiting figures', async () => {
    const el = await mount();
    goOffline();
    await settle(el);
    expect(q(el, 'pos-offline-banner')?.textContent).toContain(OFFLINE_BANNER);
    expect(q(el, 'pos-offline-banner-figures')?.textContent).toContain('Room left');
    const offline = q(el, 'pos-offline-mode') as HTMLElement & { seed: { lines: unknown[]; replacesSaleId: string } };
    expect(offline).toBeTruthy();
    expect(offline.seed.replacesSaleId).toBe('s1');
    expect(offline.seed.lines).toEqual([{ id: expect.any(String), product_id: 'p-a', quantity: '3', uom: 'EA' }]);
    expect(q(el, 'pos-pay-bar')).toBeNull();
    el.remove();
  });

  it('holds a cart whose completion answer was lost as result unknown, carries nothing and holds the close', async () => {
    const el = await mount();
    svc.completeSale.mockImplementation(async () => { online = false; throw new TypeError('Failed to fetch'); });
    q(el, 'tender-cash')?.click();
    await el.updateComplete;
    (q(el, 'pos-pay') as HTMLButtonElement).click();
    await settle(el);
    expect(el.textContent).toContain(RESULT_UNKNOWN);
    expect(localStorage.getItem('gable.pos.offline.unknown-cart.REG-01')).toBe('s1');
    const offline = q(el, 'pos-offline-mode') as HTMLElement & { seed: unknown; unknownCartId: string };
    expect(offline.seed).toBeNull();
    expect(offline.unknownCartId).toBe('s1');
    await (el as unknown as { _beginCloseTill(): Promise<void> })._beginCloseTill();
    await settle(el);
    expect(el.textContent).toContain(UNKNOWN_HOLDS_CLOSE);
    expect(svc.tillReport).not.toHaveBeenCalled();
    el.remove();
  });

  it('holds the till close while the queue has sales the server has not answered', async () => {
    const el = await mount();
    core.queue = { ...core.queue, waiting_count: 1, items: [{ device_seq: 1, state: 'waiting', reason_code: null, reason_message: null, invoice_id: null, invoice_number: null, sale: {} as never }] };
    await (el as unknown as { _beginCloseTill(): Promise<void> })._beginCloseTill();
    await settle(el);
    expect(el.textContent).toContain(QUEUE_HOLDS_CLOSE);
    expect(svc.tillReport).not.toHaveBeenCalled();
    el.remove();
  });

  it('drains the queue before it takes new prices on reconnect, and says what was sent', async () => {
    const el = await mount();
    goOffline();
    await settle(el);
    core.calls.length = 0;
    core.sync = () => ({ sent: 2, posted: 1, refused: 1, held: 0, resolved: 0, errors: 0, confirmed_seq: 2, open_exception_count: 1, queue: core.queue });
    goOnline();
    await settle(el);
    const order = core.calls.map((c) => c.command).filter((c) => c === 'sync_offline_queue' || c === 'refresh_offline_snapshot');
    expect(order).toEqual(['sync_offline_queue', 'refresh_offline_snapshot']);
    expect(q(el, 'pos-offline-strip')?.textContent).toContain('Sent 1, 1 needs a manager');
    expect(q(el, 'pos-offline-mode')).toBeNull();
    el.remove();
  });

  it('reads the unknown cart on reconnect: abandoned by a linked sale, the clerk is asked and the answer recorded', async () => {
    localStorage.setItem('gable.pos.offline.unknown-cart.REG-01', 's1');
    svc.getSale.mockResolvedValue(sale('abandoned'));
    core.queue = { ...core.queue, items: [{ device_seq: 1, state: 'synced', reason_code: null, reason_message: null, invoice_id: null, invoice_number: 'IN-1',
      sale: { client_id: 'off-1', offline_ref: 'REG-01-2-000001', replaces_sale_id: 's1' } as never }] };
    const el = await mount();
    await settle(el);
    expect(svc.getSale).toHaveBeenCalledWith('s1');
    expect(q(el, 'pos-offline-same-goods')?.textContent).toContain(SAME_GOODS_QUESTION);
    expect(q(el, 'pos-offline-same-goods')?.textContent).toContain('REG-01-2-000001');
    svc.answerReplacedCart.mockResolvedValue(undefined);
    q(el, 'pos-offline-same-goods-no')?.click();
    await settle(el);
    expect(svc.answerReplacedCart).toHaveBeenCalledWith('off-1', false);
    expect(localStorage.getItem('gable.pos.offline.unknown-cart.REG-01')).toBeNull();
    el.remove();
  });

  it('shows the unknown cart complete when it committed online', async () => {
    localStorage.setItem('gable.pos.offline.unknown-cart.REG-01', 's1');
    svc.getSale.mockResolvedValue(sale('completed'));
    const el = await mount();
    await settle(el);
    expect(el.textContent).toContain('completed online: POS-000101');
    expect(localStorage.getItem('gable.pos.offline.unknown-cart.REG-01')).toBeNull();
    el.remove();
  });

  it('keeps the online only refusal in a browser tab, which has no core', async () => {
    setOfflineInvoke(null);
    const el = await mount();
    goOffline();
    await settle(el);
    expect(q(el, 'pos-offline-mode')).toBeNull();
    q(el, 'tender-cash')?.click();
    await el.updateComplete;
    (q(el, 'pos-pay') as HTMLButtonElement).click();
    await settle(el);
    expect(el.textContent).toContain('money never queues');
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });
});
