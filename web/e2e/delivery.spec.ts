// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * The delivery module's flow against the real stack on the converted contract
 * (C5-1d): the board read is one payload (routes with their stops embedded,
 * include=stops), the date filter is honoured exactly, a route dispatches and
 * completes through the transitions route carrying its revision, and a driver
 * completes a stop with proof of delivery. The strict refusals and the
 * revision preconditions are pinned at the API level.
 */

const SHOTS_DIR = process.env.SHOTS_DIR ?? path.join('test-results', 'shots');
fs.mkdirSync(SHOTS_DIR, { recursive: true });
const RUN = Date.now().toString(36);
let counter = 0;

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

async function freshCustomer(request: APIRequestContext) {
  const res = await request.post('/api/v1/customers', { data: { account_number: `E2E-DLV-${RUN}-${++counter}`, name: `E2E Delivery ${RUN} ${counter}` } });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as { id: string; name: string };
}

// A stocked product with plenty on hand (the fulfilment spec's walker).
async function stockedProduct(request: APIRequestContext) {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: { id: string; sku: string }[] };
  const seeded = products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse();
  for (const p of seeded) {
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as { items: { available: string }[] };
    const available = page.items.reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (available >= 50) return p;
  }
  throw new Error('no stocked product in the demo seed');
}

// A business date as YYYY-MM-DD: the wire's date filter and the route's
// scheduled_date travel as dates, not moments.
function dateStr(offsetDays = 0): string {
  const d = new Date();
  d.setDate(d.getDate() + offsetDays);
  return d.toLocaleDateString('en-CA');
}

interface Stop { id: string; status: string; stop_sequence: number; revision: number; order_number: string | null }
interface Route { id: string; status: string; revision: number; stops: Stop[] | null; vehicle_name: string; driver_name: string }

// A confirmed delivery order: the only kind the board assigns.
async function confirmedDeliveryOrder(request: APIRequestContext, customer: { id: string }, product: { id: string }) {
  const created = await request.post('/api/v1/orders', {
    data: { customer_id: customer.id, delivery_type: 'delivery', lines: [{ product_id: product.id, quantity: '4' }] },
  });
  expect(created.status(), await created.text()).toBe(201);
  const order = await created.json();
  const confirmed = await request.post(`/api/v1/orders/${order.id}/transitions`, { data: { to: 'confirmed', revision: 1 } });
  expect(confirmed.status(), await confirmed.text()).toBe(200);
  return order as { id: string; number: string };
}

// A route on today's board with its own uniquely named driver, so the desk
// and driver pages can tell it from the seed's dispatch-day routes.
async function routeToday(request: APIRequestContext) {
  const vehicles = (await (await request.get('/api/v1/delivery/vehicles?limit=200')).json()) as { items: { id: string; name: string }[] };
  expect(vehicles.items.length, 'a seeded vehicle').toBeGreaterThan(0);
  const driverName = `E2E Driver ${RUN}`;
  const made = await request.post('/api/v1/delivery/drivers', { data: { name: driverName } });
  expect(made.status(), await made.text()).toBe(201);
  const driver = await made.json();
  const res = await request.post('/api/v1/delivery/routes', {
    data: { vehicle_id: vehicles.items[0].id, driver_id: driver.id, scheduled_date: dateStr() },
  });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as Route;
}

