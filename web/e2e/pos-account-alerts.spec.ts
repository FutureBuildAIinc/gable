// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { answerAppDialog, pickOptionByValue } from './app-controls';
import path from 'path';
import fs from 'fs';

/**
 * The counter and the customer's alerts (CP-11) against the real stack: the
 * office leaves a customer a note and a warning, the counter reads the note in
 * the customer panel, the warning that asks to be acknowledged stops the sale
 * until the clerk says so, and an ended warning stops asking.
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

/** An account with credit, so a charge on it is allowed. */
async function makeAccount(request: APIRequestContext, tag: string) {
  const run = Date.now().toString(36).toUpperCase();
  const res = await request.post('/api/v1/customers', {
    data: { account_number: `E2E-ALERT-${tag}-${run}`, name: `E2E Alerts ${tag} ${run}`, credit_limit_cents: 5000000 },
  });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as { id: string; name: string; account_number: string };
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
    const atBranch = page.items
      .filter((r) => r.location_name === 'KEL-MAIN')
      .reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (atBranch >= 50) return p;
  }
  throw new Error('no stocked product for the counter run');
}

/** Opens the counter (the Panels board) and the drawer when none is open. */
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

/** Adds one unit of the product through the Items card. */
async function addOne(page: Page, sku: string) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
}

/** Names the customer on the open sale, the way the counter does. */
async function attachCustomer(page: Page, account: { name: string; account_number: string }) {
  await page.locator('#pos-customer-search').fill(account.name);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(account.account_number) }).click();
  await expect(page.getByTestId('pos-customer-name')).toContainText(account.name);
}

/** Opens the Payment panel (one panel is open at a time; its tenders are in it). */
async function openPayment(page: Page) {
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
}

test('the counter: the customer alerts are read at the counter, and a warning stops the sale until it is acknowledged', async ({ page, request }) => {
  const account = await makeAccount(request, 'POS');
  await signIn(page, 'POS Account Alerts E2E');
  const product = await stockedProduct(request);

  // The office leaves the account a note and a warning on the account page.
  await page.goto(`/accounts/${account.id}`);
  await page.getByRole('button', { name: 'Alerts', exact: true }).click();
  await expect(page.getByTestId('alert-empty')).toBeVisible();

  await page.getByTestId('alert-add').click();
  await page.getByLabel('Alert', { exact: true }).fill('Call before the delivery');
  await page.getByTestId('alert-save').click();
  await expect(page.getByTestId('alert-row')).toHaveCount(1);
  await expect(page.getByTestId('alert-text')).toContainText('Call before the delivery');

  await page.getByTestId('alert-add').click();
  await pickOptionByValue(page.getByTestId('alert-form').locator('gable-select'), 'warning');
  await page.getByLabel('Alert', { exact: true }).fill('Check the site before the delivery');
  await page.getByLabel('The counter must acknowledge it before the sale').check();
  await page.getByTestId('alert-save').click();
  await expect(page.getByTestId('alert-row')).toHaveCount(2);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'customer-alerts.png'), fullPage: true });

  // The counter: the customer named, the note in the panel and the warning
  // as a banner.
  await openCounter(page);
  await attachCustomer(page, account);
  await expect(page.getByTestId('pos-alert-note')).toContainText('Call before the delivery');
  await expect(page.getByTestId('pos-alert-warning')).toContainText('Check the site before the delivery');

  await addOne(page, product.sku);
  await openPayment(page);
  await page.getByTestId('tender-cash').click();
  const total = Number(((await page.getByTestId('summary-total').textContent()) ?? '0').replace(/[$,\s]/g, ''));
  await page.getByLabel('Tender amount').fill(total.toFixed(2));

  // Pay asks the clerk to read the warning first, and takes no sale until
  // they say so.
  await page.getByTestId('pos-pay').click();
  const dialog = page.locator('gable-app-dialog [aria-modal="true"]');
  await expect(dialog).toContainText('Check the site before the delivery');
  await expect(dialog).toContainText('I have read this');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-alert-acknowledgement.png'), fullPage: true });

  const completed = page.waitForResponse(
    (r) => new URL(r.url()).pathname.startsWith('/api/v1/pos/transactions/') && new URL(r.url()).pathname.endsWith('/complete'),
  );
  await answerAppDialog(page, 'I have read this');
  const completedRes = await completed;
  expect(completedRes.status(), await completedRes.text()).toBe(200);
  // The acknowledgement goes on the sale with it.
  expect(completedRes.request().postDataJSON().acknowledged_alert_ids).toHaveLength(1);
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });

  // The office ends the warning: the counter stops asking.
  await page.goto(`/accounts/${account.id}`);
  await page.getByRole('button', { name: 'Alerts', exact: true }).click();
  const warningRow = page.locator('[data-testid="alert-row"][data-alert-kind="warning"]');
  await warningRow.getByTestId('alert-end').click();
  await answerAppDialog(page, 'End alert');
  // The ended alert leaves the open list, and it is still on the account.
  await expect(page.locator('[data-testid="alert-row"][data-alert-kind="warning"]')).toHaveCount(0);
  await page.getByTestId('alert-show-ended').check();
  await expect(warningRow.getByTestId('alert-ended')).toBeVisible();

  // A new sale with the same customer: the warning is ended, so no dialog.
  await openCounter(page);
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await expect(page.getByTestId('pos-no-lines')).toBeVisible();
  await attachCustomer(page, account);
  await expect(page.getByTestId('pos-alert-note')).toContainText('Call before the delivery');
  await expect(page.getByTestId('pos-alert-warning')).toHaveCount(0);
  await addOne(page, product.sku);
  await openPayment(page);
  await page.getByTestId('tender-cash').click();
  await page.getByLabel('Tender amount').fill(total.toFixed(2));
  const second = page.waitForResponse(
    (r) => new URL(r.url()).pathname.startsWith('/api/v1/pos/transactions/') && new URL(r.url()).pathname.endsWith('/complete'),
  );
  await page.getByTestId('pos-pay').click();
  const secondRes = await second;
  expect(secondRes.status(), await secondRes.text()).toBe(200);
  expect(secondRes.request().postDataJSON().acknowledged_alert_ids).toEqual([]);
});
