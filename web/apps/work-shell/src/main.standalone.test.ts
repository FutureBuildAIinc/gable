// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The work shell's boot, standalone: the orphaned frame guard reads false at
 * the top level and the host mounts. The framed half lives in
 * main.orphaned.test.ts (the mock is per file, so the two never mix).
 */
import { describe, expect, it } from 'vitest';

describe('work-shell boot, standalone', () => {
  it('mounts the host', async () => {
    const root = document.createElement('div');
    root.id = 'root';
    document.body.appendChild(root);
    await import('./main.ts');
    expect(document.querySelector('gable-work-shell')).not.toBeNull();
  });
});
