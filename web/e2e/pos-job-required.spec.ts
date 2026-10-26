// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { pickOptionByValue } from './app-controls';
import path from 'path';
import fs from 'fs';

/**
 * CP-02: an account that requires a job on a charge sale. Job required is
 * ticked on the account page, the counter marks the Job select as required
 * for a charge and keeps the charge off until a job is picked, and the
 * completed sale carries the job.
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

/** The branch the counter's register belongs to, the yard the whole run works in. */
async function registerBranch(request: APIRequestContext): Promise<string> {
  const registers = (await (await request.get('/api/v1/pos/registers')).json()) as {
    items: { id: string; branch_id: string }[];
  };
  const reg = registers.items.find((r) => r.id === 'REG-01');
  if (!reg) throw new Error('the seed holds no REG-01 register for the counter run');
  return reg.branch_id;
}

/**
 * A seeded customer with at least one active job and credit room, discovered
 * through its quotes (the seed draws fresh job ids every run). The counter's
 * job roster is the customer's active projects, which only the seed carries.
 */
async function customerWithAJob(
  request: APIRequestContext,
  branchId: string,
): Promise<{ id: string; name: string; account_number: string; job_id: string } | null> {
  const quotes = (await (await request.get('/api/v1/quotes?limit=200')).json()) as {
    items: { customer_id: string; job_id: string | null }[];
  };
  const jobsByCustomer = new Map<string, Set<string>>();
  for (const q of quotes.items) {
    if (!q.job_id) continue;
    if (!jobsByCustomer.has(q.customer_id)) jobsByCustomer.set(q.customer_id, new Set<string>());
    jobsByCustomer.get(q.customer_id)!.add(q.job_id);
  }
  for (const [customer, jobs] of jobsByCustomer) {
    const c = (await (await request.get(`/api/v1/customers/${customer}`)).json()) as {
      id: string; name: string; account_number: string; credit_limit_cents: number | null; balance_cents: number;
      branch_ids: string[]; is_active: boolean;
    };
    // one branch only, and the register's own: a shared customer's restricted
    // fields (the Job required control among them) are refused to the role the
    // run signs in, and a write through another yard's header is a 404
    if (c.branch_ids.length !== 1 || c.branch_ids[0] !== branchId || !c.is_active) continue;
    if (c.credit_limit_cents === null || c.credit_limit_cents - c.balance_cents >= 2_000_000) {
      return { id: c.id, name: c.name, account_number: c.account_number, job_id: [...jobs][0] };
    }
  }
  return null;
}

/** A product with stock at the counter's branch, for the item search. */
async function stockedProduct(request: APIRequestContext): Promise<{ sku: string }> {
  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as { items: { id: string; sku: string }[] };
  for (const p of products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse()) {
    const page = (await (await request.get(`/api/v1/inventory?product_id=${p.id}`)).json()) as {
      items: { location_name: string; available: string }[];
    };
    const atBranch = page.items
      .filter((r) => r.location_name === 'KEL-MAIN')
      .reduce((n, r) => n + (Number(r.available) || 0), 0);
    if (atBranch >= 50) return p;
  }
  throw new Error('no stocked product for the counter run');
}

/** Opens the Payment panel (one panel is open at a time; its tenders are in it). */
async function openPayment(page: Page) {
  const head = page.getByTestId('panel-payment-toggle');
  if ((await head.getAttribute('aria-expanded')) !== 'true') await head.click();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
}

/** The counter with the drawer open (see pos.spec.ts's openCounter). */
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

