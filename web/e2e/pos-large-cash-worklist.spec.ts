// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from './font-guard';
import path from 'path';
import { pickOptionByValue } from './app-controls';
import { openCounter } from './pos-till';
import fs from 'fs';

/**
 * The manager's large cash worklist (CP-17b) against the real stack: a United
 * States cash sale over 10,000 dollars lands on the worklist with the deadline
 * the filing is due, the manager records the filing they made in BSA E-Filing
 * and the row moves to filed, a walk-in over the branch's identity floor who
 * declined to identify shows as identity declined with no filing to make, and
 * the adviser's words are on every row.
 *
 * The catalogue the other specs share is left alone: this spec sells a
 * product of its own, stocked at the yard the counter actually opened, so the
 * yard other specs read is the yard this spec found.
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

/**
 * The yard the counter is at: the register it opened, and the stock row under
 * that yard. The counter picks its register by the yard the desk is working
 * in, so the spec reads the register off the screen and asks the API which
 * yard that register belongs to. The yard's own row is where the sale's stock
 * is held, so a product stocked there can be sold.
 */
async function counterYard(page: Page, request: APIRequestContext): Promise<string> {
  const registerId = ((await page.getByTestId('pos-register').textContent()) ?? '').trim();
  expect(registerId, 'the counter shows no register').not.toBe('');
  const registers = (await (await request.get('/api/v1/pos/registers')).json()) as {
    items: { id: string; branch_id: string }[];
  };
  const register = registers.items.find((r) => r.id === registerId);
  expect(register, `the counter holds no register ${registerId}`).toBeTruthy();
  const locations = (await (await request.get('/api/v1/locations?limit=200')).json()) as {
    items: { id: string; branch_id: string }[];
  };
  const yard = locations.items.find((l) => l.branch_id === register!.branch_id);
  expect(yard, 'the counter’s yard holds no stock row').toBeTruthy();
  return yard!.id;
}

/**
 * A product of this spec's own, at the given price, stocked at the counter's
 * yard in the quantity the sale needs. The stock the sale takes is this
 * spec's own, so the catalogue the other specs read is whole again.
 */
async function pricedProduct(
  request: APIRequestContext,
  locationId: string,
  sku: string,
  price: number,
  qty: number,
): Promise<string> {
  const created = await request.post('/api/v1/products', {
    data: {
      sku,
      description: `E2E large cash ${sku}`,
      stock_uom: 'PCS',
      base_price_ten_thousandths: Math.round(price * 10000),
    },
  });
  expect(created.status(), await created.text()).toBe(201);
  const product = (await created.json()) as { id: string };
  const stocked = await request.post('/api/v1/inventory/adjust', {
    data: {
      product_id: product.id,
      location_id: locationId,
      quantity: qty,
      reason: 'e2e stock for the large cash run',
    },
  });
  expect(stocked.status(), await stocked.text()).toBe(200);
  return sku;
}

/** Rings one line of the product and raises its quantity to the given one. */
async function ringQuantity(page: Page, sku: string, qty: number) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(1, { timeout: 10000 });
  // the one unit's total, read before the quantity is raised: the wait below
  // holds the spec until the raised quantity has reached the summary
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

/** A payer name and a product of this run alone, so the spec runs twice on one stack. */
function stamp(): string {
  return Date.now().toString(36).slice(-6).toUpperCase();
}

test.afterEach(async ({ request }) => {
  // the dealer default country returns to the seeded state (Canada, threshold off) for the next spec
  await request.put('/api/v1/admin/large-cash/settings', { data: { country_default: 'CA', ca_enabled: false } });
});

