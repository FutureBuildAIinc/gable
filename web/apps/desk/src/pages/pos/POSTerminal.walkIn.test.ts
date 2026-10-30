// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The branch's walk-in identity floor on the counter (CP-17a, item 296): over
// the floor the counter asks who paid before Pay, the identity the till
// session holds rides the next sale so the clerk is asked once, a walk-in who
// declines lets the sale go ahead, and with the floor off the counter says the
// walk-in's cash is not aggregated.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Sale } from '../../types/pos';

const svc = vi.hoisted(() => ({
  listRegisters: vi.fn(),
  currentTill: vi.fn(),
  startSale: vi.fn(),
  completeSale: vi.fn(),
  largeCashPreview: vi.fn(),
  searchProducts: vi.fn(),
  findCustomers: vi.fn(),
  attachCustomer: vi.fn(),
  addItem: vi.fn(),
  removeItem: vi.fn(),
  changeQuantity: vi.fn(),
  getSale: vi.fn(),
  searchSales: vi.fn(),
  createReturn: vi.fn(),
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
import './PayerDialog';
import type { POSTerminal } from './POSTerminal';
import type { CounterRegister } from '../../types/pos';

const reg = (id: string, branch: string, name = 'Front till'): CounterRegister => ({
  id, name, branch_id: `b-${branch}`, branch_code: branch.toUpperCase(), branch_name: `${branch} yard`, card_terminal: 'run',
});
const REG_01 = reg('REG-01', 'main');
const OPEN_TILL = { id: 't', register_id: 'REG-01', cashier_id: 'c', status: 'open', opening_float_cents: 20000 };

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

/** The preview's answer when the branch asks a walk-in who paid (the floor). */
const FLOOR_ASK = {
  country: 'US', rule_family: 'US_8300', counted_cents: 598813, aggregate_cents: 598813,
  threshold_cents: 1000000, comparator: 'greater', asks_details: true, asks_floor: true,
  walk_in_aggregated: true, walk_in_floor_cents: 300000, threshold_crossed: false,
  fields: ['name', 'address', 'taxpayer_id', 'date_of_birth', 'occupation', 'id_type', 'id_number', 'third_party_payer'],
  dealer_threshold: false,
};

/** The preview's answer when the branch has the floor off. */
const FLOOR_OFF = {
  country: 'US', rule_family: 'US_8300', counted_cents: 598813, aggregate_cents: 598813,
  threshold_cents: 1000000, comparator: 'greater', asks_details: false, asks_floor: false,
  walk_in_aggregated: false, walk_in_floor_cents: 0, threshold_crossed: false,
  walk_in_note: "Walk-in cash is not aggregated at this branch: each walk-in's cash is counted on its own.",
  fields: [], dealer_threshold: false,
};

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
const settle = async () => {
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
};
/** The counter's look is debounced, so the words land a moment after the tender. */
const settleLook = async () => {
  await new Promise((r) => setTimeout(r, 400));
  await new Promise((r) => setTimeout(r, 0));
};

describe("the branch's walk-in identity floor on the counter", () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('asks who paid before Pay when the walk-in paid cash over the floor', async () => {
    svc.largeCashPreview.mockResolvedValue(FLOOR_ASK);
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await chooseCash(el);
    await typeInto(el, '100.00');
    q(el, 'pos-pay').click();
    await settle();
    await el.updateComplete;

    // the counter asked, so nothing was completed
    expect(svc.completeSale).not.toHaveBeenCalled();
    const dialog = el.querySelector('gable-payer-dialog') as HTMLElement;
    expect(dialog).toBeTruthy();
    expect(dialog.textContent).toContain('Who is paying for this walk-in?');
    // the floor's ask can be declined: the sale goes ahead and a manager reviews it
    expect(q(el, 'payer-decline')).toBeTruthy();
    el.remove();
  });

  it('keeps the identity on the till session, so the next sale is not asked again', async () => {
    svc.largeCashPreview.mockResolvedValue(FLOOR_ASK);
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await chooseCash(el);
    await typeInto(el, '100.00');
    q(el, 'pos-pay').click();
    await settle();
    await el.updateComplete;

    // the clerk captures the walk-in's identity
    const dialog = el.querySelector('gable-payer-dialog') as HTMLElement & { requestUpdate(): void };
    const payer = {
      name: 'Riverbend Builders', address: '12 Mill Road', occupation: 'Contractor',
      date_of_birth: '1980-01-01', id_type: 'drivers_license', id_number: 'D1234567',
      taxpayer_id: '00-1234567', third_party_payer: false,
    };
    dialog.dispatchEvent(new CustomEvent('payer-save', { detail: payer, bubbles: true, composed: true }));
    await settle();
    await el.updateComplete;

    // the completion carries the identity
    expect(svc.completeSale).toHaveBeenCalledTimes(1);
    const extras = svc.completeSale.mock.calls[0][4];
    expect(extras?.payer?.taxpayer_id).toBe('00-1234567');

    // the next sale in the same till session starts with the identity held, so
    // the counter is not asked again and the preview keys the same person
    svc.largeCashPreview.mockClear();
    svc.startSale.mockResolvedValue(sale({ id: 's2' }));
    (el as unknown as { _startNewSale(): Promise<void> })._startNewSale();
    await settle();
    await el.updateComplete;
    await chooseCash(el);
    await typeInto(el, '100.00');
    q(el, 'pos-pay').click();
    await settle();
    await el.updateComplete;

    // every preview the counter made in this sale was about this sale and
    // carried the identity the till session holds, so the preview keys the
    // same person the completion does
    const previewCalls = svc.largeCashPreview.mock.calls;
    expect(previewCalls.length).toBeGreaterThan(0);
    for (const call of previewCalls) {
      expect(call[0]).toBe('s2');
      expect(call[3]?.taxpayer_id).toBe('00-1234567');
    }
    expect(el.querySelector('gable-payer-dialog')).toBeFalsy();
    el.remove();
  });

  it('lets the sale go ahead when the walk-in declines to identify', async () => {
    svc.largeCashPreview.mockResolvedValue(FLOOR_ASK);
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await chooseCash(el);
    await typeInto(el, '100.00');
    q(el, 'pos-pay').click();
    await settle();
    await el.updateComplete;

    q(el, 'payer-decline').click();
    await settle();
    await el.updateComplete;

    expect(svc.completeSale).toHaveBeenCalledTimes(1);
    const extras = svc.completeSale.mock.calls[0][4];
    expect(extras?.payer_declined).toBe(true);
    expect(extras?.payer).toBeUndefined();
    el.remove();
  });

  it('does not ask the server about the sale once the counter is off the page', async () => {
    svc.largeCashPreview.mockResolvedValue(FLOOR_OFF);
    const el = await mount(sale());
    await chooseCash(el);
    await typeInto(el, '100.00');
    // the counter is taken off the page with its look still pending
    el.remove();
    await new Promise((r) => setTimeout(r, 400));

    expect(svc.largeCashPreview).not.toHaveBeenCalled();
    expect(svc.completeSale).not.toHaveBeenCalled();
  });

  it('says on the counter that the walk-in cash is not aggregated when the floor is off', async () => {
    svc.largeCashPreview.mockResolvedValue(FLOOR_OFF);
    svc.completeSale.mockResolvedValue(sale({ status: 'completed' }));
    const el = await mount(sale());
    await chooseCash(el);
    await typeInto(el, '100.00');
    await settleLook();
    await el.updateComplete;

    const note = q(el, 'pos-walk-in-note');
    expect(note).toBeTruthy();
    expect(note.textContent).toContain('not aggregated');
    // the counter asked nothing: the sale went straight through
    q(el, 'pos-pay').click();
    await settle();
    await el.updateComplete;
    expect(el.querySelector('gable-payer-dialog')).toBeFalsy();
    expect(svc.completeSale).toHaveBeenCalledTimes(1);
    el.remove();
  });
});
