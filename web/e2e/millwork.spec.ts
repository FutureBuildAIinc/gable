// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from '@playwright/test';

/**
 * The millwork and configurator flows against the real stack (see
 * playwright.config.ts), on the converted contracts: integer cents for the
 * price adjustment, the list envelope with the required category, the
 * by-id read the create's Location resolves to, the audit row and event
 * written with the create in one transaction, and the configurator's
 * declared selections parameter with deterministic, conflict-explaining
 * answers.
 */

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

test.describe('Millwork and configurator flows on the new contract', () => {
  test('create an option in cents, read it by id, price a door, validate a conflict', async ({ page, request }) => {
    await signIn(page, 'Playwright Millwork');
    const run = Date.now().toString(36).toUpperCase();
    const category = `e2e-door-${run}`;

    // The create: integer cents, a Location, an ETag, an event.
    const created = await request.post('/api/v1/millwork/options', {
      data: { category, name: `E2E Clear Pine ${run}`, price_adjustment_cents: 1250, attributes: { grade: 'Clear' } },
    });
    expect(created.status(), await created.text()).toBe(201);
    const option = await created.json();
    expect(option.price_adjustment_cents).toBe(1250);
    expect(option.revision).toBe(1);
    const location = created.headers()['location'];
    expect(location).toBe(`/api/v1/millwork/options/${option.id}`);

    // The Location resolves: the by-id read with its ETag.
    const byId = await request.get(location!);
    expect(byId.status()).toBe(200);
    expect((await byId.json()).id).toBe(option.id);
    // nginx weakens a proxied strong ETag; the number is what counts
    // (ADR 0001 section 11).
    expect(byId.headers()['etag']).toMatch(/^W\/?"1"$/);

    // The list is the envelope; the empty category is [] and junk is a 400.
    const list = await request.get(`/api/v1/millwork/options?category=${category}`);
    expect(list.status()).toBe(200);
    const page1 = await list.json();
    expect(Array.isArray(page1.items)).toBe(true);
    expect(page1.items).toHaveLength(1);
    const empty = await request.get(`/api/v1/millwork/options?category=e2e-none-${run}`);
    expect((await empty.json()).items).toEqual([]);
    const junk = await request.get(`/api/v1/millwork/options?category=${category}&zzz=1`);
    expect(junk.status()).toBe(400);
    expect((await junk.json()).error.code).toBe('unsupported_query_parameter');

    // The create wrote its audit row and its event in one transaction. The
    // feed's envelope carries the entity as {kind, id} (ADR 0003 section 5).
    const events = await request.get('/api/v1/events?types=millwork_option.created&limit=200');
    const items = (await events.json()).items as { entity: { id: string } }[];
    expect(items.some((e) => e.entity.id === option.id)).toBe(true);

    // The door configurator prices in cents from the wire.
    await page.goto('/millwork/configure');
    await expect(page.getByRole('heading', { name: /Configure Door/i })).toBeVisible();
    await expect(page.getByText('$250.00').first()).toBeVisible();

    // The product configurator: the selections ride as one declared
    // parameter, and a conflict names its rule.
    const options = await request.get('/api/v1/configurator/options?attribute_type=Grade&selections=Species%3DSYP');
    expect(options.status()).toBe(200);
    const values = (await options.json()) as { value: string; allowed: boolean }[];
    const appearance = values.find((v) => v.value === 'Appearance');
    expect(appearance?.allowed).toBe(false);
    const oldStyle = await request.get('/api/v1/configurator/options?attribute_type=Grade&Species=SYP');
    expect(oldStyle.status()).toBe(400);
    expect((await oldStyle.json()).error.code).toBe('unsupported_query_parameter');

    const verdict = await request.post('/api/v1/configurator/validate', {
      data: { selections: { Species: 'SYP', Grade: 'Appearance' } },
    });
    expect(verdict.status()).toBe(200);
    const validation = await verdict.json();
    expect(validation.valid).toBe(false);
    expect(validation.conflicts.length).toBeGreaterThan(0);
    const build = await request.post('/api/v1/configurator/build-sku', {
      data: { product_type: 'Lumber', selections: { Species: 'SYP', Grade: 'Appearance' } },
    });
    expect(build.status()).toBe(400);
    const refusal = (await build.json()).error;
    expect(refusal.code).toBe('validation_failed');
    expect(refusal.details.some((d: { code?: string }) => d.code === 'config_conflict')).toBe(true);

    const clean = await request.post('/api/v1/configurator/build-sku', {
      data: { product_type: 'Lumber', selections: { Species: 'SYP', Grade: '#2' } },
    });
    expect(clean.status()).toBe(200);
    expect((await clean.json()).sku).toBe('NS-LBR-SYP-#2');
  });
});
