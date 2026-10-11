// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * The fulfilment and will-call flow against the real stack (ADR 0005 5.4 to
 * 5.6, the cycle 2 exit line): a will-call order is created, confirmed and
 * fulfilled from the desk with the name of the person who collected it, and the
 * journal entry the fulfilment posted carries the whole story: accounts
 * receivable debited the total, revenue and sales tax credited, and the cost of
 * goods sold debited against inventory. A back ordered order shows its stock
 * side and the allocate retry.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });
const RUN = Date.now().toString(36);
let counter = 0;

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

async function freshCustomer(request: APIRequestContext) {
  const res = await request.post('/api/v1/customers', { data: { account_number: `E2E-FUL-${RUN}-${++counter}`, name: `E2E Fulfilment ${RUN} ${counter}` } });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as { id: string; name: string };
}

// A stocked product with plenty on hand at the default branch and a cost.
// The product list is the cursor envelope (C3-1): read items, newest first.
async function stockedProduct(request: APIRequestContext) {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: { id: string; sku: string }[] };
  // The list is newest first (C3-1); walk it oldest first, the order the
  // seed inserted its products, so the first match is a product stocked at
  // the default branch (the availability sum below spans every branch).
  const seeded = products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse();
  for (const p of seeded) {
    // The inventory levels list is the cursor envelope (C3-1b): quantities
    // are scale 4 decimal strings; available is quantity - allocated.
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as {
      items: { available: string }[];
    };
    const available = page.items.reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (available >= 50) return p;
  }
  throw new Error('no stocked product in the demo seed');
}

