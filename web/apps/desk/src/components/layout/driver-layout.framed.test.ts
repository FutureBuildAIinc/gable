// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The driver layout framed by the work shell: its own header band is gone
 * (one header, the shell's). The standalone half lives in
 * driver-layout.standalone.test.ts.
 */
import { describe, expect, it, vi } from 'vitest';

vi.mock('@gable/design-system', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@gable/design-system')>();
  return { ...actual, framedByShell: () => true };
});

import { mount } from '../../test/dom.ts';
import type { GableDriverLayout } from './driver-layout.ts';
import './driver-layout.ts';

describe('gable-driver-layout framed', () => {
  it('renders no header band of its own', async () => {
    const el = await mount<GableDriverLayout>('gable-driver-layout');
    expect(el.querySelector('header')).toBeNull();
  });
});
