// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type Page } from './font-guard';
import { stockedProduct } from './pos-stock';
import { openCounter } from './pos-till';
import path from 'path';
import fs from 'fs';

/**
 * The counter's offline mode (CP-13 C2, ADR 0011 section 7's client lines)
 * against the real stack. The browser has no Tauri window, so the work
 * shell's Rust core is stood in for by a script the page reaches through the
 * same `__TAURI_INTERNALS__.invoke` door the shell gives it: the stand in
 * answers the named commands, prices every unit at the product's base price
 * and keeps what it signed. What this proves is the page: it carries the open
 * cart into the offline mode, shows the core's figures, greys card and store
 * credit, keeps a cart whose completion answer was lost as result unknown and
 * holds the till close, and on reconnect asks the core to sync before it asks
 * for new prices. The core's own signing, store and sync are proven by
 * `cargo test` in tauri/core, and the server's posting by the Go sync tests;
 * the whole chain in the real shell is P1's walk.
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

/** The stand in for the work shell's core, installed before the page loads. */
function installCore() {
  type Req = { client_id: string; customer_id: string | null; business_date: string; sold_at: string;
    lines: Array<{ id: string; product_id: string; quantity?: string; uom: string }>;
    tenders: Array<{ method: string; amount_cents: number; reference?: string | null }>;
    replaces_sale_id?: string | null; different_customer?: boolean; unknown_cart_id?: string | null };
  const w = window as unknown as {
    __TAURI_INTERNALS__?: unknown;
    __core: { register: string; productId: string; sku: string; unitCents: number; calls: string[]; sold: unknown[] };
  };
  w.__core = { register: '', productId: '', sku: '', unitCents: 0, calls: [], sold: [] };
  const snapshot = () => ({
    snapshot_id: 'e2e-snap', register_id: w.__core.register, branch_id: 'b', device_epoch: 1, issued_at: new Date().toISOString(),
    till_session: { id: 'e2e-till', cashier_id: 'e2e-cashier', opened_at: new Date(Date.now() - 3600000).toISOString(), business_date: new Date().toISOString().slice(0, 10), opening_float_cents: 0 },
    policy: { enabled: true, max_sale_cents: 10000000, max_check_cents: 1000000, default_check_limit_cents: null, account_cap_cents: 100000,
      max_queue_count: 10, max_queue_total_cents: 10000000, allowed_tenders: ['cash', 'check', 'account'], require_id_for_tenders: [] },
    tax: { branch_rate: '0.000000', provider_configured: false },
    walk_in_customer: { id: 'walk-in', currency: 'USD' },
    products: [{ id: w.__core.productId, sku: w.__core.sku, barcode: null, description: `Offline ${w.__core.sku}`, category: '', taxable: false,
      is_kit: false, random_length: false, board_thickness_in: null, board_width_in: null, stock_uom: 'EA', sale_uom: 'EA', price_uom: 'EA',
      in_stock: '100', unit_set: [{ uom: 'EA', unit_qty: '1', stock_qty: '1', sell: true, price: true }] }],
    accounts: [],
  });
  const queue = () => ({ register_id: w.__core.register, device_epoch: 1, last_seq: w.__core.sold.length, confirmed_seq: 0,
    waiting_count: w.__core.sold.length, waiting_total_cents: 0, pending_count: 0, review_count: 0, synced_count: 0,
    items: (w.__core.sold as Array<{ device_seq: number }>).map((sale) => ({ device_seq: sale.device_seq, state: 'waiting', reason_code: null,
      reason_message: null, invoice_id: null, invoice_number: null, sale })) });
  const price = (req: Req) => {
    const lines = req.lines.map((l, i) => ({ id: l.id, position: i, product_id: l.product_id, sku: w.__core.sku, description: `Offline ${w.__core.sku}`,
      quantity: l.quantity ?? '1', uom: l.uom, price_uom: l.uom, stock_uom: 'EA', stock_quantity: l.quantity ?? '1', price_key: 'walkin',
      price_source: 'list', level_fallback: false, unit_price_ten_thousandths: w.__core.unitCents * 100,
      line_total_cents: Math.round(Number(l.quantity ?? '1') * w.__core.unitCents), taxable: false }));
    const total = lines.reduce((s, l) => s + l.line_total_cents, 0);
    const t = req.tenders[0];
    const card = t !== undefined && !['cash', 'check', 'account'].includes(t.method);
    return { snapshot_id: 'e2e-snap', currency: 'USD', tax_rate: '0.000000', tax_exempt: false, lines, subtotal_cents: total, tax_cents: 0,
      total_cents: total, change_cents: t && !card ? Math.max(t.amount_cents - total, 0) : null,
      tender_refusal: card ? { code: 'tender_not_offline', message: 'card is not taken offline' } : null,
      account_room_cents: null, queue_room_count: 10, queue_room_cents: 10000000 };
  };
  w.__TAURI_INTERNALS__ = {
    invoke: async (command: string, args?: { request?: Req }) => {
      w.__core.calls.push(command);
      switch (command) {
        case 'offline_enrolment_status':
          return { register_id: w.__core.register, device_epoch: 1, public_key_hex: 'e2e', gable_url: location.origin, last_seq: w.__core.sold.length, confirmed_seq: 0, chain_head: 'e2e' };
        case 'load_offline_snapshot': return snapshot();
        case 'refresh_offline_snapshot': return { status: 'unchanged', snapshot_id: 'e2e-snap', issued_at: new Date().toISOString() };
        case 'price_offline_cart': return price(args!.request!);
        case 'sign_and_queue_offline_sale': {
          const req = args!.request!;
          const p = price(req);
          const seq = w.__core.sold.length + 1;
          const sale = { ...p, client_id: req.client_id, register_id: w.__core.register, device_epoch: 1, device_seq: seq,
            offline_ref: `${w.__core.register}-1-${String(seq).padStart(6, '0')}`, till_session_id: 'e2e-till', customer_id: null,
            business_date: req.business_date, sold_at: req.sold_at, change_cents: p.change_cents ?? 0,
            tenders: req.tenders.map((t) => ({ method: t.method, amount_cents: t.amount_cents, reference: t.reference ?? null })),
            ...(req.replaces_sale_id ? { replaces_sale_id: req.replaces_sale_id } : {}) };
          w.__core.sold.push(sale);
          return sale;
        }
        case 'offline_queue_status': return queue();
        case 'sync_offline_queue': {
          const sent = w.__core.sold.length;
          w.__core.sold = [];
          return { sent, posted: sent, refused: 0, held: 0, resolved: 0, errors: 0, confirmed_seq: sent, open_exception_count: 0, queue: queue() };
        }
        default: throw { code: 'unknown_command', message: command };
      }
    },
  };
}

