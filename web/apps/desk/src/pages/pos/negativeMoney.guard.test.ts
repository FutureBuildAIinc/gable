// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { formatCents } from '../../lib/utils';

/** One negative money format everywhere on the counter: "-$27.80", never "$-27.80". */
describe('money on the counter pages', () => {
  it('formats a negative as -$27.80 and groups thousands', () => {
    expect(formatCents(-2780)).toBe('-$27.80');
    expect(formatCents(-412639)).toBe('-$4,126.39');
  });

  it('keeps no private cents formatter in a counter page', () => {
    for (const f of readdirSync(__dirname).filter((n) => n.endsWith('.ts') && !n.endsWith('.test.ts'))) {
      const src = readFileSync(join(__dirname, f), 'utf8');
      expect(src, f).not.toMatch(/`\$\$\{\([^)]*\/ 100\)\.toFixed\(2\)\}`/);
      expect(src, f).not.toMatch(/\$\$\{\(\w+(\.\w+)* \/ 100\)\.toFixed\(2\)\}/);
    }
  });
});
