// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * One name, one thing: a colour and a shadow that share a key would make
 * Tailwind emit two rules for one class (a tile colour and a tile shadow).
 */
import { describe, it, expect } from 'vitest'
// @ts-expect-error the theme is a plain JS module
import { gableTheme } from '../tailwind/theme.js'

describe('the Tailwind theme names', () => {
  it('shares no key between colours and box shadows', () => {
    const colours = Object.keys(gableTheme.extend.colors)
    const shadows = Object.keys(gableTheme.extend.boxShadow)
    expect(shadows.filter((s) => colours.includes(s))).toEqual([])
  })
})
