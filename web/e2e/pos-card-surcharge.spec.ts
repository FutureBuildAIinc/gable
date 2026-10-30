// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * CP-15 card surcharges: a manager switches the branch's card fee on at 3
 * percent on the Counter rules screen (off by default), a rate above the
 * brands' cap is refused, and the Pay screen tells the clerk the expected fee
 * and the cash price before the card is presented. The notice is only a
 * notice: the processor's answer decides the fee. The CI stack wires no
 * terminal, so its card tender carries no answer and the sale stores no fee,
 * the total unchanged. What the answer's fee does is proved in the Go tests:
 * stored on the card tender (TestCP15_AnswerFeeIsStoredOnTheCardTender), booked
 * to its own income account (TestCP15_FeeBooksToItsOwnIncomeAccountNotRevenueOrTax),
 * "Card fee" on the 80mm receipt (TestThermalReceiptCardFeeLine), and given
 * back with a card return or void (TestCP15_ReturnRefundsTheFeeWithTheCard,
 * TestCP15_AVoidTakesTheFeeBack).
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

async function stockedProduct(request: APIRequestContext): Promise<{ sku: string }> {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as {
    items: { id: string; sku: string }[];
  };
  const list = products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse();
  for (const p of list) {
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as {
      items: { location_name: string; available: string }[];
    };
    const atBranch = page.items
      .filter((r) => r.location_name === 'KEL-MAIN')
      .reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (atBranch >= 50) return p;
  }
  throw new Error('no stocked product for the card surcharge run');
}

async function openCounter(page: Page) {
  await page.goto('/pos');
  await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible();
  const tillState = page.getByTestId('pos-till-state');
  await expect(tillState).toBeVisible();
  if ((await tillState.textContent())?.includes('No till')) {
    await page.getByRole('button', { name: 'Open Till' }).click();
    await page.getByTestId('till-float-amount').fill('100.00');
    await page.getByRole('button', { name: 'Open Till' }).last().click();
  }
  await expect(tillState).toContainText('Till open');
}

async function saleIdByNumber(request: APIRequestContext, number: string): Promise<string> {
  const sales = (await (
    await request.get('/api/v1/pos/transactions?register_id=REG-01')
  ).json()) as { items: { id: string; number: string; status: string }[] };
  const sale = sales.items.find((s) => s.number === number);
  expect(sale, `the sale ${number} is listed`).toBeTruthy();
  return sale!.id;
}

