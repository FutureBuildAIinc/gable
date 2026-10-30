// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's offline mode (ADR 0011 slice C2) against a stand in for the
// work shell's core: the page holds the cart and shows the core's figures,
// card and store credit wait for the connection, a kit is refused, and the
// sale the core signs names the replaced cart or the declaration.

import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { setOfflineInvoke } from '../../services/OfflineCounterService';
import { CORE_TOTAL, fakeOfflineCore, type FakeCore } from '../../test/fakeOfflineCore';
import type { OfflineSaleRequest } from '../../types/offline';
import './OfflineCounter';
import type { OfflineCounter } from './OfflineCounter';
import { NEEDS_CONNECTION, OFFLINE_PRICES } from './offline';

const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement | null;
const settle = async (el: OfflineCounter) => {
  for (let i = 0; i < 4; i++) {
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
  }
};

let core: FakeCore;

async function mount(props: Partial<Pick<OfflineCounter, 'seed' | 'unknownCartId'>> = {}): Promise<OfflineCounter> {
  const el = document.createElement('gable-pos-offline') as OfflineCounter;
  Object.assign(el, props);
  document.body.appendChild(el);
  await settle(el);
  return el;
}

async function addProduct(el: OfflineCounter, text: string) {
  const search = q(el, 'pos-offline-search') as HTMLInputElement;
  search.value = text;
  search.dispatchEvent(new Event('input'));
  await settle(el);
  (q(el, 'pos-offline-result') as HTMLButtonElement).click();
  await settle(el);
}

const signed = () => core.calls.filter((c) => c.command === 'sign_and_queue_offline_sale').map((c) => c.args?.request as OfflineSaleRequest);

describe('the offline counter', () => {
  beforeEach(() => {
    core = fakeOfflineCore();
    setOfflineInvoke(core.invoke);
  });
  afterEach(() => {
    setOfflineInvoke(undefined);
    document.body.innerHTML = '';
  });

  it('shows the core\'s figures, never its own, with the offline prices line', async () => {
    const el = await mount();
    expect(q(el, 'pos-offline-prices-note')?.textContent).toBe(OFFLINE_PRICES);
    await addProduct(el, 'item');
    expect(core.calls.some((c) => c.command === 'price_offline_cart')).toBe(true);
    // the stand in's odd figures are on the screen as it answered them
    expect(q(el, 'pos-offline-total')?.textContent).toBe('$123.45');
    expect(q(el, 'pos-offline-line-total')?.textContent).toBe('$111.11');
    expect(q(el, 'pos-offline-complete')?.textContent).toContain('$123.45');
  });

  it('greys card and store credit with the connection\'s words, and the account until a cached account is named', async () => {
    const el = await mount();
    for (const m of ['card', 'store_credit']) {
      const b = q(el, `pos-offline-method-${m}`) as HTMLButtonElement;
      expect(b.disabled).toBe(true);
      expect(b.textContent).toContain(NEEDS_CONNECTION);
    }
    expect((q(el, `pos-offline-method-account`) as HTMLButtonElement).disabled).toBe(true);
    expect((q(el, `pos-offline-method-cash`) as HTMLButtonElement).disabled).toBe(false);
  });

  it('refuses a kit at the counter with words, and no kit line reaches the core', async () => {
    const el = await mount();
    await addProduct(el, 'deck kit');
    expect(q(el, 'pos-offline-line-error')?.textContent).toContain('kits do not sell offline');
    expect(el.querySelectorAll('[data-testid="pos-offline-line"]').length).toBe(0);
    expect(core.calls.filter((c) => c.command === 'price_offline_cart')).toHaveLength(0);
  });

  it('sends the core the cart and no figure, names the carried cart, and prints an offline receipt', async () => {
    const el = await mount({ seed: { lines: [{ id: 'l1', product_id: 'p-a', quantity: '2', uom: 'EA' }], refused: ['Item c: charge lines do not sell offline'], replacesSaleId: 'cart-1' } });
    expect(q(el, 'pos-offline-carried-refusal')?.textContent).toContain('charge lines');
    (q(el, 'pos-offline-complete') as HTMLButtonElement).click();
    await settle(el);
    const [req] = signed();
    expect(req.replaces_sale_id).toBe('cart-1');
    expect(req.different_customer).toBe(false);
    expect(req.lines).toEqual([{ id: 'l1', product_id: 'p-a', quantity: '2', uom: 'EA' }]);
    // a blank amount is the core's total, as the core answered it
    expect(req.tenders).toEqual([{ method: 'cash', amount_cents: CORE_TOTAL, reference: null, id_checked: false, id_type: null }]);
    for (const k of ['subtotal_cents', 'tax_cents', 'total_cents', 'tax_rate', 'device_seq', 'prev_digest']) {
      expect(req).not.toHaveProperty(k);
    }
    const receipt = q(el, 'pos-offline-receipt-text')?.textContent ?? '';
    expect(receipt).toContain('OFFLINE SALE');
    expect(receipt).toContain('REG-01-2-000001');
    expect(receipt).toContain('promotions, quantity breaks and job prices not applied offline');
    // the next sale replaces nothing
    (q(el, 'pos-offline-new-sale') as HTMLButtonElement).click();
    await settle(el);
    await addProduct(el, 'item');
    (q(el, 'pos-offline-complete') as HTMLButtonElement).click();
    await settle(el);
    expect(signed()[1].replaces_sale_id).toBeNull();
  });

  it('links every sale to a result unknown cart by default, and the declaration clears it', async () => {
    const el = await mount({ unknownCartId: 'cart-9' });
    await addProduct(el, 'item');
    (q(el, 'pos-offline-complete') as HTMLButtonElement).click();
    await settle(el);
    expect(signed()[0]).toMatchObject({ replaces_sale_id: 'cart-9', different_customer: false, unknown_cart_id: null });
    (q(el, 'pos-offline-new-sale') as HTMLButtonElement).click();
    await settle(el);
    await addProduct(el, 'item');
    const declare = q(el, 'pos-offline-declare')?.querySelector('input') as HTMLInputElement;
    declare.checked = true;
    declare.dispatchEvent(new Event('change'));
    await settle(el);
    (q(el, 'pos-offline-complete') as HTMLButtonElement).click();
    await settle(el);
    expect(signed()[1]).toMatchObject({ replaces_sale_id: null, different_customer: true, unknown_cart_id: 'cart-9' });
    expect(q(el, 'pos-offline-receipt-text')?.textContent).toContain('Declared: a different customer');
  });

  it('marks a line priced at the account\'s level', async () => {
    const el = await mount();
    const find = q(el, 'pos-offline-account-search') as HTMLInputElement;
    find.value = 'harbor';
    find.dispatchEvent(new Event('input'));
    await settle(el);
    (q(el, 'pos-offline-account') as HTMLButtonElement).click();
    await settle(el);
    await addProduct(el, 'item');
    expect(q(el, 'pos-offline-level-price')?.textContent).toContain('Level price');
    expect((q(el, 'pos-offline-method-account') as HTMLButtonElement).disabled).toBe(false);
  });

  it('says the register must be enrolled again when the core finds its store gone', async () => {
    core.refuse.load_offline_snapshot = { code: 're_enrol_required', message: 'x' };
    const el = await mount();
    expect(q(el, 'pos-offline-refused')?.textContent).toContain('This register must be enrolled again before it can sell offline');
  });
});
