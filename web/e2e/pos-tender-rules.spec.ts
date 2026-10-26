// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { pickOptionByValue } from './app-controls';

/**
 * The counter's tender rules a dealer sets (CP-04): the Counter rules screen
 * writes the check ID rule and the database wide check limit, the counter
 * holds a covered check until the customer's ID is checked (never its
 * number), a check over the limit is refused, and a customer's own check
 * limit set on its account page beats the database wide one.
 */

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

/** The stocked product with the highest price, so a few units cover the checks this walk takes. */
async function stockedProduct(request: APIRequestContext): Promise<{ sku: string }> {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as {
    items: { id: string; sku: string; base_price_ten_thousandths: number }[];
  };
  const priced = products.items
    .filter((p) => !p.sku.startsWith('E2E-'))
    .map((p) => ({ ...p, price: Number(p.base_price_ten_thousandths) || 0 }))
    .sort((a, b) => b.price - a.price);
  for (const p of priced) {
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as {
      items: { location_name: string; available: string }[];
    };
    const atBranch = page.items
      .filter((r) => r.location_name === 'KEL-MAIN')
      .reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (atBranch >= 40) return p;
  }
  throw new Error('no stocked product for the counter run');
}

async function openCounter(page: Page) {
  await page.goto('/pos');
  await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible();
  const tillState = page.getByTestId('pos-till-state');
  await expect(tillState).toBeVisible();
  if ((await tillState.textContent())?.includes('No till')) {
    await page.getByRole('button', { name: 'Open Till' }).click();
    await page.getByTestId('till-float-amount').fill('100.00');
    await page.getByRole('button', { name: 'Open Till' }).last().click();
  }
  await expect(tillState).toContainText('Till open');
}

async function addOne(page: Page, sku: string) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
}

/** The sale total in dollars, read off the Order summary. */
async function saleTotal(page: Page): Promise<number> {
  const text = (await page.getByTestId('summary-total').textContent()) ?? '';
  return Number(text.replace(/[$,\s]/g, ''));
}

/** Rings one unit at a time until the sale's total passes the cents named. */
async function ringPast(page: Page, sku: string, cents: number) {
  for (let i = 0; i < 40 && (await saleTotal(page)) <= cents; i++) await addOne(page, sku);
  const total = await saleTotal(page);
  expect(total).toBeGreaterThan(cents);
  return total;
}

/** Opens the counter and names the account the sale goes to. */
async function nameAccount(page: Page, accountNumber: string) {
  const search = page.locator('#pos-customer-search');
  await search.fill(accountNumber);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(accountNumber) }).click();
  await expect(page.getByTestId('pos-customer')).toContainText(accountNumber);
}

/** Takes a check for the whole sale: choose check, type the total, check the ID and pick its kind. */
async function payByCheck(page: Page, total: number, withID: boolean) {
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
  await page.getByTestId('tender-check').click();
  await page.locator('#pos-tender-amount').fill(total.toFixed(2));
  if (withID) {
    await page.locator('#pos-id-checked').check();
    await pickOptionByValue(page.getByTestId('pos-id-type'), 'drivers_license');
  }
}

test('the counter rules a dealer sets: the check ID rule, the check limit, and a customer own limit', async ({ page, request }) => {
  await signIn(page, 'Playwright Admin');
  const product = await stockedProduct(request);

  // The Counter rules screen: checks over $50 need the customer's ID, and no
  // check over $100 is taken database wide.
  await page.goto('/admin/counter-rules');
  await expect(page.getByRole('heading', { name: 'Counter rules' })).toBeVisible();
  await page.getByTestId('counter-rules-limit').fill('100.00');
  await page.getByTestId('counter-rules-id-choice-over_amount').locator('input').check();
  await page.getByTestId('counter-rules-id-over').fill('50.00');
  await page.getByTestId('counter-rules-save').click();
  await expect(page.getByTestId('counter-rules-saved')).toContainText('Counter rules saved');

  // The account this walk uses: no check limit of its own yet, so the
  // database wide rule governs it.
  const run = Date.now().toString(36).toUpperCase();
  const res = await request.post('/api/v1/customers', {
    data: { account_number: `E2E-CP04-${run}`, name: `E2E Counter Rules ${run}` },
  });
  expect(res.status(), await res.text()).toBe(201);
  const account = (await res.json()) as { id: string; account_number: string };

  // At the counter: a $60 check is held until the ID is checked.
  await openCounter(page);
  const total = await ringPast(page, product.sku, 6000);
  await payByCheck(page, 60, false);
  await expect(page.getByTestId('pos-check-id-needed')).toContainText(
    `A check over $50.00 needs the customer's ID`,
  );
  // Pay stays off with the ID still unchecked (the split leaves the rest
  // owed, so the button says that; the ID hold is the band above).
  await expect(page.getByTestId('pos-pay')).toBeDisabled();
  // The ID is checked and its kind picked (the number is never taken), and
  // the rest of the sale in cash: Pay completes it.
  await page.locator('#pos-id-checked').check();
  await pickOptionByValue(page.getByTestId('pos-id-type'), 'drivers_license');
  await expect(page.getByTestId('pos-check-id-needed')).toHaveCount(0);
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('tender-cash').click();
  await page.locator('#pos-tender-amount').fill((total - 60).toFixed(2));
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });

  // A $150 check is over the $100 database wide limit: naming the account,
  // the counter says so and the sale is not completed.
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await expect(page.getByTestId('pos-no-lines')).toBeVisible();
  await nameAccount(page, account.account_number);
  await addOne(page, product.sku);
  await payByCheck(page, 150, true);
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-check-over')).toContainText('over the check limit');
  await expect(page.getByTestId('pos-check-ask-manager')).toBeVisible();
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toHaveCount(0);

  // The account's own page: a $200 check limit on one account.
  await page.goto(`/accounts/${account.id}`);
  await expect(page.getByTestId('account-check-limit')).toContainText('Counter rule');
  await page.getByRole('button', { name: 'Edit Profile' }).click();
  await page.getByLabel('Check limit (dollars)').fill('200.00');
  await page.getByRole('button', { name: 'Save changes' }).click();
  await expect(page.getByTestId('account-check-limit')).toContainText('$200.00');

  // The same $150 check to that account completes with the ID: the
  // customer's own limit beats the database wide one.
  await openCounter(page);
  await nameAccount(page, account.account_number);
  await ringPast(page, product.sku, 15000);
  await payByCheck(page, 150, true);
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });
});
