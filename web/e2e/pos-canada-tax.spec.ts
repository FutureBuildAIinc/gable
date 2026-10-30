// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from './font-guard';
import { openCounter, tillState } from './pos-till';
import path from 'path';
import fs from 'fs';

/**
 * Canadian tax on the counter's documents (CP-14 A1). The demo dealer is in
 * British Columbia: a counter sale there charges GST and PST, each rounded on
 * its own, and every document it leaves (the 80mm receipt, the default receipt
 * and the invoice print) prints the two as their own lines with the dealer's
 * GST/HST number, at any total (BC's PST must stand as its own item; GST and
 * the number print from the first dollar). A $27.50 sale is the case where the
 * parts sum one cent over the combined rate rounded once.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

const RUN = Date.now().toString(36).toUpperCase();

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible({ timeout: 15000 });
}

/** A product at $27.50, stocked at the counter's branch. */
async function pricedProduct(request: APIRequestContext): Promise<{ id: string; sku: string }> {
  const registers = (await (await request.get('/api/v1/pos/registers')).json()) as {
    items: { id: string; branch_id: string }[];
  };
  const register = registers.items.find((r) => r.id === 'REG-01') ?? registers.items[0];
  const sku = `E2E-GST-${RUN}`;
  const created = await request.post('/api/v1/products', {
    data: { sku, description: 'E2E Canadian tax', stock_uom: 'PCS', base_price_ten_thousandths: 275000 },
  });
  expect(created.status(), await created.text()).toBe(201);
  const product = (await created.json()) as { id: string };
  const stocked = await request.post('/api/v1/inventory/adjust', {
    data: { product_id: product.id, location_id: register.branch_id, quantity: 5, reason: 'e2e stock for the Canadian tax run' },
  });
  expect(stocked.status(), await stocked.text()).toBe(200);
  return { id: product.id, sku };
}

test('a BC counter sale prints GST and PST and the GST/HST number on every document', async ({ page, request }) => {
  const product = await pricedProduct(request);
  await signIn(page, 'POS Canadian Tax E2E');
  const { opened } = await openCounter(page, { timeout: 15000 });

  const lines = page.locator('[data-testid="pos-lines"] li');
  await page.getByTestId('catalog-search').fill(product.sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(1, { timeout: 10000 });
  // GST 1.38 and PST 1.93 on 27.50: the running tax is their sum.
  await expect(page.getByTestId('summary-tax')).toContainText('$3.31', { timeout: 15000 });

  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await page.getByTestId('tender-card').click();
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();
  const complete = page.getByTestId('pos-complete');
  await expect(complete).toContainText('Sale complete', { timeout: 15000 });
  await expect(complete).toContainText('$30.81', { timeout: 15000 });
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-canada-tax-complete.png'), fullPage: true });

  const number = ((await complete.textContent()) ?? '').match(/POS-\d+/)?.[0] ?? '';
  expect(number, 'the completed card names the sale').not.toBe('');
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { id: string; number: string }[];
  };
  const saleId = sales.items.find((s) => s.number === number)!.id;
  const sale = (await (await request.get(`/api/v1/pos/transactions/${saleId}`)).json()) as {
    invoice_id: string;
    tax_lines: { label: string; rate_percent: string; cents: number }[] | null;
  };
  expect(sale.tax_lines).toEqual([
    expect.objectContaining({ label: 'GST', rate_percent: '5', cents: 138 }),
    expect.objectContaining({ label: 'PST', rate_percent: '7', cents: 193 }),
  ]);

  const documents: [string, string][] = [
    ['the 80mm receipt', `/api/v1/pos/transactions/${saleId}/receipt?layout=thermal80`],
    ['the default receipt', `/api/v1/pos/transactions/${saleId}/receipt`],
    ['the invoice print', `/api/v1/documents/print/invoice/${sale.invoice_id}`],
  ];
  for (const [name, url] of documents) {
    const res = await request.get(url);
    expect(res.status(), `${name}: ${await res.text()}`).toBe(200);
    const text = (await res.body()).toString('latin1');
    expect(text, name).toContain('GST 5%: $1.38');
    expect(text, name).toContain('PST 7%: $1.93');
    expect(text, name).toContain('GST/HST No. 123456789 RT0001');
    expect(text, name).not.toContain('TAX @ 12%');
  }

  // The drawer is closed counting what it holds, as the other counter specs
  // leave it, so this run's sale does not leak into the next spec's drawer.
  const current = (await (await request.get('/api/v1/pos/till/current')).json()) as { id: string };
  const report = (await (await request.get(`/api/v1/pos/till/${current.id}/report`)).json()) as {
    expected_by_method: Record<string, number>;
  };
  if (opened) {
    await page.getByRole('button', { name: 'Close Till' }).click();
    await expect(page.getByRole('heading', { name: 'Close Till - Blind Count' })).toBeVisible({ timeout: 15000 });
    const countFields = page.locator('[data-testid^="till-count-"]');
    for (let i = 0; i < (await countFields.count()); i++) {
      const testid = await countFields.nth(i).getAttribute('data-testid');
      const method = (testid ?? '').replace('till-count-', '').toUpperCase();
      await countFields.nth(i).fill(((report.expected_by_method[method] ?? 0) / 100).toFixed(2));
    }
    await page.getByRole('button', { name: 'Post Count & Close' }).click();
    await expect(page.getByRole('heading', { name: 'Z-Report - Till Closed' })).toBeVisible({ timeout: 15000 });
    await page.getByRole('button', { name: 'Done' }).click();
    await expect(tillState(page)).toContainText('No till', { timeout: 15000 });
  }
});
