// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The yard layout framed by the work shell: its own header band is gone (one
 * header, the shell's); its bottom tab nav stays (that is the app's own
 * navigation, not chrome). The standalone half lives in
 * yard-layout.standalone.test.ts.
 */
import { describe, expect, it, vi } from 'vitest';

vi.mock('@gable/design-system', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@gable/design-system')>();
  return { ...actual, framedByShell: () => true };
});

import { mount } from '../../test/dom.ts';
import type { GableYardLayout } from './yard-layout.ts';
import './yard-layout.ts';

describe('gable-yard-layout framed', () => {
  it('renders no header band of its own, and keeps its tab nav', async () => {
    const el = await mount<GableYardLayout>('gable-yard-layout');
    expect(el.querySelector('header')).toBeNull();
    expect(el.querySelector('nav')).not.toBeNull();
  });
});
