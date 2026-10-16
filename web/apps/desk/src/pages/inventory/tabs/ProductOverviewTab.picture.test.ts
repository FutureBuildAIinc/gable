// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The Overview image panel: a PIM image first, then the product's picture, then "No image". */
import { describe, it, expect, afterEach } from 'vitest'
import './ProductOverviewTab'
import type { GableProductOverviewTab } from './ProductOverviewTab'
import { mount, text } from '../../../test/dom'

function detail(imageUrl: string | null, media: unknown[] = []) {
  return {
    product: {
      id: 'p1', sku: 'OV-1', description: 'Overview product', stock_uom: 'PCS', base_price_ten_thousandths: 1000,
      average_unit_cost_ten_thousandths: 500, target_margin: 0, commission_rate: 0, vendor: null,
      on_hand: '1', allocated: '0', available: '1', reorder_point: '0', reorder_qty: '0', image_url: imageUrl,
    },
    content: null, media, collateral: [],
  }
}

afterEach(() => { document.body.innerHTML = '' })

describe('overview image panel', () => {
  it('shows the product picture, large, when there is no PIM image', async () => {
    const el = await mount<GableProductOverviewTab>('gable-product-overview-tab', { detail: detail('/catalog-illustrations/a.svg') as never })
    const img = el.querySelector('img[data-testid="overview-picture"]')
    expect(img?.getAttribute('src')).toBe('/catalog-illustrations/a.svg')
    expect(text(el)).not.toContain('No image')
  })

  it('prefers the PIM image to the product picture', async () => {
    const media = [{ id: 'm', url: '/uploads/pim.png', is_primary: true, alt_text: '' }]
    const el = await mount<GableProductOverviewTab>('gable-product-overview-tab', { detail: detail('/catalog-illustrations/a.svg', media) as never })
    expect(el.querySelector('img[data-testid="overview-picture"]')).toBeNull()
    expect(el.querySelector('img')?.getAttribute('src')).toBe('/uploads/pim.png')
  })

  it('says No image only when there is neither', async () => {
    const el = await mount<GableProductOverviewTab>('gable-product-overview-tab', { detail: detail(null) as never })
    expect(text(el)).toContain('No image')
    expect(el.querySelector('img')).toBeNull()
  })
})
