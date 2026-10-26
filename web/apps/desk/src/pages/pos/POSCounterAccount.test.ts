// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's account sale, tenders, returns, exchange and documents on the
// screen: the standing line and its three
// ways forward, the buyer pick, the check limit and ID checked, store credit
// as a tender, the reason list and restocking fee on a return, the exchange
// with the server's own figures, the signature pad and the reprint.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Sale, CounterCustomer, CounterStanding, ReturnReason } from '../../types/pos';

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
  approveOverride: vi.fn(),
  stepUpManagers: vi.fn(),
  stepUp: vi.fn(),
  captureSignature: vi.fn(),
  returnReasons: vi.fn(),
  createReturn: vi.fn(),
  previewExchange: vi.fn(),
  createExchange: vi.fn(),
}));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './POSTerminal';
import './StepUpDialog';
import type { POSTerminal } from './POSTerminal';
import type { StepUpDialog } from './StepUpDialog';

/** Completes the step up dialog the terminal opened, as the manager's PIN would. */
async function answerStepUp(el: HTMLElement, managerName = 'E2E Manager') {
  const dlg = el.querySelector<StepUpDialog>('gable-step-up-dialog');
  if (!dlg) throw new Error('no step up dialog opened');
  dlg.dispatchEvent(new CustomEvent('step-up-approved', { detail: { token: 'tok-1', managerName }, bubbles: true }));
  await flush();
  await dlg.updateComplete;
}

import { answerAppDialog, typeIntoAppDialog, chooseOption, flush } from '../../test/dom';
import type { CounterRegister } from '../../types/pos';

const REG_01: CounterRegister = { id: 'REG-01', name: 'Front till', branch_id: 'b-main', branch_code: 'MAIN', branch_name: 'main yard' };

const line = (id: string, cents: number) => ({
  id, position: 1, line_type: 'product', parent_line_id: null, product_id: `p-${id}`,
  charge_code_id: null, charge_code: null, sku: `SKU-${id}`, description: `Item ${id}`,
  quantity: '2', uom: 'EA', unit_price_ten_thousandths: (cents / 2) * 100, line_total_cents: cents,
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

const completed = (over: Partial<Sale> = {}) => sale({ status: 'completed', invoice_id: 'inv-1', ...over });

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
  ...over,
});

const blockedStanding = (): CounterStanding => standing({
  on_account: {
    verdict: 'blocked', reason: 'credit_limit', over_cents: 593,
    message: 'This sale would put the account 5.93 USD over its credit limit of 30.00 USD.',
    ways_forward: [
      { kind: 'pay_down', available: true, amount_cents: 593, message: 'Take 5.93 USD on the account to clear it.' },
      { kind: 'manager_override', available: true, message: 'A manager can approve this sale on account.' },
      { kind: 'switch_tender', available: true, message: 'Pay another way.' },
    ],
  },
});

const reasons: ReturnReason[] = [
  { code: 'defective', label: 'Defective', restocking_fee_bps: 0, needs_note: false },
  { code: 'changed_mind', label: 'Changed their mind', restocking_fee_bps: 1500, needs_note: false },
  { code: 'other', label: 'Other (needs a note)', restocking_fee_bps: 0, needs_note: true },
];

async function mount(s: Sale): Promise<POSTerminal> {
  svc.listRegisters.mockResolvedValue([REG_01]);
  svc.currentTill.mockResolvedValue({ id: 't', register_id: 'REG-01', cashier_id: 'c', status: 'open', opening_float_cents: 20000 });
  svc.startSale.mockResolvedValue(s);
  svc.returnReasons.mockResolvedValue(reasons);
  const el = document.createElement('gable-pos-terminal') as POSTerminal;
  document.body.appendChild(el);
  await flush();
  await el.updateComplete;
  return el;
}

const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;
type Internals = {
  _customer: CounterCustomer | null; _sale: Sale | null; _completed: Sale | null; _standing: CounterStanding | null;
  _syncStanding: () => Promise<void>; _buyerId: string; _tenderMethod: string | null;
};
const inner = (el: POSTerminal) => el as unknown as Internals;

