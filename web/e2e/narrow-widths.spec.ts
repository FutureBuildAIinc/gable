// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';

/**
 * The desk at 390 (CP polish B, fix round 2): the document is never wider than
 * the viewport on the pages the walks found spilling. The header fits, the
 * three admin pages, the Daily Till with its end of day, and the counter.
 */
test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByText(name, { exact: true }).first().click();
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

const PAGES = [
  '/home', '/orders', '/accounts', '/reports/daily-till',
  '/admin/counter-rules', '/pos/large-cash', '/admin/offline-counter', '/pos',
];

test('the document is 390 wide or less on every page the walks found spilling', async ({ page }) => {
  await signIn(page, 'Morgan Hale');
  for (const path of PAGES) {
    await page.goto(path);
    await page.waitForLoadState('networkidle');
    const width = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(width, path).toBeLessThanOrEqual(390);
  }
});

test('the narrow header offers a search icon, not a sliver of a field', async ({ page }) => {
  await signIn(page, 'Morgan Hale');
  await page.goto('/orders');
  await expect(page.getByTestId('header-search-open')).toBeVisible();
  await expect(page.locator('header input[aria-label="Search everything"]')).toBeHidden();
});
