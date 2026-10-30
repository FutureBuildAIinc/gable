// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';

/**
 * The counter's card present flow over the terminal seam (CP-05): the Run
 * Terminal API driver fills the same seam the simulator fills, so the
 * counter's card flow is unchanged and this spec runs on the simulator
 * stack. The dealer's steps of the row, end to end: ring a sale, take it on
 * a card (the terminal takes the leg and answers), the sale completes with
 * the card tender, and the books carry the card payment.
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

test('the counter: a card present sale over the terminal seam', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  const product = await stockedProduct(request);

  await openCounter(page);

  // Ring one unit of the product.
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(product.sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });

  // Read the total, then take it all on a card: the terminal takes the leg,
  // the counter waits on the device's answer, and the sale completes on it.
  const totalText = (await page.getByTestId('summary-total').textContent()) ?? '';
  const total = Number(totalText.replace(/[$,\s]/g, ''));
  expect(total).toBeGreaterThan(0);
  const payment = page.getByTestId('panel-payment-toggle');
  if ((await payment.getAttribute('aria-expanded')) !== 'true') await payment.click();
  await page.getByTestId('tender-card').click();
  await page.getByLabel('Tender amount').fill(total.toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
  await page.getByTestId('pos-pay').click();

  // The Sale complete card carries the sale's number and the total.
  const card = page.getByTestId('pos-complete');
  await expect(card).toContainText('Sale complete', { timeout: 30000 });
  await expect(card).toContainText(/POS-\d+/);
  await expect(card).toContainText(`$${total.toFixed(2)}`);

  // The books tell the same story: the completed sale was paid by card.
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { id: string; number: string; status: string; total_cents: number }[];
  };
  const completed = sales.items.find((s) => s.status === 'completed');
  expect(completed, 'a completed sale is listed').toBeTruthy();
  const full = (await (await request.get(`/api/v1/pos/transactions/${completed!.id}`)).json()) as {
    tenders: { method: string; amount_cents: number }[];
  };
  expect(full.tenders.map((t) => t.method)).toEqual(['card']);
  expect(full.tenders[0].amount_cents).toBe(completed!.total_cents);
});
