// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext } from '@playwright/test';
import {
  ID,
  REGISTER,
  WORDS,
  addOne,
  clearOffline,
  createManager,
  cutNetwork,
  enableOffline,
  enrolDevice,
  expectReadyToSellOffline,
  openExceptions,
  payCash,
  restoreNetwork,
  signIn,
  waitsOn,
} from './pos-offline';
import { stockedProduct, registerBranch } from './pos-stock';
import { openCounter } from './pos-till';

/**
 * The offline counter, the client half of ADR 0011 section 7 (CP-13, slice P1).
 *
 * Written before the slices that make them pass (O2, O4, C2 and ADR 0012
 * slice 5), so each test is red until its `waits-on` slices land and P1 is a
 * run, not a build. Every assumption about those slices lives in
 * `pos-offline.ts`. The signing core is the shell's (C1): the specs drive the
 * page, and the page reaches the core through the seam C2 ships.
 *
 * One test per bullet of the ADR's client list, in its order. The two
 * register proof is `pos-offline-two-registers.spec.ts`; the worker and the
 * webview legs are `pos-offline-worker.spec.ts`.
 */

let branchId = '';
let manager: { staffNo: string; name: string };

test.describe.configure({ mode: 'serial' });

test.beforeAll(async ({ request }) => {
  branchId = await registerBranch(request);
  manager = await createManager(request);
  await enableOffline(request, branchId);
  await enrolDevice(request, REGISTER, manager);
});

test.afterAll(async ({ request }) => {
  await clearOffline(request, branchId);
});

/** A second stocked product, so a sale has two different lines. */
async function extraProduct(request: APIRequestContext, yard: string): Promise<{ sku: string }> {
  const sku = `E2E-OFF2-${Date.now().toString(36).toUpperCase()}`;
  const created = await request.post('/api/v1/products', {
    data: { sku, description: `E2E offline second line ${sku}`, stock_uom: 'PCS', base_price_ten_thousandths: 120000 },
  });
  expect(created.status(), await created.text()).toBe(201);
  const p = (await created.json()) as { id: string };
  const stocked = await request.post('/api/v1/inventory/adjust', {
    data: { product_id: p.id, location_id: yard, quantity: 20, reason: 'e2e stock for the offline run' },
  });
  expect(stocked.status(), await stocked.text()).toBe(200);
  return { sku };
}

/** The server's cart id from the counter's own create call, to read its status later. */
function captureCartId(page: import('@playwright/test').Page): Promise<string> {
  return page
    .waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/pos/transactions' && r.request().method() === 'POST')
    .then(async (r) => ((await r.json()) as { id: string }).id);
}

async function cartStatus(request: APIRequestContext, id: string): Promise<string> {
  const res = await request.get(`/api/v1/pos/transactions/${id}`);
  expect(res.status(), await res.text()).toBe(200);
  return ((await res.json()) as { status: string }).status.toUpperCase();
}

test('connection cut mid sale: the offline receipt, then one posting and an abandoned cart', async ({ page, context, request }) => {
  waitsOn('O2', 'C1', 'C2', 'ADR0012-5');
  await signIn(page, 'POS Offline Mid Sale');
  const product = await stockedProduct(request);
  const second = await extraProduct(request, branchId);
  await openCounter(page);
  const cart = captureCartId(page);
  await addOne(page, product.sku);
  await addOne(page, second.sku);
  const cartId = await cart;
  await expectReadyToSellOffline(page);

  await cutNetwork(context);
  await expect(page.getByTestId(ID.banner)).toContainText(WORDS.banner);
  await expect(page.getByTestId(ID.pricesLine)).toHaveText(WORDS.pricesLine);
  await addOne(page, product.sku);
  await payCash(page);

  const receipt = page.getByTestId(ID.receipt);
  await expect(receipt).toContainText(WORDS.receiptHead);
  await expect(receipt).toContainText(WORDS.receiptPrices);
  await expect(receipt).not.toContainText(/INV-\d+/);
  await expect(page.getByTestId(ID.stripWaiting)).toContainText('1');

  await restoreNetwork(context);
  await expect(page.getByTestId(ID.stripSent)).toContainText('Sent 1', { timeout: 30000 });
  expect(await cartStatus(request, cartId)).toBe('ABANDONED');
  expect(await openExceptions(request, branchId)).toHaveLength(0);
});

test('connection cut during the completion: result unknown, no offline tender, the close waits', async ({ page, context, request }) => {
  waitsOn('O2', 'C1', 'C2', 'ADR0012-5');
  await signIn(page, 'POS Offline Result Unknown');
  const product = await stockedProduct(request);
  await openCounter(page);
  await addOne(page, product.sku);
  await expectReadyToSellOffline(page);

  // Let the completion reach the server, then drop its answer and the network.
  await page.route('**/api/v1/pos/transactions/*/complete', async (route) => {
    await route.fetch(); // the server completes the sale
    await cutNetwork(context); // the answer never reaches the page
    await route.abort('connectionfailed');
  });
  await payCash(page);

  await expect(page.getByTestId(ID.resultUnknown)).toContainText(WORDS.resultUnknown);
  // No offline tender is offered on a cart whose completion may have committed.
  await expect(page.getByTestId('tender-cash')).toBeDisabled();
  await expect(page.getByTestId(ID.closeTill)).toBeDisabled();
  await expect(page.getByTestId(ID.closeWhy)).toBeVisible();

  // On reconnect the page reads the sale, shows it complete, and queues nothing.
  await page.unroute('**/api/v1/pos/transactions/*/complete');
  await restoreNetwork(context);
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 30000 });
  await expect(page.getByTestId(ID.stripWaiting)).toContainText('0');
  await expect(page.getByTestId(ID.closeTill)).toBeEnabled();
});

