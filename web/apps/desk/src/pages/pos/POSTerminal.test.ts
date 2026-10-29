// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Sale, CounterCustomer } from '../../types/pos';

const svc = vi.hoisted(() => ({
  listRegisters: vi.fn(),
  currentTill: vi.fn(),
  startSale: vi.fn(),
  completeSale: vi.fn(),
  largeCashPreview: vi.fn().mockResolvedValue({ asks_details: false, fields: [], dealer_threshold: false }),
  searchProducts: vi.fn(),
  findCustomers: vi.fn(),
  attachCustomer: vi.fn(),
  addItem: vi.fn(),
  removeItem: vi.fn(),
  changeQuantity: vi.fn(),
  getSale: vi.fn(),
  searchSales: vi.fn(),
  createReturn: vi.fn(),
  previewReturn: vi.fn(),
  returnReasons: vi.fn(),
  openTill: vi.fn(),
  tillReport: vi.fn(),
  closeTill: vi.fn(),
  recordMovement: vi.fn(),
  printReceipt: vi.fn(),
  emailReceipt: vi.fn(),
  settings: vi.fn(),
}));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './POSTerminal';
import type { POSTerminal } from './POSTerminal';
import { branchContext } from '../../services/BranchContext';
import { authCustody } from '@gable/auth';
import { openAppDialog, answerAppDialog, chooseOption } from '../../test/dom';
import type { CounterRegister, SaleSummary } from '../../types/pos';

const reg = (id: string, branch: string, name = 'Front till'): CounterRegister => ({
  id, name, branch_id: `b-${branch}`, branch_code: branch.toUpperCase(), branch_name: `${branch} yard`,
});
const REG_01 = reg('REG-01', 'main');
const OPEN_TILL = { id: 't', register_id: 'REG-01', cashier_id: 'c', status: 'open', opening_float_cents: 20000 };
const NIL_CASHIER = '00000000-0000-0000-0000-000000000000';
const CASHIER_A = '11111111-1111-4111-8111-111111111111';
const CASHIER_B = '22222222-2222-4222-8222-222222222222';
const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_');
const tokenFor = (sub: string) => `${b64({ alg: 'none', typ: 'JWT' })}.${b64({ sub, exp: 4102444800 })}.sig`;


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
  po_required: false, job_required: false, jobs: [],
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
// A tender is never assumed: a leg that is about the amount presses its tender
// itself, with chooseCash or the tender's own button.
const chooseCash = async (el: POSTerminal) => {
  q(el, 'tender-cash').click();
  await el.updateComplete;
};
const typeInto = async (el: POSTerminal, v: string) => {
  const input = el.querySelector('#pos-tender-amount') as HTMLInputElement;
  input.value = v;
  input.dispatchEvent(new Event('input'));
  await el.updateComplete;
};