/** An account sale open on screen: the customer named, its standing read. */
async function withAccount(st: CounterStanding, s: Sale = sale({ customer_id: 'cu1' })): Promise<POSTerminal> {
  svc.standing.mockResolvedValue(st);
  const el = await mount(s);
  inner(el)._customer = account;
  inner(el)._sale = s;
  await el.updateComplete;
  await inner(el)._syncStanding();
  await el.updateComplete;
  return el;
}

const typeAmount = async (el: POSTerminal, v: string) => {
  const input = el.querySelector('#pos-tender-amount') as HTMLInputElement;
  input.value = v;
  input.dispatchEvent(new Event('input'));
  await el.updateComplete;
};

describe('can this sale go on account? the standing line and its ways forward', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('says plainly that a sale can go on the account, with no way forward to offer', async () => {
    const el = await withAccount(standing());
    expect(q(el, 'pos-verdict-line').textContent).toContain('This sale can go on the account');
    expect(q(el, 'pos-ways-forward')).toBeNull();
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(false);
    el.remove();
  });

  it('reads the verdict at what is owed, the sale’s id with it', async () => {
    const el = await withAccount(standing());
    expect(svc.standing).toHaveBeenLastCalledWith('cu1', 1198, 's1');
    el.remove();
  });

  it('says why a sale cannot go on the account and offers the three ways forward as actions', async () => {
    const el = await withAccount(blockedStanding());
    expect(q(el, 'pos-verdict-line').textContent).toContain('5.93 USD over its credit limit');
    const kinds = [...el.querySelectorAll('[data-testid="pos-ways-forward"] button')].map((b) => b.getAttribute('data-testid'));
    expect(kinds).toEqual(['pos-way-pay_down', 'pos-way-manager_override', 'pos-way-switch_tender']);
    expect(q(el, 'pos-way-pay_down').textContent).toContain('Take $5.93 on the account');
    // the account tender is off, and the refusal beside the tenders is the server's own words
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(true);
    expect(q(el, 'pos-account-refusal').textContent).toContain('over its credit limit of 30.00 USD');
    el.remove();
  });

  it('pays another way: the way forward chooses cash', async () => {
    const el = await withAccount(blockedStanding());
    q(el, 'pos-way-switch_tender').click();
    await el.updateComplete;
    expect(inner(el)._tenderMethod).toBe('cash');
    el.remove();
  });

  it('opens the account’s own page to take a payment, and the sale stays open', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    const el = await withAccount(blockedStanding());
    q(el, 'pos-way-pay_down').click();
    expect(open).toHaveBeenCalledWith('/accounts/cu1', '_blank');
    expect(inner(el)._sale?.status).toBe('open');
    open.mockRestore();
    el.remove();
  });

  it('asks a manager for one approval, with a reason and the sale’s total as the cap, then lets the charge through', async () => {
    svc.approveOverride.mockResolvedValue({ id: 'o1', sale_id: 's1', kind: 'account', approved_by: 'm', reason: 'phone', cap_cents: 1198, created_at: '', consumed_at: null });
    const el = await withAccount(blockedStanding());
    q(el, 'pos-way-manager_override').click();
    await flush();
    await typeIntoAppDialog('agreed on the phone');
    // once approved, the read counts the approval and the account is allowed
    svc.standing.mockResolvedValue(standing({
      on_account: { verdict: 'allowed', reason: 'manager_override', message: 'A manager approved this sale on account up to 11.98 USD.', over_cents: null, ways_forward: [] },
    }));
    await answerAppDialog(true);
    await flush();
    await answerStepUp(el);
    expect(svc.approveOverride).toHaveBeenCalledWith('s1', 'account', 'agreed on the phone', 1198, 'tok-1');
    expect(q(el, 'pos-verdict-line').textContent).toContain('A manager approved this sale on account');
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(false);
    el.remove();
  });

  it('says the server’s words when the approval is refused (the cashier is no manager)', async () => {
    const { ApiError } = await import('../../services/apiError');
    svc.approveOverride.mockRejectedValue(new ApiError(403, 'forbidden', 'a manager other than the sale’s own cashier approves an override'));
    const el = await withAccount(blockedStanding());
    q(el, 'pos-way-manager_override').click();
    await flush();
    await typeIntoAppDialog('agreed');
    await answerAppDialog(true);
    await flush();
    await answerStepUp(el);
    expect(el.textContent).toContain('a manager other than the sale’s own cashier approves an override');
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(true);
    el.remove();
  });

  it('leaves the lookup’s own words when the verdict cannot be read', async () => {
    svc.standing.mockRejectedValue(new Error('down'));
    const el = await mount(sale({ customer_id: 'cu1' }));
    inner(el)._customer = account;
    inner(el)._sale = sale({ customer_id: 'cu1' });
    await el.updateComplete;
    await inner(el)._syncStanding();
    await el.updateComplete;
    expect(q(el, 'pos-account-verdict')).toBeNull();
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(false);
    el.remove();
  });
});

