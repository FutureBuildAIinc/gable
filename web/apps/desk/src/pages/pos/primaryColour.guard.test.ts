// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors


import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

/**
 * One primary colour: every primary action on the counter's dialogs is the
 * design system green in both themes, through the till accent token, which is
 * the Gable green token itself, with the deep space token for its text.
 */
const dir = __dirname;
const sources = readdirSync(dir).filter((f) => f.endsWith('.ts') && !f.endsWith('.test.ts'));

describe('the primary action colour on the counter dialogs', () => {
  it('draws white plate text on no till accent fill', () => {
    for (const f of sources) {
      const src = readFileSync(join(dir, f), 'utf8');
      expect(src, f).not.toMatch(/background:rgb\(var\(--gb-till-accent\)\);[^"`]*?color:rgb\(var\(--gb-plate\)\)/);
    }
  });

  it('hard codes no purple on a button', () => {
    for (const f of sources) {
      expect(readFileSync(join(dir, f), 'utf8'), f).not.toMatch(/bg-(purple|violet|indigo)-\d+|#8957e5/i);
    }
  });
});