test('a re-ring under a result unknown cart carries the cart id; the declared case carries none', async ({ page, context, request }) => {
  waitsOn('O2', 'O4', 'C1', 'C2', 'ADR0012-5');
  await signIn(page, 'POS Offline Re-ring');
  const product = await stockedProduct(request);
  await openCounter(page);
  await addOne(page, product.sku);
  await expectReadyToSellOffline(page);

  await page.route('**/api/v1/pos/transactions/*/complete', async (route) => {
    await route.fetch();
    await cutNetwork(context);
    await route.abort('connectionfailed');
  });
  await payCash(page);
  await expect(page.getByTestId(ID.resultUnknown)).toBeVisible();
  await page.unroute('**/api/v1/pos/transactions/*/complete');

  // Default: the same goods rung again offline keep the replaces link without the clerk doing anything.
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await addOne(page, product.sku);
  await payCash(page);
  await expect(page.getByTestId(ID.differentCustomer)).not.toBeChecked();
  const sent = page.waitForRequest((r) => new URL(r.url()).pathname === '/api/v1/pos/sync' && r.method() === 'POST');
  await restoreNetwork(context);
  const body = (await (await sent).postDataJSON()) as { items: { replaces_sale_id?: string | null }[] };
  expect(body.items[0].replaces_sale_id, 'the default link names the result unknown cart').toBeTruthy();

  // One posting only, and the second sale is refused as a settled replaced cart: a manager's item, nothing voided.
  await expect.poll(async () => (await openExceptions(request, branchId)).map((e) => e.reason_code), { timeout: 30000 }).toContain(
    'replaced_sale_settled',
  );

  // Declared case: "different customer" carries no link and posts.
  await cutNetwork(context);
  await page.getByRole('button', { name: 'New Sale', exact: true }).click();
  await addOne(page, product.sku);
  await page.getByTestId(ID.differentCustomer).check();
  await payCash(page);
  const declared = page.waitForRequest((r) => new URL(r.url()).pathname === '/api/v1/pos/sync' && r.method() === 'POST');
  await restoreNetwork(context);
  const declaredBody = (await (await declared).postDataJSON()) as {
    items: { replaces_sale_id?: string | null; different_customer?: boolean }[];
  };
  expect(declaredBody.items[0].replaces_sale_id ?? null).toBeNull();
  expect(declaredBody.items[0].different_customer).toBe(true);
});

test('store missing: with an enrolment and no queue file the counter refuses offline selling', async ({ page, context, request }) => {
  waitsOn('C1', 'C2', 'ADR0012-5');
  await signIn(page, 'POS Offline Store Missing');
  const product = await stockedProduct(request);
  await openCounter(page);
  await addOne(page, product.sku);
  // The shell removes the queue file out from under a live enrolment: the seam is the test shell's `remove-queue-file`.
  await page.evaluate(() => (window as unknown as { __gableTestCore: { removeQueueFile(): Promise<void> } }).__gableTestCore.removeQueueFile());
  await cutNetwork(context);
  await expect(page.getByTestId(ID.strip)).toContainText(WORDS.storeMissing);
  await payCash(page);
  await expect(page.getByTestId(ID.receipt)).toHaveCount(0);
  await restoreNetwork(context);
});

test('a refused sync is shown as "needs a manager" and the till close waits', async ({ page, context, request }) => {
  waitsOn('O2', 'O4', 'C1', 'C2', 'ADR0012-5');
  await signIn(page, 'POS Offline Refused Sync');
  const product = await stockedProduct(request);
  await openCounter(page);
  await addOne(page, product.sku);
  await expectReadyToSellOffline(page);

  await cutNetwork(context);
  await payCash(page);
  // Meanwhile the stock the sale needs goes away.
  const left = await request.get(`/api/v1/inventory?product_id=${product.id}`);
  const rows = ((await left.json()) as { items: { location_id: string; available: string }[] }).items;
  for (const row of rows) {
    const take = Number(row.available);
    if (take > 0) {
      const res = await request.post('/api/v1/inventory/adjust', {
        data: { product_id: product.id, location_id: row.location_id, quantity: -take, reason: 'e2e offline refused sync' },
      });
      expect(res.status(), await res.text()).toBe(200);
    }
  }
  await restoreNetwork(context);

  await expect(page.getByTestId(ID.stripSent)).toContainText(WORDS.needsManager, { timeout: 30000 });
  await expect(page.getByTestId(ID.closeTill)).toBeDisabled();
  await expect(page.getByTestId(ID.closeWhy)).toContainText(/manager|exception/i);
  const open = await openExceptions(request, branchId);
  expect(open.map((e) => e.reason_code)).toContain('stock_short');
});