describe('the buyer pick on an account sale', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const roster = () => standing({
    named_buyer_required: true,
    buyers: [{ id: 'b1', name: 'Ada Buyer', cap_cents: 5000 }, { id: 'b2', name: 'Bo Buyer', cap_cents: null }],
  });

  it('asks for a buyer when the account requires one, and holds the charge until one is named', async () => {
    const el = await withAccount(roster());
    expect(q(el, 'pos-buyer').textContent).toContain('required for a charge');
    expect(q(el, 'pos-buyer-needed').textContent).toContain('asks for a named buyer');
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(true);
    expect(q(el, 'pos-account-refusal').textContent).toContain('asks for a named buyer');
    el.remove();
  });

  it('names the buyer on the sale with the purchase order and the job, so none is cleared', async () => {
    svc.attachCustomer.mockResolvedValue(sale({ customer_id: 'cu1', buyer_contact_id: 'b2' }));
    const el = await withAccount(roster());
    inner(el)._buyerId = '';
    await chooseOption(q(el, 'pos-buyer-pick') as HTMLElement & { value: string }, 'b2');
    await el.updateComplete;
    expect(svc.attachCustomer).toHaveBeenCalledWith('s1', { customer_id: 'cu1', customer_po: null, job_id: null, buyer_contact_id: 'b2' });
    expect(q(el, 'pos-buyer-needed')).toBeNull();
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(false);
    el.remove();
  });

  it('holds a buyer to the buyer’s own cap', async () => {
    const el = await withAccount(roster(), sale({ customer_id: 'cu1', total_cents: 8650, lines: [line('a', 8000)] as unknown as Sale['lines'] }));
    inner(el)._buyerId = 'b1';
    await el.updateComplete;
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(true);
    expect(q(el, 'pos-account-refusal').textContent).toContain('Ada Buyer may put at most $50.00');
    inner(el)._buyerId = 'b2';
    await el.updateComplete;
    expect((q(el, 'tender-account') as HTMLButtonElement).disabled).toBe(false);
    el.remove();
  });

  it('shows no buyer pick for an account with no roster that asks for none', async () => {
    const el = await withAccount(standing());
    expect(q(el, 'pos-buyer')).toBeNull();
    el.remove();
  });
});

