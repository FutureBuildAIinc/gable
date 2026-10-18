// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The desk standalone (no work shell frame): exactly as today, the desk's own
 * header band renders with the brand, the search and the user chip. The framed
 * half lives in app-shell.framed.test.ts.
 */
import { describe, expect, it } from 'vitest';
import { mount, q } from '../../test/dom.ts';
import type { GableAppShell } from './app-shell.ts';
import './app-shell.ts';

describe('gable-app-shell standalone', () => {
  it('renders its own header band', async () => {
    const el = await mount<GableAppShell>('gable-app-shell');
    const header = q<HTMLElement>(el, 'header');
    expect(header.querySelector('gable-brand-logo')).not.toBeNull();
    const strip = q<HTMLElement>(el, 'nav[aria-label="Open modules"]');
    expect(strip.className).toContain('top-16');
  });
});
