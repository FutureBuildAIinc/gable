// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The product page's header shows the product's picture at 72, and nothing while it has none. */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './ProductDetail'
import type { GableProductDetail } from './ProductDetail'
import { mountAsync } from '../../test/dom'

const PRODUCT_ID = '66666666-6666-4666-8666-666666666666'

function stubDetail(imageUrl: string | null) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
    product: {
      id: PRODUCT_ID, sku: 'PIC-1', description: 'Picture product', stock_uom: 'PCS',
      base_price_ten_thousandths: 55000, on_hand: '1', allocated: '0', available: '1', revision: 1, image_url: imageUrl,
    },
    content: null, media: [], collateral: [],
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
}

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('product page picture', () => {
  it('draws the picture at 72 in the header', async () => {
    stubDetail('/catalog-illustrations/a.svg')
    const el = await mountAsync<GableProductDetail>('gable-product-detail', { routeId: PRODUCT_ID })
    const thumb = el.querySelector('gable-product-thumb') as HTMLElement & { size: number }
    expect(thumb).not.toBeNull()
    expect(thumb.size).toBe(72)
    expect(thumb.querySelector('img')?.getAttribute('src')).toBe('/catalog-illustrations/a.svg')
  })

  it('draws no thumb host for a path that is not a same origin path', async () => {
    stubDetail('https://example.com/a.svg')
    const el = await mountAsync<GableProductDetail>('gable-product-detail', { routeId: PRODUCT_ID })
    expect(el.querySelector('gable-product-thumb')).toBeNull()
  })

  it('draws nothing for a product with no picture', async () => {
    stubDetail(null)
    const el = await mountAsync<GableProductDetail>('gable-product-detail', { routeId: PRODUCT_ID })
    expect(el.querySelector('gable-product-thumb')).toBeNull()
  })
})
