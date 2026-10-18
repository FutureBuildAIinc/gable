// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The yard layout standalone: exactly as today, its own header band renders.
 * The framed half lives in yard-layout.framed.test.ts.
 */
import { describe, expect, it } from 'vitest';
import { mount, q } from '../../test/dom.ts';
import type { GableYardLayout } from './yard-layout.ts';
import './yard-layout.ts';

describe('gable-yard-layout standalone', () => {
  it('renders its own header band', async () => {
    const el = await mount<GableYardLayout>('gable-yard-layout');
    expect(q<HTMLElement>(el, 'header').textContent).toContain('YARD');
  });
});