describe('a check, its ID and its limit', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const checkEntry = async (el: POSTerminal) => {
    q(el, 'tender-check').click();
    await el.updateComplete;
  };

  it('sends the check number and the kind of ID checked, never an ID number', async () => {
    svc.completeSale.mockResolvedValue(completed());
    const el = await withAccount(standing({ check_limit_cents: 200000 }));
    await checkEntry(el);
    (el.querySelector('#pos-check-ref') as HTMLInputElement).value = '1042';
    el.querySelector('#pos-check-ref')!.dispatchEvent(new Event('input'));
    const idBox = q(el, 'pos-id-checked') as HTMLInputElement;
    idBox.checked = true;
    idBox.dispatchEvent(new Event('change'));
    await el.updateComplete;
    await chooseOption(q(el, 'pos-id-type') as HTMLElement & { value: string }, 'drivers_license');
    await el.updateComplete;
    q(el, 'pos-pay').click();
    await flush();
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [
      { method: 'check', amount_cents: 1198, reference: '1042', id_checked: true, id_type: 'drivers_license' },
    ]);
    expect(el.querySelector('input[name*="number" i]')).toBeNull();
    el.remove();
  });

  it('asks for the kind of ID once ID is checked, before the check can go', async () => {
    const el = await withAccount(standing({ check_limit_cents: 200000 }));
    await checkEntry(el);
    const idBox = q(el, 'pos-id-checked') as HTMLInputElement;
    idBox.checked = true;
    idBox.dispatchEvent(new Event('change'));
    await el.updateComplete;
    q(el, 'pos-pay').click();
    await flush();
    expect(svc.completeSale).not.toHaveBeenCalled();
    expect(el.textContent).toContain('Pick the kind of ID that was checked');
    el.remove();
  });

  it('refuses a check over the customer’s limit in words, with Ask a manager beside it', async () => {
    const el = await withAccount(standing({ check_limit_cents: 1000 }));
    await checkEntry(el);
    q(el, 'pos-pay').click();
    await flush();
    await el.updateComplete;
    expect(svc.completeSale).not.toHaveBeenCalled();
    expect(q(el, 'pos-check-over').textContent).toContain('over the check limit of $10.00');
    expect(q(el, 'pos-check-limit').textContent).toContain('$10.00');
    el.remove();
  });

  it('lets the check go once a manager approved a check that size for this sale', async () => {
    svc.approveOverride.mockResolvedValue({ id: 'o', sale_id: 's1', kind: 'check', approved_by: 'm', reason: 'r', cap_cents: 1198, created_at: '', consumed_at: null });
    svc.completeSale.mockResolvedValue(completed());
    const el = await withAccount(standing({ check_limit_cents: 1000 }));
    await checkEntry(el);
    q(el, 'pos-pay').click();
    await flush();
    await el.updateComplete;
    q(el, 'pos-check-ask-manager').click();
    await flush();
    await typeIntoAppDialog('regular customer');
    await answerAppDialog(true);
    await flush();
    await answerStepUp(el);
    expect(svc.approveOverride).toHaveBeenCalledWith('s1', 'check', 'regular customer', 1198, 'tok-1');
    q(el, 'pos-pay').click();
    await flush();
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [{ method: 'check', amount_cents: 1198 }]);
    el.remove();
  });

  it('says a check over the database wide limit in the server’s words, with Ask a manager beside them', async () => {
    const { ApiError } = await import('../../services/apiError');
    svc.completeSale.mockRejectedValue(new ApiError(409, 'conflict', 'a check of 11.98 USD is over this customer’s check limit of 10.00 USD',
      [{ message: 'over', code: 'check_over_limit' }]));
    const el = await mount(sale());
    q(el, 'pos-walk-in').click();
    await flush();
    await el.updateComplete;
    q(el, 'tender-check').click();
    await el.updateComplete;
    q(el, 'pos-pay').click();
    await flush();
    await el.updateComplete;
    expect(q(el, 'pos-check-over').textContent).toContain('over this customer');
    expect(q(el, 'pos-check-ask-manager')).toBeTruthy();
    el.remove();
  });
});

describe('store credit as a tender', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  it('shows store credit only while the account holds any', async () => {
    const el = await withAccount(standing());
    expect(q(el, 'tender-store_credit')).toBeNull();
    el.remove();
  });

  it('offers what the account holds, up to what is owed, and splits the rest to cash', async () => {
    svc.completeSale.mockResolvedValue(completed());
    const el = await withAccount(standing({ open_credit_memo_cents: 599 }));
    expect(q(el, 'tender-store_credit').textContent).toContain('Store credit');
    q(el, 'tender-store_credit').click();
    await el.updateComplete;
    q(el, 'pos-add-tender').click();
    await el.updateComplete;
    expect(q(el, 'pos-added-tenders').textContent).toContain('$5.99');
    q(el, 'tender-cash').click();
    await el.updateComplete;
    q(el, 'pos-pay').click();
    await flush();
    expect(svc.completeSale).toHaveBeenCalledWith('s1', 3, [
      { method: 'store_credit', amount_cents: 599 },
      { method: 'cash', amount_cents: 599 },
    ]);
    el.remove();
  });

  it('refuses more store credit than the account holds, in words', async () => {
    const el = await withAccount(standing({ open_credit_memo_cents: 599 }));
    q(el, 'tender-store_credit').click();
    await el.updateComplete;
    await typeAmount(el, '8.00');
    q(el, 'pos-add-tender').click();
    await el.updateComplete;
    expect(el.textContent).toContain('The account holds $5.99 of store credit');
    expect(q(el, 'pos-added-tenders')).toBeNull();
    // and typed to cover the whole sale, Pay says the same and sends nothing
    await typeAmount(el, '11.98');
    q(el, 'pos-pay').click();
    await flush();
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });
});

