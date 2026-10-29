// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { openCounter } from './pos-till';
import path from 'path';
import fs from 'fs';

/**
 * CP-10: a branch's tax jurisdiction and default rate are set on the admin
 * screen, and the counter prices a sale at that rate. The rate the branch
 * held before is put back at the end, so the seeded yards keep their own.
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

type Branch = {
  id: string; path: string; code: string; description: string | null; name: string | null;
  address: string | null; city: string | null; state: string | null; zip: string | null;
  phone: string | null; tax_jurisdiction_code: string | null; default_tax_rate: number | null;
  timezone: string | null; active: boolean; revision: number;
};

async function branchOf(request: APIRequestContext, code: string): Promise<Branch> {
  const page = (await (await request.get('/api/v1/branches?limit=200')).json()) as { items: Branch[] };
  const branch = page.items.find((b) => b.code === code);
  expect(branch, `the seeded branch ${code}`).toBeTruthy();
  return branch!;
}

/** The whole row PUT body, as the branch form's save sends it. */
function branchBody(b: Branch, over: Partial<Branch>): Record<string, unknown> {
  return {
    path: b.path, code: b.code, description: b.description, name: b.name, address: b.address,
    city: b.city, state: b.state, zip: b.zip, phone: b.phone,
    tax_jurisdiction_code: b.tax_jurisdiction_code, default_tax_rate: b.default_tax_rate,
    timezone: b.timezone, active: b.active, ...over,
  };
}

async function stockedProduct(request: APIRequestContext): Promise<{ sku: string; id: string }> {
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
  throw new Error('no stocked product for the counter run');
}

test('a branch tax rate set on the admin screen prices the counter sale', async ({ page, request }) => {
  await signIn(page, 'Playwright Admin');
  const branch = await branchOf(request, 'KEL-MAIN');
  const product = await stockedProduct(request);

  // The branch form shows the tax fields and saves them.
  await page.goto('/admin/branches');
  await expect(page.getByRole('heading', { name: 'Branches' })).toBeVisible();
  const row = page.locator('tr', { hasText: 'KEL-MAIN' });
  await row.getByTitle('Edit').click();
  const dialog = page.locator('.fixed.inset-0');
  await expect(dialog).toBeVisible();
  const rateInput = dialog.locator('label', { hasText: 'Default tax rate (percent)' }).locator('input');
  const jurInput = dialog.locator('label', { hasText: 'Tax jurisdiction code' }).locator('input');
  await expect(rateInput).toHaveValue(branch.default_tax_rate !== null ? String(branch.default_tax_rate * 100) : '');
  await jurInput.fill('CT-HARTFORD');
  await rateInput.fill('10');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'admin-branch-tax-form.png'), fullPage: true });
  await dialog.getByRole('button', { name: 'Save changes' }).click();
  await expect(page.getByText('Branch updated')).toBeVisible({ timeout: 10000 });

  const after = (await (await request.get(`/api/v1/branches/${branch.id}`)).json()) as Branch;
  expect(after.default_tax_rate).toBeCloseTo(0.1);
  expect(after.tax_jurisdiction_code).toBe('CT-HARTFORD');

  // The counter prices a sale at the branch's new rate.
  await openCounter(page);
  const lines = page.locator('[data-testid="pos-lines"] li');
  await page.getByTestId('catalog-search').fill(product.sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(1, { timeout: 10000 });

  const subtotal = Number(((await page.getByTestId('summary-subtotal').textContent()) ?? '').replace(/[$,\s]/g, ''));
  const tax = Number(((await page.getByTestId('summary-tax').textContent()) ?? '').replace(/[$,\s]/g, ''));
  expect(subtotal).toBeGreaterThan(0);
  // the tax is the branch's 10 percent, rounded once
  expect(tax).toBeCloseTo(Math.round(subtotal * 10) / 100, 2);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'admin-branch-tax-counter.png'), fullPage: true });

  // The rate the branch held before is put back, so the seeded yards keep it.
  const fresh = (await (await request.get(`/api/v1/branches/${branch.id}`)).json()) as Branch;
  const put = await request.put(`/api/v1/branches/${branch.id}`, {
    headers: { 'If-Match': `"${fresh.revision}"` },
    data: branchBody(branch, {}),
  });
  expect(put.status(), await put.text()).toBe(200);
});
