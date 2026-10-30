// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { authCustody } from '@gable/auth';
import './OfflineCounter';
import { draftOf, policyOf } from './OfflineCounter';
import type { GableOfflineCounter } from './OfflineCounter';
import { branchContext } from '../../services/BranchContext';
import { flush, jsonResponse, mountAsync, q, text, update } from '../../test/dom';
import type { OfflinePolicy } from '../../services/OfflineAdminService';

const policy = (patch: Partial<OfflinePolicy> = {}): OfflinePolicy => ({
  branch_id: 'b-1', enabled: true, tenders: ['cash', 'check'], sale_cap_cents: 50000, check_cap_cents: 20000,
  account_cap_cents: 100000, queue_total_cap_cents: 500000, max_queued: 40, enrolled_register_count: 3,
  max_account_exposure_cents: 300000, source: 'branch', updated_by: 'mgr', updated_at: '2030-01-01T00:00:00Z', revision: 2, ...patch,
});

let requests: { method: string; url: string; body?: Record<string, unknown> }[] = [];
let current: OfflinePolicy;

let roles: { mockReturnValue(v: string[]): unknown; mockRestore(): void };

beforeEach(() => {
  roles = vi.spyOn(authCustody, 'roles', 'get').mockReturnValue(['admin']);
  requests = [];
  current = policy();
  localStorage.setItem('token', 'test-token');
  branchContext.setCurrent('b-1');
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET';
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    requests.push({ method, url, body });
    if (url.includes('/pos/offline/devices')) return jsonResponse({ items: [] });
    if (method === 'PUT' && body?.clear) {
      current = policy({ source: 'default', enabled: false, tenders: [] });
    } else if (method === 'PUT') {
      current = policy({ enabled: body.enabled, tenders: body.tenders, account_cap_cents: body.account_cap_cents, sale_cap_cents: body.sale_cap_cents });
    }
    return jsonResponse(current);
  }));
});

afterEach(() => {
  roles.mockRestore();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
  branchContext.setCurrent(null);
});

const type = async (el: GableOfflineCounter, testid: string, value: string) => {
  const input = q<HTMLInputElement>(el, `[data-testid="${testid}"]`);
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  await update(el, {});
};

describe('the counter\'s offline settings', () => {
  it('reads the branch policy and shows the worst case on one account for its enrolled registers', async () => {
    const el = await mountAsync<GableOfflineCounter>('gable-offline-counter');
    expect(requests[0].url).toContain('/api/v1/pos/offline/policy?branch_id=b-1');
    expect(q<HTMLInputElement>(el, '[data-testid="offline-cap-account"]').value).toBe('1000.00');
    expect(text(q(el, '[data-testid="offline-exposure"]'))).toContain('$3,000.00');
    expect(text(q(el, '[data-testid="offline-exposure"]'))).toContain('3 enrolled registers');
    expect(q<HTMLInputElement>(el, '[data-testid="offline-tender-account"]').checked).toBe(false);
  });

  it('moves the worst case figure as the account cap is typed', async () => {
    const el = await mountAsync<GableOfflineCounter>('gable-offline-counter');
    await type(el, 'offline-cap-account', '250.00');
    expect(text(q(el, '[data-testid="offline-exposure"]'))).toContain('$750.00');
  });

  it('saves the draft in cents with the tenders chosen, and says it saved', async () => {
    const el = await mountAsync<GableOfflineCounter>('gable-offline-counter');
    const account = q<HTMLInputElement>(el, '[data-testid="offline-tender-account"]');
    account.checked = true;
    account.dispatchEvent(new Event('change', { bubbles: true }));
    await update(el, {});
    await type(el, 'offline-cap-sale', '750.50');
    q<HTMLButtonElement>(el, '[data-testid="offline-save"]').click();
    await flush();
    await update(el, {});
    const put = requests.find((r) => r.method === 'PUT');
    expect(put?.url).toContain('/api/v1/pos/offline/policy');
    expect(put?.body).toEqual({
      branch_id: 'b-1', enabled: true, tenders: ['cash', 'check', 'account'], sale_cap_cents: 75050,
      check_cap_cents: 20000, account_cap_cents: 100000, queue_total_cap_cents: 500000, max_queued: 40,
    });
    expect(text(q(el, '[data-testid="offline-counter-saved"]'))).toContain('Offline settings saved');
  });

  it('refuses a bad figure in words before any call is made', async () => {
    const el = await mountAsync<GableOfflineCounter>('gable-offline-counter');
    await type(el, 'offline-cap-check', 'abc');
    q<HTMLButtonElement>(el, '[data-testid="offline-save"]').click();
    await update(el, {});
    expect(text(q(el, '[data-testid="offline-counter-error"]'))).toContain('The cap on a check must be a dollar amount');
    expect(requests.some((r) => r.method === 'PUT')).toBe(false);
  });

  it('goes back to the dealer wide settings with clear true', async () => {
    const el = await mountAsync<GableOfflineCounter>('gable-offline-counter');
    q<HTMLButtonElement>(el, '[data-testid="offline-use-defaults"]').click();
    await flush();
    await update(el, {});
    expect(requests.find((r) => r.method === 'PUT')?.body).toEqual({ branch_id: 'b-1', clear: true });
    expect(text(q(el, '[data-testid="offline-counter-source"]'))).toContain('follows the dealer wide');
    expect(el.querySelector('[data-testid="offline-use-defaults"]')).toBeNull();
  });
});

describe('a finance caller', () => {
  it('reads the figures but is offered no Save, no dealer wide button and no editable field', async () => {
    roles.mockReturnValue(['finance']);
    const el = await mountAsync<GableOfflineCounter>('gable-offline-counter');
    expect(text(q(el, '[data-testid="offline-exposure"]'))).toContain('$3,000.00');
    expect(el.querySelector('[data-testid="offline-save"]')).toBeNull();
    expect(el.querySelector('[data-testid="offline-use-defaults"]')).toBeNull();
    expect(q<HTMLFieldSetElement>(el, '[data-testid="offline-caps"]').disabled).toBe(true);
    expect(q<HTMLInputElement>(el, '[data-testid="offline-enabled"]').disabled).toBe(true);
    expect(text(q(el, '[data-testid="offline-counter-readonly"]'))).toContain('owner or admin');
  });
});

describe('a token that names no role', () => {
  it('leaves the writes to the server, as local development signs in', async () => {
    roles.mockReturnValue([]);
    const el = await mountAsync<GableOfflineCounter>('gable-offline-counter');
    expect(el.querySelector('[data-testid="offline-save"]')).not.toBeNull();
  });
});

describe('the draft and its policy', () => {
  it('round trips a policy to the same cents', () => {
    const p = policy({ sale_cap_cents: 5 });
    const out = policyOf(draftOf(p));
    expect(out.ok && out.input.sale_cap_cents).toBe(5);
  });

  it('refuses an enabled policy with no tender and a queue count past the limit', () => {
    expect(policyOf({ ...draftOf(policy()), tenders: [] }).ok).toBe(false);
    expect(policyOf({ ...draftOf(policy()), maxQueued: '100001' }).ok).toBe(false);
    expect(policyOf({ ...draftOf(policy()), maxQueued: '100000' }).ok).toBe(true);
    expect(policyOf({ ...draftOf(policy()), enabled: false, tenders: [] }).ok).toBe(true);
  });
});