describe('returns: the reason on each line, the note, the fee', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const openReturn = async (): Promise<POSTerminal> => {
    const el = await mount(completed());
    inner(el)._completed = completed();
    inner(el)._sale = completed();
    await el.updateComplete;
    const ret = [...q(el, 'pos-complete').querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Return') as HTMLButtonElement;
    ret.click();
    await flush();
    await el.updateComplete;
    return el;
  };
  const setQty = async (el: POSTerminal, v: string) => {
    const input = q(el, 'return-qty') as HTMLInputElement;
    input.value = v;
    input.dispatchEvent(new Event('input'));
    await el.updateComplete;
  };

  it('lists the closed reasons with the fee each carries once a line is named', async () => {
    const el = await openReturn();
    expect(q(el, 'return-reason')).toBeNull();
    await setQty(el, '1');
    expect(q(el, 'return-reason')).toBeTruthy();
    const labels = ((q(el, 'return-reason') as unknown as { options: Array<{ label: string }> }).options).map((o) => o.label);
    expect(labels).toEqual(['Why is it coming back?', 'Defective', 'Changed their mind (15% restocking fee)', 'Other (needs a note)']);
    expect(q(el, 'return-fee-note').textContent).toContain('restocking fee');
    el.remove();
  });

  it('wants a reason on each returned line and the note when the reason is other', async () => {
    const el = await openReturn();
    await setQty(el, '1');
    q(el, 'return-take').click();
    await flush();
    expect(svc.createReturn).not.toHaveBeenCalled();
    expect(el.textContent).toContain('Pick a reason for each line coming back');
    await chooseOption(q(el, 'return-reason') as HTMLElement & { value: string }, 'other');
    await el.updateComplete;
    q(el, 'return-take').click();
    await flush();
    expect(svc.createReturn).not.toHaveBeenCalled();
    expect(el.textContent).toContain('needs a note saying why');
    el.remove();
  });

  it('sends the reason code on the line, no note needed for a coded reason, and states the fee withheld', async () => {
    svc.createReturn.mockResolvedValue({ number: 'RTN-1', total_cents: -551, refund_method: 'cash', restocking_fee_cents: 82 });
    const el = await openReturn();
    await setQty(el, '1');
    await chooseOption(q(el, 'return-reason') as HTMLElement & { value: string }, 'changed_mind');
    await el.updateComplete;
    q(el, 'return-take').click();
    await flush();
    await el.updateComplete;
    expect(svc.createReturn).toHaveBeenCalledWith(expect.objectContaining({
      original_sale_id: 's1', lines: [{ line_id: 'a', quantity: '1', restock: true, reason_code: 'changed_mind' }],
    }));
    const call = svc.createReturn.mock.calls[0][0] as { reason?: string };
    expect(call.reason).toBeUndefined();
    expect(el.textContent).toContain('less a $0.82 restocking fee');
    el.remove();
  });
});

