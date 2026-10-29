// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import { pickOptionByValue } from './app-controls';
import fs from 'fs';

/**
 * The manager's large cash worklist (CP-17b) against the real stack: a United
 * States cash sale over 10,000 dollars lands on the worklist with the deadline
 * the filing is due, the manager records the filing they made in BSA E-Filing
 * and the row moves to filed, a walk-in over the branch's identity floor who
 * declined to identify shows as identity declined with no filing to make, and
 * the adviser's words are on every row.
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

/** A product whose branch stock can cover a sale over 10,000 dollars. */
async function stockedProduct(request: APIRequestContext): Promise<{ id: string; sku: string; qty: number }> {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as {
    items: { id: string; sku: string; base_price_ten_thousandths: number }[];
  };
  const list = products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse();
  for (const p of list) {
    const price = p.base_price_ten_thousandths / 10000;
    if (price <= 0) continue;
    const qty = Math.ceil(11000 / price) + 2;
    if (qty > 5000) continue;
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as {
      items: { location_name: string; available: string }[];
    };
    const atBranch = page.items
      .filter((r) => r.location_name === 'KEL-MAIN')
      .reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (atBranch >= qty) return { id: p.id, sku: p.sku, qty };
  }
  throw new Error('no stocked product for the large cash run');
}

/**
 * A product whose branch stock can cover a cash sale over 3,000 dollars and
 * under 10,000: the walk-in's floor asks there, and the legal threshold does
 * not.
 */
async function stockedProductOverFloor(request: APIRequestContext): Promise<{ id: string; sku: string; qty: number }> {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as {
    items: { id: string; sku: string; base_price_ten_thousandths: number }[];
  };
  const list = products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse();
  for (const p of list) {
    const price = p.base_price_ten_thousandths / 10000;
    if (price < 50 || price > 500) continue;
    const qty = Math.ceil(3200 / price);
    if (qty * price * 1.08875 >= 9000) continue;
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as {
      items: { location_name: string; available: string }[];
    };
    const atBranch = page.items
      .filter((r) => r.location_name === 'KEL-MAIN')
      .reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (atBranch >= qty * 2) return { id: p.id, sku: p.sku, qty };
  }
  throw new Error('no stocked product for the walk-in floor run');
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

async function setCountryDefault(request: APIRequestContext, country: string) {
  const res = await request.put('/api/v1/admin/large-cash/settings', { data: { country_default: country } });
  expect(res.ok()).toBeTruthy();
}

/** Rings one line of the product and raises its quantity until the sale is over 10,000 dollars. */
async function ringOverTenThousand(page: Page, sku: string, qty: number) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
  const quantity = lines.nth(0).getByRole('textbox', { name: /Quantity of/i });
  await quantity.fill(String(qty));
  await quantity.press('Enter');
  await expect(page.getByTestId('summary-total')).toContainText(/[1-9][0-9],?[0-9]{3}/);
}

/** Rings one line of the product and raises its quantity to the given one. */
async function ringQuantity(page: Page, sku: string, qty: number) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(1, { timeout: 10000 });
  const oneUnit = (await page.getByTestId('summary-total').textContent()) ?? '';
  const quantity = lines.nth(0).getByRole('textbox', { name: /Quantity of/i });
  await quantity.fill(String(qty));
  await quantity.press('Enter');
  await expect(page.getByTestId('summary-total')).not.toContainText(oneUnit, { timeout: 10000 });
}

