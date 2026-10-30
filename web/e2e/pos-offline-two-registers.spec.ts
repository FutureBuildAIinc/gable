// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { test, expect } from '@playwright/test';
import {
  ID,
  clearOffline,
  createManager,
  cutNetwork,
  enableOffline,
  enrolDevice,
  expectReadyToSellOffline,
  addOne,
  payCash,
  receiptRef,
  restoreNetwork,
  signIn,
  waitsOn,
} from './pos-offline';
import { stockedProduct } from './pos-stock';
import { openCounter } from './pos-till';

/**
 * Two registers (ADR 0011 section 7, the client list's last bullet): two work
 * shell contexts on two registers of one yard, both offline, both sync. The
 * receipts' offline references and the invoice numbers are unique.
 *
 * Each context is its own browser context, so each has its own storage and
 * its own queue: the stand in for two machines with two keychains. The
 * counter takes the current yard's register and the seed makes one register per
 * yard, so the two registers are two yards' (REG-01 and the multi yard seed's
 * second); an enrolment and a policy are per register and per yard. Sequence
 * numbers overlap because each register's first sale is its number one.
 */
const REGISTER_COUNT = 2;

test('two registers offline together: unique offline references and unique invoice numbers', async ({ browser, request }) => {
  waitsOn('O2', 'O4', 'C1', 'C2', 'ADR0012-5');
  const listed = await request.get('/api/v1/pos/registers');
  const all = ((await listed.json()) as { items: { id: string; branch_id: string }[] }).items;
  const first = all.find((r) => r.id === 'REG-01');
  const other = all.find((r) => first && r.branch_id !== first.branch_id);
  expect(first && other, 'the seed holds REG-01 and a register of a second yard (the multi yard seed)').toBeTruthy();
  const registers = [first!, other!].slice(0, REGISTER_COUNT);
  const manager = await createManager(request);
  for (const r of registers) await enableOffline(request, r.branch_id);
  try {
    for (const r of registers) await enrolDevice(request, r.id, manager);

    const contexts = await Promise.all(registers.map(() => browser.newContext()));
    const pages = await Promise.all(contexts.map((c) => c.newPage()));
    const refs: string[] = [];
    try {
      for (const [i, page] of pages.entries()) {
        await signIn(page, `POS Offline Register ${i + 1}`);
        // Each context works its own yard, so the counter takes that yard's register, then the drawer.
        await page.addInitScript((id) => localStorage.setItem('gable_current_branch_id', id), registers[i].branch_id);
        await openCounter(page);
        await expectReadyToSellOffline(page);
      }
      // Both offline with overlapping sequence numbers: each register's first sale is its sequence one.
      await Promise.all(contexts.map((c) => cutNetwork(c)));
      for (const page of pages) {
        await addOne(page, (await stockedProduct(request, registers[pages.indexOf(page)].branch_id)).sku);
        await payCash(page);
        refs.push(await receiptRef(page));
      }
      expect(new Set(refs).size, `offline references ${refs.join(', ')}`).toBe(registers.length);

      // Both sync at once.
      await Promise.all(contexts.map((c) => restoreNetwork(c)));
      for (const page of pages) await expect(page.getByTestId(ID.stripSent)).toContainText('Sent 1', { timeout: 30000 });
    } finally {
      await Promise.all(contexts.map((c) => c.close()));
    }

    // The server numbered each once: one POS number and one invoice number per sale, no repeat.
    const list = await request.get(`/api/v1/pos/transactions?limit=50`);
    expect(list.status(), await list.text()).toBe(200);
    const items = ((await list.json()) as { items: { offline_ref?: string | null; number: string; invoice_number?: string | null }[] })
      .items;
    const posted = items.filter((t) => t.offline_ref && refs.includes(t.offline_ref));
    expect(posted).toHaveLength(registers.length);
    expect(new Set(posted.map((t) => t.number)).size).toBe(registers.length);
    const invoices = posted.map((t) => t.invoice_number).filter((n): n is string => !!n);
    expect(invoices).toHaveLength(registers.length);
    expect(new Set(invoices).size).toBe(registers.length);
  } finally {
    for (const r of registers) await clearOffline(request, r.branch_id);
  }
});
