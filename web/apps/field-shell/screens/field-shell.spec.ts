// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The field shell screens: the phone-first shell framing the field apps (Yard
// and Driver, one header), the bottom tab bar, the yard picker open, and the
// honest offline banner, at phone (390) and tablet (768) widths, in
// Industrial Dark and Gable light. The API is mocked by page.route; the token
// session is seeded into the tab's session storage before any bundle boots.
// Screens land in SHOTS_DIR (default screens/shots). Every control's touch
// target is checked against the 44 px floor, and the last test of each theme
// measures the chrome's contrast against the WCAG floors.

import { expect, test, type Page } from '@playwright/test';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';

const SHOTS_DIR = process.env.SHOTS_DIR ?? 'screens/shots';
mkdirSync(SHOTS_DIR, { recursive: true });

// The session the core answers for the cookie (custody holds a staff session in
// memory only, so each document asks GET /api/v1/auth/session at boot).
const SESSION = { sub: 'screens-field', roles: ['warehouse', 'driver'] };

const BRANCHES = [
  { id: 'b-kel', code: 'KEL-MAIN', name: 'Kelowna Main Yard', is_home: true },
  { id: 'b-wk', code: 'WK', name: 'West Kelowna Satellite', is_home: false },
  { id: 'b-lk', code: 'LK', name: 'Lake Country Outpost', is_home: false },
];

const ORDERS = [
  {
    id: 'ord-1042',
    number: 'SO-001042',
    customer_id: 'cust-1',
    customer_name: 'Ridgeview Framing Ltd.',
    status: 'confirmed',
    total_cents: 184500,
    invoice_ids: ['inv-1'],
    created_at: new Date(1776000000000).toISOString(),
  },
  {
    id: 'ord-1045',
    number: 'SO-001045',
    customer_id: 'cust-2',
    customer_name: 'Okanagan Timber Craft',
    status: 'confirmed',
    total_cents: 92400,
    invoice_ids: [],
    created_at: new Date(1776003600000).toISOString(),
  },
];

const DRIVERS = [
  { id: 'drv-1', name: 'Riley Vance', phone: '250-555-0142', status: 'active' },
  { id: 'drv-2', name: 'Morgan Lee', phone: '250-555-0188', status: 'active' },
];

const ROUTES = [
  {
    id: 'rt-104',
    driver_id: 'drv-1',
    vehicle_id: 'veh-2',
    vehicle_name: 'Flatbed 02 - Crane',
    scheduled_date: new Date(1776000000000).toISOString(),
    status: 'in_transit',
    stop_count: 4,
    notes: 'Framing package + engineered joists',
    revision: 1,
    stops: [],
  },
  {
    id: 'rt-108',
    driver_id: 'drv-1',
    vehicle_id: 'veh-4',
    vehicle_name: 'Boom Truck 04 - Moffett',
    scheduled_date: new Date(1776086400000).toISOString(),
    status: 'scheduled',
    stop_count: 3,
    notes: 'Cedar siding & exterior trim',
    revision: 1,
    stops: [],
  },
];

async function mockApi(page: Page): Promise<void> {
  // Playwright matches the LAST registered route first: the catch-all goes
  // first so the specific answers win.
  await page.route('**/api/**', (route) => route.fulfill({ json: {} }));
  await page.route('**/api/v1/orders**', (route) => route.fulfill({ json: { items: ORDERS, next_cursor: null } }));
  await page.route('**/api/v1/delivery/drivers**', (route) => route.fulfill({ json: { items: DRIVERS, next_cursor: null } }));
  await page.route('**/api/v1/delivery/routes**', (route) => route.fulfill({ json: { items: ROUTES, next_cursor: null } }));
  await page.route('**/api/v1/me/branches', (route) => route.fulfill({ json: BRANCHES }));
  await page.route('**/api/v1/dealer-profile/locale', (route) => route.fulfill({ json: { locale: 'en-CA' } }));
  await page.route('**/api/v1/dealer-profile', (route) =>
    route.fulfill({
      json: {
        legal_name: 'Gable Building Supply Ltd.',
        trading_name: 'Gable Building Supply',
        logo_url: null,
        revision: 3,
      },
    }),
  );
  await page.route('**/api/v1/apps', (route) =>
    route.fulfill({
      json: {
        apps: [
          { key: 'millwork', name: 'Millwork', enabled: true },
          { key: 'governance', name: 'Governance (RFCs)', enabled: true },
        ],
      },
    }),
  );
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
  await expect(page.locator('gable-shell-header button[data-action="yards"]')).toContainText('Kelowna Main Yard');
  await page.locator('gable-shell-tabs nav').waitFor();
  const frame = page.frameLocator('iframe[data-testid="app-frame"]');
  await frame.locator('gable-pick-queue').waitFor();
  await expect(frame.locator('gable-pick-queue')).toContainText('Ridgeview Framing Ltd.');
}

async function shot(page: Page, name: string): Promise<void> {
  await page.screenshot({ path: join(SHOTS_DIR, `${name}.png`) });
}

