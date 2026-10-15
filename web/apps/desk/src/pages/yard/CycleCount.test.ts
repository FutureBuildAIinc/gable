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
const ROW = (id: string, loc: string, name: string, qty: string, allocated = '0.0000') => ({
  id, product_id: PRODUCT.id, location_id: loc, location_name: name,
  quantity: qty, allocated, available: qty, uom: 'EA', updated_at: '2026-01-01T00:00:00Z',
})
const LOC = (id: string, path: string, branch: string) => ({ id, path, code: path, branch_id: branch, type: 'zone', name: null, parent_id: branch })

// The levels list answers newest first: the zone (a later receipt) before the yard's own row.
let levels = [ROW('i2', 'loc-zone', 'KEL-MAIN/KEL-A', '300.0000'), ROW('i1', 'loc-yard', 'KEL-MAIN', '112.0000')]

let posts: Array<Record<string, unknown>>
// Location ids whose write the server refuses in words.
let refused: string[]

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  levels = [ROW('i2', 'loc-zone', 'KEL-MAIN/KEL-A', '300.0000'), ROW('i1', 'loc-yard', 'KEL-MAIN', '112.0000')]
  posts = []
  refused = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.includes('/api/v1/inventory/adjust')) {
      const body = JSON.parse(String(init?.body))
      posts.push(body)
      if (refused.includes(body.location_id)) {
        return jsonResponse({ error: { code: 'below_allocated', message: 'Name the location you counted.' } }, 422)
      }
      return jsonResponse({ status: 'ok' })
    }
    if (url.includes('/api/v1/inventory')) {
      return jsonResponse({ items: levels, next_cursor: null })
    }
    if (url.includes('/api/v1/locations')) {
      return jsonResponse({ items: [
        LOC('loc-yard', 'KEL-MAIN', 'yard-1'), LOC('loc-zone', 'KEL-MAIN/KEL-A', 'yard-1'),
        LOC('loc-zone-b', 'KEL-MAIN/KEL-B', 'yard-1'), LOC('loc-other', 'LK-MAIN/LK-A', 'yard-2'),
      ], next_cursor: null, limit: 200 })
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
    const byName = (n: string) => rows.find((r) => text(r).startsWith(`${n} Expected`)) as Element
    expect(text(byName('KEL-MAIN/KEL-A'))).toContain('300')
    expect(text(byName('KEL-MAIN'))).toContain('112')
  })

  it('writes each count to its own location and none without one', async () => {
    const el = await openZone()
    const inputs = Array.from(el.querySelectorAll<HTMLInputElement>('[data-testid="count-input"]'))
    const yardInput = inputs.find((i) => i.getAttribute('aria-label')?.endsWith('KEL-MAIN')) as HTMLInputElement
    const zoneInput = inputs.find((i) => i.getAttribute('aria-label')?.endsWith('KEL-A')) as HTMLInputElement
    yardInput.value = '110'
    yardInput.dispatchEvent(new Event('input', { bubbles: true }))
    zoneInput.value = '300'
    zoneInput.dispatchEvent(new Event('input', { bubbles: true }))
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

  it('adds a scan to the location the person chose, never the newest one', async () => {
    const el = await openZone()
    const scan = (code: string) => (el as unknown as { _handleScan(c: string): void })._handleScan(code)
    // With two locations and none chosen, a scan asks which and counts nothing.
    scan('HANGER-28')
    await update(el, {})
    for (const i of el.querySelectorAll<HTMLInputElement>('[data-testid="count-input"]')) expect(i.value).toBe('')
    // Choose the yard's own row, then scan: the +1 lands there.
    const chooseYard = Array.from(el.querySelectorAll<HTMLElement>('[data-testid="count-choose"]'))
      .find((b) => b.getAttribute('aria-label')?.endsWith('KEL-MAIN')) as HTMLElement
    chooseYard.click()
    await update(el, {})
    scan('HANGER-28')
    await update(el, {})
    const value = (name: string) => (Array.from(el.querySelectorAll<HTMLInputElement>('[data-testid="count-input"]'))
      .find((i) => i.getAttribute('aria-label')?.endsWith(name)) as HTMLInputElement).value
    expect(value('KEL-MAIN')).toBe('1')
    expect(value('KEL-A')).toBe('')
  })

  it('offers only the yard\'s own locations for a found pallet', async () => {
    const el = await openZone()
    const found = Array.from(el.querySelectorAll('button')).find((b) => text(b).includes('Found elsewhere')) as HTMLButtonElement
    found.click()
    await flush()
    await update(el, {})
    const offered = Array.from(el.querySelectorAll('button')).map((b) => text(b))
    expect(offered).toContain('KEL-MAIN/KEL-B')
    expect(offered).not.toContain('LK-MAIN/LK-A')
  })

  it('says in words that a count is under the allocated figure, and holds the submit', async () => {
    levels = [ROW('i1', 'loc-yard', 'KEL-MAIN', '112.0000', '40.0000')]
    const el = await openZone()
    const input = q<HTMLInputElement>(el, '[data-testid="count-input"]')
    input.value = '10'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await update(el, {})
    expect(text(q(el, '[data-testid="count-allocated-warning"]'))).toContain('40 allocated to orders')
    const submit = Array.from(el.querySelectorAll('button')).find((b) => text(b).startsWith('Submit Count')) as HTMLButtonElement
    expect(submit.disabled).toBe(true)
  })

  it('keeps a refused location on the page with its words, and shows done only when every count was written', async () => {
    refused = ['loc-zone']
    const el = await openZone()
    const set = (name: string, value: string) => {
      const i = Array.from(el.querySelectorAll<HTMLInputElement>('[data-testid="count-input"]'))
        .find((x) => x.getAttribute('aria-label')?.endsWith(name)) as HTMLInputElement
      i.value = value
      i.dispatchEvent(new Event('input', { bubbles: true }))
    }
    set('KEL-MAIN', '110')
    set('KEL-A', '290')
    await update(el, {})
    const submit = Array.from(el.querySelectorAll('button')).find((b) => text(b).startsWith('Submit Count')) as HTMLButtonElement
    submit.click()
    await flush()
    await flush()
    await update(el, {})
    expect(posts.length).toBe(2)
    expect(q(el, '[data-testid="count-done"]')).toBeNull()
    const failed = q(el, '[data-testid="count-failed"]')
    expect(text(failed)).toContain('KEL-MAIN/KEL-A')
    expect(text(failed)).toContain('Name the location you counted.')
    // The refused count is still in its box, so it can be corrected and sent again.
    const zoneInput = Array.from(el.querySelectorAll<HTMLInputElement>('[data-testid="count-input"]'))
      .find((x) => x.getAttribute('aria-label')?.endsWith('KEL-A')) as HTMLInputElement
    expect(zoneInput.value).toBe('290')
  })
})
