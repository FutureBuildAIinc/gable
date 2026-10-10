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
  product: { id: string; sku: string; stock_uom: string };
}

// The server limits one address to 120 requests a minute and the desk makes
// many calls per page, so the seeded pair is looked up once per run.
let seeded: Seeded | undefined;

async function firstCustomerAndProduct(request: import('@playwright/test').APIRequestContext): Promise<Seeded> {
  if (seeded) return seeded;
  // The customer list is the cursor envelope, newest first; the customer spec adds customers
  // named E2E-*, so the seeded customers are the ones a quote is built for.
  const customers = (await (await request.get('/api/v1/customers?limit=200')).json()) as { items: { id: string; name: string; account_number: string }[] };
  const customer = customers.items.find((c) => !c.account_number.startsWith('E2E-'))!;
  // The product list is the cursor envelope, newest first; the product spec adds products with
  // E2E- SKUs, so the seeded products are the ones a quote is built for.
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: { id: string; sku: string; stock_uom: string }[] };
  // An EA stocked product: the convert test sells it per M by the each.
  const product = products.items.find((p) => !p.sku.startsWith('E2E-') && p.stock_uom === 'EA')!;
  seeded = { customer, product };
  return seeded;
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
    expect(quote.lines[0].uom).toBe(product.stock_uom);

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
    // The unit the successful send carries is read from the product's unit
    // set: a line's uom must be a sale row of the set, so the code comes
    // from the server, never from this file.
    const set = await (await request.get(`/api/v1/products/${product.id}/units`)).json();
    const saleUnit = (set.units as { uom: string; sell: boolean }[]).find((r) => r.sell)!.uom;
    // The catalogue answers one product with no stocking unit, so the builder
    // meets the empty-unit state the select exists for.
    await page.route(/\/api\/v1\/products(\?|$)/, async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      for (const p of body.items) if (p.id === product.id) p.stock_uom = '';
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
    await select.selectOption(saleUnit);

    const created = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/quotes' && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Create Quote' }).click();
    const res = await created;
    expect(res.status(), await res.text()).toBe(201);
    expect(res.request().postDataJSON().lines[0].uom).toBe(saleUnit);
  });

  test('editing a draft sends the loaded revision, and an edit made on a stale one reloads', async ({ page, request }) => {
    const { customer, product } = await firstCustomerAndProduct(request);
    const draft = (qty: string) => ({
      customer_id: customer.id,
      lines: [{ product_id: product.id, quantity: qty, uom: product.stock_uom, unit_price_ten_thousandths: 10000 }],
    });
    const made = await (await request.post('/api/v1/quotes', { data: draft('2') })).json();
    await signIn(page, 'Playwright Edit');

    const addProductLine = async () => {
      await page.getByPlaceholder('Search SKU or Desc...').fill(product.sku);
      await page.locator('div.cursor-pointer', { hasText: product.sku }).first().click();
      await page.locator('gable-line-item-editor input[type="number"]').first().fill('5');
      await page.getByRole('button', { name: 'Add', exact: true }).click();
    };

    // Edit at the loaded revision: the PUT carries If-Match "1" and the new line.
    await page.goto(`/quotes/${made.id}/edit`);
    await expect(page.getByRole('heading', { name: 'Edit Quote' })).toBeVisible();
    await addProductLine();
    const put = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${made.id}`) && r.request().method() === 'PUT');
    await page.getByRole('button', { name: 'Save Changes' }).click();
    const putRes = await put;
    expect(putRes.status(), await putRes.text()).toBe(200);
    expect(putRes.request().headers()['if-match']).toBe('"1"');
    const sent = putRes.request().postDataJSON();
    expect(sent.lines).toHaveLength(2);
    // The edit sends only what a PUT applies.
    for (const fixedAtCreate of ['source', 'parse_map', 'original_file', 'margin_total_cents', 'branch_id']) {
      expect(sent).not.toHaveProperty(fixedAtCreate);
    }
    expect((await putRes.json()).revision).toBe(2);

    // Someone else edits the quote while this editor is open: the stale save is a 409 and the editor reloads.
    await page.goto(`/quotes/${made.id}/edit`);
    await expect(page.getByRole('heading', { name: 'Edit Quote' })).toBeVisible();
    const elsewhere = await request.put(`/api/v1/quotes/${made.id}`, { data: draft('9'), headers: { 'If-Match': '"2"' } });
    expect(elsewhere.status(), await elsewhere.text()).toBe(200);
    await addProductLine();
    const stale = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${made.id}`) && r.request().method() === 'PUT');
    await page.getByRole('button', { name: 'Save Changes' }).click();
    expect((await stale).status()).toBe(409);
    await expect(page.getByText(/changed elsewhere/)).toBeVisible();
    const after = await (await request.get(`/api/v1/quotes/${made.id}`)).json();
    expect(after.revision).toBe(3);
    expect(after.lines).toHaveLength(1);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'quote-stale-edit.png') });
  });

  test('convert carries a line priced per another unit without loss (the R1-15 refusal lifted)', async ({ page, request }) => {
    const { customer, product } = await firstCustomerAndProduct(request);
    // The line prices per M while the product stocks and sells in EA, so the
    // set needs M as a price row first: one PUT adds it beside the existing
    // rows, its pair (1 M = 1000 EA) derived from the two standard sizes.
    const set = await (await request.get(`/api/v1/products/${product.id}/units`)).json();
    const put = await request.put(`/api/v1/products/${product.id}/units`, {
      data: {
        revision: set.revision,
        stock_uom: product.stock_uom, sale_uom: product.stock_uom,
        price_uom: product.stock_uom, purchase_uom: product.stock_uom,
        units: [
          ...(set.units as { uom: string; sell: boolean; purchase: boolean; price: boolean }[]).map(
            (r) => ({ uom: r.uom, sell: r.sell, purchase: r.purchase, price: r.price }),
          ),
          { uom: 'M', sell: true, purchase: true, price: true },
        ],
      },
    });
    expect(put.status(), await put.text()).toBe(200);
    const made = await (await request.post('/api/v1/quotes', {
      data: {
        customer_id: customer.id,
        lines: [
          { product_id: product.id, quantity: '1', uom: product.stock_uom, unit_price_ten_thousandths: 10000 },
          {
            product_id: product.id, quantity: '1500', uom: 'EA', price_uom: 'M', uom_qty: '1000', price_uom_qty: '1',
            unit_price_ten_thousandths: 37500,
          },
        ],
      },
    })).json();
    await signIn(page, 'Playwright Convert');

    await page.goto(`/quotes/${made.id}`);
    await expect(page.getByRole('heading', { name: `Quote ${made.number}` })).toBeVisible();
    const convert = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${made.id}/convert`));
    await page.getByRole('button', { name: /Convert to Order/ }).click();
    const res = await convert;
    // One act (ADR 0005 5.8): 201 with the order, the pair intact.
    expect(res.status(), await res.text()).toBe(201);
    const order = await res.json();
    expect(order.status).toBe('draft');
    expect(order.lines).toHaveLength(2);
    const perM = order.lines[1];
    expect(perM.price_uom).toBe('M');
    expect(perM.uom_qty).toBe('1000');
    expect(perM.price_uom_qty).toBe('1');
    expect(perM.unit_price_ten_thousandths).toBe(37500);
    expect(perM.price_source).toBe('quote');
    // 1500 EA at 3.75 per M: 1500/1000 x 3.75 = 5.625 -> 563 cents.
    expect(perM.line_total_cents).toBe(563);
    await expect(page).toHaveURL(new RegExp(`/orders/${order.id}$`));
    await page.screenshot({ path: path.join(SHOTS_DIR, 'quote-convert-per-M.png') });

    const after = await (await request.get(`/api/v1/quotes/${made.id}`)).json();
    expect(after.status).toBe('accepted');
  });

  test('the list filters by status on the server and pages by cursor', async ({ page, request }) => {
    const { customer, product } = await firstCustomerAndProduct(request);
    for (let i = 0; i < 3; i++) {
      const r = await request.post('/api/v1/quotes', {
        data: {
          customer_id: customer.id,
          lines: [{ product_id: product.id, quantity: '1', uom: product.stock_uom, unit_price_ten_thousandths: 10000 }],
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
    expect(defaultedBody.lines[0].uom).toBe(product.stock_uom);
    expect(defaultedBody.lines[0].price_uom).toBe(product.stock_uom);

    const missing = await request.post('/api/v1/quotes', {
      data: { customer_id: customer.id, lines: [{ sku: 'SPECIAL-1', description: 'special order', quantity: '1', unit_price_ten_thousandths: 10000 }] },
    });
    expect(missing.status()).toBe(400);
    const body = await missing.json();
    expect(body.error.code).toBe('validation_failed');
    expect(body.error.details.map((d: { field: string }) => d.field)).toContain('lines[0].uom');

    const ok = await (await request.post('/api/v1/quotes', {
      data: { customer_id: customer.id, lines: [{ product_id: product.id, quantity: '1', uom: product.stock_uom, unit_price_ten_thousandths: 10000 }] },
    })).json();
    const stale = await request.post(`/api/v1/quotes/${ok.id}/transitions`, { data: { to: 'sent', revision: 9 } });
    expect(stale.status()).toBe(409);
    expect((await stale.json()).error.code).toBe('stale_revision');
    const none = await request.post(`/api/v1/quotes/${ok.id}/transitions`, { data: { to: 'sent' } });
    expect(none.status()).toBe(428);
  });
});
