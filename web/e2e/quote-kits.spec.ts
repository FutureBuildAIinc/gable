// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';
import path from 'path';
import fs from 'fs';

/**
 * A kit on a quote: one kit line priced as a whole, with a component line per
 * part of the kit's definition beneath it (ADR 0005 sections 2.1 and 2.6).
 * The desk marks the kit, shows its parts indented under it, and carries the
 * cost of the parts, so a dealer quotes a kit below cost with the margin to
 * show for it.
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

// Unique per run, so a database that already holds earlier runs still gives a clean kit.
const RUN = Date.now().toString(36).toUpperCase();
let counter = 0;
function unique(suffix: string): { sku: string; description: string } {
  counter += 1;
  return { sku: `E2E-KIT-${RUN}-${counter}-${suffix}`, description: `E2E Kit ${RUN} ${counter} ${suffix}` };
}

interface ProductWire {
  id: string;
  sku: string;
  description: string;
  stock_uom: string;
  base_price_ten_thousandths: number;
  average_unit_cost_ten_thousandths: number;
  is_kit: boolean;
  revision: number;
}

/** A seeded product that carries a cost, so a kit's rolled up cost is not zero. */
async function costedParts(request: import('@playwright/test').APIRequestContext): Promise<ProductWire[]> {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: ProductWire[] };
  const parts = products.items.filter((p) => !p.sku.startsWith('E2E-') && !p.is_kit && p.average_unit_cost_ten_thousandths > 0);
  if (parts.length < 2) throw new Error('the catalogue needs two seeded products with a cost');
  return parts.slice(0, 2);
}

async function seededCustomer(request: import('@playwright/test').APIRequestContext) {
  const customers = (await (await request.get('/api/v1/customers?limit=200')).json()) as { items: { id: string; name: string; account_number: string }[] };
  return customers.items.find((c) => !c.account_number.startsWith('E2E-'))!;
}