describe('the exchange: the server states what it comes to', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); vi.useRealTimers(); });

  const openExchange = async (): Promise<POSTerminal> => {
    const el = await mount(completed({ customer_id: 'cu1' }));
    inner(el)._completed = completed({ customer_id: 'cu1' });
    inner(el)._sale = completed({ customer_id: 'cu1' });
    await el.updateComplete;
    const ret = [...q(el, 'pos-complete').querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Return') as HTMLButtonElement;
    ret.click();
    await flush();
    await el.updateComplete;
    const input = q(el, 'return-qty') as HTMLInputElement;
    input.value = '1';
    input.dispatchEvent(new Event('input'));
    await el.updateComplete;
    await chooseOption(q(el, 'return-reason') as HTMLElement & { value: string }, 'defective');
    await el.updateComplete;
    q(el, 'return-exchange-toggle').click();
    await el.updateComplete;
    svc.searchProducts.mockResolvedValue([{ product_id: 'p-new', sku: 'NEW-1', description: 'New board', uom: 'EA', unit_price_cents: 599, in_stock: '10' }]);
    const search = q(el, 'exchange-search') as HTMLInputElement;
    search.value = 'new';
    search.dispatchEvent(new Event('input'));
    await new Promise((r) => setTimeout(r, 260));
    await el.updateComplete;
    (el.querySelector('[data-testid="exchange-results"] button') as HTMLButtonElement).click();
    await el.updateComplete;
    return el;
  };

  it('previews with the server’s figures and says what the customer pays', async () => {
    svc.previewExchange.mockResolvedValue({ returned_cents: 599, restocking_fee_cents: 0, replacement_cents: 1198, difference_cents: 599 });
    const el = await openExchange();
    await new Promise((r) => setTimeout(r, 360));
    await el.updateComplete;
    expect(svc.previewExchange).toHaveBeenCalledWith(expect.objectContaining({
      original_sale_id: 's1',
      returned_lines: [{ line_id: 'a', quantity: '1', restock: true, reason_code: 'defective' }],
      sale_lines: [{ product_id: 'p-new', quantity: '1' }],
    }));
    expect(q(el, 'exchange-difference').textContent).toContain('Customer pays $5.99');
    expect(q(el, 'exchange-totals').textContent).toContain('$11.98');
    expect(q(el, 'exchange-tender')).toBeTruthy();
    el.remove();
  });

  it('takes the exchange with a tender for the difference only', async () => {
    svc.previewExchange.mockResolvedValue({ returned_cents: 599, restocking_fee_cents: 0, replacement_cents: 1198, difference_cents: 599 });
    svc.createExchange.mockResolvedValue({
      returned_cents: 599, restocking_fee_cents: 0, replacement_cents: 1198, difference_cents: 599, refunded_cents: 0,
      return: { number: 'RTN-2' }, sale: completed({ id: 's2', number: 'POS-2' }),
    });
    const el = await openExchange();
    await new Promise((r) => setTimeout(r, 360));
    await el.updateComplete;
    q(el, 'exchange-take').click();
    await flush();
    await el.updateComplete;
    expect(svc.createExchange).toHaveBeenCalledWith(expect.objectContaining({
      tenders: [{ method: 'cash', amount_cents: 599 }],
    }));
    expect(el.textContent).toContain('Exchange RTN-2 complete');
    expect(inner(el)._sale?.id).toBe('s2');
    el.remove();
  });

  it('says what goes back when the return is worth more, and takes no tender', async () => {
    svc.previewExchange.mockResolvedValue({ returned_cents: 1198, restocking_fee_cents: 0, replacement_cents: 599, difference_cents: -599 });
    svc.createExchange.mockResolvedValue({
      returned_cents: 1198, restocking_fee_cents: 0, replacement_cents: 599, difference_cents: -599, refunded_cents: 599,
      return: { number: 'RTN-3' }, sale: completed({ id: 's3' }),
    });
    const el = await openExchange();
    await new Promise((r) => setTimeout(r, 360));
    await el.updateComplete;
    expect(q(el, 'exchange-difference').textContent).toContain('Pay the customer back $5.99');
    expect(q(el, 'exchange-refund')).toBeTruthy();
    q(el, 'exchange-take').click();
    await flush();
    await el.updateComplete;
    const body = svc.createExchange.mock.calls[0][0] as { tenders?: unknown };
    expect(body.tenders).toBeUndefined();
    el.remove();
  });

  it('says the server’s words when the exchange cannot be priced', async () => {
    const { ApiError } = await import('../../services/apiError');
    svc.previewExchange.mockRejectedValue(new ApiError(409, 'conflict', 'the line sold 2 and 1 is already returned: 2 more is refused'));
    const el = await openExchange();
    await new Promise((r) => setTimeout(r, 360));
    await el.updateComplete;
    expect(q(el, 'exchange-error').textContent).toContain('2 more is refused');
    expect((q(el, 'exchange-take') as HTMLButtonElement).disabled).toBe(false);
    el.remove();
  });
});

