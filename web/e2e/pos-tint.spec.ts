// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from './font-guard';
import path from 'path';
import fs from 'fs';
import { pickOption, pickOptionByValue } from './app-controls';
import { openCounter } from './pos-till';

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

/**
 * Sets the dealer's tint charge rule on the counter settings (owner and admin,
 * audited): the flat figure per container and a tier that covers a heavy mix.
 * The walk then shows the charge on the tinted line, which is what the dealer
 * asked for: the counter prices the tint, not the base alone.
 */
async function tintChargeRule(request: APIRequestContext): Promise<void> {
  const settings = (await (await request.get('/api/v1/pos/settings')).json()) as {
    revision: number;
    rules: {
      check_limit_cents: number | null;
      check_id_required: { mode: string; over_cents: number | null };
      restocking_fees: Record<string, number>;
    };
  };
  const put = await request.put('/api/v1/pos/settings', {
    data: {
      revision: settings.revision,
      rules: {
        check_limit_cents: settings.rules.check_limit_cents,
        check_id_required: settings.rules.check_id_required,
        restocking_fees: settings.rules.restocking_fees,
        tint_charge: {
          flat_cents: 250,
          tiers: [{ base_tier: '', min_colorant_oz: '2.0000', charge_cents: 4000 }],
        },
        tinted_no_return: false,
      },
    },
  });
  expect(put.status(), await put.text()).toBe(200);
}

/**
 * Seeds a paint product with stock at the counter's branch: a tinted can is
 * paint, so the walk's screens read true (a lumber product tinted as paint
 * reads false on the receipt and in the cart).
 */
async function paintProduct(request: APIRequestContext): Promise<{ id: string; sku: string }> {
  const run = Date.now().toString(36).slice(-5).toUpperCase();
  const sku = `PT-INT-${run}`;
  const created = await request.post('/api/v1/products', {
    data: {
      sku,
      description: 'Interior Latex Eggshell Base, 1 Gallon',
      stock_uom: 'GAL',
      base_price_ten_thousandths: 725000,
    },
  });
  expect(created.status(), await created.text()).toBe(201);
  const product = (await created.json()) as { id: string };
  const locations = (await (await request.get('/api/v1/locations?limit=200')).json()) as {
    items: { id: string; code: string | null }[];
  };
  const branch = locations.items.find((l) => l.code === 'KEL-MAIN') ?? locations.items[0];
  const stocked = await request.post('/api/v1/inventory/adjust', {
    data: {
      product_id: product.id,
      location_id: branch.id,
      quantity: 50,
      reason: 'initial paint stock for counter tint walk',
      is_delta: false,
    },
  });
  expect(stocked.status(), await stocked.text()).toBe(200);
  return { id: product.id, sku };
}

