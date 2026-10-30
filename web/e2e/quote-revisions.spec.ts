// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';

/**
 * QE-01-b on the real stack: a dealer builds and sends a quote (v1), revises
 * it, changes a line's quantity and sends again (v2), reads both versions in
 * the Revision history with the changed line, and converts: the order links
 * the same quote.
 */

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

test('build and send a quote, revise it as v2, see both versions in the history, convert from the same quote', async ({ page, request }) => {
  test.setTimeout(150_000);
  const customers = (await (await request.get('/api/v1/customers?limit=200')).json()) as { items: { id: string; account_number: string }[] };
  const customer = customers.items.find((c) => !c.account_number.startsWith('E2E-'))!;
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: { id: string; sku: string; stock_uom: string }[] };
  const product = products.items.find((p) => !p.sku.startsWith('E2E-') && p.stock_uom === 'EA')!;

  await signIn(page, 'Playwright Revisions');

  // v1: build the quote in the builder.
  await page.goto('/quotes/new');
  await expect(page.getByRole('heading', { name: 'New Quote' })).toBeVisible();
  await page.getByPlaceholder('Select Customer...').click();
  await page.getByText(customer.name, { exact: true }).first().click();
  await page.getByPlaceholder('Search SKU or Desc...').fill(product.sku);
  await page.locator('div.cursor-pointer', { hasText: product.sku }).first().click();
  await page.locator('gable-line-item-editor input[type="number"]').first().fill('10');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  const created = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/quotes' && r.request().method() === 'POST');
  await page.getByRole('button', { name: 'Save draft' }).click();
  const res = await created;
  expect(res.status(), await res.text()).toBe(201);
  const quote = await res.json();
  expect(quote.version_number).toBe(1);

  // v1 sent: the detail page's own button, one snapshot written by the send.
  await expect(page).toHaveURL(new RegExp(`/quotes/${quote.id}$`));
  await expect(page.getByTestId('quote-number-version')).toHaveText(`Quote ${quote.number}`);
  const sent1 = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${quote.id}/transitions`));
  await page.getByRole('button', { name: /Mark Sent/ }).click();
  expect((await sent1).status()).toBe(200);

  // Revise: back to the builder as v2, the number and id unchanged.
  const revised = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${quote.id}/revise`));
  await page.getByRole('button', { name: 'Revise', exact: true }).click();
  const reviseRes = await revised;
  expect(reviseRes.status(), await reviseRes.text()).toBe(200);
  const afterRevise = await reviseRes.json();
  expect(afterRevise.id).toBe(quote.id);
  expect(afterRevise.number).toBe(quote.number);
  expect(afterRevise.version_number).toBe(2);
  expect(afterRevise.status).toBe('draft');
  await expect(page.getByRole('heading', { name: 'Edit Quote' })).toBeVisible();

  // Change the line's quantity and save.
  const qty = page.getByTestId('line-qty').first();
  await qty.fill('20');
  await qty.press('Tab');
  const saved = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${quote.id}`) && r.request().method() === 'PUT');
  await page.getByRole('button', { name: 'Save Changes' }).click();
  expect((await saved).status(), await (await saved).text()).toBe(200);
  await page.waitForURL(new RegExp(`/quotes/${quote.id}$`));

  // v2 sent.
  const sent2 = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${quote.id}/transitions`));
  await page.getByRole('button', { name: /Mark Sent/ }).click();
  expect((await sent2).status()).toBe(200);

  // The number now carries its version, and the history holds both sent
  // versions: v1 as it was sent, v2 with the changed line.
  await expect(page.getByTestId('quote-number-version')).toHaveText(`Quote ${quote.number} v2`);
  const history = page.getByTestId('quote-revision-history');
  await expect(history).toBeVisible();
  const rows = history.getByTestId('quote-revision-row');
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toContainText('v1');
  await expect(rows.nth(0)).toContainText('The first version');
  await expect(rows.nth(1)).toContainText('v2');
  await expect(rows.nth(1)).toContainText(`Quantity 10 to 20`);
  await expect(rows.nth(1)).toContainText(product.sku);

  // The list shows the version beside the number.
  await page.goto('/quotes');
  const row = page.locator('tr', { hasText: quote.number });
  await expect(row.getByTestId('quote-number-version')).toHaveText(`${quote.number} v2`);

  // Convert: the order links the same quote id.
  await page.goto(`/quotes/${quote.id}`);
  const convert = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${quote.id}/convert`));
  await page.getByRole('button', { name: /Convert to Order/ }).click();
  const convertRes = await convert;
  expect(convertRes.status(), await convertRes.text()).toBe(201);
  const order = await convertRes.json();
  expect(order.quote_id).toBe(quote.id);
  await page.waitForURL(new RegExp(`/orders/${order.id}$`));

  // And the versions are still readable after the conversion, two of them.
  const revisions = await (await request.get(`/api/v1/quotes/${quote.id}/revisions`)).json();
  expect(revisions.items).toHaveLength(2);
  expect(revisions.items[0].version_number).toBe(2);
  expect(revisions.items[0].snapshot.total_cents).toBe(order.lines.reduce((s: number, l: { line_total_cents: number }) => s + l.line_total_cents, 0));
  expect(revisions.items[1].version_number).toBe(1);
});
