// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The product page follows the yard switcher: the product detail carries the
 * current yard's stock figures (the Overview's On Hand, Allocated and
 * Available), so a yard change must load it again.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './ProductDetail'
import type { GableProductDetail } from './ProductDetail'
import { mountAsync, update, flush, text } from '../../test/dom'

const PRODUCT_ID = '55555555-5555-4555-8555-555555555555'

let onHand = '133.0000'
let fetchMock: ReturnType<typeof vi.fn>

function detailCalls(): number {
  return fetchMock.mock.calls.filter((c) => String(c[0]).includes(`/api/v1/products/${PRODUCT_ID}/detail`)).length
}

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

describe('the product page follows the yard', () => {
  beforeEach(() => {
    onHand = '133.0000'
    fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes(`/api/v1/products/${PRODUCT_ID}/detail`)) {
        return json({
          product: {
            id: PRODUCT_ID,
            sku: 'LUM-248-PREM',
            description: '2x4x8 SPF Premium',
            stock_uom: 'PCS',
            base_price_ten_thousandths: 55000,
            on_hand: onHand,
            allocated: '0.0000',
            available: onHand,
            revision: 1,
          },
          content: { short_description: 'saved text' },
          media: [],
          collateral: [],
        })
      }
      return json({})
    })
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
  })

  it('loads the product again when the yard changes', async () => {
    const el = await mountAsync<GableProductDetail>('gable-product-detail', { routeId: PRODUCT_ID })
    const before = detailCalls()
    expect(before).toBe(1)

    onHand = '14.0000'
    window.dispatchEvent(new CustomEvent('gable:branch-changed', { detail: { branchId: 'lk' } }))
    await flush()
    await update(el, {})

    expect(detailCalls()).toBe(before + 1)
    expect(text(el)).toContain('14')
  })

  it('keeps the open tab and its content on a yard change', async () => {
    const el = await mountAsync<GableProductDetail>('gable-product-detail', { routeId: PRODUCT_ID })
    const page = el as unknown as { activeTab: string; detail: { content: unknown } }
    page.activeTab = 'content'
    await update(el, {})
    const tab = el.querySelector('gable-product-content-tab')
    expect(tab).not.toBeNull()
    const contentBefore = page.detail.content

    onHand = '14.0000'
    window.dispatchEvent(new CustomEvent('gable:branch-changed', { detail: { branchId: 'lk' } }))
    await flush()
    await update(el, {})

    expect(el.querySelector('gable-product-content-tab')).toBe(tab)
    expect(page.detail.content).toBe(contentBefore)
  })

  it('stops listening once it leaves the page', async () => {
    const el = await mountAsync<GableProductDetail>('gable-product-detail', { routeId: PRODUCT_ID })
    el.remove()
    const before = detailCalls()

    window.dispatchEvent(new CustomEvent('gable:branch-changed', { detail: { branchId: 'lk' } }))
    await flush()

    expect(detailCalls()).toBe(before)
  })
})
