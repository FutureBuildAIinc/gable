// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import path from 'path';
import fs from 'fs';

/**
 * The desk's customer flow against the real stack (see playwright.config.ts), on the converted
 * customer contract: cents money with a null limit for no limit, payment terms by id, ship-to
 * addresses as an entity, a revision every write moves, the cursor list.
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

// Unique per run, so a database that already holds earlier runs still gives a clean list.
const RUN = Date.now().toString(36).toUpperCase();
let counter = 0;
function unique(): { account: string; name: string } {
  counter += 1;
  return { account: `E2E-${RUN}-${counter}`, name: `E2E Customer ${RUN} ${counter}` };
}

async function termsByCode(request: APIRequestContext, code: string): Promise<{ id: string; code: string; name: string }> {
  const res = await request.get('/api/v1/payment-terms?is_active=true&limit=200');
  expect(res.status(), await res.text()).toBe(200);
  const found = ((await res.json()) as { items: { id: string; code: string; name: string }[] }).items.find((t) => t.code === code);
  expect(found, `payment terms ${code} are seeded`).toBeTruthy();
  return found!;
}

async function makeCustomer(request: APIRequestContext, data: Record<string, unknown>) {
  const u = unique();
  const res = await request.post('/api/v1/customers', { data: { account_number: u.account, name: u.name, ...data } });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as { id: string; name: string; account_number: string; revision: number };
}

test.describe('Customer flow on the new contract', () => {
  test('create with payment terms, add ship-tos, edit at the loaded revision, and a stale save reloads', async ({ page, request }) => {
    const net45 = await termsByCode(request, 'NET45');
    const u = unique();
    await signIn(page, 'Playwright Customers');

    // Create through the form, with terms chosen and a credit limit in dollars.
    await page.goto('/accounts');
    await expect(page.getByRole('heading', { name: 'Accounts' })).toBeVisible();
    await page.getByRole('button', { name: 'New customer' }).click();
    await page.getByLabel('Name', { exact: true }).fill(u.name);
    await page.getByLabel('Account number').fill(u.account);
    await page.getByLabel('Email').fill('buyer@example.test');
    await page.getByLabel('Phone').fill('555-0142');
    await page.getByLabel('Billing address').fill('12 Mill Road');
    await page.getByLabel('Tier').last().selectOption('gold');
    await page.getByLabel('Payment terms').selectOption(net45.id);
    await page.getByLabel('Credit limit (dollars)').fill('2,500.50');
    await page.getByLabel('Purchase order required').check();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'customer-create-form.png') });

    const created = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/customers' && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Create customer' }).click();
    const res = await created;
    expect(res.status(), await res.text()).toBe(201);
    const sentBody = res.request().postDataJSON();
    expect(sentBody.payment_terms_id).toBe(net45.id);
    expect(sentBody.credit_limit_cents).toBe(250050);
    expect(sentBody.po_required).toBe(true);
    expect(sentBody.tier).toBe('gold');
    const customer = await res.json();
    expect(customer.tier).toBe('gold');
    expect(customer.credit_limit_cents).toBe(250050);
    expect(customer.payment_terms.code).toBe('NET45');
    expect(customer.po_required).toBe(true);
    expect(customer.revision).toBe(1);

    // The new customer's page: terms, PO required, currency and revision are shown.
    await expect(page).toHaveURL(new RegExp(`/accounts/${customer.id}$`));
    await expect(page.getByRole('heading', { name: u.name })).toBeVisible();
    await expect(page.locator('[data-fact="terms"]')).toContainText('NET45');
    await expect(page.locator('[data-fact="po"]')).toHaveText('Yes');
    await expect(page.locator('[data-fact="currency"]')).toContainText(customer.effective_currency);
    await expect(page.locator('[data-fact="revision"]')).toHaveText('1');

    // Ship-to addresses: add two, the first becomes the default.
    await page.getByRole('button', { name: 'Ship-to Addresses' }).click();
    await expect(page.getByText('No ship-to addresses yet.')).toBeVisible();
    const addShipTo = async (code: string, shipName: string, line1: string, makeDefault: boolean) => {
      await page.getByRole('button', { name: 'Add ship-to' }).click();
      await page.getByLabel('Code', { exact: true }).fill(code);
      await page.getByLabel('Ship-to name').fill(shipName);
      await page.getByLabel('Address line 1').fill(line1);
      await page.getByLabel('City').fill('Kelowna');
      await page.getByLabel('Region').fill('BC');
      await page.getByLabel('Postal code').fill('V1Y 1A1');
      await page.getByLabel('Country', { exact: true }).fill('ca');
      await page.getByLabel('Tax rate percent').fill('12');
      if (makeDefault) await page.getByLabel('Make default').check();
      const posted = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/customers/${customer.id}/ship-tos` && r.request().method() === 'POST');
      await page.getByRole('button', { name: 'Save ship-to' }).click();
      const shipRes = await posted;
      expect(shipRes.status(), await shipRes.text()).toBe(201);
      expect(shipRes.request().postDataJSON().country).toBe('CA');
      expect(shipRes.request().postDataJSON().tax_rate_percent).toBe('12');
    };
    await addShipTo('YARD1', 'Main Yard', '12 Mill Road', false);
    await addShipTo('SITE2', 'Lakeside Site', '90 Shore Drive', false);

    const yard = page.locator('[data-ship-to="YARD1"]');
    const site = page.locator('[data-ship-to="SITE2"]');
    await expect(yard).toContainText('Main Yard');
    await expect(site).toContainText('Lakeside Site');
    await expect(yard.getByText('Default', { exact: true })).toBeVisible();
    await expect(site.getByText('Default', { exact: true })).toHaveCount(0);
    await expect(site).toContainText('12%');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'customer-ship-tos.png') });

    // Deactivate the second ship-to through is_active; it stays listed with the inactive marker.
    const deactivated = page.waitForResponse((r) => new URL(r.url()).pathname.startsWith('/api/v1/ship-tos/') && r.request().method() === 'PUT');
    await site.getByRole('button', { name: 'Deactivate' }).click();
    const deactivatedRes = await deactivated;
    expect(deactivatedRes.status(), await deactivatedRes.text()).toBe(200);
    expect(deactivatedRes.request().headers()['if-match']).toBe('"1"');
    await expect(site.getByText('Inactive', { exact: true })).toBeVisible();

    // Edit the header at its loaded revision.
    const renamed = `${u.name} Renamed`;
    await page.getByRole('button', { name: 'Edit Profile' }).click();
    await page.getByLabel('Name', { exact: true }).fill(renamed);
    await page.getByLabel('Credit limit (dollars)').fill('');
    const put = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/customers/${customer.id}` && r.request().method() === 'PUT');
    await page.getByRole('button', { name: 'Save changes' }).click();
    const putRes = await put;
    expect(putRes.status(), await putRes.text()).toBe(200);
    expect(putRes.request().headers()['if-match']).toBe('"1"');
    const putSent = putRes.request().postDataJSON();
    expect(putSent.credit_limit_cents).toBeNull();
    expect(putSent.payment_terms_id).toBe(net45.id);
    expect(putSent).not.toHaveProperty('balance_cents');
    const edited = await putRes.json();
    expect(edited.name).toBe(renamed);
    expect(edited.revision).toBe(2);
    expect(edited.credit_limit_cents).toBeNull();
    await expect(page.getByRole('heading', { name: renamed })).toBeVisible();
    await expect(page.locator('[data-fact="revision"]')).toHaveText('2');
    await expect(page.getByText('No limit').first()).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'customer-edited.png') });

    // Someone else saves the customer while this page is at revision 2: the stale save is a 409 and the page reloads.
    const current = (await (await request.get(`/api/v1/customers/${customer.id}`)).json()) as Record<string, unknown>;
    expect(current.revision).toBe(2);
    const elsewhereName = `${u.name} Elsewhere`;
    const elsewhere = await request.put(`/api/v1/customers/${customer.id}`, {
      data: {
        account_number: current.account_number, name: elsewhereName, email: current.email, phone: current.phone,
        address: current.address, tier: current.tier, is_active: current.is_active, credit_limit_cents: null,
        payment_terms_id: current.payment_terms_id, po_required: current.po_required,
      },
      headers: { 'If-Match': '"2"' },
    });
    expect(elsewhere.status(), await elsewhere.text()).toBe(200);
    expect((await elsewhere.json()).revision).toBe(3);

    await page.getByRole('button', { name: 'Edit Profile' }).click();
    await page.getByLabel('Name', { exact: true }).fill(`${u.name} Stale`);
    const stale = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/customers/${customer.id}` && r.request().method() === 'PUT');
    await page.getByRole('button', { name: 'Save changes' }).click();
    const staleRes = await stale;
    expect(staleRes.status()).toBe(409);
    expect(staleRes.request().headers()['if-match']).toBe('"2"');
    expect((await staleRes.json()).error.code).toBe('stale_revision');
    await expect(page.getByText(/changed after this revision was read/)).toBeVisible();
    await expect(page.getByText(/The account was reloaded/)).toBeVisible();
    await expect(page.getByRole('heading', { name: elsewhereName })).toBeVisible();
    await expect(page.locator('[data-fact="revision"]')).toHaveText('3');
    await expect(page.getByRole('button', { name: 'Save changes' })).toHaveCount(0);
    await page.screenshot({ path: path.join(SHOTS_DIR, 'customer-stale-edit.png') });

    const after = (await (await request.get(`/api/v1/customers/${customer.id}`)).json()) as { name: string; revision: number };
    expect(after.name).toBe(elsewhereName);
    expect(after.revision).toBe(3);
  });

  test('a field problem from the server shows beside its field, for the ship-to form and the create form', async ({ page, request }) => {
    const customer = await makeCustomer(request, { credit_limit_cents: null });
    await signIn(page, 'Playwright Field Errors');

    await page.goto(`/accounts/${customer.id}`);
    await page.getByRole('button', { name: 'Ship-to Addresses' }).click();
    await page.getByRole('button', { name: 'Add ship-to' }).click();
    await page.getByLabel('Code', { exact: true }).fill('BAD1');
    await page.getByLabel('Ship-to name').fill('Bad rate');
    await page.getByLabel('Address line 1').fill('1 Test Street');
    await page.getByLabel('Tax rate percent').fill('150');
    const posted = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/customers/${customer.id}/ship-tos` && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Save ship-to' }).click();
    expect((await posted).status()).toBe(400);
    const rateField = page.locator('div', { has: page.getByLabel('Tax rate percent') }).locator('[data-field-error]').first();
    await expect(rateField).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'customer-field-error.png') });

    // A duplicate account number on create is refused and stays on the form.
    await page.goto('/accounts');
    await page.getByRole('button', { name: 'New customer' }).click();
    await page.getByLabel('Name', { exact: true }).fill('Duplicate Account');
    await page.getByLabel('Account number').fill(customer.account_number);
    const dup = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/customers' && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Create customer' }).click();
    expect((await dup).status()).toBe(409);
    await expect(page.getByRole('button', { name: 'Create customer' })).toBeVisible();
    await expect(page).toHaveURL(/\/accounts$/);

    // A credit limit that is not money is caught before any request.
    await page.getByLabel('Account number').fill(`${customer.account_number}-X`);
    await page.getByLabel('Credit limit (dollars)').fill('12.345');
    await page.getByRole('button', { name: 'Create customer' }).click();
    await expect(page.getByText('Use at most two decimal places')).toBeVisible();
  });

  test('the list searches on the server, shows No limit, and splits credit from cash on a zero limit', async ({ page, request }) => {
    const noLimit = await makeCustomer(request, { credit_limit_cents: null });
    const cashOnly = await makeCustomer(request, { credit_limit_cents: 0 });
    const capped = await makeCustomer(request, { credit_limit_cents: 100000 });
    expect(noLimit.revision).toBe(1);
    await signIn(page, 'Playwright Accounts List');

    await page.goto('/accounts');
    const searched = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/customers' && new URL(r.url()).searchParams.get('q') === RUN);
    await page.getByLabel('Search accounts').fill(RUN);
    const searchRes = await searched;
    expect(searchRes.status()).toBe(200);

    const row = (c: { id: string }) => page.locator(`a[href="/accounts/${c.id}"]`);
    await expect(row(noLimit)).toBeVisible();
    await expect(row(cashOnly)).toBeVisible();
    await expect(row(capped)).toBeVisible();
    await expect(row(noLimit)).toContainText('No limit');
    await expect(row(capped)).toContainText('$1,000.00');
    await page.screenshot({ path: path.join(SHOTS_DIR, 'customer-list.png') });

    await page.getByRole('button', { name: 'Cash', exact: true }).click();
    await expect(row(cashOnly)).toBeVisible();
    await expect(row(noLimit)).toHaveCount(0);
    await expect(row(capped)).toHaveCount(0);

    await page.getByRole('button', { name: 'Credit', exact: true }).click();
    await expect(row(noLimit)).toBeVisible();
    await expect(row(capped)).toBeVisible();
    await expect(row(cashOnly)).toHaveCount(0);

    // The cursor: a page of one, then the next.
    const first = (await (await request.get('/api/v1/customers?limit=1&include=total')).json()) as { items: { id: string }[]; next_cursor: string | null; total: number };
    expect(first.items).toHaveLength(1);
    expect(typeof first.next_cursor).toBe('string');
    expect(first.total).toBeGreaterThan(1);
    const second = (await (await request.get(`/api/v1/customers?limit=1&cursor=${first.next_cursor}`)).json()) as { items: { id: string }[] };
    expect(second.items[0].id).not.toBe(first.items[0].id);
    const badParam = await request.get('/api/v1/customers?offset=0');
    expect(badParam.status()).toBe(400);
  });

  test('a contact is added with its order authority, edited on its revision, and deleted with If-Match', async ({ page, request }) => {
    const customer = await makeCustomer(request, {});
    await signIn(page, 'Playwright Contacts');

    await page.goto(`/accounts/${customer.id}`);
    await page.getByRole('button', { name: 'Contacts', exact: true }).click();
    await expect(page.getByText('No contacts found for this account.')).toBeVisible();
    await page.getByRole('button', { name: 'Add Contact' }).click();
    await page.getByLabel('First name').fill('Robin');
    await page.getByLabel('Last name').fill('Tester');
    await page.getByLabel('Order limit (dollars)').fill('750');
    const created = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/customers/${customer.id}/contacts` && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Save contact' }).click();
    const createdRes = await created;
    expect(createdRes.status(), await createdRes.text()).toBe(201);
    expect(createdRes.request().postDataJSON().order_limit_cents).toBe(75000);
    expect(createdRes.request().postDataJSON().can_place_orders).toBe(true);
    const contact = await createdRes.json();
    expect(contact.revision).toBe(1);

    await expect(page.getByText('Robin Tester')).toBeVisible();
    await expect(page.getByText('Up to $750.00')).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS_DIR, 'customer-contacts.png') });

    await page.getByRole('button', { name: 'Edit', exact: true }).click();
    await page.getByLabel('Can place orders').uncheck();
    const put = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/contacts/${contact.id}` && r.request().method() === 'PUT');
    await page.getByRole('button', { name: 'Save contact' }).click();
    const putRes = await put;
    expect(putRes.status(), await putRes.text()).toBe(200);
    expect(putRes.request().headers()['if-match']).toBe('"1"');
    expect((await putRes.json()).revision).toBe(2);
    await expect(page.getByText('Cannot place orders')).toBeVisible();

    page.once('dialog', (d) => void d.accept());
    const del = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/contacts/${contact.id}` && r.request().method() === 'DELETE');
    await page.getByRole('button', { name: 'Delete', exact: true }).click();
    const delRes = await del;
    expect(delRes.status()).toBe(204);
    expect(delRes.request().headers()['if-match']).toBe('"2"');
    await expect(page.getByText('No contacts found for this account.')).toBeVisible();
  });
});