async function assertTouchTargets(page: Page): Promise<void> {
  const selectors = [
    'gable-shell-header button[data-action="yards"]',
    'gable-shell-header button[data-action="theme"]',
    'gable-shell-header button[data-action="account"]',
    'gable-shell-tabs button[data-tab="yard"]',
    'gable-shell-tabs button[data-tab="driver"]',
  ];
  for (const sel of selectors) {
    const box = await page.locator(sel).boundingBox();
    expect(box, `bounding box for ${sel}`).not.toBeNull();
    expect(box!.height, `${sel} height >= 44px`).toBeGreaterThanOrEqual(44);
    expect(box!.width, `${sel} width >= 44px`).toBeGreaterThanOrEqual(44);
  }
}

// -- WCAG contrast over computed styles ----------------------------------------

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

/** Read the first match's text colour against the first solid background up its ancestor chain. */
async function probe(page: Page, what: string, selector: string): Promise<Probe | null> {
  return page.evaluate(
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
  await page.evaluate(() => {
    Object.defineProperty(window.navigator, 'onLine', { value: false, configurable: true });
    window.dispatchEvent(new Event('offline'));
  });
  await expect(page.locator('gable-shell-banner [role="status"][data-mode="offline"]')).toBeVisible();

  const probes: (Probe | null)[] = [
    await probe(page, 'header dealer initials', 'gable-shell-header header span[aria-hidden="true"]'),
    await probe(page, 'header dealer name', 'gable-shell-header header span.truncate'),
    await probe(page, 'header yard label', 'gable-shell-header button[data-action="yards"]'),
    await probe(page, 'header theme control', 'gable-shell-header button[data-action="theme"]'),
    await probe(page, 'header account avatar', 'gable-shell-header button[data-action="account"] span[aria-hidden="true"]'),
    await probe(page, 'bottom tab active (Yard)', 'gable-shell-tabs button[data-tab="yard"]'),
    await probe(page, 'bottom tab idle (Driver)', 'gable-shell-tabs button[data-tab="driver"]'),
    await probe(page, 'offline banner words', 'gable-shell-banner [role="status"][data-mode="offline"]'),
  ];
  const lines: string[] = [];
  for (const p of probes) {
    if (!p) continue;
    const fg = parseRgb(p.color);
    const bg = parseRgb(p.background);
    if (!fg || !bg) {
      lines.push(`${p.what}: unmeasurable (${p.color} on ${p.background})`);
      continue;
    }
    const r = ratio(fg, bg);
    lines.push(`${p.what}: ${r.toFixed(2)}:1 (${p.color} on ${p.background})`);
    expect(r, `${theme} ${p.what}`).toBeGreaterThanOrEqual(4.5);
  }
  console.log(`contrast ${theme}:\n${lines.join('\n')}`);
}

// -- the matrix (390 and 768 in dark and light) ---------------------------------

for (const theme of ['dark', 'light'] as const) {
  test.describe(`theme ${theme}`, () => {
    for (const [width, height] of [
      [390, 844],
      [768, 1024],
    ] as const) {
      test(`${width}: tabs, yard picker open, framed app, offline`, async ({ page }) => {
        await boot(page, theme, width, height);
        await assertTouchTargets(page);

        const frame = page.frameLocator('iframe[data-testid="app-frame"]');
        await expect(frame.locator('header')).toHaveCount(0);
        await shot(page, `${width}-${theme}-tabs`);

        // Open the yard picker in the compact top bar.
        await page.locator('gable-shell-header button[data-action="yards"]').click();
        await expect(page.locator('gable-shell-header [role="listbox"]')).toBeVisible();
        const optionBox = await page.locator('gable-shell-header [role="option"]').first().boundingBox();
        expect(optionBox!.height).toBeGreaterThanOrEqual(44);
        await shot(page, `${width}-${theme}-yard-picker-open`);

        // Close the yard picker by picking the home yard, then switch to the Driver tab.
        await page.locator('gable-shell-header [role="option"]').first().click();
        await expect(page.locator('gable-shell-header [role="listbox"]')).toHaveCount(0);
        await page.locator('gable-shell-tabs button[data-tab="driver"]').click();
        await frame.locator('gable-driver-route-list').waitFor();
        await expect(frame.locator('header')).toHaveCount(0);
        await frame.locator('gable-select button').click();
        await frame.locator('gable-select [role="option"]', { hasText: 'Riley Vance' }).click();
        await expect(frame.locator('gable-driver-route-list')).toContainText('Flatbed 02 - Crane');
        await shot(page, `${width}-${theme}-framed-app`);

        // Honest offline banner (money never queues).
        await page.evaluate(() => {
          Object.defineProperty(window.navigator, 'onLine', { value: false, configurable: true });
          window.dispatchEvent(new Event('offline'));
        });
        const banner = page.locator('gable-shell-banner [role="status"][data-mode="offline"]');
        await expect(banner).toBeVisible();
        await expect(banner).toContainText('money never queues');
        await shot(page, `${width}-${theme}-offline`);
      });
    }

    test(`contrast of the field shell chrome (${theme})`, async ({ page }) => {
      await boot(page, theme, 390, 844);
      await contrastReport(page, theme);
    });
  });
}
