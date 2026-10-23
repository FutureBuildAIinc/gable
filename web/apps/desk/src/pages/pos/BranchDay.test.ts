// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import './BranchDay';
import './TillSignoffQueue';
import type { GableBranchDay } from './BranchDay';
import type { GableTillSignoffQueue } from './TillSignoffQueue';
import { branchContext } from '../../services/BranchContext';
import { addDays, todayDay } from '../../lib/dates';
import { chooseOption, flush, jsonResponse, mountAsync, q, text, update } from '../../test/dom';
import type { BranchDay, DayFigures } from '../../types/pos';

// days are relative to today: the screens read the dates they are given, none is fixed
const DAY = todayDay();
const NEXT_DAY = addDays(DAY, 1);
const AFTER = addDays(DAY, 2);

const figures = (patch: Partial<DayFigures> = {}): DayFigures => ({
  session_count: 1, sale_count: 2, sales_total_cents: 1198, tendered_by_method: { CASH: 1198 }, movement_totals_by_kind: {},
  expected_by_method: { CASH: 11198 }, counted_by_method: { CASH: 11148 }, over_short_cents: -50,
  cash_to_deposit_cents: 1148, checks_to_deposit_cents: 0, card_total_cents: 0, ...patch,
});

const day = (patch: Partial<BranchDay> = {}): BranchDay => ({
  branch_id: 'b-1', business_date: DAY,
  registers: [{ register_id: 'REG-01', sessions: [], ...figures() }],
  totals: figures(), open_session_count: 0, pending_signoff_count: 0, ready_to_close: true,
  collections_net_cents: 1198, close: null, ...patch,
});

const closed = {
  id: 'c-1', branch_id: 'b-1', business_date: DAY, deposit_slip_ref: 'SLIP-9', closed_by: 'mgr-1',
  closed_at: `${DAY}T22:00:00Z`, cash_to_deposit_cents: 1148, checks_to_deposit_cents: 0, card_total_cents: 0, bank_account_id: 'ba-2', gl_entry_id: 'e-1', deposit_match: null,
};

let requests: { method: string; url: string; body?: Record<string, unknown> }[] = [];
let handler: (method: string, url: string) => Response;

const banks = [
  { id: 'ba-1', name: 'Operating account', account_number: '1', routing_number: '', gl_account_id: 'g', is_active: true, created_at: '' },
  { id: 'ba-2', name: 'Payroll account', account_number: '2', routing_number: '', gl_account_id: 'g', is_active: true, created_at: '' },
];

beforeEach(() => {
  requests = [];
  localStorage.setItem('token', 'test-token');
  branchContext.setCurrent('b-1');
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET';
    requests.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (url.includes('/bankrecon/accounts')) return jsonResponse(banks);
    return handler(method, url);
  }));
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
  branchContext.setCurrent(null);
});

