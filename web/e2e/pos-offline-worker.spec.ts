// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect } from '@playwright/test';
import { WORDS, cutNetwork, restoreNetwork, signIn, waitsOn } from './pos-offline';

/**
 * The worker follows the frame (ADR 0011 section 7, section 5.2): the desk
 * registers the `/pos` service worker only when it runs as the work shell's
 * frame (`gable-framed=work-shell`), and a desk open in a browser tab at the
 * dealer installs nothing. The offline reload of `/pos` in the shell's own
 * webviews is the WebKitGTK, WKWebView and WebView2 legs of the walk in
 * docs/parity/WALKS.md; this spec proves the rule in Chromium.
 */

test('a desk open in a browser tab installs no worker and shows no reload warning', async ({ page, context }) => {
  waitsOn('C1');
  await signIn(page, 'POS Offline Worker Tab');
  await page.goto('/pos');
  await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible();
  // Give a registration time to happen if the desk wrongly attempted one.
  await page.waitForTimeout(1500);
  const registrations = await page.evaluate(async () => (await navigator.serviceWorker.getRegistrations()).length);
  expect(registrations).toBe(0);
  await expect(page.locator('html')).not.toHaveAttribute('data-pos-offline-reload-warning', /.+/);

  // And a reload with the network cut gets nothing from a worker: the browser's own error.
  await cutNetwork(context);
  await expect(page.reload()).rejects.toThrow();
  await restoreNetwork(context);
});

test('framed by the work shell the desk registers the worker, and /pos reloads offline from the cache', async ({ page, context }) => {
  waitsOn('C1', 'ADR0012-5');
  await signIn(page, 'POS Offline Worker Frame');
  // The shell's host page frames the desk at /pos with the framed flag; the host opens it the way the shell does.
  await page.goto('/shell/');
  const frame = page.frameLocator('iframe[src*="gable-framed=work-shell"]');
  await expect(frame.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible({ timeout: 20000 });
  const controlled = await page
    .frames()
    .find((f) => f.url().includes('gable-framed=work-shell'))!
    .evaluate(async () => {
      const reg = await navigator.serviceWorker.ready;
      return reg.scope.endsWith('/pos');
    });
  expect(controlled, 'the worker is registered with scope /pos').toBe(true);

  await cutNetwork(context);
  await page.reload();
  await expect(page.frameLocator('iframe[src*="gable-framed=work-shell"]').getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible({
    timeout: 20000,
  });
  // Where a webview refuses the worker, the document says so instead (the warning the strip shows).
  await expect(page.locator('html')).not.toHaveAttribute('data-pos-offline-reload-warning', WORDS.noReload);
  await restoreNetwork(context);
});