test.describe('Delivery on the converted contract', () => {
  test('the board is one payload, the date filter filters, and a stop completes through the driver page', async ({ page, request }) => {
    const customer = await freshCustomer(request);
    const product = await stockedProduct(request);
    const order = await confirmedDeliveryOrder(request, customer, product);
    const route = await routeToday(request);
    expect(route.status).toBe('draft');
    expect(route.revision).toBe(1);
    // The route document carries its stops as null when it holds none; the
    // board read below normalises the same route to an empty array.
    expect(route.stops).toBeNull();
    const driverName = route.driver_name;

    // The stop: stop_sequence defaults to the route's next position and the
    // order's document number rides along.
    const assigned = await request.post('/api/v1/delivery/deliveries', { data: { route_id: route.id, order_id: order.id } });
    expect(assigned.status(), await assigned.text()).toBe(201);
    const stop = (await assigned.json()) as { delivery: Stop };
    expect(stop.delivery.status).toBe('pending');
    expect(stop.delivery.stop_sequence).toBe(1);
    expect(stop.delivery.order_number).toBe(order.number);
    expect((await assigned.headers())['location']).toBe(`/api/v1/delivery/deliveries/${stop.delivery.id}`);

    // The board read: routes with their stops embedded, one payload.
    const board = (await (await request.get('/api/v1/delivery/routes?include=stops&limit=200')).json()) as { items: Route[] };
    const mine = board.items.find((r) => r.id === route.id);
    expect(mine?.stops?.map((s) => s.id)).toEqual([stop.delivery.id]);

    // The date filter is honoured exactly (the live failure answered 500 for
    // an unparseable value and matched a parsed moment instead of the date).
    const todays = (await (await request.get(`/api/v1/delivery/routes?date=${dateStr()}`)).json()) as { items: Route[] };
    expect(todays.items.some((r) => r.id === route.id)).toBe(true);
    const tomorrows = (await (await request.get(`/api/v1/delivery/routes?date=${dateStr(1)}`)).json()) as { items: Route[] };
    expect(tomorrows.items.some((r) => r.id === route.id)).toBe(false);
    const badDate = await request.get('/api/v1/delivery/routes?date=whenever');
    expect(badDate.status()).toBe(400);
    expect((await badDate.json()).error.details.map((d: { field: string }) => d.field)).toContain('date');
    const upper = await request.get('/api/v1/delivery/routes?status=DRAFT');
    expect(upper.status()).toBe(400);
    const unknown = await request.get('/api/v1/delivery/routes?wat=1');
    expect(unknown.status()).toBe(400);
    expect((await unknown.json()).error.code).toBe('unsupported_query_parameter');

    // The cursor walks the routes once.
    const p1 = await (await request.get('/api/v1/delivery/routes?limit=1')).json();
    expect(p1.items).toHaveLength(1);
    expect(typeof p1.next_cursor).toBe('string');
    const p2 = await (await request.get(`/api/v1/delivery/routes?limit=200&cursor=${p1.next_cursor}`)).json();
    expect(p2.items.some((r: Route) => r.id === p1.items[0].id)).toBe(false);

    // The desk board: the manifest shows the stop against its route.
    await signIn(page, 'Playwright Delivery');
    await page.goto('/dispatch');
    const card = page.locator('gable-route-list-component .space-y-3 > div', { hasText: driverName }).first();
    await expect(card).toBeVisible();
    await card.click();
    await expect(page.getByText(customer.name).first()).toBeVisible();
    await expect(page.getByText(`Order #${order.number}`).first()).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'delivery-board.png') });

    // Dispatch is the transitions route carrying the loaded revision.
    // The assign moved the route's revision (PR 70 review round 2 P2-1):
    // the dispatch carries revision 2, the value the desk read at select.
    const dispatch = page.waitForResponse((r) => r.url().endsWith(`/api/v1/delivery/routes/${route.id}/transitions`));
    await page.getByRole('button', { name: 'Dispatch' }).click();
    const dispatchRes = await dispatch;
    expect(dispatchRes.status(), await dispatchRes.text()).toBe(200);
    expect(dispatchRes.request().postDataJSON()).toEqual({ to: 'in_transit', revision: 2 });
    expect((await dispatchRes.json()).status).toBe('in_transit');

    // The driver app: pick the driver, open the route, complete the stop with
    // a recipient name (the POD the delivered status requires).
    await page.goto('/driver');
    await page.getByRole('combobox').selectOption({ label: driverName });
    const routeCard = page.locator('gable-driver-route-list .space-y-3 > div', { hasText: route.vehicle_name }).first();
    await expect(routeCard).toBeVisible();
    await routeCard.click();
    await expect(page.getByText(customer.name).first()).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'driver-stop-list.png') });
    await page.locator('gable-stop-list h3', { hasText: customer.name }).first().click();

    await expect(page.getByRole('button', { name: 'Complete Delivery' })).toBeVisible();
    await page.getByRole('button', { name: 'Complete Delivery' }).click();
    const modal = page.locator('div.fixed.inset-0').filter({ hasText: 'Proof of Delivery' });
    await expect(modal).toBeVisible();
    await modal.getByPlaceholder('Received by...').fill('Site foreman');
    const complete = page.waitForResponse((r) => r.url().endsWith(`/api/v1/delivery/deliveries/${stop.delivery.id}/transitions`));
    await modal.getByRole('button', { name: 'Confirm Delivery' }).click();
    const completeRes = await complete;
    expect(completeRes.status(), await completeRes.text()).toBe(200);
    const body = completeRes.request().postDataJSON();
    expect(body.to).toBe('delivered');
    // The signature uploaded as a pod photo first (the transition's proof URL
    // names it), and a photo attach moves the stop's revision, so the
    // transition carries the revision the upload left.
    expect(body.revision).toBe(stop.delivery.revision + 1);
    expect(body.pod_signed_by).toBe('Site foreman');
    expect(body.pod_proof_url).not.toContain('data:');
    expect((await completeRes.json()).status).toBe('delivered');
    await expect(page.getByText('Delivery completed successfully')).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'driver-stop-delivered.png') });

    // The route completes once every stop is terminal: re-read the board so
    // the desk carries the route's in_transit status.
    await page.goto('/dispatch');
    const cardAgain = page.locator('gable-route-list-component .space-y-3 > div', { hasText: driverName }).first();
    await expect(cardAgain).toBeVisible();
    await cardAgain.click();
    await expect(page.getByText(customer.name).first()).toBeVisible();
    const finish = page.waitForResponse((r) => r.url().endsWith(`/api/v1/delivery/routes/${route.id}/transitions`));
    await page.getByRole('button', { name: 'Complete Route' }).click();
    const finishRes = await finish;
    expect(finishRes.status(), await finishRes.text()).toBe(200);
    expect(finishRes.request().postDataJSON()).toEqual({ to: 'completed', revision: 3 });
    expect((await finishRes.json()).status).toBe('completed');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'delivery-route-completed.png') });

    // The module's events are observable downstream, in order.
    const events = (await (await request.get('/api/v1/events?types=route.created,route.in_transit,delivery.created,delivery.delivered,route.completed&limit=200')).json()) as {
      items: { type: string; entity: { kind: string; id: string } }[];
    };
    const kinds = events.items
      .filter((e) => (e.entity.kind === 'route' && e.entity.id === route.id) || (e.entity.kind === 'delivery' && e.entity.id === stop.delivery.id))
      .map((e) => e.type);
    expect(kinds).toEqual(['route.created', 'delivery.created', 'route.in_transit', 'delivery.delivered', 'route.completed']);
  });

  test('writes carry the revision precondition, refusals name their field, and the adjustment is recorded', async ({ request }) => {
    const customer = await freshCustomer(request);
    const product = await stockedProduct(request);
    const order = await confirmedDeliveryOrder(request, customer, product);
    const route = await routeToday(request);
    const assigned = await request.post('/api/v1/delivery/deliveries', { data: { route_id: route.id, order_id: order.id } });
    expect(assigned.status(), await assigned.text()).toBe(201);
    const stop = ((await assigned.json()) as { delivery: Stop }).delivery;
    const transition = (data: Record<string, unknown>) => request.post(`/api/v1/delivery/deliveries/${stop.id}/transitions`, { data });

    // The precondition: none is 428, a stale one is 409 stale_revision.
    const none = await transition({ to: 'failed' });
    expect(none.status()).toBe(428);
    const stale = await transition({ to: 'failed', revision: 99 });
    expect(stale.status()).toBe(409);
    expect((await stale.json()).error.code).toBe('stale_revision');

    // An unknown target is a 400 naming to; a delivered stop needs its POD fields named.
    const unknownTo = await transition({ to: 'somewhere', revision: stop.revision });
    expect(unknownTo.status()).toBe(400);
    expect((await unknownTo.json()).error.details.map((d: { field: string }) => d.field)).toContain('to');
    const noPod = await transition({ to: 'delivered', revision: stop.revision });
    expect(noPod.status()).toBe(400);
    const noPodFields = (await noPod.json()).error.details.map((d: { field: string }) => d.field);
    expect(noPodFields).toContain('pod_proof_url');
    expect(noPodFields).toContain('pod_signed_by');

    // The adjustment records: adjusted_by names who recorded it, a lowercase
    // reason code, the quantities as decimal strings, and the revision moves
    // with the write.
    const badReason = await request.post(`/api/v1/delivery/deliveries/${stop.id}/adjust-qty`, {
      data: { adjusted_by: customer.id, adjustments: [{ product_id: product.id, original_qty: '4', adjusted_qty: '3', reason_code: 'SHORT_SHIP' }] },
    });
    expect(badReason.status()).toBe(400);
    expect((await badReason.json()).error.details.map((d: { field: string }) => d.field)).toContain('adjustments[0].reason_code');
    const adjusted = await request.post(`/api/v1/delivery/deliveries/${stop.id}/adjust-qty`, {
      data: { adjusted_by: customer.id, adjustments: [{ product_id: product.id, original_qty: '4', adjusted_qty: '3', reason_code: 'short_ship', notes: 'one bundle short' }] },
    });
    expect(adjusted.status(), await adjusted.text()).toBe(200);
    const adjustedStop = (await adjusted.json()) as Stop;
    expect(adjustedStop.revision).toBe(stop.revision + 1);

    // The recorded adjustment is observable: the event names it and the stop
    // that failed completes on the revision the adjustment moved it to.
    const events = (await (await request.get('/api/v1/events?types=delivery.adjusted&limit=200')).json()) as {
      items: { type: string; entity: { id: string }; data: Record<string, unknown> }[];
    };
    const mine = events.items.find((e) => e.entity.id === stop.id);
    expect(mine?.type).toBe('delivery.adjusted');
    const failed = await transition({ to: 'failed', revision: adjustedStop.revision, pod_signed_by: 'n/a' });
    expect(failed.status(), await failed.text()).toBe(200);
    expect((await failed.json()).status).toBe('failed');

    // A terminal stop refuses a second transition.
    const again = await transition({ to: 'failed', revision: adjustedStop.revision + 1 });
    expect(again.status()).toBe(409);
    expect((await again.json()).error.code).toBe('invalid_state_transition');
  });
});
