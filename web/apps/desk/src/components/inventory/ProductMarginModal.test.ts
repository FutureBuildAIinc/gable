// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The pricing controls modal writes the margins on the revision it was handed (If-Match), shows the
 * suggested price from integer ten thousandths, and on a 409 stale_revision reloads through its
 * parent with a toast instead of showing a raw error.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './ProductMarginModal'
import type { GableProductMarginModal } from './ProductMarginModal'
import type { Product } from '../../types/product'
import { ToastService } from '../../lib/toast-service'
import { mount, update, flush, q, text, jsonResponse } from '../../test/dom'

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
  fetchMock = vi.fn(async () => jsonResponse({ ...PRODUCT, revision: 4 }))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

async function open(): Promise<GableProductMarginModal> {
  const el = await mount<GableProductMarginModal>('gable-product-margin-modal', { isOpen: true, product: PRODUCT })
  await flush()
  await update(el, {})
  return el
}

async function save(el: GableProductMarginModal) {
  const button = Array.from(el.querySelectorAll('button')).find((b) => (b.textContent ?? '').includes('Save Controls'))
  if (!button) throw new Error('no Save Controls button')
  button.click()
  await flush()
  await update(el, {})
}

describe('ProductMarginModal', () => {
  it('shows cost, base price and the suggested price from the ten thousandths fields', async () => {
    const el = await open()
    const shown = text(el)
    expect(shown).toContain('$3.00') // cost 30000
    expect(shown).toContain('$4.25') // base price 42500
    // 3.00 / (1 - 0.30) = 4.2857 at scale 4
    expect(shown).toContain('$4.2857')
  })

  it('writes PATCH /margins with the loaded revision as a quoted If-Match', async () => {
    const el = await open()
    const onSuccess = vi.fn()
    el.addEventListener('success', onSuccess)

    const margin = q<HTMLInputElement>(el, 'input[max="99"]')
    margin.value = '35'
    margin.dispatchEvent(new Event('input', { bubbles: true }))
    await update(el, {})
    await save(el)

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(`/api/v1/products/${PRODUCT.id}/margins`)
    expect(init.method).toBe('PATCH')
    expect((init.headers as Headers).get('If-Match')).toBe('"3"')
    expect(JSON.parse(String(init.body))).toEqual({ target_margin: 35, commission_rate: 5 })
    expect(onSuccess).toHaveBeenCalledTimes(1)
  })

  it('on a 409 stale_revision toasts the server message and asks its parent to reload', async () => {
    const shown: string[] = []
    vi.spyOn(ToastService, 'show').mockImplementation((message: string) => { shown.push(message) })
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { code: 'stale_revision', message: 'the product changed since it was loaded', details: [] }, meta: { request_id: 'r' } }, 409),
    )
    const el = await open()
    const onSuccess = vi.fn()
    const onClose = vi.fn()
    el.addEventListener('success', onSuccess)
    el.addEventListener('close', onClose)

    await save(el)

    expect(shown.some((m) => m.includes('the product changed since it was loaded'))).toBe(true)
    expect(onSuccess).toHaveBeenCalledTimes(1)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('shows a validation refusal inline with its field details and stays open', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { code: 'validation_failed', message: 'one or more fields failed validation', details: [{ field: 'target_margin', message: 'must be below 100' }] }, meta: { request_id: 'r' } }, 400),
    )
    const el = await open()
    const onClose = vi.fn()
    el.addEventListener('close', onClose)

    await save(el)

    expect(text(q(el, '[role="alert"]'))).toContain('target_margin: must be below 100')
    expect(onClose).not.toHaveBeenCalled()
  })
})
