// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page, type APIRequestContext } from './font-guard';

/**
 * The configured item (QE-06) against the real stack (see
 * playwright.config.ts): the server prices a configuration from the stored
 * options, the same configuration always gives the same SKU and the same
 * product row, a quote line that carries a configuration is priced by the
 * server and keeps the configuration, and the configured row is a product the
 * dealer never holds.
 */

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

/** The catalog carries the option the price is computed from. */
async function seedOption(request: APIRequestContext, run: string) {
  const door = await request.post('/api/v1/millwork/options', {
    data: {
      category: 'door_type',
      name: `E2E Configured Door ${run}`,
      price_adjustment_cents: 0,
      base_price_cents: 25000,
      base_cost_cents: 15000,
    },
  });
  expect(door.status(), await door.text()).toBe(201);
  const material = await request.post('/api/v1/millwork/options', {
    data: {
      category: 'material',
      name: `E2E Configured Material ${run}`,
      price_adjustment_cents: 15000,
      base_price_cents: 0,
      base_cost_cents: 8000,
    },
  });
  expect(material.status(), await material.text()).toBe(201);
  return {
    door: (await door.json()).name as string,
    material: (await material.json()).name as string,
  };
}

test.describe('The configured item is priced by the server', () => {
  test('the estimate and the build-sku agree, and the same configuration gives the same row', async ({ page, request }) => {
    await signIn(page, 'Playwright Configured');
    const run = Date.now().toString(36).toUpperCase();
    const names = await seedOption(request, run);

    const configuration = {
      product_type: 'Door',
      selections: { door_type: names.door, material: names.material },
      width: 36,
      height: 80,
    };

    // The estimate route: the price is the stored options' sum, in the
    // wire's integer units.
    const estimate = await request.post('/api/v1/millwork/estimate', { data: configuration });
    expect(estimate.status(), await estimate.text()).toBe(200);
    const priced = await estimate.json();
    expect(priced.unit_price_ten_thousandths).toBe((25000 + 15000) * 100);
    expect(priced.unit_cost_ten_thousandths).toBe((15000 + 8000) * 100);
    expect(priced.sku).toMatch(/^NS-DR-[0-9A-F]{12}$/);
    expect(priced.description).toContain(names.door);
    expect(priced.description).toContain(names.material);

    // The build-sku route: the same price, from the same catalog.
    const build = await request.post('/api/v1/configurator/build-sku', { data: configuration });
    expect(build.status(), await build.text()).toBe(200);
    const built = await build.json();
    expect(built.unit_price_ten_thousandths).toBe(priced.unit_price_ten_thousandths);
    expect(built.unit_cost_ten_thousandths).toBe(priced.unit_cost_ten_thousandths);
    expect(built.sku).toBe(priced.sku);
    expect(built.product_id).toBe(priced.product_id);

    // The same configuration again: the same SKU and the same product row.
    const again = await request.post('/api/v1/configurator/build-sku', { data: configuration });
    expect(again.status()).toBe(200);
    const repeated = await again.json();
    expect(repeated.sku).toBe(built.sku);
    expect(repeated.product_id).toBe(built.product_id);

    // The configured row is a product the dealer never holds: it carries
    // the non-stock mark and no stock.
    const product = await request.get(`/api/v1/products/${built.product_id}`);
    expect(product.status(), await product.text()).toBe(200);
    expect((await product.json()).is_non_stock).toBe(true);

    // The counter's stocked search does not offer it.
    const search = await request.get(`/api/v1/pos/products/search?q=${encodeURIComponent(built.sku)}`);
    expect(search.status()).toBe(200);
    const hits = (await search.json()) as { items?: { sku: string }[] }[];
    const items = Array.isArray(hits) ? hits : (hits.items ?? []);
    expect(items.some((p) => p.sku === built.sku)).toBe(false);
  });

  test('a quote line that carries a configuration is priced by the server', async ({ page, request }) => {
    await signIn(page, 'Playwright Configured');
    const run = Date.now().toString(36).toUpperCase();
    const names = await seedOption(request, run);
    const configuration = {
      product_type: 'Door',
      selections: { door_type: names.door, material: names.material },
      width: 36,
      height: 80,
    };
    const build = await request.post('/api/v1/configurator/build-sku', { data: configuration });
    expect(build.status(), await build.text()).toBe(200);
    const item = await build.json();

    const customers = (await (await request.get('/api/v1/customers?limit=200')).json()) as {
      items: { id: string; name: string; account_number: string }[];
    };
    const customer = customers.items.find((c) => !c.account_number.startsWith('E2E-'))!;

    // The client sends a price of its own: one cent. The server ignores it
    // and prices the line from the configuration.
    const created = await request.post('/api/v1/quotes', {
      data: {
        customer_id: customer.id,
        delivery_type: 'pickup',
        lines: [
          {
            product_id: item.product_id,
            sku: item.sku,
            description: item.description,
            quantity: '1',
            uom: 'EA',
            unit_price_ten_thousandths: 1,
            configuration_json: item.configuration_json,
          },
        ],
      },
    });
    expect(created.status(), await created.text()).toBe(201);
    const quote = await created.json();
    expect(quote.lines[0].unit_price_ten_thousandths).toBe(item.unit_price_ten_thousandths);
    expect(quote.lines[0].configuration_json).toEqual(item.configuration_json);
    // The line shows the item as a short description.
    expect(quote.lines[0].description).toContain(names.door);
  });

  test('the desk configures an item beside the quote and adds it', async ({ page, request }) => {
    await signIn(page, 'Playwright Configured');
    const run = Date.now().toString(36).toUpperCase();
    const names = await seedOption(request, run);

    const customers = (await (await request.get('/api/v1/customers?limit=200')).json()) as {
      items: { id: string; name: string; account_number: string }[];
    };
    const customer = customers.items.find((c) => !c.account_number.startsWith('E2E-'))!;

    await page.goto('/quotes/new');
    await expect(page.getByRole('heading', { name: 'New Quote' })).toBeVisible();
    await page.getByPlaceholder('Select Customer...').click();
    await page.getByText(customer.name, { exact: true }).first().click();

    // The Configure item entry opens the configurator beside the quote.
    await page.getByTestId('configure-door').click();
    await expect(page.getByTestId('configurator-dialog')).toBeVisible();

    // The configurator prices on the server: the price appears once the
    // dealer has chosen a door type. The door type carries the door's base,
    // so the price is the base alone.
    await page.getByRole('button', { name: names.door }).click();
    await expect(page.getByTestId('estimate-price')).toHaveText('$250.00');

    // The dealer adds it: the server's price and the server's product row
    // travel with it.
    await page.getByTestId('add-to-order').click();
    await expect(page.getByTestId('configurator-dialog')).toBeHidden();
    // The line the quote carries names the item: the short description the
    // server wrote.
    await expect(page.getByText(names.door).first()).toBeVisible();

    const created = page.waitForResponse(
      (r) => new URL(r.url()).pathname === '/api/v1/quotes' && r.request().method() === 'POST',
    );
    await page.getByRole('button', { name: 'Save draft' }).click();
    const res = await created;
    expect(res.status(), await res.text()).toBe(201);
    const quote = await res.json();
    expect(quote.lines).toHaveLength(1);
    expect(quote.lines[0].unit_price_ten_thousandths).toBe(25000 * 100);
    expect(quote.lines[0].configuration_json).toBeTruthy();
  });
});
