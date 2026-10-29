// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { pickOption } from './app-controls';
import path from 'path';
import fs from 'fs';

/**
 * Cash rounding without pennies (CP-18): the branch's own rule rounds the cash
 * leg to the nearest five cents. The counter shows the rounded cash due
 * before the sale is committed, the drawer keeps the rounded cash, and the
 * receipt says what the rounding was.
 *
 * The rule is turned on for the seeded branch for this run and put back
 * afterwards, so the rest of the suite sees the branch as it found it.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

const RUN = Date.now().toString(36).toUpperCase();

interface BranchWire {
  id: string;
  path: string;
  code: string;
  description: string | null;
  name: string | null;
  address: string | null;
  city: string | null;
  state: string | null;
  zip: string | null;
  phone: string | null;
  tax_jurisdiction_code: string | null;
  default_tax_rate: number | null;
  cash_rounding_mode: 'OFF' | 'NEAREST_5_CENTS';
  timezone: string | null;
  active: boolean;
  revision: number;
}

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

/** The branch the counter's register belongs to. */
async function counterBranch(request: APIRequestContext): Promise<BranchWire> {
  const registers = (await (
    await request.get('/api/v1/pos/registers')
  ).json()) as { items: { id: string; branch_id: string }[] };
  const register = registers.items.find((r) => r.id === 'REG-01') ?? registers.items[0];
  expect(register, 'the counter has a register').toBeTruthy();
  const branch = (await (await request.get(`/api/v1/branches/${register.branch_id}`)).json()) as BranchWire;
  return branch;
}

/** The branch's own settings: the rounding rule and the tax rate. */
async function setBranchRule(
  request: APIRequestContext,
  branch: BranchWire,
  mode: 'OFF' | 'NEAREST_5_CENTS',
  taxRate: number | null,
): Promise<BranchWire> {
  const res = await request.put(`/api/v1/branches/${branch.id}`, {
    headers: { 'If-Match': `"${branch.revision}"` },
    data: {
      path: branch.path,
      code: branch.code,
      description: branch.description,
      name: branch.name,
      address: branch.address,
      city: branch.city,
      state: branch.state,
      zip: branch.zip,
      phone: branch.phone,
      tax_jurisdiction_code: branch.tax_jurisdiction_code,
      default_tax_rate: taxRate,
      cash_rounding_mode: mode,
      timezone: branch.timezone,
      active: branch.active,
    },
  });
  expect(res.status(), await res.text()).toBe(200);
  return (await res.json()) as BranchWire;
}

/** A product at the given price, stocked at the branch so the counter can sell it. */
async function pricedProduct(
  request: APIRequestContext,
  branchId: string,
  price: string,
): Promise<{ id: string; sku: string }> {
  const sku = `E2E-RND-${RUN}-${price.replace('.', '')}`;
  const created = await request.post('/api/v1/products', {
    data: {
      sku,
      description: `E2E rounding ${price}`,
      stock_uom: 'PCS',
      base_price_ten_thousandths: Math.round(Number(price) * 10000),
    },
  });
  expect(created.status(), await created.text()).toBe(201);
  const product = (await created.json()) as { id: string };
  const stocked = await request.post('/api/v1/inventory/adjust', {
    data: {
      product_id: product.id,
      location_id: branchId,
      quantity: 10,
      reason: 'e2e stock for the cash rounding run',
    },
  });
  expect(stocked.status(), await stocked.text()).toBe(200);
  return { id: product.id, sku };
}

/** Opens the counter (the Panels board) and the drawer when none is open. */
async function openCounter(page: Page): Promise<boolean> {
  await page.goto('/pos');
  await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible();
  const tillState = page.getByTestId('pos-till-state');
  await expect(tillState).toBeVisible();
  let opened = false;
  if ((await tillState.textContent())?.includes('No till')) {
    await page.getByRole('button', { name: 'Open Till' }).click();
    await page.getByTestId('till-float-amount').fill('100.00');
    await page.getByRole('button', { name: 'Open Till' }).last().click();
    opened = true;
  }
  await expect(tillState).toContainText('Till open');
  return opened;
}

/** Adds one unit of the product through the Items card: the exact SKU in its search, then Enter. */
async function addOne(page: Page, sku: string) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
}

/** Opens the Payment panel (one panel is open at a time; its tenders are in it). */
async function openPayment(page: Page) {
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
}

/** The id of the sale the counter completed, by its number. */
async function saleIdByNumber(request: APIRequestContext, number: string): Promise<string> {
  const sales = (await (
    await request.get('/api/v1/pos/transactions?register_id=REG-01')
  ).json()) as { items: { id: string; number: string; status: string }[] };
  const sale = sales.items.find((s) => s.number === number);
  expect(sale, `the sale ${number} is listed`).toBeTruthy();
  return sale!.id;
}

test.afterAll(async ({ playwright }) => {
  // The branch goes back to the rule it had, so the rest of the suite is
  // unaffected by this run. The stack runs without authentication, so a
  // fresh context can put it back.
  if (!restore) return;
  const request = await playwright.request.newContext({
    baseURL: process.env.BASE_URL ?? 'http://127.0.0.1:18080',
  });
  try {
    await restore(request);
  } finally {
    await request.dispose();
  }
});

