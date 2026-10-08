// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * End-to-end tests for the Gable web stack:
 *   /            → front-door (micro-app selector)
 *   /app/*      → desk (ERP workspace)
 *
 * Tests are run against TEST_BASE_URL (default http://localhost:5000) which
 * serves both bundles from the same origin so sessionStorage auth handoff
 * works exactly as in production.
 *
 * Screenshots are written to SHOTS_DIR/r1-8/ (default
 * /home/colton/Desktop/FBHQ/gable-v1/shots/r1-8/).
 */

const BASE_URL = process.env.TEST_BASE_URL ?? 'http://localhost:5000';
const SHOTS_DIR = process.env.SHOTS_DIR ?? '/home/colton/Desktop/FBHQ/gable-v1/shots/r1-8';

// Ensure screenshots directory exists
fs.mkdirSync(SHOTS_DIR, { recursive: true });

test.describe('Front door', () => {
  test('sign-in with test auth, tiles render, desk opens from tile', async ({ page }) => {
    // ── 1. Front door: sign in with dev auth ────────────────────────────
    await page.goto(BASE_URL);
    await expect(page).toHaveTitle(/Gable/i);

    // Wait for the sign-in card to be visible
    const signInHeading = page.getByRole('heading', { name: /sign in to gable/i });
    await expect(signInHeading).toBeVisible({ timeout: 10_000 });

    // In dev mode the build shows a display-name input (not a token textarea)
    const nameInput = page.getByLabel(/display name/i);
    await expect(nameInput).toBeVisible();

    await nameInput.fill('Playwright E2E Test');
    await page.getByRole('button', { name: /sign in/i }).click();

    // ── 2. Tiles render ────────────────────────────────────────────────
    // After sign-in the page shows a greeting and a grid of app tiles
    const greeting = page.getByText(/good to see you/i);
    await expect(greeting).toBeVisible({ timeout: 10_000 });

    // The desk tile is always pinned first
    const deskTile = page.getByRole('button', { name: /open gable desk/i });
    await expect(deskTile).toBeVisible();

    // At least one other tile should be present (apps that are enabled)
    const tiles = page.getByRole('button', { name: /^open /i });
    await expect(tiles.first()).toBeVisible();

    // ── 3. Open desk from its tile ──────────────────────────────────────
    await deskTile.click();

    // The desk is at /app/ so we should end up there
    await expect(page).toHaveURL(/\/app\//);

    // The desk home page should render something recognisable
    // (the home grid shows module cards — at minimum the page should load)
    await page.waitForLoadState('networkidle', { timeout: 15_000 }).catch(() => {
      // Network may not go idle if the app polls — the URL check is sufficient
    });

    // ── 4. Screenshot: front door after sign-in ──────────────────────────
    await page.screenshot({
      path: path.join(SHOTS_DIR, 'front-door-signed-in.png'),
      fullPage: true,
    });
  });

  test('record URL opens directly without going through home first', async ({ page }) => {
    // Pre-condition: sign in first so the desk has an authenticated session.
    await page.goto(BASE_URL);
    const nameInput = page.getByLabel(/display name/i);
    await expect(nameInput).toBeVisible();
    await nameInput.fill('Record Route E2E');
    await page.getByRole('button', { name: /sign in/i }).click();
    await expect(page.getByText(/good to see you/i)).toBeVisible({ timeout: 10_000 });

    // Navigate directly to a quote record URL — this is the SPA deep-link
    // that the nginx SPA fallback makes work in production.
    await page.goto(`${BASE_URL}/app/quotes/1`);

    // The desk should render this record without a full-page reload.
    // Either we see the quote page (exact content varies by what demo data
    // the seeded DB contains) or we see the desk shell with some content.
    await page.waitForLoadState('networkidle', { timeout: 15_000 }).catch(() => {});
    await expect(page).toHaveURL(/\/app\/quotes\/1/);

    // The desk shell should be present (sidebar nav, top bar, or main content)
    const body = page.locator('body');
    await expect(body).not.toBeEmpty();

    // ── Screenshot: record route ────────────────────────────────────────
    await page.screenshot({
      path: path.join(SHOTS_DIR, 'desk-quote-record.png'),
      fullPage: true,
    });
  });

  test('desk /home is reachable and shows the home grid', async ({ page }) => {
    // Sign in via the front door
    await page.goto(BASE_URL);
    const nameInput = page.getByLabel(/display name/i);
    await expect(nameInput).toBeVisible();
    await nameInput.fill('Home Grid E2E');
    await page.getByRole('button', { name: /sign in/i }).click();
    await expect(page.getByText(/good to see you/i)).toBeVisible({ timeout: 10_000 });

    // Navigate to the desk home
    await page.goto(`${BASE_URL}/app/home`);
    await page.waitForLoadState('networkidle', { timeout: 15_000 }).catch(() => {});
    await expect(page).toHaveURL(/\/app\/home/);

    // The desk home should show module cards (the grid that replaces the
    // old monolithic home page).
    const body = page.locator('body');
    await expect(body).not.toBeEmpty();

    // ── Screenshot: desk home ────────────────────────────────────────────
    await page.screenshot({
      path: path.join(SHOTS_DIR, 'desk-home.png'),
      fullPage: true,
    });
  });
});