describe('the counter as the Panels layout', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('mounts without setting state while its first update completes', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const el = await mount(sale());
    await el.updateComplete;
    const messages = warn.mock.calls.map((c) => String(c[0]));
    expect(messages.filter((m) => m.includes('gable-pos-terminal scheduled an update'))).toEqual([]);
    warn.mockRestore();
    el.remove();
  });

  it('shows the three panels and Pay as the one primary action carrying the summary total', async () => {
    const el = await mount(sale());
    expect(q(el, 'panel-customer')).toBeTruthy();
    expect(q(el, 'panel-payment')).toBeTruthy();
    expect(q(el, 'panel-summary')).toBeTruthy();
    for (const n of ['customer', 'payment', 'summary']) expect(q(el, `progress-${n}`)).toBeTruthy();
    await chooseCash(el);
    const pay = q(el, 'pos-pay');
    expect(el.querySelectorAll('[data-testid="pos-pay"]').length).toBe(1);
    expect(q(el, 'summary-total').textContent?.trim()).toBe('$86.50');
    expect(pay.textContent).toContain('$86.50');
    expect(q(el, 'pos-till-state').textContent).toContain('Till open');
  });

  it('ticks Payment on the Summary step only once a tender is chosen', async () => {
    const el = await mount(sale());
    q(el, 'pos-walk-in').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    q(el, 'pos-next-summary').click();
    await el.updateComplete;
    // No tender chosen ("Tender: None yet"): Payment shows its number, not a
    // tick, on the panel head and in the header's progress chip.
    expect(q(el, 'panel-payment-toggle').querySelector('span')!.textContent).toBe('2');
    expect(q(el, 'progress-payment').getAttribute('data-state')).not.toBe('done');
    await chooseCash(el);
    expect(q(el, 'panel-payment-toggle').querySelector('span')!.textContent).toBe('✓');
    expect(q(el, 'progress-payment').getAttribute('data-state')).toBe('done');
    el.remove();
  });

  it('pays in cash with the tendered amount, sending one cash tender on the sale revision', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed', change_cents: 1350 }));
    const el = await mount(sale());
    await chooseCash(el);
    await typeInto(el, '100.00');
    expect(q(el, 'pos-change').textContent).toContain('$13.50');
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [{ method: 'cash', amount_cents: 10000 }], []);
  });

  it('takes a split between two tenders as the added tender plus the entry', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await chooseCash(el);
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
    ], []);
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
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [{ method: 'account', amount_cents: 8650 }], []);
  });

  it('keeps Pay off while the sale has no lines or the tenders fall short', async () => {
    const el = await mount(sale({ lines: [], subtotal_cents: 0, tax_cents: 0, total_cents: 0 }));
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    (el as unknown as { _sale: Sale })._sale = sale();
    await el.updateComplete;
    await chooseCash(el);
    await typeInto(el, '10.00');
    const pay = q(el, 'pos-pay') as HTMLButtonElement;
    expect(pay.disabled).toBe(true);
    expect(pay.textContent).toContain('Still owed $76.50');
    expect(svc.completeSale).not.toHaveBeenCalled();
  });

  it('sends only the corrected entry after a refused Pay, never the first attempt again', async () => {
    svc.completeSale
      .mockRejectedValueOnce(new Error('the drawer is closed'))
      .mockResolvedValueOnce(sale({ status: 'completed' }));
    const el = await mount(sale());
    q(el, 'tender-cash').click();
    await el.updateComplete;
    await typeInto(el, '100.00');
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.completeSale).toHaveBeenCalledTimes(1);
    expect(svc.completeSale).toHaveBeenLastCalledWith('s1', 3, [{ method: 'cash', amount_cents: 10000 }], []);
    await typeInto(el, '86.50');
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    expect(svc.completeSale).toHaveBeenCalledTimes(2);
    expect(svc.completeSale).toHaveBeenLastCalledWith('s1', 3, [{ method: 'cash', amount_cents: 8650 }], []);
  });

  describe('the account refusal guards', () => {
    type Inner = { _customer: CounterCustomer | null; _sale: Sale; _poText: string; _jobId: string; _tenderMethod: string; _accountRefusal: string | null; _chooseMethod(m: string): void };
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
    const needsJob = { ...account, job_required: true, jobs: [{ id: 'j1', name: 'Ridge Build' }] } as unknown as CounterCustomer;
    const pay = async (el: POSTerminal) => {
      q(el, 'pos-pay').click();
      await new Promise((r) => setTimeout(r, 0));
      await el.updateComplete;
    };

    for (const [name, c] of [['on hold', onHold], ['over the limit', overLimit], ['asking for a PO that is empty', needsPO], ['requiring a job that is not picked', needsJob]] as const) {
      it(`refuses the choice of the account for a customer ${name}`, async () => {
        const el = await withCustomer(c);
        expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(true);
        inner(el)._chooseMethod('account');
        await el.updateComplete;
        expect(inner(el)._tenderMethod).toBeNull();
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

    it('marks the Job select as required for a charge and clears the refusal when a job is picked', async () => {
      const el = await withCustomer(needsJob);
      const label = el.querySelector('label[for="pos-job"]');
      expect(label?.textContent).toMatch(/required for a charge/);
      expect(inner(el)._accountRefusal).toMatch(/requires a job on every charge sale/);
      expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(true);
      inner(el)._jobId = 'j1';
      await el.updateComplete;
      expect(inner(el)._accountRefusal).toBeNull();
      expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(false);
    });

    it('says the account has no job on file when the requirement cannot be met', async () => {
      const noneOnFile = { ...account, job_required: true, jobs: [] } as unknown as CounterCustomer;
      const el = await withCustomer(noneOnFile);
      expect(el.querySelector('label[for="pos-job"]')).toBeTruthy();
      expect(inner(el)._accountRefusal).toMatch(/none on file/);
      const select = el.querySelector('#pos-job') as HTMLSelectElement;
      expect(select.disabled).toBe(true);
      expect(select.textContent).toContain('No jobs on file');
      expect(el.querySelector('#pos-customer')?.textContent).toContain('add one on the account page');
    });

    it('shows the chosen job and purchase order on the summary panels and the completed sale', async () => {
      const el = await withCustomer(needsJob, 'PO-42');
      inner(el)._jobId = 'j1';
      inner(el)._sale = sale({ customer_id: needsJob.id, customer_po: 'PO-42', job_id: 'j1', status: 'completed' });
      await el.updateComplete;
      expect(q(el, 'review-po').textContent).toBe('PO-42');
      expect(q(el, 'review-job').textContent).toBe('Ridge Build');
      expect(q(el, 'pos-order-refs').textContent).toContain('PO PO-42');
      expect(q(el, 'pos-order-refs').textContent).toContain('Job Ridge Build');
      expect(q(el, 'pos-complete-refs').textContent).toContain('PO PO-42');
      expect(q(el, 'pos-complete-refs').textContent).toContain('Job Ridge Build');
    });

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
      expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [{ method: 'account', amount_cents: 8650 }], []);
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
    const result = { product_id: 'p-2x4', sku: '2X4-8', description: '2x4 8ft', uom: 'EA', unit_price_cents: 350, in_stock: '100' };
    const add = (el: Element, qty: string, over: object = {}) =>
      el.dispatchEvent(new CustomEvent('pos-catalog-add', { detail: { product: { ...result, ...over }, qty }, bubbles: true, composed: true }));
    const flush = async (el: POSTerminal) => { await new Promise((r) => setTimeout(r, 0)); await el.updateComplete; };

    it('adds a result with the typed quantity as one line with its total', async () => {
      const twelve = { ...line('x', 4200), quantity: '12', unit_price_ten_thousandths: 35000 };
      svc.addItem.mockResolvedValue(sale({ lines: [twelve as never], subtotal_cents: 4200, total_cents: 4200 }));
      const el = await mount(sale({ lines: [] }));
      add(el, '12');
      await flush(el);
      expect(svc.addItem).toHaveBeenCalledWith('s1', 'p-2x4', '12', 'EA');
      expect(el.querySelectorAll('[data-testid="pos-lines"] li').length).toBe(1);
      expect(el.querySelector('[data-testid="pos-lines"]')?.textContent).toContain('$42.00');
    });

    it('allows a decimal quantity for a length unit', async () => {
      svc.addItem.mockResolvedValue(sale());
      const el = await mount(sale({ lines: [] }));
      add(el, '2.5', { product_id: 'p-trim', sku: 'TRIM-LF', uom: 'LF' });
      await flush(el);
      expect(svc.addItem).toHaveBeenCalledWith('s1', 'p-trim', '2.5', 'LF');
    });

    it('refuses a decimal quantity for a unit counted whole and sends nothing', async () => {
      const el = await mount(sale({ lines: [] }));
      add(el, '2.5');
      await flush(el);
      expect(el.querySelector('[role="alert"]')?.textContent).toContain('EA is counted in whole units');
      expect(svc.addItem).not.toHaveBeenCalled();
    });

    it('adds a whole quantity of a unit counted whole, written with a zero fraction', async () => {
      svc.addItem.mockResolvedValue(sale());
      const el = await mount(sale({ lines: [] }));
      add(el, '3.0');
      await flush(el);
      expect(svc.addItem).toHaveBeenCalledWith('s1', 'p-2x4', '3', 'EA');
    });

    it('refuses a zero or negative quantity and adds nothing', async () => {
      const el = await mount(sale({ lines: [] }));
      for (const v of ['0', '-3', '']) {
        add(el, v);
        await flush(el);
        expect(el.querySelector('[role="alert"]')?.textContent).toContain('quantity');
      }
      expect(svc.addItem).not.toHaveBeenCalled();
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
    await chooseCash(el);
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
    // a catalogue chip is a selection, not an action
    const filled = (el: Element) => [...el.querySelectorAll('button')]
      .filter((b) => b.className.includes('bg-gable-green') && b.getAttribute('data-testid') !== 'catalog-chip'
        // the phone's pinned pay bar is the narrow surface's own Pay, not a second primary of the page body
        && b.getAttribute('data-testid') !== 'pos-pay-bar-button');

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
      await chooseCash(el);
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

    it('flows the right column with the page: one scroller, no sticky column, no inner region, no pinned block', async () => {
      const el = await mount(sale());
      const body = q(el, 'pos-body');
      expect(body.className).toMatch(/(^|\s)overflow-y-auto(\s|$)/);
      expect(body.className).not.toContain('overflow-hidden');
      for (const id of ['pos-panels', 'pos-items']) {
        const n = q(el, id);
        expect(n.className).not.toMatch(/sticky|overflow-y-auto|overflow-auto|overflow-hidden|max-h-/);
      }
      for (const n of q(el, 'pos-panels').querySelectorAll('*')) {
        expect(n.className.toString()).not.toMatch(/sticky|overflow-y-auto|overflow-auto|max-h-/);
      }
    });

    it('keeps the order summary as one card holding the lines, the totals, Pay and Clear sale', async () => {
      const el = await mount(sale());
      const card = q(el, 'pos-order-summary');
      expect(card.tagName).toBe('SECTION');
      for (const id of ['pos-lines', 'summary-total', 'pos-pay', 'pos-clear-sale']) expect(card.contains(q(el, id))).toBe(true);
      expect(card.parentElement).toBe(q(el, 'pos-panels'));
      expect(q(el, 'pos-order-footer')).toBeNull();
      expect(q(el, 'pos-order-lines')).toBeNull();
    });

    it('holds Payment and the order summary with Pay in the one right column', async () => {
      const el = await mount(sale());
      const column = q(el, 'pos-panels');
      expect(column.contains(q(el, 'panel-payment'))).toBe(true);
      expect(column.contains(q(el, 'pos-order-summary'))).toBe(true);
      expect(q(el, 'pos-order-summary').contains(q(el, 'pos-pay'))).toBe(true);
    });

    it('keeps two columns down to 768 pixels and puts the catalogue first below that (the touch order)', async () => {
      const el = await mount(sale());
      expect(q(el, 'pos-body').className).toContain('min-[768px]:flex-row');
      expect(q(el, 'pos-items').className).toMatch(/min-\[768px\]:w-\[/);
      expect(q(el, 'pos-items').className).not.toContain('order-last');
      expect(q(el, 'pos-panels').className).not.toContain('order-first');
      // Below 768 the working order is the thumb's: the panels (one step open
      // at a time) above the ticket, the ticket last; a pinned pay bar carries
      // the total and Pay at the bottom of the screen.
      const panels = q(el, 'pos-panels');
      const items = q(el, 'pos-items');
      expect(items.compareDocumentPosition(panels) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      const summary = q(el, 'pos-order-summary');
      expect(panels.compareDocumentPosition(summary) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      expect(summary.className).not.toContain('order-first');
      const bar = q(el, 'pos-pay-bar');
      expect(bar).toBeTruthy();
      expect(bar.className).toContain('min-[768px]:hidden');
      expect(q(el, 'pos-body').compareDocumentPosition(bar) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it('lists every line of the order summary, never in a clipped scroller', async () => {
      const el = await mount(sale());
      const list = q(el, 'pos-lines');
      expect(list.className).not.toMatch(/max-h-/);
      expect(list.className).not.toContain('overflow-y-auto');
      expect(list.querySelectorAll('li').length).toBe(2);
    });

    it('draws Void as a plain secondary action, never a red outline', async () => {
      svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
      const el = await mount(sale());
      await chooseCash(el);
      await typeInto(el, '100.00');
      q(el, 'pos-pay').click();
      await new Promise((r) => setTimeout(r, 0));
      await el.updateComplete;
      const voidBtn = q(el, 'pos-void') as HTMLButtonElement;
      expect(voidBtn.disabled).toBe(false);
      expect(voidBtn.className).not.toContain('safety-red');
    });

    it('shows no remove control at all on a completed sale', async () => {
      const el = await mount(sale({ status: 'completed' }));
      expect(el.querySelectorAll('[data-testid="pos-lines"] li').length).toBe(2);
      expect(el.querySelectorAll('[data-testid="pos-lines"] button[aria-label^="Remove"]').length).toBe(0);
    });

    it('leaves the completion to the card: no green band over the page, so nothing shifts', async () => {
      svc.completeSale.mockResolvedValue(sale({ status: 'completed', number: 'POS-000002', total_cents: 13810, change_cents: 0 }));
      const el = await mount(sale());
      await chooseCash(el);
      await typeInto(el, '86.50');
      q(el, 'pos-pay').click();
      await new Promise((r) => setTimeout(r, 0));
      await el.updateComplete;
      expect(el.querySelector('div[role="status"]')).toBeNull();
      expect(q(el, 'pos-complete').textContent).toContain('Sale complete');
      expect(q(el, 'pos-complete').textContent).toContain('POS-000002');
      expect(q(el, 'pos-complete').textContent).toContain('$138.10');
      expect(q(el, 'pos-complete').getAttribute('aria-live')).toBe('polite');
    });

    it('gives the line remove control a 44 pixel touch target', async () => {
      const el = await mount(sale());
      const remove = el.querySelector('[data-testid="pos-lines"] button[aria-label^="Remove"]') as HTMLElement;
      expect(remove.className).toContain('h-11');
      expect(remove.className).toContain('w-11');
    });
  });

  it('tells the cashier to name the customer first, from the start of an empty sale', async () => {
    const el = await mount(sale({ lines: [] }));
    expect(q(el, 'panel-customer').textContent).toContain('Name the customer, then add products');
  });

  it('words the account tender in sentence case', async () => {
    const el = await mount(sale());
    expect(q(el, 'tender-account').textContent?.trim()).toBe('Charge to account');
    expect(q(el, 'tender-account').className).not.toContain('capitalize');
  });
});

describe('the Panels board', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });
  type Inner = {
    _customer: CounterCustomer | null; _sale: Sale; _chooseCustomer(c: CounterCustomer): Promise<void>;
    _till: unknown;
  };
  const toggle = (el: Element, key: string) => q(el, `panel-${key}-toggle`) as HTMLButtonElement;
  const expanded = (el: Element, key: string) => toggle(el, key).getAttribute('aria-expanded');
  const flush = async (el: POSTerminal) => { await new Promise((r) => setTimeout(r, 0)); await el.updateComplete; };
  const product = { product_id: 'p-z', sku: 'SKU-z', description: 'Item z', uom: 'EA', unit_price_cents: 700, in_stock: '5' };

  it('opens one panel at a time and starts on Customer', async () => {
    const el = await mount(sale());
    expect(expanded(el, 'customer')).toBe('true');
    expect(expanded(el, 'payment')).toBe('false');
    expect(expanded(el, 'summary')).toBe('false');
    toggle(el, 'payment').click();
    await el.updateComplete;
    expect(expanded(el, 'customer')).toBe('false');
    expect(expanded(el, 'payment')).toBe('true');
    expect(q(el, 'panel-customer-body').hasAttribute('hidden')).toBe(true);
    expect(q(el, 'panel-payment-body').hasAttribute('hidden')).toBe(false);
    toggle(el, 'summary').click();
    await el.updateComplete;
    expect(expanded(el, 'payment')).toBe('false');
    expect(expanded(el, 'summary')).toBe('true');
    el.remove();
  });

  it('keeps Customer open on an account customer, showing the standing, PO and job, and collapses on Next: Payment', async () => {
    svc.attachCustomer.mockResolvedValue(sale({ customer_id: 'cu1' }));
    const el = await mount(sale());
    await (el as unknown as Inner)._chooseCustomer(account);
    await el.updateComplete;
    expect(expanded(el, 'customer')).toBe('true');
    expect(expanded(el, 'payment')).toBe('false');
    expect(q(el, 'pos-customer-name').textContent).toBe('Test Builders');
    expect(q(el, 'pos-customer-standing').textContent).toContain('Credit available');
    expect(el.querySelector('#pos-po')).toBeTruthy();
    expect((q(el, 'pos-next-payment') as HTMLButtonElement).disabled).toBe(false);
    q(el, 'pos-next-payment').click();
    await el.updateComplete;
    const head = toggle(el, 'customer');
    expect(head.textContent).toContain('Test Builders');
    expect(head.textContent).toContain('Change');
    expect(head.getAttribute('aria-expanded')).toBe('false');
    expect(expanded(el, 'payment')).toBe('true');
    head.click();
    await el.updateComplete;
    expect(expanded(el, 'customer')).toBe('true');
    expect(expanded(el, 'payment')).toBe('false');
    expect((el as unknown as Inner)._customer?.id).toBe('cu1');
    expect(q(el, 'pos-customer-name').textContent).toBe('Test Builders');
    expect(svc.attachCustomer).toHaveBeenCalledTimes(1);
    el.remove();
  });

  it('asks for items before Next: Payment when the sale has no line yet, and keeps the chips with the panel', async () => {
    svc.attachCustomer.mockResolvedValue(sale({ customer_id: 'cu1', lines: [] }));
    const el = await mount(sale({ lines: [] }));
    await (el as unknown as Inner)._chooseCustomer(account);
    await el.updateComplete;
    expect(expanded(el, 'customer')).toBe('true');
    expect((q(el, 'pos-next-payment') as HTMLButtonElement).disabled).toBe(true);
    expect(q(el, 'panel-customer-body').textContent).toContain('Add items to continue');
    expect(q(el, 'progress-customer').getAttribute('data-state')).toBe('current');
    el.remove();
  });

  it('labels the control that clears the customer Walk-in sale and returns to a walk in', async () => {
    svc.attachCustomer
      .mockResolvedValueOnce(sale({ customer_id: 'cu1', lines: [] }))
      .mockResolvedValueOnce(sale({ customer_id: null, lines: [] }));
    const el = await mount(sale({ lines: [] }));
    await (el as unknown as Inner)._chooseCustomer(account);
    await el.updateComplete;
    const buttons = [...q(el, 'pos-customer').querySelectorAll('button')].map((b) => b.textContent?.trim());
    expect(buttons).toContain('Walk-in sale');
    expect(buttons).not.toContain('Change');
    ([...q(el, 'pos-customer').querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Walk-in sale') as HTMLElement).click();
    await flush(el);
    expect(svc.attachCustomer).toHaveBeenLastCalledWith('s1', { customer_id: null });
    expect((el as unknown as Inner)._customer).toBeNull();
    el.remove();
  });

  it('cannot reach Pay on account for a PO required customer until the PO is entered', async () => {
    svc.attachCustomer.mockResolvedValue(sale({ customer_id: 'cu1' }));
    const needsPO = { ...account, po_required: true } as unknown as CounterCustomer;
    const el = await mount(sale());
    await (el as unknown as Inner)._chooseCustomer(needsPO);
    await el.updateComplete;
    q(el, 'pos-next-payment').click();
    await el.updateComplete;
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(true);
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });

  it('moves to Payment on a walk in once the sale has a line, and keeps Customer open before one', async () => {
    const el = await mount(sale());
    q(el, 'pos-walk-in').click();
    await flush(el);
    expect(toggle(el, 'customer').textContent).toContain('Walk-in sale');
    expect(expanded(el, 'payment')).toBe('true');
    const empty = await mount(sale({ lines: [] }));
    q(empty, 'pos-walk-in').click();
    await flush(empty);
    expect(expanded(empty, 'payment')).toBe('false');
    expect(expanded(empty, 'customer')).toBe('false');
    el.remove();
    empty.remove();
  });

  it('opens Payment when the first line lands on a sale whose customer is settled', async () => {
    svc.addItem.mockResolvedValue(sale({ lines: [line('z', 700)] as unknown as Sale['lines'] }));
    const el = await mount(sale({ lines: [] }));
    q(el, 'pos-walk-in').click();
    await flush(el);
    expect(expanded(el, 'payment')).toBe('false');
    await (el as unknown as { _addItem(p: unknown, q: string): Promise<void> })._addItem(product, '1');
    await el.updateComplete;
    expect(expanded(el, 'payment')).toBe('true');
    el.remove();
  });

  it('has the step chips follow the panels', async () => {
    const el = await mount(sale());
    const states = () => ['customer', 'payment', 'summary'].map((n) => q(el, `progress-${n}`).getAttribute('data-state'));
    expect(states()).toEqual(['current', 'upcoming', 'upcoming']);
    q(el, 'pos-walk-in').click();
    await flush(el);
    expect(states()).toEqual(['done', 'current', 'upcoming']);
    // Payment ticks only once a tender is chosen; choose one before Summary.
    await chooseCash(el);
    q(el, 'pos-next-summary').click();
    await el.updateComplete;
    expect(states()).toEqual(['done', 'done', 'current']);
    expect(expanded(el, 'summary')).toBe('true');
    q(el, 'pos-summary-back').click();
    await el.updateComplete;
    expect(states()).toEqual(['done', 'current', 'upcoming']);
    q(el, 'pos-payment-back').click();
    await el.updateComplete;
    expect(states()).toEqual(['current', 'upcoming', 'upcoming']);
    el.remove();
  });

  it('shows the payment value on a done Payment panel and the review on Summary', async () => {
    const el = await mount(sale());
    await chooseCash(el);
    await typeInto(el, '100.00');
    q(el, 'pos-next-summary').click();
    await el.updateComplete;
    expect(toggle(el, 'payment').textContent).toContain('Cash $100.00');
    expect(q(el, 'panel-summary-body').textContent).toContain('$86.50');
    expect(q(el, 'panel-summary-body').textContent).toContain('2');
    el.remove();
  });

  it('changes no money when a panel opens or closes', async () => {
    const el = await mount(sale());
    await chooseCash(el);
    for (const key of ['payment', 'summary', 'customer', 'customer', 'payment']) {
      toggle(el, key).click();
      await el.updateComplete;
    }
    q(el, 'pos-next-summary').click();
    await el.updateComplete;
    q(el, 'pos-summary-back').click();
    await el.updateComplete;
    for (const m of ['completeSale', 'addItem', 'removeItem', 'attachCustomer', 'startSale']) {
      if (m === 'startSale') expect(svc[m as 'startSale']).toHaveBeenCalledTimes(1);
      else expect(svc[m as 'completeSale']).not.toHaveBeenCalled();
    }
    expect(q(el, 'summary-total').textContent?.trim()).toBe('$86.50');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    el.remove();
  });

  it('puts Pay in the order summary, off on a sale that is not open', async () => {
    const el = await mount(sale());
    expect(q(el, 'pos-order-summary').contains(q(el, 'pos-pay'))).toBe(true);
    expect(q(el, 'pos-order-summary').textContent).toContain('Item a');
    expect(q(el, 'pos-order-summary').textContent).toContain('1 EA at $50.00');
    await chooseCash(el);
    await typeInto(el, '100.00');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    const done = await mount(sale({ status: 'completed' }));
    expect((q(done, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    el.remove();
    done.remove();
  });

  describe('Clear sale', () => {
    it('is off with no lines and not offered on a completed sale', async () => {
      const el = await mount(sale({ lines: [] }));
      expect((q(el, 'pos-clear-sale') as HTMLButtonElement).disabled).toBe(true);
      const done = await mount(sale({ status: 'completed' }));
      expect(q(done, 'pos-clear-sale')).toBeNull();
      el.remove();
      done.remove();
    });

    it('asks first, and declining removes nothing', async () => {
      const el = await mount(sale());
      q(el, 'pos-clear-sale').click();
      await flush(el);
      expect(openAppDialog()).toBeTruthy();
      await answerAppDialog(false);
      await flush(el);
      expect(svc.removeItem).not.toHaveBeenCalled();
      el.remove();
    });

    it('removes every line through the line removal once confirmed', async () => {
      svc.removeItem
        .mockResolvedValueOnce(sale({ lines: [line('b', 3000)] as unknown as Sale['lines'] }))
        .mockResolvedValueOnce(sale({ lines: [] }));
      const el = await mount(sale());
      q(el, 'pos-clear-sale').click();
      await flush(el);
      await answerAppDialog(true);
      await flush(el);
      await flush(el);
      expect(svc.removeItem.mock.calls).toEqual([['s1', 'a'], ['s1', 'b']]);
      expect(q(el, 'pos-lines')).toBeNull();
      el.remove();
    });
  });

  describe('the catalogue add event', () => {
    const fire = (el: Element, quantity: string, over: object = {}) =>
      el.dispatchEvent(new CustomEvent('pos-catalog-add', { detail: { product: { ...product, ...over }, qty: quantity }, bubbles: true, composed: true }));

    it('mounts the catalogue open with a till, locked without, keyed on the yard register', async () => {
      const el = await mount(sale());
      const cat = el.querySelector('gable-pos-catalog') as HTMLElement & { disabled: boolean; yardKey: string };
      expect(cat.disabled).toBe(false);
      expect(cat.yardKey).toBe('REG-01');
      (el as unknown as Inner)._till = null;
      await el.updateComplete;
      expect(cat.disabled).toBe(true);
      el.remove();
    });

    it('adds through the add path with its quantity', async () => {
      svc.addItem.mockResolvedValue(sale({ lines: [line('z', 700)] as unknown as Sale['lines'] }));
      const el = await mount(sale({ lines: [] }));
      fire(el, '3');
      await flush(el);
      expect(svc.addItem).toHaveBeenCalledWith('s1', 'p-z', '3', 'EA');
      expect(svc.startSale).toHaveBeenCalledTimes(1);
      el.remove();
    });

    it('starts one sale for two quick adds over a completed one', async () => {
      const el = await mount(sale({ status: 'completed' }));
      svc.startSale.mockClear();
      svc.startSale.mockResolvedValue(sale({ id: 's2', number: 'S-2', lines: [] }));
      svc.addItem.mockResolvedValue(sale({ id: 's2', number: 'S-2', lines: [line('z', 700)] as unknown as Sale['lines'] }));
      fire(el, '1');
      fire(el, '1');
      await flush(el);
      await flush(el);
      expect(svc.startSale).toHaveBeenCalledTimes(1);
      expect(svc.addItem).toHaveBeenCalledWith('s2', 'p-z', '1', 'EA');
      el.remove();
    });

    it('resets the card once an add is accepted, and leaves it as typed when the add is refused', async () => {
      svc.addItem.mockResolvedValue(sale({ lines: [line('z', 700)] as unknown as Sale['lines'] }));
      const el = await mount(sale({ lines: [] }));
      const cat = el.querySelector('gable-pos-catalog') as HTMLElement & { resetQuantity(): void };
      const reset = vi.spyOn(cat, 'resetQuantity');
      // refused: a decimal on a counted unit
      fire(el, '1.5');
      await flush(el);
      expect(svc.addItem).not.toHaveBeenCalled();
      expect(reset).not.toHaveBeenCalled();
      // refused: the server turns the add away
      svc.addItem.mockRejectedValueOnce(new Error('stale revision'));
      fire(el, '2');
      await flush(el);
      expect(reset).not.toHaveBeenCalled();
      // accepted
      el.dispatchEvent(new CustomEvent('pos-catalog-add', { detail: { product, qty: '3', query: 'pav' }, bubbles: true, composed: true }));
      await flush(el);
      expect(svc.addItem).toHaveBeenLastCalledWith('s1', 'p-z', '3', 'EA');
      expect(reset).toHaveBeenCalledTimes(1);
      expect(reset).toHaveBeenCalledWith({ qty: '3', query: 'pav' });
      el.remove();
    });

    it('does not reset the card for an add made with no till', async () => {
      const el = await mount(sale());
      const cat = el.querySelector('gable-pos-catalog') as HTMLElement & { resetQuantity(): void };
      const reset = vi.spyOn(cat, 'resetQuantity');
      (el as unknown as Inner)._till = null;
      await el.updateComplete;
      fire(el, '1');
      await flush(el);
      expect(reset).not.toHaveBeenCalled();
      el.remove();
    });

    it('answers with no till and adds nothing', async () => {
      const el = await mount(sale());
      (el as unknown as Inner)._till = null;
      await el.updateComplete;
      fire(el, '1');
      await flush(el);
      expect(svc.addItem).not.toHaveBeenCalled();
      expect(el.querySelector('[role="alert"]')?.textContent).toContain('Open the till to start a sale');
      el.remove();
    });

    it('shows the picture the catalogue gave on the order summary line', async () => {
      svc.addItem.mockResolvedValue(sale({ lines: [line('z', 700)] as unknown as Sale['lines'] }));
      const el = await mount(sale({ lines: [] }));
      fire(el, '1', { image_url: '/catalog-illustrations/pavers.svg' });
      await flush(el);
      const img = q(el, 'pos-lines').querySelector('img') as HTMLImageElement;
      expect(img.getAttribute('src')).toBe('/catalog-illustrations/pavers.svg');
      el.remove();
    });

    it('draws the no picture mark for a line with no known picture', async () => {
      const el = await mount(sale());
      expect(q(el, 'pos-lines').querySelector('img')).toBeNull();
      expect(q(el, 'pos-lines').querySelectorAll('[data-testid="pos-line-thumb"]').length).toBe(2);
      el.remove();
    });
  });

  describe('the account refusal in words', () => {
    const needsPO = { ...account, po_required: true } as unknown as CounterCustomer;
    const onHold = { ...account, standing: 'no_credit', available_credit_cents: 0 } as unknown as CounterCustomer;
    const withCustomer = async (c: CounterCustomer) => {
      const el = await mount(sale({ customer_id: c.id }));
      (el as unknown as Inner)._customer = c;
      await el.updateComplete;
      return el;
    };

    it('prints the reason under the tenders, not only as a tooltip, with the way on for a missing PO', async () => {
      const el = await withCustomer(needsPO);
      const note = q(el, 'pos-account-refusal');
      expect(note.textContent).toContain('purchase order');
      expect(q(el, 'panel-payment-body').contains(note)).toBe(true);
      toggle(el, 'payment').click();
      await el.updateComplete;
      expect(expanded(el, 'customer')).toBe('false');
      q(el, 'pos-refusal-customer').click();
      await el.updateComplete;
      expect(expanded(el, 'customer')).toBe('true');
      expect(expanded(el, 'payment')).toBe('false');
      el.remove();
    });

    it('offers the way back to Customer for a missing job, worded for the job', async () => {
      const needsJobOnly = { ...account, job_required: true, jobs: [{ id: 'j1', name: 'Ridge Build' }] } as unknown as CounterCustomer;
      const el = await withCustomer(needsJobOnly);
      expect(q(el, 'pos-account-refusal').textContent).toContain('requires a job on every charge sale');
      toggle(el, 'payment').click();
      await el.updateComplete;
      expect(expanded(el, 'customer')).toBe('false');
      const way = q(el, 'pos-refusal-customer');
      expect(way.textContent).toContain('Pick the job in Customer');
      way.click();
      await el.updateComplete;
      expect(expanded(el, 'customer')).toBe('true');
      el.remove();
    });

    it('offers no way back for a job when the account has none on file to pick', async () => {
      const noJobs = { ...account, job_required: true, jobs: [] } as unknown as CounterCustomer;
      const el = await withCustomer(noJobs);
      expect(q(el, 'pos-account-refusal').textContent).toContain('none on file');
      expect(q(el, 'pos-refusal-customer')).toBeNull();
      el.remove();
    });

    it('offers the purchase order way on only when the purchase order is the reason', async () => {
      // over the available credit AND a PO required and empty: the words are the credit's
      const both = { ...needsPO, available_credit_cents: 5000 } as unknown as CounterCustomer;
      const el = await withCustomer(both);
      expect(q(el, 'pos-account-refusal').textContent).toContain('over the available credit');
      expect(q(el, 'pos-refusal-customer')).toBeNull();
      el.remove();
    });

    it('says an account on hold in words with no way on, and says nothing for a walk in', async () => {
      const held = await withCustomer(onHold);
      expect(q(held, 'pos-account-refusal').textContent).toContain('no credit');
      expect(q(held, 'pos-refusal-customer')).toBeNull();
      const walk = await mount(sale());
      expect(q(walk, 'pos-account-refusal')).toBeNull();
      held.remove();
      walk.remove();
    });

    it('drops the words once the purchase order is entered', async () => {
      const el = await withCustomer(needsPO);
      (el as unknown as { _poText: string })._poText = 'PO-7';
      await el.updateComplete;
      expect(q(el, 'pos-account-refusal')).toBeNull();
      el.remove();
    });
  });

  it('ends the order summary at Paid in full, with no Clear sale on a completed sale', async () => {
    const done = await mount(sale({ status: 'completed' }));
    expect(q(done, 'pos-pay').textContent?.trim()).toBe('Paid in full');
    expect(q(done, 'pos-clear-sale')).toBeNull();
    const open = await mount(sale());
    expect(q(open, 'pos-clear-sale')).toBeTruthy();
    done.remove();
    open.remove();
  });

  it('dims Clear sale with no lines as Pay is dimmed', async () => {
    const el = await mount(sale({ lines: [] }));
    const clear = q(el, 'pos-clear-sale') as HTMLButtonElement;
    expect(clear.disabled).toBe(true);
    expect(clear.className).toContain('opacity-40');
    expect(clear.className).toContain('text-slate-400');
    el.remove();
  });

  it('puts the next sale note in the card\'s own header row, only once a sale is complete', async () => {
    const open = await mount(sale());
    const catOpen = open.querySelector('gable-pos-catalog') as HTMLElement & { note: string; disabled: boolean };
    expect(catOpen.note).toBe('');
    expect(q(open, 'pos-next-sale-note')).toBeNull();
    const done = await mount(sale({ status: 'completed' }));
    const cat = done.querySelector('gable-pos-catalog') as HTMLElement & { note: string; disabled: boolean };
    expect(cat.note).toBe('Add a product to start the next sale');
    expect(cat.disabled).toBe(false);
    open.remove();
    done.remove();
  });

  describe('the receipt choices', () => {
    const done = (over: Partial<Sale> = {}) => sale({ status: 'completed', number: 'POS-000009', ...over });

    it('shows Print and Email for a completed sale that has an invoice and a named customer', async () => {
      const el = await mount(done({ invoice_id: 'inv-1', customer_id: 'cu1' }));
      const card = q(el, 'pos-complete');
      expect(card.textContent).toContain('Sale complete');
      expect(card.textContent).toContain('POS-000009');
      expect(q(el, 'pos-receipt-print')).toBeTruthy();
      expect(q(el, 'pos-receipt-email')).toBeTruthy();
      el.remove();
    });

    it('shows Print alone for a walk in sale and no receipt choice without an invoice', async () => {
      const walk = await mount(done({ invoice_id: 'inv-2', customer_id: null }));
      expect(q(walk, 'pos-receipt-print')).toBeTruthy();
      expect(q(walk, 'pos-receipt-email')).toBeNull();
      walk.remove();
      const bare = await mount(done({ invoice_id: null }));
      expect(q(bare, 'pos-receipt-print')).toBeNull();
      expect(q(bare, 'pos-receipt-email')).toBeNull();
      bare.remove();
    });

    it('prints through the counter\'s own receipt route with the sale\'s id', async () => {
      svc.printReceipt.mockResolvedValue(undefined);
      const el = await mount(done({ id: 'sale-9', invoice_id: 'inv-1', customer_id: 'cu1' }));
      q(el, 'pos-receipt-print').click();
      await flush(el);
      expect(svc.printReceipt).toHaveBeenCalledWith('sale-9');
      el.remove();
    });

    it('says in words beside Print receipt when the tab is blocked and offers the document to download', async () => {
      svc.printReceipt.mockResolvedValue({ url: 'blob:receipt-9', blocked: true });
      const el = await mount(done({ number: 'POS-000009', invoice_id: 'inv-1', customer_id: 'cu1' }));
      q(el, 'pos-receipt-print').click();
      await flush(el);
      expect(q(el, 'pos-receipt-note').textContent).toContain('Allow pop ups for this page to print the receipt');
      const link = q(el, 'pos-receipt-download') as HTMLAnchorElement;
      expect(link.getAttribute('href')).toBe('blob:receipt-9');
      expect(link.hasAttribute('download')).toBe(true);
      expect(q(el, 'pos-complete').contains(link)).toBe(true);
      el.remove();
    });

    it('shows no download link when the tab opened', async () => {
      svc.printReceipt.mockResolvedValue({ url: 'blob:receipt-9', blocked: false });
      const el = await mount(done({ invoice_id: 'inv-1', customer_id: 'cu1' }));
      q(el, 'pos-receipt-print').click();
      await flush(el);
      expect(q(el, 'pos-receipt-download')).toBeNull();
      el.remove();
    });

    it('says in words beside Print receipt when the receipt cannot be fetched', async () => {
      svc.printReceipt.mockRejectedValue(new Error('this sale has no receipt'));
      const el = await mount(done({ invoice_id: 'inv-1', customer_id: 'cu1' }));
      q(el, 'pos-receipt-print').click();
      await flush(el);
      expect(q(el, 'pos-receipt-note').textContent).toContain('no receipt');
      expect(q(el, 'pos-complete').contains(q(el, 'pos-receipt-note'))).toBe(true);
      el.remove();
    });

    it('sends the email once per click: Email receipt is off while its request is in flight', async () => {
      let finish: () => void = () => {};
      svc.emailReceipt.mockImplementation(() => new Promise<void>((r) => { finish = r; }));
      const el = await mount(done({ id: 'sale-9', invoice_id: 'inv-1', customer_id: 'cu1' }));
      const btn = q(el, 'pos-receipt-email') as HTMLButtonElement;
      btn.click();
      btn.click();
      await el.updateComplete;
      expect(svc.emailReceipt).toHaveBeenCalledTimes(1);
      expect((q(el, 'pos-receipt-email') as HTMLButtonElement).disabled).toBe(true);
      finish();
      await flush(el);
      expect((q(el, 'pos-receipt-email') as HTMLButtonElement).disabled).toBe(false);
      el.remove();
    });

    it('sends the invoice for Email and says so, or says why not', async () => {
      svc.emailReceipt.mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error('This account has no email address on file'));
      const el = await mount(done({ id: 'sale-9', invoice_id: 'inv-1', customer_id: 'cu1' }));
      q(el, 'pos-receipt-email').click();
      await flush(el);
      expect(svc.emailReceipt).toHaveBeenCalledWith('sale-9');
      expect(q(el, 'pos-receipt-note').textContent).toContain('sent');
      q(el, 'pos-receipt-email').click();
      await flush(el);
      // the words sit beside Email receipt, not in the page band
      expect(q(el, 'pos-receipt-note').textContent).toContain('no email address');
      expect(q(el, 'pos-complete').contains(q(el, 'pos-receipt-note'))).toBe(true);
      el.remove();
    });

    it('draws Void and Return smaller and quieter than the receipt choices, with the contact line beside Email', async () => {
      const el = await mount(done({ invoice_id: 'inv-1', customer_id: 'cu1' }));
      const print = q(el, 'pos-receipt-print');
      for (const b of [q(el, 'pos-void'), [...q(el, 'pos-complete').querySelectorAll('button')].find((x) => x.textContent?.trim() === 'Return') as HTMLElement]) {
        expect(b.className).toContain('text-xs');
        expect(b.className).not.toContain('text-sm');
        // a text button, not the same outlined box as Print and Email
        // a small outlined secondary button, at least 36 pixels high, in the quieter text colour
        expect(b.className).toContain('min-h-9');
        expect(b.className).toContain('border-white/10');
        expect(b.className).toContain('text-slate-400');
        expect(b.className).not.toContain('underline');
      }
      expect(print.className).toContain('text-sm');
      expect(q(el, 'pos-receipt-contact').textContent).toContain('contact on the account');
      el.remove();
      const walk = await mount(done({ invoice_id: 'inv-2', customer_id: null }));
      expect(q(walk, 'pos-receipt-contact')).toBeNull();
      walk.remove();
    });

    it('shows how it was paid on the order summary, with the change given', async () => {
      const tenders = [{ id: 't1', sale_id: 's1', method: 'cash', amount_cents: 10000 }] as unknown as Sale['tenders'];
      const el = await mount(done({ tenders, change_cents: 1350 }));
      const text = q(el, 'pos-order-summary').textContent ?? '';
      expect(text).toContain('Paid by cash');
      expect(text).toContain('$100.00');
      expect(text).toContain('Change given');
      expect(text).toContain('$13.50');
      el.remove();
    });
  });

  it('keeps the header to one row from 900 pixels, with the sale number beside the title and short till and cashier words', async () => {
    const el = await mount(sale());
    const header = q(el, 'pos-header');
    expect(header.className).toContain('min-[900px]:flex-nowrap');
    expect(q(el, 'pos-title-row').textContent).toContain('S-1');
    expect(q(el, 'pos-till-state').textContent?.trim()).toBe('Till open');
    expect(q(el, 'pos-till-state').getAttribute('title')).toContain('$200.00');
    expect(q(el, 'pos-cashier').textContent).not.toContain('Cashier');
    el.remove();
  });

  it('shows the yard and register beneath New sale, and the till controls on the right', async () => {
    const el = await mount(sale());
    const header = q(el, 'pos-header');
    expect(header.querySelector('h1')?.textContent?.trim()).toBe('New sale');
    expect(q(el, 'pos-yard-register').textContent).toContain('REG-01');
    expect(header.textContent).toContain('Close Till');
    el.remove();
  });

  it('dims the Items card and numbers a collapsed Customer panel before the till opens', async () => {
    svc.listRegisters.mockResolvedValue([REG_01]);
    svc.currentTill.mockResolvedValue(null);
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'pos-items').className).toContain('opacity-60');
    expect(q(el, 'catalog-closed').textContent?.trim()).toBe('Open the till to start a sale');
    expect(expanded(el, 'customer')).toBe('false');
    expect(toggle(el, 'customer').textContent).toContain('1');
    expect(toggle(el, 'customer').disabled).toBe(true);
    el.remove();
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
    vi.restoreAllMocks();
    vi.clearAllMocks();
    localStorage.clear();
    svc.currentTill.mockResolvedValue(OPEN_TILL);
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
    await answerAppDialog(true);
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
    const picker = q(el, 'pos-register-picker') as HTMLElement & { value: string };
    expect(picker).toBeTruthy();
    await chooseOption(picker, 'REG-WK2');
    await answerAppDialog(true);
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

describe('the account customer after a charge', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });
  type Inner = { _customer: CounterCustomer | null; _sale: Sale };

  it('reloads the standing once a sale that charged the account completes', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed', customer_id: 'cu1' }));
    const charged = {
      ...account, available_credit_cents: 400000 - 8650, open_receivable_cents: 100000 + 8650,
    } as unknown as CounterCustomer;
    svc.findCustomers.mockResolvedValue([{ ...account, id: 'other', account_number: 'A-1000' } as never, charged]);
    const el = await mount(sale({ customer_id: 'cu1' }));
    (el as unknown as Inner)._customer = account;
    await el.updateComplete;
    q(el, 'tender-account').click();
    await el.updateComplete;
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.findCustomers).toHaveBeenCalledWith('A-100');
    const text = q(el, 'pos-customer').textContent ?? '';
    expect(text).toContain('Credit available $3,913.50 of $5,000.00');
    expect(text).toContain('Open $1,086.50');
  });

  it('does not look the customer up again after a cash sale', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed', customer_id: 'cu1' }));
    const el = await mount(sale({ customer_id: 'cu1' }));
    (el as unknown as Inner)._customer = account;
    await el.updateComplete;
    await chooseCash(el);
    await typeInto(el, '100.00');
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.findCustomers).not.toHaveBeenCalled();
  });
});

describe('the register and the sale when the cashier moves', () => {
  const lk = reg('REG-LK', 'lk');
  const wk = reg('REG-WK', 'wk');
  const settle = async (el: POSTerminal) => {
    for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
  };
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    authCustody.signOut();
    svc.currentTill.mockResolvedValue(OPEN_TILL);
    svc.startSale.mockResolvedValue(sale({ lines: [] }));
    svc.getSale.mockRejectedValue(new Error('not found'));
    branchContext.setCurrent('b-lk');
  });
  const serve = (all: CounterRegister[]) =>
    svc.listRegisters.mockImplementation(async () =>
      all.filter((r) => branchContext.currentId === null || r.branch_id === branchContext.currentId));

  it('asks before a yard change leaves an open sale that has lines, and stays when declined', async () => {
    serve([lk, wk]);
    svc.startSale.mockResolvedValue(sale({ register_id: 'REG-LK' }));
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    branchContext.setCurrent('b-wk');
    await settle(el);
    expect(openAppDialog()).toBeTruthy();
    await answerAppDialog(false);
    await settle(el);
    expect(branchContext.currentId).toBe('b-lk');
    expect(q(el, 'pos-register').textContent).toContain('REG-LK');
    expect(svc.startSale).toHaveBeenCalledTimes(1);
    branchContext.setCurrent('b-wk');
    await settle(el);
    expect(openAppDialog()).toBeTruthy();
    await answerAppDialog(true);
    await settle(el);
    expect(q(el, 'pos-register').textContent).toContain('REG-WK');
    el.remove();
  });

  it('does not ask when the open sale has no lines', async () => {
    serve([lk, wk]);
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    branchContext.setCurrent('b-wk');
    await settle(el);
    expect(openAppDialog()).toBeNull();
    expect(q(el, 'pos-register').textContent).toContain('REG-WK');
    el.remove();
  });

  it('drops a till answer that arrives for a register no longer selected', async () => {
    serve([lk, wk]);
    let lateTill: (v: unknown) => void = () => {};
    svc.currentTill.mockImplementationOnce(() => new Promise((r) => { lateTill = r; }));
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    svc.currentTill.mockResolvedValue({ id: 'wk-till', register_id: 'REG-WK', cashier_id: 'c', status: 'open', opening_float_cents: 5000 });
    branchContext.setCurrent('b-wk');
    await settle(el);
    lateTill({ id: 'lk-till', register_id: 'REG-LK', cashier_id: 'c', status: 'open', opening_float_cents: 99900 });
    await settle(el);
    expect(q(el, 'pos-till-state').getAttribute('title')).toContain('$50.00');
    expect(q(el, 'pos-register').textContent).toContain('REG-WK');
    el.remove();
  });

  it('drops a sale answer that arrives for a register no longer selected', async () => {
    serve([lk, wk]);
    let lateSale: (v: unknown) => void = () => {};
    svc.startSale.mockImplementationOnce(() => new Promise((r) => { lateSale = r; }));
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    svc.startSale.mockResolvedValue(sale({ id: 'wk-sale', register_id: 'REG-WK', number: 'S-WK', lines: [] }));
    branchContext.setCurrent('b-wk');
    await settle(el);
    lateSale(sale({ id: 'lk-sale', register_id: 'REG-LK', number: 'S-LK', lines: [] }));
    await settle(el);
    const header = q(el, 'pos-header').textContent ?? '';
    expect(header).toContain('S-WK');
    expect(header).not.toContain('S-LK');
    el.remove();
  });

  it('says the register list failed, not that the yard has none', async () => {
    svc.listRegisters.mockRejectedValue(new Error('Failed to list the registers'));
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    expect(q(el, 'pos-no-register')).toBeNull();
    expect(q(el, 'pos-registers-failed').textContent).toContain('could not be loaded');
    expect(el.textContent).not.toContain('has no register');
    el.remove();
  });

  it('orders a mixed list by yard name first, then register name', async () => {
    branchContext.setCurrent(null);
    const zed = reg('REG-Z', 'aaa', 'Zed till');
    const mid = reg('REG-M', 'mmm', 'Alpha till');
    svc.listRegisters.mockResolvedValue([mid, zed]);
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    expect(svc.startSale).toHaveBeenLastCalledWith('REG-Z');
    el.remove();
  });

  it('with the yard switch off the server lists every register and the counter takes the first by yard and name', async () => {
    branchContext.setCurrent('b-wk');
    svc.listRegisters.mockResolvedValue([wk, lk]);
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    expect(svc.startSale).toHaveBeenLastCalledWith('REG-LK');
    el.remove();
  });

  it('reuses the cashier\'s empty open sale on the register instead of opening another', async () => {
    serve([lk]);
    const empty = sale({ id: 'empty-1', register_id: 'REG-LK', lines: [], customer_id: null, cashier_id: NIL_CASHIER });
    svc.startSale.mockResolvedValue(empty);
    const first = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(first);
    await settle(first);
    expect(svc.startSale).toHaveBeenCalledTimes(1);
    first.remove();
    svc.getSale.mockResolvedValue(empty);
    const again = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(again);
    await settle(again);
    expect(svc.getSale).toHaveBeenCalledWith('empty-1');
    expect(svc.startSale).toHaveBeenCalledTimes(1);
    expect(q(again, 'pos-header').textContent).toContain('S-1');
    again.remove();
  });

  it('opens a new sale when the remembered one has lines or is no longer open', async () => {
    serve([lk]);
    svc.startSale.mockResolvedValue(sale({ id: 'empty-1', register_id: 'REG-LK', lines: [], cashier_id: NIL_CASHIER }));
    const first = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(first);
    await settle(first);
    first.remove();
    svc.getSale.mockResolvedValue(sale({ id: 'empty-1', register_id: 'REG-LK', cashier_id: NIL_CASHIER }));
    const again = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(again);
    await settle(again);
    expect(svc.startSale).toHaveBeenCalledTimes(2);
    again.remove();
  });
  const mountLk = async () => {
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    return el;
  };

  it('does not hand one cashier the empty sale another cashier left on the register', async () => {
    serve([lk]);
    authCustody.signInWithToken(tokenFor(CASHIER_A));
    const aSale = sale({ id: 'a-empty', register_id: 'REG-LK', lines: [], cashier_id: CASHIER_A });
    svc.startSale.mockResolvedValue(aSale);
    (await mountLk()).remove();
    expect(svc.startSale).toHaveBeenCalledTimes(1);
    authCustody.signOut();
    authCustody.signInWithToken(tokenFor(CASHIER_B));
    const bSale = sale({ id: 'b-new', register_id: 'REG-LK', lines: [], cashier_id: CASHIER_B });
    svc.startSale.mockResolvedValue(bSale);
    svc.getSale.mockResolvedValue(aSale);
    const el = await mountLk();
    expect(svc.getSale).not.toHaveBeenCalledWith('a-empty');
    expect(svc.startSale).toHaveBeenCalledTimes(2);
    // signing out forgot A's remembered sale, so A starts afresh
    el.remove();
    authCustody.signOut();
    expect(localStorage.getItem(`gable.pos.sale.${CASHIER_A}.REG-LK`)).toBeNull();
    authCustody.signInWithToken(tokenFor(CASHIER_A));
    svc.getSale.mockClear();
    (await mountLk()).remove();
    expect(svc.getSale).not.toHaveBeenCalled();
    expect(svc.startSale).toHaveBeenCalledTimes(3);
  });

  it('does not reuse a sale remembered under another cashier even when the server hands it back', async () => {
    serve([lk]);
    authCustody.signInWithToken(tokenFor(CASHIER_B));
    localStorage.setItem(`gable.pos.sale.${CASHIER_B}.REG-LK`, 'a-empty');
    svc.getSale.mockResolvedValue(sale({ id: 'a-empty', register_id: 'REG-LK', lines: [], cashier_id: CASHIER_A }));
    svc.startSale.mockResolvedValue(sale({ id: 'b-new', register_id: 'REG-LK', lines: [], cashier_id: CASHIER_B }));
    (await mountLk()).remove();
    expect(svc.startSale).toHaveBeenCalledTimes(1);
  });

  it('does not reuse an empty sale that belongs to another till session', async () => {
    serve([lk]);
    authCustody.signInWithToken(tokenFor(CASHIER_A));
    const old = sale({ id: 'old-till', register_id: 'REG-LK', lines: [], cashier_id: CASHIER_A, till_session_id: 'previous-till' });
    localStorage.setItem(`gable.pos.sale.${CASHIER_A}.REG-LK`, 'old-till');
    svc.getSale.mockResolvedValue(old);
    svc.startSale.mockResolvedValue(sale({ id: 'fresh', register_id: 'REG-LK', lines: [], cashier_id: CASHIER_A }));
    (await mountLk()).remove();
    expect(svc.getSale).toHaveBeenCalledWith('old-till');
    expect(svc.startSale).toHaveBeenCalledTimes(1);
  });

  it('opens no sale until the till is open, and opening the till starts exactly one', async () => {
    serve([lk]);
    svc.currentTill.mockResolvedValue(null);
    svc.openTill.mockResolvedValue({ ...OPEN_TILL, register_id: 'REG-LK' });
    svc.startSale.mockResolvedValue(sale({ id: 'first', register_id: 'REG-LK', lines: [], cashier_id: NIL_CASHIER }));
    const el = await mountLk();
    expect(svc.startSale).not.toHaveBeenCalled();
    const openBtn = [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Open Till') as HTMLElement;
    openBtn.click();
    await el.updateComplete;
    const float = el.querySelector('input[type="number"]') as HTMLInputElement;
    float.value = '200.00';
    float.dispatchEvent(new Event('input'));
    await el.updateComplete;
    const confirm = [...el.querySelectorAll('button')].filter((b) => b.textContent?.trim() === 'Open Till').pop() as HTMLElement;
    confirm.click();
    await settle(el);
    expect(svc.openTill).toHaveBeenCalledTimes(1);
    expect(svc.startSale).toHaveBeenCalledTimes(1);
    el.remove();
  });

  it('shows Void and Return disabled on the empty new sale that follows a completed one', async () => {
    serve([lk]);
    const done = sale({ id: 'done', register_id: 'REG-LK', status: 'completed', cashier_id: NIL_CASHIER });
    svc.startSale.mockResolvedValue(done);
    const el = await mountLk();
    // a completed sale this visit did not complete is not offered either
    expect((q(el, 'pos-void') as HTMLButtonElement).disabled).toBe(true);
    (el as unknown as { _completed: Sale })._completed = done;
    await el.updateComplete;
    expect((q(el, 'pos-void') as HTMLButtonElement).disabled).toBe(false);
    svc.startSale.mockResolvedValue(sale({ id: 'next', register_id: 'REG-LK', lines: [], cashier_id: NIL_CASHIER }));
    const newSale = [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'New Sale') as HTMLElement;
    newSale.click();
    await settle(el);
    expect(q(el, 'pos-header').textContent).toContain('open');
    // the new open sale offers neither: they live on the completed sale's card
    expect(q(el, 'pos-complete')).toBeNull();
    expect(q(el, 'pos-void')).toBeNull();
    el.remove();
  });
});

describe('the counter before the till opens, and after a sale completes', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  async function mountNoTill(): Promise<POSTerminal> {
    svc.listRegisters.mockResolvedValue([REG_01]);
    svc.currentTill.mockResolvedValue(null);
    svc.searchProducts.mockResolvedValue([
      { product_id: 'p1', sku: 'SKU-1', description: 'Item 1', uom: 'EA', unit_price_cents: 500, in_stock: '9' },
    ]);
    svc.findCustomers.mockResolvedValue([account]);
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    return el;
  }
  const alertText = (el: Element) => el.querySelector('[role="alert"]')?.textContent ?? '';
  const answered = (el: POSTerminal) => {
    expect(alertText(el)).toContain('Open the till to start a sale');
    expect(document.activeElement?.id).toBe('pos-open-till');
    expect(svc.startSale).not.toHaveBeenCalled();
    expect(svc.addItem).not.toHaveBeenCalled();
    expect(svc.attachCustomer).not.toHaveBeenCalled();
  };

  it('says the till must be open in the empty state', async () => {
    const el = await mountNoTill();
    expect(q(el, 'catalog-closed').textContent?.trim()).toBe('Open the till to start a sale');
    el.remove();
  });

  it('locks and dims the catalogue, customer find and Payment card with no till', async () => {
    const el = await mountNoTill();
    expect((el.querySelector('#pos-customer-search') as HTMLInputElement).disabled).toBe(true);
    for (const id of ['catalog-search', 'catalog-qty', 'catalog-add']) {
      expect((q(el, id) as HTMLInputElement).disabled).toBe(true);
    }
    expect(q(el, 'pos-items').className).toContain('opacity-60');
    expect(q(el, 'panel-payment').className).toContain('opacity-50');
    expect(q(el, 'panel-customer').textContent).not.toContain('Open the till to start a sale');
    // Void and Return belong to a completed sale only: with no sale done, neither is offered
    expect(q(el, 'pos-void')).toBeNull();
    el.remove();
  });

  it('answers a product added with no till', async () => {
    const el = await mountNoTill();
    await (el as unknown as { _addItem(p: unknown, q: string): Promise<void> })._addItem(
      { product_id: 'p1', sku: 'SKU-1', description: 'Item 1', uom: 'EA', unit_price_cents: 500, in_stock: '9' }, '1');
    await el.updateComplete;
    answered(el);
    el.remove();
  });

  it('answers picking a customer with no till', async () => {
    const el = await mountNoTill();
    await (el as unknown as { _chooseCustomer(c: CounterCustomer): Promise<void> })._chooseCustomer(account);
    await el.updateComplete;
    answered(el);
    el.remove();
  });

  it('dims Void and Return as the locked tenders are, and shows the tender unselected with no till', async () => {
    const el = await mountNoTill();
    expect(q(el, 'pos-void')).toBeNull();
    const pay = q(el, 'pos-pay');
    expect(pay.className).toContain('opacity-40');
    expect(pay.className).toContain('border-white/10');
    expect(pay.className).not.toContain('hover:');
    expect(q(el, 'tender-cash').className).not.toContain('border-gable-green');
    expect(q(el, 'tender-cash').getAttribute('aria-pressed')).toBe('false');
    expect(el.textContent?.split('Open the till to start a sale').length).toBe(2);
    el.remove();
  });

  it('cannot press a tender while no till is open', async () => {
    const el = await mountNoTill();
    (el as unknown as { _sale: Sale })._sale = sale();
    await el.updateComplete;
    for (const m of ['cash', 'check', 'card', 'account']) {
      expect((q(el, `tender-${m}`) as HTMLButtonElement).disabled).toBe(true);
    }
    (el as unknown as { _chooseMethod(m: string): void })._chooseMethod('cash');
    expect((el as unknown as { _tenderMethod: string | null })._tenderMethod).toBeNull();
    el.remove();
  });

  describe('a completed sale', () => {
    async function completeOne(): Promise<POSTerminal> {
      svc.completeSale.mockResolvedValue(sale({ status: 'completed', number: 'POS-000001', change_cents: 1350 }));
      const el = await mount(sale());
      await chooseCash(el);
      await typeInto(el, '100.00');
      q(el, 'pos-pay').click();
      await new Promise((r) => setTimeout(r, 0));
      await el.updateComplete;
      return el;
    }

    it('stays on screen past the old timer, with its band, Void and Return', async () => {
      svc.completeSale.mockResolvedValue(sale({ status: 'completed', number: 'POS-000001', change_cents: 1350 }));
      const el = await mount(sale());
      await chooseCash(el);
      await typeInto(el, '100.00');
      // fake timers go in before Pay, so a timer the completion arms is one
      // this test advances
      vi.useFakeTimers({ toFake: ['setTimeout'] });
      try {
        q(el, 'pos-pay').click();
        await vi.advanceTimersByTimeAsync(0);
        await el.updateComplete;
        await vi.advanceTimersByTimeAsync(5000);
        await el.updateComplete;
        expect(svc.startSale).toHaveBeenCalledTimes(1);
        expect(q(el, 'pos-complete').textContent).toContain('POS-000001');
        expect(q(el, 'pos-complete').textContent).toContain('change $13.50');
        expect(q(el, 'pos-header').textContent).toContain('completed');
        expect((q(el, 'pos-void') as HTMLButtonElement).disabled).toBe(false);
        const ret = [...q(el, 'pos-complete').querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Return') as HTMLButtonElement;
        expect(ret.disabled).toBe(false);
        ret.click();
        await el.updateComplete;
        expect(el.textContent).toContain('Return');
        expect((el as unknown as { _showReturn: boolean })._showReturn).toBe(true);
        el.remove();
      } finally {
        vi.useRealTimers();
      }
    });

    const toggle = (el: Element, key: string) => q(el, `panel-${key}-toggle`) as HTMLButtonElement;

    it('makes the card\'s New sale the one primary, marks every step done and locks Change, purchase order and job', async () => {
      const el = await completeOne();
      const newSale = [...q(el, 'pos-header').querySelectorAll('button')].find((x) => x.textContent?.trim() === 'New Sale') as HTMLElement;
      expect(newSale.className).not.toContain('bg-gable-green');
      const cardNew = [...q(el, 'pos-complete').querySelectorAll('button')].find((x) => x.textContent?.trim() === 'New sale') as HTMLElement;
      expect(cardNew.className).toContain('bg-gable-green');
      const closeTill = [...q(el, 'pos-header').querySelectorAll('button')].find((x) => x.textContent?.trim() === 'Close Till') as HTMLElement;
      expect(closeTill.className).not.toContain('bg-gable-green');
      expect(q(el, 'progress-summary').getAttribute('data-state')).toBe('done');
      expect(q(el, 'progress-summary').textContent).toContain('\u2713');
      expect(toggle(el, 'summary').textContent).toContain('\u2713');
      // a finished sale's done lines show the check and the value, no Change
      for (const key of ['customer', 'payment', 'summary']) {
        expect(toggle(el, key).textContent).not.toContain('Change');
        expect(toggle(el, key).disabled).toBe(true);
      }
      // the clear control only exists once a customer is named; a walk in's own button is the choice
      const change = [...q(el, 'pos-customer').querySelectorAll('button:not([data-testid="pos-walk-in"])')].find((x) => x.textContent?.trim() === 'Walk-in sale') as HTMLButtonElement | undefined;
      if (change) expect(change.disabled).toBe(true);
      const po = el.querySelector('#pos-po') as HTMLInputElement | null;
      if (po) expect(po.disabled).toBe(true);
      const job = el.querySelector('#pos-job') as HTMLSelectElement | null;
      if (job) expect(job.disabled).toBe(true);
      expect(q(el, 'pos-complete')).toBeTruthy();
      el.remove();
    });

    it('gives way to a fresh sale after a void, and for two quick adds starts only one', async () => {
      const el = await completeOne();
      const anyEl = el as unknown as { _sale: Sale; _addItem(p: unknown, q: string): Promise<void> };
      anyEl._sale = sale({ status: 'voided', number: 'POS-000001' });
      await el.updateComplete;
      svc.startSale.mockClear();
      svc.startSale.mockResolvedValue(sale({ id: 's2', number: 'S-2', lines: [] }));
      svc.addItem.mockResolvedValue(sale({ id: 's2', number: 'S-2', lines: [line('z', 700)] as unknown as Sale['lines'] }));
      const p = { product_id: 'p-z', sku: 'SKU-z', description: 'Item z', uom: 'EA', unit_price_cents: 700, in_stock: '5' };
      await Promise.all([anyEl._addItem(p, '1'), anyEl._addItem(p, '1')]);
      expect(svc.startSale).toHaveBeenCalledTimes(1);
      expect(svc.addItem).toHaveBeenCalledWith('s2', 'p-z', '1', 'EA');
      el.remove();
    });

    it('New Sale then a quick add starts one sale and puts the line on the sale on screen', async () => {
      const el = await completeOne();
      const anyEl = el as unknown as { _addItem(p: unknown, q: string): Promise<void> };
      svc.startSale.mockClear();
      svc.startSale.mockImplementationOnce(() => new Promise((r) => setTimeout(() => r(sale({ id: 'new1', number: 'S-N1', lines: [] })), 40)));
      svc.startSale.mockResolvedValue(sale({ id: 'new2', number: 'S-N2', lines: [] }));
      svc.addItem.mockImplementation(async (id: string) => sale({ id, number: id === 'new1' ? 'S-N1' : 'S-N2', lines: [line('z', 700)] as unknown as Sale['lines'] }));
      const newSale = [...q(el, 'pos-header').querySelectorAll('button')].find((x) => x.textContent?.trim() === 'New Sale') as HTMLElement;
      newSale.click();
      const p = { product_id: 'p-z', sku: 'SKU-z', description: 'Item z', uom: 'EA', unit_price_cents: 700, in_stock: '5' };
      await anyEl._addItem(p, '1');
      await new Promise((r) => setTimeout(r, 60));
      await el.updateComplete;
      expect(svc.startSale).toHaveBeenCalledTimes(1);
      expect(svc.addItem).toHaveBeenCalledTimes(1);
      expect(svc.addItem).toHaveBeenCalledWith('new1', 'p-z', '1', 'EA');
      expect(q(el, 'pos-header').textContent).toContain('S-N1');
      expect(q(el, 'pos-lines').textContent).toContain('Item z');
      el.remove();
    });

    it('New Sale over an open sale then a quick add puts the line on the new sale, not the old one', async () => {
      const el = await mount(sale({ id: 'old1', number: 'S-OLD', lines: [line('a', 500)] as unknown as Sale['lines'] }));
      const anyEl = el as unknown as { _addItem(p: unknown, q: string): Promise<void> };
      svc.startSale.mockClear();
      svc.addItem.mockClear();
      svc.startSale.mockImplementationOnce(() => new Promise((r) => setTimeout(() => r(sale({ id: 'new1', number: 'S-N1', lines: [] })), 40)));
      svc.addItem.mockImplementation(async (id: string) => sale({ id, number: id === 'new1' ? 'S-N1' : 'S-OLD', lines: [line('z', 700)] as unknown as Sale['lines'] }));
      const newSale = [...q(el, 'pos-header').querySelectorAll('button')].find((x) => x.textContent?.trim() === 'New Sale') as HTMLElement;
      newSale.click();
      const p = { product_id: 'p-z', sku: 'SKU-z', description: 'Item z', uom: 'EA', unit_price_cents: 700, in_stock: '5' };
      await anyEl._addItem(p, '1');
      await new Promise((r) => setTimeout(r, 60));
      await el.updateComplete;
      expect(svc.startSale).toHaveBeenCalledTimes(1);
      expect(svc.addItem).toHaveBeenCalledTimes(1);
      expect(svc.addItem).toHaveBeenCalledWith('new1', 'p-z', '1', 'EA');
      expect(q(el, 'pos-header').textContent).toContain('S-N1');
      el.remove();
    });

    it('locks the walk-in control, purchase order and job on a completed account sale', async () => {
      const withJob = { ...account, jobs: [{ id: 'j1', name: 'Back patio' }] } as unknown as CounterCustomer;
      svc.completeSale.mockResolvedValue(sale({ status: 'completed', number: 'POS-000001', customer_id: 'cu1' }));
      const el = await mount(sale({ customer_id: 'cu1' }));
      (el as unknown as { _customer: CounterCustomer | null })._customer = withJob;
      await el.updateComplete;
      await chooseCash(el);
      await typeInto(el, '100.00');
      q(el, 'pos-pay').click();
      await new Promise((r) => setTimeout(r, 0));
      await el.updateComplete;
      const change = [...q(el, 'pos-customer').querySelectorAll('button')].find((x) => x.textContent?.trim() === 'Walk-in sale') as HTMLButtonElement;
      expect(change.disabled).toBe(true);
      expect(change.title).toBe('Start a new sale to change the customer');
      expect((el.querySelector('#pos-po') as HTMLInputElement).disabled).toBe(true);
      expect((el.querySelector('#pos-job') as HTMLSelectElement).disabled).toBe(true);
      el.remove();
    });

    it('ignores an attach answer for a sale no longer on screen or an older revision', async () => {
      const el = await completeOne();
      const anyEl = el as unknown as { _customer: CounterCustomer | null; _sale: Sale; _poText: string; _saveAccountRefs(): Promise<void> };
      anyEl._sale = sale({ id: 'open1', status: 'open', revision: 5 } as Partial<Sale>);
      anyEl._customer = account;
      anyEl._poText = 'PO-1';
      let answer!: (s: Sale) => void;
      svc.attachCustomer.mockImplementationOnce(() => new Promise((r) => { answer = r; }));
      const saving = anyEl._saveAccountRefs();
      // the screen moves on to another sale before the answer
      const moved = sale({ id: 'open2', number: 'S-2', status: 'open', revision: 1 } as Partial<Sale>);
      anyEl._sale = moved;
      answer(sale({ id: 'open1', status: 'open', revision: 6 } as Partial<Sale>));
      await saving;
      expect(anyEl._sale).toBe(moved);
      // the same sale, an older revision than the one on screen
      const newer = sale({ id: 'open2', status: 'open', revision: 9 } as Partial<Sale>);
      anyEl._sale = newer;
      svc.attachCustomer.mockResolvedValueOnce(sale({ id: 'open2', status: 'open', revision: 8 } as Partial<Sale>));
      await anyEl._saveAccountRefs();
      expect(anyEl._sale).toBe(newer);
      el.remove();
    });

    it('never sends a customer change for a completed or voided sale', async () => {
      const el = await completeOne();
      const anyEl = el as unknown as { _customer: CounterCustomer | null; _sale: Sale; _saveAccountRefs(): Promise<void>; _clearCustomer(): Promise<void> };
      anyEl._customer = account;
      for (const status of ['completed', 'voided'] as const) {
        anyEl._sale = sale({ status });
        await el.updateComplete;
        await anyEl._saveAccountRefs();
        await anyEl._clearCustomer();
        expect((el.querySelector('#pos-po') as HTMLInputElement).disabled).toBe(true);
      }
      expect(svc.attachCustomer).not.toHaveBeenCalled();
      el.remove();
    });

    it('gives way to a new sale carrying the product added next', async () => {
      const el = await completeOne();
      svc.startSale.mockResolvedValue(sale({ id: 's2', number: 'S-2', lines: [] }));
      svc.addItem.mockResolvedValue(sale({ id: 's2', number: 'S-2', lines: [line('z', 700)] as unknown as Sale['lines'] }));
      await (el as unknown as { _addItem(p: unknown, q: string): Promise<void> })._addItem(
        { product_id: 'p-z', sku: 'SKU-z', description: 'Item z', uom: 'EA', unit_price_cents: 700, in_stock: '5' }, '1');
      await el.updateComplete;
      expect(svc.startSale).toHaveBeenCalledTimes(2);
      expect(svc.addItem).toHaveBeenCalledWith('s2', 'p-z', '1', 'EA');
      expect(q(el, 'pos-header').textContent).toContain('S-2');
      expect(q(el, 'pos-lines').textContent).toContain('Item z');
      expect(q(el, 'pos-void')).toBeNull();
      el.remove();
    });

    it('gives way to a new sale for the customer named next', async () => {
      const el = await completeOne();
      svc.startSale.mockResolvedValue(sale({ id: 's2', number: 'S-2', lines: [] }));
      svc.attachCustomer.mockResolvedValue(sale({ id: 's2', number: 'S-2', lines: [], customer_id: 'cu1' }));
      (el as unknown as { _custResults: unknown[] })._custResults = [account];
      await el.updateComplete;
      expect((el.querySelector('#pos-customer-search') as HTMLInputElement).disabled).toBe(false);
      (q(el, 'pos-customer-results').querySelector('button') as HTMLElement).click();
      await new Promise((r) => setTimeout(r, 0));
      await el.updateComplete;
      expect(svc.startSale).toHaveBeenCalledTimes(2);
      expect(svc.attachCustomer).toHaveBeenCalledWith('s2', { customer_id: 'cu1' });
      expect(q(el, 'pos-customer-name').textContent).toBe('Test Builders');
      el.remove();
    });
  });
});

describe('closing the till', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('asks first over a sale with lines, and declining keeps the till and the sale', async () => {
    const el = await mount(sale());
    const close = [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Close Till') as HTMLElement;
    close.click();
    await el.updateComplete;
    expect(openAppDialog()).toBeTruthy();
    await answerAppDialog(false);
    await el.updateComplete;
    expect(svc.tillReport).not.toHaveBeenCalled();
    expect(q(el, 'pos-till-state').textContent).toContain('Till open');
    expect((el as unknown as { _sale: Sale })._sale.id).toBe('s1');
    el.remove();
  });

  it('clears the sale and hides New Sale once the till is closed', async () => {
    const el = await mount(sale({ lines: [] }));
    const inner = el as unknown as { _confirmCloseTill(): Promise<void>; _till: unknown; _counts: Record<string, string> };
    svc.closeTill.mockResolvedValue({ variance_cents: 0 });
    await inner._confirmCloseTill();
    await el.updateComplete;
    expect([...el.querySelectorAll('button')].some((b) => b.textContent?.trim() === 'New Sale')).toBe(false);
    expect(q(el, 'pos-till-state').textContent).toContain('No till');
    expect((el as unknown as { _sale: Sale | null })._sale).toBeNull();
    el.remove();
  });
});

describe('no tender until the cashier chooses one', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const pressed = (el: Element) => ['cash', 'check', 'card', 'account']
    .filter((m) => q(el, `tender-${m}`)?.getAttribute('aria-pressed') === 'true');
  const settle = () => new Promise((r) => setTimeout(r, 0));

  it('starts a sale with a line and the till open with no tender pressed and Pay off', async () => {
    const el = await mount(sale());
    expect(pressed(el)).toEqual([]);
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    expect(q(el, 'pos-pay').textContent).toContain('Choose how the customer pays');
    expect((el.querySelector('#pos-tender-amount') as HTMLInputElement).disabled).toBe(true);
    expect((q(el, 'pos-add-tender') as HTMLButtonElement).disabled).toBe(true);
    expect(q(el, 'panel-summary-body').textContent).toContain('None yet');
    el.remove();
  });

  it('does not complete the sale when Pay is pressed with no tender chosen', async () => {
    const el = await mount(sale());
    q(el, 'pos-pay').click();
    await settle();
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });

  it('sends one account tender for a named account customer who chose Charge to account', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale({ customer_id: 'cu1' }));
    (el as unknown as { _customer: CounterCustomer })._customer = account;
    await el.updateComplete;
    expect(pressed(el)).toEqual([]);
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    q(el, 'tender-account').click();
    await el.updateComplete;
    expect(pressed(el)).toEqual(['account']);
    q(el, 'pos-pay').click();
    await settle();
    expect(svc.completeSale).toHaveBeenCalledTimes(1);
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [{ method: 'account', amount_cents: 8650 }], []);
    el.remove();
  });

  it('asks for a tender again after a completed sale and New Sale', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    q(el, 'tender-card').click();
    await el.updateComplete;
    q(el, 'pos-pay').click();
    await settle();
    await el.updateComplete;
    svc.startSale.mockResolvedValue(sale({ id: 's2', lines: [line('c', 5000)] as unknown as Sale['lines'], total_cents: 5000 }));
    const newSale = [...q(el, 'pos-header').querySelectorAll('button')].find((x) => x.textContent?.trim() === 'New Sale') as HTMLElement;
    newSale.click();
    await settle();
    await el.updateComplete;
    expect(pressed(el)).toEqual([]);
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    el.remove();
  });

  it('asks for the next tender again after Add as split tender', async () => {
    const el = await mount(sale());
    q(el, 'tender-cash').click();
    await el.updateComplete;
    await typeInto(el, '50.00');
    q(el, 'pos-add-tender').click();
    await el.updateComplete;
    expect(pressed(el)).toEqual([]);
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    el.remove();
  });
});