describe('the signature and the reprint on the completed sale', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

  const done = async (over: Partial<Sale> = {}): Promise<POSTerminal> => {
    const el = await mount(completed(over));
    inner(el)._completed = completed(over);
    inner(el)._sale = completed(over);
    await el.updateComplete;
    return el;
  };

  const draw = async (el: POSTerminal) => {
    const pad = el.querySelector('gable-signature-pad') as HTMLElement & { strokes: unknown[]; png: () => string | null };
    const canvas = pad.querySelector('canvas') as HTMLCanvasElement;
    // jsdom has no canvas: the pad keeps the strokes as points and asks the canvas for its PNG
    canvas.toDataURL = () => 'data:image/png;base64,iVBORw0KGgo=';
    canvas.getBoundingClientRect = () => ({ left: 0, top: 0, width: 360, height: 140, right: 360, bottom: 140, x: 0, y: 0, toJSON: () => ({}) });
    const fire = (type: string, x: number, y: number) =>
      canvas.dispatchEvent(Object.assign(new Event(type, { bubbles: true, cancelable: true }), { clientX: x, clientY: y, pointerId: 1 }));
    fire('pointerdown', 10, 10);
    fire('pointermove', 60, 40);
    fire('pointermove', 90, 20);
    fire('pointerup', 90, 20);
    await el.updateComplete;
  };

  it('refuses to seal a blank pad or an unnamed signer, in words', async () => {
    const el = await done({ tenders: [{ method: 'account', amount_cents: 1198 } as never] });
    expect(q(el, 'pos-signature').textContent).toContain('account sign-off');
    q(el, 'pos-signature-seal').click();
    await el.updateComplete;
    expect(q(el, 'pos-signature-note').textContent).toContain('signer');
    const name = q(el, 'pos-signer-name') as HTMLInputElement;
    name.value = 'Pat Signer';
    name.dispatchEvent(new Event('input'));
    await el.updateComplete;
    q(el, 'pos-signature-seal').click();
    await el.updateComplete;
    expect(q(el, 'pos-signature-note').textContent).toContain('Draw the signature');
    expect(svc.captureSignature).not.toHaveBeenCalled();
    el.remove();
  });

  it('seals the typed name and the drawn PNG, and then says it is sealed', async () => {
    svc.captureSignature.mockResolvedValue({ sale_id: 's1', signer_name: 'Pat Signer', signed_at: '', total_cents: 1198, image_sha256: 'x', seal: 'abcdef0123456789abcdef', captured_by: '' });
    const el = await done();
    const name = q(el, 'pos-signer-name') as HTMLInputElement;
    name.value = 'Pat Signer';
    name.dispatchEvent(new Event('input'));
    await draw(el);
    q(el, 'pos-signature-seal').click();
    await flush();
    await el.updateComplete;
    expect(svc.captureSignature).toHaveBeenCalledWith('s1', 'Pat Signer', 'iVBORw0KGgo=');
    expect(q(el, 'pos-signature-sealed').textContent).toContain('Signed by Pat Signer, sealed abcdef012345');
    expect(q(el, 'pos-signature-seal')).toBeNull();
    el.remove();
  });

  it('clears the pad and says the server’s words when the seal is refused', async () => {
    const { ApiError } = await import('../../services/apiError');
    svc.captureSignature.mockRejectedValue(new ApiError(409, 'conflict', 'this sale is already signed: a sealed signature never changes'));
    const el = await done();
    const name = q(el, 'pos-signer-name') as HTMLInputElement;
    name.value = 'Pat Signer';
    name.dispatchEvent(new Event('input'));
    await draw(el);
    q(el, 'pos-signature-seal').click();
    await flush();
    await el.updateComplete;
    expect(q(el, 'pos-signature-note').textContent).toContain('already signed');
    el.remove();
  });

  it('counts each print and says Reprint after the first', async () => {
    svc.printReceipt.mockResolvedValue({ url: 'blob:x', blocked: false });
    svc.getSale.mockResolvedValue(completed({ print_count: 1 }));
    const el = await done();
    expect(q(el, 'pos-receipt-print').textContent).toContain('Print receipt');
    expect(q(el, 'pos-print-count')).toBeNull();
    q(el, 'pos-receipt-print-80').click();
    await flush();
    await el.updateComplete;
    expect(svc.printReceipt).toHaveBeenCalledWith('s1', 'thermal80');
    expect(q(el, 'pos-receipt-print').textContent).toContain('Reprint receipt');
    expect(q(el, 'pos-receipt-print-80').textContent).toContain('Reprint 80mm');
    expect(q(el, 'pos-print-count').textContent).toContain('Printed 1 time');
    el.remove();
  });

  it('says the server’s words when the 80mm layout is not available', async () => {
    svc.printReceipt.mockRejectedValue(new Error('the 80mm receipt layout is not available on this server: print the default layout'));
    const el = await done();
    q(el, 'pos-receipt-print-80').click();
    await flush();
    await el.updateComplete;
    expect(q(el, 'pos-receipt-note').textContent).toContain('not available on this server');
    el.remove();
  });
});
