// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';
import { stockedProduct } from './pos-stock';
import { openCounter } from './pos-till';
import path from 'path';
import fs from 'fs';

/**
 * The emailed receipt's honest answer (CP-14): on the default stack, with no
 * SMTP configured, the log sender takes the mail and the counter says the
 * receipt was NOT sent and why (never success on the 202 alone), the delivery
 * row the 202 named reads logged, never sent, and the invoice page's email
 * sends list says the same in its own words.
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

/** Adds one unit of the product through the Items card. */
async function addOne(page: Page, sku: string) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
}

test('an emailed receipt on a server with no email set up says not sent', async ({ page, request }) => {
  const run = Date.now().toString(36).toUpperCase();
  const created = await request.post('/api/v1/customers', {
    data: {
      account_number: `E2E-MAIL-${run}`,
      name: `E2E Mail ${run}`,
      email: `billing-${run.toLowerCase()}@example.test`,
      credit_limit_cents: 5000000,
    },
  });
  expect(created.status(), await created.text()).toBe(201);
  const account = (await created.json()) as { id: string; name: string; account_number: string; email: string };

  await signIn(page, 'Email Status E2E');
  const product = await stockedProduct(request);
  await openCounter(page);

  // Name the account (its contact has the email), ring one unit, pay cash.
  await page.locator('#pos-customer-search').fill(account.name);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(account.account_number) }).click();
  await expect(page.getByTestId('pos-customer-name')).toHaveText(account.name);
  await addOne(page, product.sku);
  const total = Number(((await page.getByTestId('summary-total').textContent()) ?? '').replace(/[$,\s]/g, ''));
  expect(total).toBeGreaterThan(0);
  await page.getByTestId('pos-next-payment').click();
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await page.getByTestId('tender-cash').click();
  await expect(page.getByTestId('pos-pay')).toContainText(`$${total.toFixed(2)}`);
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });

  // Email the receipt. The default stack runs the log sender: the answer must
  // say not sent and why, in plain words, not success.
  await page.getByTestId('pos-receipt-email').click();
  const note = page.getByTestId('pos-receipt-note');
  await expect(note).toContainText('not sent', { timeout: 15000 });
  await expect(note).toContainText('Email is not set up on this server');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'email-status-receipt-not-sent.png'), fullPage: true });

  // The delivery row the 202 named reads logged, never sent.
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { id: string; customer_id: string | null; invoice_id: string | null }[];
  };
  const sale = sales.items.find((s) => s.customer_id === account.id);
  expect(sale, 'the sale is listed for the account').toBeTruthy();
  const deliveries = (await (await request.get(`/api/v1/email-deliveries?document_id=${sale!.invoice_id}`)).json()) as {
    items: { recipient: string; status: string; sender_answer: string | null }[];
  };
  expect(deliveries.items.length, 'the email wrote its delivery row').toBeGreaterThanOrEqual(1);
  const row = deliveries.items[0];
  expect(row.status).toBe('logged');
  expect(row.recipient).toBe(account.email);
  expect(row.sender_answer).toContain('logged by the log sender');

  // The invoice page lists the send as not sent: email not set up.
  await page.goto(`/invoices/${sale!.invoice_id}`);
  const sends = page.getByTestId('invoice-email-sends');
  await expect(sends).toContainText('Not sent: email not set up', { timeout: 15000 });
  await expect(sends).toContainText(account.email);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'email-status-sends-list.png'), fullPage: true });
});
