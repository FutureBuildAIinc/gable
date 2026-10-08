// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * The desk's quote flow against the real stack (see playwright.config.ts), on
 * the converted quote contract: a document number, a lowercase status, a
 * revision every write moves, cents money, the cursor list.
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

interface Seeded {
  customer: { id: string; name: string };
  product: { id: string; sku: string; uom_primary: string };
}

async function firstCustomerAndProduct(request: import('@playwright/test').APIRequestContext): Promise<Seeded> {
  const customers = (await (await request.get('/api/v1/customers')).json()) as { id: string; name: string }[] | { data: { id: string; name: string }[] };
  const customer = (Array.isArray(customers) ? customers : customers.data)[0];
  const products = (await (await request.get('/api/v1/products')).json()) as { id: string; sku: string; uom_primary: string }[] | { data: { id: string; sku: string; uom_primary: string }[] };
  const product = (Array.isArray(products) ? products : products.data)[0];
  return { customer, product };
}

test.describe('Quote flow on the new contract', () => {
  test('build a quote in the builder, then send and accept it from its page', async ({ page, request }) => {
    const { customer, product } = await firstCustomerAndProduct(request);
    await signIn(page, 'Playwright Quote');

    await page.goto('/quotes/new');
    await expect(page.getByRole('heading', { name: 'New Quote' })).toBeVisible();

    await page.getByPlaceholder('Select Customer...').click();
    await page.getByText(customer.name, { exact: true }).first().click();

    await page.getByPlaceholder('Search SKU or Desc...').fill(product.sku);
    await page.locator('div.cursor-pointer', { hasText: product.sku }).first().click();
    await page.locator('gable-line-item-editor input[type="number"]').first().fill('12');
    await page.getByRole('button', { name: 'Add', exact: true }).click();

    const created = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/quotes' && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Create Quote' }).click();
    const res = await created;
    expect(res.status(), await res.text()).toBe(201);
    const quote = await res.json();
    expect(quote.status).toBe('draft');
    expect(quote.number).toMatch(/^Q-\d{6,}$/);
    expect(quote.revision).toBe(1);
    expect(Number.isInteger(quote.total_cents)).toBe(true);
    expect(quote.lines[0].quantity).toBe('12');
    expect(quote.lines[0].uom).toBe(product.uom_primary);

    // The record page: the document number is the heading.
    await expect(page).toHaveURL(new RegExp(`/quotes/${quote.id}$`));
    await expect(page.getByRole('heading', { name: `Quote ${quote.number}` })).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'quote-draft.png') });

    // Mark sent: a transition on the loaded revision.
    const sent = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${quote.id}/transitions`));
    await page.getByRole('button', { name: /Mark Sent/ }).click();
    const sentRes = await sent;
    expect(sentRes.status()).toBe(200);
    expect(sentRes.request().postDataJSON()).toEqual({ to: 'sent', revision: 1 });
    expect((await sentRes.json()).revision).toBe(2);

    const accepted = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${quote.id}/transitions`));
    await page.getByRole('button', { name: /^Accept/ }).click();
    expect((await accepted).status()).toBe(200);

    const after = await (await request.get(`/api/v1/quotes/${quote.id}`)).json();
    expect(after.status).toBe('accepted');
    expect(after.revision).toBe(3);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'quote-accepted.png') });
  });

  test('a line with no unit gets a select of the unit codes that stays while the server complains', async ({ page, request }) => {
    const { customer, product } = await firstCustomerAndProduct(request);
    // The catalogue answers one product with no primary unit, so the builder
    // meets the empty-unit state the select exists for.
    await page.route('**/api/v1/products', async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      const list = Array.isArray(body) ? body : body.data;
      for (const p of list) if (p.id === product.id) p.uom_primary = '';
      await route.fulfill({ response: res, json: body });
    });
    await signIn(page, 'Playwright Unit Select');

    await page.goto('/quotes/new');
    await page.getByPlaceholder('Select Customer...').click();
    await page.getByText(customer.name, { exact: true }).first().click();
    await page.getByPlaceholder('Search SKU or Desc...').fill(product.sku);
    await page.locator('div.cursor-pointer', { hasText: product.sku }).first().click();
    await page.locator('gable-line-item-editor input[type="number"]').first().fill('3');
    await page.getByRole('button', { name: 'Add', exact: true }).click();

    // Empty unit: a select of the 16 codes, not a free text box.
    const select = page.getByLabel('Unit of measure');
    await expect(select).toBeVisible();
    expect(await select.evaluate((el) => el.tagName)).toBe('SELECT');
    expect(await select.locator('option').count()).toBe(17); // 16 codes and the prompt
    await expect(page.getByText('Unit of measure required')).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'quote-unit-select.png') });

    // Picking a unit does not strand the line: the server answers once with a
    // field error, and the select is still there to change the unit again.
    await select.selectOption('BOX');
    let refused = false;
    await page.route('**/api/v1/quotes', async (route) => {
      if (route.request().method() === 'POST' && !refused) {
        refused = true;
        await route.fulfill({
          status: 400,
          contentType: 'application/json',
          json: {
            error: { code: 'validation_failed', message: 'one or more fields failed validation', details: [{ field: 'lines[0].uom', message: 'must be one of the listed units' }] },
            meta: { request_id: 'req-e2e-uom' },
          },
        });
        return;
      }
      await route.continue();
    });
    await page.getByRole('button', { name: 'Create Quote' }).click();
    await expect(page.getByText(/uom: must be one of/)).toBeVisible();
    await expect(select).toBeVisible();
    await select.selectOption('PCS');

    const created = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/quotes' && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Create Quote' }).click();
    const res = await created;
    expect(res.status(), await res.text()).toBe(201);
    expect(res.request().postDataJSON().lines[0].uom).toBe('PCS');
  });

  test('the list filters by status on the server and pages by cursor', async ({ page, request }) => {
    const { customer, product } = await firstCustomerAndProduct(request);
    for (let i = 0; i < 3; i++) {
      const r = await request.post('/api/v1/quotes', {
        data: {
          customer_id: customer.id,
          lines: [{ product_id: product.id, quantity: '1', uom: product.uom_primary, unit_price_ten_thousandths: 10000 }],
        },
      });
      expect(r.status(), await r.text()).toBe(201);
    }

    const sentOnly = await (await request.get('/api/v1/quotes?status=sent&limit=2&include=total')).json();
    for (const q of sentOnly.items) expect(q.status).toBe('sent');
    expect(typeof sentOnly.total).toBe('number');

    const bad = await request.get('/api/v1/quotes?status=SENT');
    expect(bad.status()).toBe(400);
    expect((await bad.json()).error.code).toBe('validation_failed');

    const p1 = await (await request.get('/api/v1/quotes?limit=2')).json();
    expect(p1.items).toHaveLength(2);
    expect(typeof p1.next_cursor).toBe('string');
    const p2 = await (await request.get(`/api/v1/quotes?limit=2&cursor=${p1.next_cursor}`)).json();
    expect(p2.items.map((q: { id: string }) => q.id)).not.toContain(p1.items[0].id);

    await signIn(page, 'Playwright List');
    await page.goto('/quotes');
    await expect(page.getByText(/Q-\d{6,}/).first()).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'quote-list.png') });
  });

  test('a line with no unit and no product is refused with the field named, a product line takes the product unit, and a stale revision is a 409', async ({ request }) => {
    const { customer, product } = await firstCustomerAndProduct(request);
    const defaulted = await request.post('/api/v1/quotes', {
      data: { customer_id: customer.id, lines: [{ product_id: product.id, quantity: '1', unit_price_ten_thousandths: 10000 }] },
    });
    expect(defaulted.status()).toBe(201);
    const defaultedBody = await defaulted.json();
    expect(defaultedBody.lines[0].uom).toBe(product.uom_primary);
    expect(defaultedBody.lines[0].price_uom).toBe(product.uom_primary);

    const missing = await request.post('/api/v1/quotes', {
      data: { customer_id: customer.id, lines: [{ sku: 'SPECIAL-1', description: 'special order', quantity: '1', unit_price_ten_thousandths: 10000 }] },
    });
    expect(missing.status()).toBe(400);
    const body = await missing.json();
    expect(body.error.code).toBe('validation_failed');
    expect(body.error.details.map((d: { field: string }) => d.field)).toContain('lines[0].uom');

    const ok = await (await request.post('/api/v1/quotes', {
      data: { customer_id: customer.id, lines: [{ product_id: product.id, quantity: '1', uom: product.uom_primary, unit_price_ten_thousandths: 10000 }] },
    })).json();
    const stale = await request.post(`/api/v1/quotes/${ok.id}/transitions`, { data: { to: 'sent', revision: 9 } });
    expect(stale.status()).toBe(409);
    expect((await stale.json()).error.code).toBe('stale_revision');
    const none = await request.post(`/api/v1/quotes/${ok.id}/transitions`, { data: { to: 'sent' } });
    expect(none.status()).toBe(428);
  });
});