describe('the branch end of day', () => {
  it('reads the branch and prints each register and the branch to the cent', async () => {
    handler = () => jsonResponse(day());
    const el = await mountAsync<GableBranchDay>('gable-branch-day');
    expect(requests[0].url).toContain('/api/v1/pos/end-of-day?branch_id=b-1');
    expect(text(q(el, '[data-testid="day-register-REG-01"]'))).toContain('REG-01 1 $11.98 $111.98 $111.48 -$0.50');
    expect(text(q(el, '[data-testid="day-totals"]'))).toContain('Branch 1 $11.98 $111.98 $111.48 -$0.50');
    expect(text(q(el, '[data-testid="day-cash"]'))).toBe('$11.48');
    expect(text(q(el, '[data-testid="day-checks"]'))).toBe('$0.00');
    expect(text(q(el, '[data-testid="day-card"]'))).toBe('$0.00');
  });

  it('closes the day with the slip reference once every drawer is settled', async () => {
    handler = (method) => (method === 'POST' ? jsonResponse(day({ close: closed }), 201) : jsonResponse(day()));
    const el = await mountAsync<GableBranchDay>('gable-branch-day');
    const button = q<HTMLButtonElement>(el, '[data-testid="day-close"]');
    expect(button.disabled).toBe(true); // no slip reference yet
    const input = q<HTMLInputElement>(el, '[data-testid="day-slip-input"]');
    input.value = 'SLIP-9';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await update(el, {});
    // cash is to be deposited: the bank account it goes to is chosen first
    expect(q<HTMLButtonElement>(el, '[data-testid="day-close"]').disabled).toBe(true);
    await chooseOption(q<HTMLSelectElement & { value: string }>(el, '[data-testid="day-bank"]'), 'ba-2');
    await update(el, {});
    expect(q<HTMLButtonElement>(el, '[data-testid="day-close"]').disabled).toBe(false);
    q<HTMLButtonElement>(el, '[data-testid="day-close"]').click();
    await flush();
    await update(el, {});
    const post = requests.find((r) => r.method === 'POST');
    expect(post?.url).toContain('/api/v1/pos/end-of-day/close');
    expect(post?.body).toEqual({ branch_id: 'b-1', date: DAY, deposit_slip_ref: 'SLIP-9', bank_account_id: 'ba-2' });
    expect(text(q(el, '[data-testid="day-closed"]'))).toContain('Closed by mgr-1');
    expect(text(q(el, '[data-testid="day-slip"]'))).toBe('SLIP-9');
    expect(el.querySelector('[data-testid="day-close"]')).toBeNull();
  });

  it('will not close while a drawer is open or waits for sign off, and says which', async () => {
    handler = () => jsonResponse(day({ ready_to_close: false, open_session_count: 1, pending_signoff_count: 2 }));
    const el = await mountAsync<GableBranchDay>('gable-branch-day');
    const input = q<HTMLInputElement>(el, '[data-testid="day-slip-input"]');
    input.value = 'SLIP-9';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await update(el, {});
    expect(q<HTMLButtonElement>(el, '[data-testid="day-close"]').disabled).toBe(true);
    const why = text(q(el, '[data-testid="day-blocked"]'));
    expect(why).toContain('1 drawer is still open');
    expect(why).toContain('2 counted drawers wait for a manager');
  });

  it('shows the server\'s refusal of a close', async () => {
    handler = (method) => (method === 'POST'
      ? jsonResponse({ error: { code: 'conflict', message: 'the branch day cannot close while a till is open or waiting for sign off' }, meta: { request_id: 'r' } }, 409)
      : jsonResponse(day()));
    const el = await mountAsync<GableBranchDay>('gable-branch-day');
    const input = q<HTMLInputElement>(el, '[data-testid="day-slip-input"]');
    input.value = 'SLIP-9';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await chooseOption(q<HTMLSelectElement & { value: string }>(el, '[data-testid="day-bank"]'), 'ba-1');
    await update(el, {});
    q<HTMLButtonElement>(el, '[data-testid="day-close"]').click();
    await flush();
    await update(el, {});
    expect(text(q(el, '[data-testid="branch-day-error"]'))).toContain('cannot close');
  });

  it('matches the deposit to a bank line and shows the difference', async () => {
    const matched = { ...closed, deposit_match: {
      bank_transaction_id: 'bt-1', bank_amount_cents: 1150, bank_transaction_date: NEXT_DAY, bank_description: 'DEPOSIT',
      difference_cents: 2, matched_by: 'fin-1', matched_at: `${NEXT_DAY}T14:00:00Z` } };
    handler = (method, url) => {
      if (method === 'POST') return jsonResponse(matched);
      if (url.includes('deposit-candidates')) {
        return jsonResponse({ items: [
          { bank_transaction_id: 'bt-1', transaction_date: NEXT_DAY, amount_cents: 1150, description: 'DEPOSIT', reference: '' },
          { bank_transaction_id: 'bt-2', transaction_date: AFTER, amount_cents: 9000, description: 'OTHER', reference: '' },
        ], next_cursor: null, limit: 20 });
      }
      return jsonResponse(day({ close: closed }));
    };
    const el = await mountAsync<GableBranchDay>('gable-branch-day');
    q<HTMLButtonElement>(el, '[data-testid="deposit-find"]').click();
    await flush();
    await update(el, {});
    expect(el.querySelectorAll('[data-testid="deposit-candidates"] input[type="radio"]')).toHaveLength(2);
    q<HTMLButtonElement>(el, '[data-testid="deposit-match"]').click();
    await flush();
    await update(el, {});
    const post = requests.find((r) => r.method === 'POST');
    expect(post?.url).toContain('/api/v1/pos/end-of-day/c-1/deposit-match');
    expect(post?.body).toEqual({ bank_transaction_id: 'bt-1' });
    expect(text(q(el, '[data-testid="deposit-matched"]'))).toContain('$11.50');
    expect(text(q(el, '[data-testid="deposit-difference"]'))).toContain('Difference +$0.02');
  });

  it('does not show to a user the server refuses it to', async () => {
    handler = () => jsonResponse({ error: { code: 'forbidden', message: 'insufficient role' }, meta: { request_id: 'r' } }, 403);
    const el = await mountAsync<GableBranchDay>('gable-branch-day');
    expect(text(el)).toBe('');
  });

  it('reopens a closed day with the reason, and the card is open again', async () => {
    handler = (method) => (method === 'POST' ? jsonResponse(day()) : jsonResponse(day({ close: closed })));
    const el = await mountAsync<GableBranchDay>('gable-branch-day');
    // a blank reason is said, not sent
    q<HTMLButtonElement>(el, '[data-testid="day-reopen"]').click();
    await update(el, {});
    q<HTMLButtonElement>(el, '[data-testid="day-reopen-confirm"]').click();
    await flush();
    await update(el, {});
    expect(text(q(el, '[data-testid="branch-day-error"]'))).toContain('Say why');
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
    const reason = q<HTMLInputElement>(el, '[data-testid="day-reopen-reason"]');
    reason.value = 'a late drawer';
    reason.dispatchEvent(new Event('input', { bubbles: true }));
    await update(el, {});
    q<HTMLButtonElement>(el, '[data-testid="day-reopen-confirm"]').click();
    await flush();
    await update(el, {});
    const post = requests.find((r) => r.method === 'POST');
    expect(post?.url).toContain('/api/v1/pos/end-of-day/c-1/reopen');
    expect(post?.body).toEqual({ reason: 'a late drawer' });
    expect(el.querySelector('[data-testid="day-closed"]')).toBeNull();
    expect(el.querySelector('[data-testid="day-close"]')).not.toBeNull();
  });

  it('shows the server\'s refusal of a reopen', async () => {
    handler = (method) => (method === 'POST'
      ? jsonResponse({ error: { code: 'conflict', message: 'this day\'s deposit is already matched to a bank statement line: it cannot reopen' }, meta: { request_id: 'r' } }, 409)
      : jsonResponse(day({ close: closed })));
    const el = await mountAsync<GableBranchDay>('gable-branch-day');
    q<HTMLButtonElement>(el, '[data-testid="day-reopen"]').click();
    await update(el, {});
    const reason = q<HTMLInputElement>(el, '[data-testid="day-reopen-reason"]');
    reason.value = 'too late';
    reason.dispatchEvent(new Event('input', { bubbles: true }));
    await update(el, {});
    q<HTMLButtonElement>(el, '[data-testid="day-reopen-confirm"]').click();
    await flush();
    await update(el, {});
    expect(text(q(el, '[data-testid="branch-day-error"]'))).toContain('already matched');
    expect(el.querySelector('[data-testid="day-closed"]')).not.toBeNull();
  });

  it('offers no reopen once the deposit is matched', async () => {
    handler = () => jsonResponse(day({ close: { ...closed, deposit_match: {
      bank_transaction_id: 'bt-1', bank_amount_cents: 1150, bank_transaction_date: NEXT_DAY, bank_description: 'DEPOSIT',
      difference_cents: 0, matched_by: 'fin-1', matched_at: `${NEXT_DAY}T14:00:00Z` } } }));
    const el = await mountAsync<GableBranchDay>('gable-branch-day');
    expect(el.querySelector('[data-testid="deposit-matched"]')).not.toBeNull();
    expect(el.querySelector('[data-testid="day-reopen"]')).toBeNull();
  });

  it('says so when the yard has no drawer that day', async () => {
    handler = () => jsonResponse(day({ registers: [], totals: figures({ session_count: 0, sale_count: 0, sales_total_cents: 0, expected_by_method: {}, counted_by_method: {}, over_short_cents: 0, cash_to_deposit_cents: 0 }) }));
    const el = await mountAsync<GableBranchDay>('gable-branch-day');
    expect(text(q(el, '[data-testid="day-empty"]'))).toContain('No drawer was opened');
  });
});

