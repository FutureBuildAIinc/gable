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
 * A product the counter's own yard holds free, for the item search, together
 * with its base price so the credit room check is measured against the sale.
 *
 * The CI run seeds with the multi yard switch off, which makes the branch
 * header inert: every stock read then sums every yard. The sale itself is
 * issued at the register's yard only, so this helper rebuilds the yard's free
 * stock (quantity minus allocated, the sale's own measure) by pairing each
 * inventory row with the branch its location names, and picks against the
 * register's yard with room to spare. Earlier specs in the same run spend and
 * allocate the same catalogue pick, so an all-yard figure can read healthy
 * while the register's yard holds nothing free.
 */
async function stockedProduct(
  request: APIRequestContext,
  branchId: string,
): Promise<{ sku: string; base_price_ten_thousandths: number }> {
  const locations = (await (await request.get('/api/v1/locations?limit=200')).json()) as {
    items: { id: string; branch_id: string }[];
  };
  const branchOf = new Map(locations.items.map((l) => [l.id, l.branch_id]));

  const levels: { product_id: string; location_id: string | null; available: string }[] = [];
  let cursor = '';
  for (;;) {
    const page = (await (
      await request.get(`/api/v1/inventory?limit=200${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`)
    ).json()) as {
      items: { product_id: string; location_id: string | null; available: string }[];
      next_cursor: string | null;
    };
    levels.push(...page.items);
    if (!page.next_cursor) break;
    cursor = page.next_cursor;
  }
  const freeAtBranch = new Map<string, number>();
  for (const lv of levels) {
    if (!lv.location_id || branchOf.get(lv.location_id) !== branchId) continue;
    freeAtBranch.set(lv.product_id, (freeAtBranch.get(lv.product_id) ?? 0) + (Number(lv.available) || 0));
  }

  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as {
    items: { id: string; sku: string; base_price_ten_thousandths: number }[];
  };
  // The sale takes one piece; ask for a yard's free stock comfortably above that.
  const FLOOR = 10;
  for (const p of products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse()) {
    if ((freeAtBranch.get(p.id) ?? 0) >= FLOOR) return p;
  }
  throw new Error('no stocked product for the counter run');
}

interface CustomerHeader {
  id: string;
  name: string;
  account_number: string;
  email: string | null;
  phone: string | null;
  address: string | null;
  price_profile_id: string | null;
  salesperson_id: string | null;
  is_active: boolean;
  credit_limit_cents: number | null;
  balance_cents: number;
  currency: string | null;
  payment_terms_id: string;
  po_required: boolean;
  job_required: boolean;
  branch_ids: string[];
  revision: number;
}

interface PreparedAccount {
  id: string;
  name: string;
  account_number: string;
  job_id: string;
  original_credit_limit_cents: number | null;
  original_job_required: boolean;
}

/**
 * Prepares a single-branch customer at the register's branch with an active
 * job and credit room for the sale. In AUTH_MODE=dev the portal project route
 * (POST /api/portal/v1/projects) is pinned by serve.go to TARNWICK-001 (which
 * the multi-yard seed shares across KEL-MAIN and WK), while the seed's four
 * single-branch KEL-MAIN customers always hold active projects and vary only
 * in their random receivable balance. We query the register's branch directly
 * (X-Branch-Id), read the counter's own customer lookup for the active job and
 * open receivable, and set credit_limit_cents through the customer PUT so the
 * account always has room for the sale on every seed draw.
 */
async function prepareAccountWithJob(
  request: APIRequestContext,
  branchId: string,
  saleRoomCents: number,
): Promise<PreparedAccount> {
  const listRes = await request.get('/api/v1/customers?limit=50', {
    headers: { 'X-Branch-Id': branchId },
  });
  expect(listRes.status(), await listRes.text()).toBe(200);
  const customers = (await listRes.json()) as { items: CustomerHeader[] };
  for (const c of customers.items) {
    if (c.branch_ids.length !== 1 || c.branch_ids[0] !== branchId || !c.is_active) continue;
    const lookupRes = await request.get(
      `/api/v1/pos/customers?q=${encodeURIComponent(c.account_number)}`,
      { headers: { 'X-Branch-Id': branchId } },
    );
    expect(lookupRes.status(), await lookupRes.text()).toBe(200);
    const lookup = (await lookupRes.json()) as {
      id: string;
      open_receivable_cents: number;
      jobs: { id: string; name: string }[];
    }[];
    const counter = lookup.find((item) => item.id === c.id);
    if (!counter || counter.jobs.length === 0) continue;

    const neededLimit = Math.max(c.balance_cents, counter.open_receivable_cents, 0) + saleRoomCents;
    const targetLimit =
      c.credit_limit_cents === null ? neededLimit : Math.max(c.credit_limit_cents, neededLimit);
    const putRes = await request.put(`/api/v1/customers/${c.id}`, {
      headers: { 'X-Branch-Id': branchId },
      data: {
        account_number: c.account_number,
        name: c.name,
        email: c.email,
        phone: c.phone,
        address: c.address,
        price_profile_id: c.price_profile_id,
        salesperson_id: c.salesperson_id,
        is_active: c.is_active,
        credit_limit_cents: targetLimit,
        currency: c.currency,
        payment_terms_id: c.payment_terms_id,
        po_required: c.po_required,
        job_required: false,
        revision: c.revision,
      },
    });
    expect(putRes.status(), await putRes.text()).toBe(200);
    return {
      id: c.id,
      name: c.name,
      account_number: c.account_number,
      job_id: counter.jobs[0].id,
      original_credit_limit_cents: c.credit_limit_cents,
      original_job_required: c.job_required,
    };
  }
  throw new Error('expected a single-branch customer at the register branch with an active job');
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
  // The run works in the register's yard: the account page PUT and the
  // counter both write through the branch the header carries, and a write on
  // another yard's customer is a 404 (or shares the customer out).
  await page.addInitScript((id) => localStorage.setItem('gable_current_branch_id', id), branchId);
  await signIn(page, 'POS Account E2E');
  const product = await stockedProduct(request, branchId);
  const unitCents = Math.max(100, Math.ceil(product.base_price_ten_thousandths / 100));
  const saleRoomCents = unitCents * 4;
  const account = await prepareAccountWithJob(request, branchId, saleRoomCents);

  try {
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
    const sales = (
      await (
        await request.get(`/api/v1/pos/transactions?register_id=REG-01&limit=200`, {
          headers: { 'X-Branch-Id': branchId },
        })
      ).json()
    ) as {
      items: { id: string; customer_id: string | null; status: string }[];
    };
    const sale = sales.items.find((s) => s.customer_id === account.id && s.status === 'completed');
    expect(sale, 'the charge sale is listed for the account').toBeTruthy();
    const full = (
      await (
        await request.get(`/api/v1/pos/transactions/${sale!.id}`, {
          headers: { 'X-Branch-Id': branchId },
        })
      ).json()
    ) as {
      job_id: string | null;
      customer_po: string | null;
      revision: number;
    };
    expect(full.job_id).toBe(account.job_id);
    expect(full.customer_po).toBe('PO-E2E-JOB-91');

    // Void the run's charge sale so the account's balance and invoices return
    // to what the seed held.
    const voided = await request.post(`/api/v1/pos/transactions/${sale!.id}/void`, {
      headers: { 'X-Branch-Id': branchId },
      data: { reason: 'e2e cleanup', revision: full.revision },
    });
    expect(voided.status(), await voided.text()).toBe(200);
  } finally {
    // Put the account header back the way the seed held it. The customer
    // stayed on its one branch (the register's own), so the header the run
    // pins carries the write and the revert is a plain success.
    const fresh = (
      await (
        await request.get(`/api/v1/customers/${account.id}`, {
          headers: { 'X-Branch-Id': branchId },
        })
      ).json()
    ) as CustomerHeader;
    const revert = await request.put(`/api/v1/customers/${account.id}`, {
      headers: { 'X-Branch-Id': branchId },
      data: {
        account_number: fresh.account_number,
        name: fresh.name,
        email: fresh.email,
        phone: fresh.phone,
        address: fresh.address,
        price_profile_id: fresh.price_profile_id,
        salesperson_id: fresh.salesperson_id,
        is_active: fresh.is_active,
        credit_limit_cents: account.original_credit_limit_cents,
        currency: fresh.currency,
        payment_terms_id: fresh.payment_terms_id,
        po_required: fresh.po_required,
        job_required: account.original_job_required,
        revision: fresh.revision,
      },
    });
    expect(revert.status(), await revert.text()).toBe(200);
  }
});