describe('the resets after Clear tenders and Clear sale', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const pressed = (el: Element) => ['cash', 'check', 'card', 'account']
    .filter((m) => q(el, `tender-${m}`)?.getAttribute('aria-pressed') === 'true');
  const settle = () => new Promise((r) => setTimeout(r, 0));

  it('puts the chosen method back to none on Clear tenders', async () => {
    const el = await mount(sale());
    await chooseCash(el);
    await typeInto(el, '50.00');
    q(el, 'pos-add-tender').click();
    await el.updateComplete;
    q(el, 'tender-card').click();
    await el.updateComplete;
    expect(pressed(el)).toEqual(['card']);
    const clear = [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Clear tenders') as HTMLElement;
    clear.click();
    await el.updateComplete;
    expect(pressed(el)).toEqual([]);
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    el.remove();
  });

  it('puts a tender that was pressed but not added back to none on Clear sale', async () => {
    svc.removeItem
      .mockResolvedValueOnce(sale({ lines: [line('b', 3000)] as unknown as Sale['lines'] }))
      .mockResolvedValueOnce(sale({ lines: [], subtotal_cents: 0, tax_cents: 0, total_cents: 0 }));
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    q(el, 'tender-cash').click();
    await el.updateComplete;
    expect(pressed(el)).toEqual(['cash']);
    q(el, 'pos-clear-sale').click();
    await settle();
    await answerAppDialog(true);
    await settle();
    await el.updateComplete;
    (el as unknown as { _sale: Sale })._sale = sale({
      lines: [line('n', 5000)] as unknown as Sale['lines'], subtotal_cents: 5000, tax_cents: 0, total_cents: 5000,
    });
    await el.updateComplete;
    expect(pressed(el)).toEqual([]);
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    q(el, 'pos-pay').click();
    await settle();
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });

  it('drops the added tenders on Clear sale, so the next lines are not paid on them', async () => {
    svc.removeItem
      .mockResolvedValueOnce(sale({ lines: [line('b', 3000)] as unknown as Sale['lines'] }))
      .mockResolvedValueOnce(sale({ lines: [], subtotal_cents: 0, tax_cents: 0, total_cents: 0 }));
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await chooseCash(el);
    await typeInto(el, '86.50');
    q(el, 'pos-add-tender').click();
    await el.updateComplete;
    q(el, 'pos-clear-sale').click();
    await settle();
    await answerAppDialog(true);
    await settle();
    await el.updateComplete;
    // a new line lands on the emptied sale
    (el as unknown as { _sale: Sale })._sale = sale({
      lines: [line('n', 5000)] as unknown as Sale['lines'], subtotal_cents: 5000, tax_cents: 0, total_cents: 5000,
    });
    await el.updateComplete;
    expect(pressed(el)).toEqual([]);
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    q(el, 'pos-pay').click();
    await settle();
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });
});

