// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The package's export surface: importing it registers the shared custom
 * elements (the desk's layouts and the front door's header both rely on the
 * side effect), and cn keeps its Tailwind-aware merge behaviour.
 */
import { describe, it, expect } from 'vitest'
import { cn, GableBrandLogo } from './index'

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
