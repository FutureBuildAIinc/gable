// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';
import path from 'path';
import fs from 'fs';

/**
 * The follow up on a quote against the real stack (see playwright.config.ts):
 * set a follow up on a sent quote, work the follow up queue, log a call on the
 * quote and see it on the quote and on the customer, then clear the follow up
 * and watch it leave the queue.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

/** The business day n days from today, in the machine's own calendar. */
function businessDay(offsetDays: number): string {
  const d = new Date();
  d.setDate(d.getDate() + offsetDays);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

test.describe('Quote follow up', () => {
  test('set a follow up, work the queue, log a call on the quote and clear the follow up', async ({ page, request }) => {
    const run = Date.now().toString(36).toUpperCase();

    await signIn(page, 'Playwright Follow Up');

    // A customer and a sent quote for this run.
    const customerRes = await request.post('/api/v1/customers', {
      data: { account_number: `E2E-FU-${run}`, name: `E2E Follow Up ${run}` },
    });
    expect(customerRes.status(), await customerRes.text()).toBe(201);
    const customer = await customerRes.json();

    const quoteRes = await request.post('/api/v1/quotes', {
      data: { customer_id: customer.id, lines: [] },
    });
    expect(quoteRes.status(), await quoteRes.text()).toBe(201);
    const quote = await quoteRes.json();
    const sent = await request.post(`/api/v1/quotes/${quote.id}/transitions`, {
      data: { to: 'sent', revision: quote.revision },
    });
    expect(sent.status(), await sent.text()).toBe(200);
    const sentQuote = await sent.json();
    expect(sentQuote.status).toBe('sent');

    // The quote page carries the follow up card and the quote's own activity feed.
    await page.goto(`/quotes/${quote.id}`);
    await expect(page.getByRole('heading', { name: `Quote ${quote.number}` })).toBeVisible();
    const card = page.locator('[data-testid="follow-up-card"]');
    await expect(card).toBeVisible();
    await expect(card.getByTestId('follow-up-date')).toHaveText('No follow up set');

    // Set the follow up for yesterday.
    await card.getByRole('button', { name: 'Set follow up' }).click();
    await card.getByLabel('Follow up date').fill(businessDay(-1));
    const put = page.waitForResponse(
      (r) => new URL(r.url()).pathname === `/api/v1/quotes/${quote.id}/follow-up` && r.request().method() === 'PUT',
    );
    await card.getByRole('button', { name: 'Save follow up' }).click();
    const putRes = await put;
    expect(putRes.status(), await putRes.text()).toBe(200);
    expect(putRes.request().headers()['if-match']).toBe(`"${sentQuote.revision}"`);
    const afterSet = await putRes.json();
    expect(afterSet.follow_up_at).not.toBeNull();

    // The card marks a follow up whose day has passed overdue.
    await expect(card.getByTestId('follow-up-overdue')).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'quote-follow-up-card.png') });

    // The queue tab asks the server for the due queue, and the quote is in it, marked overdue.
    await page.goto('/quotes');
    const queued = page.waitForResponse(
      (r) => new URL(r.url()).pathname === '/api/v1/quotes' && r.url().includes('follow_up=due'),
    );
    await page.getByRole('tab', { name: 'Follow ups due' }).click();
    await queued;
    await expect(page.locator('tbody tr').filter({ hasText: quote.number })).toBeVisible();
    await expect(
      page.locator('tbody tr').filter({ hasText: quote.number }).getByTestId('follow-up-overdue'),
    ).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'quote-follow-up-queue.png') });

    // Log a call on the quote: the wire names the quote's own route.
    await page.goto(`/quotes/${quote.id}`);
    await page.locator('gable-activity-feed').getByRole('button', { name: /Log Activity/i }).first().click();
    await expect(page.getByRole('heading', { name: 'Log Activity' })).toBeVisible();
    await page.getByRole('button', { name: 'Call', exact: true }).click();
    await page.getByLabel(/Description/i).fill(`E2E follow up call ${run}`);
    const posted = page.waitForResponse(
      (r) => new URL(r.url()).pathname === `/api/v1/quotes/${quote.id}/activities` && r.request().method() === 'POST',
    );
    await page.getByRole('button', { name: /Log Activity|Save|Submit/i }).last().click();
    const post = await posted;
    expect(post.status(), await post.text()).toBe(201);
    const activity = await post.json();
    expect(activity.activity_type).toBe('call');
    expect(activity.quote_id).toBe(quote.id);
    expect(activity.customer_id).toBe(customer.id);

    // The quote's own feed shows the call.
    await expect(page.locator('gable-activity-feed').getByText(`E2E follow up call ${run}`)).toBeVisible();

    // The customer's feed shows it too: the activity carries the customer.
    await page.goto(`/accounts/${customer.id}`);
    await page.getByRole('button', { name: /CRM Activity/i }).click();
    await expect(page.getByRole('heading', { name: /Activity History/i })).toBeVisible();
    await expect(page.locator('gable-activity-feed').getByText(`E2E follow up call ${run}`)).toBeVisible();

    // Clearing the follow up takes it out of the queue.
    await page.goto(`/quotes/${quote.id}`);
    const card2 = page.locator('[data-testid="follow-up-card"]');
    const cleared = page.waitForResponse(
      (r) => new URL(r.url()).pathname === `/api/v1/quotes/${quote.id}/follow-up` && r.request().method() === 'PUT',
    );
    await card2.getByRole('button', { name: 'Clear' }).click();
    const clearRes = await cleared;
    expect(clearRes.status(), await clearRes.text()).toBe(200);
    expect(clearRes.request().postDataJSON()).toMatchObject({ follow_up_at: null, follow_up_owner_id: null });
    await expect(card2.getByTestId('follow-up-date')).toHaveText('No follow up set');

    await page.goto('/quotes');
    await page.getByRole('tab', { name: 'Follow ups due' }).click();
    await expect(page.locator('tbody tr').filter({ hasText: quote.number })).toHaveCount(0);

    // The wire agrees: the quote carries no follow up, and the queue no longer holds it.
    const after = await (await request.get(`/api/v1/quotes/${quote.id}`)).json();
    expect(after.follow_up_at).toBeNull();
    expect(after.follow_up_owner_id).toBeNull();
    const queue = await (await request.get('/api/v1/quotes?follow_up=due&limit=200')).json();
    expect(queue.items.some((q: { id: string }) => q.id === quote.id)).toBe(false);
  });
});
