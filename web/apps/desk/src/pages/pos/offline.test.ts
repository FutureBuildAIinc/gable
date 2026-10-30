// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's offline words and rules (ADR 0011 sections 4 and 5.3), as
// pure functions: the tenders the register takes offline, the cart carried
// from the server, the sync's words, what holds the till close and the
// offline receipt.

import { describe, expect, it } from 'vitest';
import type { Sale } from '../../types/pos';
import { emptyQueue, offlineSnapshot } from '../../test/fakeOfflineCore';
import type { OfflineQueueItem, OfflineSignedSale } from '../../types/offline';
import {
  NEEDS_CONNECTION, QUEUE_HOLDS_CLOSE, RE_ENROL, RECEIPT_PRICES_NOTE, UNKNOWN_HOLDS_CLOSE,
  carryCart, isLostAnswer, ratePercent, receiptLines, snapshotRefusalWords, syncWords, tenderAvailability, tillCloseWait,
} from './offline';

const line = (id: string, type = 'product', product = `p-${id}`) => ({
  id, position: 1, line_type: type, parent_line_id: null, product_id: product, charge_code_id: null, charge_code: null,
  sku: `SKU-${id}`, description: `Item ${id}`, quantity: '2', uom: 'EA',
});

const sale = (status = 'open', lines = [line('a')]) => ({ id: 'cart-1', status, lines } as unknown as Sale);

describe('the tenders offline', () => {
  const snap = offlineSnapshot();
  it('greys card and store credit with the connection\'s words', () => {
    for (const m of ['card', 'store_credit'] as const) {
      expect(tenderAvailability(snap, m, null)).toEqual({ method: m, enabled: false, words: NEEDS_CONNECTION });
    }
  });
  it('takes cash and check where the branch allows them, and refuses one it does not', () => {
    expect(tenderAvailability(snap, 'cash', null).enabled).toBe(true);
    expect(tenderAvailability(snap, 'check', null).enabled).toBe(true);
    const cashOnly = { ...snap, policy: { ...snap.policy, allowed_tenders: ['cash'] } };
    expect(tenderAvailability(cashOnly, 'check', null)).toMatchObject({ enabled: false, words: 'not taken offline at this branch' });
  });
  it('takes the account only for a cached account with room left', () => {
    expect(tenderAvailability(snap, 'account', null)).toMatchObject({ enabled: false, words: 'cached accounts only' });
    expect(tenderAvailability(snap, 'account', 0).enabled).toBe(false);
    expect(tenderAvailability(snap, 'account', 100).enabled).toBe(true);
  });
});

describe('the open cart becomes the local cart', () => {
  const snap = offlineSnapshot();
  let n = 0;
  const id = () => `l${++n}`;
  it('carries the product lines with their units and quantities and names the cart', () => {
    const carried = carryCart(sale('open', [line('a')]), snap, id);
    expect(carried.replacesSaleId).toBe('cart-1');
    expect(carried.lines).toEqual([{ id: expect.any(String), product_id: 'p-a', quantity: '2', uom: 'EA' }]);
    expect(carried.refused).toEqual([]);
  });
  it('leaves charge and kit lines behind in words, and a text line carries nothing', () => {
    const carried = carryCart(sale('open', [line('a'), line('c', 'charge', 'x'), line('k', 'product', 'p-kit'), line('t', 'text', 'x')]), snap, id);
    expect(carried.lines).toHaveLength(1);
    expect(carried.refused).toEqual(['Item c: charge lines do not sell offline', 'Item k: kits do not sell offline']);
  });
  it('carries nothing from a completed sale', () => {
    expect(carryCart(sale('completed'), snap, id)).toEqual({ lines: [], refused: [], replacesSaleId: null });
  });
});

