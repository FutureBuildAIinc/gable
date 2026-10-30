// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';
import path from 'path';
import fs from 'fs';

/**
 * The market index source and the CSV import (QE-07-b1), against the real
 * stack: the Tech Admin card reads Manual, a file of index values is
 * previewed and then applied through the same path a manual refresh takes,
 * the history row carries the day the value is for, importing the same file
 * again changes nothing, and an old value raises the staleness banner.
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

/** The day a value is for, `days` days back. */
function daysAgo(days: number): string {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() - days);
  return d.toISOString().slice(0, 10);
}

/** One index's code and id, read from the list. */
async function oneIndex(request: { get: (path: string) => Promise<{ json: () => Promise<unknown> }> }) {
  const listed = (await (await request.get('/api/v1/market-indices')).json()) as {
    id: string;
    name: string;
    index_code?: string;
    current_value?: number;
  }[];
  const withCode = listed.find((i) => i.index_code);
  if (!withCode) throw new Error('no market index carries a code');
  return withCode;
}

test.describe('market index source and import', () => {
  test('Tech Admin reads Manual for a fresh deployment', async ({ page }) => {
    await signIn(page, 'Index Source E2E');
    await page.goto('/admin');
    await page.getByRole('button', { name: 'Index Source' }).click();

    await expect(page.getByTestId('source-manual')).toBeChecked();
    await expect(page.getByTestId('source-feed')).not.toBeChecked();
    await expect(page.getByTestId('staleness-days')).toHaveValue('7');
    // The two secrets are never answered, so the card has nothing to show
    // but "Saved" and a Replace.
    await expect(page.getByTestId('feed-url-saved')).toBeVisible();
    await expect(page.getByTestId('feed-key-saved')).toBeVisible();
    await expect(page.locator('input[name="feed_url"]')).toHaveCount(0);
    await expect(page.locator('input[name="feed_key"]')).toHaveCount(0);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'market-index-source.png'), fullPage: true });
  });

  test('a file moves one index, and importing it again changes nothing', async ({ page, request }) => {
    await signIn(page, 'Index Source E2E');
    const index = await oneIndex(request as never);
    const day = daysAgo(0);
    const value = (index.current_value ?? 100) + 11;
    const csv = ['index_code,value,as_of_date', `${index.index_code},${value},${day}`].join('\n');

    await page.goto('/admin/market-indices');
    await page.getByTestId('import-file').setInputFiles({
      name: 'indices.csv',
      mimeType: 'text/csv',
      buffer: Buffer.from(csv),
    });

    // The preview names what the file would do before anything is written.
    await page.getByTestId('import-preview').click();
    await expect(page.getByTestId('import-summary')).toContainText('1 row(s) will be written');
    await expect(page.getByTestId('import-row')).toHaveCount(1);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'market-index-import-preview.png'), fullPage: true });

    // Apply moves the index and reports what it did.
    await page.getByTestId('import-apply').click();
    await expect(page.getByTestId('import-result')).toContainText('1 row(s) written');
    await expect(page.getByTestId('import-result')).toContainText('1 index value(s) moved');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'market-index-import-applied.png'), fullPage: true });

    // The index carries the new value, and its history row carries the day
    // the value is for.
    const row = page.locator('tr', { hasText: index.index_code });
    await expect(row).toContainText(String(value));
    const history = (await (await request.get(`/api/v1/market-indices/${index.id}/history`)).json()) as {
      points: { as_of_date?: string | null; value: number }[];
    };
    expect(history.points.length, 'the import wrote a history row').toBeGreaterThanOrEqual(1);
    const dated = history.points.find((p) => p.as_of_date);
    expect(dated, 'the history row carries its as of date').toBeTruthy();
    expect(dated!.as_of_date).toBe(day);

    // The same file again changes nothing.
    await page.getByTestId('import-file').setInputFiles({
      name: 'indices.csv',
      mimeType: 'text/csv',
      buffer: Buffer.from(csv),
    });
    await page.getByTestId('import-preview').click();
    await expect(page.getByTestId('import-summary')).toContainText('already stored');
    await page.getByTestId('import-apply').click();
    await expect(page.getByTestId('import-result')).toContainText('0 row(s) written');
    await expect(page.getByTestId('import-result')).toContainText('0 index value(s) moved');
    await expect(page.getByTestId('import-result')).toContainText('0 exposure scan(s) ran');
  });

  test('an old value raises the staleness banner', async ({ page, request }) => {
    await signIn(page, 'Index Source E2E');
    const indices = (await (await request.get('/api/v1/market-indices')).json()) as {
      id: string;
      index_code?: string;
    }[];
    const withCode = indices.filter((i) => i.index_code);
    expect(withCode.length, 'the deployment carries coded indices').toBeGreaterThanOrEqual(2);

    // A value for a day far behind the seven day threshold.
    const target = withCode[withCode.length - 1];
    const old = daysAgo(60);
    const csv = ['index_code,value,as_of_date', `${target.index_code},500,${old}`].join('\n');

    await page.goto('/admin/market-indices');
    await page.getByTestId('import-file').setInputFiles({
      name: 'old.csv',
      mimeType: 'text/csv',
      buffer: Buffer.from(csv),
    });
    await page.getByTestId('import-preview').click();
    await expect(page.getByTestId('import-summary')).toContainText('1 row(s) will be written');
    await page.getByTestId('import-apply').click();
    await expect(page.getByTestId('import-result')).toContainText('1 row(s) written');

    // The banner names the stale index and the threshold.
    const banner = page.getByTestId('index-staleness-banner');
    await expect(banner).toBeVisible();
    await expect(page.getByTestId('staleness-message')).toContainText('7 day');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'market-index-staleness.png'), fullPage: true });
  });
});
