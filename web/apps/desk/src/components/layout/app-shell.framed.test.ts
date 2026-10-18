// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The desk framed by the work shell: the desk's own header band is gone (one
 * header, the shell's), and the tab strip pins to the top in its place.
 * framedByShell is mocked framed for this file; the standalone half lives in
 * app-shell.standalone.test.ts.
 */
import { describe, expect, it, vi } from 'vitest';

vi.mock('@gable/design-system', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@gable/design-system')>();
  return { ...actual, framedByShell: () => true };
});

import { mount, q } from '../../test/dom.ts';
import type { GableAppShell } from './app-shell.ts';
import './app-shell.ts';

describe('gable-app-shell framed', () => {
  it('renders no header band of its own', async () => {
    const el = await mount<GableAppShell>('gable-app-shell');
    expect(el.querySelector('header')).toBeNull();
    // The workspace tab strip stays, pinned to the top in the header's place.
    const strip = q<HTMLElement>(el, 'nav[aria-label="Open modules"]');
    expect(strip.className).toContain('top-0');
    expect(strip.className).not.toContain('top-16');
  });
});