describe('a changed customer starts the tender over', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  type Inner = {
    _customer: CounterCustomer | null; _tenderMethod: string | null; _pendingTenders: unknown[];
    _chooseCustomer(c: CounterCustomer): Promise<void>; _clearCustomer(): Promise<void>;
  };
  const inner = (el: POSTerminal) => el as unknown as Inner;
  const withTender = async () => {
    const el = await mount(sale({ lines: [], customer_id: 'cu1' }));
    inner(el)._customer = account;
    inner(el)._tenderMethod = 'account';
    inner(el)._pendingTenders = [{ method: 'cash', amount_cents: 1000 }];
    await el.updateComplete;
    return el;
  };

  it('clears the chosen and the added tenders when another customer is named', async () => {
    const el = await withTender();
    svc.attachCustomer.mockResolvedValue(sale({ lines: [], customer_id: 'cu2' }));
    await inner(el)._chooseCustomer({ ...account, id: 'cu2', name: 'Other Builders' } as CounterCustomer);
    expect(inner(el)._tenderMethod).toBeNull();
    expect(inner(el)._pendingTenders).toEqual([]);
    el.remove();
  });

  it('clears them when the sale goes back to a walk in', async () => {
    const el = await withTender();
    svc.attachCustomer.mockResolvedValue(sale({ lines: [], customer_id: null }));
    await inner(el)._clearCustomer();
    expect(inner(el)._tenderMethod).toBeNull();
    expect(inner(el)._pendingTenders).toEqual([]);
    el.remove();
  });
});

