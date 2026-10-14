// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The product's Stock & Locations tab follows the yard switcher: the stock
 * levels are the current yard's (the fetch client stamps the yard on every
 * request), so a yard change must load them again, as the other yard aware
 * pages do through `onBranchChanged`.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './ProductStockTab'
import type { GableProductStockTab } from './ProductStockTab'
import { mountAsync, update, flush, text } from '../../../test/dom'

const PRODUCT_ID = '44444444-4444-4444-8444-444444444444'

/** One stock row as `GET /api/v1/inventory` returns it. */
function level(location: string, quantity: string) {
  return {
    id: `inv-${location}`,
    product_id: PRODUCT_ID,
    location_id: `loc-${location}`,
    location_name: location,
    quantity,
    allocated: '0.0000',
    available: quantity,
    uom: 'PCS',
    updated_at: '2026-01-01T00:00:00Z',
  }
}

/** The stock each yard's request answers; the stub plays the server's yard filter. */
const stockByYard: Record<string, ReturnType<typeof level>[]> = {
  'KEL-MAIN': [level('KEL-MAIN', '133.0000')],
  LK: [level('LK', '14.0000')],
}
let yard = 'KEL-MAIN'
let fetchMock: ReturnType<typeof vi.fn>

function inventoryCalls(): number {
  return fetchMock.mock.calls.filter((c) => String(c[0]).includes('/api/v1/inventory?')).length
}

describe('the product stock tab follows the yard', () => {
  beforeEach(() => {
    yard = 'KEL-MAIN'
    fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/api/v1/inventory?')) {
        return new Response(JSON.stringify({ items: stockByYard[yard], next_cursor: null }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    })
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
  })

  it('loads the new yard stock when the yard changes', async () => {
    const el = await mountAsync<GableProductStockTab>('gable-product-stock-tab', { productId: PRODUCT_ID })
    expect(text(el)).toContain('KEL-MAIN')
    const before = inventoryCalls()

    yard = 'LK'
    window.dispatchEvent(new CustomEvent('gable:branch-changed', { detail: { branchId: 'lk' } }))
    await flush()
    await update(el, {})

    expect(inventoryCalls()).toBe(before + 1)
    expect(text(el)).toContain('LK')
    expect(text(el)).not.toContain('KEL-MAIN')
  })

  it('stops listening once it leaves the page', async () => {
    const el = await mountAsync<GableProductStockTab>('gable-product-stock-tab', { productId: PRODUCT_ID })
    el.remove()
    const before = inventoryCalls()

    window.dispatchEvent(new CustomEvent('gable:branch-changed', { detail: { branchId: 'lk' } }))
    await flush()

    expect(inventoryCalls()).toBe(before)
  })
})