test('CP-15: the branch switches the card fee on, the counter states it before the card, and only an answer charges it', async ({ page, request }) => {
  await signIn(page, 'Playwright Admin');
  const saved = (await (await request.get('/api/v1/pos/settings')).json()) as {
    revision: number;
    rules: Record<string, unknown> & { card_surcharge: { enabled: boolean; rate_bps: number } | null };
  };
  expect(saved.rules.card_surcharge, 'the settings name the branch').not.toBeNull();
  expect(saved.rules.card_surcharge?.enabled, 'the card fee is off by default').toBe(false);
  try {
    // The Counter rules screen: the switch, the rate and the disclosure.
    await page.goto('/admin/counter-rules');
    await expect(page.getByRole('heading', { name: 'Counter rules' })).toBeVisible();
    const sw = page.getByTestId('counter-rules-card-fee-switch').locator('input');
    await expect(sw).not.toBeChecked();
    await expect(page.getByTestId('counter-rules-card-fee-disclosure')).toContainText('never more than 3 percent');
    await sw.check();
    await page.getByTestId('counter-rules-card-fee-rate').fill('3');
    await page.getByTestId('counter-rules-save').click();
    await expect(page.getByTestId('counter-rules-saved')).toContainText('Counter rules saved');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'cp15-switch-on.png'), fullPage: true });
    const now = (await (await request.get('/api/v1/pos/settings')).json()) as {
      rules: { card_surcharge: { enabled: boolean; rate_bps: number } };
    };
    expect(now.rules.card_surcharge).toMatchObject({ enabled: true, rate_bps: 300 });

    // A rate above the brands' cap is refused by the screen and by the server.
    await page.getByTestId('counter-rules-card-fee-rate').fill('3.5');
    await page.getByTestId('counter-rules-save').click();
    await expect(page.getByTestId('counter-rules-error')).toContainText('0 to 3');
    const over = await request.put('/api/v1/pos/settings', {
      headers: { 'If-Match': `"${(await (await request.get('/api/v1/pos/settings')).json()).revision}"` },
      data: { rules: { ...rulesToWrite(saved.rules), card_surcharge: { enabled: true, rate_bps: 350 } } },
    });
    expect(over.status(), await over.text()).toBe(400);

    // Ring a sale and take it on a card.
    const product = await stockedProduct(request);
    await openCounter(page);
    const lines = page.locator('[data-testid="pos-lines"] li');
    const before = await lines.count();
    await page.getByTestId('catalog-search').fill(product.sku);
    await page.getByTestId('catalog-search').press('Enter');
    await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
    const totalText = (await page.getByTestId('summary-total').textContent()) ?? '';
    const totalCents = Math.round(Number(totalText.replace(/[$,\s]/g, '')) * 100);
    expect(totalCents).toBeGreaterThan(0);
    const expectedFee = Math.floor((totalCents * 300 * 2 + 10000) / 20000);
    const head = page.getByTestId('panel-payment-toggle');
    if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
    await expect(page.getByTestId('pos-card-fee')).toHaveCount(0);
    await page.getByTestId('tender-card').click();
    // The notice before the card is presented: the rate, the expected fee, the cash price.
    await expect(page.getByTestId('pos-card-fee-notice')).toContainText('Card fee 3%');
    await expect(page.getByTestId('pos-card-fee-notice')).toContainText(`$${(expectedFee / 100).toFixed(2)}`);
    await expect(page.getByTestId('pos-card-fee-cash')).toContainText(`Cash price $${(totalCents / 100).toFixed(2)}`);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'cp15-notice.png'), fullPage: true });
    await page.getByRole('button', { name: 'Add as split tender' }).click();
    await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
    await page.getByTestId('pos-pay').click();
    const complete = page.getByTestId('pos-complete');
    await expect(complete).toContainText('Sale complete', { timeout: 15000 });

    // No answer carried a fee, so none is on the sale: the notice never
    // charges, and the total is the one the clerk rang.
    await expect(page.getByTestId('summary-card-fee')).toHaveCount(0);
    await expect(page.getByTestId('summary-total')).toContainText(totalText.trim());
    await page.screenshot({ path: path.join(SHOTS_DIR, 'cp15-complete.png'), fullPage: true });
    const number = ((await complete.textContent()) ?? '').match(/POS-\d+/)?.[0] ?? '';
    expect(number, 'the completed card names the sale').not.toBe('');
    const saleId = await saleIdByNumber(request, number);
    const sale = (await (await request.get(`/api/v1/pos/transactions/${saleId}`)).json()) as {
      total_cents: number;
      tenders: { method: string; amount_cents: number; surcharge_cents: number }[];
    };
    expect(sale.total_cents).toBe(totalCents);
    const cardTender = sale.tenders.find((t) => t.method === 'card');
    expect(cardTender?.amount_cents).toBe(totalCents);
    expect(cardTender?.surcharge_cents).toBe(0);

    // The 80mm receipt prints no fee line for a sale that carries none.
    const receipt = await request.get(`/api/v1/pos/transactions/${saleId}/receipt?layout=thermal80`);
    expect(receipt.status(), await receipt.text()).toBe(200);
    expect((await receipt.body()).toString('latin1')).not.toContain('Card fee');
  } finally {
    // The rules the walk found go back: the card fee off, as it was.
    const now = (await (await request.get('/api/v1/pos/settings')).json()) as { revision: number };
    const put = await request.put('/api/v1/pos/settings', {
      headers: { 'If-Match': `"${now.revision}"` },
      data: {
        rules: {
          ...rulesToWrite(saved.rules),
          card_surcharge: { enabled: saved.rules.card_surcharge?.enabled ?? false, rate_bps: saved.rules.card_surcharge?.rate_bps ?? 0 },
        },
        revision: now.revision,
      },
    });
    expect(await put.text(), `putting the rules back = ${put.status()}`).toContain('"rules"');
  }
});

/** The read's rules as a write: without the read only suggested figures. */
function rulesToWrite(rules: Record<string, unknown>): Record<string, unknown> {
  const { restocking_fee_defaults: _suggested, card_surcharge: _card, ...writable } = rules;
  return writable;
}
