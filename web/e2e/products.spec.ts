// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * The desk's product flow against the real stack (see playwright.config.ts), on the converted
 * product contract (C3-1): stock_uom and the base price in ten thousandths, quantities as decimal
 * strings, a revision every write moves, the cursor list, and the PIM detail with the product nested.
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

// Unique per run, so a database that already holds earlier runs still gives a clean list. The E2E-
// prefix is what the quote spec skips when it picks a seeded product.
const RUN = Date.now().toString(36).toUpperCase();
let counter = 0;
function unique(): { sku: string; description: string } {
  counter += 1;
  return { sku: `E2E-${RUN}-${counter}`, description: `E2E Product ${RUN} ${counter}` };
}

interface ProductWire {
  id: string;
  sku: string;
  stock_uom: string;
  base_price_ten_thousandths: number;
  target_margin: number;
  commission_rate: number;
  on_hand: string;
  allocated: string;
  available: string;
  reorder_point: string;
  revision: number;
}

test.describe('Product flow on the new contract', () => {
  test('create in the UI, see it listed, open its detail, edit margins at the loaded revision, and a stale save reloads', async ({ page, request }) => {
    const u = unique();
    await signIn(page, 'Playwright Products');

    // Create through the Add Product modal: a unit and a price typed in dollars.
    await page.goto('/inventory');
    await expect(page.getByRole('heading', { name: 'The Pile' })).toBeVisible();
    await page.getByRole('button', { name: 'Add Product' }).click();
    await expect(page.getByRole('heading', { name: 'Add Product to Pile' })).toBeVisible();
    await page.getByPlaceholder('e.g. 2x4x8-SPF').fill(u.sku);
    await page.getByPlaceholder('e.g. 2x4x8 SPF Premium Stud').fill(u.description);
    await page.locator('gable-add-product-modal select').first().selectOption('BOX');
    await page.locator('gable-add-product-modal input[type="number"]').fill('4.25');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'product-create-form.png') });

    const created = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/products' && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Create Product' }).click();
    const createdRes = await created;
    expect(createdRes.status(), await createdRes.text()).toBe(201);
    const sent = createdRes.request().postDataJSON();
    expect(sent).toEqual({ sku: u.sku, description: u.description, stock_uom: 'BOX', base_price_ten_thousandths: 42500 });
    for (const old of ['uom_primary', 'base_price']) expect(sent).not.toHaveProperty(old);
    const product = (await createdRes.json()) as ProductWire;
    expect(product.stock_uom).toBe('BOX');
    expect(product.base_price_ten_thousandths).toBe(42500);
    expect(product.revision).toBe(1);
    // Quantities are decimal strings in the stocking unit, never numbers.
    for (const q of [product.on_hand, product.allocated, product.available, product.reorder_point]) expect(typeof q).toBe('string');

    // It shows in the list: the modal closed, the row carries the unit and the price from the integer.
    await expect(page.getByRole('heading', { name: 'Add Product to Pile' })).toHaveCount(0);
    await page.getByPlaceholder('Search SKUs, products, or categories...').fill(u.sku);
    const row = page.locator('gable-inventory-table tr', { hasText: u.sku });
    await expect(row).toBeVisible();
    await expect(row).toContainText('BOX');
    await expect(row).toContainText('$4.25');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'product-listed.png') });

    // Open its detail: the PIM aggregate nests the product.
    const detail = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/products/${product.id}/detail`);
    await row.locator('td').first().click();
    const detailRes = await detail;
    expect(detailRes.status(), await detailRes.text()).toBe(200);
    const detailBody = await detailRes.json();
    expect(detailBody.product.id).toBe(product.id);
    expect(detailBody.product.revision).toBe(1);
    expect(detailBody.product).not.toHaveProperty('uom_primary');
    await expect(page).toHaveURL(new RegExp(`/inventory/${product.id}$`));
    await expect(page.getByRole('heading', { name: u.description })).toBeVisible();
    await expect(page.getByText('$4.25').first()).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'product-detail.png') });

    // Edit the margins at the loaded revision (1).
    await page.getByTitle('Edit pricing controls').click();
    await expect(page.getByRole('heading', { name: 'Pricing Controls' })).toBeVisible();
    await page.locator('gable-product-margin-modal input[max="99"]').fill('35');
    await page.locator('gable-product-margin-modal input[max="100"]').fill('6');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'product-margins-edit.png') });
    const patched = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/products/${product.id}/margins` && r.request().method() === 'PATCH');
    await page.getByRole('button', { name: 'Save Controls' }).click();
    const patchRes = await patched;
    expect(patchRes.status(), await patchRes.text()).toBe(200);
    expect(patchRes.request().headers()['if-match']).toBe('"1"');
    expect(patchRes.request().postDataJSON()).toEqual({ target_margin: 35, commission_rate: 6 });
    const edited = (await patchRes.json()) as ProductWire;
    expect(edited.revision).toBe(2);
    expect(edited.target_margin).toBe(35);
    expect(edited.commission_rate).toBe(6);
    // The answer is the whole product, and the page now shows the new numbers.
    expect(edited.base_price_ten_thousandths).toBe(42500);
    await expect(page.getByRole('heading', { name: 'Pricing Controls' })).toHaveCount(0);
    await expect(page.getByText('35.0%', { exact: true })).toBeVisible();
    await expect(page.getByText('6.0%', { exact: true })).toBeVisible();

    // Someone else writes the margins while this page is at revision 2: the page's next save is a 409.
    const elsewhere = await request.patch(`/api/v1/products/${product.id}/margins`, {
      data: { target_margin: 40, commission_rate: 6 },
      headers: { 'If-Match': '"2"' },
    });
    expect(elsewhere.status(), await elsewhere.text()).toBe(200);
    expect(((await elsewhere.json()) as ProductWire).revision).toBe(3);
    // No revision at all is refused outright (428), a quoted one that is behind is a 409.
    const noRevision = await request.patch(`/api/v1/products/${product.id}/margins`, { data: { target_margin: 41, commission_rate: 6 } });
    expect(noRevision.status()).toBe(428);

    await page.getByTitle('Edit pricing controls').click();
    await page.locator('gable-product-margin-modal input[max="99"]').fill('45');
    const stale = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/products/${product.id}/margins` && r.request().method() === 'PATCH');
    await page.getByRole('button', { name: 'Save Controls' }).click();
    const staleRes = await stale;
    expect(staleRes.status()).toBe(409);
    expect(staleRes.request().headers()['if-match']).toBe('"2"');
    expect((await staleRes.json()).error.code).toBe('stale_revision');
    await expect(page.getByText(/changed after this revision was read/)).toBeVisible();
    await expect(page.getByText(/The product was reloaded/)).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Pricing Controls' })).toHaveCount(0);
    // The page reloaded to the other session's write; the refused 45 was not saved.
    await expect(page.getByText('40.0%', { exact: true })).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'product-stale-edit.png') });

    const after = (await (await request.get(`/api/v1/products/${product.id}`)).json()) as ProductWire;
    expect(after.target_margin).toBe(40);
    expect(after.revision).toBe(3);
  });

  test('the product list is the cursor envelope, and the old shapes are refused', async ({ request }) => {
    // Two products, so a page of one has a next page whatever else the database holds.
    for (let i = 0; i < 2; i += 1) {
      const u = unique();
      const res = await request.post('/api/v1/products', {
        data: { sku: u.sku, description: u.description, stock_uom: 'EA', base_price_ten_thousandths: 10000 },
      });
      expect(res.status(), await res.text()).toBe(201);
    }

    const first = (await (await request.get('/api/v1/products?limit=1&include=total')).json()) as { items: ProductWire[]; next_cursor: string | null; limit: number; total: number };
    expect(first.items).toHaveLength(1);
    expect(first.limit).toBe(1);
    expect(typeof first.next_cursor).toBe('string');
    expect(first.total).toBeGreaterThan(1);
    const second = (await (await request.get(`/api/v1/products?limit=1&cursor=${first.next_cursor}`)).json()) as { items: ProductWire[] };
    expect(second.items[0].id).not.toBe(first.items[0].id);
    expect(first.items[0]).toHaveProperty('stock_uom');
    expect(first.items[0]).not.toHaveProperty('total_quantity');

    // Offset and unknown query parameters are 400s, and so is a body that names the old fields.
    expect((await request.get('/api/v1/products?offset=0')).status()).toBe(400);
    expect((await request.get('/api/v1/products?q=lumber')).status()).toBe(400);
    const u = unique();
    const oldBody = await request.post('/api/v1/products', { data: { sku: u.sku, description: u.description, uom_primary: 'PCS', base_price: 4.25 } });
    expect(oldBody.status()).toBe(400);
    expect((await oldBody.json()).error.code).toBe('bad_request');
  });
});
