// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect, afterEach } from 'vitest'
import { channelsToHex, themeHex, cssVarColor, brandOnCard, brandLabelOnCard, brandLabelOnTint, inkOrPaperOn } from './theme-colors.ts'

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

  describe('brandOnCard', () => {
    afterEach(() => document.documentElement.removeAttribute('data-theme'))

    it('keeps the brand colour in dark', () => {
      expect(brandOnCard('#00FFA3')).toBe('#00FFA3')
    })

    it('gives a brand colour that does not read on the light card the deep green', () => {
      document.documentElement.dataset.theme = 'light'
      document.documentElement.style.setProperty('--gb-slate-steel', '248 243 236')
      document.documentElement.style.setProperty('--gb-gable-green', '4 108 67')
      expect(brandOnCard('#00FFA3')).toBe('#046c43')
    })

    it('keeps a brand colour that reads on the light card', () => {
      document.documentElement.dataset.theme = 'light'
      document.documentElement.style.setProperty('--gb-slate-steel', '248 243 236')
      expect(brandOnCard('#1b50a7')).toBe('#1b50a7')
    })
  })

  describe('brandLabelOnCard (a label is text: 4.5:1)', () => {
    afterEach(() => document.documentElement.removeAttribute('data-theme'))

    function light() {
      document.documentElement.dataset.theme = 'light'
      document.documentElement.style.setProperty('--gb-slate-steel', '248 243 236')
      document.documentElement.style.setProperty('--gb-gable-green', '4 108 67')
    }

    it('keeps the brand colour in dark', () => {
      expect(brandLabelOnCard('#00FFA3')).toBe('#00FFA3')
    })

    it('a brand between 3:1 and 4.5:1 serves a bar or a fill but not a label', () => {
      light()
      // #8a8a8a is about 3.2:1 on the card: a control, not text
      expect(brandOnCard('#8a8a8a')).toBe('#8a8a8a')
      expect(brandLabelOnCard('#8a8a8a')).toBe('#046c43')
    })

    it('keeps a brand that reads as text', () => {
      light()
      expect(brandLabelOnCard('#1b50a7')).toBe('#1b50a7')
    })
  })

  describe('brandLabelOnTint (a label on the brand tinted row: 4.5:1 where it is drawn)', () => {
    afterEach(() => document.documentElement.removeAttribute('data-theme'))

    function light() {
      document.documentElement.dataset.theme = 'light'
      document.documentElement.style.setProperty('--gb-slate-steel', '248 243 236')
      document.documentElement.style.setProperty('--gb-portal-panel', '248 243 236')
      document.documentElement.style.setProperty('--gb-gable-green', '4 108 67')
    }

    it('gives a brand just over 4.5:1 on the card the deep green, since it sits on the tint', () => {
      light()
      // #736f6b is 4.51:1 on the bare card but 4.09:1 on the row it is drawn on
      expect(brandLabelOnCard('#736f6b')).toBe('#736f6b')
      expect(brandLabelOnTint('#736f6b', 0x15 / 255)).toBe('#046c43')
    })

    it('keeps a dark brand and leaves dark alone', () => {
      light()
      expect(brandLabelOnTint('#1b2a4a', 0x15 / 255)).toBe('#1b2a4a')
      document.documentElement.removeAttribute('data-theme')
      expect(brandLabelOnTint('#736f6b', 0x15 / 255)).toBe('#736f6b')
    })
  })

  describe('inkOrPaperOn (a label on a tenant brand fill)', () => {
    afterEach(() => document.documentElement.removeAttribute('data-theme'))

    it('is black in dark, as it was', () => {
      expect(inkOrPaperOn('#8a8a8a')).toBe('rgb(var(--gb-black))')
    })

    it('picks ink or paper by contrast in light', () => {
      document.documentElement.dataset.theme = 'light'
      document.documentElement.style.setProperty('--gb-black', '252 250 246')
      document.documentElement.style.setProperty('--gb-white', '34 31 29')
      // paper on #8a8a8a is 3.31:1; ink on it is better
      expect(inkOrPaperOn('#8a8a8a')).toBe('rgb(var(--gb-white))')
      // paper on a deep green is the better label
      expect(inkOrPaperOn('#046c43')).toBe('rgb(var(--gb-black))')
    })
  })
})
