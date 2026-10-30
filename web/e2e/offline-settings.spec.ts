// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';

/**
 * The counter's offline settings (CP-13, ADR 0011 section 4): a manager turns
 * offline selling on for the yard, picks the tenders and the caps, sees the
 * worst case on one account, saves, and finds the same figures after a reload;
 * then puts the yard back on the dealer wide settings so no later run inherits
 * a policy no dealer chose.
 */

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

test('a manager sets the yard\'s offline selling and finds it saved', async ({ page, request }) => {
  // the yard is the counter's register's yard, as the other counter specs find it
  const registers = (await (await request.get('/api/v1/pos/registers')).json()) as { items: { id: string; branch_id: string }[] };
  const register = registers.items.find((r) => r.id === 'REG-01') ?? registers.items[0];
  expect(register, 'the counter has a register').toBeTruthy();
  const yard = register.branch_id;
  await page.addInitScript((id) => localStorage.setItem('gable_current_branch_id', id), yard);
  await signIn(page, 'Playwright Admin');
  const headers = { 'X-Branch-Id': yard as string };
  try {
    await page.goto('/admin/offline-counter');
    await expect(page.getByRole('heading', { name: 'Offline selling' })).toBeVisible();

    await page.getByTestId('offline-enabled').check();
    await page.getByTestId('offline-tender-cash').check();
    await page.getByTestId('offline-tender-account').check();
    await page.getByTestId('offline-cap-sale').fill('500.00');
    await page.getByTestId('offline-cap-check').fill('200.00');
    await page.getByTestId('offline-cap-account').fill('1000.00');
    await page.getByTestId('offline-cap-queue').fill('5000.00');
    await page.getByTestId('offline-max-queued').fill('40');
    // the worst case on one account follows the cap, times the yard's enrolled registers
    await expect(page.getByTestId('offline-exposure')).toContainText('enrolled register');
    await page.getByTestId('offline-save').click();
    await expect(page.getByTestId('offline-counter-saved')).toContainText('Offline settings saved');
    await expect(page.getByTestId('offline-counter-source')).toContainText('own settings');

    // the server holds it: a reload reads the same figures back
    await page.reload();
    await expect(page.getByTestId('offline-cap-sale')).toHaveValue('500.00');
    await expect(page.getByTestId('offline-cap-account')).toHaveValue('1000.00');
    await expect(page.getByTestId('offline-max-queued')).toHaveValue('40');
    await expect(page.getByTestId('offline-tender-account')).toBeChecked();
    await expect(page.getByTestId('offline-tender-check')).not.toBeChecked();

    // a figure that is not money is refused in words, and nothing is sent
    await page.getByTestId('offline-cap-check').fill('lots');
    await page.getByTestId('offline-save').click();
    await expect(page.getByTestId('offline-counter-error')).toContainText('must be a dollar amount');

    // back to the dealer wide settings
    await page.reload();
    await page.getByTestId('offline-use-defaults').click();
    await expect(page.getByTestId('offline-counter-saved')).toContainText('dealer wide');
    await expect(page.getByTestId('offline-use-defaults')).toHaveCount(0);
  } finally {
    await request.put('/api/v1/pos/offline/policy', {
      headers: { ...headers, 'Idempotency-Key': crypto.randomUUID() },
      data: { branch_id: yard, clear: true },
    });
  }
});