describe('the sign off queue', () => {
  const pending = {
    id: 's-1', register_id: 'REG-01', branch_id: 'b-1', cashier_id: 'u-1', status: 'pending_signoff', opening_float_cents: 10000,
    opened_at: `${DAY}T14:00:00Z`, closed_at: `${DAY}T22:00:00Z`, expected_by_method: { CASH: 11000 }, counted_by_method: { CASH: 10000 },
    over_short_cents: -1000, gl_entry_id: null, notes: '', closed_by: 'cashier-1', variance_threshold_cents: 500, sign_off: null,
  };

  it('lists a counted drawer with its variance and signs it off with the reason', async () => {
    let queue = [pending];
    handler = (method) => (method === 'POST' ? ((queue = []), jsonResponse({ session: { ...pending, status: 'closed' } })) : jsonResponse({ items: queue, next_cursor: null, limit: 50 }));
    const el = await mountAsync<GableTillSignoffQueue>('gable-till-signoff-queue');
    expect(text(q(el, '[data-testid="signoff-variance"]'))).toBe('Short $10.00');
    expect(text(q(el, '[data-testid="signoff-s-1"]'))).toContain('closed by cashier-1');
    // a sign off without a reason says so and calls nothing
    q<HTMLButtonElement>(el, '[data-testid="signoff-button-s-1"]').click();
    await flush();
    await update(el, {});
    expect(text(q(el, '[data-testid="signoff-error-s-1"]'))).toContain('Say why');
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
    const reason = q<HTMLInputElement>(el, '[data-testid="signoff-reason-s-1"]');
    reason.value = 'recounted, the till roll agrees';
    reason.dispatchEvent(new Event('input', { bubbles: true }));
    await update(el, {});
    q<HTMLButtonElement>(el, '[data-testid="signoff-button-s-1"]').click();
    await flush();
    await update(el, {});
    // the step up dialog opens: the manager's approval completes the sign off
    const stepUp = el.querySelector('gable-step-up-dialog');
    expect(stepUp).not.toBeNull();
    stepUp!.dispatchEvent(new CustomEvent('step-up-approved', { detail: { token: 'tok-1', managerName: 'E2E Manager' }, bubbles: true }));
    await flush();
    await update(el, {});
    const post = requests.find((r) => r.method === 'POST');
    expect(post?.url).toContain('/api/v1/pos/till/s-1/signoff');
    expect(post?.body).toEqual({ reason: 'recounted, the till roll agrees', approval_token: 'tok-1' });
    expect(el.querySelector('[data-testid="signoff-s-1"]')).toBeNull();
    expect(text(q(el, '[data-testid="signoff-empty"]'))).toContain('No counted drawer waits');
  });

  it('shows the server\'s refusal when the signer is the closer', async () => {
    handler = (method) => (method === 'POST'
      ? jsonResponse({ error: { code: 'conflict', message: 'the person who closed the drawer cannot sign off its variance' }, meta: { request_id: 'r' } }, 409)
      : jsonResponse({ items: [pending], next_cursor: null, limit: 50 }));
    const el = await mountAsync<GableTillSignoffQueue>('gable-till-signoff-queue');
    const reason = q<HTMLInputElement>(el, '[data-testid="signoff-reason-s-1"]');
    reason.value = 'mine';
    reason.dispatchEvent(new Event('input', { bubbles: true }));
    await update(el, {});
    q<HTMLButtonElement>(el, '[data-testid="signoff-button-s-1"]').click();
    await flush();
    await update(el, {});
    el.querySelector('gable-step-up-dialog')!.dispatchEvent(
      new CustomEvent('step-up-approved', { detail: { token: 'tok-1', managerName: 'E2E Manager' }, bubbles: true }));
    await flush();
    await update(el, {});
    expect(text(q(el, '[data-testid="signoff-error-s-1"]'))).toContain('cannot sign off');
    expect(el.querySelector('[data-testid="signoff-s-1"]')).not.toBeNull();
  });

  it('does not show to a user the server refuses it to', async () => {
    handler = () => jsonResponse({ error: { code: 'forbidden', message: 'insufficient role' }, meta: { request_id: 'r' } }, 403);
    const el = await mountAsync<GableTillSignoffQueue>('gable-till-signoff-queue');
    expect(text(el)).toBe('');
  });
});
