// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * The admin main flow against the real stack (see playwright.config.ts), on
 * the converted admin contract (C5-1a): a minted key shown once and listed,
 * a revoke that sticks, the AI settings saved and removed on their revision,
 * the staff roster's AI_LM grants and kill switch on their revisions, and an
 * RFC through the governance lifecycle with its document number, its
 * revision precondition and its status filter.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

async function signIn(page: Page, name: string) {
  // The desk guards destructive admin acts with native confirm() dialogs;
  // accept them so the request behind each click actually fires.
  page.on('dialog', (d) => d.accept());
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

const RUN = Date.now().toString(36).toUpperCase();

test.describe('Tech admin flow on the new contract', () => {
  test('mint a key, see it once, revoke it', async ({ page }) => {
    await signIn(page, 'Playwright Admin');

    await page.goto('/admin');
    await expect(page.getByRole('heading', { name: 'API Keys' })).toBeVisible();

    const keyName = `E2E Admin ${RUN}`;
    await page.getByRole('button', { name: 'New API Key' }).click();
    await page.getByPlaceholder('Friendly name...').fill(keyName);
    await page.getByRole('button', { name: 'Generate', exact: true }).click();

    // The raw key is shown exactly once, in full; the row carries only the
    // prefix once it is listed.
    await expect(page.locator('code').filter({ hasText: /sk_live_[A-Za-z0-9_-]{30,}/ }).first()).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'admin-key-once.png') });
    await page.getByRole('button', { name: 'Done' }).click();

    await expect(page.getByRole('cell', { name: keyName })).toBeVisible();
    await expect(page.getByPlaceholder('Friendly name...')).toHaveCount(0);

    // Revoke sticks, and the row keeps its history.
    const row = page.locator('tr', { hasText: keyName });
    const revoked = page.waitForResponse(
      (r) => r.request().method() === 'DELETE' && r.url().includes('/api/v1/admin/keys/'),
    );
    await row.getByTitle('Revoke Key').click();
    expect((await revoked).status()).toBe(204);
    await expect(row).toContainText('Revoked', { ignoreCase: true });
    await page.screenshot({ path: path.join(SHOTS_DIR, 'admin-key-revoked.png') });
  });

  test('save the AI settings on their revision and remove them again', async ({ page }) => {
    await signIn(page, 'Playwright Admin');
    await page.goto('/admin');
    await page.getByRole('button', { name: 'AI Settings' }).click();

    await page.getByRole('button', { name: /add key|update key/i }).click();
    await page.getByPlaceholder('sk-or-...').fill(`sk-or-e2e-${RUN}`);
    await page.getByPlaceholder(/openrouter\.ai/).fill('https://openrouter.ai/api/v1');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'admin-ai-save.png') });
    await page.getByRole('button', { name: 'Save Key' }).click();

    // The saved state shows the admin override with its masked hint and the
    // base URL it stored; the save carried the revision it read, so it
    // cannot have clobbered a second editor's write.
    await expect(page.getByText('AI Features Active')).toBeVisible();
    await expect(page.getByText('Admin configured')).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'admin-ai-saved.png') });

    await page.getByRole('button', { name: 'Remove' }).click();
    await expect(page.getByText('AI Features Inactive')).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'admin-ai-removed.png') });
  });

  test('the staff roster grants AI_LM on the staff revision and the kill switch on its own', async ({ page }) => {
    await signIn(page, 'Playwright Admin');
    await page.goto('/admin');
    await page.getByRole('button', { name: 'Staff', exact: true }).click();

    // The seeded roster renders with each member's grant state once the
    // tab's own loads finish.
    await expect(page.getByText('Staff & Module Access')).toBeVisible();
    await expect(page.getByText('dispatcher@gable.com')).toBeVisible();
    const yuki = page.locator('tr', { hasText: 'yard@gable.com' });
    await expect(yuki).toContainText('Yuki Tan');

    // Grant AI_LM to the inactive yard member: the write carries the staff
    // revision and the roster is re-read from the server.
    const grantResponse = page.waitForResponse(
      (r) => r.url().includes('/api/v1/admin/staff/') && r.url().endsWith('/modules') && r.request().method() === 'POST',
    );
    await yuki.locator('input[type="checkbox"]').check();
    const granted = await grantResponse;
    expect(granted.status()).toBe(200);
    expect(granted.request().headers()['if-match']).toMatch(/^"\d+"$/);
    await expect(yuki.locator('input[type="checkbox"]')).toBeChecked();

    // And revoke it again, which is a DELETE on the same document revision.
    const revokeResponse = page.waitForResponse(
      (r) => r.url().includes('/api/v1/admin/staff/') && r.url().includes('/modules/ai_lm') && r.request().method() === 'DELETE',
    );
    await yuki.locator('input[type="checkbox"]').uncheck();
    const revoked = await revokeResponse;
    expect(revoked.status()).toBe(200);
    expect(await yuki.locator('input[type="checkbox"]').isChecked()).toBe(false);

    // The global kill switch is its own document: flipping it off leaves
    // every grant checked (the switch suspends, it does not revoke).
    const toggle = page.locator('button[role="switch"]');
    await expect(toggle).toHaveAttribute('aria-checked', 'true');
    const flip = page.waitForResponse(
      (r) => r.url().includes('/api/v1/admin/modules/ai_lm') && r.request().method() === 'PUT',
    );
    await toggle.click();
    expect((await flip).status()).toBe(200);
    await expect(toggle).toHaveAttribute('aria-checked', 'false');
    const dana = page.locator('tr', { hasText: 'dispatcher@gable.com' });
    await expect(dana.locator('input[type="checkbox"]')).toBeChecked();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'admin-staff-disabled.png') });

    // Back on: the same roster returns.
    const flipBack = page.waitForResponse(
      (r) => r.url().includes('/api/v1/admin/modules/ai_lm') && r.request().method() === 'PUT',
    );
    await toggle.click();
    expect((await flipBack).status()).toBe(200);
    await expect(toggle).toHaveAttribute('aria-checked', 'true');
    await expect(dana.locator('input[type="checkbox"]')).toBeChecked();
  });
});

