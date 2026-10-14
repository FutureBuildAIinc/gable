// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Transfer Stock asks for a source and a destination with the app's own
 * selects. Confirming without a destination must say what is missing in
 * words, not fail silently (the browser's own refusal went away with the
 * native selects).
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './InventoryTransferModal'
import type { GableInventoryTransferModal } from './InventoryTransferModal'
import type { Product } from '../../types/product'
import { mountAsync, update, q, text, jsonResponse } from '../../test/dom.ts'

const PRODUCT = { id: 'p-1', sku: 'SKU-1', description: 'Stud' } as unknown as Product
const LEVELS = { items: [{ location_id: 'loc-a', location_name: 'A bin', quantity: '50000' }], next_cursor: null }
const LOCATIONS = {
  items: [
    { id: 'loc-a', code: 'A', path: 'A', type: 'bin' },
    { id: 'loc-b', code: 'B', path: 'B', type: 'bin' },
  ],
  next_cursor: null,
}

let posts: string[]

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  posts = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (init?.method === 'POST') {
      posts.push(url)
      return jsonResponse({})
    }
    if (url.includes('/api/v1/inventory/levels')) return jsonResponse(LEVELS)
    if (url.includes('/api/v1/locations')) return jsonResponse(LOCATIONS)
    throw new Error(`unexpected fetch: ${init?.method ?? 'GET'} ${url}`)
  }))
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function open(): Promise<GableInventoryTransferModal> {
  return mountAsync<GableInventoryTransferModal>('gable-inventory-transfer-modal', { isOpen: true, product: PRODUCT })
}

describe('gable-inventory-transfer-modal required picks', () => {
  it('the source and destination selects read as required', async () => {
    const el = await open()
    const selects = Array.from(el.querySelectorAll('gable-select'))
    expect(selects.length).toBe(2)
    for (const s of selects) {
      expect(s.querySelector('button')!.getAttribute('aria-required')).toBe('true')
    }
  })

  it('a confirm with no destination says what is missing and sends nothing', async () => {
    const el = await open()
    const qty = q<HTMLInputElement>(el, 'input[type="number"]')
    qty.value = '1'
    qty.dispatchEvent(new Event('input', { bubbles: true }))
    await update(el, {})
    q<HTMLFormElement>(el, 'form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await update(el, {})
    const message = q<HTMLElement>(el, '[data-testid="transfer-form-error"]')
    expect(message.getAttribute('role')).toBe('alert')
    expect(text(message)).toContain('destination')
    expect(posts).toEqual([])
  })
})
