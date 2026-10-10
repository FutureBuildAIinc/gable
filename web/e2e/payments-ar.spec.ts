// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * Payments, deposits and AR against the real stack (C2-4, ADR 0005 sections
 * 9 and 10): the desk takes a payment on an invoice (the modal posts cents
 * applied to the invoice in the same act, an overpayment held as unapplied
 * cash), applies that unapplied cash to another invoice later from the
 * account's payments tab, voids a payment there (the applications reversed,
 * the invoice reopened), and reads the aging grouped by job and by ship-to
 * and the customer statement on the new AR routes.
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
  const res = await request.post('/api/v1/customers', { data: { account_number: `E2E-PAY-${RUN}-${++counter}`, name: `E2E Payments ${RUN} ${counter}` } });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as { id: string; name: string };
}

// Products with plenty on hand; the caller tries each until one confirms
// (the same walk the invoices spec does).
async function stockedProducts(request: APIRequestContext) {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: { id: string; sku: string }[] };
  const list = products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse();
  const out: { id: string; sku: string }[] = [];
  for (const p of list) {
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as { items: { available: string }[] };
    if (page.items.reduce((n, r) => n + (Number(r.available) || 0), 0) >= 50) out.push(p);
  }
  return out;
}

/** One billed, unpaid invoice of the customer, on the job and ship-to given. */
async function billedInvoice(
  request: APIRequestContext,
  customer: { id: string; name: string },
  extras: { job_id?: string; ship_to_id?: string } = {},
): Promise<{ id: string; number: string; total_cents: number }> {
  const tried: string[] = [];
  for (const product of await stockedProducts(request)) {
    const created = await request.post('/api/v1/orders', {
      data: {
        customer_id: customer.id, delivery_type: 'delivery',
        job_id: extras.job_id, ship_to_id: extras.ship_to_id,
        lines: [{ product_id: product.id, quantity: '10' }],
      },
    });
    expect(created.status(), await created.text()).toBe(201);
    const order = await created.json();
    const confirmed = await request.post(`/api/v1/orders/${order.id}/transitions`, { data: { to: 'confirmed', revision: order.revision } });
    expect(confirmed.status(), await confirmed.text()).toBe(200);
    const confirmedOrder = await confirmed.json();
    if (confirmedOrder.status !== 'confirmed' || confirmedOrder.lines[0].quantity_allocated !== '10') {
      tried.push(`${product.sku}: ${confirmedOrder.status}, ${confirmedOrder.lines[0].quantity_allocated} allocated`);
      await request.post(`/api/v1/orders/${order.id}/transitions`, { data: { to: 'cancelled', revision: confirmedOrder.revision, reason: 'e2e: not enough stock here' } });
      continue;
    }
    const fulfilled = await request.post(`/api/v1/orders/${order.id}/fulfillments`, {
      data: { revision: confirmedOrder.revision },
      headers: { 'If-Match': `"${confirmedOrder.revision}"` },
    });
    expect(fulfilled.status(), await fulfilled.text()).toBe(201);
    const id = fulfilled.headers()['location'].replace('/api/v1/invoices/', '');
    return await (await request.get(`/api/v1/invoices/${id}`)).json();
  }
  throw new Error(`no product could be confirmed and fully allocated for ten units (${tried.join('; ') || 'no stocked product'})`);
}

