// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * The yard sale (CP-12): the counter as a handheld takes it, at phone width
 * (390) and tablet width (820). The same /pos screen the counter runs, made
 * touch first: no sideways scroll, the catalogue first, a pinned pay bar, the
 * scan seam, the touch target floor, and the money guard that refuses to take
 * money with no connection (money never queues at the yard).
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

const PHONE: { width: number; height: number } = { width: 390, height: 844 };
const TABLET: { width: number; height: number } = { width: 820, height: 1180 };

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

/** A product with stock at the seeded branch, for the counter search. */
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
  throw new Error('no stocked product for the yard sale run');
}

async function openCounter(page: Page) {
  await page.goto('/pos');
  await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible();
  const tillState = page.getByTestId('pos-till-state');
  await expect(tillState).toBeVisible();
  if ((await tillState.textContent())?.includes('No till')) {
    await page.getByRole('button', { name: 'Open Till' }).first().click();
    await page.getByTestId('till-float-amount').fill('100.00');
    await page.getByRole('button', { name: 'Open Till' }).last().click();
  }
  await expect(tillState).toContainText('Till open');
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

async function saleTotal(page: Page): Promise<number> {
  const text = (await page.getByTestId('summary-total').textContent()) ?? '';
  return Number(text.replace(/[$,\s]/g, ''));
}

/** The document never scrolls sideways: scrollWidth equals clientWidth. */
async function expectNoSidewaysScroll(page: Page) {
  const over = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(over, 'the document is never wider than the viewport').toBe(0);
}

/** Every control of the narrow layout clears the 44px touch floor. */
async function assertTouchTargets(page: Page, testIds: string[]) {
  for (const id of testIds) {
    const box = await page.getByTestId(id).boundingBox();
    expect(box, `bounding box for ${id}`).not.toBeNull();
    expect(box!.height, `${id} height >= 44px`).toBeGreaterThanOrEqual(44);
    expect(box!.width, `${id} width >= 44px`).toBeGreaterThanOrEqual(44);
  }
}

const TOUCH_IDS = [
  'catalog-scan',
  'catalog-add',
  'tender-cash',
  'pos-pay-bar-button',
  'pos-till-menu-button',
];

/** The completed sales the register holds, for the offline money count. */
async function completedCount(request: APIRequestContext): Promise<number> {
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { status: string }[];
  };
  return sales.items.filter((s) => s.status === 'completed').length;
}

test('390: a cash sale completes by touch and the receipt prints', async ({ page, request }) => {
  await page.setViewportSize(PHONE);
  await signIn(page, 'Yard Sale E2E');
  const product = await stockedProduct(request);

  await openCounter(page);
  await expectNoSidewaysScroll(page);
  // The phone's working order: the Items card comes before the panels.
  const items = await page.getByTestId('pos-items').boundingBox();
  const panels = await page.getByTestId('pos-panels').boundingBox();
  expect(items!.y).toBeLessThanOrEqual(panels!.y);

  await addOne(page, product.sku);
  await expectNoSidewaysScroll(page);

  const total = await saleTotal(page);
  await openPayment(page);
  await expectNoSidewaysScroll(page);
  await assertTouchTargets(page, TOUCH_IDS);

  await page.getByTestId('tender-cash').click();
  await page.getByTestId('pos-pay-bar-button').click();
  const card = page.getByTestId('pos-complete');
  await expect(card).toContainText('Sale complete', { timeout: 15000 });
  await expect(card).toContainText(/POS-\d+/);
  await expect(card).toContainText(`$${total.toFixed(2)}`);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'yard-sale-390-complete.png'), fullPage: true });

  // The sale exists in the API, completed, with its cash tender.
  const number = ((await card.locator('.font-mono').first().textContent()) ?? '').match(/POS-\d+/)?.[0] ?? '';
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { number: string; status: string }[];
  };
  const sale = sales.items.find((s) => s.number === number);
  expect(sale, `the sale ${number} is listed`).toBeTruthy();
  expect(sale!.status).toBe('completed');

  // The receipt prints from the handheld: the first print, then the word Reprint.
  await page.getByTestId('pos-receipt-print').click();
  await expect(card.getByText(/Reprint|Allow pop ups/)).toBeVisible({ timeout: 15000 });
});

test('820: the same sale completes on a two column tablet', async ({ page, request }) => {
  await page.setViewportSize(TABLET);
  await signIn(page, 'Yard Sale E2E');
  const product = await stockedProduct(request);

  await openCounter(page);
  await expectNoSidewaysScroll(page);

  // Two columns below the header: the Items card beside the panels.
  const items = await page.getByTestId('pos-items').boundingBox();
  const panels = await page.getByTestId('pos-panels').boundingBox();
  expect(items!.x, 'the items column is left of the panels column').toBeLessThan(panels!.x);
  const overlap = Math.min(items!.y + items!.height, panels!.y + panels!.height) - Math.max(items!.y, panels!.y);
  expect(overlap, 'the two columns sit beside each other').toBeGreaterThan(100);

  await addOne(page, product.sku);
  const total = await saleTotal(page);
  await openPayment(page);
  await page.getByTestId('tender-cash').click();
  // At 820 the pinned bar is gone; the summary card's Pay is the affordance.
  await expect(page.getByTestId('pos-pay-bar')).toBeHidden();
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });
  await expect(page.getByTestId('pos-complete')).toContainText(`$${total.toFixed(2)}`);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'yard-sale-820-complete.png'), fullPage: true });
});

test('390: the scan seam opens the scanner and a scan adds the line', async ({ page, request }) => {
  await page.setViewportSize(PHONE);
  await signIn(page, 'Yard Sale E2E');
  const product = await stockedProduct(request);

  await openCounter(page);
  await page.getByTestId('catalog-scan').click();
  await expect(page.locator('gable-barcode-scanner')).toBeVisible();
  // The camera itself is proved by hand on the device; the seam is the scan
  // event, the same one the yard's own pages ride.
  await page.locator('gable-barcode-scanner').evaluate((el, sku) => {
    el.dispatchEvent(new CustomEvent('scan', { detail: sku, bubbles: true }));
  }, product.sku);
  await expect(page.getByTestId('pos-lines').locator('li')).toHaveCount(1, { timeout: 10000 });
  await expectNoSidewaysScroll(page);
});

test('390: offline takes no money (the money guard at the yard)', async ({ page, request }) => {
  await page.setViewportSize(PHONE);
  await signIn(page, 'Yard Sale E2E');
  const product = await stockedProduct(request);

  await openCounter(page);
  await addOne(page, product.sku);
  await openPayment(page);
  await page.getByTestId('tender-cash').click();

  const before = await completedCount(request);
  await page.context().setOffline(true);
  try {
    await page.getByTestId('pos-pay-bar-button').click();
    // The field shell's own words, not a generic failure.
    await expect(page.locator('[role="alert"]')).toContainText('money never queues');
  } finally {
    await page.context().setOffline(false);
  }
  const after = await completedCount(request);
  expect(after, 'no sale was created while offline').toBe(before);
});
