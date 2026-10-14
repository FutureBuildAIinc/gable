// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from '@playwright/test';

/**
 * The crm activity flow against the real stack (see playwright.config.ts),
 * on the converted activities contract: the lowercase vocabulary, the one
 * error envelope, the list envelope, and the audit row and event written
 * with the create in one transaction.
 */

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

test.describe('Activities flow on the new contract', () => {
  test('log a call through the modal, see it on the feed, and the wire is the contract', async ({ page, request }) => {
    await signIn(page, 'Playwright Activities');

    // A customer for this run, made through the converted customer create.
    const run = Date.now().toString(36).toUpperCase();
    const res = await request.post('/api/v1/customers', {
      data: { account_number: `E2E-ACT-${run}`, name: `E2E Activities ${run}` },
    });
    expect(res.status(), await res.text()).toBe(201);
    const customer = await res.json();

    // The account page carries the activity feed, on its CRM tab.
    await page.goto(`/accounts/${customer.id}`);
    await page.getByRole('button', { name: /CRM Activity/i }).click();
    await expect(page.getByRole('heading', { name: /Activity History/i })).toBeVisible();

    // Log a call through the modal; the wire body carries the lowercase type.
    await page.getByRole('button', { name: /Log Activity/i }).first().click();
    // The modal's host element wraps a fixed-position overlay and has no box
    // of its own; the heading is the visible proof it opened.
    await expect(page.getByRole('heading', { name: 'Log Activity' })).toBeVisible();
    await page.getByRole('button', { name: 'Call', exact: true }).click();
    await page.getByLabel(/Description/i).fill(`E2E call ${run}`);
    const posted = page.waitForResponse(
      (r) => new URL(r.url()).pathname === `/api/v1/customers/${customer.id}/activities` && r.request().method() === 'POST',
    );
    await page.getByRole('button', { name: /Log Activity|Save|Submit/i }).last().click();
    const post = await posted;
    expect(post.status(), await post.text()).toBe(201);
    expect(post.request().postDataJSON().activity_type).toBe('call');
    const created = await post.json();
    expect(created.activity_type).toBe('call');
    expect(created.revision).toBe(1);
    expect(created.contact_id).toBeNull();
    // nginx rewrites a proxied strong ETag into a weak one; the revision
    // number is all the contract reads (ADR 0001 section 11).
    expect(post.headers()['etag']).toMatch(/^(W\/)?"1"$/);

    // The feed shows the logged call without a reload.
    await expect(page.locator('gable-activity-feed').getByText(`E2E call ${run}`)).toBeVisible();

    // The list is the envelope, and the filter filters.
    const list = await request.get(`/api/v1/customers/${customer.id}/activities?activity_type=call`);
    expect(list.status()).toBe(200);
    const page1 = await list.json();
    expect(Array.isArray(page1.items)).toBe(true);
    expect(page1.items.some((a: { id: string }) => a.id === created.id)).toBe(true);
    const filtered = await request.get(`/api/v1/customers/${customer.id}/activities?activity_type=CALL`);
    expect(filtered.status()).toBe(400);
    expect((await filtered.json()).error.code).toBe('validation_failed');

    // The mutation wrote its audit row and its event in one transaction. The
    // feed's envelope carries the entity as {kind, id} (ADR 0003 section 5).
    const audit = await request.get(`/api/v1/events?types=activity.created&limit=200`);
    const events = (await audit.json()).items as { entity: { id: string }; type: string }[];
    expect(events.some((e) => e.entity.id === created.id && e.type === 'activity.created')).toBe(true);

    // The update refuses a blind write and takes the loaded revision.
    const blind = await request.put(`/api/v1/activities/${created.id}`, {
      data: { activity_type: 'note', description: 'no revision' },
    });
    expect(blind.status()).toBe(428);
    const stale = await request.put(`/api/v1/activities/${created.id}`, {
      data: { activity_type: 'note', description: 'stale' },
      headers: { 'If-Match': '"9"' },
    });
    expect(stale.status()).toBe(409);
    expect((await stale.json()).error.code).toBe('stale_revision');
    const good = await request.put(`/api/v1/activities/${created.id}`, {
      data: { activity_type: 'note', description: `E2E note ${run}` },
      headers: { 'If-Match': 'W/"1"' },
    });
    expect(good.status()).toBe(200);
    const updated = await good.json();
    expect(updated.revision).toBe(2);
    expect(updated.activity_type).toBe('note');

    // The delete takes the same precondition, by If-Match alone.
    const deleted = await request.delete(`/api/v1/activities/${created.id}`, { headers: { 'If-Match': '"2"' } });
    expect(deleted.status()).toBe(204);
  });
});
