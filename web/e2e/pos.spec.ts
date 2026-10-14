// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * The counter (POS and till) against the real stack (C2-5, ADR 0005 section
 * 14.2): the desk opens the drawer, rings a sale and pays it with a SPLIT
 * tender (part cash, the rest by check, one invoice, one payment per tender),
 * returns a line (the credit memo posted and refunded out of the drawer), and
 * voids a completed sale (the ledger and the stock whole again).
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

/** A product with stock at the branch and a price, for the counter search. */
async function stockedProduct(request: APIRequestContext): Promise<{ sku: string }> {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as {
    items: { id: string; sku: string }[];
  };
  const list = products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse();
  for (const p of list) {
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as {
      items: { location_name: string; available: string }[];
    };
    // The counter issues from the branch REG-01 belongs to (the seeded
    // Kelowna branch); stock at another branch cannot serve the till.
    const atBranch = page.items
      .filter((r) => r.location_name === 'KEL-MAIN')
      .reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (atBranch >= 50) return p;
  }
  throw new Error('no stocked product for the counter run');
}

/** Opens the counter (the Panels board) and the drawer when none is open (a run may find one open). */
async function openCounter(page: Page) {
  await page.goto('/pos');
  await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible();
  const tillState = page.getByTestId('pos-till-state');
  await expect(tillState).toBeVisible();
  if ((await tillState.textContent())?.includes('No till')) {
    await page.getByRole('button', { name: 'Open Till' }).click();
    await page.getByPlaceholder('200.00').fill('100.00');
    await page.getByRole('button', { name: 'Open Till' }).last().click();
  }
  await expect(tillState).toContainText('Till open');
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

/** The sale total in dollars, read off the Order summary. */
async function saleTotal(page: Page): Promise<number> {
  const text = (await page.getByTestId('summary-total').textContent()) ?? '';
  return Number(text.replace(/[$,\s]/g, ''));
}

test('the counter: split tender, return and void', async ({ page, request }) => {
  await signIn(page, 'POS E2E');
  const product = await stockedProduct(request);

  await openCounter(page);

  // Ring two units of the product.
  await addOne(page, product.sku);
  await addOne(page, product.sku);
  await expect(page.locator('[data-testid="pos-lines"] li')).toHaveCount(2);

  // Read the total off the Order summary, then pay it split in the Payment
  // panel: $1.00 in cash, the rest by check. The completion carries the
  // sale's revision; the page sends the whole tender list at once.
  const total = await saleTotal(page);
  expect(total).toBeGreaterThan(1);
  await expect(page.getByTestId('pos-pay')).toContainText(`$${total.toFixed(2)}`);
  await openPayment(page);
  await page.getByTestId('tender-cash').click();
  await page.getByLabel('Tender amount').fill('1.00');
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  const added = page.getByTestId('pos-added-tenders');
  await expect(added).toContainText('Still owed');
  await expect(added).toContainText(`$${(total - 1).toFixed(2)}`);
  await page.getByTestId('tender-check').click();
  await page.getByLabel('Tender amount').fill((total - 1).toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(added).toContainText('Covered');
  await expect(added).toContainText(/cash/i);
  await expect(added).toContainText(/check/i);
  await expect(added).toContainText('$1.00');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-split-tender.png'), fullPage: true });

  await page.getByTestId('pos-pay').click();
  // The Sale complete card carries the sale's number and what it took.
  const card = page.getByTestId('pos-complete');
  await expect(card).toContainText('Sale complete', { timeout: 15000 });
  await expect(card).toContainText(/POS-\d+/);
  await expect(card).toContainText(`$${total.toFixed(2)}`);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-sale-completed.png'), fullPage: true });

  // Return one unit of the first line: a credit memo posts, the stock comes
  // back and the refund leaves the drawer.
  await page.getByRole('button', { name: 'Return', exact: true }).click();
  const firstQty = page.locator('input[type="number"]').first();
  await firstQty.fill('1');
  await page.getByRole('button', { name: /Take the Return/ }).click();
  await expect(page.getByText(/Return RTN-\d+ complete/)).toBeVisible({ timeout: 15000 });
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-return.png'), fullPage: true });

  // A second sale paid in cash with change, then voided: the ledger returns
  // to zero and the stock comes back.
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await expect(page.getByTestId('pos-no-lines')).toBeVisible();
  await addOne(page, product.sku);
  await openPayment(page);
  await page.getByTestId('tender-cash').click();
  await page.getByLabel('Tender amount').fill('100.00');
  await expect(page.getByTestId('pos-change')).toBeVisible();
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();
  // The change is given: the card says it and the Order summary shows it.
  await expect(page.getByTestId('pos-complete')).toContainText(/change \$\d/, { timeout: 15000 });
  await expect(page.getByTestId('pos-order-summary')).toContainText('Change given');

  page.once('dialog', (dialog) => void dialog.accept('e2e: wrong customer'));
  await page.getByRole('button', { name: 'Void', exact: true }).click();
  await expect(page.getByText(/POS-\d+ voided/)).toBeVisible({ timeout: 15000 });
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-void.png'), fullPage: true });

  // The books tell the same story: the day's sales list the completed sale,
  // the voided one and the return's credit memo behind it.
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { number: string; status: string }[];
  };
  expect(sales.items.length).toBeGreaterThanOrEqual(2);
  expect(sales.items.some((s) => s.status === 'voided')).toBe(true);
  expect(sales.items.some((s) => s.status === 'completed')).toBe(true);
});

/** An account with the credit terms the counter must respect. */
async function makeAccount(request: APIRequestContext, tag: string, extra: Record<string, unknown>) {
  const run = Date.now().toString(36).toUpperCase();
  const res = await request.post('/api/v1/customers', {
    data: { account_number: `E2E-POS-${tag}-${run}`, name: `E2E Counter ${tag} ${run}`, ...extra },
  });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as { id: string; name: string; account_number: string };
}

test('the counter: a charge sale to an account that asks for a purchase order', async ({ page, request }) => {
  const account = await makeAccount(request, 'PO', { credit_limit_cents: 5000000, po_required: true });
  const held = await makeAccount(request, 'HOLD', { credit_limit_cents: 0 });
  await signIn(page, 'POS Account E2E');
  const product = await stockedProduct(request);

  await openCounter(page);

  // The walk-in sale cannot be charged: the account button is off and says why.
  await expect(page.getByTestId('pos-customer')).toContainText('Walk-in sale');

  // An account on hold shows in words, and a sale is not charged to it.
  const search = page.locator('#pos-customer-search');
  await search.fill(held.account_number);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(held.account_number) }).click();
  await expect(page.getByTestId('pos-customer-standing')).toHaveText('On hold: this account has no credit');
  await addOne(page, product.sku);
  await page.getByTestId('pos-next-payment').click();
  const charge = page.getByRole('button', { name: 'Charge to account' });
  await expect(charge).toBeDisabled();
  await expect(page.getByTestId('pos-account-refusal')).toContainText('no credit');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-account-on-hold.png'), fullPage: true });

  // A new sale for the account with credit: its standing in words, the PO asked.
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await expect(page.getByTestId('pos-no-lines')).toBeVisible();
  await expect(page.getByTestId('pos-customer')).toContainText('Walk-in sale');
  await page.locator('#pos-customer-search').fill(account.name);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(account.account_number) }).click();
  await expect(page.getByTestId('pos-customer-name')).toHaveText(account.name);
  await expect(page.getByTestId('pos-customer-standing')).toContainText('Credit available $50,000.00 of $50,000.00');
  await addOne(page, product.sku);
  // Payment: the charge is off for want of the purchase order, and says so with the way on.
  await page.getByTestId('pos-next-payment').click();
  await expect(page.getByRole('button', { name: 'Charge to account' })).toBeDisabled();
  await expect(page.getByTestId('pos-account-refusal')).toContainText(/purchase order/i);
  await page.getByTestId('pos-refusal-customer').click();
  await page.getByLabel(/Purchase order/).fill('PO-E2E-4471');
  await page.getByLabel(/Purchase order/).blur();
  await openPayment(page);
  await expect(page.getByRole('button', { name: 'Charge to account' })).toBeEnabled();
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-account-charge.png'), fullPage: true });

  await page.getByRole('button', { name: 'Charge to account' }).click();
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });
  await expect(page.getByTestId('pos-complete')).toContainText(/POS-\d+/);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-account-charged.png'), fullPage: true });

  // The books: the sale is the account's, the invoice is open on it with the
  // purchase order, and the account's balance carries the charge.
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { id: string; customer_id: string | null; status: string; invoice_id: string | null; total_cents: number }[];
  };
  const sale = sales.items.find((s) => s.customer_id === account.id);
  expect(sale, 'the charge sale is listed for the account').toBeTruthy();
  expect(sale!.status).toBe('completed');
  const full = (await (await request.get(`/api/v1/pos/transactions/${sale!.id}`)).json()) as {
    customer_po: string | null;
    tenders: { method: string; amount_cents: number }[];
  };
  expect(full.customer_po).toBe('PO-E2E-4471');
  expect(full.tenders.map((t) => t.method)).toEqual(['account']);
  const summary = (await (await request.get(`/api/v1/accounts/${account.id}`)).json()) as { balance_cents: number };
  expect(summary.balance_cents).toBe(sale!.total_cents);
});
