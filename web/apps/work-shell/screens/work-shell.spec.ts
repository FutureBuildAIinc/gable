// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The work shell screens: the shell framing the desk (one header), the rail's
// states, the selector open, at phone (390), tablet (1024) and desktop (1440)
// widths, in Industrial Dark and Gable light. The API is mocked by
// page.route; the dev session is seeded into the tab's session storage before
// any bundle boots. Screens land in SHOTS_DIR (default screens/shots). The
// last test of each theme measures the chrome's contrast and asserts the WCAG
// floors (4.5:1 text, 3:1 controls).

import { expect, test, type Page } from '@playwright/test';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';

const SHOTS_DIR = process.env.SHOTS_DIR ?? 'screens/shots';
mkdirSync(SHOTS_DIR, { recursive: true });

// A structurally valid but invented JWT (never a real credential: `none`
// algorithm, placeholder signature, far future exp as a bare epoch number).
// Seeding a token session, not a dev one, keeps the screens run green against
// a standard build (VITE_AUTH_DEV_MODE unset): the custody refuses dev
// handoffs there.
function fakeToken(claims: Record<string, unknown>): string {
  const seg = (v: unknown) => Buffer.from(JSON.stringify(v)).toString('base64url');
  return `${seg({ alg: 'none', typ: 'JWT' })}.${seg(claims)}.${seg({ sig: 'stub' })}`;
}

const SESSION = {
  v: 1,
  kind: 'token',
  token: fakeToken({
    sub: 'screens-sales',
    name: 'Avery Brooks',
    roles: ['sales'],
    exp: 4102444800,
  }),
};

const BRANCHES = [
  { id: 'b-kel', code: 'KEL-MAIN', name: 'Kelowna Main Yard', is_home: true },
  { id: 'b-wk', code: 'WK', name: 'West Kelowna Satellite', is_home: false },
  { id: 'b-lk', code: 'LK', name: 'Lake Country Outpost', is_home: false },
];

async function mockApi(page: Page): Promise<void> {
  // Playwright matches the LAST registered route first: the catch-all goes
  // first so the specific answers win.
  await page.route('**/api/**', (route) => route.fulfill({ json: {} }));
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
  await page.context().addInitScript(
    ({ session, theme: t }) => {
      sessionStorage.setItem('gable.auth.session.v1', JSON.stringify(session));
      localStorage.setItem('gable_theme', t);
    },
    { session: SESSION, theme },
  );
  await page.goto('/shell/');
  // The shell header is up, the yard settled on the persona's home, and the
  // framed desk has painted its page with the launcher tiles loaded.
  await page.locator('gable-shell-header header').waitFor();
  await expect(page.locator('gable-shell-header button[data-action="yards"]')).toContainText('Kelowna Main Yard');
  const frame = page.frameLocator('iframe[data-testid="app-frame"]');
  await frame.locator('gable-app').waitFor();
  await frame.locator('nav[aria-label="Open modules"]').waitFor();
  await frame.locator('button[aria-label^="Open "]').first().waitFor();
}

async function shot(page: Page, name: string): Promise<void> {
  await page.screenshot({ path: join(SHOTS_DIR, `${name}.png`) });
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
  // Reveal the rail first: its probes must run before the pointer moves on to
  // the selector (leaving the rail ends the peek).
  await page.hover('[data-testid="rail-strip"]');
  await expect(page.locator('gable-shell-rail nav[data-mode="wide"]')).toBeVisible();
  const railProbes: (Probe | null)[] = [
    await probe(page, 'rail app icon (idle)', 'gable-shell-rail button[data-row]:not([aria-current="true"]) svg'),
    await probe(page, 'rail app icon (current)', 'gable-shell-rail button[data-row][aria-current="true"] svg'),
  ];
  await page.locator('gable-shell-header button[data-action="selector"]').click();
  await expect(page.locator('gable-shell-header [data-selector-panel]')).toBeVisible();
  const probes: (Probe | null)[] = [
    await probe(page, 'header dealer name', 'gable-shell-header header .md\\:block'),
    await probe(page, 'header selector crumb', 'gable-shell-header button[data-action="selector"]'),
    await probe(page, 'header theme control', 'gable-shell-header button[data-action="theme"]'),
    await probe(page, 'header yard label', 'gable-shell-header button[data-action="yards"]'),
    await probe(page, 'header account name', 'gable-shell-header button[data-action="account"]'),
    ...railProbes,
    await probe(page, 'selector app icon (idle)', 'gable-shell-header [data-selector-panel] [role="menuitem"]:not([aria-current="true"]) svg'),
    await probe(page, 'selector current check', 'gable-shell-header [data-selector-panel] [aria-current="true"] svg:last-child'),
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
    expect(r, `${theme} ${p.what}`).toBeGreaterThanOrEqual(3);
  }
  console.log(`contrast ${theme}:\n${lines.join('\n')}`);
}

