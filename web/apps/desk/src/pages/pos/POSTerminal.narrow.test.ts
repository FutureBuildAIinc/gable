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
    lines: [],
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
    expect(hasClass(button, 'w-11')).toBe(true);
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
