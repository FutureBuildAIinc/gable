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

  it('sends only the corrected entry after a refused Pay, never the first attempt again', async () => {
    svc.completeSale
      .mockRejectedValueOnce(new Error('only a cash tender can give change'))
      .mockResolvedValueOnce(sale({ status: 'completed' }));
    const el = await mount(sale());
    q(el, 'tender-card').click();
    await el.updateComplete;
    await typeInto(el, '100.00');
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.completeSale).toHaveBeenCalledTimes(1);
    expect(svc.completeSale).toHaveBeenLastCalledWith('s1', 3, [{ method: 'card', amount_cents: 10000 }]);
    await typeInto(el, '86.50');
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    expect(svc.completeSale).toHaveBeenCalledTimes(2);
    expect(svc.completeSale).toHaveBeenLastCalledWith('s1', 3, [{ method: 'card', amount_cents: 8650 }]);
  });

  describe('the account refusal guards', () => {
    type Inner = { _customer: CounterCustomer | null; _sale: Sale; _poText: string; _tenderMethod: string; _chooseMethod(m: string): void };
    const inner = (el: POSTerminal) => el as unknown as Inner;
    const withCustomer = async (c: CounterCustomer, po = ''): Promise<POSTerminal> => {
      const el = await mount(sale({ customer_id: c.id }));
      inner(el)._customer = c;
      inner(el)._poText = po;
      await el.updateComplete;
      return el;
    };
    const onHold = { ...account, standing: 'no_credit', available_credit_cents: 0 } as unknown as CounterCustomer;
    const overLimit = { ...account, standing: 'over_limit', available_credit_cents: -2000 } as unknown as CounterCustomer;
    const needsPO = { ...account, po_required: true } as unknown as CounterCustomer;
    const pay = async (el: POSTerminal) => {
      q(el, 'pos-pay').click();
      await new Promise((r) => setTimeout(r, 0));
      await el.updateComplete;
    };

    for (const [name, c] of [['on hold', onHold], ['over the limit', overLimit], ['asking for a PO that is empty', needsPO]] as const) {
      it(`refuses the choice of the account for a customer ${name}`, async () => {
        const el = await withCustomer(c);
        expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(true);
        inner(el)._chooseMethod('account');
        await el.updateComplete;
        expect(inner(el)._tenderMethod).toBe('cash');
        expect(el.querySelector('[role="alert"]')).toBeTruthy();
        expect(svc.completeSale).not.toHaveBeenCalled();
      });

      it(`refuses Pay on account for a customer ${name} and never completes the sale`, async () => {
        const el = await withCustomer(c);
        inner(el)._tenderMethod = 'account';
        await el.updateComplete;
        await pay(el);
        expect(el.querySelector('[role="alert"]')).toBeTruthy();
        expect(svc.completeSale).not.toHaveBeenCalled();
      });
    }

    it('rechecks at Pay when the customer goes on hold after the account was chosen', async () => {
      const el = await withCustomer(account);
      q(el, 'tender-account').click();
      await el.updateComplete;
      expect(inner(el)._tenderMethod).toBe('account');
      inner(el)._customer = onHold;
      await el.updateComplete;
      await pay(el);
      expect(el.querySelector('[role="alert"]')?.textContent).toContain('no credit');
      expect(svc.completeSale).not.toHaveBeenCalled();
    });

    it('rechecks at Pay when the PO is cleared after the account was chosen', async () => {
      svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
      const el = await withCustomer(needsPO, 'PO-7');
      q(el, 'tender-account').click();
      await el.updateComplete;
      expect(inner(el)._tenderMethod).toBe('account');
      inner(el)._poText = '';
      await el.updateComplete;
      await pay(el);
      expect(el.querySelector('[role="alert"]')?.textContent).toContain('purchase order');
      expect(svc.completeSale).not.toHaveBeenCalled();
      inner(el)._poText = 'PO-7';
      await el.updateComplete;
      await pay(el);
      expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [{ method: 'account', amount_cents: 8650 }]);
    });
    it('rechecks at Pay an account tender already added to the list, after the PO is cleared', async () => {
      const el = await withCustomer(needsPO, 'PO-7');
      q(el, 'tender-account').click();
      await el.updateComplete;
      // The account tender goes onto the added list in full, as a split would.
      (el as unknown as { _pendingTenders: { method: string; amount_cents: number }[] })._pendingTenders = [{ method: 'account', amount_cents: 8650 }];
      inner(el)._poText = '';
      await el.updateComplete;
      await pay(el);
      expect(el.querySelector('[role="alert"]')?.textContent).toContain('purchase order');
      expect(svc.completeSale).not.toHaveBeenCalled();
    });
  });

  describe('a quantity on a line', () => {
    const result = { product_id: 'p-2x4', sku: '2X4-8', description: '2x4 8ft', uom: 'EA', unit_price_cents: 350, in_stock: 100 };
    type Inner = { _searchResults: unknown[]; _searchQuery: string };
    const setResults = async (el: POSTerminal) => {
      (el as unknown as Inner)._searchResults = [result];
      await el.updateComplete;
    };
    const setQty = async (el: POSTerminal, v: string) => {
      const input = el.querySelector('#pos-qty') as HTMLInputElement;
      input.value = v;
      input.dispatchEvent(new Event('input'));
      await el.updateComplete;
    };
    const flush = async (el: POSTerminal) => { await new Promise((r) => setTimeout(r, 0)); await el.updateComplete; };

    it('adds a result with the typed quantity as one line with its total', async () => {
      const twelve = { ...line('x', 4200), quantity: '12', unit_price_ten_thousandths: 35000 };
      svc.addItem.mockResolvedValue(sale({ lines: [twelve as never], subtotal_cents: 4200, total_cents: 4200 }));
      const el = await mount(sale({ lines: [] }));
      await setResults(el);
      await setQty(el, '12');
      (el.querySelector('[data-testid="pos-results"] button') as HTMLElement).click();
      await flush(el);
      expect(svc.addItem).toHaveBeenCalledWith('s1', 'p-2x4', '12', 'EA');
      expect(el.querySelectorAll('[data-testid="pos-lines"] tbody tr').length).toBe(1);
      expect(el.querySelector('[data-testid="pos-lines"]')?.textContent).toContain('$42.00');
    });

    it('allows a decimal quantity and defaults to one', async () => {
      svc.addItem.mockResolvedValue(sale());
      const el = await mount(sale({ lines: [] }));
      expect((el.querySelector('#pos-qty') as HTMLInputElement).value).toBe('1');
      await setResults(el);
      await setQty(el, '2.5');
      (el.querySelector('[data-testid="pos-results"] button') as HTMLElement).click();
      await flush(el);
      expect(svc.addItem).toHaveBeenCalledWith('s1', 'p-2x4', '2.5', 'EA');
    });

    it('refuses a zero or negative quantity and adds nothing', async () => {
      const el = await mount(sale({ lines: [] }));
      for (const v of ['0', '-3', '']) {
        await setResults(el);
        await setQty(el, v);
        (el.querySelector('[data-testid="pos-results"] button') as HTMLElement).click();
        await flush(el);
        expect(el.querySelector('[role="alert"]')?.textContent).toContain('quantity');
      }
      expect(svc.addItem).not.toHaveBeenCalled();
    });

    it('adds the highlighted result when Enter is pressed in the quantity field', async () => {
      svc.addItem.mockResolvedValue(sale());
      const el = await mount(sale({ lines: [] }));
      await setResults(el);
      await setQty(el, '4');
      (el.querySelector('#pos-qty') as HTMLInputElement).dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }));
      await flush(el);
      expect(svc.addItem).toHaveBeenCalledWith('s1', 'p-2x4', '4', 'EA');
    });
  });

  it('shows a unit price at the wire precision and its line total to the cent', async () => {
    const odd = { ...line('o', 1658), unit_price_ten_thousandths: 165750 };
    const whole = { ...line('w', 500), unit_price_ten_thousandths: 50000 };
    const el = await mount(sale({ lines: [odd as never, whole as never] }));
    const text = el.querySelector('[data-testid="pos-lines"]')?.textContent ?? '';
    expect(text).toContain('$16.575');
    expect(text).toContain('$16.58');
    expect(text).toContain('$5.00');
    expect(text).not.toContain('$5.0000');
  });

  it('tells the cashier to name the customer first, from the start of an empty sale', async () => {
    const el = await mount(sale({ lines: [] }));
    expect(q(el, 'panel-customer').textContent).toContain('Name the customer before the first line');
  });

  it('words the account tender in sentence case', async () => {
    const el = await mount(sale());
    expect(q(el, 'tender-account').textContent?.trim()).toBe('Charge to account');
    expect(q(el, 'tender-account').className).not.toContain('capitalize');
  });
});
