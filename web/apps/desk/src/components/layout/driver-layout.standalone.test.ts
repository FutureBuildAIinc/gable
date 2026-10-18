// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The driver layout standalone: exactly as today, its own header band
 * renders. The framed half lives in driver-layout.framed.test.ts.
 */
import { describe, expect, it } from 'vitest';
import { mount, q } from '../../test/dom.ts';
import type { GableDriverLayout } from './driver-layout.ts';
import './driver-layout.ts';

describe('gable-driver-layout standalone', () => {
  it('renders its own header band', async () => {
    const el = await mount<GableDriverLayout>('gable-driver-layout');
    expect(q<HTMLElement>(el, 'header').textContent).toContain('DRIVER');
  });
});
