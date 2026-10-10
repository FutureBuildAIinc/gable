// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * A quote line built from a product copies the product's base_price_ten_thousandths as the unit price
 * and its stock_uom as the unit; with a customer, the resolved scale 4 price replaces the base price
 * when it is priced in the same unit.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './LineItemEditor'
import type { GableLineItemEditor } from './LineItemEditor'
import type { Product } from '../../types/product'
import { mount, update, flush, q, jsonResponse } from '../../test/dom'

const PRODUCT: Product = {
  id: '33333333-3333-4333-8333-333333333333',
  sku: 'SPF-2x4x8',
  description: '2x4x8 SPF Stud',
  stock_uom: 'PCS',
  base_price_ten_thousandths: 42500,
  average_unit_cost_ten_thousandths: 30000,
  target_margin: 30,
  commission_rate: 5,
  vendor: null,
  vendor_id: null,
  upc: null,
  weight_lbs: 0,
  length_in: null,
  width_in: null,
  height_in: null,
  stackable: null,
  geometry_source: null,
  lead_time_days: null,
  reorder_point: '0.0000',
  reorder_qty: '0.0000',
  on_hand: '0.0000',
  allocated: '0.0000',
  available: '0.0000',
  revision: 3,
  sale_uom: 'PCS',
  price_uom: 'PCS',
  purchase_uom: 'PCS',
  board_thickness_in: null,
  board_width_in: null,
  board_length_ft: null,
  random_length: false,
  units: [],
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  fetchMock = vi.fn(async () => jsonResponse({}))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

async function pickAndAdd(el: GableLineItemEditor): Promise<CustomEvent['detail']> {
  const search = q<HTMLInputElement>(el, 'input[placeholder="Search SKU or Desc..."]')
  search.value = 'SPF'
  search.dispatchEvent(new Event('input', { bubbles: true }))
  await update(el, {})
  const option = Array.from(el.querySelectorAll('div.cursor-pointer')).find((d) => (d.textContent ?? '').includes('SPF-2x4x8'))
  if (!option) throw new Error('no product option')
  ;(option as HTMLElement).click()
  await flush()
  await update(el, {})
  let detail: CustomEvent['detail'] = null
  el.addEventListener('add-line', (e) => { detail = (e as CustomEvent).detail })
  const add = Array.from(el.querySelectorAll('button')).find((b) => (b.textContent ?? '').includes('Add'))
  if (!add) throw new Error('no Add button')
  add.click()
  return detail
}

describe('LineItemEditor', () => {
  it('copies the base price in ten thousandths and the stock unit when no customer is set', async () => {
    const el = await mount<GableLineItemEditor>('gable-line-item-editor', { products: [PRODUCT] })
    const detail = await pickAndAdd(el)
    expect(fetchMock).not.toHaveBeenCalled()
    expect(detail.unitPriceTenThousandths).toBe(42500)
    expect(detail.uom).toBe('PCS')
    expect(detail.quantity).toBe('1')
  })

  it('uses the resolved scale 4 price when it is priced in the stock unit', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({
      customer_id: 'c-1', product_id: PRODUCT.id, quantity: '1', uom: 'PCS', unit_price_ten_thousandths: 36125,
      price_uom: 'PCS', uom_qty: '1', price_uom_qty: '1', line_total_cents: 361, price_basis: 'tier', details: 'Gold Tier (15%)',
    }))
    const el = await mount<GableLineItemEditor>('gable-line-item-editor', { products: [PRODUCT], customerId: 'c-1' })
    const detail = await pickAndAdd(el)
    const url = new URL(fetchMock.mock.calls[0][0] as string, 'http://localhost')
    expect(url.pathname).toBe('/api/v1/pricing/calculate')
    expect(detail.unitPriceTenThousandths).toBe(36125)
  })

  it('falls back to the base price when the resolved price is in another unit', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({
      customer_id: 'c-1', product_id: PRODUCT.id, quantity: '1', uom: 'PCS', unit_price_ten_thousandths: 3612500,
      price_uom: 'MBF', uom_qty: '1', price_uom_qty: '1', line_total_cents: 361, price_basis: 'tier', details: '',
    }))
    const el = await mount<GableLineItemEditor>('gable-line-item-editor', { products: [PRODUCT], customerId: 'c-1' })
    const detail = await pickAndAdd(el)
    expect(detail.unitPriceTenThousandths).toBe(42500)
  })
})
