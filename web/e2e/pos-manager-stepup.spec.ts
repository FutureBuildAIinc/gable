// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { answerAppDialog, fillAppDialogPrompt, pickOptionByValue } from './app-controls';
import { stockedProduct } from './pos-stock';

/**
 * The manager step up at the terminal (CP-04, with CP-08's pickup and sign
 * off): an admin sets a manager's counter PIN on the staff screen, then a
 * cashier at /pos gets a manager's approval for an over limit check with
 * that PIN, a pickup's taker is the manager by PIN, and a held variance is
 * signed off by PIN. The sale names the manager; the wrong PIN is refused
 * in plain words.
 */

const PIN = '4481';
const WRONG_PIN = '9999';

/** A manager on the roster, with an optional counter PIN set through the admin API. */
async function createManager(request: APIRequestContext, withPin = true): Promise<{ staffNo: string; name: string }> {
  const run = Date.now().toString(36).toUpperCase();
  const staffNo = `E2E-MGR-${run}`;
  const name = `E2E Manager ${run}`;
  // the owner role may approve every act: an override, a pickup, a sign off
  const res = await request.post('/api/v1/admin/staff', {
    data: { email: `e2e-mgr-${run}@example.com`, full_name: name, staff_no: staffNo, role: 'owner', active: true },
  });
  expect(res.status(), await res.text()).toBe(201);
  const member = (await res.json()) as { id: string };
  if (withPin) {
    const pin = await request.put(`/api/v1/admin/staff/${member.id}/counter-pin`, { data: { pin: PIN } });
    expect(pin.status(), await pin.text()).toBe(200);
  }
  return { staffNo, name };
}

async function signIn(page: Page, name: string) {
  await page.goto('/');
  await page.getByLabel('Display name').fill(name);
  const catalog = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/apps' && r.ok());
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await catalog;
  await expect(page.getByRole('heading', { name: /good to see you/i })).toBeVisible();
}

async function openCounter(page: Page) {
  await page.goto('/pos');
  await expect(page.getByRole('heading', { name: 'New sale', level: 1 })).toBeVisible();
  const tillState = page.getByTestId('pos-till-state');
  await expect(tillState).toBeVisible();
  if ((await tillState.textContent())?.includes('No till')) {
    await page.getByRole('button', { name: 'Open Till' }).click();
    await page.getByTestId('till-float-amount').fill('100.00');
    await page.getByRole('button', { name: 'Open Till' }).last().click();
  }
  await expect(tillState).toContainText('Till open');
}

async function addOne(page: Page, sku: string) {
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
}

async function openPayment(page: Page) {
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
}

/** Types the PIN on the step up dialog's keypad and approves. */
async function stepUp(page: Page, pin: string, staffNo: string) {
  await expect(page.getByTestId('step-up-dialog')).toBeVisible();
  await pickOptionByValue(page.getByTestId('step-up-manager'), staffNo);
  for (const d of pin) await page.getByTestId(`step-up-key-${d}`).click();
  await page.getByTestId('step-up-approve').click();
}