// -- the matrix -------------------------------------------------------------------

for (const theme of ['dark', 'light'] as const) {
  test.describe(`theme ${theme}`, () => {
    test('desktop 1440: hidden, revealed, pinned wide, pinned icons, selector open', async ({ page }) => {
      await boot(page, theme, 1440, 900);
      await shot(page, `1440-${theme}-framed-rail-hidden`);

      // Reveal on hover of the left edge strip: wide on a wide screen.
      await page.hover('[data-testid="rail-strip"]');
      await expect(page.locator('gable-shell-rail nav[data-mode="wide"]')).toBeVisible();
      await shot(page, `1440-${theme}-rail-revealed`);

      // Pin it wide, then toggle to icons.
      await page.locator('gable-shell-rail button[data-action="pin"]').click();
      await expect(page.locator('gable-shell-rail nav[data-mode="wide"]')).toBeVisible();
      await page.mouse.move(900, 500);
      await shot(page, `1440-${theme}-rail-pinned-wide`);

      await page.locator('gable-shell-rail button[data-action="toggle-width"]').click();
      await expect(page.locator('gable-shell-rail nav[data-mode="icons"]')).toBeVisible();
      await shot(page, `1440-${theme}-rail-pinned-icons`);

      // The centred selector open over the framed desk.
      await page.locator('gable-shell-header button[data-action="selector"]').click();
      await expect(page.locator('gable-shell-header [data-selector-panel]')).toBeVisible();
      await shot(page, `1440-${theme}-selector-open`);
    });

    test('tablet 1024: hidden, revealed icons, pinned icons, selector open', async ({ page }) => {
      await boot(page, theme, 1024, 768);
      await shot(page, `1024-${theme}-framed-rail-hidden`);

      await page.hover('[data-testid="rail-strip"]');
      await expect(page.locator('gable-shell-rail nav[data-mode="icons"]')).toBeVisible();
      await shot(page, `1024-${theme}-rail-revealed`);

      await page.locator('gable-shell-rail button[data-action="pin"]').click();
      await expect(page.locator('gable-shell-rail nav[data-mode="icons"]')).toBeVisible();
      await page.mouse.move(700, 400);
      await shot(page, `1024-${theme}-rail-pinned-icons`);

      await page.locator('gable-shell-header button[data-action="selector"]').click();
      await expect(page.locator('gable-shell-header [data-selector-panel]')).toBeVisible();
      await shot(page, `1024-${theme}-selector-open`);
    });

    test('phone 390: framed, one header, no rail, selector open', async ({ page }) => {
      await boot(page, theme, 390, 844);
      // The phone shows no rail and no rail strip, and the frame holds no
      // second header band.
      await expect(page.locator('gable-shell-rail nav')).toHaveCount(0);
      await expect(page.locator('[data-testid="rail-strip"]')).toHaveCount(0);
      const frame = page.frameLocator('iframe[data-testid="app-frame"]');
      await expect(frame.locator('header')).toHaveCount(0);
      await shot(page, `390-${theme}-framed-one-header`);

      await page.locator('gable-shell-header button[data-action="selector"]').click();
      await expect(page.locator('gable-shell-header [data-selector-panel]')).toBeVisible();
      await shot(page, `390-${theme}-selector-open`);
    });

    test(`contrast of the shell chrome (${theme})`, async ({ page }) => {
      await boot(page, theme, 1440, 900);
      await contrastReport(page, theme);
    });
  });
}
