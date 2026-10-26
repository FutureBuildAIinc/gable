// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { answerAppDialog } from './app-controls';
import path from 'path';
import fs from 'fs';

/**
 * CP-10 at the counter, end to end: a certificate is recorded and ended on
 * the account page in the UI, the counter's summary says Tax exempt with the
 * certificate number and the untaxed total, a charge sale to the exempt
 * account completes, an ended or expired certificate brings the tax back.
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

/** A product with stock at the counter's branch (the seeded Kelowna yard). */
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

async function makeAccount(request: APIRequestContext, tag: string, extra: Record<string, unknown>) {
  const run = Date.now().toString(36).toUpperCase();
  const res = await request.post('/api/v1/customers', {
    data: { account_number: `E2E-TEX-${tag}-${run}`, name: `E2E Tax Exempt ${tag} ${run}`, ...extra },
  });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as { id: string; name: string; account_number: string };
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

/** Names the account on the open sale through the customer panel. */
async function nameAccount(page: Page, account: { name: string; account_number: string }) {
  const search = page.locator('#pos-customer-search');
  await search.fill(account.account_number);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(account.account_number) }).click();
  await expect(page.getByTestId('pos-customer-name')).toHaveText(account.name);
}

/** Adds a certificate on the account page, in the UI. */
async function addCertificate(
  page: Page,
  customerId: string,
  number: string,
  effective: string,
  expiry: string,
) {
  await page.goto(`/accounts/${customerId}`);
  await page.getByRole('button', { name: 'Tax Exemption' }).click();
  await expect(page.getByTestId('tax-exemption-panel')).toBeVisible();
  await page.getByTestId('tax-exemption-add').click();
  const dialog = page.getByTestId('tax-exemption-dialog');
  await expect(dialog).toBeVisible();
  await dialog.locator('#tax-exempt-number').fill(number);
  await dialog.locator('#tax-exempt-reason').fill('RESALE');
  await dialog.locator('#tax-exempt-state').fill('CT');
  await dialog.locator('#tax-exempt-effective').fill(effective);
  if (expiry) await dialog.locator('#tax-exempt-expiry').fill(expiry);
  await page.getByTestId('tax-exemption-save').click();
  await expect(page.getByTestId('tax-exemption-row')).toContainText(number, { timeout: 10000 });
}

test('the counter honours a tax exemption end to end', async ({ page, request }) => {
  await signIn(page, 'POS Tax E2E');
  const product = await stockedProduct(request);
  const account = await makeAccount(request, 'OK', { credit_limit_cents: 5000000 });
  const cert = `CERT-E2E-${Date.now().toString(36).toUpperCase()}`;
  const today = new Date().toISOString().slice(0, 10);

  // The certificate is recorded on the account page, in the UI.
  await addCertificate(page, account.id, cert, today, '');
  await expect(page.getByTestId('tax-exemption-status')).toHaveText('Active');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-tax-exempt-account.png'), fullPage: true });

  // At the counter: ring lines, name the exempt account, see the untaxed
  // total and the certificate the exemption stands on.
  await openCounter(page);
  await addOne(page, product.sku);
  await addOne(page, product.sku);
  const subtotal = Number(
    ((await page.getByTestId('summary-subtotal').textContent()) ?? '').replace(/[$,\s]/g, ''),
  );
  expect(subtotal).toBeGreaterThan(0);
  const taxed = Number(
    ((await page.getByTestId('summary-tax').textContent()) ?? '').replace(/[$,\s]/g, ''),
  );
  expect(taxed).toBeGreaterThan(0);
  await nameAccount(page, account);
  await expect(page.getByTestId('summary-tax-exempt')).toContainText('Tax exempt');
  await expect(page.getByTestId('summary-tax-exempt')).toContainText(cert);
  await expect(page.getByTestId('summary-tax')).toHaveCount(0);
  await expect(page.getByTestId('summary-total')).toContainText(`$${subtotal.toFixed(2)}`);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-tax-exempt-summary.png'), fullPage: true });

  // The charge sale to the exempt account completes at the untaxed total.
  await page.getByTestId('pos-next-payment').click();
  await page.getByRole('button', { name: 'Charge to account' }).click();
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-tax-exempt-charged.png'), fullPage: true });

  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { id: string; customer_id: string | null; status: string }[];
  };
  const sale = sales.items.find((s) => s.customer_id === account.id && s.status === 'completed');
  expect(sale, 'the exempt charge sale completed').toBeTruthy();
  const full = (await (await request.get(`/api/v1/pos/transactions/${sale!.id}`)).json()) as {
    tax_cents: number; total_cents: number; tax_exemption: { certificate_number: string } | null;
  };
  expect(full.tax_cents).toBe(0);
  expect(full.total_cents).toBe(Math.round(subtotal * 100));
  expect(full.tax_exemption?.certificate_number).toBe(cert);

  // The certificate ends in the UI, and the tax returns on a new sale.
  await page.goto(`/accounts/${account.id}`);
  await page.getByRole('button', { name: 'Tax Exemption' }).click();
  await page.getByTestId('tax-exemption-end').click();
  await answerAppDialog(page, 'End certificate');
  await expect(page.getByTestId('tax-exemption-status')).toHaveText('Ended', { timeout: 10000 });
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-tax-exempt-ended.png'), fullPage: true });

  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await expect(page.getByTestId('pos-no-lines')).toBeVisible();
  await addOne(page, product.sku);
  await nameAccount(page, account);
  await expect(page.getByTestId('summary-tax-exempt')).toHaveCount(0);
  await expect(page.getByTestId('summary-tax')).toContainText('$');

  // An expired certificate exempts nothing: the tax stays on.
  const yesterday = new Date(Date.now() - 24 * 3600 * 1000 * 2).toISOString().slice(0, 10);
  const old = new Date(Date.now() - 24 * 3600 * 1000 * 30).toISOString().slice(0, 10);
  await addCertificate(page, account.id, `CERT-OLD-${Date.now().toString(36).toUpperCase()}`, old, yesterday);
  await expect(page.getByTestId('tax-exemption-status')).toHaveText('Expired');
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await expect(page.getByTestId('pos-no-lines')).toBeVisible();
  await addOne(page, product.sku);
  await nameAccount(page, account);
  await expect(page.getByTestId('summary-tax-exempt')).toHaveCount(0);
  const taxBack = Number(
    ((await page.getByTestId('summary-tax').textContent()) ?? '').replace(/[$,\s]/g, ''),
  );
  expect(taxBack).toBeGreaterThan(0);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-tax-exempt-expired.png'), fullPage: true });
});
