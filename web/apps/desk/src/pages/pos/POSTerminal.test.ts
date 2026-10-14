// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Sale, CounterCustomer } from '../../types/pos';

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
}));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './POSTerminal';
import type { POSTerminal } from './POSTerminal';
import { branchContext } from '../../services/BranchContext';
import type { CounterRegister } from '../../types/pos';

const reg = (id: string, branch: string, name = 'Front till'): CounterRegister => ({
  id, name, branch_id: `b-${branch}`, branch_code: branch.toUpperCase(), branch_name: `${branch} yard`,
});
const REG_01 = reg('REG-01', 'main');

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
  svc.listRegisters.mockResolvedValue([REG_01]);
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

    it('allows a decimal quantity for a length unit and defaults to one', async () => {
      svc.addItem.mockResolvedValue(sale());
      const el = await mount(sale({ lines: [] }));
      expect((el.querySelector('#pos-qty') as HTMLInputElement).value).toBe('1');
      (el as unknown as Inner)._searchResults = [{ ...result, product_id: 'p-trim', sku: 'TRIM-LF', uom: 'LF' }];
      await el.updateComplete;
      await setQty(el, '2.5');
      (el.querySelector('[data-testid="pos-results"] button') as HTMLElement).click();
      await flush(el);
      expect(svc.addItem).toHaveBeenCalledWith('s1', 'p-trim', '2.5', 'LF');
    });

    it('refuses a decimal quantity for a unit counted whole and sends nothing', async () => {
      const el = await mount(sale({ lines: [] }));
      await setResults(el);
      await setQty(el, '2.5');
      (el.querySelector('[data-testid="pos-results"] button') as HTMLElement).click();
      await flush(el);
      expect(el.querySelector('[role="alert"]')?.textContent).toContain('EA is counted in whole units');
      expect(svc.addItem).not.toHaveBeenCalled();
    });

    it('adds a whole quantity of a unit counted whole, written with a zero fraction', async () => {
      svc.addItem.mockResolvedValue(sale());
      const el = await mount(sale({ lines: [] }));
      await setResults(el);
      await setQty(el, '3.0');
      (el.querySelector('[data-testid="pos-results"] button') as HTMLElement).click();
      await flush(el);
      expect(svc.addItem).toHaveBeenCalledWith('s1', 'p-2x4', '3', 'EA');
    });

    it('on Enter right after typing, adds a result of the current query, never the earlier one', async () => {
      svc.addItem.mockResolvedValue(sale());
      const ten = { ...result, product_id: 'p-10', sku: '2X4-10' };
      // The search answers "2X4-8" with the longer board first and the exact SKU second.
      svc.searchProducts.mockResolvedValue([ten, result]);
      const el = await mount(sale({ lines: [] }));
      (el as unknown as Inner)._searchResults = [ten, result];
      await el.updateComplete;
      const input = el.querySelector('#pos-search-input') as HTMLInputElement;
      input.value = '2X4-8';
      input.dispatchEvent(new Event('input'));
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }));
      await flush(el);
      expect(svc.searchProducts).toHaveBeenCalledWith('2X4-8');
      expect(svc.addItem).toHaveBeenCalledTimes(1);
      expect(svc.addItem).toHaveBeenCalledWith('s1', 'p-2x4', '1', 'EA');
    });

    it('on Enter with no exact SKU match, adds the top result of the current search', async () => {
      svc.addItem.mockResolvedValue(sale());
      const board = { ...result, product_id: 'p-board', sku: 'STUD-92' };
      svc.searchProducts.mockResolvedValue([board]);
      const el = await mount(sale({ lines: [] }));
      (el as unknown as Inner)._searchResults = [result];
      await el.updateComplete;
      const input = el.querySelector('#pos-search-input') as HTMLInputElement;
      input.value = 'stud';
      input.dispatchEvent(new Event('input'));
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }));
      await flush(el);
      expect(svc.addItem).toHaveBeenCalledTimes(1);
      expect(svc.addItem).toHaveBeenCalledWith('s1', 'p-board', '1', 'EA');
    });

    it('drops a search answer that arrives after a newer query', async () => {
      let late: (v: unknown) => void = () => {};
      svc.searchProducts
        .mockImplementationOnce(() => new Promise((r) => { late = r; }))
        .mockResolvedValueOnce([result]);
      const el = await mount(sale({ lines: [] }));
      const input = el.querySelector('#pos-search-input') as HTMLInputElement;
      input.value = 'old';
      input.dispatchEvent(new Event('input'));
      await el.updateComplete;
      await new Promise((r) => setTimeout(r, 250));
      input.value = '2X4-8';
      input.dispatchEvent(new Event('input'));
      await el.updateComplete;
      await new Promise((r) => setTimeout(r, 250));
      late([{ ...result, product_id: 'p-old', sku: 'OLD-1' }]);
      await flush(el);
      const shown = el.querySelector('[data-testid="pos-results"]')?.textContent ?? '';
      expect(shown).toContain('2X4-8');
      expect(shown).not.toContain('OLD-1');
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

  it('does not start a second sale over the one the cashier began after a completion', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await typeInto(el, '100.00');
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    expect(svc.startSale).toHaveBeenCalledTimes(1);
    // The cashier starts the next sale at once; the completion's own timer must not start another.
    const newSale = [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'New Sale') as HTMLElement;
    newSale.click();
    await new Promise((r) => setTimeout(r, 2300));
    expect(svc.startSale).toHaveBeenCalledTimes(2);
  });

  describe('the visual review', () => {
    const filled = (el: Element) => [...el.querySelectorAll('button')].filter((b) => b.className.includes('bg-gable-green'));

    it('before a till is open, Pay is a disabled neutral button and Open Till the one primary', async () => {
      const el = await mount(sale());
      (el as unknown as { _till: unknown })._till = null;
      await el.updateComplete;
      const pay = q(el, 'pos-pay') as HTMLButtonElement;
      expect(pay.disabled).toBe(true);
      expect(pay.className).not.toContain('bg-gable-green');
      const primaries = filled(el);
      expect(primaries.length).toBe(1);
      expect(primaries[0].textContent?.trim()).toBe('Open Till');
    });

    it('with a till open and a payable sale, Pay is the one primary', async () => {
      const el = await mount(sale());
      const primaries = filled(el);
      expect(primaries.length).toBe(1);
      expect(primaries[0]).toBe(q(el, 'pos-pay'));
      expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    });

    it('takes the tendered amount as text, with no spinner, and shows it with two decimals on leaving', async () => {
      const el = await mount(sale());
      const input = el.querySelector('#pos-tender-amount') as HTMLInputElement;
      expect(input.type).toBe('text');
      expect(input.inputMode).toBe('decimal');
      await typeInto(el, '150.5');
      input.dispatchEvent(new Event('blur'));
      await el.updateComplete;
      expect(input.value).toBe('150.50');
    });

    it('keeps Void and Return and Pay in the foot of the right column, outside its scrolling area', async () => {
      const el = await mount(sale());
      const foot = q(el, 'pos-panel-foot');
      expect(foot.contains(q(el, 'pos-pay'))).toBe(true);
      expect(foot.textContent).toContain('Void');
      expect(foot.textContent).toContain('Return');
      expect(foot.contains(q(el, 'panel-payment'))).toBe(false);
    });

    it('gives the line remove control a 40 pixel hit area', async () => {
      const el = await mount(sale());
      const remove = el.querySelector('[data-testid="pos-lines"] button[aria-label^="Remove"]') as HTMLElement;
      expect(remove.className).toContain('h-10');
      expect(remove.className).toContain('w-10');
    });
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

describe('the counter opens on its own yard\'s register', () => {
  const lk = reg('REG-LK', 'lk');
  const wk = reg('REG-WK', 'wk');
  const wk2 = reg('REG-WK2', 'wk', 'Yard till');
  const settle = async (el: POSTerminal) => {
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
  };
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    svc.currentTill.mockResolvedValue(null);
    svc.startSale.mockResolvedValue(sale());
  });
  // The server's answer: the request carries the yard, so the list is that
  // yard's registers (every register with no yard selected).
  const serve = (all: CounterRegister[]) =>
    svc.listRegisters.mockImplementation(async () =>
      all.filter((r) => branchContext.currentId === null || r.branch_id === branchContext.currentId));

  it('opens a cashier at Lake Country on that yard\'s register and shows its code', async () => {
    branchContext.setCurrent('b-lk');
    serve([REG_01, lk, wk]);
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    expect(svc.currentTill).toHaveBeenCalledWith('REG-LK');
    expect(svc.startSale).toHaveBeenCalledWith('REG-LK');
    expect(q(el, 'pos-register').textContent).toContain('REG-LK');
    el.remove();
  });

  it('moves to the new yard\'s register when the yard changes', async () => {
    branchContext.setCurrent('b-main');
    serve([REG_01, lk, wk]);
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    expect(svc.startSale).toHaveBeenLastCalledWith('REG-01');
    branchContext.setCurrent('b-wk');
    await settle(el);
    expect(svc.currentTill).toHaveBeenLastCalledWith('REG-WK');
    expect(svc.startSale).toHaveBeenLastCalledWith('REG-WK');
    expect(q(el, 'pos-register').textContent).toContain('REG-WK');
    el.remove();
  });

  it('says so plainly and offers no till or sale action when the yard has no register', async () => {
    branchContext.setCurrent('b-none');
    serve([REG_01, lk]);
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    expect(q(el, 'pos-no-register').textContent).toContain('no register');
    expect(svc.startSale).not.toHaveBeenCalled();
    expect(svc.currentTill).not.toHaveBeenCalled();
    expect(q(el, 'pos-pay')).toBeNull();
    expect(el.textContent).not.toContain('Open Till');
    expect(el.textContent).not.toContain('New Sale');
    el.remove();
  });

  it('offers a picker when the yard has more than one register and remembers the choice per yard', async () => {
    branchContext.setCurrent('b-wk');
    serve([REG_01, wk, wk2]);
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    // the first by name: Front till
    expect(svc.startSale).toHaveBeenLastCalledWith('REG-WK');
    const picker = q(el, 'pos-register-picker') as HTMLSelectElement;
    expect(picker).toBeTruthy();
    picker.value = 'REG-WK2';
    picker.dispatchEvent(new Event('change'));
    await settle(el);
    expect(svc.startSale).toHaveBeenLastCalledWith('REG-WK2');
    el.remove();

    const again = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(again);
    await settle(again);
    expect(svc.startSale).toHaveBeenLastCalledWith('REG-WK2');
    again.remove();
  });
});
