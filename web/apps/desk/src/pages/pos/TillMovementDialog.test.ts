// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import './TillMovementDialog';
import type { TillMovementDialog } from './TillMovementDialog';
import type { GableSelect } from '../../components/ui/gable-select';
import { chooseOption, flush, jsonResponse, mount, q, text, update } from '../../test/dom';

const movement = {
  id: 'm-1', till_session_id: 's-1', register_id: 'REG-01', branch_id: 'b-1', kind: 'safe_drop', amount_cents: 2050,
  reason_code: null, note: '', actor: 'u-1', taken_by: null, gl_entry_id: 'g-1', created_at: '2026-01-01T00:00:00Z',
};

let calls: { url: string; body: Record<string, unknown> }[] = [];

beforeEach(() => {
  calls = [];
  localStorage.setItem('token', 'test-token');
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : {} });
    return jsonResponse(movement, 201);
  }));
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function type(el: TillMovementDialog, testid: string, value: string) {
  const input = q<HTMLInputElement>(el, `[data-testid="${testid}"]`);
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  await update(el, {});
}

describe('the drawer cash dialog', () => {
  it('offers the four kinds and starts on a safe drop', async () => {
    const el = await mount<TillMovementDialog>('gable-till-movement-dialog', { sessionId: 's-1' });
    for (const kind of ['safe_drop', 'pickup', 'paid_in', 'paid_out']) {
      expect(el.querySelector(`[data-testid="movement-kind-${kind}"]`), kind).not.toBeNull();
    }
    expect(q(el, '[data-testid="movement-kind-safe_drop"]').getAttribute('aria-pressed')).toBe('true');
    expect(el.querySelector('[data-testid="movement-reason"]')).toBeNull();
    expect(el.querySelector('[data-testid="movement-taken_by"]')).toBeNull();
  });

  it('shows the reason list for a paid out and the second person for a pickup', async () => {
    const el = await mount<TillMovementDialog>('gable-till-movement-dialog', { sessionId: 's-1' });
    q<HTMLButtonElement>(el, '[data-testid="movement-kind-paid_out"]').click();
    await update(el, {});
    const reasons = q<GableSelect>(el, '[data-testid="movement-reason"]');
    expect(reasons.options.map((o) => o.value)).toEqual(['supplies', 'freight', 'fuel', 'postage', 'meals', 'repairs', 'other']);
    q<HTMLButtonElement>(el, '[data-testid="movement-kind-pickup"]').click();
    await update(el, {});
    expect(el.querySelector('[data-testid="movement-taken_by"]')).not.toBeNull();
    expect(el.querySelector('[data-testid="movement-reason"]')).toBeNull();
  });

  it('sends the reason chosen in the list with a paid out', async () => {
    const el = await mount<TillMovementDialog>('gable-till-movement-dialog', { sessionId: 's-1' });
    q<HTMLButtonElement>(el, '[data-testid="movement-kind-paid_out"]').click();
    await update(el, {});
    await type(el, 'movement-amount', '3');
    await chooseOption(q<GableSelect>(el, '[data-testid="movement-reason"]'), 'postage');
    await update(el, {});
    q<HTMLButtonElement>(el, '[data-testid="movement-record"]').click();
    await flush();
    expect(calls[0].body).toEqual({ kind: 'paid_out', amount_cents: 300, reason_code: 'postage' });
  });

  it('says what is missing and sends nothing', async () => {
    const el = await mount<TillMovementDialog>('gable-till-movement-dialog', { sessionId: 's-1' });
    q<HTMLButtonElement>(el, '[data-testid="movement-record"]').click();
    await flush();
    await update(el, {});
    expect(text(q(el, '[data-testid="movement-amount-error"]'))).toContain('Enter the amount');
    expect(calls).toHaveLength(0);
  });

  it('records a drop against the session and tells the terminal', async () => {
    const el = await mount<TillMovementDialog>('gable-till-movement-dialog', { sessionId: 's-1' });
    const recorded = vi.fn();
    el.addEventListener('movement-recorded', (e) => recorded((e as CustomEvent).detail));
    await type(el, 'movement-amount', '20.50');
    expect(text(q(el, '[data-testid="movement-record"]'))).toBe('Record safe drop $20.50');
    q<HTMLButtonElement>(el, '[data-testid="movement-record"]').click();
    await flush();
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toContain('/api/v1/pos/till/s-1/movements');
    expect(calls[0].body).toEqual({ kind: 'safe_drop', amount_cents: 2050 });
    expect(recorded).toHaveBeenCalledWith(movement);
  });

  it('shows the server\'s refusal in the dialog and keeps the form', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({
      error: { code: 'conflict', message: 'a pickup is cash taken by a second person: the cashier cannot take it alone', details: [{ code: 'pickup_needs_second_person', message: 'x' }] },
      meta: { request_id: 'r' },
    }, 409)));
    const el = await mount<TillMovementDialog>('gable-till-movement-dialog', { sessionId: 's-1' });
    q<HTMLButtonElement>(el, '[data-testid="movement-kind-pickup"]').click();
    await update(el, {});
    await type(el, 'movement-amount', '40');
    await type(el, 'movement-taken_by', 'me');
    q<HTMLButtonElement>(el, '[data-testid="movement-record"]').click();
    await flush();
    await update(el, {});
    expect(text(q(el, '[data-testid="movement-failure"]'))).toContain('second person');
    expect((q(el, '[data-testid="movement-amount"]') as HTMLInputElement).value).toBe('40');
  });
});
