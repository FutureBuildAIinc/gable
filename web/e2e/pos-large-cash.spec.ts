// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import { pickOptionByValue } from './app-controls';
import { openCounter } from './pos-till';
import fs from 'fs';

/**
 * The counter's large cash capture (CP-17a) against the real stack: a United
 * States branch's cash sale over 10,000 dollars asks for the payer before
 * Pay, is refused without the taxpayer identification number, and completes
 * with it; a Canadian branch with the dealer threshold off (the default)
 * asks nothing; the same branch with the threshold on asks and says plainly
 * that the capture is for the dealer's own records and is not a FINTRAC
 * report. The branch's walk-in identity floor rides the same counter: a
 * United States walk-in over the floor is asked who paid and the identity
 * stays on the till session, and a branch with the floor off says on the
 * screen that the walk-in's cash is not aggregated.
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

async function setCountryDefault(request: APIRequestContext, country: string) {
  const res = await request.put('/api/v1/admin/large-cash/settings', { data: { country_default: country } });
  expect(res.ok()).toBeTruthy();
}

async function setCAThreshold(request: APIRequestContext, enabled: boolean, cents = 1000000) {
  const res = await request.put('/api/v1/admin/large-cash/settings', {
    data: { country_default: 'CA', ca_enabled: enabled, ca_threshold_cents: cents },
  });
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
    // a price in this band keeps the quantity small: two sales of it fit on
    // the branch's stock, and the sale stays under the legal threshold
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

/** Rings one line of the product and raises its quantity to the given one. */
async function ringQuantity(page: Page, sku: string, qty: number) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  // the sale holds one line: the product that was searched for
  await expect(lines).toHaveCount(1, { timeout: 10000 });
  // the one unit's total, read before the quantity is raised: the wait below
  // holds the spec until the raised quantity has reached the summary, so a
  // slow counter cannot hand the tender step the one unit's total
  const oneUnit = (await page.getByTestId('summary-total').textContent()) ?? '';
  const quantity = lines.nth(0).getByRole('textbox', { name: /Quantity of/i });
  await quantity.fill(String(qty));
  await quantity.press('Enter');
  await expect(page.getByTestId('summary-total')).not.toContainText(oneUnit, { timeout: 10000 });
}

/** Adds a cash tender for the whole total through the Payment panel. */
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

test.afterEach(async ({ request }) => {
  // the dealer default country returns to the seeded state (Canada, threshold off) for the next spec
  await request.put('/api/v1/admin/large-cash/settings', { data: { country_default: 'CA', ca_enabled: false } });
});

test('a United States cash sale over the threshold asks for the payer, refuses without the taxpayer id, completes with it', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  const product = await stockedProduct(request);
  await setCountryDefault(request, 'US');

  await openCounter(page);
  await ringOverTenThousand(page, product.sku, product.qty);

  await payWholeInCash(page);
  await page.getByTestId('pos-pay').click();

  // the payer dialog asks before the sale completes
  const dialog = page.getByTestId('payer-dialog');
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId('payer-title')).toContainText("payer's details");
  await expect(dialog).not.toContainText('FINTRAC');
  await page.getByTestId('payer-name').fill('Riverbend Builders');
  await page.getByTestId('payer-address').fill('12 Mill Road, Kelowna');
  await page.getByTestId('payer-date_of_birth').fill('1984-05-06');
  await page.getByTestId('payer-occupation').fill('Contractor');
  await pickOptionByValue(page.getByTestId('payer-id_type'), 'drivers_license');
  await page.getByTestId('payer-id_number').fill('DL-00-1234567');
  // no taxpayer identification number yet: Save is held
  await expect(page.getByTestId('payer-save')).toBeDisabled();
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-large-cash-us-dialog.png'), fullPage: true });
  await page.getByTestId('payer-taxpayer_id').fill('00-1234567');
  await expect(page.getByTestId('payer-save')).toBeEnabled();
  await page.getByTestId('payer-save').click();

  await expect(page.getByTestId('pos-till-state')).toContainText('Till open', { timeout: 20000 });
  await expect(page.getByRole('heading', { name: /sale complete/i })).toBeVisible();
});

