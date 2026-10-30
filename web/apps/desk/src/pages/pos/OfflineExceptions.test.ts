// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import './OfflineExceptions';
import type { GableOfflineExceptions } from './OfflineExceptions';
import { branchContext } from '../../services/BranchContext';
import { flush, jsonResponse, mountAsync, q, text, update } from '../../test/dom';
import type { OfflineException } from '../../services/OfflineAdminService';

const item = (patch: Partial<OfflineException> = {}): OfflineException => ({
  id: 'x-1', sale_id: 's-1', register_id: 'REG-01', epoch: 2, sequence: 7, reason_code: 'insufficient_stock',
  message: 'Stock is short', status: 'open', session_id: 'ts-1', business_date: '2030-01-02', total_cents: 4250,
  tendered_by_method: { CASH: 4250 }, offline_reference: 'OFF-0007', created_at: '2030-01-02T10:00:00Z', ...patch,
});

let requests: { method: string; url: string; body?: Record<string, unknown> }[] = [];
// SEAM O4: the exception routes are faked here until refactor/cp-offline-o4 lands
let handler: (method: string, url: string) => Response;

beforeEach(() => {
  requests = [];
  localStorage.setItem('token', 'test-token');
  branchContext.setCurrent('b-1');
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET';
    requests.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    return handler(method, url);
  }));
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
  branchContext.setCurrent(null);
});

const type = async (el: HTMLElement, selector: string, value: string) => {
  const input = q<HTMLInputElement>(el, selector);
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  await update(el as GableOfflineExceptions, {});
};

describe('the offline exceptions on the branch day page', () => {
  it('lists the branch\'s open exceptions with the count, reason and total', async () => {
    handler = () => jsonResponse({ items: [item(), item({ id: 'x-2', reason_code: 'bad_signature', offline_reference: 'OFF-0008', total_cents: 100 })] });
    const el = await mountAsync<GableOfflineExceptions>('gable-offline-exceptions');
    expect(requests[0].url).toContain('/api/v1/pos/offline/exceptions?branch_id=b-1&status=open');
    expect(text(q(el, '[data-testid="offline-exceptions-count"]'))).toBe('2');
    expect(text(q(el, '[data-testid="offline-exception-x-1"]'))).toContain('Not enough stock on hand');
    expect(text(q(el, '[data-testid="offline-exception-x-1"]'))).toContain('$42.50');
  });

  it('says plainly that nothing waits when the list is empty', async () => {
    handler = () => jsonResponse({ items: [] });
    const el = await mountAsync<GableOfflineExceptions>('gable-offline-exceptions');
    expect(text(q(el, '[data-testid="offline-exceptions-none"]'))).toContain('No offline sale is waiting');
  });

  it('shows nothing to a user the server refuses, and nothing while the route is not built', async () => {
    handler = () => jsonResponse({ error: { code: 'forbidden', message: 'no' } }, 403);
    let el = await mountAsync<GableOfflineExceptions>('gable-offline-exceptions');
    expect(el.querySelector('[data-testid="offline-exceptions"]')).toBeNull();
    document.body.innerHTML = '';
    handler = () => jsonResponse({ error: { code: 'not_found', message: 'no' } }, 404);
    el = await mountAsync<GableOfflineExceptions>('gable-offline-exceptions');
    expect(el.querySelector('[data-testid="offline-exceptions"]')).toBeNull();
  });

  it('settles a stock shortage by write off only with a reason, then drops it from the list', async () => {
    handler = (method) => (method === 'POST' ? jsonResponse(item({ status: 'resolved' })) : jsonResponse({ items: [item()] }));
    const el = await mountAsync<GableOfflineExceptions>('gable-offline-exceptions');
    q<HTMLButtonElement>(el, '[data-testid="offline-settle"]').click();
    await update(el, {});
    expect(Array.from(el.querySelectorAll('input[data-testid^="offline-way-"]')).map((n) => n.getAttribute('data-testid')))
      .toEqual(['offline-way-retry', 'offline-way-write_off']);
    q<HTMLInputElement>(el, '[data-testid="offline-way-write_off"]').click();
    q<HTMLInputElement>(el, '[data-testid="offline-way-write_off"]').dispatchEvent(new Event('change', { bubbles: true }));
    await update(el, {});
    q<HTMLButtonElement>(el, '[data-testid="offline-resolve"]').click();
    await flush();
    await update(el, {});
    expect(requests.some((r) => r.method === 'POST')).toBe(false); // no reason, no call
    expect(text(q(el, '[data-testid="offline-exceptions-error"]'))).toContain('Say why');
    await type(el, '[data-testid="offline-reason"]', 'Counted short, goods gone');
    q<HTMLButtonElement>(el, '[data-testid="offline-resolve"]').click();
    await flush();
    await update(el, {});
    const post = requests.find((r) => r.method === 'POST');
    expect(post?.url).toContain('/api/v1/pos/offline/exceptions/x-1/resolve');
    expect(post?.body).toEqual({ resolution: 'write_off', reason: 'Counted short, goods gone' });
    expect(el.querySelector('[data-testid="offline-exception-x-1"]')).toBeNull();
  });

  it('retries with no reason, and keeps the item with the server\'s words when the manager is refused', async () => {
    handler = (method) => (method === 'POST' ? jsonResponse({ error: { code: 'forbidden', message: 'A manager other than the cashier settles it' } }, 403) : jsonResponse({ items: [item()] }));
    const el = await mountAsync<GableOfflineExceptions>('gable-offline-exceptions');
    q<HTMLButtonElement>(el, '[data-testid="offline-settle"]').click();
    await update(el, {});
    q<HTMLButtonElement>(el, '[data-testid="offline-resolve"]').click(); // retry is first and needs no reason
    await flush();
    await update(el, {});
    expect(requests.find((r) => r.method === 'POST')?.body).toEqual({ resolution: 'retry', reason: '' });
    expect(text(q(el, '[data-testid="offline-exceptions-error"]'))).toContain('A manager other than the cashier');
    expect(el.querySelector('[data-testid="offline-exception-x-1"]')).not.toBeNull();
  });
});
