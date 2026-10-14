// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** A quote line shows its product's picture beside its name while the product has one. */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './QuoteBuilder'
import type { GableQuoteBuilder } from './QuoteBuilder'
import type { ParsedItem } from '../types/parsing'
import { mount, update, flush, q, jsonResponse } from '../test/dom'

const WITH = '33333333-3333-4333-8333-333333333333'
const WITHOUT = '44444444-4444-4444-8444-444444444444'

function parsed(id: string, sku: string): ParsedItem {
  return {
    raw_text: sku,
    matched_product: { product_id: id, sku, description: sku, uom: 'PCS', base_price: 1 },
    quantity: 1, uom: 'PCS', confidence: 0.9, is_special_order: false, alternatives: [],
  }
}

function productRow(id: string, sku: string, imageUrl: string | null) {
  return { id, sku, description: sku, stock_uom: 'PCS', base_price_ten_thousandths: 10000, image_url: imageUrl, units: [] }
}

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const path = new URL(url, 'http://localhost').pathname
    if (path === '/api/v1/products') {
      return jsonResponse({
        items: [productRow(WITH, 'WITH-PIC', '/catalog-illustrations/a.svg'), productRow(WITHOUT, 'NO-PIC', null)],
        next_cursor: null,
      })
    }
    return jsonResponse({ items: [], next_cursor: null })
  }))
})
afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('quote line pictures', () => {
  it('draws a thumb for a line whose product has a picture and none for one without', async () => {
    const el = await mount<GableQuoteBuilder>('gable-quote-builder')
    await flush()
    await update(el, {})
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', {
      detail: [parsed(WITH, 'WITH-PIC'), parsed(WITHOUT, 'NO-PIC')], bubbles: true,
    }))
    await flush()
    await update(el, {})
    const rows = Array.from(el.querySelectorAll('[data-testid="quote-line"]'))
    expect(rows.length).toBe(2)
    const [first, second] = rows.map((r) => r.querySelector('gable-product-thumb') as HTMLElement | null)
    expect(first?.querySelector('img')?.getAttribute('src')).toBe('/catalog-illustrations/a.svg')
    expect(second?.querySelector('img')).toBeNull()
    // the line without a picture keeps the slot and draws the neutral mark on the same plate
    expect(second?.querySelector('[data-testid="product-thumb-none"]')).not.toBeNull()
    expect(second?.querySelector<HTMLElement>('[data-testid="product-thumb"]')?.style.width).toBe('40px')
  })

  it('draws no thumb element at all while no product has a picture', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const path = new URL(url, 'http://localhost').pathname
      if (path === '/api/v1/products') return jsonResponse({ items: [productRow(WITHOUT, 'NO-PIC', null)], next_cursor: null })
      return jsonResponse({ items: [], next_cursor: null })
    }))
    const el = await mount<GableQuoteBuilder>('gable-quote-builder')
    await flush()
    await update(el, {})
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [parsed(WITHOUT, 'NO-PIC')], bubbles: true }))
    await flush()
    await update(el, {})
    expect(el.querySelectorAll('[data-testid="quote-line"]').length).toBe(1)
    expect(el.querySelector('gable-product-thumb')).toBeNull()
  })
})