async function primeCore(page: Page, product: { id: string; sku: string; base_price_ten_thousandths: number }) {
  const register = ((await page.getByTestId('pos-register').textContent()) ?? '').trim();
  await page.evaluate(({ register, product }) => {
    const w = window as unknown as { __core: { register: string; productId: string; sku: string; unitCents: number } };
    w.__core.register = register;
    w.__core.productId = product.id;
    w.__core.sku = product.sku;
    w.__core.unitCents = Math.round(product.base_price_ten_thousandths / 100);
  }, { register, product });
  return register;
}

async function coreCalls(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { __core: { calls: string[] } }).__core.calls.slice());
}

async function addOne(page: Page, sku: string) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
}

test('a browser tab sells online only when the connection drops', async ({ page, context, request }) => {
  await signIn(page, 'POS Offline Tab E2E');
  const product = await stockedProduct(request);
  await openCounter(page);
  await addOne(page, product.sku);
  await context.setOffline(true);
  await page.getByTestId('panel-payment-toggle').click();
  await page.getByTestId('tender-cash').click();
  await page.getByTestId('pos-pay').click();
  await expect(page.getByRole('alert')).toContainText('money never queues');
  await expect(page.getByTestId('pos-offline-mode')).toHaveCount(0);
  await context.setOffline(false);
});

test('the connection cut mid sale: the cart sells offline, the receipt says so, and the queue drains first on reconnect', async ({ page, context, request }) => {
  await page.addInitScript(installCore);
  await signIn(page, 'POS Offline E2E');
  const product = await stockedProduct(request);
  await openCounter(page);
  await primeCore(page, product);
  await addOne(page, product.sku);

  await context.setOffline(true);
  await page.evaluate(() => window.dispatchEvent(new Event('offline')));
  await expect(page.getByTestId('pos-offline-banner')).toContainText('Offline: sales are kept on this register');
  await expect(page.getByTestId('pos-offline-prices-note')).toContainText('promotions, quantity breaks and job prices are not applied');
  await expect(page.getByTestId('pos-offline-line')).toHaveCount(1);
  await expect(page.getByTestId('pos-offline-method-card')).toBeDisabled();
  await expect(page.getByTestId('pos-offline-method-card')).toContainText('needs the connection');
  await expect(page.getByTestId('pos-offline-method-store_credit')).toBeDisabled();
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-offline-cart.png'), fullPage: true });

  await page.getByTestId('pos-offline-complete').click();
  const receipt = page.getByTestId('pos-offline-receipt-text');
  await expect(receipt).toContainText('OFFLINE SALE');
  await expect(receipt).toContainText('-1-000001');
  await expect(receipt).toContainText('promotions, quantity breaks and job prices not applied offline');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-offline-receipt.png'), fullPage: true });
  const sold = await page.evaluate(() => (window as unknown as { __core: { sold: Array<{ replaces_sale_id?: string }> } }).__core.sold);
  expect(sold[0].replaces_sale_id, 'the offline sale names the server cart it replaces').toBeTruthy();

  const before = (await coreCalls(page)).length;
  await context.setOffline(false);
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect(page.getByTestId('pos-offline-strip')).toContainText('Sent 1');
  const after = (await coreCalls(page)).slice(before).filter((c) => c === 'sync_offline_queue' || c === 'refresh_offline_snapshot');
  expect(after.slice(0, 2)).toEqual(['sync_offline_queue', 'refresh_offline_snapshot']);
  await expect(page.getByTestId('pos-offline-mode')).toHaveCount(0);
});

test('the connection cut during the completion: result unknown, no offline tender for it, and the till close waits', async ({ page, context, request }) => {
  await page.addInitScript(installCore);
  await signIn(page, 'POS Offline Unknown E2E');
  const product = await stockedProduct(request);
  await openCounter(page);
  await primeCore(page, product);
  await addOne(page, product.sku);
  await page.getByTestId('panel-payment-toggle').click();
  await page.getByTestId('tender-cash').click();

  // the completion leaves and its answer never comes back
  await page.route('**/api/v1/pos/transactions/*/complete', async (route) => {
    await context.setOffline(true);
    await route.abort('internetdisconnected');
  });
  await page.getByTestId('pos-pay').click();
  await expect(page.getByText('Result unknown: this sale may already be complete')).toBeVisible();
  await expect(page.getByTestId('pos-offline-line')).toHaveCount(0);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-offline-unknown.png'), fullPage: true });

  await page.locator('[data-testid="pos-close-till"]:visible').first().click();
  await expect(page.getByText('A sale\'s result is unknown: reconnect to resolve it first')).toBeVisible();
  await page.unroute('**/api/v1/pos/transactions/*/complete');
  await context.setOffline(false);
});
