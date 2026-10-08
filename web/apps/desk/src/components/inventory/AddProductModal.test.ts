// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The add product modal creates on the converted wire: stock_uom and base_price_ten_thousandths parsed
 * from the typed dollars with string arithmetic, none of the old field names, and the server's refusal
 * shown inline.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './AddProductModal'
import type { GableAddProductModal } from './AddProductModal'
import { mount, update, flush, q, text, jsonResponse } from '../../test/dom'

let fetchMock: ReturnType<typeof vi.fn>
let postBodies: Record<string, unknown>[]

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  postBodies = []
  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.endsWith('/api/v1/vendors')) return jsonResponse([])
    if (url.endsWith('/api/v1/products') && init?.method === 'POST') {
      postBodies.push(JSON.parse(String(init.body)) as Record<string, unknown>)
      return jsonResponse({ id: 'p-9', revision: 1 }, 201)
    }
    throw new Error(`unexpected fetch: ${init?.method ?? 'GET'} ${url}`)
  })
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

async function fill(el: GableAddProductModal, selector: string, value: string) {
  const input = q<HTMLInputElement>(el, selector)
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await update(el, {})
}

async function submit(el: GableAddProductModal) {
  q<HTMLFormElement>(el, 'form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await flush()
  await update(el, {})
}

describe('AddProductModal', () => {
  it('posts stock_uom and the base price in ten thousandths, and announces success', async () => {
    const el = await mount<GableAddProductModal>('gable-add-product-modal', { isOpen: true })
    await flush()
    const onSuccess = vi.fn()
    el.addEventListener('success', onSuccess)

    await fill(el, 'input[placeholder="e.g. 2x4x8-SPF"]', 'SPF-2x4x8')
    await fill(el, 'input[placeholder="e.g. 2x4x8 SPF Premium Stud"]', '2x4x8 SPF Stud')
    await fill(el, 'input[type="number"]', '4.25')
    await submit(el)

    expect(postBodies).toEqual([
      { sku: 'SPF-2x4x8', description: '2x4x8 SPF Stud', stock_uom: 'PCS', base_price_ten_thousandths: 42500 },
    ])
    expect(onSuccess).toHaveBeenCalledTimes(1)
  })

  it('refuses a price with more than four decimals before any request', async () => {
    const el = await mount<GableAddProductModal>('gable-add-product-modal', { isOpen: true })
    await flush()
    await fill(el, 'input[type="number"]', '1.23456')
    await submit(el)

    expect(postBodies).toEqual([])
    expect(text(q(el, '[role="alert"]'))).toContain('four decimal places')
  })

  it('shows the server refusal with its field details and does not announce success', async () => {
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/api/v1/vendors')) return jsonResponse([])
      if (init?.method === 'POST') {
        return jsonResponse({ error: { code: 'validation_failed', message: 'one or more fields failed validation', details: [{ field: 'sku', message: 'already exists' }] }, meta: { request_id: 'r' } }, 400)
      }
      throw new Error('unexpected')
    })
    const el = await mount<GableAddProductModal>('gable-add-product-modal', { isOpen: true })
    await flush()
    const onSuccess = vi.fn()
    el.addEventListener('success', onSuccess)

    await submit(el)

    expect(text(q(el, '[role="alert"]'))).toContain('sku: already exists')
    expect(onSuccess).not.toHaveBeenCalled()
  })
})
