// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import type { ExceptionResolution } from '../../services/OfflineAdminService';
import { reasonWords, resolutionsFor } from './offlineExceptionRules';

const ways = (actions: ExceptionResolution[]) => resolutionsFor(actions).map((c) => c.value);

describe('the ways a manager may settle an offline exception', () => {
  it('offers exactly the actions the server lists, in its order', () => {
    expect(ways(['restate', 'write_off'])).toEqual(['restate', 'write_off']);
    expect(ways(['replaced_sale_settled' as never, 'post_separate', 'write_off'])).toEqual(['post_separate', 'write_off']); // an action it has no words for is not offered
    expect(ways(['review'])).toEqual(['review']);
  });

  it('names a reason it does not know as Other reason, never as the raw code', () => {
    expect(reasonWords('something_new')).toBe('Other reason (something_new)');
    expect(reasonWords('unknown_tail')).toContain('re-enrolled');
  });

  // Every reason code the server can store on an exception row, with the place it is set (core/internal/pos/).
  const SERVER_CODES: Record<string, string> = {
    account_unverified: 'offline_verify.go:343',
    bad_signature: 'offline_sync.go:355',
    broken_chain: 'offline_sync.go:387',
    cap_exceeded: 'offline_verify.go:317',
    cashier_mismatch: 'offline_verify.go:199',
    clock_unverified: 'offline_verify.go:211',
    day_closed: 'offline_verify.go:216',
    device_revoked: 'offline_verify.go:149',
    id_unverified: 'offline_verify.go:335',
    insufficient_stock: 'offline_sync.go:948',
    late_after_gap: 'offline_sync.go:377',
    one_account_leg: 'offline_verify.go:237',
    out_of_wall: 'offline_sync.go:974',
    payer_details_required: 'offline_sync.go:957',
    period_closed: 'offline_sync.go:950',
    price_unverified: 'offline_verify.go:166',
    queue_mark_unverified: 'offline_verify.go:383',
    replay_mismatch: 'offline_sync.go:322',
    replaced_sale_mismatch: 'offline_sync.go:662',
    replaced_sale_settled: 'offline_sync.go:666',
    sequence_gap: 'offline_sync.go:48',
    sequence_reused: 'offline_sync.go:362',
    session_closed: 'offline_sync.go:949',
    snapshot_stale: 'offline_verify.go:203',
    snapshot_unknown: 'offline_verify.go:181',
    sold_past_warning: 'offline_sync.go:49',
    stock_unit_changed: 'offline_sync.go:951',
    stock_unverified: 'offline_verify.go:530',
    tax_flag_unverified: 'offline_verify.go:524',
    tax_unverified: 'offline_verify.go:277',
    tender_mismatch: 'offline_verify.go:230',
    tender_not_offline: 'offline_verify.go:219',
    unknown_tail: 'offline_exceptions.go:44',
    walk_in_account: 'offline_sync.go:952',
  };

  it.each(Object.entries(SERVER_CODES))('has a clerk label for %s (%s)', (code) => {
    const words = reasonWords(code);
    expect(words).not.toMatch(/^Other reason/);
    expect(words).not.toContain('_');
    expect(words).toMatch(/^[A-Z]/);
  });

  it('asks for a reason on every act, a retry included, as the server does', () => {
    expect(resolutionsFor(['retry', 'write_off']).map((c) => c.needsReason)).toEqual([true, true]);
  });
});
