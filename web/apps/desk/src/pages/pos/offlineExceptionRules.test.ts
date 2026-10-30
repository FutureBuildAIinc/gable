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

  it('names a reason it does not know in plain words', () => {
    expect(reasonWords('something_new')).toBe('something new');
    expect(reasonWords('unknown_tail')).toContain('re-enrolled');
  });

  it('asks for a reason on every act, a retry included, as the server does', () => {
    expect(resolutionsFor(['retry', 'write_off']).map((c) => c.needsReason)).toEqual([true, true]);
  });
});
