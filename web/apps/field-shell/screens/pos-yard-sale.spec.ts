// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The yard sale in the field shell (CP-12): the cashier's Sale tab frames the
// counter at phone (390) and tablet (820) widths, the framed document never
// scrolls sideways, the hotkey legend is absent below 900, every control of
// the narrow layout clears the 44px touch floor, and the new surfaces hold
// WCAG contrast in Industrial Dark and Gable light. The API is mocked by
// page.route (the stack cases, the real sale, are web/e2e/pos-yard-sale).

import { expect, test, type Frame, type Page } from '@playwright/test';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';

const SHOTS_DIR = process.env.SHOTS_DIR ?? 'screens/shots';
mkdirSync(SHOTS_DIR, { recursive: true });

// The session the core answers for the cookie (custody holds a staff session in
// memory only, so each document asks GET /api/v1/auth/session at boot).
const SESSION = { sub: 'screens-yard-sale', roles: ['cashier'] };

const BRANCHES = [
  { id: 'b-kel', code: 'KEL-MAIN', name: 'Kelowna Main Yard', is_home: true },
  { id: 'b-wk', code: 'WK', name: 'West Kelowna Satellite', is_home: false },
];

const REGISTERS = [
  { id: 'REG-01', name: 'Front till', branch_id: 'b-kel', branch_code: 'KEL-MAIN', branch_name: 'Kelowna Main Yard' },
];

const TILL = { id: 't-1', register_id: 'REG-01', cashier_id: 'c-1', status: 'open', opening_float_cents: 10000 };

const OPEN_SALE = {
  id: 's-1', number: 'POS-000049', revision: 1, branch_id: 'b-kel', register_id: 'REG-01', cashier_id: 'c-1',
  customer_id: null, currency: 'USD', subtotal_cents: 0, tax_cents: 0, total_cents: 0, change_cents: 0,
  till_session_id: 't-1', status: 'open', invoice_id: null, completed_at: null, created_at: '',
  customer_po: null, job_id: null, tenders: [], lines: [],
};

const TILES = {
  categories: [{ name: 'Lumber', count: 2 }],
  items: [
    { product_id: 'p-1', sku: 'LUM-248', description: 'SPF 2x4 8ft', uom: 'EA', unit_price_cents: 789, in_stock: '240', image_url: null },
    { product_id: 'p-2', sku: 'LUM-250', description: 'SPF 2x6 12ft', uom: 'EA', unit_price_cents: 1499, in_stock: '120', image_url: null },
  ],
  images: false,
};

async function mockApi(page: Page): Promise<void> {
  // The catch-all goes first so the specific answers win (Playwright matches
  // the LAST registered route first).
  await page.route('**/api/**', (route) => route.fulfill({ json: {} }));
  await page.route('**/api/v1/pos/registers', (route) => route.fulfill({ json: { items: REGISTERS } }));
  await page.route('**/api/v1/pos/till/current**', (route) => route.fulfill({ json: TILL }));
  await page.route('**/api/v1/pos/transactions', (route) => route.fulfill({ json: OPEN_SALE }));
  await page.route('**/api/v1/pos/tiles**', (route) => route.fulfill({ json: TILES }));
  await page.route('**/api/v1/me/branches', (route) => route.fulfill({ json: BRANCHES }));
  await page.route('**/api/v1/dealer-profile/locale', (route) => route.fulfill({ json: { locale: 'en-CA' } }));
  await page.route('**/api/v1/dealer-profile', (route) =>
    route.fulfill({
      json: { legal_name: 'Gable Building Supply Ltd.', trading_name: 'Gable Building Supply', logo_url: null, revision: 3 },
    }),
  );
  await page.route('**/api/v1/apps', (route) => route.fulfill({ json: { apps: [] } }));
}

async function boot(page: Page, theme: 'dark' | 'light', width: number, height: number): Promise<void> {
  await page.setViewportSize({ width, height });
  await mockApi(page);
  await page.route('**/api/v1/auth/session', (route) => route.fulfill({ json: SESSION }));
  await page.context().addInitScript(
    ({ theme: t }) => {
      localStorage.setItem('gable_theme', t);
    },
    { theme },
  );
  await page.goto('/field/');
  await page.locator('gable-shell-header header[data-mode="phone"]').waitFor();
  await page.locator('gable-shell-tabs nav').waitFor();
  // The cashier's only tab is Sale; it frames the counter.
  const tabs = page.locator('gable-shell-tabs button[data-tab]');
  await expect(tabs).toHaveCount(1);
  await expect(tabs.first()).toHaveAttribute('data-tab', 'sale');
  await tabs.first().click();
  const frame = page.frameLocator('iframe[data-testid="app-frame"]');
  await frame.locator('[data-testid="pos-terminal"]').waitFor();
  await frame.locator('[data-testid="pos-catalog"]').waitFor();
  await expect(frame.locator('[data-testid="pos-till-state"]')).toContainText('Till open');
}

function counterFrame(page: Page): Frame {
  const frame = page.frames().find((f) => f.url().includes('/pos'));
  if (!frame) throw new Error('the counter frame is not open');
  return frame;
}

async function assertNoSidewaysScroll(page: Page): Promise<void> {
  const over = await counterFrame(page).evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(over, 'the framed document is never wider than the frame').toBe(0);
}

