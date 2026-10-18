// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The front door ?console=1 launcher screens: the five-group role picker and
// "Continue as" from identity-tap at desktop (1440x900) and phone (390x844)
// viewports in Industrial Dark and Gable Light, served on port 18395 by
// scripts/preview-server.mjs. Every control's touch target is checked against
// the 44 px floor, and WCAG contrast (>= 4.5:1 text, >= 3.0:1 control borders)
// is measured and asserted in both themes.

import { expect, test, type Page } from '@playwright/test';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';

const SHOTS_DIR = process.env.SHOTS_DIR ?? 'screens/shots';
mkdirSync(SHOTS_DIR, { recursive: true });

const TAPPED_IDENTITY = {
  v: 1,
  displayName: 'Sam Ortiz',
  role: 'warehouse',
  yard: 'KEL-MAIN',
};

async function bootConsoleLauncher(
  page: Page,
  theme: 'dark' | 'light',
  width: number,
  height: number,
  withIdentityTap: boolean,
): Promise<void> {
  await page.setViewportSize({ width, height });
  await page.route('**/api/**', (route) => route.fulfill({ json: {} }));
  await page.context().addInitScript(
    ({ theme: t, tap }) => {
      localStorage.clear();
      sessionStorage.clear();
      localStorage.setItem('gable_theme', t);
      if (tap !== null) {
        localStorage.setItem('gable.identity-tap.v1', JSON.stringify(tap));
      }
    },
    { theme, tap: withIdentityTap ? TAPPED_IDENTITY : null },
  );
  await page.goto('/?console=1');
  await page.locator('[data-testid="role-group-counter-sales"]').waitFor();
}

async function shot(page: Page, name: string): Promise<void> {
  await page.screenshot({ path: join(SHOTS_DIR, `${name}.png`), fullPage: true });
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

interface TextProbe {
  what: string;
  color: string;
  background: string;
}

interface BorderProbe {
  what: string;
  borderColor: string;
  background: string;
}

async function probeText(page: Page, what: string, selector: string): Promise<TextProbe | null> {
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

async function probeBorder(page: Page, what: string, selector: string): Promise<BorderProbe | null> {
  return page.evaluate(
    ({ what: w, selector: s }) => {
      const el = document.querySelector(s);
      if (!el) return null;
      const borderColor = getComputedStyle(el).borderTopColor;
      const background = getComputedStyle(el).backgroundColor;
      return { what: w, borderColor, background };
    },
    { what, selector },
  );
}

for (const theme of ['dark', 'light'] as const) {
  test.describe(`console launcher (${theme})`, () => {
    for (const [width, height] of [
      [1440, 900],
      [390, 844],
    ] as const) {
      test(`${width}x${height}: five-group role picker and Continue as`, async ({ page }) => {
        await bootConsoleLauncher(page, theme, width, height, false);
        await expect(page.locator('[data-testid^="role-group-"]')).toHaveCount(5);
        await expect(page.locator('button[data-testid^="role-card-"]')).toHaveCount(9);
        await expect(page.locator('button[data-testid="console-continue-as"]')).toHaveCount(0);
        await shot(page, `console-${width}-${theme}-picker`);

        await bootConsoleLauncher(page, theme, width, height, true);
        const continueBtn = page.locator('button[data-testid="console-continue-as"]');
        await expect(continueBtn).toBeVisible();
        await expect(continueBtn).toContainText('Continue as Sam Ortiz');
        await expect(continueBtn).toContainText('Gable Field');
        const continueBox = await continueBtn.boundingBox();
        expect(continueBox).not.toBeNull();
        expect(continueBox!.height).toBeGreaterThanOrEqual(44);

        const cards = page.locator('button[data-testid^="role-card-"]');
        const count = await cards.count();
        for (let i = 0; i < count; i++) {
          const box = await cards.nth(i).boundingBox();
          expect(box).not.toBeNull();
          expect(box!.height).toBeGreaterThanOrEqual(44);
        }
        await shot(page, `console-${width}-${theme}-continue-as`);
      });
    }

    test(`WCAG contrast of the ?console=1 launcher (${theme})`, async ({ page }) => {
      await bootConsoleLauncher(page, theme, 1440, 900, true);

      const textProbes: (TextProbe | null)[] = [
        await probeText(page, 'launcher heading', 'h1'),
        await probeText(page, 'continue-as name', 'button[data-testid="console-continue-as"] .text-white'),
        await probeText(page, 'group heading', '#group-heading-counter-sales'),
        await probeText(page, 'role card title', 'button[data-testid="role-card-sales"] .text-white'),
        await probeText(page, 'role card line', 'button[data-testid="role-card-sales"] p'),
        await probeText(page, 'surface chip text', 'button[data-testid="role-card-sales"] .bg-surface-3'),
      ];
      for (const p of textProbes) {
        expect(p).not.toBeNull();
        const fg = parseRgb(p!.color);
        const bg = parseRgb(p!.background);
        expect(fg, `fg for ${p!.what}`).not.toBeNull();
        expect(bg, `bg for ${p!.what}`).not.toBeNull();
        const r = ratio(fg!, bg!);
        expect(r, `${theme} text ${p!.what} (${p!.color} on ${p!.background})`).toBeGreaterThanOrEqual(4.5);
      }

      const borderProbes: (BorderProbe | null)[] = [
        await probeBorder(page, 'continue-as border', 'button[data-testid="console-continue-as"]'),
        await probeBorder(page, 'role card border', 'button[data-testid="role-card-sales"]'),
      ];
      for (const b of borderProbes) {
        expect(b).not.toBeNull();
        const border = parseRgb(b!.borderColor);
        const bg = parseRgb(b!.background);
        expect(border, `border for ${b!.what}`).not.toBeNull();
        expect(bg, `bg for ${b!.what}`).not.toBeNull();
        const r = ratio(border!, bg!);
        expect(r, `${theme} control border ${b!.what} (${b!.borderColor} on ${b!.background})`).toBeGreaterThanOrEqual(3.0);
      }
    });
  });
}
