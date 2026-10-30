// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The offline counter's spec helpers (CP-13, ADR 0011 section 7, slice P1).
 *
 * Every assumption the P1 specs make about slices that had not landed when
 * they were written is in this file, so P1 is a run, not a build: when O2,
 * O4, C2 and ADR 0012 slice 5 land, a name that differs is fixed here once.
 * The words are the ADR's (section 4); the test ids are the ones the counter's
 * existing screens use where they exist, and named here where C2 adds them.
 *
 * What each spec waits on is recorded with `waitsOn`, so a red run names the
 * slice that has not landed instead of a bare failure.
 */
import { createPublicKey, generateKeyPairSync } from 'node:crypto';
import { expect, test, type APIRequestContext, type BrowserContext, type Page } from '@playwright/test';

/** The slices a spec waits on (ADR 0011 section 8). */
export type Slice = 'O2' | 'O4' | 'C1' | 'C2' | 'C3' | 'ADR0012-5';

/** Records, on the test itself, the slices that must land before it can pass. */
export function waitsOn(...slices: Slice[]): void {
  test.info().annotations.push({ type: 'waits-on', description: slices.join(', ') });
}

/** The words the clerk reads (ADR 0011 section 4), pinned so a rewording is a red run. */
export const WORDS = {
  banner: 'Offline: sales are kept on this register and sent when the connection returns',
  pricesLine: 'Offline prices: promotions, quantity breaks and job prices are not applied',
  needsConnection: 'needs the connection',
  receiptHead: 'OFFLINE SALE',
  receiptPrices: 'prices and tax as known on this register: promotions, quantity breaks and job prices not applied offline',
  resultUnknown: 'Result unknown',
  kitRefusal: 'kits do not sell offline',
  storeMissing: 'This register must be enrolled again before it can sell offline',
  noReload: 'Do not reload while offline',
  needsManager: 'needs a manager',
} as const;

/** Test ids C2 adds to the counter; the older ones (`pos-lines`, `catalog-search`, ...) already exist. */
export const ID = {
  banner: 'gable-shell-banner',
  strip: 'pos-offline-strip',
  stripSent: 'pos-offline-sent',
  stripWaiting: 'pos-offline-waiting',
  stripRoom: 'pos-offline-room',
  stripSnapshotNote: 'pos-offline-snapshot-note',
  pricesLine: 'pos-offline-prices-line',
  receipt: 'pos-offline-receipt',
  receiptRef: 'pos-offline-ref',
  resultUnknown: 'pos-result-unknown',
  differentCustomer: 'pos-different-customer',
  closeTill: 'pos-close-till',
  closeWhy: 'pos-close-till-why',
  kitNote: 'pos-offline-kit-note',
  accountRoom: 'pos-offline-account-room',
} as const;

/** The branch and register the counter specs work in (the seed's REG-01). */
export const REGISTER = 'REG-01';

/** The manager's counter PIN the specs set (as pos-manager-stepup.spec.ts does). */
export const MANAGER_PIN = '4481';

/** A manager on the roster with a counter PIN, for the enrolment and the resolutions. */
export async function createManager(request: APIRequestContext): Promise<{ staffNo: string; name: string }> {
  const run = Date.now().toString(36).toUpperCase() + Math.floor(Math.random() * 1e4).toString(36).toUpperCase();
  const staffNo = `E2E-OFF-${run}`;
  const name = `E2E Offline Manager ${run}`;
  const res = await request.post('/api/v1/admin/staff', {
    data: { email: `e2e-off-${run}@example.com`, full_name: name, staff_no: staffNo, role: 'owner', active: true },
  });
  expect(res.status(), await res.text()).toBe(201);
  const member = (await res.json()) as { id: string };
  const pin = await request.put(`/api/v1/admin/staff/${member.id}/counter-pin`, { data: { pin: MANAGER_PIN } });
  expect(pin.status(), await pin.text()).toBe(200);
  return { staffNo, name };
}

/** Turns offline selling on for a branch: cash, check and account, with caps a spec can cross (O1's route). */
export async function enableOffline(
  request: APIRequestContext,
  branchId: string,
  over: { tenders?: string[]; saleCapCents?: number; accountCapCents?: number; maxQueued?: number } = {},
): Promise<void> {
  const res = await request.put('/api/v1/pos/offline/policy', {
    data: {
      branch_id: branchId,
      enabled: true,
      tenders: over.tenders ?? ['CASH', 'CHECK', 'ACCOUNT'],
      sale_cap_cents: over.saleCapCents ?? 500000,
      check_cap_cents: 200000,
      account_cap_cents: over.accountCapCents ?? 100000,
      queue_total_cap_cents: 2000000,
      max_queued: over.maxQueued ?? 40,
    },
  });
  expect(res.status(), await res.text()).toBe(200);
}

