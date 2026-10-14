// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import { payPlan, panelProgress } from './panels';

describe('payPlan', () => {
  it('sends the tenders already added, unchanged, when they cover the total', () => {
    const plan = payPlan(10000, [{ method: 'cash', amount_cents: 10000 }], null);
    expect(plan).toEqual({ ok: true, tenders: [{ method: 'cash', amount_cents: 10000 }], changeCents: 0 });
  });

  it('takes the entry as the last tender when it covers what is left', () => {
    const plan = payPlan(10000, [{ method: 'card', amount_cents: 4000 }], { method: 'cash', amount_cents: 6000 });
    expect(plan).toEqual({
      ok: true,
      tenders: [{ method: 'card', amount_cents: 4000 }, { method: 'cash', amount_cents: 6000 }],
      changeCents: 0,
    });
  });

  it('reports change when cash tendered is over what is owed', () => {
    const plan = payPlan(8750, [], { method: 'cash', amount_cents: 10000 });
    expect(plan).toEqual({ ok: true, tenders: [{ method: 'cash', amount_cents: 10000 }], changeCents: 1250 });
  });

  it('refuses while something is still owed, saying how much', () => {
    const plan = payPlan(10000, [], { method: 'card', amount_cents: 2500 });
    expect(plan).toEqual({ ok: false, owedCents: 7500 });
  });

  it('refuses an empty sale and a non positive entry', () => {
    expect(payPlan(0, [], { method: 'cash', amount_cents: 0 }).ok).toBe(false);
    expect(payPlan(500, [], { method: 'cash', amount_cents: -100 })).toEqual({ ok: false, owedCents: 500 });
  });
});

describe('panelProgress', () => {
  it('walks Customer, Payment, Summary one step at a time', () => {
    expect(panelProgress('customer', false)).toEqual({ customer: 'current', payment: 'upcoming', summary: 'upcoming' });
    expect(panelProgress('payment', false)).toEqual({ customer: 'done', payment: 'current', summary: 'upcoming' });
    expect(panelProgress('summary', false)).toEqual({ customer: 'done', payment: 'done', summary: 'current' });
  });

  it('lights Summary and nothing else on a completed sale', () => {
    expect(panelProgress('customer', true)).toEqual({ customer: 'done', payment: 'done', summary: 'current' });
  });
});
