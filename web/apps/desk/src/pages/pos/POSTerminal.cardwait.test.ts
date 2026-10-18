// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The card the terminal still holds, on the desk (CP-05): the plain waiting
// state while the intent is pending at the device, the cancel that says
// plainly the device may still answer, and the declined and no answer
// outcomes in words.

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
import type { TerminalIntent } from '../../types/pos';

const INTENT_ID = '9c8b7a6d-5e4f-4c3b-2a19-080715d4e3f2';
const REG_01 = { id: 'REG-01', name: 'Front till', branch_id: 'b-main', branch_code: 'MAIN', branch_name: 'main yard' };
const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;

const line = (id: string, cents: number) => ({
  id, position: 1, line_type: 'product', parent_line_id: null, product_id: `p-${id}`,
  charge_code_id: null, charge_code: null, sku: `SKU-${id}`, description: `Item ${id}`,
  quantity: '1', uom: 'EA', unit_price_ten_thousandths: cents * 100, line_total_cents: cents,
});

function sale(): Sale {
  return {
    id: 's1', number: 'S-1', revision: 3, branch_id: 'b', register_id: 'REG-01', cashier_id: 'c',
    customer_id: null, currency: 'USD', subtotal_cents: 8000, tax_cents: 650, total_cents: 8650,
    change_cents: 0, till_session_id: 't', status: 'open', invoice_id: null, completed_at: null,
    created_at: '', customer_po: null, job_id: null, tenders: [],
    lines: [line('a', 8650)],
  } as unknown as Sale;
}

const intent = (state: TerminalIntent['state']): TerminalIntent => ({
  id: INTENT_ID, transaction_id: 's1', leg_key: 'card-0', kind: 'sale', state,
  amount_cents: 8650, provider_ref: '', answer_verdict: '',
});

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

/** Pays by card: the tender the waiting refusal is about. */
async function payByCard(el: POSTerminal) {
  q(el, 'tender-card').click();
  await el.updateComplete;
  q(el, 'pos-pay').click();
  await new Promise((r) => setTimeout(r, 0));
  await el.updateComplete;
}

describe('the card the terminal still holds', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('waits in plain words with the amount, and its cancel never claims the charge was stopped', async () => {
    const el = await mount(sale());
    svc.completeSale.mockRejectedValue(new Error(
      `the terminal has the card leg and has not answered (intent ${INTENT_ID}): the sale is not completed`));
    svc.getTerminalIntent.mockResolvedValue(intent('pending_at_device'));
    await payByCard(el);

    const card = q(el, 'pos-card-wait');
    expect(card).toBeTruthy();
    expect(q(el, 'pos-card-wait-title').textContent).toBe('Card at the terminal');
    expect(q(el, 'pos-card-wait-body').textContent).toContain('has not answered yet');
    expect(q(el, 'pos-card-wait-amount').textContent).toBe('$86.50');
    expect(q(el, 'pos-card-cancel')).toBeTruthy();
    expect(q(el, 'pos-card-cancel-words').textContent).toContain('does not stop the terminal');
    expect(q(el, 'pos-card-cancel-words').textContent).toContain('may still move');
    el.remove();
  });

  it('says the device\'s own refusal beside the card when the cancel is refused', async () => {
    const el = await mount(sale());
    svc.completeSale.mockRejectedValue(new Error(
      `the terminal has the card leg and has not answered (intent ${INTENT_ID}): the sale is not completed`));
    svc.getTerminalIntent.mockResolvedValue(intent('pending_at_device'));
    await payByCard(el);

    svc.cancelTerminalIntent.mockRejectedValue(new Error('the terminal has the card leg: a cancel cannot recall it. The money may still move; the sweep files the manager item once the lease runs out'));
    q(el, 'pos-card-cancel').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'pos-card-note').textContent).toContain('a cancel cannot recall it');
    expect(svc.cancelTerminalIntent).toHaveBeenCalledWith(INTENT_ID);
    el.remove();
  });

  it('closes the waiting when the cancel is taken, in words', async () => {
    const el = await mount(sale());
    svc.completeSale.mockRejectedValue(new Error(
      `the terminal has the card leg and has not answered (intent ${INTENT_ID}): the sale is not completed`));
    svc.getTerminalIntent.mockResolvedValue(intent('pending_at_device'));
    await payByCard(el);

    svc.cancelTerminalIntent.mockResolvedValue(intent('lost'));
    q(el, 'pos-card-cancel').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'pos-card-wait-title').textContent).toBe('The terminal did not answer');
    expect(q(el, 'pos-card-wait-body').textContent).toContain('terminal was never reached');
    expect(q(el, 'pos-card-note').textContent).toContain('no money moved');
    el.remove();
  });

  it('says an approval that came after the wait, and tells the cashier to pay again', async () => {
    const el = await mount(sale());
    svc.completeSale.mockRejectedValue(new Error(
      `the terminal has the card leg and has not answered (intent ${INTENT_ID}): the sale is not completed`));
    svc.getTerminalIntent.mockResolvedValue(intent('authorized'));
    await payByCard(el);
    expect(q(el, 'pos-card-wait-title').textContent).toBe('Card approved');
    expect(q(el, 'pos-card-wait-body').textContent).toContain('Pay again with the same card tender');
    expect(q(el, 'pos-card-cancel')).toBeFalsy();
    el.remove();
  });

  it('clears the waiting card when the pay that follows completes the sale', async () => {
    const el = await mount(sale());
    svc.completeSale.mockRejectedValueOnce(new Error(
      `the terminal has the card leg and has not answered (intent ${INTENT_ID}): the sale is not completed`));
    svc.getTerminalIntent.mockResolvedValue(intent('authorized'));
    await payByCard(el);
    expect(q(el, 'pos-card-wait')).toBeTruthy();
    svc.completeSale.mockResolvedValue({ ...sale(), status: 'completed' });
    q(el, 'pos-pay').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'pos-card-wait')).toBeFalsy();
    el.remove();
  });

  it('says a decline in words, with the sale still open', async () => {
    const el = await mount(sale());
    svc.completeSale.mockRejectedValue(new Error('the card was declined at the terminal'));
    await payByCard(el);
    expect(q(el, 'pos-card-wait')).toBeFalsy();
    const band = el.querySelector('[role="alert"]');
    expect(band?.textContent).toContain('The card was declined at the terminal');
    expect(band?.textContent).toContain('The sale is still open');
    el.remove();
  });

  it('says the no answer outcome in words and points at the manager\'s list', async () => {
    const el = await mount(sale());
    svc.completeSale.mockRejectedValue(new Error(
      `the terminal has the card leg and has not answered (intent ${INTENT_ID}): the sale is not completed`));
    svc.getTerminalIntent.mockResolvedValue({ ...intent('lost'), provider_ref: 'devref-1' });
    await payByCard(el);

    expect(q(el, 'pos-card-wait-title').textContent).toBe('The terminal did not answer');
    expect(q(el, 'pos-card-wait-body').textContent).toContain('manager');
    const link = q(el, 'pos-card-wait-lost') as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('/pos/lost-answers');
    expect(q(el, 'pos-card-dismiss')).toBeTruthy();
    el.remove();
  });
});
