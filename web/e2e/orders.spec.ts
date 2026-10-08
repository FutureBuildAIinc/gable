// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * The desk's order flow against the real stack (see playwright.config.ts),
 * on the converted order contract (C2-2a): the quote convert creates the
 * order in one act (201 with the order and its Location), the order page
 * shows the document number, the cents money and the scaled price, the
 * confirm is the transitions route, and the list filters on the lowercase
 * statuses through the cursor envelope.
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

let seeded: Seeded | undefined;

async function firstCustomerAndProduct(request: import('@playwright/test').APIRequestContext): Promise<Seeded> {
  if (seeded) return seeded;
  const customers = (await (await request.get('/api/v1/customers?limit=200')).json()) as { items: { id: string; name: string; account_number: string }[] };
  const customer = customers.items.find((c) => !c.account_number.startsWith('E2E-'))!;
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: { id: string; sku: string; stock_uom: string }[] };
  // The product list is the cursor envelope, newest first; the product spec adds E2E- SKUs.
  const product = products.items.find((p) => !p.sku.startsWith('E2E-') && p.stock_uom === 'EA')!;
  seeded = { customer, product };
  return seeded;
}

test.describe('Order flow on the new contract', () => {
  test('convert a quote into an order, confirm it through the transition, find it in the list', async ({ page, request }) => {
    const { customer, product } = await firstCustomerAndProduct(request);
    await signIn(page, 'Playwright Orders');

    // A one-line draft quote to convert.
    await page.goto('/quotes/new');
    await expect(page.getByRole('heading', { name: 'New Quote' })).toBeVisible();
    await page.getByPlaceholder('Select Customer...').click();
    await page.getByText(customer.name, { exact: true }).first().click();
    await page.getByPlaceholder('Search SKU or Desc...').fill(product.sku);
    await page.locator('div.cursor-pointer', { hasText: product.sku }).first().click();
    await page.locator('gable-line-item-editor input[type="number"]').first().fill('10');
    await page.getByRole('button', { name: 'Add', exact: true }).click();
    const created = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/quotes' && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Create Quote' }).click();
    const createdRes = await created;
    expect(createdRes.status(), await createdRes.text()).toBe(201);
    const quote = await createdRes.json();

    // The convert is one act now (ADR 0005 5.8): 201 with the order, and the
    // desk navigates straight to it.
    const converted = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${quote.id}/convert`));
    await page.getByRole('button', { name: /Convert to Order/i }).click();
    const convertRes = await converted;
    expect(convertRes.status(), await convertRes.text()).toBe(201);
    const order = await convertRes.json();
    expect(order.status).toBe('draft');
    expect(order.quote_id).toBe(quote.id);
    expect(order.number).toMatch(/^SO-\d{6,}$/);
    expect(order.revision).toBe(1);
    expect(Number.isInteger(order.total_cents)).toBe(true);
    expect(order.lines[0].quantity).toBe('10');
    expect(order.lines[0].line_type).toBe('product');
    expect(order.lines[0].price_source).toBe('quote');

    await expect(page).toHaveURL(new RegExp(`/orders/${order.id}$`));
    await expect(page.getByRole('heading', { name: order.number })).toBeVisible();
    await expect(page.getByText('Draft', { exact: true })).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'order-draft.png') });

    // The confirm is the transition on the loaded revision (ADR 0005 5.2).
    const confirm = page.waitForResponse((r) => r.url().endsWith(`/api/v1/orders/${order.id}/transitions`));
    page.once('dialog', (dialog) => dialog.accept());
    await page.getByRole('button', { name: /Confirm Order/i }).click();
    const confirmRes = await confirm;
    expect(confirmRes.status(), await confirmRes.text()).toBe(200);
    expect(confirmRes.request().postDataJSON()).toEqual({ to: 'confirmed', revision: 1 });
    const confirmed = await confirmRes.json();
    // A confirm over the customer's credit limit lands the hold instead: both
    // are committed states with their events, so accept either and assert the
    // page agrees with the server.
    expect(['confirmed', 'on_hold', 'backordered']).toContain(confirmed.status);
    expect(confirmed.revision).toBe(2);
    await expect(page.getByText({ on_hold: 'On Hold', backordered: 'Backordered', confirmed: 'Confirmed' }[confirmed.status as string]!, { exact: true })).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'order-after-confirm.png') });

    // The list: the envelope's items carry the order, and the status filter
    // filters on the lowercase vocabulary.
    await page.goto('/orders');
    await expect(page.getByRole('heading', { name: 'Orders', exact: true })).toBeVisible();
    const row = page.locator('tr', { hasText: order.number });
    await expect(row).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'orders-list.png') });
    const fulfilled = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/orders' && new URL(r.url()).searchParams.get('status') === 'fulfilled');
    await page.getByRole('button', { name: 'Fulfilled', exact: true }).click();
    await fulfilled;
    await expect(row).toHaveCount(0);
    // Click the filter for the status the order actually landed in: the buttons
    // render On Hold before Confirmed, so a pattern matching both picked On Hold
    // and lost a confirmed order whenever the customer had credit to spare. A
    // short stock lands it back ordered (C2-2b).
    const filtered = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/orders' && new URL(r.url()).searchParams.get('status') === confirmed.status);
    await page.getByRole('button', { name: { on_hold: 'On Hold', backordered: 'Backordered', confirmed: 'Confirmed' }[confirmed.status as string]!, exact: true }).click();
    await filtered;
    await expect(page.locator('tr', { hasText: order.number })).toBeVisible();

    // The record page by API agrees with the page.
    const after = await (await request.get(`/api/v1/orders/${order.id}`)).json();
    expect(after.status).toBe(confirmed.status);
    expect(after.revision).toBe(2);
    expect(Number.isInteger(after.subtotal_cents)).toBe(true);
  });

  test('the list is the cursor envelope, newest first, and the filter is a server-side one', async ({ page }) => {
    await signIn(page, 'Playwright Orders List');
    await page.goto('/orders');
    const list = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/orders');
    await expect(page.getByRole('heading', { name: 'Orders', exact: true })).toBeVisible();
    const res = await list;
    expect(res.status()).toBe(200);
    const body = await res.json();
    expect(Array.isArray(body.items)).toBe(true);
    expect(body.items.length).toBeLessThanOrEqual(body.limit);
    // The rows on the page are the envelope's items.
    for (const item of body.items.slice(0, 5)) {
      await expect(page.locator('tr', { hasText: item.number })).toBeVisible();
    }
    // The status filter goes to the server with the lowercase vocabulary.
    const filtered = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/orders' && new URL(r.url()).searchParams.get('status') === 'draft');
    await page.getByRole('button', { name: 'Draft', exact: true }).click();
    expect((await filtered).status()).toBe(200);
  });
});