test('the manager step up: PIN set on the staff screen, an over limit check approved by PIN', async ({ page, request }) => {
  const manager = await createManager(request, false);
  // An account whose check limit is one dollar: any real check is over it.
  const run = Date.now().toString(36).toUpperCase();
  const accountRes = await request.post('/api/v1/customers', {
    data: { account_number: `E2E-CHK-${run}`, name: `E2E Check Limit ${run}`, check_limit_cents: 100 },
  });
  expect(accountRes.status(), await accountRes.text()).toBe(201);
  const account = (await accountRes.json()) as { id: string; name: string; account_number: string };

  // The staff screen: an admin sets the manager's counter PIN on the Staff tab
  // and the roster says Set.
  await signIn(page, 'POS Step Up E2E');
  await page.goto('/admin');
  await page.getByRole('button', { name: 'Staff', exact: true }).click();
  const mgrRow = page.locator('tbody tr', { hasText: manager.name });
  await expect(mgrRow.getByTestId('staff-pin-none')).toBeVisible();
  await mgrRow.getByTestId('staff-pin-set-new').click();
  await fillAppDialogPrompt(page, PIN);
  await answerAppDialog(page, 'Set PIN');
  await expect(mgrRow.getByTestId('staff-pin-set')).toBeVisible();

  const product = await stockedProduct(request);
  await openCounter(page);

  await page.locator('#pos-customer-search').fill(account.name);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(account.account_number) }).click();
  await expect(page.getByTestId('pos-customer-name')).toHaveText(account.name);
  await addOne(page, product.sku);
  await page.getByTestId('pos-next-payment').click();
  await openPayment(page);

  // A check over the one dollar limit: the words say it, with the way on.
  await page.getByTestId('tender-check').click();
  await expect(page.getByTestId('pos-check-limit')).toBeVisible();
  const total = Number(((await page.getByTestId('summary-total').textContent()) ?? '').replace(/[$,\s]/g, ''));
  await page.getByLabel('Tender amount').fill(total.toFixed(2));
  // the refused tender is not added: the words say it and the way is on
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-check-over')).toBeVisible();
  await page.getByTestId('pos-check-ask-manager').click();
  await fillAppDialogPrompt(page, 'e2e: the manager knows this customer');
  await answerAppDialog(page, 'Approve');

  // The wrong PIN is refused in plain words; the right one approves and the
  // manager is named.
  await stepUp(page, WRONG_PIN, manager.staffNo);
  await expect(page.getByTestId('step-up-refusal')).toContainText('That PIN is not right');
  await stepUp(page, PIN, manager.staffNo);
  await expect(page.getByText(`Approved by ${manager.name}`)).toBeVisible({ timeout: 15000 });

  // The sale completes on the check the manager approved.
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
  await page.getByTestId('pos-pay').click();
  const card = page.getByTestId('pos-complete');
  await expect(card).toContainText('Sale complete', { timeout: 15000 });

  // The books: the override names the manager, not the cashier.
  const sales = (await (await request.get('/api/v1/pos/transactions?register_id=REG-01')).json()) as {
    items: { id: string; customer_id: string | null; status: string }[];
  };
  const sale = sales.items.find((s) => s.customer_id === account.id && s.status === 'completed');
  expect(sale, 'the check sale is completed for the account').toBeTruthy();
  const full = (await (await request.get(`/api/v1/pos/transactions/${sale!.id}`)).json()) as {
    tenders: { method: string }[];
  };
  expect(full.tenders.map((t) => t.method)).toEqual(['check']);
});

test('a pickup taken and a held variance signed off by the manager\'s PIN', async ({ page, request }) => {
  const manager = await createManager(request, true);
  await signIn(page, 'POS Step Up B E2E');
  await openCounter(page);

  // A pickup with the manager as the taker by PIN.
  await page.getByTestId('pos-drawer-cash').click();
  await page.getByTestId('movement-kind-pickup').click();
  await page.getByTestId('movement-amount').fill('20.00');
  await page.getByTestId('movement-ask-manager').click();
  await stepUp(page, PIN, manager.staffNo);
  await expect(page.getByText(`Pickup of $20.00 recorded by ${manager.name}`)).toBeVisible({ timeout: 15000 });
  const current = (await (await request.get('/api/v1/pos/till/current')).json()) as { id: string };
  const movements = (await (await request.get(`/api/v1/pos/till/${current.id}/movements`)).json()) as {
    items: { kind: string; taken_by: string | null }[];
  };
  const pickup = movements.items.find((m) => m.kind === 'pickup');
  expect(pickup?.taken_by, 'the pickup names the manager as the taker').toBe(manager.name);

  // A count far over the threshold waits for a manager; the sign off queue
  // steps the manager up with the same PIN.
  await page.getByRole('button', { name: 'Close Till' }).click();
  // the count dialog lists one field per tender the drawer expects; the
  // cash field is the row whose label reads Cash
  // every field the drawer expects must be counted: cash at 60 (the drawer
  // holds 80 of it after the float and the pickup), the rest at nothing
  const counts = page.getByRole('spinbutton');
  await expect(counts.first()).toBeVisible();
  for (let i = 0; i < await counts.count(); i++) {
    await counts.nth(i).fill(i === 0 ? '60.00' : '0.00');
  }
  await page.getByRole('button', { name: 'Post Count & Close' }).click();
  await expect(page.getByTestId('till-waiting-signoff')).toBeVisible({ timeout: 15000 });

  await page.goto('/reports/daily-till');
  const queue = page.getByTestId('signoff-queue');
  await expect(queue).toBeVisible({ timeout: 15000 });
  const reason = queue.locator('input[type="text"]').first();
  await reason.fill('e2e: counted twice, short accepted');
  await queue.getByRole('button', { name: 'Sign off' }).click();
  await stepUp(page, PIN, manager.staffNo);
  await expect(page.getByText(new RegExp(`signed off by ${manager.name}`))).toBeVisible({ timeout: 15000 });
});