/** Seeds one tint system, a base linked to the product, a colorant, a colour and a gallon formula. */
async function seedTintCatalogue(request: APIRequestContext, productId: string) {
  const run = Date.now().toString(36).slice(-5).toUpperCase();
  const system = (await (await request.post('/api/v1/tint/systems', {
    data: { code: `GEN-${run}`, name: 'Waterborne Colorant', manufacturer: 'Harbor Coatings', unit_denominator: 384 },
  })).json()) as { id: string };
  expect(system.id).toBeTruthy();
  const base = (await (await request.post('/api/v1/tint/bases', {
    data: { tint_system_id: system.id, code: 'DEEP', name: 'Deep base', product_id: productId, fill_volume_oz: 122 },
  })).json()) as { id: string };
  const colorant = (await (await request.post('/api/v1/tint/colorants', {
    data: { tint_system_id: system.id, code: 'B1', name: 'Lamp Black' },
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

test('the counter: a tinted paint line, its receipt and its mis tint return', async ({ page, request }) => {
  await signIn(page, 'Alex Rivera');
  const product = await paintProduct(request);
  await seedTintCatalogue(request, product.id);
  await tintChargeRule(request);

  await openCounter(page);

  // Ring the base paint, then tint it.
  await page.getByTestId('catalog-search').fill(product.sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(page.locator('[data-testid="pos-lines"] li')).toHaveCount(1, { timeout: 10000 });
  await page.getByTestId('pos-line-tint-button').click();
  await expect(page.getByRole('heading', { name: 'Tint paint' })).toBeVisible();

  await page.getByTestId('tint-color-search').fill('Essex');
  await page.getByTestId('tint-color-results').getByRole('option', { name: /Essex Green/ }).click();
  await expect(page.getByTestId('tint-components')).toBeVisible();
  await expect(page.getByTestId('tint-components')).toContainText('B1 Lamp Black');
  await expect(page.getByTestId('tint-price')).toContainText('$75.00 (includes the $2.50 tint charge)');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-tint-dialog.png'), fullPage: true });

  await page.getByTestId('tint-add').click();
  await expect(page.getByTestId('pos-line-tint')).toContainText('Essex Green');
  await expect(page.getByTestId('pos-line-tint')).toContainText('1 OZ + 12/32');
  await expect(page.locator('[data-testid="pos-lines"] li')).toHaveCount(1);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-tint-line.png'), fullPage: true });

  // Pay in cash.
  const total = Number(((await page.getByTestId('summary-total').textContent()) ?? '').replace(/[$,\s]/g, ''));
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
  await page.getByTestId('tender-cash').click();
  await page.getByLabel('Tender amount').fill(total.toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });

  // The books: the sale's one line carries the frozen tint and the dealer's tint charge.
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { id: string; status: string }[];
  };
  const saleID = sales.items[0].id;
  const full = (await (await request.get(`/api/v1/pos/transactions/${saleID}`)).json()) as {
    lines: Array<{
      unit_price_ten_thousandths: number | null;
      tint: { color_name: string; color_code: string; tint_charge_cents: number | null; components: Array<{ amount: { whole_oz: number; increments_coarse: number; unit_denominator: number } }> } | null;
    }>;
  };
  expect(full.lines, 'the tinted line replaced the scanned plain base line').toHaveLength(1);
  const tint = full.lines[0].tint;
  expect(tint, 'the sale line carries its tint').toBeTruthy();
  expect(tint!.color_name).toBe('Essex Green');
  expect(tint!.components[0].amount).toEqual({ whole_oz: 1, increments_coarse: 12, increments_fine: 0, unit_denominator: 384 });
  // The dealer's charge rides the line: the mix is 1.375 oz, under the tier's
  // floor, so the flat figure is the charge ($72.50 base + $2.50 tint = $75.00).
  expect(tint!.tint_charge_cents, 'the tinted line carries the dealer charge').toBe(250);
  expect(full.lines[0].unit_price_ten_thousandths).toBe(750000);

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

/**
 * The tint catalogue screen (row CP-16, items 341 and 366): the walk makes
 * its own system and an unlinked base through the API, links the base to the
 * paint product on the Tint Catalogue screen, then rings a charged tinted sale
 * at the counter. It never relies on the seed's tint rows. Shots in light and
 * charcoal at 1440 and 390.
 */
test('the tint catalogue screen links a base and a tinted sale rings', async ({ page, request }) => {
  await signIn(page, 'Alex Rivera');
  const product = await paintProduct(request);
  await tintChargeRule(request);
  const run = Date.now().toString(36).slice(-5).toUpperCase();
  const system = (await (await request.post('/api/v1/tint/systems', {
    data: { code: `CAT-${run}`, name: `Catalogue walk ${run}`, manufacturer: 'Harbor Coatings', unit_denominator: 384 },
  })).json()) as { id: string };

  // Before the link the base answers no product, and the screen says so.
  await page.goto('/admin/tint-catalogue');
  await expect(page.getByRole('heading', { name: 'Tint catalogue' })).toBeVisible();
  await pickOption(page.getByTestId('tint-system-pick'), `Catalogue walk ${run} (CAT-${run})`);
  await expect(page.getByTestId('tint-system-pick')).toContainText(`CAT-${run}`);
  await page.getByTestId('tint-new-base-code').fill('DEEP');
  await page.getByTestId('tint-new-base-name').fill('Deep base');
  await page.getByTestId('tint-add-base').click();
  await expect(page.getByTestId('tint-base-link-DEEP')).toContainText('Not linked to a product');

  // The screen adds the colorant and the colour.
  await page.getByTestId('tint-new-colorant-code').fill('B1');
  await page.getByTestId('tint-new-colorant-name').fill('Lamp Black');
  await page.getByTestId('tint-add-colorant').click();
  await expect(page.getByTestId('tint-colorant-list')).toContainText('Lamp Black');
  await page.getByTestId('tint-new-color-code').fill('HC-188');
  await page.getByTestId('tint-new-color-name').fill('Essex Green');
  await page.getByTestId('tint-add-color').click();
  await expect(page.getByTestId('tint-color-list')).toContainText('Essex Green');

  // The formula: Essex Green on the deep base, one colorant.
  await pickOption(page.getByTestId('tint-formula-color'), 'Essex Green');
  await pickOption(page.getByTestId('tint-formula-base'), 'Deep base (DEEP)');
  await page.getByTestId('tint-formula-add-colorant').click();
  await pickOption(page.getByTestId('tint-formula-colorant-0'), 'Lamp Black');
  await page.getByTestId('tint-formula-whole-0').fill('1');
  await page.getByTestId('tint-formula-coarse-0').fill('12');
  await page.getByTestId('tint-add-formula').click();
  await expect(page.getByTestId('tint-success')).toContainText('Formula added');

  // The link: find the paint product and pick it.
  await page.getByTestId('tint-base-find-DEEP').fill(product.sku);
  await page.getByTestId(`tint-base-pick-DEEP-${product.sku}`).click();
  await expect(page.getByTestId('tint-base-link-DEEP')).toContainText(product.sku);
  const bases = (await (await request.get(`/api/v1/tint/bases?tint_system_id=${system.id}`)).json()) as {
    items: { code: string; product_id: string | null }[];
  };
  expect(bases.items.find((b) => b.code === 'DEEP')?.product_id, 'the link is stored').toBe(product.id);

  // Shots: the screen in light and charcoal at 1440 and 390.
  const shots = path.join(SHOTS_DIR, 'cp16');
  fs.mkdirSync(shots, { recursive: true });
  for (const [size, w, h] of [['1440', 1440, 900], ['390', 390, 844]] as const) {
    await page.setViewportSize({ width: w, height: h });
    for (const mode of ['light', 'charcoal'] as const) {
      const stored = await page.evaluate(() => localStorage.getItem('gable_theme'));
      const isLight = stored === 'light';
      if ((mode === 'light') !== isLight) await page.locator('gable-theme-toggle button').first().click();
      await page.screenshot({ path: path.join(shots, `tint-catalogue-${mode}-${size}.png`), fullPage: true });
    }
  }
  await page.setViewportSize({ width: 1440, height: 900 });

  // The counter rings a tinted sale on the linked base.
  await openCounter(page);
  await page.getByTestId('catalog-search').fill(product.sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(page.locator('[data-testid="pos-lines"] li')).toHaveCount(1, { timeout: 10000 });
  await page.getByTestId('pos-line-tint-button').click();
  await page.getByTestId('tint-color-search').fill('Essex');
  await page.getByTestId('tint-color-results').getByRole('option', { name: /Essex Green/ }).click();
  await expect(page.getByTestId('tint-components')).toContainText('B1 Lamp Black');
  await expect(page.getByTestId('tint-price')).toContainText('includes the $2.50 tint charge');
  await page.getByTestId('tint-add').click();
  await expect(page.getByTestId('pos-line-tint')).toContainText('1 OZ + 12/32');
  const total = Number(((await page.getByTestId('summary-total').textContent()) ?? '').replace(/[$,\s]/g, ''));
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await page.getByTestId('tender-cash').click();
  await page.getByLabel('Tender amount').fill(total.toFixed(2));
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });
});