test('card and store credit are greyed offline; an account over its room is refused at the counter', async ({ page, context, request }) => {
  waitsOn('O2', 'C1', 'C2', 'ADR0012-5');
  const run = Date.now().toString(36).toUpperCase();
  const made = await request.post('/api/v1/customers', {
    data: { account_number: `E2E-OFF-${run}`, name: `E2E Offline Account ${run}`, credit_limit_cents: 5000000 },
  });
  expect(made.status(), await made.text()).toBe(201);
  const account = (await made.json()) as { name: string; account_number: string };

  await signIn(page, 'POS Offline Tenders');
  const product = await stockedProduct(request);
  await openCounter(page);
  await expectReadyToSellOffline(page);
  await cutNetwork(context);

  await addOne(page, product.sku);
  await page.getByTestId('pos-next-payment').click();
  await page.getByTestId('panel-payment-toggle').click();
  for (const name of ['Card', 'Store credit']) {
    const tender = page.getByRole('button', { name, exact: true });
    await expect(tender).toBeDisabled();
    await expect(tender).toHaveAttribute('title', new RegExp(WORDS.needsConnection));
  }
  // Customer search reads the cached accounts only; the account's room is shown beside the tender.
  await page.locator('#pos-customer-search').fill(account.account_number);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(account.account_number) }).click();
  await expect(page.getByTestId(ID.accountRoom)).toContainText('$');
  // A ring far above the per account cap is refused at the counter with the room in the words.
  await page.getByRole('button', { name: 'Charge to account' }).click();
  await page.getByTestId('tender-amount').fill('99999.00');
  await expect(page.getByTestId('pos-account-refusal')).toContainText(/room/i);
  await restoreNetwork(context);
});

test('a kit line is refused at the counter offline and reaches no queue', async ({ page, context, request }) => {
  waitsOn('O2', 'C1', 'C2', 'ADR0012-5');
  const run = Date.now().toString(36).toUpperCase();
  const part = await stockedProduct(request);
  const created = await request.post('/api/v1/products', {
    data: { sku: `E2E-KIT-${run}`, description: `E2E kit ${run}`, stock_uom: 'PCS', base_price_ten_thousandths: 250000 },
  });
  expect(created.status(), await created.text()).toBe(201);
  const kit = (await created.json()) as { id: string; sku: string; revision: number };
  const defined = await request.put(`/api/v1/products/${kit.id}/kit-components`, {
    data: { revision: kit.revision, components: [{ component_product_id: part.id, quantity: '1' }] },
  });
  expect(defined.status(), await defined.text()).toBe(200);

  await signIn(page, 'POS Offline Kit');
  await openCounter(page);
  await expectReadyToSellOffline(page);
  await cutNetwork(context);
  await page.getByTestId('catalog-search').fill(kit.sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(page.getByTestId(ID.kitNote)).toContainText(WORDS.kitRefusal);
  await expect(page.locator('[data-testid="pos-lines"] li')).toHaveCount(0);
  await expect(page.getByTestId(ID.stripWaiting)).toContainText('0');
  await restoreNetwork(context);
});

test('reload offline: the counter comes back with no network and the queue is still there', async ({ page, context, request }) => {
  waitsOn('C1', 'C2', 'ADR0012-5');
  await signIn(page, 'POS Offline Reload');
  const product = await stockedProduct(request);
  await openCounter(page);
  await addOne(page, product.sku);
  await expectReadyToSellOffline(page);
  await cutNetwork(context);
  await payCash(page);
  await expect(page.getByTestId(ID.stripWaiting)).toContainText('1');

  await page.reload();
  await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible({ timeout: 20000 });
  await expect(page.getByTestId(ID.banner)).toContainText(WORDS.banner);
  await expect(page.getByTestId(ID.stripWaiting)).toContainText('1');
  await restoreNetwork(context);
  await expect(page.getByTestId(ID.stripSent)).toContainText('Sent 1', { timeout: 30000 });
});

test('a refused snapshot says why: the item that waits, and that the register sells on the snapshot it holds', async ({ page, context, request }) => {
  waitsOn('O2', 'O4', 'C1', 'C2', 'ADR0012-5');
  await signIn(page, 'POS Offline Snapshot Refused');
  const product = await stockedProduct(request);
  await openCounter(page);
  await addOne(page, product.sku);
  await expectReadyToSellOffline(page);
  await cutNetwork(context);
  await payCash(page);

  // The server refuses a new snapshot while the sale waits to be sent: hold the sync, then come back.
  await page.route('**/api/v1/pos/sync', (route) => route.abort('connectionfailed'));
  await restoreNetwork(context);
  const note = page.getByTestId(ID.stripSnapshotNote);
  await expect(note).toContainText(/waits/i, { timeout: 30000 });
  await expect(note).toContainText(/keeps selling on the snapshot it holds/i);
  await expect(note).toContainText(/\d+/); // the number of the item that waits
  await page.unroute('**/api/v1/pos/sync');
});
