// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect, afterEach } from 'vitest'
import { channelsToHex, themeHex, cssVarColor } from './theme-colors.ts'

describe('theme colours', () => {
  afterEach(() => document.documentElement.removeAttribute('style'))

  it('turns channels into hex', () => {
    expect(channelsToHex('4 108 67')).toBe('#046c43')
    expect(channelsToHex('0 255 163')).toBe('#00ffa3')
    expect(channelsToHex('nope')).toBeNull()
  })

  it('reads a variable at call time and falls back when it is unset', () => {
    expect(themeHex('plate', '#123456')).toBe('#123456')
    document.documentElement.style.setProperty('--gb-plate', '254 252 248')
    expect(themeHex('plate')).toBe('#fefcf8')
  })

  it('builds a var() colour for a style attribute', () => {
    expect(cssVarColor('gable-green')).toBe('rgb(var(--gb-gable-green))')
    expect(cssVarColor('gable-green', 0.1)).toBe('rgb(var(--gb-gable-green) / 0.1)')
  })
})
