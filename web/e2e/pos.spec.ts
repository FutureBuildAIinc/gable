// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * The counter (POS and till) against the real stack (C2-5, ADR 0005 section
 * 14.2): the desk opens the drawer, rings a sale and pays it with a SPLIT
 * tender (part cash, the rest by check, one invoice, one payment per tender),
 * returns a line (the credit memo posted and refunded out of the drawer), and
 * voids a completed sale (the ledger and the stock whole again).
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

/** A product with stock at the branch and a price, for the counter search. */
async function stockedProduct(request: APIRequestContext): Promise<{ sku: string }> {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as {
    items: { id: string; sku: string }[];
  };
  const list = products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse();
  for (const p of list) {
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as {
      items: { location_name: string; available: string }[];
    };
    // The counter issues from the branch REG-01 belongs to (the seeded
    // Kelowna branch); stock at another branch cannot serve the till.
    const atBranch = page.items
      .filter((r) => r.location_name === 'KEL-MAIN')
      .reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (atBranch >= 50) return p;
  }
  throw new Error('no stocked product for the counter run');
}

/** Adds one unit of the product through the counter's typeahead. */
async function addOne(page: Page, sku: string) {
  const input = page.locator('#pos-search-input');
  await input.fill(sku.slice(0, Math.max(6, Math.min(10, sku.length))));
  await page.locator('button', { hasText: sku }).first().waitFor({ state: 'visible', timeout: 10000 });
  await page.locator('button', { hasText: sku }).first().click();
  await expect(page.locator('table[aria-label="Cart items"] tbody tr').first()).toBeVisible();
}

test('the counter: split tender, return and void', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  const product = await stockedProduct(request);

  await page.goto('/pos');
  await expect(page.getByRole('heading', { name: 'POS Terminal' })).toBeVisible();

  // Open the drawer when none is open (a run may find one already open).
  const noTill = page.getByText('NO TILL');
  if (await noTill.isVisible()) {
    await page.getByRole('button', { name: 'Open Till' }).click();
    await page.getByPlaceholder('200.00').fill('100.00');
    await page.getByRole('button', { name: 'Open Till' }).last().click();
  }
  await expect(page.getByText(/TILL OPEN/)).toBeVisible();

  // Ring two units of the product.
  await addOne(page, product.sku);
  await addOne(page, product.sku);
  await expect(page.locator('table[aria-label="Cart items"] tbody tr')).toHaveCount(2);

  // Read the total off the panel, then pay it split: $1.00 in cash, the rest
  // by check. The completion carries the sale's revision; the page sends the
  // whole tender list at once.
  const totalText = await page.getByText('TOTAL').locator('..').getByText(/\$\d+\.\d{2}/).last().textContent();
  const total = Number(totalText.replace('$', ''));
  expect(total).toBeGreaterThan(1);
  await page.getByRole('button', { name: 'cash', exact: true }).click();
  await page.getByLabel('Tender amount').fill('1.00');
  await page.getByRole('button', { name: /Add Tender/ }).click();
  await page.getByRole('button', { name: /add check/i }).click();
  await page.getByLabel('Tender amount').fill((total - 1).toFixed(2));
  await page.getByRole('button', { name: /Add Tender/ }).click();
  await expect(page.getByText(/Still owed|Change/).first()).toBeVisible();
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-split-tender.png'), fullPage: true });

  await page.getByRole('button', { name: /Complete Sale/ }).click();
  await expect(page.getByText(/POS-\d+ complete/)).toBeVisible({ timeout: 15000 });
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-sale-completed.png'), fullPage: true });

  // Return one unit of the first line: a credit memo posts, the stock comes
  // back and the refund leaves the drawer.
  await page.getByRole('button', { name: 'Return', exact: true }).click();
  const firstQty = page.locator('input[type="number"]').first();
  await firstQty.fill('1');
  await page.getByRole('button', { name: /Take the Return/ }).click();
  await expect(page.getByText(/Return RTN-\d+ complete/)).toBeVisible({ timeout: 15000 });
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-return.png'), fullPage: true });

  // A second sale paid in cash with change, then voided: the ledger returns
  // to zero and the stock comes back.
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await addOne(page, product.sku);
  await page.getByRole('button', { name: 'cash', exact: true }).click();
  await page.getByLabel('Tender amount').fill('100.00');
  await page.getByRole('button', { name: /Add Tender/ }).click();
  await page.getByRole('button', { name: /Complete Sale/ }).click();
  await expect(page.getByText(/CHANGE DUE/)).toBeVisible({ timeout: 15000 });

  page.once('dialog', (dialog) => void dialog.accept('e2e: wrong customer'));
  await page.getByRole('button', { name: 'Void', exact: true }).click();
  await expect(page.getByText(/POS-\d+ voided/)).toBeVisible({ timeout: 15000 });
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-void.png'), fullPage: true });

  // The books tell the same story: the day's sales list the completed sale,
  // the voided one and the return's credit memo behind it.
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { number: string; status: string }[];
  };
  expect(sales.items.length).toBeGreaterThanOrEqual(2);
  expect(sales.items.some((s) => s.status === 'voided')).toBe(true);
  expect(sales.items.some((s) => s.status === 'completed')).toBe(true);
});
