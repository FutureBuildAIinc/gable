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
    expect(panelProgress('customer', false, false)).toEqual({ customer: 'current', payment: 'upcoming', summary: 'upcoming' });
    expect(panelProgress('payment', false, false)).toEqual({ customer: 'done', payment: 'current', summary: 'upcoming' });
    expect(panelProgress('summary', false, true)).toEqual({ customer: 'done', payment: 'done', summary: 'current' });
  });

  it('ticks Payment on the Summary step only once a tender is chosen', () => {
    // A cashier can reach Summary with no tender chosen ("Tender: None
    // yet"); Payment is not done until a tender is chosen.
    expect(panelProgress('summary', false, false)).toEqual({ customer: 'done', payment: 'upcoming', summary: 'current' });
    expect(panelProgress('summary', false, true).payment).toBe('done');
  });

  it('marks all three steps done on a completed sale', () => {
    expect(panelProgress('customer', true, false)).toEqual({ customer: 'done', payment: 'done', summary: 'done' });
  });
});

describe('payPlan, the one rule for splits', () => {
  // card, check and account together never exceed the total; the change
  // never exceeds the cash tendered; everything else pays
  const t = (method: 'cash' | 'card' | 'check' | 'account', amount_cents: number) => ({ method, amount_cents });
  const TOTAL = 8650;

  it('pays a card added then cash added above what is owed, with the change from the cash', () => {
    const plan = payPlan(TOTAL, [t('card', 4000), t('cash', 5000)], null);
    expect(plan.ok).toBe(true);
    if (plan.ok) expect(plan.changeCents).toBe(350);
  });

  const pays: [string, ReturnType<typeof t>[], ReturnType<typeof t> | null, number][] = [
    ['two cards that fill the total', [t('card', 4000)], t('card', 4650), 0],
    ['a card added and a check typed that fill it', [t('card', 4000)], t('check', 4650), 0],
    ['cash, card added and cash typed above', [t('cash', 2000), t('card', 3000)], t('cash', 5000), 1350],
    ['an account portion and cash above', [t('account', 4000)], t('cash', 6000), 1350],
    ['an account charge for the whole total', [], t('account', 8650), 0],
    ['cash above the total', [], t('cash', 10000), 1350],
    ['the whole total added as a card, a card typed beside it ignored', [t('card', 8650)], t('card', 1000), 0],
  ];
  for (const [name, added, entry, change] of pays) {
    it(`pays ${name}`, () => {
      const plan = payPlan(TOTAL, added, entry);
      expect(plan.ok).toBe(true);
      if (plan.ok) expect(plan.changeCents).toBe(change);
    });
  }

  const refuses: [string, ReturnType<typeof t>[], ReturnType<typeof t> | null, string][] = [
    ['two cards above the total', [t('card', 4000)], t('card', 5000), 'typed'],
    ['a card and a check above it', [t('card', 4000)], t('check', 5000), 'typed'],
    ['a card typed above the total', [], t('card', 10000), 'typed'],
    ['an account amount typed above it', [t('cash', 2000)], t('account', 9000), 'typed'],
    ['added non cash tenders above a reduced total', [t('card', 6000)], null, 'added'],
  ];
  for (const [name, added, entry, kind] of refuses) {
    it(`refuses ${name}`, () => {
      const plan = payPlan(name.includes('reduced') ? 5000 : TOTAL, added, entry);
      expect(plan.ok).toBe(false);
      if (!plan.ok) expect(plan.over).toBe(kind);
    });
  }

  it('still pays cash added above a reduced total, and non cash that fits it', () => {
    expect(payPlan(5000, [t('cash', 8650)], null).ok).toBe(true);
    const plan = payPlan(5000, [t('card', 4000), t('cash', 4650)], null);
    expect(plan.ok).toBe(true);
    if (plan.ok) expect(plan.changeCents).toBe(3650);
  });

  it('a typed refusal carries the real limit: the total less the other non cash tenders', () => {
    const plan = payPlan(8650, [t('cash', 6000)], t('card', 9000));
    expect(plan.ok).toBe(false);
    if (!plan.ok) {
      expect(plan.over).toBe('typed');
      expect(plan.limitCents).toBe(8650);
    }
    const split = payPlan(8650, [t('card', 4000)], t('check', 5000));
    expect(split.ok).toBe(false);
    if (!split.ok) expect(split.limitCents).toBe(4650);
  });
});