async function assertTouchTargets(page: Page, width: number): Promise<void> {
  const frame = page.frameLocator('iframe[data-testid="app-frame"]');
  // The tenders live in the payment step of the sheet: open it before
  // measuring them.
  const head = frame.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
  const ids = ['catalog-scan', 'catalog-add', 'pos-till-menu-button', 'tender-cash'];
  // The pinned pay bar is the phone's Pay; at 768 and wider it is hidden by
  // design and the order summary card's Pay is the affordance.
  ids.push(width < 768 ? 'pos-pay-bar-button' : 'pos-pay');
  for (const id of ids) {
    const box = await frame.getByTestId(id).boundingBox();
    expect(box, `bounding box for ${id}`).not.toBeNull();
    expect(box!.height, `${id} height >= 44px`).toBeGreaterThanOrEqual(44);
    expect(box!.width, `${id} width >= 44px`).toBeGreaterThanOrEqual(44);
  }
}

// -- WCAG contrast over computed styles (inside the frame) ---------------------

interface Probe {
  what: string;
  color: string;
  background: string;
}

function luminance(rgb: [number, number, number]): number {
  const f = (c: number) => {
    const s = c / 255;
    return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * f(rgb[0]) + 0.7152 * f(rgb[1]) + 0.0722 * f(rgb[2]);
}

function ratio(a: [number, number, number], b: [number, number, number]): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

function parseRgb(value: string): [number, number, number] | null {
  const m = value.match(/rgba?\((\d+)[,\s]+(\d+)[,\s]+(\d+)/);
  if (!m) return null;
  return [Number(m[1]), Number(m[2]), Number(m[3])];
}

async function probe(frame: Frame, what: string, selector: string): Promise<Probe | null> {
  return frame.evaluate(
    ({ what: w, selector: s }) => {
      const el = document.querySelector(s);
      if (!el) return null;
      const color = getComputedStyle(el).color;
      let node: Element | null = el;
      let background = '';
      while (node) {
        const bg = getComputedStyle(node).backgroundColor;
        const m = bg.match(/rgba?\((\d+)[,\s]+(\d+)[,\s]+(\d+)(?:[,\s/]+([\d.]+))?/);
        if (m && (m[4] === undefined || Number(m[4]) > 0.9)) {
          background = bg;
          break;
        }
        node = node.parentElement;
      }
      return { what: w, color, background };
    },
    { what, selector },
  );
}

async function contrastReport(page: Page, theme: string): Promise<void> {
  const frame = counterFrame(page);
  const probes: (Probe | null)[] = [
    await probe(frame, 'counter title', '[data-testid="pos-title-row"] h1'),
    await probe(frame, 'till state chip', '[data-testid="pos-till-state"]'),
    await probe(frame, 'pay bar total', '[data-testid="pos-pay-bar-total"]'),
    await probe(frame, 'pay bar words', '[data-testid="pos-pay-bar-lines"]'),
    await probe(frame, 'tender button', '[data-testid="tender-cash"]'),
  ];
  for (const p of probes) {
    if (!p) continue;
    const fg = parseRgb(p.color);
    const bg = parseRgb(p.background);
    if (!fg || !bg) continue;
    const r = ratio(fg, bg);
    console.log(`${theme} ${p.what}: ${r.toFixed(2)}:1`);
    expect(r, `${theme} ${p.what}`).toBeGreaterThanOrEqual(4.5);
  }
}

// -- the matrix (390 and 820 in dark and light) --------------------------------

for (const theme of ['dark', 'light'] as const) {
  test.describe(`theme ${theme}`, () => {
    for (const [width, height] of [
      [390, 844],
      [820, 1180],
    ] as const) {
      test(`${width}: the Sale tab frames the counter and it fits`, async ({ page }) => {
        await boot(page, theme, width, height);
        const frame = page.frameLocator('iframe[data-testid="app-frame"]');

        // No sideways scroll inside the frame, the assertion that would have
        // caught the 442 against 390.
        await assertNoSidewaysScroll(page);

        // The hotkey legend is chrome below 900: hidden on a touch screen.
        await expect(frame.locator('[data-testid="pos-hotkey-legend"]')).toBeHidden();

        // The catalogue tiles are there and the catalogue precedes the panels.
        await expect(frame.locator('[data-testid="catalog-tile"]')).toHaveCount(2);
        const items = await frame.getByTestId('pos-items').boundingBox();
        const panels = await frame.getByTestId('pos-panels').boundingBox();
        expect(items!.y).toBeLessThanOrEqual(panels!.y);

        // Every control clears the touch floor.
        await assertTouchTargets(page, width);
        await page.screenshot({ path: join(SHOTS_DIR, `yard-sale-${width}-${theme}.png`) });
      });
    }

    test(`contrast of the counter's narrow surfaces (${theme})`, async ({ page }) => {
      await boot(page, theme, 390, 844);
      await contrastReport(page, theme);
    });
  });
}

test('1280: the legend is present on a desktop width', async ({ page }) => {
  await boot(page, 'dark', 1280, 900);
  const frame = page.frameLocator('iframe[data-testid="app-frame"]');
  await expect(frame.locator('[data-testid="pos-hotkey-legend"]')).toBeVisible();
});

test('390: the open Till menu opens fully on screen', async ({ page }) => {
  await boot(page, 'dark', 390, 844);
  const frame = page.frameLocator('iframe[data-testid="app-frame"]');
  await frame.getByTestId('pos-till-menu-button').click();
  const menu = frame.getByTestId('pos-till-menu-items');
  await expect(menu).toBeVisible();
  const box = await menu.boundingBox();
  expect(box, 'the open menu has a box').not.toBeNull();
  console.log(`till menu at 390: x=${box!.x} width=${box!.width}`);
  expect(box!.x, "the menu's left edge stays on screen").toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width, "the menu's right edge stays on screen").toBeLessThanOrEqual(390);
});
