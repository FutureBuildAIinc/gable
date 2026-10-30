// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import './DailyTill';
import './pos/BranchDay';
import './pos/TillSignoffQueue';
import type { GableDailyTill } from './DailyTill';
import { jsonResponse, mountAsync, q, text } from '../test/dom';
import type { DailyTillReport, SalesSummaryReport } from '../types/reporting';

// Every figure on the page carries digit grouping: the same dollars helper
// renders each, so a five figure amount never sits beside a grouped one.
const till = (patch: Partial<DailyTillReport> = {}): DailyTillReport => ({
  date: '2026-10-30', total_collected: 4126.39, received: 82854.61, refunds: 0,
  net_collected: 4126.39, by_method: { CASH: 4126.39, CARD: 78728.22 },
  transaction_count: 41, ...patch,
});

const summary = (patch: Partial<SalesSummaryReport> = {}): SalesSummaryReport => ({
  start_date: '2026-10-01', end_date: '2026-10-30', total_invoiced: 78728.22,
  total_collected: 4126.39, outstanding_ar: 78728.22, invoice_count: 12, ...patch,
});

beforeEach(() => {
  localStorage.setItem('token', 'test-token');
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.includes('/api/v1/reports/daily-till')) return jsonResponse(till());
    if (url.includes('/api/v1/reports/sales-summary')) return jsonResponse(summary());
    // the queue and the branch day render nothing when the server refuses them
    return jsonResponse({ error: { code: 'forbidden', message: 'insufficient role' }, meta: { request_id: 'r' } }, 403);
  }));
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the daily till and its financial dashboard', () => {
  it('prints every figure grouped, to two places', async () => {
    const el = await mountAsync<GableDailyTill>('gable-daily-till');
    expect(text(q(el, '[data-testid="till-collected"]'))).toBe('$4,126.39');
    const methods = [...el.querySelectorAll('[data-testid="till-method"]')].map((n) => text(n));
    expect(methods).toEqual(['$4,126.39', '$78,728.22']);
    expect(text(q(el, '[data-testid="summary-invoiced"]'))).toBe('$78,728.22');
    expect(text(q(el, '[data-testid="summary-collected"]'))).toBe('$4,126.39');
    expect(text(q(el, '[data-testid="summary-ar"]'))).toBe('$78,728.22');
    expect(text(el)).not.toContain('$4126.39');
    expect(text(el)).not.toContain('$78728.22');
  });

  it('says the parts of a day with refunds in the same grouped figures', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.includes('/api/v1/reports/daily-till')) return jsonResponse(till({ received: 83854.61, refunds: 39728.22, net_collected: 44126.39 }));
      if (url.includes('/api/v1/reports/sales-summary')) return jsonResponse(summary());
      return jsonResponse({ error: { code: 'forbidden', message: 'insufficient role' }, meta: { request_id: 'r' } }, 403);
    }));
    const el = await mountAsync<GableDailyTill>('gable-daily-till');
    expect(text(q(el, '[data-testid="till-parts"]'))).toBe(
      'Received $83,854.61 less refunds $39,728.22 is net $44,126.39');
  });
});
