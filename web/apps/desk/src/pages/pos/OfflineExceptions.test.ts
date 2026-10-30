// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import './OfflineExceptions';
import type { GableOfflineExceptions } from './OfflineExceptions';
import { branchContext } from '../../services/BranchContext';
import { flush, jsonResponse, mountAsync, q, text, update } from '../../test/dom';
import type { OfflineException } from '../../services/OfflineAdminService';

const item = (patch: Partial<OfflineException> = {}): OfflineException => ({
  id: 'x-1', sale_id: 's-1', register_id: 'REG-01', branch_id: 'b-1', device_epoch: 2, device_seq: 7,
  reason_code: 'insufficient_stock', reason_message: 'Stock is short', status: 'open', till_session_id: 'ts-1',
  business_date: '2030-01-02', total_cents: 4250, offline_ref: 'REG-01-2-7', actions: ['retry', 'write_off'],
  revision: 3, created_at: '2030-01-02T10:00:00Z', ...patch,
});

let requests: { method: string; url: string; body?: Record<string, unknown> }[] = [];
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
    handler = () => jsonResponse({ items: [item(), item({ id: 'x-2', reason_code: 'bad_signature', offline_ref: 'REG-01-2-8', total_cents: 100 })] });
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

  it('shows nothing to a user the server refuses', async () => {
    handler = () => jsonResponse({ error: { code: 'forbidden', message: 'no' } }, 403);
    const el = await mountAsync<GableOfflineExceptions>('gable-offline-exceptions');
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
    expect(post?.body).toEqual({ action: 'write_off', reason: 'Counted short, goods gone', revision: 3 });
    expect(el.querySelector('[data-testid="offline-exception-x-1"]')).toBeNull();
  });

  it('offers exactly the actions the server lists for the row', async () => {
    handler = () => jsonResponse({ items: [item({ reason_code: 'stock_unit_changed', actions: ['restate', 'write_off'] })] });
    const el = await mountAsync<GableOfflineExceptions>('gable-offline-exceptions');
    q<HTMLButtonElement>(el, '[data-testid="offline-settle"]').click();
    await update(el, {});
    expect(Array.from(el.querySelectorAll('input[data-testid^="offline-way-"]')).map((n) => n.getAttribute('data-testid')))
      .toEqual(['offline-way-restate', 'offline-way-write_off']);
  });

  it('follows the cursor until every page is read', async () => {
    handler = (_m, url) => (url.includes('cursor=c2')
      ? jsonResponse({ items: [item({ id: 'x-2' })], next_cursor: null, limit: 1 })
      : jsonResponse({ items: [item()], next_cursor: 'c2', limit: 1 }));
    const el = await mountAsync<GableOfflineExceptions>('gable-offline-exceptions');
    expect(text(q(el, '[data-testid="offline-exceptions-count"]'))).toBe('2');
  });

  it('retries with no reason, and keeps the item with the server\'s words when the manager is refused', async () => {
    handler = (method) => (method === 'POST' ? jsonResponse({ error: { code: 'forbidden', message: 'A manager other than the cashier settles it' } }, 403) : jsonResponse({ items: [item()] }));
    const el = await mountAsync<GableOfflineExceptions>('gable-offline-exceptions');
    q<HTMLButtonElement>(el, '[data-testid="offline-settle"]').click();
    await update(el, {});
    q<HTMLButtonElement>(el, '[data-testid="offline-resolve"]').click(); // retry is first and needs no reason
    await flush();
    await update(el, {});
    expect(requests.find((r) => r.method === 'POST')?.body).toEqual({ action: 'retry', reason: '', revision: 3 });
    expect(text(q(el, '[data-testid="offline-exceptions-error"]'))).toContain('A manager other than the cashier');
    expect(el.querySelector('[data-testid="offline-exception-x-1"]')).not.toBeNull();
  });
});
