// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import { MOVEMENT_REASONS, planMovement, type MovementForm } from './movements';

const form = (patch: Partial<MovementForm>): MovementForm => ({
  kind: 'safe_drop', amount: '', reason: '', note: '', takenBy: '', ...patch,
});

describe('planMovement', () => {
  it('sends a drop as integer cents with no reason and no taker', () => {
    expect(planMovement(form({ amount: '20.50' }))).toEqual({ ok: true, request: { kind: 'safe_drop', amount_cents: 2050 } });
  });

  it('refuses an amount that is missing, zero, negative or not money', () => {
    for (const amount of ['', '0', '-5', 'abc', '1.234']) {
      const plan = planMovement(form({ amount }));
      expect(plan.ok, amount).toBe(false);
      if (!plan.ok) expect(plan.field).toBe('amount');
    }
  });

  it('asks a paid kind for a reason from its own list', () => {
    expect(planMovement(form({ kind: 'paid_out', amount: '3' }))).toMatchObject({ ok: false, field: 'reason' });
    // an income reason is not a reason to pay out, and the reverse
    expect(planMovement(form({ kind: 'paid_out', amount: '3', reason: 'fee_received' }))).toMatchObject({ ok: false, field: 'reason' });
    expect(planMovement(form({ kind: 'paid_in', amount: '3', reason: 'supplies' }))).toMatchObject({ ok: false, field: 'reason' });
    expect(planMovement(form({ kind: 'paid_out', amount: '3', reason: 'postage' }))).toEqual({
      ok: true, request: { kind: 'paid_out', amount_cents: 300, reason_code: 'postage' },
    });
  });

  it('asks for a note when the reason is other', () => {
    expect(planMovement(form({ kind: 'paid_in', amount: '3', reason: 'other', note: '  ' }))).toMatchObject({ ok: false, field: 'note' });
    expect(planMovement(form({ kind: 'paid_in', amount: '3', reason: 'other', note: ' found cash ' }))).toEqual({
      ok: true, request: { kind: 'paid_in', amount_cents: 300, reason_code: 'other', note: 'found cash' },
    });
  });

  it('asks a pickup for the second person', () => {
    expect(planMovement(form({ kind: 'pickup', amount: '40' }))).toMatchObject({ ok: false, field: 'taken_by' });
    expect(planMovement(form({ kind: 'pickup', amount: '40', takenBy: ' mgr-7 ' }))).toEqual({
      ok: true, request: { kind: 'pickup', amount_cents: 4000, taken_by: 'mgr-7' },
    });
  });

  it('lists the closed reason set the server holds', () => {
    expect(MOVEMENT_REASONS.paid_out.map((r) => r.value)).toEqual(['supplies', 'freight', 'fuel', 'postage', 'meals', 'repairs', 'other']);
    expect(MOVEMENT_REASONS.paid_in.map((r) => r.value)).toEqual(['fee_received', 'vendor_refund', 'other']);
  });
});
