// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The manager's lost card answers, on the desk (CP-05): the queue in words,
// the sweep's own trigger with its counts, and the two resolve acts, each
// carrying what the terminal's own record said.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const svc = vi.hoisted(() => ({
  listTerminalLostItems: vi.fn(),
  resolveTerminalLostItem: vi.fn(),
  sweepTerminalIntents: vi.fn(),
}));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './TerminalLostAnswers';
import type { TerminalLostAnswers } from './TerminalLostAnswers';
import type { TerminalLostItem } from '../../types/pos';

const item = (over: Partial<TerminalLostItem> = {}): TerminalLostItem => ({
  id: 'li1', intent_id: 'in1', branch_id: 'b', register_id: 'REG-07', transaction_id: 'tx1',
  kind: 'sale', amount_cents: 12000, device_id: 'REG-07', provider_ref: 'devref-9', state: 'open',
  late_verdict: '', late_provider_ref: '', ...over,
});

const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;
const all = (el: Element, id: string) => [...el.querySelectorAll(`[data-testid="${id}"]`)] as HTMLElement[];

async function mount(items: TerminalLostItem[]): Promise<TerminalLostAnswers> {
  svc.listTerminalLostItems.mockResolvedValue(items);
  const el = document.createElement('gable-terminal-lost-answers') as TerminalLostAnswers;
  document.body.appendChild(el);
  await new Promise((r) => setTimeout(r, 0));
  await el.updateComplete;
  return el;
}

describe('the manager\'s lost card answers', () => {
  beforeEach(() => { vi.clearAllMocks(); });

  it('lists each lost answer with its amount, register and device reference', async () => {
    const el = await mount([item(), item({ id: 'li2', intent_id: 'in2', kind: 'refund', amount_cents: 450, register_id: 'REG-02', provider_ref: '' })]);
    const rows = all(el, 'lost-answer-item');
    expect(rows.length).toBe(2);
    expect(rows[0].textContent).toContain('a card charge of $120.00');
    expect(rows[0].textContent).toContain('register REG-07');
    expect(rows[0].textContent).toContain('device reference devref-9');
    expect(rows[1].textContent).toContain('a card refund of $4.50');
    expect(rows[1].textContent).not.toContain('device reference');
    el.remove();
  });

  it('says the empty queue in words', async () => {
    const el = await mount([]);
    expect(q(el, 'lost-answers-empty').textContent).toContain('No lost card answers');
    el.remove();
  });

  it('refuses an approval with no reference when the device sent none, in words', async () => {
    const el = await mount([item({ provider_ref: '' })]);
    q(el, 'lost-answer-authorized').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'lost-answers-error').textContent).toContain("Read the terminal's own record first");
    expect(svc.resolveTerminalLostItem).not.toHaveBeenCalled();
    el.remove();
  });

  it('records an approval with no typed reference when the item carries the device\'s own', async () => {
    const el = await mount([item()]);
    svc.resolveTerminalLostItem.mockResolvedValue(item({ state: 'resolved_authorized' }));
    svc.listTerminalLostItems.mockResolvedValue([]);
    q(el, 'lost-answer-authorized').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.resolveTerminalLostItem).toHaveBeenCalledWith('li1', 'authorized', '', '');
    el.remove();
  });

  it('says the device\'s late word on an item, and holds an item another manager is settling', async () => {
    const el = await mount([
      item({ late_verdict: 'authorized', late_provider_ref: 'devref-late' }),
      item({ id: 'li2', intent_id: 'in2', state: 'resolving' }),
    ]);
    const rows = all(el, 'lost-answer-item');
    expect(rows[0].querySelector('[data-testid="lost-answer-late"]')?.textContent)
      .toContain('The terminal answered late: it approved this (reference devref-late).');
    expect(rows[1].querySelector('[data-testid="lost-answer-resolving"]')?.textContent).toContain('settling this one now');
    expect((rows[1].querySelector('[data-testid="lost-answer-authorized"]') as HTMLButtonElement).disabled).toBe(true);
    expect((rows[1].querySelector('[data-testid="lost-answer-declined"]') as HTMLButtonElement).disabled).toBe(true);
    el.remove();
  });

  it('records an approval under the reference the manager read', async () => {
    const el = await mount([item()]);
    const ref = el.querySelector('#lost-ref-li1') as HTMLInputElement;
    ref.value = 'terminal batch 41, line 3';
    ref.dispatchEvent(new Event('input'));
    await el.updateComplete;
    svc.resolveTerminalLostItem.mockResolvedValue(item({ state: 'resolved_authorized' }));
    svc.listTerminalLostItems.mockResolvedValue([]);
    q(el, 'lost-answer-authorized').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.resolveTerminalLostItem).toHaveBeenCalledWith('li1', 'authorized', 'terminal batch 41, line 3', '');
    expect(q(el, 'lost-answers-note').textContent).toContain('the terminal approved $120.00');
    expect(q(el, 'lost-answers-note').textContent).toContain('or back to the card');
    el.remove();
  });

  it('records a decline under the reference the manager read', async () => {
    const el = await mount([item()]);
    const ref = el.querySelector('#lost-ref-li1') as HTMLInputElement;
    ref.value = 'terminal batch 41, line 3';
    ref.dispatchEvent(new Event('input'));
    const reason = el.querySelector('#lost-reason-li1') as HTMLInputElement;
    reason.value = 'the terminal\'s journal says declined';
    reason.dispatchEvent(new Event('input'));
    await el.updateComplete;
    svc.resolveTerminalLostItem.mockResolvedValue(item({ state: 'resolved_declined' }));
    svc.listTerminalLostItems.mockResolvedValue([]);
    q(el, 'lost-answer-declined').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(svc.resolveTerminalLostItem).toHaveBeenCalledWith('li1', 'declined', 'terminal batch 41, line 3', "the terminal's journal says declined");
    expect(q(el, 'lost-answers-note').textContent).toContain('the terminal declined $120.00');
    el.remove();
  });

  it('runs the sweep\'s own trigger and says what it closed and queued', async () => {
    const el = await mount([]);
    svc.sweepTerminalIntents.mockResolvedValue({ claimed_closed: 2, dispatched_lost: 1 });
    svc.listTerminalLostItems.mockResolvedValue([item()]);
    q(el, 'lost-answers-sweep').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'lost-answers-note').textContent).toContain('closed 2 claims');
    expect(q(el, 'lost-answers-note').textContent).toContain('filed 1 lost answer');
    expect(all(el, 'lost-answer-item').length).toBe(1);
    el.remove();
  });

  it('says a refusal in words', async () => {
    const el = await mount([]);
    svc.sweepTerminalIntents.mockRejectedValue(new Error('the terminal sweep is run by a person, never a machine key'));
    q(el, 'lost-answers-sweep').click();
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(q(el, 'lost-answers-error').textContent).toContain('never a machine key');
    el.remove();
  });
});
