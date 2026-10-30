// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { pickOptionByValue } from './app-controls';
import { openCounter } from './pos-till';
import path from 'path';
import fs from 'fs';

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

// The weigh in and out walk polls the simulator's settling reading: on a
// loaded machine the stable wait and the UI steps outrun the default timeout.
test.setTimeout(90_000);

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

async function openPayment(page: Page) {
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
}

async function saleTotal(page: Page): Promise<number> {
  const text = (await page.getByTestId('summary-total').textContent()) ?? '';
  return Number(text.replace(/[$,\s]/g, ''));
}

async function addOne(page: Page, sku: string) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
}

async function setSimulator(request: APIRequestContext, weightLb: string) {
  const res = await request.put('/api/v1/pos/scale/simulator', {
    data: { weight_lb: weightLb, unit: 'lb', motion: false, status: 'none' },
  });
  expect(res.ok(), await res.text()).toBe(true);
}

async function createScaleProduct(request: APIRequestContext): Promise<string> {
  const run = Date.now().toString(36).toUpperCase();
  const sku = `E2E-SCALE-${run}`;
  const res = await request.post('/api/v1/products', {
    data: {
      sku,
      description: `E2E scale product ${run}`,
      stock_uom: 'TON',
      base_price_ten_thousandths: 300000,
      density_lb_per_cy: '2700',
      density_volume_state: 'loose',
    },
  });
  expect(res.status(), await res.text()).toBe(201);
  const product = (await res.json()) as { id: string };

  // Stock it at the counter's branch: the completion issues what the line
  // sells, and a product with no row cannot be sold.
  const locations = (await (
    await request.get('/api/v1/locations?limit=200')
  ).json()) as { items: { id: string; code: string | null }[] };
  const branch = locations.items.find((l) => l.code === 'KEL-MAIN') ?? locations.items[0];
  const stock = await request.post('/api/v1/inventory/adjust', {
    data: { product_id: product.id, location_id: branch.id, quantity: 500, reason: 'e2e scale stock', is_delta: false },
  });
  expect(stock.ok(), await stock.text()).toBe(true);
  return sku;
}

async function openScalePanel(page: Page) {
  await page.getByTestId('pos-scale-button').click();
  await expect(page.getByTestId('scale-panel')).toBeVisible();
}

test('weigh in and out sets the line quantity and tags the sale', async ({ page, request }) => {
  await signIn(page, 'POS Scale E2E');
  const sku = await createScaleProduct(request);

  await setSimulator(request, '32000');
  await openCounter(page);
  await openScalePanel(page);

  await page.getByTestId('scale-vehicle-in').fill('E2E-TRUCK');
  await page.getByTestId('scale-weigh-in').click();
  await expect(page.getByTestId('scale-ticket')).toContainText('E2E-TRUCK');
  const ticketText = await page.getByTestId('scale-ticket').first().textContent() ?? '';
  const ticketNumber = ticketText.match(/WT-\d+/)?.[0] ?? '';
  expect(ticketNumber).toMatch(/WT-\d+/);
  await setSimulator(request, '44400');
  await page.waitForTimeout(2500);
  await addOne(page, sku);

  // The first weighable line is the only one; pick it by its value.
  const lineSelect = page.getByTestId('scale-line-out');
  const firstOptionValue = async (el: ReturnType<Page['getByTestId']>) =>
    el.evaluate((node) => {
      const opts = (node as unknown as { options: { value: string; label: string }[] }).options;
      return opts.find((o) => o.value !== '')?.value ?? '';
    });
  const value = await firstOptionValue(lineSelect);
  await pickOptionByValue(lineSelect, value);
  await pickOptionByValue(page.getByTestId('scale-ticket-out'), await firstOptionValue(page.getByTestId('scale-ticket-out')));
  await page.getByTestId('scale-weigh-out').click();

  const qtyInput = page.locator('[data-testid="pos-line-quantity"] input').first();
  await expect(qtyInput).toHaveValue('6.2');
  // The badge names the ticket the line carries, which is the weigh out
  // ticket (the weigh in keeps its own number): read the line's ticket from
  // the API and hold the badge to it.
  const badge = page.getByTestId('pos-line-weight-badge').first();
  await expect(badge).toContainText(/WT-\d+/);
  const badgeTicket = (await badge.textContent())?.match(/WT-\d+/)?.[0] ?? '';
  const sales = (await (
    await request.get('/api/v1/pos/transactions?register_id=REG-01')
  ).json()) as { items: { id: string }[] };
  const saleId = sales.items[0]?.id ?? '';
  const sale = (await (await request.get(`/api/v1/pos/transactions/${saleId}`)).json()) as {
    lines: { scale_ticket_number: string | null }[];
  };
  const lineTicket = sale.lines.find((l) => l.scale_ticket_number)?.scale_ticket_number ?? '';
  expect(badgeTicket, 'the badge names the ticket the line carries').toBe(lineTicket);

  // The scale panel is a dialog over the counter: close it before the pay
  // steps, or its overlay intercepts the payment panel's clicks.
  await page.getByTestId('scale-close').click();

  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-scale-weighed.png'), fullPage: true });

  await openPayment(page);
  await page.getByTestId('tender-cash').click();
  const total = await saleTotal(page);
  await page.getByLabel('Tender amount').fill(total.toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();

  const card = page.getByTestId('pos-complete');
  await expect(card).toContainText('Sale complete', { timeout: 15000 });
  await expect(badge).toContainText(badgeTicket);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-scale-completed.png'), fullPage: true });
});

test('manual weight tags the line as MAN WT', async ({ page, request }) => {
  await signIn(page, 'POS Scale Manual E2E');
  const sku = await createScaleProduct(request);

  await setSimulator(request, '0');
  await openCounter(page);
  await openScalePanel(page);
  await addOne(page, sku);

  const lineSelect = page.getByTestId('scale-line-manual');
  const value = await lineSelect.evaluate((el) => {
    const opts = (el as unknown as { options: { value: string; label: string }[] }).options;
    return opts.find((o) => o.value !== '')?.value ?? '';
  });
  await pickOptionByValue(lineSelect, value);
  await page.getByTestId('scale-weight-manual').fill('5000');
  await pickOptionByValue(page.getByTestId('scale-reason'), 'scale_offline');
  await page.getByTestId('scale-manual-submit').click();

  const badge = page.getByTestId('pos-line-weight-badge').first();
  await expect(badge).toContainText('MAN WT');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-scale-manual.png'), fullPage: true });
});

test('manual weight is refused while the scale is not at zero', async ({ page, request }) => {
  await signIn(page, 'POS Scale Refusal E2E');
  const sku = await createScaleProduct(request);

  await setSimulator(request, '1200');
  await openCounter(page);
  await openScalePanel(page);
  await addOne(page, sku);

  const lineSelect = page.getByTestId('scale-line-manual');
  const value = await lineSelect.evaluate((el) => {
    const opts = (el as unknown as { options: { value: string; label: string }[] }).options;
    return opts.find((o) => o.value !== '')?.value ?? '';
  });
  await pickOptionByValue(lineSelect, value);
  await page.getByTestId('scale-weight-manual').fill('5000');
  await pickOptionByValue(page.getByTestId('scale-reason'), 'scale_offline');
  await page.getByTestId('scale-manual-submit').click();

  const error = page.getByTestId('scale-error');
  await expect(error).toContainText('not at zero');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-scale-refused.png'), fullPage: true });
});
