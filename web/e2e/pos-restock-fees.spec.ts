// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { pickOptionByValue } from './app-controls';
import path from 'path';
import fs from 'fs';

/**
 * CP-06 restocking fees (slice 7b): a dealer sets the fee each return reason
 * carries on the Counter rules screen (the suggested figures one click away),
 * and the counter states the fee and the refund after it from the server's
 * own preview before the return is taken, the return honouring it to the cent.
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

async function stockedProduct(request: APIRequestContext): Promise<{ sku: string }> {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as {
    items: { id: string; sku: string }[];
  };
  const list = products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse();
  for (const p of list) {
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as {
      items: { location_name: string; available: string }[];
    };
    const atBranch = page.items
      .filter((r) => r.location_name === 'KEL-MAIN')
      .reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (atBranch >= 50) return p;
  }
  throw new Error('no stocked product for the restocking fee run');
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

async function openPayment(page: Page) {
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
}

/** Rings two units of the product and completes the sale in cash, answering its number. */
async function completeCashSale(page: Page, request: APIRequestContext): Promise<string> {
  const product = await stockedProduct(request);
  await openCounter(page);
  await addOne(page, product.sku);
  await addOne(page, product.sku);
  await expect(page.locator('[data-testid="pos-lines"] li')).toHaveCount(2);
  await openPayment(page);
  await page.getByTestId('tender-cash').click();
  const totalText = (await page.getByTestId('summary-total').textContent()) ?? '';
  const total = Number(totalText.replace(/[$,\s]/g, ''));
  await page.getByLabel('Tender amount').fill(total.toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();
  const card = page.getByTestId('pos-complete');
  await expect(card).toContainText('Sale complete', { timeout: 15000 });
  const numberMatch = (await card.textContent())?.match(/POS-\d+/);
  const saleNumber = numberMatch?.[0] ?? '';
  expect(saleNumber).not.toBe('');
  return saleNumber;
}

test('CP-06: a dealer sets the restocking fees and the counter states the fee before the return', async ({ page, request }) => {
  await signIn(page, 'Playwright Admin');

  // The rules this walk finds, put back at the end so no later run or manual
  // walk inherits fees no dealer chose.
  const saved = (await (await request.get('/api/v1/pos/settings')).json()) as {
    revision: number;
    rules: Record<string, unknown>;
  };
  try {
    // The Counter rules screen: the suggested fees fill every reason, the
    // dealer keeps them, and the reasons the counter reads carry them.
    await page.goto('/admin/counter-rules');
    await expect(page.getByRole('heading', { name: 'Counter rules' })).toBeVisible();
    await expect(page.getByTestId('counter-rules-fee-suggested-changed_mind')).toContainText('Suggested 15%');
    await page.getByTestId('counter-rules-fees-suggested').click();
    await expect(page.getByTestId('counter-rules-fee-changed_mind')).toHaveValue('15');
    await page.getByTestId('counter-rules-save').click();
    await expect(page.getByTestId('counter-rules-saved')).toContainText('Counter rules saved');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'cp06-fees-saved.png'), fullPage: true });
    const reasons = ((await (await request.get('/api/v1/pos/return-reasons')).json()) as {
      items: { code: string; restocking_fee_bps: number }[];
    }).items;
    const changedMind = reasons.find((r) => r.code === 'changed_mind');
    expect(changedMind?.restocking_fee_bps).toBe(1500);
    const defective = reasons.find((r) => r.code === 'defective');
    expect(defective?.restocking_fee_bps).toBe(0);

    // A completed sale to return.
    const saleNumber = await completeCashSale(page, request);
    await page.getByRole('button', { name: 'New Sale', exact: true }).click();
    await expect(page.getByTestId('pos-no-lines')).toBeVisible();

    // Find it through the past sale search and return one line as a changed mind.
    await page.getByTestId('return-search-open').click();
    await page.getByTestId('return-search-query').fill(saleNumber);
    await page.getByTestId('return-search-submit').click();
    await expect(page.getByTestId('return-search-result')).toBeVisible();
    await page.getByTestId('return-search-result-pick').click();
    await expect(page.getByTestId('return-qty').first()).toBeVisible();
    await page.locator('[data-testid="return-qty"]').nth(0).fill('1');
    await pickOptionByValue(page.getByTestId('return-reason').nth(0), 'changed_mind');

    // The server's own preview states the fee before the return is taken.
    const previewResponse = page.waitForResponse(
      (r) => new URL(r.url()).pathname === '/api/v1/pos/returns/preview' && r.request().method() === 'POST',
    );
    await expect(page.getByTestId('return-figures')).toBeVisible({ timeout: 10000 });
    const preview = (await (await previewResponse).json()) as { restocking_fee_cents: number; refund_cents: number };
    expect(preview.restocking_fee_cents).toBeGreaterThan(0);
    const feeText = (await page.getByTestId('return-figures-fee').textContent()) ?? '';
    expect(feeText).toBe(`$${(preview.restocking_fee_cents / 100).toFixed(2)}`);
    const refundText = (await page.getByTestId('return-figures-refund').textContent()) ?? '';
    expect(refundText).toBe(`$${(preview.refund_cents / 100).toFixed(2)}`);
    await expect(page.getByTestId('return-figures-line-fee').first()).toContainText('Changed their mind');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'cp06-fee-stated.png'), fullPage: true });

    // The return honours the stated fee to the cent.
    const returnResponse = page.waitForResponse(
      (r) => new URL(r.url()).pathname === '/api/v1/pos/returns' && r.request().method() === 'POST',
    );
    await page.getByTestId('return-take').click();
    const retRes = await returnResponse;
    expect(retRes.status()).toBe(201);
    const ret = (await retRes.json()) as { restocking_fee_cents: number; total_cents: number };
    expect(ret.restocking_fee_cents).toBe(preview.restocking_fee_cents);
    expect(Math.abs(ret.total_cents)).toBe(preview.refund_cents);
    await expect(page.getByText(/less a \$[\d,.]+ restocking fee/)).toBeVisible({ timeout: 15000 });
    await page.screenshot({ path: path.join(SHOTS_DIR, 'cp06-fee-taken.png'), fullPage: true });
  } finally {
    // The rules the walk found go back on the revision the walk left, without
    // the read only suggested figures the GET carries.
    const { restocking_fee_defaults: _suggested, ...writable } = saved.rules;
    const now = (await (await request.get('/api/v1/pos/settings')).json()) as { revision: number };
    const put = await request.put('/api/v1/pos/settings', {
      headers: { 'If-Match': `"${now.revision}"` },
      data: { rules: writable, revision: now.revision },
    });
    expect(await put.text(), `putting the rules back = ${put.status()}`).toContain('"rules"');
  }
});
