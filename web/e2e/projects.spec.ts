// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from '@playwright/test';

/**
 * The project flow against the real stack (see playwright.config.ts), on
 * the converted portal projects contract: the lowercase status, the list
 * envelope, the revision the status toggle sends as If-Match, and the
 * audit row and event written with each write in one transaction. The
 * portal chain runs in dev mode as the seeded demo customer, which is the
 * desk's projects page caller.
 */

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

/**
 * The portal surface's own gate: the desk's portal pages redirect to
 * /portal/login until a portal user is stored, so sign the seeded demo
 * customer (demo@kelbrook.ca / password) in through the real form. The API
 * side needs no token in AUTH_MODE=dev (serve injects the demo customer's
 * claims); this step is what the page's client-side gate requires.
 */
async function signInPortal(page: Page) {
  await page.goto('/portal/login');
  await page.getByLabel('Email Address').fill('demo@kelbrook.ca');
  await page.getByLabel('Password').fill('password');
  await page.getByRole('button', { name: 'Sign In' }).click();
  await page.waitForURL('**/portal');
}

test.describe('Projects flow on the new contract', () => {
  test('create a job, see the lowercase status, complete it on the loaded revision', async ({ page, request }) => {
    await signIn(page, 'Playwright Projects');
    await signInPortal(page);
    const run = Date.now().toString(36).toUpperCase();
    const name = `E2E Project ${run}`;

    await page.goto('/portal/projects');
    await expect(page.getByRole('heading', { name: /Projects/i }).first()).toBeVisible();

    // Create through the form.
    await page.getByRole('button', { name: /New Project|Add.*Project/i }).first().click();
    await page.getByLabel(/Project Name/i).fill(name);
    const created = page.waitForResponse(
      (r) => new URL(r.url()).pathname === '/api/portal/v1/projects' && r.request().method() === 'POST',
    );
    await page.getByRole('button', { name: /Create|Add/i }).last().click();
    const post = await created;
    expect(post.status(), await post.text()).toBe(201);
    const project = await post.json();
    expect(project.status).toBe('active');
    expect(project.revision).toBe(1);

    // The dashboard of the new job, with its lowercase badge.
    await page.waitForURL(`/portal/projects/${project.id}`);
    await expect(page.getByText(name).first()).toBeVisible();

    // Complete the job: the PUT carries the loaded revision as If-Match.
    const toggled = page.waitForResponse(
      (r) => new URL(r.url()).pathname === `/api/portal/v1/projects/${project.id}` && r.request().method() === 'PUT',
    );
    await page.getByRole('button', { name: /Mark completed/i }).click();
    const put = await toggled;
    expect(put.status(), await put.text()).toBe(200);
    expect(put.request().headers()['if-match']).toBe('"1"');
    const updated = await put.json();
    expect(updated.status).toBe('completed');
    expect(updated.revision).toBe(2);

    // The list is the envelope and the status filter filters.
    const list = await request.get('/api/portal/v1/projects?status=completed&limit=50');
    expect(list.status()).toBe(200);
    const page1 = await list.json();
    expect(Array.isArray(page1.items)).toBe(true);
    expect(page1.items.some((p: { id: string }) => p.id === project.id)).toBe(true);
    const refused = await request.get('/api/portal/v1/projects?status=ACTIVE');
    expect(refused.status()).toBe(400);
    expect((await refused.json()).error.code).toBe('validation_failed');
    const unknown = await request.get('/api/portal/v1/projects?zzz=1');
    expect(unknown.status()).toBe(400);
    expect((await unknown.json()).error.code).toBe('unsupported_query_parameter');

    // A blind write is a 428, and the module wrote its events (the feed's
    // envelope carries the entity as {kind, id}, ADR 0003 section 5).
    const blind = await request.put(`/api/portal/v1/projects/${project.id}`, { data: { name: `${name} nope` } });
    expect(blind.status()).toBe(428);
    const events = await request.get('/api/v1/events?types=project.created,project.updated&limit=200');
    const items = (await events.json()).items as { entity: { id: string }; type: string }[];
    expect(items.some((e) => e.entity.id === project.id && e.type === 'project.created')).toBe(true);
    expect(items.some((e) => e.entity.id === project.id && e.type === 'project.updated')).toBe(true);
  });
});