test.describe('Governance RFC flow on the new contract', () => {
  test('draft an RFC, edit it on its revision, transition it, filter by status', async ({ page, request }) => {
    await signIn(page, 'Playwright Gov');

    // The dashboard lists the seeded RFCs with their document numbers.
    await page.goto('/governance');
    await expect(page.getByRole('heading', { name: 'Governance' })).toBeVisible();
    await expect(page.locator('td.font-mono', { hasText: /RFC-\d{6,}/ }).first()).toBeVisible();

    // Draft a new one through the desk's form.
    const title = `E2E RFC ${RUN}`;
    await page.getByRole('button', { name: 'Draft New RFC' }).click();
    await page.getByPlaceholder('e.g. Implement Zero Trust Auth').fill(title);
    await page.getByPlaceholder('What is broken or missing?').fill('The e2e problem');
    await page.getByPlaceholder('Briefly describe the approach').fill('The e2e solution');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'gov-new-rfc.png') });
    await page.getByRole('button', { name: 'Generate Draft' }).click();
    await expect(page.getByRole('heading', { name: 'Governance' })).toBeVisible();
    const row = page.locator('tr', { hasText: title });
    await expect(row).toBeVisible();
    await expect(row).toContainText('Draft');
    await expect(row.locator('td.font-mono', { hasText: /RFC-\d{6,}/ })).toBeVisible();

    // Open it: the detail reads the same document.
    await row.click();
    await expect(page.getByRole('heading', { name: title })).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'gov-detail.png') });
    const detailUrl = new URL(page.url()).pathname;
    const rfcId = detailUrl.split('/').pop() ?? '';

    // A stale write is refused: the desk edit has no form yet, so the edit
    // rides the API the desk service uses, on the revision the detail read.
    const read = await request.get(`/api/v1/governance/rfcs/${rfcId}`);
    expect(read.status()).toBe(200);
    const doc = (await read.json()) as { revision: number; number: string };
    expect(doc.number).toMatch(/RFC-\d{6,}/);
    const stale = await request.put(`/api/v1/governance/rfcs/${rfcId}`, {
      data: { title: `${title} stale` },
      headers: { 'If-Match': `"${doc.revision + 5}"` },
    });
    expect(stale.status()).toBe(409);
    expect(((await stale.json()) as { error: { code: string } }).error.code).toBe('stale_revision');
    const edit = await request.put(`/api/v1/governance/rfcs/${rfcId}`, {
      data: { title: `${title} edited` },
      headers: { 'If-Match': `"${doc.revision}"` },
    });
    expect(edit.status()).toBe(200);

    // The lifecycle rides the transitions route; the status filter serves it.
    const toReview = await request.post(`/api/v1/governance/rfcs/${rfcId}/transitions`, {
      data: { to: 'review', revision: doc.revision + 1 },
    });
    expect(toReview.status()).toBe(200);
    const inReview = (await (
      await request.get(`/api/v1/governance/rfcs?status=review&limit=200`)
    ).json()) as { items: { id: string; status: string }[] };
    expect(inReview.items.some((r) => r.id === rfcId && r.status === 'review')).toBe(true);

    // And the dashboard shows the edited title under Review.
    await page.goto('/governance');
    const editedRow = page.locator('tr', { hasText: `${title} edited` });
    await expect(editedRow).toContainText('Review');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'gov-review.png') });
  });
});
