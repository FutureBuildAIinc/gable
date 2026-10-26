// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The counter and the customer's alerts: the notes show in the customer
 * panel, a warning shows as a banner, and a warning that asks to be
 * acknowledged stops the sale until the clerk says so. The acknowledgement
 * goes on the sale with it.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Sale, CounterCustomer, CounterStanding, CounterAlert } from '../../types/pos';

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
  standing: vi.fn(),
}));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './POSTerminal';
import type { POSTerminal } from './POSTerminal';
import { answerAppDialog, flush } from '../../test/dom';
import type { CounterRegister } from '../../types/pos';

const REG_01: CounterRegister = {
  id: 'REG-01', name: 'Front till', branch_id: 'b-main', branch_code: 'MAIN', branch_name: 'main yard',
};

const line = (id: string, cents: number) => ({
  id, position: 1, line_type: 'product', parent_line_id: null, product_id: `p-${id}`,
  charge_code_id: null, charge_code: null, sku: `SKU-${id}`, description: `Item ${id}`,
  quantity: '1', uom: 'EA', unit_price_ten_thousandths: cents * 100, line_total_cents: cents,
});

function sale(over: Partial<Sale> = {}): Sale {
  return {
    id: 's1', number: 'S-1', revision: 3, branch_id: 'b', register_id: 'REG-01', cashier_id: 'c',
    customer_id: null, currency: 'USD', subtotal_cents: 1100, tax_cents: 98, total_cents: 1198,
    change_cents: 0, till_session_id: 't', status: 'open', invoice_id: null, completed_at: null,
    created_at: '', customer_po: null, job_id: null, buyer_contact_id: null, print_count: 0,
    exchange_return_id: null, tenders: [], lines: [line('a', 1100)], ...over,
  } as unknown as Sale;
}

const account: CounterCustomer = {
  id: 'cu1', name: 'Test Builders', account_number: 'A-100', standing: 'good',
  credit_limit_cents: 500000, available_credit_cents: 400000, open_receivable_cents: 100000,
  po_required: false, jobs: [],
} as unknown as CounterCustomer;

const standing = (over: Partial<CounterStanding> = {}): CounterStanding => ({
  customer_id: 'cu1', name: 'Test Builders', currency: 'USD', credit_limit_cents: 3000,
  open_receivable_cents: 2395, open_credit_memo_cents: 0, available_credit_cents: 605, on_hold: false,
  terms_kind: 'net_days', named_buyer_required: false, buyers: [], check_limit_cents: null,
  payment_options: ['cash', 'check', 'card', 'account'],
  on_account: { verdict: 'allowed', reason: '', message: '', over_cents: null, ways_forward: [] },
  alerts: [],
  ...over,
});

const NOTE: CounterAlert = { id: 'a1', kind: 'note', text: 'Call before the delivery', acknowledge_required: false };
const WARNING: CounterAlert = { id: 'a2', kind: 'warning', text: 'Needs a PO on every order', acknowledge_required: false };
const ACK_WARNING: CounterAlert = { id: 'a3', kind: 'warning', text: 'Check the site before the delivery', acknowledge_required: true };

const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;

type Internals = {
  _customer: CounterCustomer | null; _sale: Sale | null; _standing: CounterStanding | null;
  _syncStanding: () => Promise<void>;
};
const inner = (el: POSTerminal) => el as unknown as Internals;

async function mount(s: Sale): Promise<POSTerminal> {
  svc.listRegisters.mockResolvedValue([REG_01]);
  svc.currentTill.mockResolvedValue({ id: 't', register_id: 'REG-01', cashier_id: 'c', status: 'open', opening_float_cents: 20000 });
  svc.startSale.mockResolvedValue(s);
  const el = document.createElement('gable-pos-terminal') as POSTerminal;
  document.body.appendChild(el);
  await flush();
  await el.updateComplete;
  return el;
}

/** An account sale on screen with the customer's standing and its alerts. */
async function withAlerts(alerts: CounterAlert[]): Promise<POSTerminal> {
  const s = sale({ customer_id: 'cu1' });
  svc.standing.mockResolvedValue(standing({ alerts }));
  const el = await mount(s);
  inner(el)._customer = account;
  inner(el)._sale = s;
  await el.updateComplete;
  await inner(el)._syncStanding();
  await el.updateComplete;
  return el;
}

/** Cash for the whole sale, the tender chosen and typed. */
async function chooseCash(el: POSTerminal) {
  q(el, 'tender-cash').click();
  await el.updateComplete;
  const input = el.querySelector('#pos-tender-amount') as HTMLInputElement;
  input.value = '11.98';
  input.dispatchEvent(new Event('input'));
  await el.updateComplete;
}

describe('the counter and the customer alerts', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('shows the customer note in the customer panel', async () => {
    const el = await withAlerts([NOTE]);
    expect(q(el, 'pos-alert-note').textContent).toContain('Call before the delivery');
    expect(q(el, 'pos-alert-warning')).toBeNull();
    el.remove();
  });

  it('shows a warning that asks for nothing as a banner', async () => {
    const el = await withAlerts([WARNING]);
    expect(q(el, 'pos-alert-warning').textContent).toContain('Needs a PO on every order');
    expect(q(el, 'pos-alert-ack')).toBeNull();
    el.remove();
  });

  it('says on a warning that asks to be acknowledged that the counter must read it', async () => {
    const el = await withAlerts([ACK_WARNING]);
    expect(q(el, 'pos-alert-ack').textContent).toContain('must read this');
    el.remove();
  });

  it('asks the clerk to read the warning before Pay, and takes no sale until they say so', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await withAlerts([ACK_WARNING]);
    await chooseCash(el);
    q(el, 'pos-pay').click();
    await flush();

    // The dialog is up, naming the warning.
    const dialog = document.querySelector('gable-app-dialog') as HTMLElement | null;
    expect(dialog).toBeTruthy();
    expect(dialog!.textContent).toContain('Check the site before the delivery');
    expect(dialog!.textContent).toContain('I have read this');
    // Nothing is taken while the question is open.
    expect(svc.completeSale).not.toHaveBeenCalled();

    // The clerk says so: the sale is taken and the acknowledgement goes with it.
    await answerAppDialog(true);
    await flush();
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [{ method: 'cash', amount_cents: 1198 }], ['a3']);
    el.remove();
  });

  it('takes no sale when the clerk does not say so', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await withAlerts([ACK_WARNING]);
    await chooseCash(el);
    q(el, 'pos-pay').click();
    await flush();
    await answerAppDialog(false);
    await flush();
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });

  it('sends no acknowledgement when the customer has no warning to read', async () => {
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await withAlerts([NOTE]);
    await chooseCash(el);
    q(el, 'pos-pay').click();
    await flush();
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [{ method: 'cash', amount_cents: 1198 }], []);
    el.remove();
  });
});