describe('a quantity on each counter line', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const byLabel = (el: Element, label: string) => el.querySelector(`[aria-label="${label}"]`) as HTMLElement;
  const settle = () => new Promise((r) => setTimeout(r, 0));
  const lineQty = (id: string, qty: string, cents: number) => ({ ...line(id, cents), quantity: qty });

  it('sends the change on the sale revision and shows the repriced line', async () => {
    svc.changeQuantity.mockResolvedValue(sale({
      revision: 4, subtotal_cents: 13000, tax_cents: 1054, total_cents: 14054,
      lines: [lineQty('a', '2', 10000), line('b', 3000)] as unknown as Sale['lines'],
    }));
    const el = await mount(sale());
    byLabel(el, 'Increase quantity of Item a').click();
    await settle();
    await el.updateComplete;
    expect(svc.changeQuantity).toHaveBeenCalledWith('s1', 'a', '2', 3);
    expect((byLabel(el, 'Quantity of Item a') as HTMLInputElement).value).toBe('2');
    expect(q(el, 'pos-lines').textContent).toContain('$100.00');
    expect(q(el, 'summary-total').textContent?.trim()).toBe('$140.54');
    el.remove();
  });

  it('turns minus off at one and sends one less above it', async () => {
    svc.changeQuantity.mockResolvedValue(sale({ lines: [lineQty('a', '2', 10000), line('b', 3000)] as unknown as Sale['lines'] }));
    const el = await mount(sale({ lines: [lineQty('a', '3', 15000), line('b', 3000)] as unknown as Sale['lines'] }));
    expect((byLabel(el, 'Decrease quantity of Item b') as HTMLButtonElement).disabled).toBe(true);
    const minus = byLabel(el, 'Decrease quantity of Item a') as HTMLButtonElement;
    expect(minus.disabled).toBe(false);
    minus.click();
    await settle();
    expect(svc.changeQuantity).toHaveBeenCalledWith('s1', 'a', '2', 3);
    el.remove();
  });

  it('commits a typed number on Enter and when the field is left', async () => {
    svc.changeQuantity.mockResolvedValue(sale({ lines: [lineQty('a', '12', 60000), line('b', 3000)] as unknown as Sale['lines'] }));
    const el = await mount(sale());
    const input = byLabel(el, 'Quantity of Item a') as HTMLInputElement;
    input.value = '12';
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    await settle();
    expect(svc.changeQuantity).toHaveBeenCalledTimes(1);
    expect(svc.changeQuantity).toHaveBeenCalledWith('s1', 'a', '12', 3);
    const second = byLabel(el, 'Quantity of Item b') as HTMLInputElement;
    second.value = '4';
    second.dispatchEvent(new Event('change'));
    await settle();
    expect(svc.changeQuantity).toHaveBeenCalledTimes(2);
    el.remove();
  });

  it('says why a change failed and puts the number back', async () => {
    svc.changeQuantity.mockRejectedValue(new Error('only 3 in stock at this yard'));
    const el = await mount(sale());
    const input = byLabel(el, 'Quantity of Item a') as HTMLInputElement;
    input.value = '9';
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    await settle();
    await el.updateComplete;
    expect(el.textContent).toContain('only 3 in stock at this yard');
    expect((byLabel(el, 'Quantity of Item a') as HTMLInputElement).value).toBe('1');
    el.remove();
  });

  it('sizes each control for touch', async () => {
    const el = await mount(sale());
    for (const l of ['Decrease quantity of Item a', 'Increase quantity of Item a', 'Quantity of Item a']) {
      expect(byLabel(el, l).className).toMatch(/min-h-11|h-11/);
    }
    el.remove();
  });
});

