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
  it('draws each mark as a currentColor svg in the icon idiom', () => {
    for (const glyph of [googleGlyph(18), microsoftGlyph(18)]) {
      const svg = document.createElement('div');
      render(glyph, svg);
      const el = svg.querySelector('svg');
      expect(el).not.toBeNull();
      expect(el?.getAttribute('stroke')).toBe('currentColor');
      expect(el?.getAttribute('width')).toBe('18');
      expect(el?.getAttribute('aria-hidden')).toBe('true');
    }
  });
});
