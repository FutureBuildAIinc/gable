// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';

/**
 * QE-01-a on the real stack: a sent quote past its Valid until is swept to
 * Expired, the desk shows it Expired with no Send or Convert, and Reopen to
 * edit, a new Valid until, Send and Convert make an order.
 */

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

test('an expired quote is re-dated, sent again and converted', async ({ page, request }) => {
  test.setTimeout(120_000);
  const customers = (await (await request.get('/api/v1/customers?limit=200')).json()) as { items: { id: string; account_number: string }[] };
  const customer = customers.items.find((c) => !c.account_number.startsWith('E2E-'))!;
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: { id: string; sku: string; stock_uom: string }[] };
  const product = products.items.find((p) => !p.sku.startsWith('E2E-') && p.stock_uom === 'EA')!;

  // Valid for a few seconds, sent, then the worker's sweep moves it to Expired.
  const soon = new Date(Date.now() + 4000).toISOString();
  const made = await request.post('/api/v1/quotes', {
    data: {
      customer_id: customer.id,
      expires_at: soon,
      lines: [{ product_id: product.id, quantity: '1', uom: product.stock_uom, unit_price_ten_thousandths: 10000 }],
    },
  });
  expect(made.status(), await made.text()).toBe(201);
  const quote = await made.json();
  const sent = await request.post(`/api/v1/quotes/${quote.id}/transitions`, {
    data: { to: 'sent', revision: quote.revision }, headers: { 'If-Match': `"${quote.revision}"` },
  });
  expect(sent.status(), await sent.text()).toBe(200);
  await expect.poll(async () => (await (await request.get(`/api/v1/quotes/${quote.id}`)).json()).status, { timeout: 60_000, intervals: [2000] }).toBe('expired');

  await signIn(page, 'Playwright Expiry');

  // List: Valid until column, Expired mark, no Convert.
  await page.goto('/quotes');
  const row = page.locator('tr', { hasText: quote.number });
  await expect(row.getByTestId('quote-expired-mark')).toBeVisible();
  await expect(row.getByRole('button', { name: 'Convert to order' })).toHaveCount(0);

  // Detail: the ruling's words, Reopen to edit, no Send or Convert.
  await page.goto(`/quotes/${quote.id}`);
  await expect(page.getByTestId('quote-expired-notice')).toContainText('revise it with a new valid until date to send or convert it');
  await expect(page.getByRole('button', { name: /Mark Sent/ })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /Convert to Order/ })).toHaveCount(0);
  await page.getByRole('button', { name: /Reopen to edit/ }).click();

  // The builder: a new Valid until, saved, then send and convert.
  await expect(page.getByRole('heading', { name: 'Edit Quote' })).toBeVisible();
  const day = new Date(Date.now() + 14 * 86_400_000).toISOString().slice(0, 10);
  await page.getByLabel('Valid until').fill(day);
  await page.getByRole('button', { name: 'Save Changes' }).click();
  await page.waitForURL(new RegExp(`/quotes/${quote.id}$`));
  await expect(page.getByTestId('quote-expired-notice')).toHaveCount(0);
  await page.getByRole('button', { name: /Mark Sent/ }).click();
  await expect(page.getByText('Quote marked as sent')).toBeVisible();
  await page.getByRole('button', { name: /Convert to Order/ }).click();
  await page.waitForURL(/\/orders\//);
  expect(page.url()).toContain('/orders/');
});