test.describe('Payments, unapplied cash and AR', () => {
  test('take a payment on an invoice, apply the overpayment later, void a payment', async ({ page, request }) => {
    await signIn(page, 'Playwright Payments');
    const customer = await freshCustomer(request);
    const first = await billedInvoice(request, customer);
    const second = await billedInvoice(request, customer);

    // Take the payment: the modal is prefilled with the open amount; paying
    // double leaves the rest as unapplied cash on the account.
    await page.goto(`/invoices/${first.id}`);
    await expect(page.getByRole('heading', { name: first.number })).toBeVisible();
    await page.getByRole('button', { name: /^Pay$/ }).click();
    const dialog = page.locator('[role="dialog"]');
    await expect(dialog).toBeVisible();
    const amount = page.locator('[role="dialog"] input[type="number"]');
    await expect(amount).toHaveValue((first.total_cents / 100).toString());
    await amount.fill(String(first.total_cents / 100 + second.total_cents / 100));
    await expect(page.getByText('the remainder is held as unapplied cash', { exact: false })).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'payment-modal-overpayment.png') });
    await page.locator('[role="dialog"] button[type="submit"]').click();
    await expect(dialog).toBeHidden();

    // The invoice is paid and its history is the application, live.
    await expect(page.getByTestId('open-amount')).toHaveText('$0.00');
    await expect(page.getByRole('table', { name: 'Payment history' })).toContainText('payment');
    await expect(page.getByRole('table', { name: 'Payment history' })).toContainText('live');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'invoice-paid.png'), fullPage: true });

    // The account's payments tab holds the unapplied cash of the overpayment.
    await page.goto(`/accounts/${customer.id}`);
    await page.getByRole('button', { name: 'Payments' }).click();
    const unappliedRow = page.getByTestId('unapplied-payment');
    await expect(unappliedRow).toHaveCount(1);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'account-payments-unapplied.png'), fullPage: true });

    // Apply it later, to the second invoice.
    await unappliedRow.getByTestId('apply-payment').click();
    const applyDialog = page.locator('[role="dialog"]');
    await expect(applyDialog).toContainText('Apply unapplied cash');
    await expect(applyDialog).toContainText(second.number);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'apply-unapplied-dialog.png') });
    await applyDialog.getByTestId('confirm-apply').click();
    await expect(applyDialog).toBeHidden();
    await expect(page.getByTestId('unapplied-payment')).toHaveCount(0);
    const secondAfter = await (await request.get(`/api/v1/invoices/${second.id}`)).json();
    expect(secondAfter.status).toBe('paid');

    // A payment of its own, then voided: the void needs a reason and reopens
    // what the payment had settled.
    const spare = await request.post('/api/v1/payments', {
      data: { customer_id: customer.id, amount_cents: 10000, method: 'cash', reference: `E2E-VOID-${RUN}` },
    });
    expect(spare.status(), await spare.text()).toBe(201);
    await page.reload();
    await page.getByRole('button', { name: 'Payments' }).click();
    const spareRow = page.getByTestId('unapplied-payment').filter({ hasText: `E2E-VOID-${RUN}` });
    await expect(spareRow).toHaveCount(1);
    await spareRow.getByTestId('void-payment').click();
    const voidDialog = page.locator('[role="dialog"]');
    await expect(voidDialog).toContainText('Void payment');
    await voidDialog.locator('input[type="text"]').fill('recorded against the wrong customer');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'void-payment-dialog.png') });
    await voidDialog.getByTestId('confirm-void').click();
    await expect(voidDialog).toBeHidden();
    await expect(page.getByTestId('unapplied-payment').filter({ hasText: `E2E-VOID-${RUN}` })).toHaveCount(0);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'account-payments-after-void.png'), fullPage: true });
  });

  test('the aging groups by job and by ship-to, and the statement reads on the new route', async ({ page, request }) => {
    await signIn(page, 'Playwright Aging');

    // A customer with at least two quoted jobs (the seed draws fresh ids every
    // run, so the customer is discovered through its quotes, never hardcoded),
    // with room under its credit limit: the seed parks one customer over its
    // limit for the credit hold demo, and an order for it goes on hold rather
    // than confirm. Quote dates are random, so without this check that
    // customer is sometimes the first one found. The room is counted as the
    // order's credit check counts it (ADR 0005 5.3): the receivable plus the
    // customer's live orders, here their whole totals, which is never less
    // than their unbilled remainder.
    const quotes = ((await (await request.get('/api/v1/quotes?limit=200')).json()) as { items: { customer_id: string; customer_name?: string; job_id: string | null }[] }).items;
    const jobsByCustomer = new Map<string, Set<string>>();
    for (const q of quotes) {
        if (!q.job_id) continue;
        if (!jobsByCustomer.has(q.customer_id)) jobsByCustomer.set(q.customer_id, new Set());
        jobsByCustomer.get(q.customer_id)!.add(q.job_id);
    }
    let kelbrook = '';
    for (const [customer, jobs] of jobsByCustomer) {
        if (jobs.size < 2) continue;
        const c = (await (await request.get(`/api/v1/customers/${customer}`)).json()) as { credit_limit_cents: number | null; balance_cents: number };
        if (c.credit_limit_cents === null) { kelbrook = customer; break; }
        let live = 0;
        for (const status of ['confirmed', 'backordered', 'on_hold']) {
          const page = (await (await request.get(`/api/v1/orders?customer_id=${customer}&status=${status}&limit=200`)).json()) as { items: { total_cents: number }[] };
          live += page.items.reduce((n, o) => n + o.total_cents, 0);
        }
        if (c.credit_limit_cents - c.balance_cents - live >= 2_000_000) { kelbrook = customer; break; }
    }
    if (!kelbrook) {
        test.skip(true, 'the seed holds no customer with two jobs and credit room to age by');
        return;
    }
    const jobIds = [...jobsByCustomer.get(kelbrook)!];
    const customerName = ((await (await request.get(`/api/v1/customers/${kelbrook}`)).json()) as { name: string }).name;
    const shipTos = ((await (await request.get(`/api/v1/customers/${kelbrook}/ship-tos`)).json()) as { items: { id: string; code: string }[] }).items;
    expect(shipTos.length).toBeGreaterThanOrEqual(1);
    if (shipTos.length < 2) {
      const second = await request.post(`/api/v1/customers/${kelbrook}/ship-tos`, {
        data: { code: 'E2ESITE', name: 'E2E second site', line1: '2 Playwright Rd', city: 'Kelowna', region: 'BC', postal_code: 'V1Y 1Z1', country: 'CA' },
      });
      expect(second.status(), await second.text()).toBe(201);
      shipTos.push(await second.json());
    }
    await billedInvoice(request, { id: kelbrook, name: customerName }, { job_id: jobIds[0], ship_to_id: shipTos[0].id });
    await billedInvoice(request, { id: kelbrook, name: customerName }, { job_id: jobIds[1], ship_to_id: shipTos[1].id });

    // By customer first, then by job and by ship-to: the toggle re-reads the
    // aging and the group rows split.
    await page.goto('/reports/ar-aging');
    await expect(page.getByRole('heading', { name: 'AR Aging Report' })).toBeVisible();
    await expect(page.getByTestId('aging-table')).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'aging-by-customer.png'), fullPage: true });

    const jobRows = ((await (await request.get(`/api/v1/ar/aging?group_by=job&customer_id=${kelbrook}`)).json()) as { items: { job_name: string | null; job_id: string | null; total_cents: number }[] }).items
      .filter((r) => r.job_id && r.total_cents !== 0);
    await page.getByTestId('group-by-job').click();
    for (const row of jobRows) {
      if (row.job_name) {
        await expect(page.getByTestId('aging-row').filter({ hasText: row.job_name }).first()).toBeVisible({ timeout: 15000 });
      }
    }
    await page.screenshot({ path: path.join(SHOTS_DIR, 'aging-by-job.png'), fullPage: true });

    await page.getByTestId('group-by-ship_to').click();
    await expect(page.getByTestId('aging-row').filter({ hasText: shipTos[0].code }).first()).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId('aging-row').filter({ hasText: shipTos[1].code }).first()).toBeVisible({ timeout: 15000 });
    await page.screenshot({ path: path.join(SHOTS_DIR, 'aging-by-ship-to.png'), fullPage: true });

    // The statement on the new AR route: the seeded customer's activity.
    await page.goto('/reports/customer-statement');
    await expect(page.getByRole('heading', { name: 'Customer Statement' })).toBeVisible();
    await page.getByLabel('Customer ID').fill(kelbrook);
    await page.getByRole('button', { name: 'Generate' }).click();
    await expect(page.getByTestId('statement-currency')).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId('statement-currency')).toContainText('Opening Balance');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'customer-statement.png'), fullPage: true });
  });
});