/** Puts the branch back on the dealer wide settings so no later run inherits a policy nobody chose. */
export async function clearOffline(request: APIRequestContext, branchId: string): Promise<void> {
  const res = await request.put('/api/v1/pos/offline/policy', { data: { branch_id: branchId, clear: true } });
  expect(res.status(), await res.text()).toBe(200);
}

/** A fresh Ed25519 public key as 64 hex characters, the shape the enrolment takes (the private half is dropped). */
export function newDevicePublicKeyHex(): string {
  const { publicKey } = generateKeyPairSync('ed25519');
  const jwk = createPublicKey(publicKey).export({ format: 'jwk' }) as { x: string };
  return Buffer.from(jwk.x, 'base64url').toString('hex');
}

/**
 * Enrols the register's device through O2's route, the way the shell does:
 * read next_epoch, make a key for it, enrol with a manager's counter PIN.
 * A real shell keeps the private half in the keychain; a spec that needs the
 * shell to sign enrols through the shell instead (C1 and ADR 0012 slice 5).
 */
export async function enrolDevice(
  request: APIRequestContext,
  registerId: string,
  manager: { staffNo: string },
): Promise<{ epoch: number; publicKey: string }> {
  const read = await request.get(`/api/v1/pos/offline/registers/${registerId}/device`);
  expect(read.status(), await read.text()).toBe(200);
  const { next_epoch } = (await read.json()) as { next_epoch: number };
  const publicKey = newDevicePublicKeyHex();
  const res = await request.post(`/api/v1/pos/offline/registers/${registerId}/device`, {
    data: { device_epoch: next_epoch, public_key: publicKey, manager: manager.staffNo, pin: MANAGER_PIN },
  });
  expect(res.status(), await res.text()).toBe(201);
  return { epoch: next_epoch, publicKey };
}

/** A manager's resolution of an exception (O4's route). */
export async function resolveException(
  request: APIRequestContext,
  id: string,
  action: 'retry' | 'accept' | 'late_post' | 'write_off' | 'post_separate' | 'restate' | 'review',
  reason: string,
): Promise<void> {
  const read = await request.get(`/api/v1/pos/offline/exceptions/${id}`);
  expect(read.status(), await read.text()).toBe(200);
  const { revision } = (await read.json()) as { revision: number };
  const res = await request.post(`/api/v1/pos/offline/exceptions/${id}/resolve`, { data: { action, reason, revision } });
  expect(res.status(), await res.text()).toBe(200);
}

/** The open exceptions of a branch, newest first (O4's route). */
export async function openExceptions(
  request: APIRequestContext,
  branchId: string,
): Promise<{ id: string; reason_code: string; offline_ref: string; status: string }[]> {
  const res = await request.get(`/api/v1/pos/offline/exceptions?branch_id=${branchId}&status=open`);
  expect(res.status(), await res.text()).toBe(200);
  return ((await res.json()) as { items: { id: string; reason_code: string; offline_ref: string; status: string }[] }).items;
}

/** Cuts the network for the whole context, as the ADR's "connection cut" steps do. */
export async function cutNetwork(context: BrowserContext): Promise<void> {
  await context.setOffline(true);
}

/** Brings the network back. */
export async function restoreNetwork(context: BrowserContext): Promise<void> {
  await context.setOffline(false);
}

/** Signs a display name in, as every counter spec does (dev auth). */
export async function signIn(page: Page, name: string): Promise<void> {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

/** Rings one line by SKU, waiting for the line to show (works offline: the catalogue is the snapshot's). */
export async function addOne(page: Page, sku: string): Promise<void> {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
}

/** Opens the payment panel. */
export async function openPayment(page: Page): Promise<void> {
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
}

/** Tenders the whole sale in cash and takes it (the same buttons online and offline). */
export async function payCash(page: Page): Promise<void> {
  await page.getByTestId('pos-next-payment').click();
  await openPayment(page);
  await page.getByTestId('tender-cash').click();
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await page.getByTestId('pos-pay').click();
}

/**
 * Waits until the register holds a snapshot and says it can sell offline.
 * C2 sets `data-state="ready"` on the strip once the core holds the snapshot
 * for the open till session; the spec never cuts the network before that.
 */
export async function expectReadyToSellOffline(page: Page): Promise<void> {
  await expect(page.getByTestId(ID.strip)).toHaveAttribute('data-state', /ready|offline/, { timeout: 20000 });
}

/** Reads the sale numbers (offline reference) off the printed receipt. */
export async function receiptRef(page: Page): Promise<string> {
  const text = (await page.getByTestId(ID.receiptRef).textContent()) ?? '';
  const m = /([A-Z0-9-]+-\d+-\d+)/.exec(text);
  expect(m, `an offline reference <register>-<epoch>-<seq> in "${text}"`).not.toBeNull();
  return (m as RegExpExecArray)[1];
}
