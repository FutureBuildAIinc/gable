// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page, type APIRequestContext } from '@playwright/test';
import { answerAppDialog } from './app-controls';
import path from 'path';
import fs from 'fs';

/**
 * A deposit on an order (CP-07) against the real stack: taken in the UI on
 * the order page, shown under Deposits with the balance it leaves, applied
 * to the invoice a fulfilment bills, and taken again at the counter for an
 * attached account customer.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

const RUN = process.env.RUN_ID ?? Date.now().toString(36);
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
  const res = await request.post('/api/v1/customers', {
    data: { account_number: `E2E-DEP-${RUN}-${++counter}`, name: `E2E Deposits ${RUN} ${counter}` },
  });
  expect(res.status(), await res.text()).toBe(201);
  return await res.json();
}

/** A stocked product: any with branch stock, as the counter and fulfilment need one. */
async function stockedProduct(request: APIRequestContext): Promise<{ id: string; sku: string }> {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: { id: string; sku: string }[] };
  const list = products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse();
  for (const p of list) {
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as {
      items: { location_name: string; available: string }[];
    };
    const atBranch = page.items
      .filter((r) => r.location_name === 'KEL-MAIN')
      .reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (atBranch >= 10) return p;
  }
  throw new Error('no stocked product for the deposits run');
}

/** A confirmed will-call order of one line, through the API as the desk does. */
async function confirmedOrder(request: APIRequestContext, customer: { id: string }, product: { id: string }) {
  const created = await request.post('/api/v1/orders', {
    data: { customer_id: customer.id, delivery_type: 'pickup', lines: [{ product_id: product.id, quantity: '6' }] },
  });
  expect(created.status(), await created.text()).toBe(201);
  const order = await created.json();
  const confirmed = await request.post(`/api/v1/orders/${order.id}/transitions`, {
    data: { to: 'confirmed', revision: 1 },
  });
  expect(confirmed.status(), await confirmed.text()).toBe(200);
  return await confirmed.json() as { id: string; number: string; total_cents: number; revision: number };
}

test.describe('Deposits on an order', () => {
  test('a deposit taken on the order page is shown, applied at the invoice, and taken at the counter', async ({ page, request }) => {
    const customer = await freshCustomer(request);
    const product = await stockedProduct(request);
    await signIn(page, 'Playwright Deposits');

    // --- The order page: take a deposit in the UI and see it under Deposits.
    const order = await confirmedOrder(request, customer, product);
    await page.goto(`/orders/${order.id}`);
    await expect(page.getByRole('heading', { name: order.number })).toBeVisible();

    await page.getByTestId('take-deposit').click();
    const dialog = page.getByTestId('deposit-dialog');
    await expect(dialog).toBeVisible();
    await dialog.locator('input[name="deposit-amount"]').fill('5.00');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'order-deposit-dialog.png'), fullPage: true });
    const deposit = page.waitForResponse((r) => r.url().endsWith('/api/v1/payments') && r.request().method() === 'POST');
    await dialog.getByTestId('deposit-submit').click();
    const depositRes = await deposit;
    expect(depositRes.status(), await depositRes.text()).toBe(201);
    expect(depositRes.request().postDataJSON()).toMatchObject({ customer_id: customer.id, order_id: order.id, amount_cents: 500, method: 'cash' });

    const panel = page.getByTestId('order-deposits');
    await expect(panel).toContainText('PAY-');
    await expect(panel.getByTestId('deposit-total')).toContainText('$5.00');
    await expect(panel.getByTestId('deposit-unapplied')).toContainText('$5.00');
    const balanceAfter = (order.total_cents - 500) / 100;
    await expect(panel.getByTestId('balance-after-deposits')).toContainText(`$${balanceAfter.toFixed(2)}`);
    await expect(panel).toContainText('Still unapplied');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'order-deposit-taken.png'), fullPage: true });

    // A deposit past the balance due is refused in plain words.
    await page.getByTestId('take-deposit').click();
    await dialog.locator('input[name="deposit-amount"]').fill(String(balanceAfter + 1));
    await dialog.getByTestId('deposit-submit').click();
    await expect(dialog.getByTestId('deposit-error')).toContainText('larger than the order\'s balance due after deposits');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toHaveCount(0);

    // --- The fulfilment: the invoice shows the deposit applied, the balance
    // reduced, and the order panel moves it from unapplied to applied.
    await page.getByRole('button', { name: /Fulfil Order/i }).click();
    const form = page.getByTestId('fulfil-pickup');
    await form.getByLabel('Picked up by').fill('Counter customer');
    const fulfil = page.waitForResponse((r) => r.url().endsWith(`/api/v1/orders/${order.id}/fulfillments`));
    await form.getByRole('button', { name: 'Confirm' }).click();
    const fulfilRes = await fulfil;
    expect(fulfilRes.status(), await fulfilRes.text()).toBe(201);
    const invoiceId = fulfilRes.headers()['location'].replace('/api/v1/invoices/', '');

    await expect(panel.getByTestId('deposit-unapplied')).toContainText('$0.00');
    await expect(panel).toContainText('Applied $5.00 to');

    await page.goto(`/invoices/${invoiceId}`);
    await expect(page.getByRole('heading', { name: /^IN-/ })).toBeVisible();
    const history = page.getByRole('table', { name: 'Payment history' });
    await expect(history).toBeVisible();
    await expect(history).toContainText('payment');
    await expect(page.getByText('Paid:', { exact: false })).toContainText('$5.00');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'invoice-deposit-applied.png'), fullPage: true });

    // --- The counter: with the account attached, a deposit on another order
    // of the same customer through the same route.
    const second = await confirmedOrder(request, customer, product);
    await page.goto('/pos');
    await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible();
    const tillState = page.getByTestId('pos-till-state');
    if ((await tillState.textContent())?.includes('No till')) {
      await page.getByRole('button', { name: 'Open Till' }).click();
      await page.getByTestId('till-float-amount').fill('100.00');
      await page.getByRole('button', { name: 'Open Till' }).last().click();
    }
    await expect(tillState).toContainText('Till open');

    await page.locator('#pos-customer-search').fill(customer.name);
    await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(customer.account_number) }).click();
    await expect(page.getByTestId('pos-customer-name')).toContainText(customer.name);

    await page.getByTestId('pos-deposit-on-order').click();
    const depositDialog = page.getByTestId('pos-deposit-dialog');
    await expect(depositDialog).toBeVisible();
    const orderButton = depositDialog.locator('[data-testid="pos-deposit-order"]', { hasText: second.number });
    await expect(orderButton).toBeVisible();
    await orderButton.click();
    await expect(depositDialog).toContainText(`Deposit on ${second.number}`);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-deposit-order.png'), fullPage: true });
    const counterDeposit = page.waitForResponse((r) => r.url().endsWith('/api/v1/payments') && r.request().method() === 'POST');
    await depositDialog.locator('input[name="deposit-amount"]').fill('3.00');
    await depositDialog.getByTestId('deposit-submit').click();
    const counterRes = await counterDeposit;
    expect(counterRes.status(), await counterRes.text()).toBe(201);
    expect(counterRes.request().postDataJSON()).toMatchObject({ customer_id: customer.id, order_id: second.id, amount_cents: 300 });
    await expect(depositDialog).toHaveCount(0);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-deposit-taken.png'), fullPage: true });
  });
});
