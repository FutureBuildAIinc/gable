// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors


import { test, expect } from '@playwright/test';

/**
 * The desk gates its admin pages by the signed in person's role (UX only; the
 * core's role walls decide what a request may do). On a dev stack the persona
 * card's role is all the desk knows, so the persona is stored the way the door
 * stores it.
 */
async function signInAs(page: import('@playwright/test').Page, name: string, role: string) {
  await page.addInitScript(
    ([n, r]) => {
      sessionStorage.setItem('gable.auth.session.v1', JSON.stringify({ v: 1, kind: 'dev', name: n, role: r }));
    },
    [name, role],
  );
}

test.describe('Desk role walls', () => {
  const walled = [
    { path: '/admin/counter-rules', words: 'This page is for admin and owner', heading: /counter rules/i },
    { path: '/admin/offline-counter', words: 'This page is for admin, owner and finance', heading: /counter rules/i },
  ];
  for (const { path, words, heading } of walled) {
    test(`a cashier persona sees the refusal on ${path}`, async ({ page }) => {
      await signInAs(page, 'Cashier Persona', 'cashier');
      await page.goto(path);
      await expect(page.getByTestId('role-refusal')).toHaveText(words);
      await expect(page.getByRole('heading', { name: heading })).toHaveCount(0);
    });
  }

  test('an owner persona gets the counter rules page', async ({ page }) => {
    await signInAs(page, 'Owner Persona', 'owner');
    await page.goto('/admin/counter-rules');
    await expect(page.getByTestId('role-refusal')).toHaveCount(0);
  });
});