let restore: ((request: APIRequestContext) => Promise<void>) | null = null;

test('the counter: the branch rounds the cash, and the receipt says so', async ({ page, request }) => {
  const branch = await counterBranch(request);
  const before = branch;
  restore = async (req: APIRequestContext) => {
    const latest = await counterBranch(req);
    await setBranchRule(req, latest, before.cash_rounding_mode, before.default_tax_rate);
  };
  await setBranchRule(request, branch, 'OFF', 0);

  const cents = await pricedProduct(request, branch.id, '1.03');
  const card = await pricedProduct(request, branch.id, '1.07');

  await signIn(page, 'POS Rounding E2E');

  // Turn the cash rounding rule on for the branch on the Branches screen.
  await page.goto('/admin/branches');
  const row = page.locator('tr', { hasText: branch.code });
  await row.locator('button[title="Edit"]').click();
  await pickOption(page.getByTestId('branch-cash-rounding'), 'Nearest five cents');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'branch-cash-rounding.png'), fullPage: true });
  const saved = page.waitForResponse(
    (r) => r.request().method() === 'PUT' && new URL(r.url()).pathname === `/api/v1/branches/${branch.id}` && r.ok(),
  );
  await page.getByRole('button', { name: 'Save changes' }).click();
  await saved;

  const openedFresh = await openCounter(page);

  // A $1.03 sale paid in cash: the drawer keeps $1.05, so the counter asks
  // for the rounded cash due and says so.
  await addOne(page, cents.sku);
  await openPayment(page);
  await page.getByTestId('tender-cash').click();
  const cashDue = page.getByTestId('pos-cash-due');
  await expect(cashDue).toBeVisible();
  await expect(cashDue).toContainText('nearest 5 cents');
  await expect(cashDue).toContainText('$1.05');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-cash-due-rounded.png'), fullPage: true });

  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
  await page.getByTestId('pos-pay').click();
  const complete = page.getByTestId('pos-complete');
  await expect(complete).toContainText('Sale complete', { timeout: 15000 });
  await expect(complete).toContainText('$1.03');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-cash-rounding-complete.png'), fullPage: true });

  // The receipt says what the rounding was: the 80mm receipt carries the
  // exact total and the rounding line.
  const number = ((await complete.textContent()) ?? '').match(/POS-\d+/)?.[0] ?? '';
  expect(number, 'the completed card names the sale').not.toBe('');
  const saleId = await saleIdByNumber(request, number);
  const receipt = await request.get(`/api/v1/pos/transactions/${saleId}/receipt?layout=thermal80`);
  expect(receipt.status(), await receipt.text()).toBe(200);
  const text = (await receipt.body()).toString('latin1');
  expect(text).toContain('Cash rounding');
  expect(text).toContain('$0.02');

  // A $1.07 sale paid by card settles to the exact cent: no cash leg, no
  // rounding, and the receipt says nothing about it.
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await expect(page.getByTestId('pos-no-lines')).toBeVisible();
  await addOne(page, card.sku);
  await openPayment(page);
  await page.getByTestId('tender-card').click();
  await expect(page.getByTestId('pos-cash-due')).toHaveCount(0);
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });
  await expect(page.getByTestId('pos-complete')).toContainText('$1.07');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-card-exact.png'), fullPage: true });

  // Leave the counter as the suite found it: close the drawer counting
  // exactly what it holds, so the close balances and no sale of this run
  // leaks into the next spec's drawer. The expected figures are read over
  // the API and keyed by the count field each method names; while the
  // branch rounds, every cash figure in the drawer is a multiple of five
  // cents.
  const current = (await (await request.get('/api/v1/pos/till/current')).json()) as { id: string };
  const report = (await (await request.get(`/api/v1/pos/till/${current.id}/report`)).json()) as {
    expected_by_method: Record<string, number>;
  };
  if (openedFresh) {
    expect(report.expected_by_method['CASH'] % 5, 'the drawer cash is rounded to the five cents').toBe(0);
  }
  await page.getByRole('button', { name: 'Close Till' }).click();
  await expect(page.getByRole('heading', { name: 'Close Till - Blind Count' })).toBeVisible();
  const countFields = page.locator('[data-testid^="till-count-"]');
  for (let i = 0; i < (await countFields.count()); i++) {
    const testid = await countFields.nth(i).getAttribute('data-testid');
    const method = (testid ?? '').replace('till-count-', '').toUpperCase();
    await countFields.nth(i).fill(((report.expected_by_method[method] ?? 0) / 100).toFixed(2));
  }
  await page.getByRole('button', { name: 'Post Count & Close' }).click();
  await expect(page.getByRole('heading', { name: 'Z-Report - Till Closed' })).toBeVisible({ timeout: 15000 });
  await expect(page.getByText('BALANCED')).toBeVisible();
  await page.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByTestId('pos-till-state')).toContainText('No till');
});