test.describe('Fulfilment and will-call', () => {
  test('a will-call order is fulfilled from the desk and posts its cost of goods sold to the GL', async ({ page, request }) => {
    const customer = await freshCustomer(request);
    const product = await stockedProduct(request);
    await signIn(page, 'Playwright Fulfilment');

    const created = await request.post('/api/v1/orders', {
      data: { customer_id: customer.id, delivery_type: 'pickup', lines: [{ product_id: product.id, quantity: '4' }] },
    });
    expect(created.status(), await created.text()).toBe(201);
    const order = await created.json();

    await page.goto(`/orders/${order.id}`);
    await expect(page.getByRole('heading', { name: order.number })).toBeVisible();
    const confirm = page.waitForResponse((r) => r.url().endsWith(`/api/v1/orders/${order.id}/transitions`));
    page.once('dialog', (dialog) => dialog.accept());
    await page.getByRole('button', { name: /Confirm Order/i }).click();
    const confirmRes = await confirm;
    expect(confirmRes.status(), await confirmRes.text()).toBe(200);
    const confirmed = await confirmRes.json();
    expect(confirmed.status).toBe('confirmed');
    expect(confirmed.lines[0].quantity_allocated).toBe('4');
    await expect(page.getByTestId('line-stock').first()).toContainText('4 allocated');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'order-allocated.png') });

    // Fulfil on a will-call order opens the pickup form under the header; the
    // header's labels stay on one line and the name is required before the
    // money moves, its refusal shown beside the field.
    await page.getByRole('button', { name: /Fulfil Order/i }).click();
    const form = page.getByTestId('fulfil-pickup');
    await expect(form).toBeVisible();
    await expect(page.getByRole('button', { name: /Fulfil Order/i })).toHaveCSS('white-space', 'nowrap');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'order-fulfil-pickup-open.png') });
    await form.getByRole('button', { name: 'Confirm' }).click();
    await expect(form.getByText('Enter the name of the person collecting this order')).toBeVisible();

    await form.getByLabel('Picked up by').fill('Counter customer');
    await expect(form.getByText('Enter the name of the person collecting this order')).toHaveCount(0);
    const fulfil = page.waitForResponse((r) => r.url().endsWith(`/api/v1/orders/${order.id}/fulfillments`));
    await form.getByRole('button', { name: 'Confirm' }).click();
    const fulfilRes = await fulfil;
    expect(fulfilRes.status(), await fulfilRes.text()).toBe(201);
    expect(fulfilRes.request().postDataJSON()).toEqual({ revision: 2, picked_up_by: 'Counter customer' });
    const invoiceId = fulfilRes.headers()['location'].replace('/api/v1/invoices/', '');
    const fulfilled = await fulfilRes.json();
    expect(fulfilled.status).toBe('fulfilled');
    expect(fulfilled.invoice_ids).toEqual([invoiceId]);
    await expect(page.getByText('Fulfilled', { exact: true })).toBeVisible();
    await expect(page.getByTestId('line-stock').first()).toContainText('4 shipped');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'order-fulfilled.png') });

    // The exit line: one journal entry for the invoice, 1020 debit = total,
    // 4010 and 2020 credits, 5010 debit and 1030 credit at the cost.
    const entries = (await (await request.get('/api/v1/gl/journal-entries')).json()) as { id: string; source_ref_id?: string }[] | { items: { id: string; source_ref_id?: string }[] };
    const all = Array.isArray(entries) ? entries : entries.items;
    const mine = all.filter((e) => e.source_ref_id === invoiceId);
    expect(mine).toHaveLength(1);
    const entry = (await (await request.get(`/api/v1/gl/journal-entries/${mine[0].id}`)).json()) as {
      lines: { account_code: string; debit: number; credit: number }[];
    };
    const leg = (code: string) => entry.lines.find((l) => l.account_code === code);
    expect(leg('1020')?.debit).toBe(fulfilled.total_cents);
    expect(leg('4010')?.credit).toBe(fulfilled.subtotal_cents);
    expect(leg('2020')?.credit).toBe(fulfilled.tax_cents);
    const cost = leg('5010')?.debit ?? 0;
    expect(cost).toBeGreaterThan(0);
    expect(leg('1030')?.credit).toBe(cost);
    const debits = entry.lines.reduce((n, l) => n + l.debit, 0);
    const credits = entry.lines.reduce((n, l) => n + l.credit, 0);
    expect(debits).toBe(credits);
  });

  test('a short order back orders, shows its stock side, and a pickup order is never routed', async ({ page, request }) => {
    const customer = await freshCustomer(request);
    const product = await stockedProduct(request);
    await signIn(page, 'Playwright Back Order');

    // More than any yard holds: the confirm allocates what there is and back orders the rest.
    const created = await request.post('/api/v1/orders', {
      data: { customer_id: customer.id, delivery_type: 'delivery', lines: [{ product_id: product.id, quantity: '99999' }] },
    });
    expect(created.status(), await created.text()).toBe(201);
    const order = await created.json();
    const confirmed = await request.post(`/api/v1/orders/${order.id}/transitions`, { data: { to: 'confirmed', revision: 1 } });
    expect(confirmed.status(), await confirmed.text()).toBe(200);
    const body = await confirmed.json();
    expect(['backordered', 'on_hold']).toContain(body.status);

    await page.goto(`/orders/${order.id}`);
    await expect(page.getByRole('heading', { name: order.number })).toBeVisible();
    if (body.status === 'backordered') {
      await expect(page.getByTestId('line-stock').first()).toContainText('back ordered');
      await expect(page.getByRole('button', { name: 'Allocate Stock' })).toBeVisible();
      await page.screenshot({ path: path.join(SHOTS_DIR, 'order-backordered.png') });
    }

    // A pickup order is refused a stop on a route: 409 pickup_order. The
    // routes list is the cursor envelope (C5-1d); the seed always holds one.
    const pickup = await request.post('/api/v1/orders', {
      data: { customer_id: customer.id, delivery_type: 'pickup', lines: [{ product_id: product.id, quantity: '1' }] },
    });
    const pickupOrder = await pickup.json();
    const routes = (await (await request.get('/api/v1/delivery/routes')).json()) as { items: { id: string }[] };
    const stop = await request.post('/api/v1/delivery/deliveries', { data: { route_id: routes.items[0].id, order_id: pickupOrder.id, stop_sequence: 99 } });
    expect(stop.status()).toBe(409);
    const err = await stop.json();
    expect(JSON.stringify(err)).toContain('pickup_order');
  });
});
