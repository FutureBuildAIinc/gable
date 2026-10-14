// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Sale, CounterCustomer } from '../../types/pos';

const svc = vi.hoisted(() => ({
  currentTill: vi.fn(),
  startSale: vi.fn(),
  completeSale: vi.fn(),
  searchProducts: vi.fn(),
  findCustomers: vi.fn(),
  attachCustomer: vi.fn(),
  addItem: vi.fn(),
  removeItem: vi.fn(),
}));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './POSTerminal';
import type { POSTerminal } from './POSTerminal';

const line = (id: string, cents: number) => ({
  id, position: 1, line_type: 'product', parent_line_id: null, product_id: `p-${id}`,
  charge_code_id: null, charge_code: null, sku: `SKU-${id}`, description: `Item ${id}`,
  quantity: '1', uom: 'EA', unit_price_ten_thousandths: cents * 100, line_total_cents: cents,
});

function sale(over: Partial<Sale> = {}): Sale {
  return {
    id: 's1', number: 'S-1', revision: 3, branch_id: 'b', register_id: 'REG-01', cashier_id: 'c',
    customer_id: null, currency: 'USD', subtotal_cents: 8000, tax_cents: 650, total_cents: 8650,
    change_cents: 0, till_session_id: 't', status: 'open', invoice_id: null, completed_at: null,
    created_at: '', customer_po: null, job_id: null, tenders: [],
    lines: [line('a', 5000), line('b', 3000)], ...over,
  } as unknown as Sale;
}

const account: CounterCustomer = {
  id: 'cu1', name: 'Test Builders', account_number: 'A-100', standing: 'good',
  credit_limit_cents: 500000, available_credit_cents: 400000, open_receivable_cents: 100000,
  po_required: false, jobs: [],
} as unknown as CounterCustomer;

async function mount(s: Sale): Promise<POSTerminal> {
  svc.currentTill.mockResolvedValue({ id: 't', register_id: 'REG-01', cashier_id: 'c', status: 'open', opening_float_cents: 20000 });
  svc.startSale.mockResolvedValue(s);
  const el = document.createElement('gable-pos-terminal') as POSTerminal;
  document.body.appendChild(el);
  await new Promise((r) => setTimeout(r, 0));
  await el.updateComplete;
  return el;
}
const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;
const typeInto = async (el: POSTerminal, v: string) => {
  const input = el.querySelector('#pos-tender-amount') as HTMLInputElement;
  input.value = v;
  input.dispatchEvent(new Event('input'));
  await el.updateComplete;
};

describe('the counter as the Panels layout', () => {
  beforeEach(() => { vi.clearAllMocks(); });

  it('shows the three panels and Pay as the one primary action carrying the summary total', async () => {
    const el = await mount(sale());
    expect(q(el, 'panel-customer')).toBeTruthy();
    expect(q(el, 'panel-payment')).toBeTruthy();
    expect(q(el, 'panel-summary')).toBeTruthy();
    for (const n of ['customer', 'payment', 'summary']) expect(q(el, `progress-${n}`)).toBeTruthy();
    const pay = q(el, 'pos-pay');
    expect(el.querySelectorAll('[data-testid="pos-pay"]').length).toBe(1);
    expect(q(el, 'summary-total').textContent?.trim()).toBe('$86.50');
    expect(pay.textContent).toContain('$86.50');
    expect(q(el, 'pos-till-state').textContent).toContain('Till open');
  });

  it('pays in cash with the tendered amount, sending one cash tender on the sale revision', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed', change_cents: 1350 }));
    const el = await mount(sale());
    await typeInto(el, '100.00');
    expect(q(el, 'pos-change').textContent).toContain('$13.50');
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [{ method: 'cash', amount_cents: 10000 }]);
  });

  it('takes a split between two tenders as the added tender plus the entry', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await typeInto(el, '50.00');
    q(el, 'pos-add-tender').click();
    await el.updateComplete;
    q(el, 'tender-card').click();
    await el.updateComplete;
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [
      { method: 'cash', amount_cents: 5000 },
      { method: 'card', amount_cents: 3650 },
    ]);
  });

  it('charges the on account tender for the whole total once a customer with credit is named', async () => {
    svc.attachCustomer.mockResolvedValue(sale({ customer_id: 'cu1', lines: [] }));
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale({ lines: [] }));
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(true);
    (el as unknown as { _customer: CounterCustomer })._customer = account;
    (el as unknown as { _sale: Sale })._sale = sale({ customer_id: 'cu1' });
    await el.updateComplete;
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(false);
    q(el, 'tender-account').click();
    await el.updateComplete;
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [{ method: 'account', amount_cents: 8650 }]);
  });

  it('keeps Pay off while the sale has no lines or the tenders fall short', async () => {
    const el = await mount(sale({ lines: [], subtotal_cents: 0, tax_cents: 0, total_cents: 0 }));
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    (el as unknown as { _sale: Sale })._sale = sale();
    await el.updateComplete;
    await typeInto(el, '10.00');
    const pay = q(el, 'pos-pay') as HTMLButtonElement;
    expect(pay.disabled).toBe(true);
    expect(pay.textContent).toContain('Still owed $76.50');
    expect(svc.completeSale).not.toHaveBeenCalled();
  });
});
