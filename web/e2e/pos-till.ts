// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The counter's drawer: the specs wait for the till state to settle before
 * they act on it.
 *
 * The panel reads the register, then that register's till, and those are two
 * round trips. The till chip reads "No till" from the first paint, so it says
 * "No till" through the whole gap before the till read lands, and the Open
 * Till button is on screen only in that gap: it is rendered when the register
 * is set and the till is not. A spec that reads the chip and then clicks the
 * button can read a state that has already changed and click a button that
 * has already gone, and the click then waits out the whole test timeout for a
 * target that is not there.
 *
 * So the answer is taken from the till read itself, the response that carries
 * it: armed before the page loads, with a target, and settled. A 200 means a
 * till is open and the spec reuses it, and any other answer means none is
 * open and the spec opens one. The button is then on screen for good, because
 * the read has landed and found no till.
 */
import { expect, type Page } from '@playwright/test';

/** The panel's till chip: "Till open" or "No till". */
export function tillState(page: Page) {
  return page.getByTestId('pos-till-state');
}

/** The drawer the counter ends up with. */
export interface CounterTill {
  /** True when this call opened the drawer, false when it reused one already open. */
  opened: boolean;
  /** The open drawer's session id. */
  tillId: string;
}

/** The panel's read of its register's drawer. */
function isTillRead(url: string): boolean {
  return new URL(url).pathname === '/api/v1/pos/till/current';
}

/** The panel's own call that opens a drawer. */
function isTillOpen(url: string, method: string): boolean {
  return new URL(url).pathname === '/api/v1/pos/till/open' && method === 'POST';
}

/**
 * Opens the counter and settles the drawer: an open till is reused, and one
 * is opened only when there is none. The till state is settled before the
 * helper acts, so no spec reads a state that is about to change.
 */
export async function openCounter(page: Page, options: { timeout?: number } = {}): Promise<CounterTill> {
  const timeout = options.timeout;
  const wait = timeout === undefined ? undefined : { timeout };
  // Armed before the page loads, so the read cannot be missed.
  const read = page.waitForResponse((r) => isTillRead(r.url()), wait);
  await page.goto('/pos');
  await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible(wait);
  const res = await read;

  if (res.status() === 200) {
    // A drawer is already open: reuse it.
    const session = (await res.json()) as { id: string };
    await expect(tillState(page)).toContainText('Till open', wait);
    return { opened: false, tillId: session.id };
  }

  // No drawer is open, and the read that says so has landed, so the Open Till
  // button is on screen and stays on screen.
  await page.getByRole('button', { name: 'Open Till' }).click();
  await page.getByTestId('till-float-amount').fill('100.00');
  const open = page.waitForResponse((r) => isTillOpen(r.url(), r.request().method()));
  await page.getByRole('button', { name: 'Open Till' }).last().click();
  const openRes = await open;
  expect(openRes.status(), await openRes.text()).toBe(201);
  const session = (await openRes.json()) as { id: string };
  await expect(tillState(page)).toContainText('Till open', wait);
  return { opened: true, tillId: session.id };
}
