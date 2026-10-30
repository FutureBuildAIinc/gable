// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import { reasonWords, resolutionsFor } from './offlineExceptions';

const ways = (code: string) => resolutionsFor(code).map((c) => c.value);

describe('the ways a manager may settle an offline exception', () => {
  it('offers only what ADR 0011 section 3.4 gives each reason', () => {
    expect(ways('insufficient_stock')).toEqual(['retry', 'write_off']);
    expect(ways('stock_unit_changed')).toEqual(['restate_stock', 'write_off']); // a retry never helps it
    expect(ways('period_closed')).toEqual(['late_post', 'write_off']);
    expect(ways('price_unverified')).toEqual(['accept', 'write_off']);
    expect(ways('replaced_sale_mismatch')).toEqual(['separate_sale', 'write_off']);
    expect(ways('replaced_sale_settled')).toContain('settled_replaced');
  });

  it('leaves a forged or unverifiable sale only the write off', () => {
    for (const code of ['bad_signature', 'replay_mismatch', 'device_revoked', 'sequence_reused']) {
      expect(ways(code)).toEqual(['write_off']);
    }
  });

  it('falls back to retry and write off for a reason it does not know', () => {
    expect(ways('something_new')).toEqual(['retry', 'write_off']);
    expect(reasonWords('something_new')).toBe('something new');
  });

  it('asks for a reason on every act except a retry', () => {
    expect(resolutionsFor('insufficient_stock').find((c) => c.value === 'retry')?.needsReason).toBe(false);
    expect(resolutionsFor('insufficient_stock').find((c) => c.value === 'write_off')?.needsReason).toBe(true);
  });
});