describe('the strip and the close', () => {
  it('says what the sync did, in the record\'s words', () => {
    expect(syncWords({ sent: 13, posted: 12, refused: 1, held: 0, resolved: 0, errors: 0, confirmed_seq: 13, open_exception_count: 1, queue: emptyQueue() }))
      .toBe('Sent 12, 1 needs a manager');
    expect(syncWords({ sent: 2, posted: 1, refused: 0, held: 0, resolved: 0, errors: 1, confirmed_seq: 1, open_exception_count: 0, queue: emptyQueue() }))
      .toBe('Sent 1, 1 will be sent again');
  });
  const item = (state: OfflineQueueItem['state']): OfflineQueueItem => ({
    device_seq: 1, state, reason_code: null, reason_message: null, invoice_id: null, invoice_number: null, sale: {} as OfflineSignedSale,
  });
  it('holds the till close for an unknown cart first, then for what waits, then for a manager', () => {
    expect(tillCloseWait(emptyQueue(), 'cart-1')).toBe(UNKNOWN_HOLDS_CLOSE);
    expect(tillCloseWait({ ...emptyQueue(), items: [item('waiting')] }, null)).toBe(QUEUE_HOLDS_CLOSE);
    expect(tillCloseWait({ ...emptyQueue(), items: [item('review')] }, null)).toContain('on a manager');
    expect(tillCloseWait({ ...emptyQueue(), items: [item('synced')] }, null)).toBeNull();
    expect(tillCloseWait(null, null)).toBeNull();
  });
  it('says why new prices could not be taken, and that the register keeps selling', () => {
    expect(snapshotRefusalWords('queue_not_drained', 'Sale REG-01-2-000004 has not been answered.')).toContain('REG-01-2-000004');
    expect(snapshotRefusalWords('queue_not_drained', 'x')).toContain('keeps selling on the prices it holds');
    expect(snapshotRefusalWords('re_enrol_required', 'x')).toBe(RE_ENROL);
  });
});

describe('the offline receipt', () => {
  const signed = {
    offline_ref: 'REG-01-2-000041', business_date: '2026-04-19', subtotal_cents: 1100, tax_cents: 91, total_cents: 1191, change_cents: 9,
    tax_exempt: false, lines: [{ quantity: '2', uom: 'EA', sku: 'SKU-a', description: 'Item a', line_total_cents: 1100, level_fallback: true }],
    tenders: [{ method: 'cash', amount_cents: 1200, reference: null }],
  } as unknown as OfflineSignedSale;
  it('reads OFFLINE SALE with the reference, the level price mark and the prices note, and no invoice number', () => {
    const lines = receiptLines(signed, null);
    expect(lines[0]).toBe('OFFLINE SALE');
    expect(lines).toContain('Reference REG-01-2-000041');
    expect(lines.some((l) => l.includes('level price'))).toBe(true);
    expect(lines).toContain(RECEIPT_PRICES_NOTE);
    expect(lines.join(' ')).not.toMatch(/invoice|IN-/i);
    expect(lines).toContain('Total $11.91');
    expect(lines).toContain('Change $0.09');
  });

  it('prints one Tax line for a US sale, exactly as before (CP-14 A1)', () => {
    const lines = receiptLines(signed, null);
    expect(lines).toContain('Tax $0.91');
    expect(lines.some((l) => l.startsWith('GST') || l.startsWith('PST'))).toBe(false);
  });

  it('prints GST and PST as their own lines on a Canadian sale, under $100 too (CP-14 A1)', () => {
    const bc = {
      ...signed, subtotal_cents: 2750, tax_cents: 331, total_cents: 3081, change_cents: 0,
      tenders: [{ method: 'cash', amount_cents: 3081, reference: null }],
      tax_lines: [
        { name: 'GOODS_TAX', rate: '0.050000', label: 'GST', cents: 138 },
        { name: 'PROVINCIAL_SALES_TAX', rate: '0.070000', label: 'PST', cents: 193 },
      ],
    } as unknown as OfflineSignedSale;
    const lines = receiptLines(bc, null);
    expect(lines).toContain('GST 5%: $1.38');
    expect(lines).toContain('PST 7%: $1.93');
    expect(lines.some((l) => l.startsWith('Tax '))).toBe(false);
    expect(lines.indexOf('GST 5%: $1.38')).toBeLessThan(lines.indexOf('Total $30.81'));
  });

  it('turns a part rate into the printed percent without trailing zeros', () => {
    expect(ratePercent('0.050000')).toBe('5');
    expect(ratePercent('0.070000')).toBe('7');
    expect(ratePercent('0.130000')).toBe('13');
    expect(ratePercent('0.052500')).toBe('5.25');
    expect(ratePercent('0.088750')).toBe('8.875');
  });
});

describe('a lost answer', () => {
  it('is the connection\'s, never the server\'s refusal', () => {
    expect(isLostAnswer(new TypeError('Failed to fetch'), true)).toBe(true);
    expect(isLostAnswer(new Error('anything'), false)).toBe(true);
    expect(isLostAnswer(new Error('the sale needs 8650 cents'), true)).toBe(false);
  });
});