/** Adds a cash tender for the whole total through the Payment panel. */
async function payWholeInCash(page: Page) {
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await page.getByTestId('tender-cash').click();
  const text = (await page.getByTestId('summary-total').textContent()) ?? '';
  const total = Number(text.replace(/[$,\s]/g, ''));
  expect(total).toBeGreaterThan(10000);
  await page.getByLabel('Tender amount').fill(total.toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
  return total;
}

/** The same, for a sale between the branch's floor and the legal threshold. */
async function payWholeInCashAtAnyAmount(page: Page) {
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await page.getByTestId('tender-cash').click();
  const text = (await page.getByTestId('summary-total').textContent()) ?? '';
  const total = Number(text.replace(/[$,\s]/g, ''));
  expect(total).toBeGreaterThan(3000);
  await page.getByLabel('Tender amount').fill(total.toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
  return total;
}

/** One row of the manager's worklist, as the API holds it. */
interface WorklistRow {
  event_id: string;
  event_state: string;
  payer_name: string;
  filing_state: string;
}

async function worklistRows(request: APIRequestContext): Promise<WorklistRow[]> {
  const res = await request.get('/api/v1/large-cash/worklist');
  expect(res.ok()).toBeTruthy();
  const body = (await res.json()) as { items?: WorklistRow[] };
  return body.items ?? [];
}

/** A payer name of this run alone, so the spec runs twice on one stack. */
function stamp(): string {
  return Date.now().toString(36).slice(-6);
}

test.afterEach(async ({ request }) => {
  // the dealer default country returns to the seeded state (Canada, threshold off) for the next spec
  await request.put('/api/v1/admin/large-cash/settings', { data: { country_default: 'CA', ca_enabled: false } });
});

test('a United States cash sale over the threshold lands on the worklist with its deadline, and the manager records the filing', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  const product = await stockedProduct(request);
  await setCountryDefault(request, 'US');
  const payer = `Riverbend ${stamp()}`;

  await openCounter(page);
  await ringOverTenThousand(page, product.sku, product.qty);
  await payWholeInCash(page);
  await page.getByTestId('pos-pay').click();

  const dialog = page.getByTestId('payer-dialog');
  await expect(dialog).toBeVisible();
  await page.getByTestId('payer-name').fill(payer);
  await page.getByTestId('payer-address').fill('12 Mill Road, Kelowna');
  await page.getByTestId('payer-date_of_birth').fill('1984-05-06');
  await page.getByTestId('payer-occupation').fill('Contractor');
  await pickOptionByValue(page.getByTestId('payer-id_type'), 'drivers_license');
  await page.getByTestId('payer-id_number').fill('DL-00-1234567');
  await page.getByTestId('payer-taxpayer_id').fill('00-1234567');
  await page.getByTestId('payer-save').click();
  await expect(page.getByRole('heading', { name: /sale complete/i })).toBeVisible({ timeout: 20000 });

  // the crossing is on the manager's worklist
  const rows = await worklistRows(request);
  const crossing = rows.find((r) => r.payer_name === payer);
  expect(crossing, 'the crossing is not on the worklist').toBeTruthy();
  expect(crossing!.filing_state).toBe('reportable');

  await page.goto('/pos/large-cash');
  const row = page.locator(`[data-event-id="${crossing!.event_id}"]`);
  await expect(row).toBeVisible({ timeout: 20000 });
  await expect(row.getByTestId('large-cash-deadline')).toContainText('The filing is due');
  await expect(row.getByTestId('large-cash-adviser')).toContainText('confirms each filing with its own adviser');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'large-cash-worklist.png'), fullPage: true });

  // the manager records the filing they made in BSA E-Filing
  await row.locator(`#lc-ack-${crossing!.event_id}`).fill('31000000000001');
  await row.getByTestId('large-cash-file').click();
  await expect(page.getByTestId('large-cash-note')).toContainText('is filed');
  await expect(row.getByTestId('large-cash-action')).toContainText('The filing is done');

  // and the worklist says so
  const after = await worklistRows(request);
  expect(after.find((r) => r.event_id === crossing!.event_id)?.filing_state).toBe('filed');
});

test('a walk-in over the floor who declined to identify shows as identity declined, with no filing to make', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  const product = await stockedProductOverFloor(request);
  await setCountryDefault(request, 'US');

  await openCounter(page);
  await ringQuantity(page, product.sku, product.qty);
  await payWholeInCashAtAnyAmount(page);
  await page.getByTestId('pos-pay').click();

  const dialog = page.getByTestId('payer-dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText('Who is paying for this walk-in?');
  await page.getByTestId('payer-decline').click();
  await expect(page.getByRole('heading', { name: /sale complete/i })).toBeVisible({ timeout: 20000 });

  const rows = await worklistRows(request);
  const declined = rows.find((r) => r.event_state === 'identity declined');
  expect(declined, 'the declined crossing is not on the worklist').toBeTruthy();

  await page.goto('/pos/large-cash');
  const row = page.locator(`[data-event-id="${declined!.event_id}"]`);
  await expect(row).toBeVisible({ timeout: 20000 });
  await expect(row.getByTestId('large-cash-note')).toContainText('Identity declined, manager review.');
  // and there is no filing to make
  await expect(row.getByTestId('large-cash-file')).toHaveCount(0);
  await expect(row.getByTestId('large-cash-action')).toContainText('A manager looks at this one');
});
