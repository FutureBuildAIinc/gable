// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';
import path from 'path';
import fs from 'fs';

/**
 * The pre-ship exposure gate on the order desk (QE-07): an order converted
 * from a quote whose commodity price has unresolved index exposure answers
 * Confirm Order with the gate's blockers in plain words, and the same
 * confirm goes through once the customer acknowledges the price change on
 * the quote. Everything is seeded through the API the way the desk seeds
 * it: the customer's policy, the sent quote, the index move, the convert.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

test.describe('the order exposure gate on the desk', () => {
  test('confirm answers the exposure blockers in plain words, and goes through once acknowledged', async ({ page, request }) => {
    // A customer on the acknowledgement policy.
    const customer = await (await request.post('/api/v1/customers', {
      data: { name: 'E2E Exposure Gate Co', account_number: 'E2E-XGS-' + Math.floor(Math.random() * 1e6) },
    })).json();
    const policy = await request.put(`/api/v1/customers/${customer.id}/escalation-policy`, {
      data: { policy: 'require_ack', threshold_percent: '5', revision: customer.revision },
    });
    expect(policy.status(), await policy.text()).toBe(200);

    // A sent quote with a commodity line (framing lumber prices on the
    // Random Lengths framing composite), so the send snapshots an escalator.
    type Product = { id: string; sku: string; stock_uom: string };
    let stud: Product | undefined;
    let cursor = '';
    const seen: string[] = [];
    for (let page = 0; page < 20 && !stud; page++) {
      const res = (await (await request.get(`/api/v1/products?limit=200${cursor}`)).json()) as { items: Product[]; next_cursor: string | null };
      seen.push(...res.items.map((p) => p.sku));
      stud = res.items.find((p) => p.sku === 'DL_SPF_2X4_L08_S');
      if (!res.next_cursor) break;
      cursor = `&cursor=${encodeURIComponent(res.next_cursor)}`;
    }
    expect(stud, `the seed frames 2x4x8 SPF as a commodity (scanned ${seen.length} products, first ${seen.slice(0, 5).join(', ')})`).toBeDefined();
    if (!stud) return;
    const quote = await (await request.post('/api/v1/quotes', {
      data: {
        customer_id: customer.id,
        lines: [{ product_id: stud.id, quantity: '200', uom: stud.stock_uom, unit_price_ten_thousandths: 55000 }],
      },
    })).json();
    const sent = await request.post(`/api/v1/quotes/${quote.id}/transitions`, { data: { to: 'sent', revision: 1 } });
    expect(sent.status(), await sent.text()).toBe(200);

    // An index move past the 5 percent threshold: the scanner rolls the
    // quote up to ACK_REQUIRED.
    const indices = (await (await request.get('/api/v1/market-indices')).json()) as { id: string; index_code: string; current_value: number }[];
    const framing = indices.find((i) => i.index_code === 'RL_FRAMING_COMP')!;
    expect(framing, 'the framing composite index is seeded').toBeDefined();
    const refresh = await request.post(`/api/v1/market-indices/${framing.id}/refresh`, {
      data: { new_value: Math.round(framing.current_value * 1.2 * 100) / 100, source: 'E2E' },
    });
    expect(refresh.status(), await refresh.text()).toBe(200);
    const exposure = await (await request.get(`/api/v1/quotes/${quote.id}/exposure`)).json();
    expect(exposure.exposure_state).toBe('ACK_REQUIRED');

    // The convert: the order is linked to the quote the gate reads.
    const order = await (await request.post(`/api/v1/quotes/${quote.id}/convert`)).json();
    expect(order.status).toBe('draft');
    expect(order.quote_id).toBe(quote.id);

    await signIn(page, 'Playwright Exposure');
    await page.goto(`/orders/${order.id}`);
    await expect(page.getByRole('button', { name: 'Confirm Order' })).toBeVisible();

    // Confirm Order is refused: the blockers speak in plain words, the quote
    // among them, never a generic error.
    await page.getByRole('button', { name: 'Confirm Order' }).click();
    await page.getByRole('button', { name: 'Confirm', exact: true }).click();
    const blocked = page.locator('[data-testid="exposure-blocked"]');
    await expect(blocked).toBeVisible();
    await expect(blocked).toContainText('no longer safe to ship');
    await expect(blocked).toContainText(quote.number);
    await expect(blocked).toContainText('must acknowledge the price change');
    await expect(blocked).toContainText('owner must override');
    await expect(blocked.locator(`a[href="/quotes/${quote.id}"]`)).toHaveText(quote.number);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'order-exposure-blocked.png') });

    // The customer acknowledges the price change on the quote.
    await page.goto(`/quotes/${quote.id}`);
    await page.getByRole('button', { name: 'Acknowledge', exact: true }).click();
    await page.locator('gable-acknowledgment-modal textarea').fill('Customer accepted the new price by phone this morning');
    await page.getByRole('button', { name: 'Record Acknowledgment' }).click();
    await expect(page.getByText('Acknowledgment recorded')).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'order-exposure-acknowledged.png') });

    // The same confirm now goes through.
    await page.goto(`/orders/${order.id}`);
    await page.getByRole('button', { name: 'Confirm Order' }).click();
    await page.getByRole('button', { name: 'Confirm', exact: true }).click();
    await expect(page.getByText('Order confirmed')).toBeVisible();
    await expect(page.getByText('Confirmed', { exact: true })).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'order-exposure-confirmed.png') });
  });
});