describe('the counter hotkeys and their pinned legend', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const settle = () => new Promise((r) => setTimeout(r, 0));
  const key = async (el: POSTerminal, k: string, mods: KeyboardEventInit = {}) => {
    const ev = new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...mods });
    document.dispatchEvent(ev);
    await el.updateComplete;
    return ev;
  };
  const pressed = (el: Element) => ['cash', 'check', 'card', 'account']
    .filter((m) => q(el, `tender-${m}`)?.getAttribute('aria-pressed') === 'true');

  it('pins a legend above the counter showing each key and its action', async () => {
    const el = await mount(sale());
    const legend = q(el, 'pos-hotkey-legend');
    expect(legend).toBeTruthy();
    expect(legend.compareDocumentPosition(q(el, 'pos-body')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(legend.textContent).toContain('F12');
    expect(legend.textContent).toContain('Pay');
    expect(legend.querySelectorAll('[data-action]').length).toBe(14);
    el.remove();
  });

  it('chooses a tender and pays from the keys', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await key(el, 'F12');
    expect(svc.completeSale).not.toHaveBeenCalled();
    await key(el, 'F8');
    expect(pressed(el)).toEqual(['card']);
    const ev = await key(el, 'F12');
    expect(ev.defaultPrevented).toBe(true);
    await settle();
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [{ method: 'card', amount_cents: 8650 }], []);
    el.remove();
  });

  it('moves between the panels with Next and Back', async () => {
    const el = await mount(sale());
    expect(q(el, 'panel-customer-body').hasAttribute('hidden')).toBe(false);
    await key(el, 'F4');
    expect(q(el, 'panel-payment-body').hasAttribute('hidden')).toBe(false);
    await key(el, 'F4', { shiftKey: true });
    expect(q(el, 'panel-customer-body').hasAttribute('hidden')).toBe(false);
    el.remove();
  });

  it('focuses the search, the customer lookup and the selected line\'s quantity', async () => {
    const el = await mount(sale());
    await key(el, 'F2');
    expect(document.activeElement).toBe(q(el, 'catalog-search'));
    await key(el, 'q', { altKey: true });
    expect(document.activeElement).toBe(el.querySelector('[aria-label="Quantity of Item b"]'));
    // the account is named before or after the lines (CP-10), so the lookup
    // key focuses the search either way
    await key(el, 'F3');
    expect(document.activeElement).toBe(el.querySelector('#pos-customer-search'));
    el.remove();
  });

  it('never fires while a dialog of the app is open, and a bare letter is only typing', async () => {
    const el = await mount(sale());
    (el as unknown as { _showTillOpen: boolean })._showTillOpen = true;
    await el.updateComplete;
    const ev = await key(el, 'F6');
    expect(pressed(el)).toEqual([]);
    expect(ev.defaultPrevented).toBe(false);
    (el as unknown as { _showTillOpen: boolean })._showTillOpen = false;
    await el.updateComplete;
    const typed = await key(el, 'c');
    expect(pressed(el)).toEqual([]);
    expect(typed.defaultPrevented).toBe(false);
    el.remove();
  });

  it('leaves a key the counter does not claim to the browser', async () => {
    const el = await mount(sale());
    const ev = await key(el, 'F5');
    expect(ev.defaultPrevented).toBe(false);
    el.remove();
  });
});

describe('the blind count at close', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('shows one row per method and posts the counted figures under lowercase keys', async () => {
    svc.tillReport.mockResolvedValue({
      sale_count: 2, sales_total_cents: 0, tax_total_cents: 0, change_cents: 0, tendered_by_method: {},
      expected_by_method: { CASH: 25000, CHECK: 4000, CARD: 9000 },
    });
    svc.closeTill.mockResolvedValue({
      session: { over_short_cents: 0, expected_by_method: {}, counted_by_method: {} },
      sale_count: 2, sales_total_cents: 0, tax_total_cents: 0, change_cents: 0,
    });
    const el = await mount(sale({ lines: [] }));
    const close = [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Close Till') as HTMLElement;
    close.click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    const labels = [...el.querySelectorAll('label')].map((l) => l.textContent?.trim()).filter((t) => ['Cash', 'Check', 'Card', 'CASH', 'CHECK', 'CARD'].includes(t ?? ''));
    expect(labels).toEqual(['Cash', 'Check', 'Card']);
    const inputs = [...el.querySelectorAll('input[type="number"]')] as HTMLInputElement[];
    expect(inputs.length).toBe(3);
    inputs[0].value = '250.00'; inputs[0].dispatchEvent(new Event('input'));
    inputs[1].value = '40.00'; inputs[1].dispatchEvent(new Event('input'));
    await el.updateComplete;
    const post = [...el.querySelectorAll('button')].find((b) => b.textContent?.includes('Post Count')) as HTMLElement;
    post.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(svc.closeTill).toHaveBeenCalledTimes(1);
    expect(svc.closeTill.mock.calls[0][1]).toEqual({ cash: 25000, check: 4000 });
    el.remove();
  });
});

describe('PR 150 fix round 1: quantity edits, controls and the blind count', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const settle = () => new Promise((r) => setTimeout(r, 0));
  const byLabel = (el: Element, label: string) => el.querySelector(`[aria-label="${label}"]`) as HTMLElement | null;
  const key = async (el: POSTerminal, k: string, init: KeyboardEventInit = {}) => {
    const ev = new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...init });
    (document.activeElement ?? document.body).dispatchEvent(ev);
    await el.updateComplete;
    return ev;
  };
  const withType = (id: string, type: string, cents: number, qty = '1') =>
    ({ ...line(id, cents), line_type: type, quantity: qty });

  it('commits a typed quantity before a key acts, so Pay never completes at the old quantity', async () => {
    svc.changeQuantity.mockResolvedValue(sale({ revision: 4, lines: [{ ...line('a', 25000), quantity: '5' }, line('b', 3000)] as unknown as Sale['lines'] }));
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    q(el, 'tender-card').click();
    await el.updateComplete;
    const input = byLabel(el, 'Quantity of Item a') as HTMLInputElement;
    input.focus();
    input.value = '5';
    input.dispatchEvent(new Event('input'));
    await key(el, 'F12');
    await settle();
    expect(svc.changeQuantity).toHaveBeenCalledWith('s1', 'a', '5', 3);
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });

  it('keeps Pay and the money keys off while a quantity change is on its way', async () => {
    svc.changeQuantity.mockReturnValue(new Promise(() => {}));
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    q(el, 'tender-card').click();
    await el.updateComplete;
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    (byLabel(el, 'Increase quantity of Item a') as HTMLButtonElement).click();
    await el.updateComplete;
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    (document.activeElement as HTMLElement | null)?.blur();
    await key(el, 'F12');
    await key(el, 'F6');
    await settle();
    expect(svc.completeSale).not.toHaveBeenCalled();
    expect(q(el, 'tender-cash').getAttribute('aria-pressed')).toBe('false');
    el.remove();
  });

  it('gives a kit line and a charge line the quantity control, and a component none', async () => {
    const lines = [
      withType('k', 'kit', 9000),
      { ...withType('c1', 'component', 0), parent_line_id: 'k' },
      withType('f', 'charge', 2500),
    ];
    const el = await mount(sale({ lines: lines as unknown as Sale['lines'] }));
    expect(byLabel(el, 'Quantity of Item k')).toBeTruthy();
    expect(byLabel(el, 'Quantity of Item f')).toBeTruthy();
    expect(byLabel(el, 'Quantity of Item c1')).toBeNull();
    el.remove();
  });

  it('picks the last line that has a control for Alt+Q, and a plus press selects its line', async () => {
    const lines = [withType('a', 'product', 5000), withType('t', 'text', 0)];
    svc.changeQuantity.mockResolvedValue(sale({ lines: [{ ...withType('a', 'product', 10000), quantity: '2' }, lines[1]] as unknown as Sale['lines'] }));
    const el = await mount(sale({ lines: lines as unknown as Sale['lines'] }));
    await key(el, 'q', { altKey: true, code: 'KeyQ' });
    expect(document.activeElement).toBe(byLabel(el, 'Quantity of Item a'));
    el.remove();

    const two = await mount(sale({ lines: [withType('a', 'product', 5000), withType('b', 'product', 3000)] as unknown as Sale['lines'] }));
    (byLabel(two, 'Increase quantity of Item a') as HTMLButtonElement).click();
    await settle();
    await two.updateComplete;
    (document.activeElement as HTMLElement | null)?.blur();
    await key(two, 'q', { altKey: true, code: 'KeyQ' });
    expect(document.activeElement).toBe(byLabel(two, 'Quantity of Item a'));
    two.remove();
  });

  it('counts only what the drawer holds: no Account row, and Post sends only cash', async () => {
    svc.tillReport.mockResolvedValue({
      sale_count: 1, sales_total_cents: 0, tax_total_cents: 0, change_cents: 0, tendered_by_method: {},
      expected_by_method: { CASH: 25000, ACCOUNT: 12000 },
    });
    svc.closeTill.mockResolvedValue({
      session: { over_short_cents: 0, expected_by_method: {}, counted_by_method: {} },
      sale_count: 1, sales_total_cents: 0, tax_total_cents: 0, change_cents: 0,
    });
    const el = await mount(sale({ lines: [] }));
    const close = [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Close Till') as HTMLElement;
    close.click();
    await settle();
    await el.updateComplete;
    const inputs = [...el.querySelectorAll('input[type="number"]')] as HTMLInputElement[];
    expect(inputs.length).toBe(1);
    inputs[0].value = '250.00';
    inputs[0].dispatchEvent(new Event('input'));
    await el.updateComplete;
    ([...el.querySelectorAll('button')].find((b) => b.textContent?.includes('Post Count')) as HTMLElement).click();
    await settle();
    expect(svc.closeTill.mock.calls[0][1]).toEqual({ cash: 25000 });
    el.remove();
  });

  it('is silenced by any element with role dialog, not only the till dialogs', async () => {
    const el = await mount(sale());
    const dialog = document.createElement('div');
    dialog.setAttribute('role', 'dialog');
    document.body.appendChild(dialog);
    const ev = await key(el, 'F6');
    expect(q(el, 'tender-cash').getAttribute('aria-pressed')).toBe('false');
    expect(ev.defaultPrevented).toBe(false);
    dialog.remove();
    el.remove();
  });
});

describe('a quantity change under tenders already added', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const settle = () => new Promise((r) => setTimeout(r, 0));
  const reduced = () => sale({
    revision: 4, subtotal_cents: 5000, tax_cents: 0, total_cents: 5000,
    lines: [{ ...line('a', 5000), quantity: '1' }] as unknown as Sale['lines'],
  });
  const addTender = async (el: POSTerminal, method: 'cash' | 'card', amount: string) => {
    q(el, `tender-${method}`).click();
    await el.updateComplete;
    await typeInto(el, amount);
    q(el, 'pos-add-tender').click();
    await el.updateComplete;
  };
  const start = async () => mount(sale({
    lines: [{ ...line('a', 8650), quantity: '2' }] as unknown as Sale['lines'], subtotal_cents: 8000, tax_cents: 650, total_cents: 8650,
  }));
  const minus = async (el: POSTerminal) => {
    (el.querySelector('[aria-label="Decrease quantity of Item a"]') as HTMLButtonElement).click();
    await settle();
    await el.updateComplete;
  };

  it('shows the amount owed at once when the total rises over added tenders', async () => {
    const el = await start();
    await addTender(el, 'cash', '50.00');
    expect(q(el, 'pos-amount-due').textContent?.trim()).toBe('$36.50');
    svc.changeQuantity.mockResolvedValue(sale({ revision: 4, total_cents: 12000, subtotal_cents: 12000, tax_cents: 0,
      lines: [{ ...line('a', 12000), quantity: '3' }] as unknown as Sale['lines'] }));
    (el.querySelector('[aria-label="Increase quantity of Item a"]') as HTMLButtonElement).click();
    await settle();
    await el.updateComplete;
    expect(q(el, 'pos-amount-due').textContent?.trim()).toBe('$70.00');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    el.remove();
  });

  it('turns Pay on with the change due when cash only is added above a reduced total', async () => {
    svc.changeQuantity.mockResolvedValue(reduced());
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await start();
    await addTender(el, 'cash', '40.00');
    await addTender(el, 'cash', '46.50');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    await minus(el);
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    expect(q(el, 'pos-change').textContent).toContain('$36.50');
    q(el, 'pos-pay').click();
    await settle();
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 4, [
      { method: 'cash', amount_cents: 4000 }, { method: 'cash', amount_cents: 4650 },
    ], []);
    el.remove();
  });

  it('keeps Pay off, in words, when a non cash tender is added above a reduced total', async () => {
    svc.changeQuantity.mockResolvedValue(reduced());
    const el = await start();
    await addTender(el, 'card', '86.50');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    await minus(el);
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    expect(q(el, 'pos-tenders-over').textContent).toContain('The added tenders are more than the sale now: clear them and choose again');
    q(el, 'pos-pay').click();
    await settle();
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });
});

describe('PR 150 fix round 2: the Z table, the guard in _pay and a typed non cash tender', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const settle = () => new Promise((r) => setTimeout(r, 0));

  it('leaves account out of the closed drawer table and names methods in words', async () => {
    const el = await mount(sale({ lines: [] }));
    const inner = el as unknown as { _closeResult: unknown; _showTillClose: boolean };
    inner._closeResult = {
      session: {
        over_short_cents: 0,
        expected_by_method: { CASH: 30000, CHECK: 5000, ACCOUNT: 4000 },
        counted_by_method: { CASH: 30000, CHECK: 5000 },
      },
      sale_count: 3, sales_total_cents: 0, tax_total_cents: 0, change_cents: 0,
    };
    inner._showTillClose = true;
    await el.updateComplete;
    expect(el.textContent).toContain('Z-Report');
    const table = el.querySelector('table')?.textContent ?? '';
    expect(table.toLowerCase()).not.toContain('account');
    expect(table).not.toContain('CASH');
    expect(table).toContain('Cash');
    expect(table).toContain('Check');
    el.remove();
  });

  it('refuses inside _pay while a quantity change is on its way', async () => {
    svc.changeQuantity.mockReturnValue(new Promise(() => {}));
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    q(el, 'tender-card').click();
    await el.updateComplete;
    (el.querySelector('[aria-label="Increase quantity of Item a"]') as HTMLButtonElement).click();
    await el.updateComplete;
    await (el as unknown as { _pay(): Promise<void> })._pay();
    await settle();
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });

  it('keeps Pay off, in words, for a non cash amount typed above what is owed', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    q(el, 'tender-card').click();
    await el.updateComplete;
    await typeInto(el, '100.00');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    expect(q(el, 'pos-tenders-over')).toBeTruthy();
    q(el, 'pos-pay').click();
    await settle();
    expect(svc.completeSale).not.toHaveBeenCalled();
    // a cash amount above what is owed still gives change
    q(el, 'tender-cash').click();
    await el.updateComplete;
    await typeInto(el, '100.00');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    expect(q(el, 'pos-change').textContent).toContain('$13.50');
    el.remove();
  });
});

