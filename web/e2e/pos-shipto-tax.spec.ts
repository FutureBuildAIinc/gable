// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { pickOptionByValue } from './app-controls';
import { openCounter } from './pos-till';
import path from 'path';
import fs from 'fs';

/**
 * Tax by ship to at the counter (CP-10): the dealer keeps a zone for a
 * postal area in the admin screen's tables, the cashier rings a delivered
 * counter sale to a customer's ship to in that area, and the tax on the
 * sale comes from the zone (the invoice carrying the delivery), while a
 * plain pickup keeps the branch rate.
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

/** A product with stock at the counter's branch. */
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

test('the counter: a delivered sale is taxed at the ship to zone, a pickup at the branch', async ({ page, request }) => {
  await signIn(page, 'POS ShipTo E2E');

  // The dealer's zone: a postal area at 5 percent (3.5 state + 1.5 city),
  // kept in Gable's own tables through the zone admin route.
  const run = Date.now().toString(36).toUpperCase();
  const zoneRes = await request.post('/api/v1/tax/zones', {
    data: {
      country: 'US',
      jurisdiction_code: `E2E-ZONE-${run}`,
      jurisdiction_type: 'CTY',
      jurisdiction_name: 'E2E Ship To County',
      zone_kind: 'POSTAL',
      sourcing: 'DESTINATION',
      effective_from: '2020-01-01',
      data_source: 'e2e',
      rates: [
        { name: 'STATE', rate: '0.035', effective_from: '2020-01-01' },
        { name: 'CITY', rate: '0.015', effective_from: '2020-01-01' },
      ],
      boundaries: [{ country: 'US', postal_code_low: '99111', postal_code_high: '99111' }],
    },
  });
  expect(zoneRes.status(), await zoneRes.text()).toBe(201);

  // The customer and the ship to inside that postal area.
  const custRes = await request.post('/api/v1/customers', {
    data: { account_number: `E2E-SHIPTO-${run}`, name: `E2E Ship To ${run}` },
  });
  expect(custRes.status(), await custRes.text()).toBe(201);
  const customer = (await custRes.json()) as { id: string; name: string; account_number: string };
  const shipRes = await request.post(`/api/v1/customers/${customer.id}/ship-tos`, {
    data: {
      code: 'SITE',
      name: 'The site',
      line1: '1 Zone Way',
      city: 'E2Eville',
      region: 'WA',
      postal_code: '99111',
      country: 'US',
    },
  });
  expect(shipRes.status(), await shipRes.text()).toBe(201);
  const shipTo = (await shipRes.json()) as { id: string };

  // The admin screen shows the zone the dealer just kept.
  await page.goto('/admin/tax-zones');
  await expect(page.getByRole('heading', { name: 'Tax Zones' })).toBeVisible();
  await expect(page.locator('table')).toContainText('E2E Ship To County');
  await expect(page.locator('table')).toContainText('5%');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-shipto-zone-admin.png'), fullPage: true });

  await openCounter(page);
  const product = await stockedProduct(request);

  // First a pickup: the customer named, one unit, paid in cash. Its tax is
  // the branch's rate, whatever the dealer's zone says.
  await page.locator('#pos-customer-search').fill(customer.name);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(customer.account_number) }).click();
  await expect(page.getByTestId('pos-customer-name')).toHaveText(customer.name);
  const lines = page.locator('[data-testid="pos-lines"] li');
  await page.getByTestId('catalog-search').fill(product.sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(1, { timeout: 10000 });
  await page.getByTestId('pos-next-payment').click();
  const pickupTotalText = (await page.getByTestId('summary-total').textContent()) ?? '';
  const pickupTotal = Number(pickupTotalText.replace(/[$,\s]/g, ''));
  await page.getByTestId('tender-cash').click();
  await page.getByLabel('Tender amount').fill(pickupTotal.toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });

  // Then the delivered sale: the same customer, the ship to named in the
  // Deliver to picker, one unit, paid in cash.
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await expect(page.getByTestId('pos-no-lines')).toBeVisible();
  await page.locator('#pos-customer-search').fill(customer.name);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(customer.account_number) }).click();
  await expect(page.getByTestId('pos-customer-name')).toHaveText(customer.name);
  // The picker names the ship to and says what it means for the tax.
  const deliverTo = page.getByTestId('pos-deliver-to');
  await expect(deliverTo).toBeVisible();
  await pickOptionByValue(deliverTo, shipTo.id);
  await expect(deliverTo).toContainText('99111');
  await page.getByTestId('catalog-search').fill(product.sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(1, { timeout: 10000 });
  await page.getByTestId('pos-next-payment').click();
  const deliveredTotalText = (await page.getByTestId('summary-total').textContent()) ?? '';
  const deliveredTotal = Number(deliveredTotalText.replace(/[$,\s]/g, ''));
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-shipto-delivery.png'), fullPage: true });
  await page.getByTestId('tender-cash').click();
  await page.getByLabel('Tender amount').fill(deliveredTotal.toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-shipto-paid.png'), fullPage: true });

  // The books: the delivered sale's invoice carries the delivery and its
  // tax is the zone's 5 percent of the subtotal, not the branch rate, and
  // the pickup's invoice stays a pickup.
  const sales = (await (await request.get(`/api/v1/pos/transactions?register_id=REG-01&limit=50`)).json()) as {
    items: { id: string; customer_id: string | null; status: string; invoice_id: string | null; total_cents: number }[];
  };
  const mine = sales.items.filter((s) => s.customer_id === customer.id && s.status === 'completed');
  expect(mine.length).toBe(2);
  const delivered = mine[0];
  const pickup = mine[1];
  const readInvoice = async (id: string | null) =>
    (await (await request.get(`/api/v1/invoices/${id}`)).json()) as {
      delivery_type: string;
      subtotal_cents: number;
      tax_cents: number;
    };
  const deliveredInvoice = await readInvoice(delivered.invoice_id);
  expect(deliveredInvoice.delivery_type).toBe('delivery');
  // 5 percent of the subtotal, rounded half away from zero.
  expect(deliveredInvoice.tax_cents).toBe(Math.round(deliveredInvoice.subtotal_cents * 0.05));
  const pickupInvoice = await readInvoice(pickup.invoice_id);
  expect(pickupInvoice.delivery_type).toBe('pickup');
  expect(pickupInvoice.tax_cents).not.toBe(Math.round(pickupInvoice.subtotal_cents * 0.05));

  // Close the zone: sales before today keep its rate.
  const zone = (await zoneRes.json()) as { id: string };
  const closeRes = await request.post(`/api/v1/tax/zones/${zone.id}/close`, { data: { effective_to: '2026-12-31' } });
  expect(closeRes.status(), await closeRes.text()).toBe(200);
});