test('the counter: an account that requires a job is charged only with a job picked', async ({ page, request }) => {
  const branchId = await registerBranch(request);
  const account = await customerWithAJob(request, branchId);
  if (!account) {
    test.skip(true, 'the seed holds no single branch customer at the register branch with a job and credit room to charge');
    return;
  }
  // The run works in the register's yard: the account page PUT and the
  // counter both write through the branch the header carries, and a write on
  // another yard's customer is a 404 (or shares the customer out).
  await page.addInitScript((id) => localStorage.setItem('gable_current_branch_id', id), branchId);
  await signIn(page, 'POS Account E2E');
  const product = await stockedProduct(request);

  // The account page: Job required is set beside PO required.
  await page.goto(`/accounts/${account.id}`);
  await expect(page.getByRole('heading', { name: account.name })).toBeVisible();
  await expect(page.locator('[data-fact="job"]')).toHaveText('No');
  await page.getByRole('button', { name: 'Edit Profile' }).click();
  await page.getByLabel('Job required').check();
  const saved = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/customers/${account.id}` && r.request().method() === 'PUT');
  await page.getByRole('button', { name: 'Save changes' }).click();
  expect((await saved).status(), await (await saved).text()).toBe(200);
  await expect(page.locator('[data-fact="job"]')).toHaveText('Yes');
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-job-required-account.png'), fullPage: true });

  // The counter: the account attached, the Job select marked and the charge off.
  await openCounter(page);
  await page.locator('#pos-customer-search').fill(account.account_number);
  await page.getByTestId('pos-customer-results').getByRole('button', { name: new RegExp(account.account_number) }).click();
  await expect(page.getByTestId('pos-customer-name')).toHaveText(account.name);
  await expect(page.locator('label[for="pos-job"]')).toContainText('required for a charge');
  const lines = page.locator('[data-testid="pos-lines"] li');
  const before = await lines.count();
  await page.getByTestId('catalog-search').fill(product.sku);
  await page.getByTestId('catalog-search').press('Enter');
  await expect(lines).toHaveCount(before + 1, { timeout: 10000 });
  await page.getByLabel(/Purchase order/).fill('PO-E2E-JOB-91');
  await page.getByLabel(/Purchase order/).blur();
  await page.getByTestId('pos-next-payment').click();
  const charge = page.getByRole('button', { name: 'Charge to account' });
  await expect(charge).toBeDisabled();
  await expect(page.getByTestId('pos-account-refusal')).toContainText(/requires a job on every charge sale/);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-job-required-refused.png'), fullPage: true });

  // A job picked: the charge is offered and the sale completes on the job.
  // The Job select lives in the Customer panel, which the move to Payment
  // collapsed, so the panel's own head reopens it.
  await page.getByTestId('panel-customer-toggle').click();
  await expect(page.locator('#pos-job')).toBeVisible();
  await pickOptionByValue(page.locator('#pos-job'), account.job_id);
  await openPayment(page);
  await expect(charge).toBeEnabled();
  await charge.click();
  await page.getByRole('button', { name: 'Add as split tender' }).click();
  await expect(page.getByTestId('pos-added-tenders')).toContainText('Covered');
  await page.getByTestId('pos-pay').click();
  await expect(page.getByTestId('pos-complete')).toContainText('Sale complete', { timeout: 15000 });
  // The completed sale shows what it was charged against, on the screen.
  await expect(page.getByTestId('pos-complete-refs')).toContainText('PO PO-E2E-JOB-91');
  await expect(page.getByTestId('pos-complete-refs')).toContainText(/Job \S/);
  await expect(page.getByTestId('pos-order-refs')).toContainText('PO PO-E2E-JOB-91');
  await expect(page.getByTestId('pos-order-refs')).toContainText(/Job \S/);
  await page.screenshot({ path: path.join(SHOTS_DIR, 'pos-job-required-charged.png'), fullPage: true });

  // The books: the completed sale and its invoice carry the job.
  const sales = (await (await request.get(`/api/v1/pos/transactions?register_id=REG-01&limit=200`)).json()) as {
    items: { id: string; customer_id: string | null; status: string }[];
  };
  const sale = sales.items.find((s) => s.customer_id === account.id && s.status === 'completed');
  expect(sale, 'the charge sale is listed for the account').toBeTruthy();
  const full = (await (await request.get(`/api/v1/pos/transactions/${sale!.id}`)).json()) as {
    job_id: string | null; customer_po: string | null;
  };
  expect(full.job_id).toBe(account.job_id);
  expect(full.customer_po).toBe('PO-E2E-JOB-91');

  // Put the account back the way the seed held it. The customer stayed on its
  // one branch (the register's own), so the header the run pins carries the
  // write and the revert is a plain success.
  const fresh = (await (await request.get(`/api/v1/customers/${account.id}`)).json()) as Record<string, unknown>;
  const revert = await request.put(`/api/v1/customers/${account.id}`, {
    headers: { 'X-Branch-Id': branchId },
    data: {
      account_number: fresh.account_number, name: fresh.name, email: fresh.email, phone: fresh.phone,
      address: fresh.address, price_profile_id: fresh.price_profile_id, is_active: fresh.is_active,
      credit_limit_cents: fresh.credit_limit_cents, currency: fresh.currency, payment_terms_id: fresh.payment_terms_id,
      po_required: fresh.po_required, job_required: false, revision: fresh.revision,
    },
  });
  expect(revert.status(), await revert.text()).toBe(200);
});