describe('PR 150 fix round 3: a normal split pays, and the two refused cases say why', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const settle = () => new Promise((r) => setTimeout(r, 0));
  const press = async (el: POSTerminal, method: string) => { q(el, `tender-${method}`).click(); await el.updateComplete; };
  const addSplit = async (el: POSTerminal, method: string, amount: string) => {
    await press(el, method);
    await typeInto(el, amount);
    q(el, 'pos-add-tender').click();
    await el.updateComplete;
  };

  it('pays a card added and cash typed above what is still owed, with the change due', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await addSplit(el, 'card', '40.00');
    await press(el, 'cash');
    await typeInto(el, '60.00');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    expect(q(el, 'pos-change').textContent).toContain('$13.50');
    expect(q(el, 'pos-tenders-over')).toBeNull();
    q(el, 'pos-pay').click();
    await settle();
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [
      { method: 'card', amount_cents: 4000 }, { method: 'cash', amount_cents: 6000 },
    ], []);
    el.remove();
  });

  it('refuses a typed card or check that takes the non cash tenders above the total, in words', async () => {
    const el = await mount(sale());
    await addSplit(el, 'card', '40.00');
    await press(el, 'check');
    await typeInto(el, '50.00');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    // The limit is the total less the other card, check and account tenders:
    // 86.50 less the card 40.00 already added.
    expect(q(el, 'pos-tenders-over').textContent).toContain('A card, check or account charge cannot be more than $46.50 here');
    expect(q(el, 'pos-tenders-over').textContent).not.toContain('added tenders');
    await typeInto(el, '46.50');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    el.remove();
  });

  it('names the real limit for a typed tender refused above it', async () => {
    // Cash 60.00 added on 86.50: a card of 90.00 is refused with the real
    // limit in words; a card of 40.00 pays.
    const el = await mount(sale());
    await addSplit(el, 'cash', '60.00');
    await press(el, 'card');
    await typeInto(el, '90.00');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    expect(q(el, 'pos-tenders-over').textContent).toContain('A card, check or account charge cannot be more than $86.50 here');
    // Pay's label names the same limit the panel names, never "what is owed".
    const pay = q(el, 'pos-pay');
    expect(pay.textContent).toContain('Type no more than $86.50');
    expect(pay.textContent).not.toContain('is owed');
    await typeInto(el, '40.00');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    expect(q(el, 'pos-tenders-over')).toBeNull();
    el.remove();
  });

  it('pays a card typed beside cash that already covers the change, as the server accepts it', async () => {
    const el = await mount(sale());
    await addSplit(el, 'cash', '60.00');
    await press(el, 'card');
    await typeInto(el, '40.00');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    expect(q(el, 'pos-change').textContent).toContain('$13.50');
    el.remove();
  });

  it('keeps the added tenders case and its words for a card added above a reduced total', async () => {
    const el = await mount(sale());
    await addSplit(el, 'card', '86.50');
    (el as unknown as { _sale: Sale })._sale = sale({ total_cents: 5000, subtotal_cents: 5000, tax_cents: 0 });
    await el.updateComplete;
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    expect(q(el, 'pos-tenders-over').textContent).toContain('The added tenders are more than the sale now');
    el.remove();
  });
});

describe('PR 150 round 4: one rule for splits', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('pays a card added then cash added above what is owed, with 3.50 change', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    for (const [m, amt] of [['card', '40.00'], ['cash', '50.00']] as const) {
      q(el, `tender-${m}`).click();
      await el.updateComplete;
      await typeInto(el, amt);
      q(el, 'pos-add-tender').click();
      await el.updateComplete;
    }
    expect(q(el, 'pos-tenders-over')).toBeNull();
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    expect(q(el, 'pos-change').textContent).toContain('$3.50');
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [
      { method: 'card', amount_cents: 4000 }, { method: 'cash', amount_cents: 5000 },
    ], []);
    el.remove();
  });
});

describe('the opening float field', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const openTillDialog = async (el: POSTerminal) => {
    (el as unknown as { _showTillOpen: boolean })._showTillOpen = true;
    await el.updateComplete;
  };
  const openTillButton = (el: POSTerminal) =>
    [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Open Till') as HTMLElement;

  it('reads empty: no figure may look entered, the placeholder is words in the till\'s muted tone', async () => {
    const el = await mount(sale());
    await openTillDialog(el);
    const field = q(el, 'till-float-amount') as HTMLInputElement;
    expect(field.value).toBe('');
    expect(field.placeholder.length).toBeGreaterThan(0);
    expect(field.placeholder, 'a placeholder that is a figure reads as entered').not.toMatch(/\d/);
    expect(field.className).toContain('placeholder:text-[rgb(var(--gb-till-muted))]');
    expect(field.className).toContain('placeholder:font-normal');
    el.remove();
  });

  it('Open Till with the field empty sends nothing, says what is missing, and focuses the field', async () => {
    const el = await mount(sale());
    await openTillDialog(el);
    openTillButton(el).click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.openTill).not.toHaveBeenCalled();
    const note = q(el, 'till-float-error');
    expect(note.getAttribute('role')).toBe('alert');
    expect(note.textContent?.toLowerCase()).toContain('opening float');
    expect(document.activeElement).toBe(q(el, 'till-float-amount'));
    el.remove();
  });

  it('a typed float opens the till with that figure, as before', async () => {
    // The pinned behaviour the money read checks: a typed "200.00" still
    // opens the till with 200.00. Passes before and after this PR by design.
    svc.openTill.mockResolvedValue({ id: 't2', register_id: 'REG-01', cashier_id: 'c', status: 'open', opening_float_cents: 20000 });
    const el = await mount(sale());
    await openTillDialog(el);
    const field = q(el, 'till-float-amount') as HTMLInputElement;
    field.value = '200.00';
    field.dispatchEvent(new Event('input'));
    await el.updateComplete;
    openTillButton(el).click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.openTill).toHaveBeenCalledWith('REG-01', 20000);
    el.remove();
  });
});

describe('the blind count fields', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('hint in words in the till\'s muted tone, never a figure that reads as entered', async () => {
    svc.tillReport.mockResolvedValue({
      sale_count: 0, sales_total_cents: 0, tax_total_cents: 0, change_cents: 0, tendered_by_method: {},
      expected_by_method: { CASH: 25000, CARD: 9000 },
    });
    const el = await mount(sale({ lines: [] }));
    const close = [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Close Till') as HTMLElement;
    close.click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    const fields = [...el.querySelectorAll('input[type="number"]')] as HTMLInputElement[];
    expect(fields.length).toBe(2);
    for (const f of fields) {
      expect(f.placeholder.length).toBeGreaterThan(0);
      expect(f.placeholder, 'a placeholder that is a figure reads as entered').not.toMatch(/\d/);
      expect(f.className).toContain('placeholder:text-[rgb(var(--gb-till-muted))]');
    }
    el.remove();
  });
});

describe('drawer cash and a count held for sign off (CP-08)', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const button = (el: Element, label: string) => [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === label) as HTMLElement | undefined;
  const settle = async (el: POSTerminal) => { await new Promise((r) => setTimeout(r, 0)); await el.updateComplete; };

  it('offers Drawer Cash only while a till is open, and opens the movement dialog', async () => {
    const el = await mount(sale({ lines: [] }));
    expect(q(el, 'pos-drawer-cash')).toBeTruthy();
    expect(el.querySelector('gable-till-movement-dialog')).toBeNull();
    q(el, 'pos-drawer-cash').click();
    await settle(el);
    const dialog = el.querySelector('gable-till-movement-dialog');
    expect(dialog).not.toBeNull();
    expect(dialog?.getAttribute('session-id')).toBe('t');
    expect(el.textContent).toContain('Drawer Cash');
    el.remove();
  });

  it('offers no Drawer Cash with no till', async () => {
    svc.listRegisters.mockResolvedValue([REG_01]);
    svc.currentTill.mockResolvedValue(null);
    svc.startSale.mockResolvedValue(sale({ lines: [] }));
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await settle(el);
    expect(el.querySelector('[data-testid="pos-drawer-cash"]')).toBeNull();
    el.remove();
  });

  it('records a drop, closes the dialog and says so', async () => {
    svc.recordMovement.mockResolvedValue({ id: 'm', kind: 'safe_drop', amount_cents: 2000 });
    const el = await mount(sale({ lines: [] }));
    q(el, 'pos-drawer-cash').click();
    await settle(el);
    const amount = q(el, 'movement-amount') as HTMLInputElement;
    amount.value = '20';
    amount.dispatchEvent(new Event('input', { bubbles: true }));
    await settle(el);
    q(el, 'movement-record').click();
    await settle(el);
    await settle(el);
    expect(svc.recordMovement).toHaveBeenCalledWith('t', { kind: 'safe_drop', amount_cents: 2000 });
    expect(el.querySelector('gable-till-movement-dialog')).toBeNull();
    expect(el.textContent).toContain('Safe drop of $20.00 recorded');
    el.remove();
  });

  it('keeps the dialog open and says why when the server refuses', async () => {
    svc.recordMovement.mockRejectedValue(new Error('the till session is not open: its cash has been counted'));
    const el = await mount(sale({ lines: [] }));
    q(el, 'pos-drawer-cash').click();
    await settle(el);
    const amount = q(el, 'movement-amount') as HTMLInputElement;
    amount.value = '20';
    amount.dispatchEvent(new Event('input', { bubbles: true }));
    await settle(el);
    q(el, 'movement-record').click();
    await settle(el);
    await settle(el);
    expect(q(el, 'movement-failure').textContent).toContain('has been counted');
    expect(el.querySelector('gable-till-movement-dialog')).not.toBeNull();
    el.remove();
  });

  it('says a count over the limit waits for a manager, not that the till is closed', async () => {
    svc.tillReport.mockResolvedValue({
      sale_count: 0, sales_total_cents: 0, tax_total_cents: 0, change_cents: 0, tendered_by_method: {},
      expected_by_method: { CASH: 25000 }, movements: [], movement_totals_by_kind: {},
    });
    svc.closeTill.mockResolvedValue({
      session: { status: 'pending_signoff', variance_threshold_cents: 500, over_short_cents: -2500, expected_by_method: { CASH: 25000 }, counted_by_method: { CASH: 22500 } },
      sale_count: 0, sales_total_cents: 0, tax_total_cents: 0, change_cents: 0,
    });
    const el = await mount(sale({ lines: [] }));
    button(el, 'Close Till')!.click();
    await settle(el);
    const field = el.querySelector('input[type="number"]') as HTMLInputElement;
    field.value = '225';
    field.dispatchEvent(new Event('input'));
    await el.updateComplete;
    [...el.querySelectorAll('button')].find((b) => b.textContent?.includes('Post Count'))!.click();
    await settle(el);
    expect(el.textContent).toContain('Waiting for Sign Off');
    expect(q(el, 'till-waiting-signoff').textContent).toContain('over the limit of $5.00');
    expect(el.textContent).not.toContain('Z-Report - Till Closed');
    el.remove();
  });

  it('still says Till Closed for a count inside the limit', async () => {
    svc.tillReport.mockResolvedValue({
      sale_count: 0, sales_total_cents: 0, tax_total_cents: 0, change_cents: 0, tendered_by_method: {}, expected_by_method: { CASH: 25000 },
    });
    svc.closeTill.mockResolvedValue({
      session: { status: 'closed', over_short_cents: 0, expected_by_method: { CASH: 25000 }, counted_by_method: { CASH: 25000 } },
      sale_count: 0, sales_total_cents: 0, tax_total_cents: 0, change_cents: 0,
    });
    const el = await mount(sale({ lines: [] }));
    button(el, 'Close Till')!.click();
    await settle(el);
    (el.querySelector('input[type="number"]') as HTMLInputElement).value = '250';
    (el.querySelector('input[type="number"]') as HTMLInputElement).dispatchEvent(new Event('input'));
    await el.updateComplete;
    [...el.querySelectorAll('button')].find((b) => b.textContent?.includes('Post Count'))!.click();
    await settle(el);
    expect(el.textContent).toContain('Z-Report - Till Closed');
    expect(el.querySelector('[data-testid="till-waiting-signoff"]')).toBeNull();
    el.remove();
  });

  it('rounds cash due from the selected register branch even when branchContext.current is another branch (CP-18)', async () => {
    (branchContext as unknown as { _branches: unknown[] })._branches = [
      { id: 'b-other', code: 'LK', name: 'Lake Country Outpost', active: true, is_home: false, timezone: 'America/Vancouver', cash_rounding_mode: 'OFF' },
      { id: 'b-main', code: 'MAIN', name: 'main yard', active: true, is_home: false, timezone: 'America/Vancouver', cash_rounding_mode: 'NEAREST_5_CENTS' },
    ];
    branchContext.setCurrent('b-other');
    const el = await mount(sale({ subtotal_cents: 103, tax_cents: 0, total_cents: 103 }));
    await chooseCash(el);
    const cashDue = q(el, 'pos-cash-due');
    expect(cashDue).not.toBeNull();
    expect(cashDue.textContent).toContain('nearest 5 cents');
    expect(cashDue.textContent).toContain('$1.05');
    el.remove();
    (branchContext as unknown as { _branches: unknown[] })._branches = [];
    branchContext.setCurrent(null);
  });
});

