// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// No silent card (CP-05 A1): a register whose counter has no card terminal
// shows Card disabled with the server's own sentence, so the clerk takes
// cash, check or the account; a register with a terminal offers Card.

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
  printReceipt: vi.fn(),
  emailReceipt: vi.fn(),
  getTerminalIntent: vi.fn(),
  cancelTerminalIntent: vi.fn(),
}));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './POSTerminal';
import type { POSTerminal } from './POSTerminal';

const REG = (card_terminal: 'run' | 'simulator' | 'none') => ({
  id: 'REG-01', name: 'Front till', branch_id: 'b-main', branch_code: 'MAIN', branch_name: 'main yard', card_terminal,
});
const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement | null;

function sale(): Sale {
  return {
    id: 's1', number: 'S-1', revision: 3, branch_id: 'b', register_id: 'REG-01', cashier_id: 'c',
    customer_id: null, currency: 'USD', subtotal_cents: 8000, tax_cents: 650, total_cents: 8650,
    change_cents: 0, till_session_id: 't', status: 'open', invoice_id: null, completed_at: null,
    created_at: '', customer_po: null, job_id: null, tenders: [],
    lines: [{
      id: 'a', position: 1, line_type: 'product', parent_line_id: null, product_id: 'p-a',
      charge_code_id: null, charge_code: null, sku: 'SKU-a', description: 'Item a',
      quantity: '1', uom: 'EA', unit_price_ten_thousandths: 865000, line_total_cents: 8650,
    }],
  } as unknown as Sale;
}

async function mount(card: 'run' | 'simulator' | 'none'): Promise<POSTerminal> {
  svc.listRegisters.mockResolvedValue([REG(card)]);
  svc.currentTill.mockResolvedValue({ id: 't', register_id: 'REG-01', cashier_id: 'c', status: 'open', opening_float_cents: 20000 });
  svc.startSale.mockResolvedValue(sale());
  const el = document.createElement('gable-pos-terminal') as POSTerminal;
  document.body.appendChild(el);
  await new Promise((r) => setTimeout(r, 0));
  await el.updateComplete;
  return el;
}

describe('Card at a register with no card terminal', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('is disabled with the sentence, and cash, check and the account stay offered', async () => {
    const el = await mount('none');
    const card = q(el, 'tender-card') as HTMLButtonElement;
    expect(card.disabled).toBe(true);
    expect(card.title).toBe('This register has no card terminal: take cash, check or the account');
    expect(q(el, 'pos-card-refusal')?.textContent).toBe('This register has no card terminal: take cash, check or the account');
    expect((q(el, 'tender-cash') as HTMLButtonElement).disabled).toBe(false);
    expect((q(el, 'tender-check') as HTMLButtonElement).disabled).toBe(false);
    // a click that reaches it anyway chooses nothing and pays nothing
    card.disabled = false;
    card.click();
    await el.updateComplete;
    expect(card.getAttribute('aria-pressed')).toBe('false');
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });

  for (const state of ['run', 'simulator'] as const) {
    it(`is offered on a register whose card terminal is ${state}`, async () => {
      const el = await mount(state);
      expect((q(el, 'tender-card') as HTMLButtonElement).disabled).toBe(false);
      expect(q(el, 'pos-card-refusal')).toBeNull();
      el.remove();
    });
  }
});
