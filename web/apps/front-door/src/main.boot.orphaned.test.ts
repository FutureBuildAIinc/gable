// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The front door's boot as an orphaned frame: production nginx serves the
 * DOOR at the origin root, so a framed desk's own sign out (it assigns '/')
 * lands the door inside the shell's frame. The guard (mocked orphaned here)
 * asks the host to switch role and the door mounts nothing. The standalone
 * half lives in main.boot.standalone.test.ts.
 */
import { describe, expect, it, vi } from 'vitest';

vi.mock('@gable/design-system', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@gable/design-system')>();
  return { ...actual, orphanedFrame: () => true };
});

describe('front-door boot, orphaned frame', () => {
  it('mounts nothing', async () => {
    const root = document.createElement('div');
    root.id = 'root';
    document.body.appendChild(root);
    await import('./main.ts');
    expect(document.querySelector('gable-front-door')).toBeNull();
  });
});
