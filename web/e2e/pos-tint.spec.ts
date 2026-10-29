// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';
import { pickOptionByValue } from './app-controls';

/**
 * The counter's tint line (row CP-16), end to end in the UI: the catalogue is
 * seeded through the API, a paint line is tinted at the counter (the base,
 * the colour and the formula recorded on the sale), the sale completes, the
 * receipt carries the formula, and the tinted line returns as a mis tint
 * (the store's own error), recorded as such. The tinted no return refusal
 * itself is a dealer policy setting the stack's seed leaves off; its Go test
 * covers both positions of the switch.
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

async function stockedProduct(request: APIRequestContext): Promise<{ id: string; sku: string }> {
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
  throw new Error('no stocked product for the tint run');
}

/** Seeds one tint system, a base linked to the product, a colorant, a colour and a gallon formula. */
async function seedTintCatalogue(request: APIRequestContext, productId: string) {
  const run = Date.now().toString(36);
  const system = (await (await request.post('/api/v1/tint/systems', {
    data: { code: `E2E-${run}`, name: `E2E Tint ${run}`, manufacturer: 'E2E Paints', unit_denominator: 384 },
  })).json()) as { id: string };
  expect(system.id).toBeTruthy();
  const base = (await (await request.post('/api/v1/tint/bases', {
    data: { tint_system_id: system.id, code: 'DEEP', name: 'Deep base', product_id: productId, fill_volume_oz: 122 },
  })).json()) as { id: string };
  const colorant = (await (await request.post('/api/v1/tint/colorants', {
    data: { tint_system_id: system.id, code: 'B1', name: 'Black' },
  })).json()) as { id: string };
  const color = (await (await request.post('/api/v1/tint/colors', {
    data: { tint_system_id: system.id, manufacturer_code: 'HC-188', name: 'Essex Green' },
  })).json()) as { id: string };
  const formula = (await (await request.post('/api/v1/tint/formulas', {
    data: {
      color_id: color.id, base_id: base.id, container_size_code: 'GAL', container_volume_oz: 128, source: 'manual',
      components: [{ colorant_id: colorant.id, whole_oz: 1, increments_coarse: 12, increments_fine: 0, unit_denominator: 384 }],
    },
  })).json()) as { id: string };
  return { system, base, colorant, color, formula };
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

test('the counter: a tinted paint line, its receipt and its mis tint return', async ({ page, request }) => {
  await signIn(page, 'POS Tint E2E');
  const product = await stockedProduct(request);
  await seedTintCatalogue(request, product.id);

  await openCounter(page);

  // Ring the base paint, then tint it.
  await page.getByTestId('catalog-search').fill(product.sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(page.locator('[data-testid="pos-lines"] li')).toHaveCount(1, { timeout: 10000 });
  await page.getByTestId('pos-line-tint-button').click();
  await expect(page.getByRole('heading', { name: 'Tint paint' })).toBeVisible();

  await page.getByTestId('tint-color-search').fill('Essex');
  await page.getByTestId('tint-color-results').getByRole('button', { name: /Essex Green/ }).click();
  await expect(page.getByTestId('tint-components')).toBeVisible();
  await expect(page.getByTestId('tint-components')).toContainText('B1 Black');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-tint-dialog.png'), fullPage: true });

  await page.getByTestId('tint-add').click();
  await expect(page.getByTestId('pos-line-tint')).toContainText('Essex Green');
  await expect(page.getByTestId('pos-line-tint')).toContainText('1 OZ + 12/32');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-tint-line.png'), fullPage: true });

  // Pay in cash.
  const total = Number(((await page.getByTestId('summary-total').textContent()) ?? '').replace(/[$,\s]/g, ''));
  await page.getByTestId('pos-next-payment').click();
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await page.getByTestId('tender-cash').click();
  await page.getByLabel('Tender amount').fill(total.toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });

  // The books: the sale's line carries the frozen tint.
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { id: string; status: string }[];
  };
  const saleID = sales.items[0].id;
  const full = (await (await request.get(`/api/v1/pos/transactions/${saleID}`)).json()) as {
    lines: Array<{ tint: { color_name: string; color_code: string; components: Array<{ amount: { whole_oz: number; increments_coarse: number; unit_denominator: number } }> } | null }>;
  };
  const tint = full.lines.find((l) => l.tint)?.tint;
  expect(tint, 'the sale line carries its tint').toBeTruthy();
  expect(tint!.color_name).toBe('Essex Green');
  expect(tint!.components[0].amount).toEqual({ whole_oz: 1, increments_coarse: 12, increments_fine: 0, unit_denominator: 384 });

  // The receipt is a PDF the customer can reorder the colour from.
  const receipt = await request.get(`/api/v1/pos/transactions/${saleID}/receipt?layout=thermal80`);
  expect(receipt.status()).toBe(200);
  expect(receipt.headers()['content-type']).toContain('pdf');

  // Return the tinted can as a mis tint (the store's own error): accepted
  // and recorded, so the dealer can see how many mis tints the branch mixed.
  // The tinted can does not go back into saleable stock: it is re-tinted and
  // put on the marked down rack.
  await page.getByRole('button', { name: 'Return', exact: true }).click();
  await page.locator('input[type="number"]').first().fill('1');
  const reason = page.getByTestId('return-reason').first();
  await pickOptionByValue(reason, 'mis_tint');
  await expect(page.getByTestId('return-tint-rack')).toBeVisible();
  await page.getByRole('button', { name: /Take the Return/ }).click();
  await expect(page.getByText(/Return RTN-\d+ complete/)).toBeVisible({ timeout: 15000 });
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-tint-return.png'), fullPage: true });
});
