// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * A cycle count counts by location (item 130, N20): the yard's own row and
 * each zone or bin that holds the product are listed with their own expected
 * figure, and each count is written to its own location, so the yard's total
 * after the count is the sum of the counts.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './CycleCount'
import type { LitElement } from 'lit'
import { mount, flush, update, q, text, jsonResponse } from '../../test/dom'

const PRODUCT = { id: 'p-hanger', sku: 'HANGER-28', description: 'Joist Hanger 2x8', on_hand: '412.0000', allocated: '0.0000' }
const ROW = (id: string, loc: string, name: string, qty: string) => ({
  id, product_id: PRODUCT.id, location_id: loc, location_name: name,
  quantity: qty, allocated: '0.0000', available: qty, uom: 'EA', updated_at: '2026-01-01T00:00:00Z',
})

let posts: Array<Record<string, unknown>>

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  posts = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.includes('/api/v1/inventory/adjust')) {
      posts.push(JSON.parse(String(init?.body)))
      return jsonResponse({ status: 'ok' })
    }
    if (url.includes('/api/v1/inventory')) {
      return jsonResponse({ items: [ROW('i1', 'loc-yard', 'KEL-MAIN', '112.0000'), ROW('i2', 'loc-zone', 'KEL-MAIN/KEL-A', '300.0000')], next_cursor: null })
    }
    if (url.includes('/api/v1/products')) return jsonResponse({ items: [PRODUCT], next_cursor: null })
    return jsonResponse({ items: [], next_cursor: null })
  }))
})
afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

async function openZone(): Promise<LitElement> {
  const el = await mount<LitElement>('gable-cycle-count')
  const card = Array.from(el.querySelectorAll('.cursor-pointer')).find((c) => text(c).includes('Lumber Storage A'))
  if (!card) throw new Error('no zone card')
  ;(card as HTMLElement).click()
  await flush()
  await flush()
  await update(el, {})
  return el
}

describe('Cycle count by location', () => {
  it('lists the yard row and the zone with their own expected figures', async () => {
    const el = await openZone()
    const rows = Array.from(el.querySelectorAll('[data-testid="count-location"]'))
    expect(rows.length).toBe(2)
    expect(text(rows[0])).toContain('KEL-MAIN')
    expect(text(rows[0])).toContain('112')
    expect(text(rows[1])).toContain('KEL-A')
    expect(text(rows[1])).toContain('300')
  })

  it('writes each count to its own location and none without one', async () => {
    const el = await openZone()
    const inputs = Array.from(el.querySelectorAll<HTMLInputElement>('[data-testid="count-input"]'))
    inputs[0].value = '110'
    inputs[0].dispatchEvent(new Event('input', { bubbles: true }))
    inputs[1].value = '300'
    inputs[1].dispatchEvent(new Event('input', { bubbles: true }))
    await update(el, {})
    const submit = Array.from(el.querySelectorAll('button')).find((b) => text(b).startsWith('Submit Count'))
    ;(submit as HTMLButtonElement).click()
    await flush()
    await flush()
    // The yard row was two short and is written to the yard's location; the
    // zone's count matched, so nothing is written for it.
    expect(posts).toEqual([
      { product_id: PRODUCT.id, location_id: 'loc-yard', quantity: 110, reason: expect.stringContaining('Cycle count'), is_delta: false },
    ])
    expect(q(el, '[data-testid="count-done"]')).toBeTruthy()
  })
})
