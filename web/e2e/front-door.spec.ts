// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * End to end against the real stack (see playwright.config.ts):
 *   exactly /      the front door
 *   everything else the desk (/home, /quotes/{id}, ...)
 * Both bundles share one origin, so the sessionStorage handoff works as in
 * production. The core runs AUTH_MODE=dev over the seeded database.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

interface CatalogApp {
  key: string;
  name: string;
  enabled: boolean;
}

/** Sign in on the door with the dev sign-in and return the catalog the door fetched. */
async function signInOnDoor(page: Page, name: string): Promise<CatalogApp[]> {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  const body = (await (await catalog).json()) as { apps: CatalogApp[] };
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
  return body.apps;
}

test.describe('Front door', () => {
  test('signed out: the sign in card, then tiles that come from GET /api/v1/apps', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveTitle(/Front Door/);
    await expect(page.getByRole('heading', { name: 'Sign in to Gable' })).toBeVisible();
    await expect(page.getByLabel('Display name')).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'door-signed-out.png') });

    const apps = await signInOnDoor(page, 'Playwright Door');

    // The pinned desk tile, then one tile per enabled app the door knows an entry for.
    await expect(page.getByRole('button', { name: 'Open Gable Desk' })).toBeVisible();
    const quote = apps.find((a) => a.key === 'quote');
    const inventory = apps.find((a) => a.key === 'inventory');
    expect(quote?.enabled, 'the seeded stack has the quote app enabled').toBe(true);
    expect(inventory?.enabled, 'the seeded stack has the inventory app enabled').toBe(true);
    await expect(page.getByRole('button', { name: `Open ${quote!.name}` })).toBeVisible();
    await expect(page.getByRole('button', { name: `Open ${inventory!.name}` })).toBeVisible();

    // Every tile on the page is the desk tile or comes from the response.
    const expected = new Set(['Gable Desk', ...apps.filter((a) => a.enabled).map((a) => a.name)]);
    const labels = await page.getByRole('button', { name: /^Open / }).evaluateAll((els) =>
      els.map((e) => (e.getAttribute('aria-label') ?? '').replace(/^Open /, '')),
    );
    expect(labels.length).toBeGreaterThan(5);
    for (const l of labels) expect(expected.has(l), `tile "${l}" is in the catalog response`).toBe(true);

    await page.screenshot({ path: path.join(SHOTS_DIR, 'door-signed-in.png') });
  });

  test('the desk tile opens /home and the desk shell renders', async ({ page }) => {
    await signInOnDoor(page, 'Playwright Desk');
    await page.getByRole('button', { name: 'Open Gable Desk' }).click();
    await expect(page).toHaveURL(/\/home$/);
    await expect(page.locator('gable-app-shell')).toBeVisible();
    await expect(page.getByRole('tab', { name: 'Apps' })).toBeVisible();
    // The door's session survived the full page navigation: no sign in card, a launcher grid.
    await expect(page.getByRole('button', { name: 'Open Quotes' })).toBeVisible();
  });

  test('a tile for an app opens that app inside the desk', async ({ page }) => {
    const apps = await signInOnDoor(page, 'Playwright Tile');
    const quote = apps.find((a) => a.key === 'quote')!;
    await page.getByRole('button', { name: `Open ${quote.name}` }).click();
    await expect(page).toHaveURL(/\/quotes$/);
    await expect(page.locator('gable-app-shell')).toBeVisible();
  });

  test('tiles are filtered by the roles in the session', async ({ page }) => {
    // A token session for a sales user (unsigned: the client never verifies, and the
    // catalog is stubbed, so no core call depends on it).
    const b64 = (o: object) => Buffer.from(JSON.stringify(o)).toString('base64url');
    const exp = Math.floor(Date.now() / 1000) + 3600;
    const token = `${b64({ alg: 'none', typ: 'JWT' })}.${b64({ sub: 'sales-user', email: 'sales@example.test', roles: ['sales'], exp })}.sig`;
    await page.addInitScript((t) => {
      sessionStorage.setItem('gable.auth.session.v1', JSON.stringify({ v: 1, kind: 'token', token: t }));
    }, token);
    await page.route('**/api/v1/apps', (route) =>
      route.fulfill({
        json: {
          apps: [
            { key: 'quote', name: 'Quotes', summary: '', category: 'Sales', core: true, enabled: true, depends_on: [] },
            { key: 'pos', name: 'Point of Sale', summary: '', category: 'Front of House', core: true, enabled: true, depends_on: [] },
            { key: 'techadmin', name: 'Tech Admin', summary: '', category: 'Platform', core: true, enabled: true, depends_on: [] },
          ],
        },
      }),
    );
    await page.goto('/');
    await expect(page.getByRole('button', { name: 'Open Gable Desk' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Open Quotes' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Open Point of Sale' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Open Tech Admin' })).toHaveCount(0);
  });
});

test.describe('Desk', () => {
  test('/home renders the launcher grid at 1440x900', async ({ page }) => {
    await signInOnDoor(page, 'Playwright Home');
    await page.goto('/home');
    await expect(page.locator('gable-app-shell')).toBeVisible();
    await expect(page.getByRole('tab', { name: 'Apps' })).toHaveAttribute('aria-selected', 'true');
    const tiles = page.getByRole('button', { name: /^Open / });
    await expect(tiles.first()).toBeVisible();
    expect(await tiles.count()).toBeGreaterThan(5);
    await expect(page.getByRole('button', { name: 'Open Quotes' })).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'desk-home.png') });
  });

  test('/quotes/<seeded id> opens directly and renders that quote', async ({ page, request }) => {
    const list = await request.get('/api/v1/quotes?limit=5');
    expect(list.ok()).toBe(true);
    const quotes = (await list.json()).data as { id: string; customer_name: string }[];
    expect(quotes.length).toBeGreaterThan(0);
    const q = quotes[0];

    await signInOnDoor(page, 'Playwright Record');
    await page.goto(`/quotes/${q.id}`);
    await expect(page).toHaveURL(new RegExp(`/quotes/${q.id}$`));
    await expect(page.getByRole('heading', { name: `Quote #${q.id.slice(0, 8)}` })).toBeVisible();
    if (q.customer_name) await expect(page.getByText(q.customer_name).first()).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'desk-quote-record.png') });
  });

  test('a record URL opened cold, with no session, does not 404 at the server', async ({ page, request }) => {
    const q = ((await (await request.get('/api/v1/quotes?limit=1')).json()).data as { id: string }[])[0];
    const res = await page.goto(`/quotes/${q.id}`);
    expect(res?.status()).toBe(200);
    await expect(page.locator('gable-app-shell, gable-not-found').first()).toBeVisible();
    await expect(page.locator('gable-not-found')).toHaveCount(0);
  });
});
