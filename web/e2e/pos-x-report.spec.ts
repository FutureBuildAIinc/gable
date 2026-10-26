// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * The X report and the stored Z reports at the counter (CP-08): on an open
 * drawer with a cash and check sale and a safe drop recorded, the X report
 * reads the drawer mid shift (not a close; the drawer stays open) and prints
 * through the browser; after the blind close, Z Reports lists the frozen Z
 * the close just wrote and opens it read only.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

async function signIn(page: Page, name: string) {
  await page.addInitScript(() => {
    (window as unknown as { __prints: number }).__prints = 0;
    window.print = () => { (window as unknown as { __prints: number }).__prints += 1; };
  });
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
    const inv = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as {
      items: { location_name: string; available: string }[];
    };
    const atBranch = inv.items
      .filter((r) => r.location_name === 'KEL-MAIN')
      .reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (atBranch >= 50) return p;
  }
  throw new Error('no stocked product for the counter run');
}

/**
 * The register starts the walk with no drawer of its own: a drawer another
 * run left open is closed through the API on its own expected figures, so
 * the X report's figures below are this walk's alone.
 */
async function closeAnyOpenTill(request: APIRequestContext) {
  const res = await request.get('/api/v1/pos/till/current?register_id=REG-01');
  if (res.status() === 404) return;
  expect(res.ok(), await res.text()).toBe(true);
  const session = (await res.json()) as { id: string };
  const report = (await (await request.get(`/api/v1/pos/till/${session.id}/report`)).json()) as {
    expected_by_method: Record<string, number>;
  };
  const counted: Record<string, number> = {};
  for (const [method, cents] of Object.entries(report.expected_by_method)) {
    if (method !== 'CARD') counted[method.toLowerCase()] = cents;
  }
  const close = await request.post(`/api/v1/pos/till/${session.id}/close`, { data: { counted_by_method: counted } });
  expect(close.ok(), await close.text()).toBe(true);
}

/** Opens the counter and a fresh drawer with a $100.00 float. */
async function openCounter(page: Page) {
  await page.goto('/pos');
  await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible();
  await expect(page.getByTestId('pos-till-state')).toContainText('No till');
  await page.getByRole('button', { name: 'Open Till' }).click();
  await page.getByTestId('till-float-amount').fill('100.00');
  await page.getByRole('button', { name: 'Open Till' }).last().click();
  await expect(page.getByTestId('pos-till-state')).toContainText('Till open');
}

/** Adds one unit of the product through the Items card. */
async function addOne(page: Page, sku: string) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
}

/** The sale total in dollars, read off the Order summary. */
async function saleTotal(page: Page): Promise<number> {
  const text = (await page.getByTestId('summary-total').textContent()) ?? '';
  return Number(text.replace(/[$,\s]/g, ''));
}

test('the counter: X report mid shift, then the close and its stored Z report', async ({ page, request }) => {
  await signIn(page, 'POS X Report E2E');
  const product = await stockedProduct(request);
  await closeAnyOpenTill(request);
  await openCounter(page);

  // One sale paid split: $1.00 in cash, the rest by check.
  await addOne(page, product.sku);
  const total = await saleTotal(page);
  expect(total).toBeGreaterThan(1);
  const payment = page.getByTestId('panel-payment-toggle');
  if ((await payment.getAttribute('aria-expanded')) !== 'true') await payment.click();
  await page.getByTestId('tender-cash').click();
  await page.getByLabel('Tender amount').fill('1.00');
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('tender-check').click();
  await page.getByLabel('Tender amount').fill((total - 1).toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });

  // A safe drop of $5.00 out of the drawer.
  await page.getByTestId('pos-drawer-cash').click();
  await page.getByTestId('movement-amount').fill('5.00');
  await page.getByTestId('movement-record').click();
  await expect(page.getByText(/Safe drop of \$5.00 recorded/)).toBeVisible({ timeout: 10000 });

  // The X report: a mid-shift read, not a close. The float $100.00 plus the
  // $1.00 cash less the $5.00 drop leaves $96.00 expected in the drawer.
  await page.getByTestId('pos-x-report').click();
  const xreport = page.getByTestId('till-x-report');
  await expect(xreport).toBeVisible();
  await expect(page.getByTestId('till-report-title')).toHaveText('X REPORT');
  await expect(xreport).toContainText('Not a close; the drawer stays open');
  await expect(page.getByTestId('till-report-register')).toContainText('REG-01');
  // The cashier is named in words (a person or the register), never by an id.
  await expect(page.getByTestId('till-report-cashier')).not.toHaveText(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}/i);
  await expect(page.getByTestId('till-report-cashier')).not.toBeEmpty();
  await expect(page.getByTestId('till-report-float')).toHaveText('$100.00');
  await expect(page.getByTestId('till-report-sales')).toContainText('1 rung');
  await expect(page.getByTestId('till-report-sales')).toContainText(`$${total.toFixed(2)}`);
  await expect(page.getByTestId('till-report-tender-cash')).toContainText('$1.00');
  await expect(page.getByTestId('till-report-tender-cash')).toContainText('$96.00');
  await expect(page.getByTestId('till-report-tender-check')).toContainText(`$${(total - 1).toFixed(2)}`);
  await expect(page.getByTestId('till-report-movement')).toContainText('Safe drop');
  await expect(page.getByTestId('till-report-movement')).toContainText('$5.00');
  await expect(page.getByTestId('till-report-expected-cash')).toHaveText('$96.00');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-x-report.png'), fullPage: true });

  // Print it: the browser print of the sheet.
  await page.getByTestId('till-report-print').click();
  expect(await page.evaluate(() => (window as unknown as { __prints: number }).__prints)).toBe(1);
  await page.getByTestId('till-report-close').click();

  // The drawer stayed open: the X read is not a close.
  await expect(page.getByTestId('pos-till-state')).toContainText('Till open');

  // The blind close on the figures the X report named: balanced.
  await page.getByRole('button', { name: 'Close Till' }).click();
  await expect(page.getByRole('heading', { name: 'Close Till - Blind Count' })).toBeVisible();
  await page.getByTestId('till-count-cash').fill('96.00');
  await page.getByTestId('till-count-check').fill((total - 1).toFixed(2));
  await page.getByRole('button', { name: 'Post Count & Close' }).click();
  await expect(page.getByRole('heading', { name: 'Z-Report - Till Closed' })).toBeVisible({ timeout: 15000 });
  await expect(page.getByText('BALANCED')).toBeVisible();
  await page.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByTestId('pos-till-state')).toContainText('No till');

  // Z Reports lists the frozen close just written and opens it read only.
  await page.getByTestId('pos-z-reports').click();
  const row = page.getByTestId('z-report-row').first();
  await expect(row).toBeVisible({ timeout: 10000 });
  await expect(row).toContainText('Balanced');
  await row.click();
  const zreport = page.getByTestId('till-z-report');
  await expect(zreport).toBeVisible();
  await expect(page.getByTestId('till-report-title')).toHaveText('Z REPORT');
  await expect(zreport).toContainText('read only');
  await expect(page.getByTestId('till-report-cashier')).not.toHaveText(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}/i);
  await expect(page.getByTestId('till-report-count')).toContainText('$96.00');
  await expect(page.getByTestId('till-report-count')).toContainText(`$${(total - 1).toFixed(2)}`);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-z-report.png'), fullPage: true });
});