describe('return a past sale', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const summary = (over: Partial<SaleSummary> = {}): SaleSummary => ({
    id: 'past1', number: 'POS-000042', revision: 2, branch_id: 'b', register_id: 'REG-01', cashier_id: 'c',
    customer_id: 'cu1', customer_name: 'Test Builders', currency: 'USD', total_cents: 12500, status: 'completed',
    invoice_id: 'inv1', invoice_number: 'IN-000042', completed_at: new Date().toISOString(), created_at: new Date().toISOString(), business_day: '2026-10-28', item_count: 2,
    ...over,
  });

  const fullSale = (over: Partial<Sale> = {}): Sale => sale({
    id: 'past1', number: 'POS-000042', revision: 2, status: 'completed', total_cents: 12500,
    lines: [line('a', 7500), line('b', 5000)] as unknown as Sale['lines'], ...over,
  });

  it('opens the search panel, searches, and renders results', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    const el = await mount(sale());
    q(el, 'return-search-open').click();
    await el.updateComplete;
    expect(el.textContent).toContain('Return a past sale');
    const query = q(el, 'return-search-query') as HTMLInputElement;
    query.value = 'POS-000042';
    query.dispatchEvent(new Event('input'));
    await el.updateComplete;
    q(el, 'return-search-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.searchSales).toHaveBeenCalledWith('POS-000042', undefined);
    const result = q(el, 'return-search-result');
    expect(result).toBeTruthy();
    expect(q(result, 'return-search-result-number').textContent).toContain('POS-000042');
    expect(q(result, 'return-search-result-customer').textContent).toContain('Test Builders');
    expect(q(result, 'return-search-result-total').textContent).toContain('$125.00');
    expect(q(result, 'return-search-result-status').textContent).toContain('Completed');
    expect(q(result, 'return-search-result-date').textContent).toContain('2026-10-28');
    el.remove();
  });

  it('shows an open result as not returnable, with no pick button', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary({ status: 'open', invoice_id: null, invoice_number: null })], next_cursor: null, limit: 50 });
    const el = await mount(sale());
    q(el, 'return-search-open').click();
    await el.updateComplete;
    const query = q(el, 'return-search-query') as HTMLInputElement;
    query.value = 'POS';
    query.dispatchEvent(new Event('input'));
    q(el, 'return-search-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'return-search-result-status').textContent).toContain('Open');
    expect(q(el, 'return-search-result-not-completed').textContent).toContain('Not completed yet');
    expect(q(el, 'return-search-result-pick')).toBeNull();
    el.remove();
  });

  it('shows a voided result as not returnable', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary({ status: 'voided' })], next_cursor: null, limit: 50 });
    const el = await mount(sale());
    q(el, 'return-search-open').click();
    await el.updateComplete;
    const query = q(el, 'return-search-query') as HTMLInputElement;
    query.value = 'void';
    query.dispatchEvent(new Event('input'));
    q(el, 'return-search-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'return-search-result-voided').textContent).toContain('Voided - cannot be returned');
    expect(q(el, 'return-search-result-pick')).toBeNull();
    el.remove();
  });

  it('picks a completed result and opens the return dialog on that sale', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    svc.getSale.mockResolvedValue(fullSale());
    svc.returnReasons.mockResolvedValue([]);
    const el = await mount(sale());
    q(el, 'return-search-open').click();
    await el.updateComplete;
    const query = q(el, 'return-search-query') as HTMLInputElement;
    query.value = 'POS-000042';
    query.dispatchEvent(new Event('input'));
    q(el, 'return-search-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    q(el, 'return-search-result-pick').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.getSale).toHaveBeenCalledWith('past1');
    expect(q(el, 'return-qty')).toBeTruthy();
    expect(el.textContent).toContain('POS-000042');
    el.remove();
  });

  it('shows the already-returned reason when every product line is fully returned', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    svc.getSale.mockResolvedValue(fullSale({ returned_quantities: { a: '1', b: '1' } }));
    const el = await mount(sale());
    q(el, 'return-search-open').click();
    await el.updateComplete;
    (q(el, 'return-search-query') as HTMLInputElement).value = 'POS-000042';
    q(el, 'return-search-query').dispatchEvent(new Event('input'));
    q(el, 'return-search-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    q(el, 'return-search-result-pick').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'return-search-error').textContent).toContain('Everything on this sale has already been returned');
    expect(q(el, 'return-qty')).toBeNull();
    el.remove();
  });

  it('searches by date when no query text is typed', async () => {
    const day = new Date().toISOString().slice(0, 10);
    svc.searchSales.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    const el = await mount(sale());
    q(el, 'return-search-open').click();
    await el.updateComplete;
    const dateField = q(el, 'return-search-date') as HTMLInputElement;
    dateField.value = day;
    dateField.dispatchEvent(new Event('input'));
    await el.updateComplete;
    q(el, 'return-search-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.searchSales).toHaveBeenCalledWith('', day);
    expect(q(el, 'return-search-result')).toBeTruthy();
    el.remove();
  });

  it('defaults restock to back to stock and sends restock choices with the return', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    svc.getSale.mockResolvedValue(fullSale());
    svc.returnReasons.mockResolvedValue([]);
    svc.previewReturn.mockResolvedValue({
      subtotal_cents: -7500, tax_cents: 0, restocking_fee_cents: 0, total_cents: -7500, refund_cents: 7500,
      lines: [{ line_id: 'a', reason_code: 'other', line_total_cents: -7500, restocking_fee_cents: 0 }],
    });
    svc.createReturn.mockResolvedValue({ id: 'r1', number: 'RTN-000001', total_cents: -7500, restocking_fee_cents: 0, refund_method: 'cash' });
    const el = await mount(sale());
    q(el, 'return-search-open').click();
    await el.updateComplete;
    (q(el, 'return-search-query') as HTMLInputElement).value = 'POS-000042';
    q(el, 'return-search-query').dispatchEvent(new Event('input'));
    q(el, 'return-search-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    q(el, 'return-search-result-pick').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    // Default is back to stock for both lines.
    expect((el as unknown as { _returnRestockByLine: Record<string, boolean> })._returnRestockByLine).toEqual({ a: true, b: true });
    // Mark first line damaged, leave second back to stock.
    const damaged = [...el.querySelectorAll('[data-testid="return-restock-damaged"]')][0] as HTMLInputElement;
    damaged.click();
    await el.updateComplete;
    const qty = [...el.querySelectorAll('[data-testid="return-qty"]')][0] as HTMLInputElement;
    qty.value = '1';
    qty.dispatchEvent(new Event('input'));
    await el.updateComplete;
    const note = q(el, 'return-note') as HTMLInputElement;
    note.value = 'customer reported damage';
    note.dispatchEvent(new Event('input'));
    await el.updateComplete;
    // the return waits for the server's figures, so the act waits for them too
    await new Promise((r) => setTimeout(r, 350));
    await el.updateComplete;
    q(el, 'return-take').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.createReturn).toHaveBeenCalledWith(expect.objectContaining({
      lines: [{ line_id: 'a', quantity: '1', restock: false }],
    }));
    expect(el.textContent).toContain('Return RTN-000001 complete - $75.00 refunded cash');
    el.remove();
  });

  it('states the restocking fee and the refund before the return is taken', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    svc.getSale.mockResolvedValue(fullSale());
    svc.returnReasons.mockResolvedValue([
      { code: 'defective', label: 'Defective', restocking_fee_bps: 0, needs_note: false },
      { code: 'changed_mind', label: 'Changed their mind', restocking_fee_bps: 1500, needs_note: false },
    ]);
    svc.previewReturn.mockResolvedValue({
      subtotal_cents: -6375, tax_cents: 0, restocking_fee_cents: 1125, total_cents: -6375, refund_cents: 6375,
      lines: [{ line_id: 'a', reason_code: 'changed_mind', line_total_cents: -7500, restocking_fee_cents: 1125 }],
    });
    svc.createReturn.mockResolvedValue({ id: 'r2', number: 'RTN-000002', total_cents: -6375, restocking_fee_cents: 1125, refund_method: 'cash' });
    const el = await mount(sale());
    q(el, 'return-search-open').click();
    await el.updateComplete;
    (q(el, 'return-search-query') as HTMLInputElement).value = 'POS-000042';
    q(el, 'return-search-query').dispatchEvent(new Event('input'));
    q(el, 'return-search-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    q(el, 'return-search-result-pick').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    const qty = [...el.querySelectorAll('[data-testid="return-qty"]')][0] as HTMLInputElement;
    qty.value = '1';
    qty.dispatchEvent(new Event('input'));
    await el.updateComplete;
    // the figures come from the server, once the dialog's debounce settles
    await new Promise((r) => setTimeout(r, 350));
    await el.updateComplete;
    expect(svc.previewReturn).toHaveBeenCalledWith(expect.objectContaining({
      original_sale_id: 'past1',
      lines: [{ line_id: 'a', quantity: '1', restock: true }],
    }));
    expect(q(el, 'return-figures')).toBeTruthy();
    expect(q(el, 'return-figures-fee').textContent).toBe('$11.25');
    expect(q(el, 'return-figures-refund').textContent).toBe('$63.75');
    expect(q(el, 'return-figures-line-fee').textContent).toContain('Item a');
    expect(q(el, 'return-figures-line-fee').textContent).toContain('Changed their mind');
    el.remove();
  });

  /** Opens the return dialog on the past sale, the way the counter does. */
  async function openOnPastSale(el: POSTerminal) {
    q(el, 'return-search-open').click();
    await el.updateComplete;
    (q(el, 'return-search-query') as HTMLInputElement).value = 'POS-000042';
    q(el, 'return-search-query').dispatchEvent(new Event('input'));
    q(el, 'return-search-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    q(el, 'return-search-result-pick').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
  }

  it('asks the server for the figures even when no reason carries a fee', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    svc.getSale.mockResolvedValue(fullSale());
    svc.returnReasons.mockResolvedValue([
      { code: 'defective', label: 'Defective', restocking_fee_bps: 0, needs_note: false },
    ]);
    svc.previewReturn.mockResolvedValue({
      subtotal_cents: -7500, tax_cents: 0, restocking_fee_cents: 0, total_cents: -7500, refund_cents: 7500,
      lines: [{ line_id: 'a', reason_code: 'other', line_total_cents: -7500, restocking_fee_cents: 0 }],
    });
    const el = await mount(sale());
    await openOnPastSale(el);
    const qty = [...el.querySelectorAll('[data-testid="return-qty"]')][0] as HTMLInputElement;
    qty.value = '1';
    qty.dispatchEvent(new Event('input'));
    await el.updateComplete;
    await new Promise((r) => setTimeout(r, 350));
    await el.updateComplete;
    expect(svc.previewReturn).toHaveBeenCalled();
    expect(el.querySelector('[data-testid="return-figures"]')).toBeNull();
    el.remove();
  });

  it('states a fee that was set after the counter read the reason list', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    svc.getSale.mockResolvedValue(fullSale());
    // the list the counter reads carries no fee in force
    svc.returnReasons.mockResolvedValue([
      { code: 'changed_mind', label: 'Changed their mind', restocking_fee_bps: 0, needs_note: false },
    ]);
    const el = await mount(sale());
    await openOnPastSale(el);
    // the dealer sets a fee after the list was read: the server's preview is
    // the only word on the fee in force, never the kept list
    svc.previewReturn.mockResolvedValue({
      subtotal_cents: -6375, tax_cents: 0, restocking_fee_cents: 1125, total_cents: -6375, refund_cents: 6375,
      lines: [{ line_id: 'a', reason_code: 'changed_mind', line_total_cents: -7500, restocking_fee_cents: 1125 }],
    });
    const qty = [...el.querySelectorAll('[data-testid="return-qty"]')][0] as HTMLInputElement;
    qty.value = '1';
    qty.dispatchEvent(new Event('input'));
    await el.updateComplete;
    await new Promise((r) => setTimeout(r, 350));
    await el.updateComplete;
    expect(svc.previewReturn).toHaveBeenCalled();
    expect(q(el, 'return-figures')).toBeTruthy();
    expect(q(el, 'return-figures-fee').textContent).toBe('$11.25');
    expect(q(el, 'return-figures-refund').textContent).toBe('$63.75');
    expect(el.querySelector('[data-testid="return-fee-note"]')).toBeTruthy();
    el.remove();
  });

  it('reads the reason list again when the dialog opens, so a fee set since is in the label', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    svc.getSale.mockResolvedValue(fullSale());
    svc.returnReasons.mockResolvedValue([
      { code: 'changed_mind', label: 'Changed their mind', restocking_fee_bps: 0, needs_note: false },
    ]);
    svc.previewReturn.mockResolvedValue({
      subtotal_cents: -7500, tax_cents: 0, restocking_fee_cents: 0, total_cents: -7500, refund_cents: 7500,
      lines: [{ line_id: 'a', reason_code: 'other', line_total_cents: -7500, restocking_fee_cents: 0 }],
    });
    const el = await mount(sale());
    await openOnPastSale(el);
    expect(svc.returnReasons).toHaveBeenCalledTimes(1);
    // the dealer sets a fee after the counter read the list
    svc.returnReasons.mockResolvedValue([
      { code: 'changed_mind', label: 'Changed their mind', restocking_fee_bps: 1500, needs_note: false },
    ]);
    // the dialog is closed and opened again: the list is read again
    const row = q(el, 'return-take').closest('div') as HTMLElement;
    const cancel = [...row.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Cancel');
    cancel?.click();
    await el.updateComplete;
    await openOnPastSale(el);
    expect(svc.returnReasons).toHaveBeenCalledTimes(2);
    const qty = [...el.querySelectorAll('[data-testid="return-qty"]')][0] as HTMLInputElement;
    qty.value = '1';
    qty.dispatchEvent(new Event('input'));
    await el.updateComplete;
    const select = q(el, 'return-reason') as HTMLElement & { options: { value: string; label: string }[] };
    expect(select.options.map((o) => o.label)).toContain('Changed their mind (15% restocking fee)');
    el.remove();
  });

  it('keeps Take the Return off while the figures are still being asked for', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    svc.getSale.mockResolvedValue(fullSale());
    svc.returnReasons.mockResolvedValue([
      { code: 'changed_mind', label: 'Changed their mind', restocking_fee_bps: 1500, needs_note: false },
    ]);
    svc.previewReturn.mockResolvedValue({
      subtotal_cents: -6375, tax_cents: 0, restocking_fee_cents: 1125, total_cents: -6375, refund_cents: 6375,
      lines: [{ line_id: 'a', reason_code: 'changed_mind', line_total_cents: -7500, restocking_fee_cents: 1125 }],
    });
    const el = await mount(sale());
    await openOnPastSale(el);
    const qty = [...el.querySelectorAll('[data-testid="return-qty"]')][0] as HTMLInputElement;
    qty.value = '1';
    qty.dispatchEvent(new Event('input'));
    await el.updateComplete;
    // the debounce has not fired yet: the figures are not on the screen
    expect(svc.previewReturn).not.toHaveBeenCalled();
    expect(el.querySelector('[data-testid="return-figures"]')).toBeNull();
    expect((q(el, 'return-take') as HTMLButtonElement).disabled).toBe(true);
    await new Promise((r) => setTimeout(r, 350));
    await el.updateComplete;
    expect(svc.previewReturn).toHaveBeenCalled();
    expect(q(el, 'return-figures')).toBeTruthy();
    expect((q(el, 'return-take') as HTMLButtonElement).disabled).toBe(false);
    el.remove();
  });

  it('keeps Take the Return off while the figures are still in flight', async () => {
    svc.searchSales.mockResolvedValue({ items: [summary()], next_cursor: null, limit: 50 });
    svc.getSale.mockResolvedValue(fullSale());
    svc.returnReasons.mockResolvedValue([
      { code: 'changed_mind', label: 'Changed their mind', restocking_fee_bps: 1500, needs_note: false },
    ]);
    let answer: (f: unknown) => void = () => {};
    const held = new Promise((resolve) => { answer = resolve; });
    svc.previewReturn.mockReturnValue(held as Promise<never>);
    const el = await mount(sale());
    await openOnPastSale(el);
    const qty = [...el.querySelectorAll('[data-testid="return-qty"]')][0] as HTMLInputElement;
    qty.value = '1';
    qty.dispatchEvent(new Event('input'));
    await el.updateComplete;
    await new Promise((r) => setTimeout(r, 350));
    await el.updateComplete;
    expect(svc.previewReturn).toHaveBeenCalled();
    expect(el.querySelector('[data-testid="return-figures"]')).toBeNull();
    expect((q(el, 'return-take') as HTMLButtonElement).disabled).toBe(true);
    answer({
      subtotal_cents: -6375, tax_cents: 0, restocking_fee_cents: 1125, total_cents: -6375, refund_cents: 6375,
      lines: [{ line_id: 'a', reason_code: 'changed_mind', line_total_cents: -7500, restocking_fee_cents: 1125 }],
    });
    await el.updateComplete;
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'return-figures')).toBeTruthy();
    expect((q(el, 'return-take') as HTMLButtonElement).disabled).toBe(false);
    el.remove();
  });
});

describe('the dealer check ID rule at the counter', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const rule = (mode: 'never' | 'every_check' | 'over_amount', over_cents: number | null = null) =>
    ({ revision: 2, rules: { check_limit_cents: null, check_id_required: { mode, over_cents } } });
  const chooseCheck = async (el: POSTerminal) => {
    q(el, 'tender-check').click();
    await el.updateComplete;
  };
  const checkTheID = async (el: POSTerminal) => {
    (el.querySelector('#pos-id-checked') as HTMLInputElement).checked = true;
    (el.querySelector('#pos-id-checked') as HTMLInputElement).dispatchEvent(new Event('change'));
    await el.updateComplete;
    await chooseOption(el.querySelector('#pos-id-type') as HTMLElement & { value: string }, 'drivers_license');
  };

  it('asks for the ID before Pay on a check the rule covers, and pays once it is checked', async () => {
    svc.settings.mockResolvedValue(rule('over_amount', 5000));
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    await chooseCheck(el);
    await typeInto(el, '86.50');
    expect(q(el, 'pos-check-id-needed').textContent)
      .toContain(`Checks over $50.00 in total need the customer's ID`);
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    expect(q(el, 'pos-pay').textContent).toContain("Check the customer's ID");
    expect(svc.completeSale).not.toHaveBeenCalled();
    await checkTheID(el);
    expect(el.querySelector('[data-testid="pos-check-id-needed"]')).toBeNull();
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3,
      [{ method: 'check', amount_cents: 8650, id_checked: true, id_type: 'drivers_license' }], []);
    el.remove();
  });

  it('carries the one ID check onto an added check once the split passes the figure', async () => {
    svc.settings.mockResolvedValue(rule('over_amount', 5000));
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    // the first check, under the figure alone, is added with no ID asked
    await chooseCheck(el);
    await typeInto(el, '43.25');
    expect(el.querySelector('[data-testid="pos-check-id-needed"]')).toBeNull();
    q(el, 'pos-add-tender').click();
    await el.updateComplete;
    // the second check passes the figure together: the one ID stamps both
    await chooseCheck(el);
    await typeInto(el, '43.25');
    expect(q(el, 'pos-check-id-needed').textContent)
      .toContain(`Checks over $50.00 in total need the customer's ID`);
    expect(q(el, 'pos-added-check-id').textContent).toContain('ID not checked yet');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    await checkTheID(el);
    expect(el.querySelector('[data-testid="pos-check-id-needed"]')).toBeNull();
    expect(q(el, 'pos-added-check-id').textContent).toContain('ID checked');
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [
      { method: 'check', amount_cents: 4325, id_checked: true, id_type: 'drivers_license' },
      { method: 'check', amount_cents: 4325, id_checked: true, id_type: 'drivers_license' },
    ], []);
    el.remove();
  });

  it('holds the ID when two checks together pass the figure, each under it', async () => {
    svc.settings.mockResolvedValue(rule('over_amount', 5000));
    const el = await mount(sale());
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    // a first check under the figure, with the ID, joins the split
    (el as unknown as { _pendingTenders: { method: string; amount_cents: number; id_checked: boolean; id_type: string }[] })
      ._pendingTenders = [{ method: 'check', amount_cents: 4325, id_checked: true, id_type: 'state_id' }];
    await el.updateComplete;
    await chooseCheck(el);
    await typeInto(el, '43.25');
    expect(q(el, 'pos-check-id-needed').textContent)
      .toContain(`Checks over $50.00 in total need the customer's ID`);
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(true);
    await checkTheID(el);
    expect(el.querySelector('[data-testid="pos-check-id-needed"]')).toBeNull();
    expect((q(el, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    el.remove();
  });

  it('warns a walk-in at entry when the check is over the dealer wide limit the same read carries', async () => {
    svc.settings.mockResolvedValue(
      { revision: 2, rules: { check_limit_cents: 10000, check_id_required: { mode: 'never', over_cents: null } } });
    const el = await mount(sale());
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    await chooseCheck(el);
    await typeInto(el, '150.00');
    expect(q(el, 'pos-check-limit').textContent).toContain('Check limit $100.00');
    q(el, 'pos-add-tender').click();
    await el.updateComplete;
    expect(q(el, 'pos-check-over').textContent).toContain('over the check limit');
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });

  it('asks on every check when the rule is every check, and never when no rule is set', async () => {
    svc.settings.mockResolvedValue(rule('every_check'));
    const el = await mount(sale());
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    await chooseCheck(el);
    await typeInto(el, '86.50');
    expect(q(el, 'pos-check-id-needed').textContent)
      .toContain(`This check needs the customer's ID`);
    el.remove();

    svc.settings.mockResolvedValue(rule('never'));
    const el2 = await mount(sale());
    await new Promise((r) => setTimeout(r, 0));
    await el2.updateComplete;
    q(el2, 'tender-check').click();
    await el2.updateComplete;
    await typeInto(el2, '86.50');
    expect(el2.querySelector('[data-testid="pos-check-id-needed"]')).toBeNull();
    expect((q(el2, 'pos-pay') as HTMLButtonElement).disabled).toBe(false);
    el2.remove();
  });
});

describe('the summary when the attached customer is tax exempt', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('shows Tax exempt with the certificate number and its expiry in place of the tax figure', async () => {
    const el = await mount(sale({ tax_cents: 0, total_cents: 8000,
      tax_exemption: { certificate_number: 'CERT-88', expiry_day: '2027-01-31' } }));
    const line = q(el, 'summary-tax-exempt');
    expect(line).toBeTruthy();
    expect(line.textContent).toContain('Tax exempt');
    expect(line.textContent).toContain('CERT-88');
    expect(line.textContent).toContain('expires 2027-01-31');
    expect(q(el, 'summary-tax')).toBeNull();
    expect(q(el, 'summary-total')?.textContent).toContain('80.00');
    el.remove();
  });

  it('shows the tax as normal when the certificate has expired (the sale carries no exemption)', async () => {
    const el = await mount(sale({ tax_cents: 650, total_cents: 8650, tax_exemption: null }));
    expect(q(el, 'summary-tax-exempt')).toBeNull();
    expect(q(el, 'summary-tax')?.textContent).toContain('6.50');
    el.remove();
  });
});
