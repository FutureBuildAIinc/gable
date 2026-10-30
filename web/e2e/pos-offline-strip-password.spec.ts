// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';
import { openCounter } from './pos-till';
import path from 'path';
import fs from 'fs';

/**
 * The offline strip's email and password form (CP-13 PR 3). The browser has
 * no Tauri window, so the shell is stood in for through the same
 * `__TAURI_INTERNALS__.invoke` door: its sync answers no_session until its
 * sign_in_with_password succeeds, and that command answers a bare code string
 * the way the real shell does. What this proves is the page: with email and
 * password first the strip opens its form and starts no redirect sign in, each
 * fixed code shows its own words, the password field is empty after every
 * call, and the form reads in light and charcoal at desk and phone widths.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });

function installCore() {
  const w = window as unknown as { __TAURI_INTERNALS__?: unknown; __core: { register: string; calls: Array<{ c: string; a?: unknown }>; signedIn: boolean; answer: string | null } };
  w.__core = { register: '', calls: [], signedIn: false, answer: null };
  const queue = () => ({ register_id: w.__core.register, device_epoch: 1, last_seq: 0, confirmed_seq: 0, waiting_count: 0, waiting_total_cents: 0,
    pending_count: 0, review_count: 0, synced_count: 0, items: [] });
  w.__TAURI_INTERNALS__ = {
    invoke: async (command: string, args?: unknown) => {
      w.__core.calls.push({ c: command, a: args });
      switch (command) {
        case 'offline_enrolment_status':
          return { register_id: w.__core.register, device_epoch: 1, public_key_hex: 'e2e', gable_url: location.origin, last_seq: 0, confirmed_seq: 0, chain_head: 'e2e' };
        case 'offline_queue_status': return queue();
        case 'load_offline_snapshot': throw { code: 'no_snapshot', message: 'none' };
        case 'refresh_offline_snapshot': {
          if (!w.__core.signedIn) throw { code: 'no_session', message: 'Sign in on this register again.' };
          return { status: 'unchanged', snapshot_id: 'e2e-snap', issued_at: new Date().toISOString() };
        }
        case 'sync_offline_queue': {
          if (!w.__core.signedIn) throw { code: 'no_session', message: 'Sign in on this register again.' };
          return { sent: 0, posted: 0, refused: 0, held: 0, resolved: 0, errors: 0, confirmed_seq: 0, open_exception_count: 0, queue: queue() };
        }
        case 'sign_in_with_password':
          if (w.__core.answer) throw w.__core.answer;
          w.__core.signedIn = true;
          return null;
        case 'begin_sign_in': return null;
        default: throw { code: 'unknown_command', message: command };
      }
    },
  };
}

async function openStrip(page: Page, methods: Array<{ method: string; kind: string }>) {
  await page.addInitScript(installCore);
  await page.route('**/api/v1/auth/config', (route) => route.fulfill({
    status: 200, json: { auth_mode: 'production', methods: methods.map((m) => ({ ...m, label: m.method, start_url: `/api/v1/auth/${m.method}` })) },
  }));
  await page.goto('/');
  await page.getByLabel('Display name').fill('POS Strip Password E2E');
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await openCounter(page);
  const register = ((await page.getByTestId('pos-register').textContent()) ?? '').trim();
  await page.evaluate((r) => { (window as unknown as { __core: { register: string } }).__core.register = r; }, register);
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect(page.getByTestId('pos-offline-sign-in')).toBeVisible();
}

const calls = (page: Page) => page.evaluate(() => (window as unknown as { __core: { calls: Array<{ c: string; a?: unknown }> } }).__core.calls.slice());

test('email and password first: the strip opens its form, each code shows its words, the password is not kept', async ({ page }) => {
  await openStrip(page, [{ method: 'email_password', kind: 'form' }, { method: 'google', kind: 'redirect' }]);
  await page.getByTestId('pos-offline-sign-in').click();
  const form = page.getByTestId('pos-offline-pw-form');
  await expect(form).toBeVisible();
  expect((await calls(page)).some((x) => x.c === 'begin_sign_in')).toBe(false);

  const words: Array<[string, string]> = [
    ['sign_in_refused', 'That email and password did not sign this register in.'],
    ['sign_in_limited', 'Too many tries. Wait a little, then try again.'],
    ['sign_in_not_allowed', 'Ask a manager'],
    ['sign_in_unavailable', 'The server cannot be reached'],
    ['no_server', 'no server'],
  ];
  for (const [code, text] of words) {
    await page.evaluate((c) => { (window as unknown as { __core: { answer: string } }).__core.answer = c; }, code);
    await page.getByTestId('pos-offline-pw-email').fill('clerk@yard.example');
    await page.getByTestId('pos-offline-pw-password').fill('hunter2-secret');
    await page.getByTestId('pos-offline-pw-submit').click();
    await expect(page.getByTestId('pos-offline-pw-error')).toContainText(text);
    await expect(page.getByTestId('pos-offline-pw-password')).toHaveValue('');
    await expect(page.getByTestId('pos-offline-pw-email')).toHaveValue('clerk@yard.example');
    expect(await page.content()).not.toContain('hunter2-secret');
  }

  for (const theme of ['charcoal', 'light'] as const) {
    for (const [name, size] of [['1440', { width: 1440, height: 900 }], ['390', { width: 390, height: 844 }]] as const) {
      await page.setViewportSize(size);
      await page.evaluate((t) => { if (t === 'light') document.documentElement.dataset.theme = 'light'; else document.documentElement.removeAttribute('data-theme'); }, theme);
      await page.screenshot({ path: path.join(SHOTS_DIR, `strip-form-${theme}-${name}.png`) });
    }
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.evaluate(() => { document.documentElement.removeAttribute('data-theme'); (window as unknown as { __core: { answer: null } }).__core.answer = null; });

  await page.getByTestId('pos-offline-pw-password').fill('hunter2-secret');
  await page.getByTestId('pos-offline-pw-submit').click();
  await expect(form).toHaveCount(0);
  await expect(page.getByTestId('pos-offline-sign-in')).toHaveCount(0);
  const made = await calls(page);
  expect(made.filter((x) => x.c === 'sign_in_with_password').pop()?.a).toEqual({ email: 'clerk@yard.example', password: 'hunter2-secret' });
  expect(made.some((x) => x.c === 'begin_sign_in')).toBe(false);
});

test('a dealer with google first still starts the redirect sign in and shows no form', async ({ page }) => {
  await openStrip(page, [{ method: 'google', kind: 'redirect' }, { method: 'email_password', kind: 'form' }]);
  await page.getByTestId('pos-offline-sign-in').click();
  await expect.poll(async () => (await calls(page)).filter((x) => x.c === 'begin_sign_in').length).toBe(1);
  await expect(page.getByTestId('pos-offline-pw-form')).toHaveCount(0);
});
