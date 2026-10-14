// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The thumb draws the picture as an img, nothing without a path, and the no picture mark on an error. */
import { describe, it, expect } from 'vitest'
import './ProductThumb'
import type { GableProductThumb } from './ProductThumb'
import { mount, update } from '../test/dom'

describe('gable-product-thumb', () => {
  it('draws the picture as an img at the given size', async () => {
    const el = await mount<GableProductThumb>('gable-product-thumb', { src: '/catalog-illustrations/a.svg', size: 24 })
    const img = el.querySelector('img')!
    expect(img.getAttribute('src')).toBe('/catalog-illustrations/a.svg')
    expect(img.getAttribute('alt')).toBe('')
    const box = el.querySelector<HTMLElement>('[data-testid="product-thumb"]')!
    expect(box.style.width).toBe('24px')
    expect(box.style.height).toBe('24px')
  })

  it('draws nothing without a path', async () => {
    for (const src of [null, undefined, '']) {
      const el = await mount<GableProductThumb>('gable-product-thumb', { src })
      expect(el.querySelector('img')).toBeNull()
      expect(el.textContent?.trim()).toBe('')
      expect(el.querySelector('[data-testid="product-thumb"]')).toBeNull()
    }
  })

  it('keeps an empty box of its size when it is told to reserve the room', async () => {
    const el = await mount<GableProductThumb>('gable-product-thumb', { src: null, size: 72, reserve: true })
    expect(el.querySelector('img')).toBeNull()
    expect(el.querySelector<HTMLElement>('span')!.style.width).toBe('72px')
  })

  it('draws the no picture mark when the image fails to load, and the img again for a new path', async () => {
    const el = await mount<GableProductThumb>('gable-product-thumb', { src: '/missing.svg', size: 40 })
    el.querySelector('img')!.dispatchEvent(new Event('error'))
    await update(el, {})
    expect(el.querySelector('img')).toBeNull()
    expect(el.querySelector('[data-testid="product-thumb-none"]')).not.toBeNull()
    await update(el, { src: '/other.svg' })
    expect(el.querySelector('img')!.getAttribute('src')).toBe('/other.svg')
  })
})
