// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * A credit hold the AR core placed stops a charge sale at the counter
 * (CP-11): a manual hold through the API, the counter shows the held
 * account in its own words with the charge tender off, the sale still pays
 * in cash, and once AR releases the hold the charge completes.
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

/** A product with stock at the branch the counter issues from, as pos.spec.ts finds one. */
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

/** Attaches the account through the customer search. */
async function attach(page: Page, account: { name: string; account_number: string }) {
  await page.locator('#pos-customer-search').fill(account.account_number);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(account.account_number) }).click();
  await expect(page.getByTestId('pos-customer-name')).toHaveText(account.name);
}

test('the counter: a credit hold AR placed stops the charge, cash still pays, the release allows it', async ({ page, request }) => {
  const run = Date.now().toString(36).toUpperCase();
  const made = await request.post('/api/v1/customers', {
    data: { account_number: `E2E-CP11-${run}`, name: `E2E Credit Hold ${run}`, credit_limit_cents: 5000000 },
  });
  expect(made.status(), await made.text()).toBe(201);
  const account = (await made.json()) as { id: string; name: string; account_number: string };

  // AR places a manual hold on the account through the API.
  const placed = await request.post('/api/v1/ar/credit-holds', {
    data: { customer_id: account.id, reason: 'past due 60 days' },
  });
  expect(placed.status(), await placed.text()).toBe(201);
  const hold = (await placed.json()) as { id: string };

  await signIn(page, 'POS Credit Hold E2E');
  const product = await stockedProduct(request);
  await openCounter(page);

  // The held account shows in its own words with the hold's reason, and the
  // charge tender is off with the ways forward offered.
  await attach(page, account);
  await expect(page.getByTestId('pos-customer-standing')).toHaveText('On credit hold');
  await expect(page.getByTestId('pos-verdict-line')).toContainText('credit hold: past due 60 days');
  await addOne(page, product.sku);
  await page.getByTestId('pos-next-payment').click();
  await openPayment(page);
  const charge = page.getByRole('button', { name: 'Charge to account' });
  await expect(charge).toBeDisabled();
  await expect(page.getByTestId('pos-account-refusal')).toContainText('past due 60 days');
  await expect(page.getByTestId('pos-refusal-way-switch_tender')).toBeVisible();
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-credit-hold.png'), fullPage: true });

  // The sale still pays in cash and completes.
  await page.getByTestId('tender-cash').click();
  await page.getByLabel('Tender amount').fill('100.00');
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });
  await expect(page.getByTestId('pos-complete')).toContainText(/POS-\d+/);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-credit-hold-cash.png'), fullPage: true });

  // AR releases the hold through the API; a new sale for the same account
  // charges and completes.
  const released = await request.post(`/api/v1/ar/credit-holds/${hold.id}/release`, { data: { reason: 'paid up' } });
  expect(released.status(), await released.text()).toBe(200);
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await expect(page.getByTestId('pos-no-lines')).toBeVisible();
  await expect(page.getByTestId('pos-customer')).toContainText('Walk-in sale');
  await attach(page, account);
  await expect(page.getByTestId('pos-customer-standing')).toContainText('Credit available');
  await addOne(page, product.sku);
  await page.getByTestId('pos-next-payment').click();
  await openPayment(page);
  await expect(page.getByRole('button', { name: 'Charge to account' })).toBeEnabled();
  await page.getByRole('button', { name: 'Charge to account' }).click();
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-credit-hold-released.png'), fullPage: true });

  // The books: the charge sale carries an open invoice on the account (the
  // list's statuses are the lowercase lifecycle vocabulary).
  const invoices = (await (await request.get(`/api/v1/invoices?customer_id=${account.id}`)).json()) as {
    items: { status: string; customer_id: string }[];
  };
  expect(invoices.items.some((i) => i.status === 'unpaid' || i.status === 'partial')).toBe(true);

  // Clean the run's account off the seeded books the way the counter's void
  // does, so a rerun of the stack finds the seed as it was.
  const sales = (await (await request.get(`/api/v1/pos/transactions?register_id=REG-01&limit=50`)).json()) as {
    items: { id: string; customer_id: string | null; status: string }[];
  };
  for (const s of sales.items.filter((x) => x.customer_id === account.id && x.status === 'completed')) {
    const full = (await (await request.get(`/api/v1/pos/transactions/${s.id}`)).json()) as { revision: number };
    const voided = await request.post(`/api/v1/pos/transactions/${s.id}/void`,
      { data: { reason: 'e2e cleanup', revision: full.revision } });
    expect(voided.status(), await voided.text()).toBe(200);
  }
});
