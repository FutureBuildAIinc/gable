// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The manager's open refund intents (CP-06): the queue in words, the pay back
// act for a failed void, and the resolve act for a pending claim.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const svc = vi.hoisted(() => ({
  listRefundIntents: vi.fn(),
  paybackRefundIntent: vi.fn(),
  resolveRefundIntent: vi.fn(),
}));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './RefundIntents';
import type { RefundIntents } from './RefundIntents';
import type { RefundIntent } from '../../types/pos';

const intent = (over: Partial<RefundIntent> = {}): RefundIntent => ({
  id: 'ri1',
  sale_id: 's1',
  sale_number: 'POS-000001',
  tender_id: 't1',
  tender_method: 'card',
  amount_cents: 12500,
  act: 'return',
  state: 'PENDING',
  held_credit_cents: 0,
  created_at: new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString(),
  ...over,
});

const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;
const all = (el: Element, id: string) => [...el.querySelectorAll(`[data-testid="${id}"]`)] as HTMLElement[];

async function mount(items: RefundIntent[]): Promise<RefundIntents> {
  svc.listRefundIntents.mockResolvedValue(items);
  const el = document.createElement('gable-refund-intents') as RefundIntents;
  document.body.appendChild(el);
  await new Promise((r) => setTimeout(r, 0));
  await el.updateComplete;
  return el;
}

describe('the manager\'s refund intents', () => {
  beforeEach(() => { vi.clearAllMocks(); });

  it('lists each intent with sale number, amount, tender, state and age', async () => {
    const el = await mount([
      intent(),
      intent({ id: 'ri2', sale_number: 'POS-000002', tender_method: 'cash', amount_cents: 450, state: 'FAILED', held_credit_cents: 450, created_at: new Date(Date.now() - 2 * 24 * 60 * 60 * 1000).toISOString() }),
    ]);
    const rows = all(el, 'refund-intent-item');
    expect(rows.length).toBe(2);
    expect(q(rows[0], 'refund-intent-amount').textContent).toContain('$125.00');
    expect(rows[0].textContent).toContain('POS-000001');
    expect(rows[0].textContent).toContain('Card tender');
    expect(q(rows[0], 'refund-intent-state').textContent).toContain("Waiting on the terminal's answer");
    expect(q(rows[0], 'refund-intent-age').textContent).toContain('waiting 3 hours');
    expect(q(rows[1], 'refund-intent-state').textContent).toContain('Refund declined - the credit is held');
    expect(q(rows[1], 'refund-intent-age').textContent).toContain('waiting 2 days');
    el.remove();
  });

  it('says the empty queue in words', async () => {
    const el = await mount([]);
    expect(q(el, 'refund-intents-empty').textContent).toContain('No open refund intents');
    el.remove();
  });

  it('pays back a failed intent with the reason and reloads', async () => {
    const el = await mount([intent({ state: 'FAILED', held_credit_cents: 12500 })]);
    svc.listRefundIntents.mockResolvedValue([]);
    svc.paybackRefundIntent.mockResolvedValue({ id: 'ri1', payback_id: 'pb1', sale_id: 's1', tender_id: 't1', amount_cents: 12500, refund_tx_id: 'refund-tx-9', state: 'DONE' });
    q(el, 'refund-intent-payback').click();
    await el.updateComplete;
    const reason = el.querySelector('#payback-reason-ri1') as HTMLInputElement;
    reason.value = 'customer left with the credit';
    reason.dispatchEvent(new Event('input'));
    await el.updateComplete;
    q(el, 'refund-intent-payback-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.paybackRefundIntent).toHaveBeenCalledWith('ri1', 'customer left with the credit');
    expect(q(el, 'refund-intents-note').textContent).toContain('Paid back $125.00');
    expect(q(el, 'refund-intents-empty')).toBeTruthy();
    el.remove();
  });

  it('resolves a pending intent with refund id and reason', async () => {
    const el = await mount([intent()]);
    svc.listRefundIntents.mockResolvedValue([]);
    svc.resolveRefundIntent.mockResolvedValue({ id: 'ri1', sale_id: 's1', tender_id: 't1', act: 'return', state: 'DONE', outcome: 'refund', refund_tx_id: 'refund-tx-9', reconciliation_reference: null });
    q(el, 'refund-intent-resolve').click();
    await el.updateComplete;
    const ref = el.querySelector('#resolve-ref-ri1') as HTMLInputElement;
    ref.value = 'refund-tx-9';
    ref.dispatchEvent(new Event('input'));
    const reason = el.querySelector('#resolve-reason-ri1') as HTMLInputElement;
    reason.value = 'terminal journal shows refund';
    reason.dispatchEvent(new Event('input'));
    await el.updateComplete;
    q(el, 'refund-intent-resolve-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.resolveRefundIntent).toHaveBeenCalledWith('ri1', { outcome: 'refund', refund_tx_id: 'refund-tx-9', reason: 'terminal journal shows refund' });
    expect(q(el, 'refund-intents-note').textContent).toContain('terminal refunded $125.00');
    el.remove();
  });

  it('resolves a pending intent as no refund with reconciliation reference and reason', async () => {
    const el = await mount([intent()]);
    svc.resolveRefundIntent.mockResolvedValue({ id: 'ri1', sale_id: 's1', tender_id: 't1', act: 'return', state: 'FAILED', outcome: 'no_refund', refund_tx_id: null, reconciliation_reference: 'recon-77' });
    q(el, 'refund-intent-resolve').click();
    await el.updateComplete;
    q(el, 'refund-intent-resolve-none').click();
    await el.updateComplete;
    const ref = el.querySelector('#resolve-ref-ri1') as HTMLInputElement;
    ref.value = 'recon-77';
    ref.dispatchEvent(new Event('input'));
    const reason = el.querySelector('#resolve-reason-ri1') as HTMLInputElement;
    reason.value = 'terminal journal shows no refund';
    reason.dispatchEvent(new Event('input'));
    await el.updateComplete;
    q(el, 'refund-intent-resolve-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.resolveRefundIntent).toHaveBeenCalledWith('ri1', { outcome: 'no_refund', reconciliation_reference: 'recon-77', reason: 'terminal journal shows no refund' });
    el.remove();
  });

  it('shows a server error in words', async () => {
    const el = await mount([intent({ state: 'FAILED', held_credit_cents: 12500 })]);
    svc.paybackRefundIntent.mockRejectedValue(new Error('the payback route needs a reason'));
    q(el, 'refund-intent-payback').click();
    await el.updateComplete;
    const reason = el.querySelector('#payback-reason-ri1') as HTMLInputElement;
    reason.value = 'e2e payback';
    reason.dispatchEvent(new Event('input'));
    await el.updateComplete;
    q(el, 'refund-intent-payback-submit').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'refund-intents-error').textContent).toContain('the payback route needs a reason');
    el.remove();
  });
});
