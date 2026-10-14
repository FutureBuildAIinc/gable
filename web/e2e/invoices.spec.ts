// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * Invoices and credit memos against the real stack (C2-3, ADR 0005 section 6):
 * a fulfilled order bills an invoice; the desk finds it in the list, shows its
 * lines and totals on the new line shape, writes a partial credit memo from the
 * invoice page, posts it (number CM-, negative total), voids it, and voids a
 * fresh invoice with a reason, refused with the server's blocker while a credit
 * memo stands. The list filters on the lowercase statuses and the computed
 * overdue flag, and walks the cursor.
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
  const res = await request.post('/api/v1/customers', { data: { account_number: `E2E-INV-${RUN}-${++counter}`, name: `E2E Invoices ${RUN} ${counter}` } });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as { id: string; name: string };
}

// Products with plenty on hand in total; an earlier spec's back order may have
// emptied the default branch of some, so the caller tries each until one confirms.
async function stockedProducts(request: APIRequestContext) {
  // The product list and the inventory levels list are cursor envelopes (C3-1,
  // C3-1b); walk the seed's products oldest first, as fulfilment.spec.ts does.
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: { id: string; sku: string }[] };
  const list = products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse();
  const out: { id: string; sku: string }[] = [];
  for (const p of list) {
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as { items: { available: string }[] };
    if (page.items.reduce((n, r) => n + (Number(r.available) || 0), 0) >= 50) out.push(p);
  }
  if (out.length === 0) throw new Error('no stocked product in the demo seed');
  return out;
}

interface Billed {
  customer: { id: string; name: string };
  product: { id: string; sku: string };
  invoice: {
    id: string;
    number: string;
    revision: number;
    total_cents: number;
    subtotal_cents: number;
    tax_cents: number;
    lines: { id: string; sku: string | null; quantity: string | null; line_total_cents: number | null }[];
  };
}

// An order for ten of one stocked product, confirmed and fulfilled: the
// fulfilment route bills the invoice (Location names it).
async function billedInvoice(request: APIRequestContext, customer?: { id: string; name: string }): Promise<Billed> {
  const cust = customer ?? (await freshCustomer(request));
  for (const product of await stockedProducts(request)) {
    const created = await request.post('/api/v1/orders', {
      data: { customer_id: cust.id, delivery_type: 'delivery', lines: [{ product_id: product.id, quantity: '10' }] },
    });
    expect(created.status(), await created.text()).toBe(201);
    const order = await created.json();
    const confirmed = await request.post(`/api/v1/orders/${order.id}/transitions`, { data: { to: 'confirmed', revision: order.revision } });
    expect(confirmed.status(), await confirmed.text()).toBe(200);
    const confirmedOrder = await confirmed.json();
    if (confirmedOrder.status !== 'confirmed' || confirmedOrder.lines[0].quantity_allocated !== '10') {
      // not fully allocated at this branch: give this order up and try the next product
      const cancelled = await request.post(`/api/v1/orders/${order.id}/transitions`, {
        data: { to: 'cancelled', revision: confirmedOrder.revision, reason: 'e2e: not enough stock here' },
      });
      expect(cancelled.status(), await cancelled.text()).toBe(200);
      continue;
    }
    const fulfilled = await request.post(`/api/v1/orders/${order.id}/fulfillments`, {
      data: { revision: confirmedOrder.revision },
      headers: { 'If-Match': `"${confirmedOrder.revision}"` },
    });
    expect(fulfilled.status(), await fulfilled.text()).toBe(201);
    const invoiceId = fulfilled.headers()['location'].replace('/api/v1/invoices/', '');
    const invoice = await (await request.get(`/api/v1/invoices/${invoiceId}`)).json();
    return { customer: cust, product, invoice };
  }
  throw new Error('no product could be fully allocated for ten units');
}

