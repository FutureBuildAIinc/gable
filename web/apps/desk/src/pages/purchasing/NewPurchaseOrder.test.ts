// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * A new purchase order opened from a recommendation row is for that row's
 * yard: the form says which yard, and the order is created at it, never at
 * the person's other yard or the default.
 */
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import './NewPurchaseOrder'
import type { LitElement } from 'lit'
import { mountAsync, text, jsonResponse } from '../../test/dom'

let posted: Array<Record<string, unknown>>

beforeEach(() => {
  posted = []
  localStorage.setItem('token', 'test-token')
  history.pushState({}, '', '/purchasing/new?from=recommendation&product_id=p1&description=HANGER-28&qty=40&cost=2'
    + '&vendor_id=11111111-2222-3333-4444-555555555555&vendor_name=Gable%20Building%20Supply&branch_id=y2&branch_name=West%20Kelowna%20Satellite')
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.includes('/api/v1/purchase-orders') && init?.method === 'POST') {
      posted.push(JSON.parse(String(init.body)))
      return jsonResponse({ id: 'po-1' })
    }
    return jsonResponse({ items: [], next_cursor: null })
  }))
})
afterEach(() => {
  vi.unstubAllGlobals()
  history.pushState({}, '', '/')
  document.body.innerHTML = ''
})

describe('new purchase order from a recommendation', () => {
  it('says which yard the order is for', async () => {
    const el = await mountAsync<LitElement>('gable-new-purchase-order')
    expect(text(el)).toContain('West Kelowna Satellite')
  })

  it('creates the order at the row\'s yard', async () => {
    const el = await mountAsync<LitElement>('gable-new-purchase-order')
    const save = Array.from(el.querySelectorAll('button')).find((b) => (b.textContent ?? '').includes('Create PO')) as HTMLButtonElement
    save.click()
    await new Promise((r) => setTimeout(r, 0))
    expect(posted.length).toBe(1)
    expect(posted[0].branch_id).toBe('y2')
  })

  it('shows the vendor\'s name and sends its id', async () => {
    const el = await mountAsync<LitElement>('gable-new-purchase-order')
    expect(text(el)).toContain('Gable Building Supply')
    const save = Array.from(el.querySelectorAll('button')).find((b) => (b.textContent ?? '').includes('Create PO')) as HTMLButtonElement
    save.click()
    await new Promise((r) => setTimeout(r, 0))
    expect(posted[0].vendor_id).toBe('11111111-2222-3333-4444-555555555555')
  })

  it('says in words when the product has no vendor, and never sends a name as an id', async () => {
    history.pushState({}, '', '/purchasing/new?from=recommendation&product_id=p1&description=HANGER-28&qty=40&cost=2&branch_id=y2&branch_name=West%20Kelowna%20Satellite')
    const el = await mountAsync<LitElement>('gable-new-purchase-order')
    expect(text(el)).toContain('has no vendor')
    const save = Array.from(el.querySelectorAll('button')).find((b) => (b.textContent ?? '').includes('Create PO')) as HTMLButtonElement
    save.click()
    await new Promise((r) => setTimeout(r, 0))
    expect(posted.length).toBe(0)
  })
})