test.describe('A kit on a quote', () => {
  test('make a product a kit on its page, quote it, see its parts and cost, and convert it', async ({ page, request }) => {
    const parts = await costedParts(request);
    const customer = await seededCustomer(request);
    const kit = unique('KIT');
    const created = await request.post('/api/v1/products', {
      data: { sku: kit.sku, description: kit.description, stock_uom: 'EA', base_price_ten_thousandths: 2000000 },
    });
    expect(created.status(), await created.text()).toBe(201);
    const kitProduct = (await created.json()) as ProductWire;
    expect(kitProduct.is_kit).toBe(false);

    await signIn(page, 'Playwright Kits');

    // The product page: the Kit Components tab makes the product a kit.
    await page.goto(`/inventory/${kitProduct.id}`);
    await expect(page.getByRole('heading', { name: kit.sku })).toBeVisible();
    await page.getByRole('button', { name: 'Kit Components' }).click();
    await expect(page.getByText('No parts yet. This product is not a kit.')).toBeVisible();

    for (const part of parts) {
      await page.getByPlaceholder('Search by SKU or description...').fill(part.sku);
      await page.locator('div.cursor-pointer', { hasText: part.sku }).first().click();
      await page.locator('[data-testid="kit-new-quantity"]').fill('2');
      await page.locator('[data-testid="kit-add-part"]').click();
      await expect(page.locator('[data-testid="kit-part"]')).toHaveCount(parts.indexOf(part) + 1);
    }
    await page.screenshot({ path: path.join(SHOTS_DIR, 'kit-components-tab.png') });

    const saved = page.waitForResponse((r) => new URL(r.url()).pathname.endsWith('/kit-components') && r.request().method() === 'PUT');
    await page.locator('[data-testid="kit-save"]').click();
    const savedRes = await saved;
    expect(savedRes.status(), await savedRes.text()).toBe(200);
    expect(savedRes.request().postDataJSON().components).toHaveLength(2);

    // The product now reads as a kit.
    const after = (await (await request.get(`/api/v1/products/${kitProduct.id}`)).json()) as ProductWire;
    expect(after.is_kit).toBe(true);
    const definition = (await (await request.get(`/api/v1/products/${kitProduct.id}/kit-components`)).json()) as {
      components: { component_product_id: string; quantity: string }[];
    };
    expect(definition.components.map((c) => c.component_product_id)).toEqual(parts.map((p) => p.id));

    // The quote: the builder marks the kit and shows its parts under it.
    await page.goto('/quotes/new');
    await page.getByPlaceholder('Select Customer...').click();
    await page.getByText(customer.name, { exact: true }).first().click();
    await page.getByPlaceholder('Search SKU or Desc...').fill(kit.sku);
    await page.locator('div.cursor-pointer', { hasText: kit.sku }).first().click();
    await page.locator('gable-line-item-editor input[type="number"]').first().fill('3');
    await page.getByRole('button', { name: 'Add', exact: true }).click();

    await expect(page.locator('[data-testid="quote-kit-mark"]')).toHaveCount(1);
    await expect(page.locator('[data-testid="quote-kit-component"]')).toHaveCount(2);
    // The rolled up cost is the parts' cost for the quantity on the line:
    // 3 kits x 2 of each part.
    const expectedCostCents = parts.reduce(
      (sum, p) => sum + Math.round((3 * 2 * p.average_unit_cost_ten_thousandths) / 100),
      0,
    );
    await expect(page.locator('[data-testid="quote-kit-cost"]')).toContainText(`$${(expectedCostCents / 100).toFixed(2)}`);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'quote-builder-kit.png') });

    const createdQuote = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/quotes' && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Save draft' }).click();
    const quoteRes = await createdQuote;
    expect(quoteRes.status(), await quoteRes.text()).toBe(201);
    const quote = await quoteRes.json();
    // One kit line and one component line per part; the request sent only the
    // kit line, so the server wrote the parts.
    expect(quoteRes.request().postDataJSON().lines).toHaveLength(1);
    expect(quote.lines).toHaveLength(3);
    const kitLine = quote.lines.find((l: { line_type: string }) => l.line_type === 'kit');
    const componentLines = quote.lines.filter((l: { line_type: string }) => l.line_type === 'component');
    expect(kitLine).toBeTruthy();
    expect(componentLines).toHaveLength(2);
    for (const component of componentLines) {
      expect(component.parent_line_id).toBe(kitLine.id);
      expect(component.quantity).toBe('6');
      expect(component.line_total_cents).toBe(0);
    }

    // The quote page: the kit is marked, its parts sit indented under it.
    await expect(page).toHaveURL(new RegExp(`/quotes/${quote.id}$`));
    await expect(page.locator('[data-testid="quote-kit-mark"]')).toHaveCount(1);
    await expect(page.locator('[data-testid="quote-kit-component"]')).toHaveCount(2);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'quote-detail-kit.png') });

    // Convert: the order carries the kit and its parts, and the margin on the
    // quote is the margin on the order.
    const convert = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${quote.id}/convert`));
    await page.getByRole('button', { name: /Convert to Order/ }).click();
    const converted = await convert;
    expect(converted.status(), await converted.text()).toBe(201);
    const order = await converted.json();
    expect(order.lines.filter((l: { line_type: string }) => l.line_type === 'kit')).toHaveLength(1);
    expect(order.lines.filter((l: { line_type: string }) => l.line_type === 'component')).toHaveLength(2);

    // The order page: the kit is marked, its parts sit indented under it.
    await expect(page).toHaveURL(new RegExp(`/orders/${order.id}$`));
    await expect(page.locator('[data-testid="order-kit-mark"]')).toHaveCount(1);
    await expect(page.locator('[data-testid="order-kit-component"]')).toHaveCount(2);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'order-detail-kit.png') });

    // The quote's margin is the order's margin, to the cent: both count the
    // parts' cost, never the kit product's own.
    const quoteRead = (await (await request.get(`/api/v1/quotes/${quote.id}`)).json()) as { margin_total_cents: number; total_cents: number };
    const orderRead = (await (await request.get(`/api/v1/orders/${order.id}`)).json()) as { total_margin_cents: number; subtotal_cents: number };
    expect(quoteRead.total_cents).toBe(orderRead.subtotal_cents);
    expect(quoteRead.margin_total_cents).toBe(orderRead.total_margin_cents);
    expect(quoteRead.margin_total_cents).toBe(orderRead.subtotal_cents - expectedCostCents);
  });

  test('the seeded kit quotes and converts with its parts and a positive margin', async ({ page, request }) => {
    const customer = await seededCustomer(request);
    const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: ProductWire[] };
    const kit = products.items.find((p) => p.sku === 'KIT-FRAME-8');
    expect(kit, 'the demo seed carries a framing kit').toBeTruthy();
    const definition = (await (await request.get(`/api/v1/products/${kit!.id}/kit-components`)).json()) as {
      components: { sku: string; quantity: string }[];
    };
    expect(definition.components.length).toBeGreaterThanOrEqual(2);

    const made = await (await request.post('/api/v1/quotes', {
      data: {
        customer_id: customer.id,
        lines: [{ product_id: kit!.id, quantity: '2', uom: kit!.stock_uom, unit_price_ten_thousandths: 890000 }],
      },
    })).json();
    await signIn(page, 'Playwright Seeded Kit');

    await page.goto(`/quotes/${made.id}`);
    await expect(page.getByRole('heading', { name: `Quote ${made.number}` })).toBeVisible();
    await expect(page.locator('[data-testid="quote-kit-mark"]')).toHaveCount(1);
    await expect(page.locator('[data-testid="quote-kit-component"]')).toHaveCount(definition.components.length);

    const convert = page.waitForResponse((r) => r.url().endsWith(`/api/v1/quotes/${made.id}/convert`));
    await page.getByRole('button', { name: /Convert to Order/ }).click();
    const converted = await convert;
    expect(converted.status(), await converted.text()).toBe(201);
    const order = await converted.json();
    expect(order.lines.filter((l: { line_type: string }) => l.line_type === 'component')).toHaveLength(definition.components.length);
    // The kit sells for 89.00 and its parts cost 62.40, so the margin is
    // 26.60 on the kit and 53.20 on two of them.
    const orderRead = (await (await request.get(`/api/v1/orders/${order.id}`)).json()) as { total_margin_cents: number };
    expect(orderRead.total_margin_cents).toBe(5320);
  });
});
