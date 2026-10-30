// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from '@playwright/test';

/**
 * ADR 0012 slice 3, against the real stack: the door signs a staff member in
 * through the core (which signs in to Appwrite server side), the session is
 * the HttpOnly cookie, sign out revokes it, and an expired session shows the
 * panel that sends the person back to the door.
 *
 * These specs need a core built WITHOUT the devauth tag and configured with
 * the Appwrite settings, over the local Appwrite seeded by
 * scripts/appwrite/setup.sh (one verified member per Gable role, at
 * <role>@s9proof.example). The dev mode stack of the Playwright job cannot
 * sign in against Appwrite, so they run only when APPWRITE_E2E=1 names such a
 * stack; every other spec of the suite keeps its dev mode persona flow.
 * APPWRITE_E2E_PASSWORD overrides the seed's shared password.
 */
test.skip(process.env.APPWRITE_E2E !== '1', 'needs the Appwrite backed stack (APPWRITE_E2E=1)');

const PASSWORD = process.env.APPWRITE_E2E_PASSWORD ?? 's9proof-not-a-real-password';
const SESSION_COOKIE = 'gable_session';

async function signIn(page: Page, role: string, password = PASSWORD) {
  await page.goto('/');
  await page.getByLabel('Email').fill(`${role}@s9proof.example`);
  await page.getByLabel('Password').fill(password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
}

test.describe('Staff sign in', () => {
  test('a cashier signs in, sees the counter and not purchasing, and the cookie is HttpOnly', async ({ page, context }) => {
    const apps = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
    await signIn(page, 'cashier');
    await apps;
    await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Open Counter' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Open Purchasing' })).toHaveCount(0);

    const cookie = (await context.cookies()).find((c) => c.name === SESSION_COOKIE);
    expect(cookie, 'the sign in sets the session cookie').toBeDefined();
    expect(cookie!.httpOnly).toBe(true);
    expect(cookie!.sameSite).toBe('Strict');
    expect(cookie!.path).toBe('/api');

    // No script can read the session, and nothing credential shaped is stored.
    const visible = await page.evaluate(() => ({
      cookie: document.cookie,
      session: JSON.stringify(sessionStorage),
      local: JSON.stringify(localStorage),
    }));
    expect(visible.cookie).not.toContain(SESSION_COOKIE);
    expect(visible.session).not.toContain(PASSWORD);
    expect(visible.local).not.toContain(PASSWORD);
    expect(visible.session).not.toContain(cookie!.value);
    expect(visible.local).not.toContain(cookie!.value);
  });

  test('a wrong password shows the plain refusal and signs nobody in', async ({ page, context }) => {
    await signIn(page, 'cashier', 'not-the-password');
    await expect(page.getByRole('alert')).toContainText('not accepted');
    await expect(page.getByRole('heading', { name: 'Sign in to Gable' })).toBeVisible();
    expect((await context.cookies()).some((c) => c.name === SESSION_COOKIE)).toBe(false);
  });

  test('a person who is not a member is refused with the same words', async ({ page }) => {
    await signIn(page, 'nomember');
    await expect(page.getByRole('alert')).toContainText('not accepted');
  });

  test('a fresh tab with the live cookie lands on the apps without a password', async ({ page, context }) => {
    await signIn(page, 'owner');
    await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();

    const other = await context.newPage();
    await other.goto('/');
    await expect(other.getByRole('heading', { name: /good to see you/i })).toBeVisible();
    await expect(other.getByLabel('Password')).toHaveCount(0);
  });

  test('sign out revokes the session at the core and returns to the form', async ({ page, context }) => {
    await signIn(page, 'admin');
    await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
    const cookie = (await context.cookies()).find((c) => c.name === SESSION_COOKIE)!;

    const out = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/auth/signout');
    await page.getByRole('button', { name: 'Sign out' }).click();
    expect((await out).status()).toBe(204);
    await expect(page.getByRole('heading', { name: 'Sign in to Gable' })).toBeVisible();
    expect((await context.cookies()).some((c) => c.name === SESSION_COOKIE)).toBe(false);

    // The old id no longer works on any route, even replayed by hand.
    const replay = await page.request.get('/api/v1/auth/session', {
      headers: { Cookie: `${SESSION_COOKIE}=${cookie.value}` },
    });
    expect(replay.status()).toBe(401);
  });

  test('an expired session shows the panel, and Sign in again goes back to the door', async ({ page, context }) => {
    await signIn(page, 'owner');
    await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();

    // The cookie goes (the idle limit, a revocation or a cleared browser): the
    // desk's next request is a 401.
    await context.clearCookies();
    await page.goto('/home');
    const panel = page.getByRole('alertdialog');
    await expect(panel).toContainText('Your session has expired');
    await panel.getByRole('button', { name: 'Sign in again' }).click();
    await expect(page).toHaveURL(/\/$/);
    await expect(page.getByRole('heading', { name: 'Sign in to Gable' })).toBeVisible();
  });
});