test.describe('Invoices and credit memos', () => {
  test('find the invoice, read its lines and totals, credit part of it, post, void the memo', async ({ page, request }) => {
    const { customer, product, invoice } = await billedInvoice(request);
    expect(invoice.number).toMatch(/^IN-\d{6,}$/);
    expect(invoice.lines[0].sku).toBe(product.sku);
    await signIn(page, 'Playwright Invoices');

    // The list: the customer filter finds the new invoice.
    await page.goto('/invoices');
    await expect(page.getByRole('heading', { name: 'Invoices', exact: true })).toBeVisible();
    await expect(page.getByTestId('invoice-total')).toBeVisible();
    await expect(page.locator('tbody tr', { hasText: invoice.number })).toHaveCount(1);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'invoice-list.png') });
    await page.getByLabel('Customer', { exact: true }).selectOption({ label: customer.name });
    const row = page.locator('tbody tr', { hasText: invoice.number });
    await expect(row).toHaveCount(1);
    await expect(page.locator('tbody tr')).toHaveCount(1);
    await expect(row).toContainText(customer.name);
    await expect(row).toContainText('Unpaid');

    // The detail: number, line, totals in dollars from the cents on the wire.
    await row.click();
    await expect(page.getByRole('heading', { name: invoice.number })).toBeVisible();
    await expect(page.locator('tr[data-line-type="product"]')).toContainText(product.sku);
    await expect(page.locator('tr[data-line-type="product"]')).toContainText('10');
    const dollars = (cents: number) => (cents / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' });
    const totals = page.getByTestId('invoice-totals');
    await expect(totals).toContainText(dollars(invoice.subtotal_cents));
    await expect(totals).toContainText(dollars(invoice.total_cents));
    await expect(page.getByTestId('open-amount')).toHaveText(dollars(invoice.total_cents));
    await page.screenshot({ path: path.join(SHOTS_DIR, 'invoice-detail.png'), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: path.join(SHOTS_DIR, 'invoice-detail-narrow.png'), fullPage: true });
    await page.setViewportSize({ width: 1440, height: 900 });

    // A partial credit memo from the invoice page: two of the ten, restocked.
    await page.getByRole('link', { name: /Create credit memo/i }).first().click();
    await expect(page.getByRole('heading', { name: 'New credit memo' })).toBeVisible();
    await expect(page.getByTestId('draft-explainer')).toContainText('Nothing moves until you post it');
    await page.getByLabel(`Credit quantity for ${product.sku}`).fill('2');
    await expect(page.getByTestId('credit-estimate')).toContainText('-$');
    // asking for more than was billed is refused beside the field, before the server
    await page.getByLabel(`Credit quantity for ${product.sku}`).fill('11');
    await page.getByLabel('Reason', { exact: true }).fill('two boards arrived split');
    await page.getByRole('button', { name: 'Create draft' }).click();
    await expect(page.getByText('Only 10 remain to credit')).toBeVisible();
    await page.getByLabel(`Credit quantity for ${product.sku}`).fill('2');
    // the shell scrolls inside its own frame, so a taller viewport shows the whole form
    await page.setViewportSize({ width: 1440, height: 1150 });
    await page.screenshot({ path: path.join(SHOTS_DIR, 'credit-memo-form.png') });
    await page.setViewportSize({ width: 1440, height: 900 });
    const createdRes = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/credit-memos' && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Create draft' }).click();
    const created = await createdRes;
    expect(created.status(), await created.text()).toBe(201);
    const draft = await created.json();
    expect(draft.status).toBe('draft');
    expect(draft.number).toBeNull();
    expect(draft.invoice_id).toBe(invoice.id);
    expect(draft.lines[0].quantity).toBe('-2');
    expect(draft.total_cents).toBeLessThan(0);
    expect(created.request().postDataJSON().lines[0]).toEqual({ invoice_line_id: invoice.lines[0].id, quantity: '-2', restock: true });

    // The draft: no number, the credit shown with its minus sign.
    await expect(page.getByRole('heading', { name: 'Draft credit memo' })).toBeVisible();
    await expect(page.getByTestId('draft-banner')).toBeVisible();
    await expect(page.getByTestId('credit-total')).toHaveText(dollars(draft.total_cents));
    await expect(page.getByTestId('credit-total')).toContainText('-$');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'credit-memo-draft.png'), fullPage: true });

    // Post it: the confirm says what posting does, then the number arrives.
    await page.getByRole('button', { name: 'Post', exact: true }).click();
    await expect(page.getByRole('dialog')).toContainText('assigns the credit memo its number');
    const posted = page.waitForResponse((r) => r.url().endsWith(`/api/v1/credit-memos/${draft.id}/transitions`));
    await page.getByRole('button', { name: 'Post credit memo' }).click();
    const postedRes = await posted;
    expect(postedRes.status(), await postedRes.text()).toBe(200);
    expect(postedRes.request().postDataJSON()).toEqual({ to: 'open', revision: draft.revision });
    const memo = await postedRes.json();
    expect(memo.status).toBe('open');
    expect(memo.number).toMatch(/^CM-\d{6,}$/);
    await expect(page.getByRole('heading', { name: memo.number })).toBeVisible();
    await expect(page.getByTestId('credit-total')).toHaveText(dollars(memo.total_cents));
    await expect(page.getByTestId('draft-banner')).toHaveCount(0);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'credit-memo-posted.png'), fullPage: true });

    // The invoice page lists it; the credit memo list shows it as a credit.
    await page.goto(`/invoices/${invoice.id}`);
    const forInvoice = page.getByRole('table', { name: 'Credit memos for this invoice' });
    await expect(forInvoice).toContainText(memo.number);
    await expect(forInvoice).toContainText('-$');
    await page.goto('/credit-memos');
    await expect(page.getByRole('heading', { name: 'Credit Memos', exact: true })).toBeVisible();
    await page.getByLabel('Customer', { exact: true }).selectOption({ label: customer.name });
    const memoRow = page.locator('tbody tr', { hasText: memo.number });
    await expect(memoRow).toHaveCount(1);
    await expect(memoRow).toContainText('Return');
    await expect(memoRow).toContainText(dollars(memo.total_cents));
    await page.getByLabel('Status', { exact: true }).selectOption('open');
    await expect(page.locator('tbody tr', { hasText: memo.number })).toHaveCount(1);
    await page.getByLabel('Status', { exact: true }).selectOption('draft');
    await expect(page.locator('tbody tr', { hasText: memo.number })).toHaveCount(0);
    await page.getByLabel('Status', { exact: true }).selectOption('all');
    await expect(page.locator('tbody tr', { hasText: memo.number })).toHaveCount(1);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'credit-memo-list.png') });

    // Void the memo: a reason is required, then the VOID banner.
    await memoRow.click();
    await page.getByRole('button', { name: 'Void', exact: true }).click();
    const confirmVoid = page.getByRole('button', { name: 'Void credit memo' });
    await expect(confirmVoid).toBeDisabled();
    await page.getByLabel('Reason', { exact: true }).fill('entered against the wrong order');
    const voided = page.waitForResponse((r) => r.url().endsWith(`/api/v1/credit-memos/${draft.id}/transitions`));
    await confirmVoid.click();
    const voidedRes = await voided;
    expect(voidedRes.status(), await voidedRes.text()).toBe(200);
    expect(voidedRes.request().postDataJSON()).toMatchObject({ to: 'void', reason: 'entered against the wrong order' });
    await expect(page.getByTestId('void-banner')).toContainText('entered against the wrong order');
    await expect(page.getByRole('button', { name: 'Void', exact: true })).toHaveCount(0);
  });

  test('voiding an invoice needs a reason and is refused while a credit memo stands', async ({ page, request }) => {
    const { invoice } = await billedInvoice(request);
    await signIn(page, 'Playwright Invoice Void');

    // A draft credit memo names the invoice: the void is refused with the server's blocker.
    const cm = await request.post('/api/v1/credit-memos', {
      data: { invoice_id: invoice.id, reason_code: 'return', reason: 'return', lines: [{ invoice_line_id: invoice.lines[0].id, quantity: '-1', restock: true }] },
    });
    expect(cm.status(), await cm.text()).toBe(201);
    const memo = await cm.json();

    await page.goto(`/invoices/${invoice.id}`);
    await expect(page.getByRole('heading', { name: invoice.number })).toBeVisible();
    await page.getByRole('button', { name: 'Void', exact: true }).click();
    const dialog = page.getByRole('dialog');
    const confirm = dialog.getByRole('button', { name: 'Void invoice' });
    await expect(confirm).toBeDisabled();
    await dialog.getByLabel('Reason').fill('billed to the wrong customer');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'invoice-void-dialog.png') });
    const refused = page.waitForResponse((r) => r.url().endsWith(`/api/v1/invoices/${invoice.id}/transitions`));
    await confirm.click();
    const refusedRes = await refused;
    expect(refusedRes.status()).toBe(409);
    expect(refusedRes.request().postDataJSON()).toEqual({ to: 'void', revision: invoice.revision, reason: 'billed to the wrong customer' });
    const body = await refusedRes.json();
    expect(JSON.stringify(body.error.details)).toContain('has_credit_memos');
    await expect(dialog.getByTestId('dialog-error')).toContainText('void them, then void the invoice');
    await expect(dialog.getByTestId('dialog-error')).toContainText(body.error.message);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'invoice-void-refused.png') });
    await dialog.getByRole('button', { name: 'Cancel' }).click();

    // Void the memo, then the invoice goes through.
    const voidMemo = await request.post(`/api/v1/credit-memos/${memo.id}/transitions`, {
      data: { to: 'void', revision: memo.revision, reason: 'cleared to void the invoice' },
      headers: { 'If-Match': `"${memo.revision}"` },
    });
    expect(voidMemo.status(), await voidMemo.text()).toBe(200);
    await page.getByRole('button', { name: 'Void', exact: true }).click();
    await page.getByRole('dialog').getByLabel('Reason').fill('billed to the wrong customer');
    const ok = page.waitForResponse((r) => r.url().endsWith(`/api/v1/invoices/${invoice.id}/transitions`));
    await page.getByRole('dialog').getByRole('button', { name: 'Void invoice' }).click();
    const okRes = await ok;
    expect(okRes.status(), await okRes.text()).toBe(200);
    expect((await okRes.json()).status).toBe('void');
    await expect(page.getByTestId('void-banner')).toContainText('billed to the wrong customer');
    await expect(page.getByTestId('void-banner')).toContainText('VOID');
    await expect(page.getByRole('button', { name: 'Void', exact: true })).toHaveCount(0);
    await expect(page.getByRole('link', { name: /Create credit memo/i })).toHaveCount(0);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'invoice-void.png'), fullPage: true });

    // The list shows it as Void.
    await page.goto('/invoices');
    await page.getByLabel('Status', { exact: true }).selectOption('void');
    await expect(page.locator('tbody tr', { hasText: invoice.number })).toContainText('Void');
  });

  test('the status and overdue filters and the cursor walk the list', async ({ page, request }) => {
    await signIn(page, 'Playwright Invoice Filters');
    await page.goto('/invoices');
    await expect(page.getByRole('heading', { name: 'Invoices', exact: true })).toBeVisible();

    // The status filter: only that status in the table.
    await page.getByLabel('Status', { exact: true }).selectOption('partial');
    await expect(page.getByText('Updating...')).toHaveCount(0);
    const partialRows = page.locator('tbody tr');
    await expect.poll(async () => partialRows.count()).toBeGreaterThan(0);
    for (const text of await partialRows.allInnerTexts()) expect(text).toContain('Partial');

    // The overdue toggle: every row carries the Overdue badge (the seed has overdue invoices).
    await page.getByLabel('Status', { exact: true }).selectOption('all');
    await page.getByLabel('Overdue only').check();
    await expect(page.getByText('Updating...')).toHaveCount(0);
    const overdueRows = page.locator('tbody tr');
    await expect.poll(async () => overdueRows.count()).toBeGreaterThan(0);
    expect(await overdueRows.count()).toBe(await page.getByTestId('overdue-badge').count());
    const apiOverdue = (await (await request.get('/api/v1/invoices?overdue=true&include=total')).json()) as { total: number };
    await expect(page.getByTestId('invoice-total')).toContainText(`${apiOverdue.total} invoice`);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'invoice-list-overdue.png') });
    await page.getByLabel('Overdue only').uncheck();

    // The overdue=false half of the API and the lowercase vocabulary: a bad status is a 400.
    const notOverdue = (await (await request.get('/api/v1/invoices?overdue=false&limit=200')).json()) as { items: { is_overdue: boolean }[] };
    expect(notOverdue.items.every((i) => !i.is_overdue)).toBe(true);
    expect((await request.get('/api/v1/invoices?status=overdue')).status()).toBe(400);

    // The cursor: five to a page, Load more appends the next five.
    await page.route('**/api/v1/invoices?*', (route) => {
      const url = new URL(route.request().url());
      url.searchParams.set('limit', '5');
      return route.continue({ url: url.toString() });
    });
    await page.reload();
    await expect(page.locator('tbody tr')).toHaveCount(5);
    const first = await page.locator('tbody tr').first().innerText();
    await page.getByRole('button', { name: 'Load more' }).click();
    await expect(page.locator('tbody tr')).toHaveCount(10);
    expect(await page.locator('tbody tr').first().innerText()).toBe(first);
    const numbers = await page.locator('tbody tr td:first-child').allInnerTexts();
    expect(new Set(numbers).size).toBe(10);
  });

  test('the portal lists the customer invoices with their numbers and the overdue badge', async ({ page }) => {
    // The seeded portal user (the repository seed documents the demo password).
    await page.goto('/portal/login');
    await page.locator('#email').fill('demo@kelbrook.ca');
    await page.locator('#password').fill('password');
    await page.locator('form button[type="submit"]').click();
    await expect(page).toHaveURL(/\/portal$/);
    await page.goto('/portal/invoices');
    await expect(page.getByRole('heading', { name: 'Invoices', level: 1 })).toBeVisible();
    await expect(page.getByText(/IN-\d{6}/).first()).toBeVisible();
    // the badge comes from is_overdue and nothing else: as many badges as flagged invoices
    const mine = (await (await page.request.get('/api/portal/v1/invoices')).json()) as { number: string; status: string; is_overdue: boolean }[];
    expect(mine.length).toBeGreaterThan(0);
    for (const inv of mine) expect(inv.number).toMatch(/^IN-\d{6,}$/);
    expect(mine.map((i) => i.status).filter((st) => st === 'OVERDUE')).toHaveLength(0);
    await expect(page.getByTestId('portal-overdue')).toHaveCount(mine.filter((i) => i.is_overdue).length);
    await expect(page.getByText('OVERDUE', { exact: true })).toHaveCount(0);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'portal-invoices.png'), fullPage: true });
  });
});