test('a Canadian branch with the dealer threshold off asks nothing', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  const product = await stockedProduct(request);
  await setCAThreshold(request, false);

  await openCounter(page);
  await ringOverTenThousand(page, product.sku, product.qty);
  await payWholeInCash(page);
  await page.getByTestId('pos-pay').click();

  await expect(page.getByTestId('payer-dialog')).toHaveCount(0);
  await expect(page.getByRole('heading', { name: /sale complete/i })).toBeVisible({ timeout: 20000 });
});

test('the Canadian dealer threshold on asks and says it is not a FINTRAC report', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  const product = await stockedProduct(request);
  await setCAThreshold(request, true);

  await openCounter(page);
  await ringOverTenThousand(page, product.sku, product.qty);
  await payWholeInCash(page);
  await page.getByTestId('pos-pay').click();

  const dialog = page.getByTestId('payer-dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText('It is not a FINTRAC report.');
  // Canada asks no taxpayer identification number
  await expect(page.getByTestId('payer-taxpayer_id')).toHaveCount(0);
  await page.getByTestId('payer-name').fill('Riverbend Builders');
  await page.getByTestId('payer-address').fill('12 Mill Road, Kelowna');
  await page.getByTestId('payer-date_of_birth').fill('1984-05-06');
  await page.getByTestId('payer-occupation').fill('Contractor');
  await pickOptionByValue(page.getByTestId('payer-id_type'), 'passport');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-large-cash-ca-dialog.png'), fullPage: true });
  await page.getByTestId('payer-save').click();

  await expect(page.getByRole('heading', { name: /sale complete/i })).toBeVisible({ timeout: 20000 });
});

test("a United States walk-in over the branch's identity floor is asked who paid, and the identity stays on the till session", async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  const product = await stockedProductOverFloor(request);
  await setCountryDefault(request, 'US');

  await openCounter(page);
  await ringQuantity(page, product.sku, product.qty);
  await payWholeInCashAtAnyAmount(page);
  await page.getByTestId('pos-pay').click();

  // the branch's floor asked who paid, in the floor's own words
  const dialog = page.getByTestId('payer-dialog');
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId('payer-title')).toContainText('Who is paying for this walk-in?');
  // a walk-in who declines is offered that way
  await expect(page.getByTestId('payer-decline')).toBeVisible();
  await page.getByTestId('payer-name').fill('Riverbend Builders');
  await page.getByTestId('payer-address').fill('12 Mill Road, Kelowna');
  await page.getByTestId('payer-date_of_birth').fill('1984-05-06');
  await page.getByTestId('payer-occupation').fill('Contractor');
  await pickOptionByValue(page.getByTestId('payer-id_type'), 'drivers_license');
  await page.getByTestId('payer-id_number').fill('DL-00-1234567');
  await page.getByTestId('payer-taxpayer_id').fill('00-1234567');
  await page.getByTestId('payer-save').click();
  await expect(page.getByRole('heading', { name: /sale complete/i })).toBeVisible({ timeout: 20000 });

  // the next sale in the same till session is not asked again: the identity
  // stays on the session, so the walk-in's cash sales are counted together
  await page.getByTestId('pos-header').getByRole('button', { name: 'New Sale' }).click();
  await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible();
  await ringQuantity(page, product.sku, product.qty);
  await payWholeInCashAtAnyAmount(page);
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('payer-dialog')).toHaveCount(0);
  await expect(page.getByRole('heading', { name: /sale complete/i })).toBeVisible({ timeout: 20000 });
});

test('with the floor off the counter says the walk-in cash is not aggregated', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  const product = await stockedProductOverFloor(request);
  // Canada: the floor is off by default
  await setCountryDefault(request, 'CA');

  await openCounter(page);
  await ringQuantity(page, product.sku, product.qty);
  await payWholeInCashAtAnyAmount(page);

  // the counter says on the screen that the walk-in's cash is not aggregated
  await expect(page.getByTestId('pos-walk-in-note')).toContainText('not aggregated');
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('payer-dialog')).toHaveCount(0);
  await expect(page.getByRole('heading', { name: /sale complete/i })).toBeVisible({ timeout: 20000 });
});
