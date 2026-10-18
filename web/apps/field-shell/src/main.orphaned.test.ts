// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The field shell's boot as an orphaned frame (framed, no flag, same origin
 * parent): it asks the host to switch role and mounts nothing. The standalone
 * half lives in main.standalone.test.ts.
 */
import { describe, expect, it, vi } from 'vitest';

vi.mock('@gable/design-system', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@gable/design-system')>();
  return { ...actual, orphanedFrame: () => true };
});

describe('field-shell boot, orphaned frame', () => {
  it('mounts nothing', async () => {
    const root = document.createElement('div');
    root.id = 'root';
    document.body.appendChild(root);
    await import('./main.ts');
    expect(document.querySelector('gable-field-shell')).toBeNull();
  });
});
