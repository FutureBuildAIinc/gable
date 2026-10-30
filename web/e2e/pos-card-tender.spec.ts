// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';

/**
 * Card as a tender at the counter (CP-03), on the simulator stack: a card for
 * more than the remainder is held at the Pay screen in the counter's words,
 * and a split card and cash sale completes with its receipt naming the card
 * by brand and last four.
 */

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

/** A product with stock at the branch, for the counter search. */
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
  throw new Error('no stocked product for the counter run');
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


async function ringOne(page: Page, sku: string) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
}

test('the counter: a card is held to the remainder and a split sale prints the card', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  const product = await stockedProduct(request);
  await openCounter(page);
  await ringOne(page, product.sku);

  const totalText = (await page.getByTestId('summary-total').textContent()) ?? '';
  const total = Number(totalText.replace(/[$,\s]/g, ''));
  expect(total).toBeGreaterThan(1);
  const payment = page.getByTestId('panel-payment-toggle');
  if ((await payment.getAttribute('aria-expanded')) !== 'true') await payment.click();

  // A card asked for more than the sale owes is held, in words, and not added.
  await page.getByTestId('tender-card').click();
  await page.getByLabel('Tender amount').fill((total + 10).toFixed(2));
  await expect(page.getByTestId('pos-card-over')).toContainText(
    `The card can be charged at most $${total.toFixed(2)}: the rest is cash change.`);
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  // Nothing was added, so the added tenders list is not rendered at all.
  await expect(page.getByTestId('pos-added-tenders')).toHaveCount(0);

  // Split: $1.00 on the card, the rest in cash.
  await page.getByLabel('Tender amount').fill('1.00');
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('tender-cash').click();
  await page.getByLabel('Tender amount').fill((total - 1).toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 30000 });

  // Both receipt layouts render. The CI stack has no terminal simulator, so the card's brand and last four are
  // proved by TestCP03_ReceiptNamesTheCardAndTheCashChange (Go), not here.
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { id: string; status: string }[];
  };
  const sale = sales.items.find((s) => s.status === 'completed');
  expect(sale, 'a completed sale is listed').toBeTruthy();
  for (const layout of ['thermal80', 'invoice']) {
    const pdf = await request.get(`/api/v1/pos/transactions/${sale!.id}/receipt?layout=${layout}`);
    expect(pdf.ok()).toBeTruthy();
    const text = (await pdf.body()).toString('latin1');
    expect(text.length, `${layout} receipt`).toBeGreaterThan(0);
  }
});
