// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's narrow (touch first) chrome: the header never forces the
// document wider than the viewport, the till controls live behind a Till
// menu button below 900, the cashier name leaves the bar below 900, and the
// hotkey legend renders at 900 and wider only (the bindings stay bound at
// every width; the legend is chrome, the key handler is not).

import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Sale } from '../../types/pos';

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
  recordMovement: vi.fn(),
  printReceipt: vi.fn(),
  emailReceipt: vi.fn(),
}));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './POSTerminal';
import type { POSTerminal } from './POSTerminal';
import type { CounterRegister } from '../../types/pos';

const REG_01: CounterRegister = {
  id: 'REG-01', name: 'Front till', branch_id: 'b-main', branch_code: 'MAIN', branch_name: 'main yard',
};
const OPEN_TILL = { id: 't', register_id: 'REG-01', cashier_id: 'c', status: 'open', opening_float_cents: 20000 };

function sale(over: Partial<Sale> = {}): Sale {
  return {
    id: 's1', number: 'S-1', revision: 3, branch_id: 'b', register_id: 'REG-01', cashier_id: 'c',
    customer_id: null, currency: 'USD', subtotal_cents: 8000, tax_cents: 650, total_cents: 8650,
    change_cents: 0, till_session_id: 't', status: 'open', invoice_id: null, completed_at: null,
    created_at: '', customer_po: null, job_id: null, tenders: [],
    lines: [{
      id: 'a', position: 1, line_type: 'product', parent_line_id: null, product_id: 'p-a',
      charge_code_id: null, charge_code: null, sku: 'SKU-A', description: 'Item A',
      quantity: '1', uom: 'EA', unit_price_ten_thousandths: 500000, line_total_cents: 5000,
    }],
    ...over,
  } as unknown as Sale;
}

async function mount(s: Sale): Promise<POSTerminal> {
  svc.listRegisters.mockResolvedValue([REG_01]);
  svc.currentTill.mockResolvedValue(OPEN_TILL);
  svc.startSale.mockResolvedValue(s);
  const el = document.createElement('gable-pos-terminal') as POSTerminal;
  document.body.appendChild(el);
  await new Promise((r) => setTimeout(r, 0));
  await el.updateComplete;
  return el;
}
const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;
const hasClass = (el: Element, cls: string) => el.className.split(/\s+/).includes(cls);

describe('the counter below 900: the header fits a phone', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('gates the hotkey legend to 900 and wider (the chords stay bound)', async () => {
    const el = await mount(sale());
    const legend = q(el, 'pos-hotkey-legend');
    expect(legend).toBeTruthy();
    // Below 900 the legend is display:none; the class pair says so.
    expect(hasClass(legend, 'hidden')).toBe(true);
    expect(hasClass(legend, 'min-[900px]:flex')).toBe(true);
    el.remove();
  });

  it('lets the header right group shrink and hides the cashier name below 900', async () => {
    const el = await mount(sale());
    const header = q(el, 'pos-header');
    const right = header.querySelector('[data-testid="pos-till-state"]')?.parentElement as HTMLElement;
    expect(hasClass(right, 'shrink-0')).toBe(false);
    expect(hasClass(right, 'min-w-0')).toBe(true);
    const cashier = q(el, 'pos-cashier');
    expect(hasClass(cashier, 'hidden')).toBe(true);
    expect(hasClass(cashier, 'min-[900px]:inline')).toBe(true);
    el.remove();
  });

  it('holds the till controls behind a Till menu button with 44px targets', async () => {
    const el = await mount(sale());
    const button = q(el, 'pos-till-menu-button');
    expect(button).toBeTruthy();
    expect(hasClass(button, 'min-[900px]:hidden')).toBe(true);
    expect(hasClass(button, 'h-11')).toBe(true);
    // a visible label beside the card icon: the button is no longer a bare icon
    expect(q(el, 'pos-till-menu-label')?.textContent?.trim()).toBe('Till');
    expect(button.getAttribute('aria-expanded')).toBe('false');

    button.click();
    await el.updateComplete;
    expect(button.getAttribute('aria-expanded')).toBe('true');
    const items = q(el, 'pos-till-menu-items');
    expect(items).toBeTruthy();
    for (const id of ['pos-drawer-cash', 'pos-close-till']) {
      const item = q(el, id);
      expect(item, id).toBeTruthy();
      expect(hasClass(item, 'min-h-11'), `${id} is 44px tall`).toBe(true);
    }

    // Drawer Cash opens the movement dialog and closes the menu.
    q(el, 'pos-drawer-cash').click();
    await el.updateComplete;
    expect(q(el, 'pos-till-menu-items')).toBeFalsy();
    expect(el.querySelector('gable-till-movement-dialog')).toBeTruthy();
    el.remove();
  });

  it('keeps New Sale as the primary action beside the Till menu', async () => {
    const el = await mount(sale());
    const right = q(el, 'pos-till-state').parentElement as HTMLElement;
    const newSale = [...right.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'New Sale');
    expect(newSale).toBeTruthy();
    el.remove();
  });
});

describe('the counter below 768: the touch first working order', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('pins a pay bar under the body with the total and the Pay action', async () => {
    const el = await mount(sale());
    const bar = q(el, 'pos-pay-bar');
    expect(bar).toBeTruthy();
    expect(bar.className).toContain('min-[768px]:hidden');
    expect(bar.textContent).toContain('$86.50');
    const pay = bar.querySelector('[data-testid="pos-pay-bar-button"]') as HTMLButtonElement;
    expect(pay).toBeTruthy();
    expect(pay.className).toContain('min-h-14');
    el.remove();
  });

  it('spans the fifth tender (store credit) across both columns below 768', async () => {
    const el = await mount(sale());
    (el as unknown as { _standing: unknown })._standing = {
      customer_id: 'cu1', name: 'Test Builders', currency: 'USD', credit_limit_cents: null,
      open_receivable_cents: 0, open_credit_memo_cents: 5000, available_credit_cents: null,
      on_hold: false, terms_kind: 'due_on_receipt', named_buyer_required: false, buyers: [],
      check_limit_cents: null, payment_options: [],
      on_account: { verdict: 'allowed', reason: null, message: '', over_cents: null, ways_forward: [] },
    };
    await el.updateComplete;
    const credit = q(el, 'tender-store_credit');
    expect(credit).toBeTruthy();
    expect(credit.className).toContain('col-span-2');
    expect(credit.className).toContain('min-[768px]:col-span-1');
    el.remove();
  });
});

describe('the money guard at the yard (offline)', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('refuses Pay with the field shell\'s own words when the connection is gone, and completes nothing', async () => {
    const el = await mount(sale());
    (el as unknown as { _online: boolean })._online = false;
    await chooseCashLike(el);
    q(el, 'pos-pay').click();
    await el.updateComplete;
    expect(el.querySelector('[role="alert"]')?.textContent).toContain('money never queues');
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });

  it('keeps the guard on the pinned pay bar too', async () => {
    const el = await mount(sale());
    (el as unknown as { _online: boolean })._online = false;
    await chooseCashLike(el);
    q(el, 'pos-pay-bar-button').click();
    await el.updateComplete;
    expect(el.querySelector('[role="alert"]')?.textContent).toContain('money never queues');
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });

  async function chooseCashLike(target: POSTerminal) {
    (target as unknown as { _tenderMethod: string })._tenderMethod = 'cash';
    await target.updateComplete;
  }
});
