// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';
import path from 'path';
import fs from 'fs';

/**
 * QE-01: a dealer's salesperson builds and sends a quote with a costed
 * product line, shares its link, and the contractor opens /dq/<token> with no
 * sign in, types a full name and accepts. Back on the desk the quote reads
 * Accepted, Convert to Order is still there, and Convert makes the order.
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

test.describe('Dealer quote share link', () => {
  test('a contractor accepts a shared quote and the desk converts it', async ({ page, browser, request }) => {
    const customers = (await (await request.get('/api/v1/customers?limit=200')).json()) as { items: { id: string; name: string; account_number: string }[] };
    const customer = customers.items.find((c) => !c.account_number.startsWith('E2E-'))!;
    const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: { id: string; sku: string; stock_uom: string }[] };
    const product = products.items.find((p) => !p.sku.startsWith('E2E-') && p.stock_uom === 'EA')!;

    await signIn(page, 'Playwright Share Accept');
    await page.goto('/quotes/new');
    await page.getByPlaceholder('Select Customer...').click();
    await page.getByText(customer.name, { exact: true }).first().click();
    await page.getByPlaceholder('Search SKU or Desc...').fill(product.sku);
    await page.locator('div.cursor-pointer', { hasText: product.sku }).first().click();
    await page.locator('gable-line-item-editor input[type="number"]').first().fill('8');
    await page.getByRole('button', { name: 'Add', exact: true }).click();

    const created = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/quotes' && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Save draft' }).click();
    const quote = await (await created).json();
    await expect(page.getByRole('heading', { name: `Quote ${quote.number}` })).toBeVisible();

    const sent = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${quote.id}/transitions`));
    await page.getByRole('button', { name: /Mark Sent/ }).click();
    expect((await sent).status()).toBe(200);

    // The share link: the desk's route, the link the contractor is given.
    const link = await request.post(`/api/v1/quotes/${quote.id}/share-link`);
    expect(link.status(), await link.text()).toBe(200);
    const linkPath = (await link.json()).path as string;
    expect(linkPath).toMatch(/^\/dq\/[A-Za-z0-9_-]{43}$/);

    // The contractor: a fresh context, no sign in.
    const contractor = await browser.newContext({ baseURL: new URL(page.url()).origin });
    const pub = await contractor.newPage();
    await pub.goto(linkPath);
    await expect(pub.getByTestId('dealer-lines')).toBeVisible();
    await expect(pub.getByText(`Quote ${quote.number}`)).toBeVisible();
    await expect(pub.getByTestId('dealer-accept')).toBeDisabled();
    await pub.screenshot({ path: path.join(SHOTS_DIR, 'dealer-quote-open.png') });
    await pub.locator('input[name="full_name"]').fill('Pat Contractor');
    const accepted = pub.waitForResponse((r) => r.url().endsWith('/accept'));
    await pub.getByTestId('dealer-accept').click();
    expect((await accepted).status()).toBe(200);
    await expect(pub.getByTestId('dealer-closed')).toContainText('Quote accepted');
    await pub.screenshot({ path: path.join(SHOTS_DIR, 'dealer-quote-accepted.png') });

    // A second visit reads the closed state, with no controls.
    await pub.reload();
    await expect(pub.getByTestId('dealer-closed')).toContainText('Quote accepted');
    await expect(pub.getByTestId('dealer-accept')).toHaveCount(0);
    await contractor.close();

    // Back on the desk: Accepted, and Convert is still offered.
    await page.reload();
    await expect(page.getByText('accepted', { exact: false }).first()).toBeVisible();
    const convert = page.getByRole('button', { name: /Convert to Order/ });
    await expect(convert).toBeVisible();
    const converted = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${quote.id}/convert`));
    await convert.click();
    expect((await converted).status()).toBe(201);
    await expect(page).toHaveURL(/\/orders\/[0-9a-f-]{36}$/);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'quote-converted-order.png') });
  });
});
