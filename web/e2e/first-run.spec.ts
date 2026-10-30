// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';
import path from 'path';
import fs from 'fs';

/**
 * The first run path the run-gable-locally skill walks a newcomer through:
 * the door's dev sign-in persona cards (a VITE_AUTH_DEV_MODE build), then the
 * desk each persona reaches. One row per role the skill names: the counter
 * clerk, the manager, finance and the owner. The demo set has no manager of
 * its own; its walk (docs/parity/WALKS.md) gives the owner persona the
 * manager's view, so manager and owner are the same card, signed in as two
 * separate sessions the way a newcomer tries them.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

const FIRST_RUN_ROLES = [
  { role: 'clerk', card: 'Sign in as Jordan Pike, Cashier, at Kelowna Main Yard', name: 'Jordan Pike', roleWords: 'Cashier' },
  { role: 'manager', card: 'Sign in as Morgan Hale, Owner, at Kelowna Main Yard', name: 'Morgan Hale', roleWords: 'Owner' },
  { role: 'finance', card: 'Sign in as Robin Diaz, Finance, at Kelowna Main Yard', name: 'Robin Diaz', roleWords: 'Finance' },
  { role: 'owner', card: 'Sign in as Morgan Hale, Owner, at Kelowna Main Yard', name: 'Morgan Hale', roleWords: 'Owner' },
] as const;

/** A screenshot taken after the entrance animations settle. */
async function shot(page: Page, name: string) {
  await page.waitForTimeout(700);
  await page.screenshot({ path: path.join(SHOTS_DIR, name) });
}

for (const { role, card, name, roleWords } of FIRST_RUN_ROLES) {
  test(`the first run ${role} sign-in card reaches the desk`, async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Sign in to Gable' })).toBeVisible();
    await expect(page.getByRole('button', { name: card })).toBeVisible();
    await page.getByRole('button', { name: card }).click();

    // The door greets the persona by the name the card carries.
    await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
    await expect(page.getByRole('heading', { name: /good to see you/i })).toContainText(name);

    // The desk tile opens the desk; the shell renders the persona and its role
    // in the account chip (the launcher grid names apps too, so the role words
    // are asserted inside the chip, not anywhere on the page).
    await page.getByRole('button', { name: 'Open Gable Desk' }).click();
    await expect(page).toHaveURL(/\/home$/);
    await expect(page.locator('gable-app-shell')).toBeVisible();
    const account = page.locator('gable-app-shell').getByRole('button', { name: 'Account menu' });
    await expect(account).toBeVisible();
    await expect(account.getByText(name, { exact: true })).toBeVisible();
    await expect(account.getByText(roleWords, { exact: true })).toBeVisible();
    await shot(page, `first-run-${role}.png`);
  });
}