test('a United States cash sale over the threshold lands on the worklist with its deadline, and the manager records the filing', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  await openCounter(page);
  const yard = await counterYard(page, request);
  const sku = await pricedProduct(request, yard, `E2E-LC-${stamp()}`, 250, 45);
  await setCountryDefault(request, 'US');
  const payer = `Riverbend ${stamp()}`;

  await ringQuantity(page, sku, 45);
  await payWholeInCash(page);
  await page.getByTestId('pos-pay').click();

  const dialog = page.getByTestId('payer-dialog');
  await expect(dialog).toBeVisible();
  await page.getByTestId('payer-name').fill(payer);
  await page.getByTestId('payer-address').fill('12 Mill Road, Kelowna');
  await page.getByTestId('payer-date_of_birth').fill('1984-05-06');
  await page.getByTestId('payer-occupation').fill('Contractor');
  await pickOptionByValue(page.getByTestId('payer-id_type'), 'drivers_license');
  const taxpayer = `00-${Date.now().toString().slice(-7)}`; // this run's own payer, so the year's statement is not already issued by an earlier run
  await page.getByTestId('payer-id_number').fill(`DL-${taxpayer}`);
  await page.getByTestId('payer-taxpayer_id').fill(taxpayer);
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
  await expect(page.getByTestId('large-cash-status')).toContainText('is filed');
  await expect(row.getByTestId('large-cash-action')).toContainText('The filing is done');

  // and the worklist says so
  const after = await worklistRows(request);
  expect(after.find((r) => r.event_id === crossing!.event_id)?.filing_state).toBe('filed');

  // with the filing done the manager issues the customer statement and gets
  // the sheet to print and keep: the dealer as the sender, the payer as the
  // addressee, the year's cash and the sentence that says the IRS was told
  await row.getByTestId('large-cash-statement').click();
  const sheet = page.getByTestId('large-cash-statement-view');
  await expect(sheet).toBeVisible({ timeout: 10000 });
  await expect(sheet).toContainText('Statement of cash received');
  await expect(sheet).toContainText(payer);
  await expect(sheet.getByTestId('large-cash-statement-note')).toContainText('furnished to the Internal Revenue Service');
  await expect(sheet.getByTestId('large-cash-statement-print')).toBeVisible();
  await page.screenshot({ path: path.join(SHOTS_DIR, 'large-cash-statement.png'), fullPage: true });

  // and the year's list holds it, openable again
  const listed = page.getByTestId('large-cash-statements').locator('[data-testid="large-cash-statement-row"]', { hasText: payer });
  await expect(listed).toContainText('Issued');
  await sheet.getByTestId('large-cash-statement-close').click();
  await expect(sheet).toHaveCount(0);
  await listed.getByTestId('large-cash-statement-open').click();
  await expect(page.getByTestId('large-cash-statement-view')).toContainText(payer);

  // a second issue of the same statement is refused by the server
  const again = await request.post(`/api/v1/large-cash/events/${crossing!.event_id}/statement`, { data: {} });
  expect(again.status()).toBe(409);
});

test('a walk-in over the floor who declined to identify shows as identity declined, with no filing to make', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  await openCounter(page);
  const yard = await counterYard(page, request);
  const sku = await pricedProduct(request, yard, `E2E-LCW-${stamp()}`, 400, 8);
  await setCountryDefault(request, 'US');

  await ringQuantity(page, sku, 8);
  await payWholeInCashAtAnyAmount(page);
  await page.getByTestId('pos-pay').click();

  const dialog = page.getByTestId('payer-dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText('Who is paying for this walk-in?');
  await page.getByTestId('payer-decline').click();
  await expect(page.getByRole('heading', { name: /sale complete/i })).toBeVisible({ timeout: 20000 });

  const rows = await worklistRows(request);
  const declined = rows.find((r) => r.event_state === 'identity declined');
  expect(declined, 'the declined walk-in is not on the worklist').toBeTruthy();

  await page.goto('/pos/large-cash');
  const row = page.locator(`[data-event-id="${declined!.event_id}"]`);
  await expect(row).toBeVisible({ timeout: 20000 });
  // and there is no filing to make
  await expect(row.getByTestId('large-cash-file')).toHaveCount(0);
  await expect(row.getByTestId('large-cash-action')).toContainText('A manager looks at this one');
});

async function setCountryDefault(request: APIRequestContext, country: string) {
  const res = await request.put('/api/v1/admin/large-cash/settings', { data: { country_default: country } });
  expect(res.ok()).toBeTruthy();
}
