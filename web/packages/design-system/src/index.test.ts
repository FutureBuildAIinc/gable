// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The package's export surface: importing it registers the shared custom
 * elements (the desk's layouts and the front door's header both rely on the
 * side effect), and cn keeps its Tailwind-aware merge behaviour.
 */
import { describe, it, expect } from 'vitest'
import { render } from 'lit'
import { cn, GableBrandLogo, googleGlyph, microsoftGlyph } from './index'

describe('the design-system export surface', () => {
  it('registers the shared custom elements under the gable- prefix', () => {
    expect(customElements.get('gable-brand-logo')).toBeDefined()
    expect(GableBrandLogo).toBeDefined()
  })
})

describe('cn', () => {
  it('joins conditional class names', () => {
    expect(cn('flex', 'items-center')).toBe('flex items-center')
  })

  it('drops falsy inputs', () => {
    expect(cn('flex', false, null, undefined, '')).toBe('flex')
  })

  it('resolves Tailwind conflicts last-wins', () => {
    expect(cn('h-6', 'h-8')).toBe('h-8')
  })
})

describe('provider glyphs', () => {
  it('draws google as a currentColor stroke svg and microsoft as four filled currentColor squares with a gap', () => {
    const gWrap = document.createElement('div');
    render(googleGlyph(18), gWrap);
    const gSvg = gWrap.querySelector('svg');
    expect(gSvg).not.toBeNull();
    expect(gSvg?.getAttribute('stroke')).toBe('currentColor');
    expect(gSvg?.getAttribute('width')).toBe('18');
    expect(gSvg?.getAttribute('aria-hidden')).toBe('true');

    const mWrap = document.createElement('div');
    render(microsoftGlyph(18), mWrap);
    const mSvg = mWrap.querySelector('svg');
    expect(mSvg).not.toBeNull();
    expect(mSvg?.getAttribute('fill')).toBe('currentColor');
    expect(mSvg?.getAttribute('stroke')).toBe('none');
    expect(mSvg?.getAttribute('width')).toBe('18');
    expect(mSvg?.getAttribute('aria-hidden')).toBe('true');
    const rects = mSvg?.querySelectorAll('rect') ?? [];
    expect(rects).toHaveLength(4);
    expect(rects[0]?.getAttribute('width')).toBe('9');
    expect(rects[1]?.getAttribute('x')).toBe('13');
  });
});
