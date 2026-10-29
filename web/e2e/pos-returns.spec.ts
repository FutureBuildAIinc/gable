// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { pickOptionByValue } from './app-controls';
import { openCounter } from './pos-till';
import path from 'path';
import fs from 'fs';

/**
 * CP-06 POS returns: ring and complete a two-line sale, then return it through
 * the "Return a past sale" search panel with a mix of restocked and damaged
 * lines, and finally open the refund intents manager screen.
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
  throw new Error('no stocked product for the returns run');
}

async function addOne(page: Page, sku: string) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
}

async function openPayment(page: Page) {
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
}

async function completeCashSale(page: Page, request: APIRequestContext): Promise<string> {
  const product = await stockedProduct(request);
  await openCounter(page);
  await addOne(page, product.sku);
  await addOne(page, product.sku);
  await expect(page.locator('[data-testid="pos-lines"] li')).toHaveCount(2);
  await openPayment(page);
  await page.getByTestId('tender-cash').click();
  const totalText = (await page.getByTestId('summary-total').textContent()) ?? '';
  const total = Number(totalText.replace(/[$,\s]/g, ''));
  await page.getByLabel('Tender amount').fill(total.toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();
  const card = page.getByTestId('pos-complete');
  await expect(card).toContainText('Sale complete', { timeout: 15000 });
  const numberMatch = (await card.textContent())?.match(/POS-\d+/);
  const saleNumber = numberMatch?.[0] ?? '';
  expect(saleNumber).not.toBe('');
  return saleNumber;
}

test('CP-06: return a past sale with mixed restock choices and open refund intents', async ({ page, request }) => {
  await signIn(page, 'POS Returns E2E');
  const saleNumber = await completeCashSale(page, request);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'cp06-sale-completed.png'), fullPage: true });

  // Start a fresh sale so the return search is the only path back to the first sale.
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await expect(page.getByTestId('pos-no-lines')).toBeVisible();

  // Open the past-sale search and find the completed sale by number.
  await page.getByTestId('return-search-open').click();
  await expect(page.getByTestId('return-search-query')).toBeVisible();
  await page.getByTestId('return-search-query').fill(saleNumber);
  await page.getByTestId('return-search-submit').click();
  await expect(page.getByTestId('return-search-result')).toBeVisible();
  await expect(page.getByTestId('return-search-result-number')).toContainText(saleNumber);
  await expect(page.getByTestId('return-search-result-status')).toContainText('Completed');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'cp06-search-result.png'), fullPage: true });

  // Pick the sale and return the first line as damaged, the second as back to stock.
  await page.getByTestId('return-search-result-pick').click();
  await expect(page.getByTestId('return-qty').first()).toBeVisible();
  const qtyInputs = page.locator('[data-testid="return-qty"]');
  await qtyInputs.nth(0).fill('1');
  await qtyInputs.nth(1).fill('1');
  const restockOptions = page.locator('[data-testid="return-restock-damaged"]');
  await restockOptions.nth(0).click();
  // Second line stays back to stock by default. Each returned line names a
  // reason from the closed list (the damaged line is the damaged one).
  await pickOptionByValue(page.getByTestId('return-reason').nth(0), 'damaged');
  await pickOptionByValue(page.getByTestId('return-reason').nth(1), 'changed_mind');
  const returnResponse = page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/api/v1/pos/returns' && r.request().method() === 'POST',
  );
  await page.getByTestId('return-take').click();
  const retRes = await returnResponse;
  expect(retRes.status()).toBe(201);
  const retBody = (await retRes.json()) as { credit_memo_id: string | null };
  expect(retBody.credit_memo_id).toBeTruthy();
  const memoRes = await request.get(`/api/v1/credit-memos/${retBody.credit_memo_id}`);
  expect(memoRes.status()).toBe(200);
  const memoBody = (await memoRes.json()) as { status: string };
  expect(memoBody.status).toBe('applied');
  await expect(page.getByText(/Return RTN-\d+ complete/)).toBeVisible({ timeout: 15000 });
  await expect(page.getByText(/\$[\d,.]+ refunded/)).toBeVisible();
  await page.screenshot({ path: path.join(SHOTS_DIR, 'cp06-return-complete.png'), fullPage: true });

  // Open the refund intents manager screen from the Daily Till menu and expect the heading.
  await page.goto('/reports/daily-till');
  await page.getByRole('link', { name: 'Refund intents' }).click();
  await expect(page.getByRole('heading', { name: 'Refund intents' })).toBeVisible();
  await expect(page.getByTestId('refund-intents-empty')).toBeVisible();
  await page.screenshot({ path: path.join(SHOTS_DIR, 'cp06-refund-intents.png'), fullPage: true });
});
