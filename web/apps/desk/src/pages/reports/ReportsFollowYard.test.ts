// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The AR aging report and the customer statement page re-read when the person
 * switches yards (`gable:branch-changed`), as the list pages do.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './ARAgingReport'
import './CustomerStatementPage'
import { mountAsync } from '../../test/dom'
import { jsonResponse } from '../../test/dom'

const CUSTOMER_ID = '00000000-0000-4000-8000-0000000000c1'
const flush = () => new Promise(r => setTimeout(r, 0))

describe('reports follow the yard switcher', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    window.history.pushState({}, '', '/')
  })

  it('AR aging fetches again on a yard change and stops after it is removed', async () => {
    const urls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      urls.push(String(input))
      return jsonResponse({ items: [], next_cursor: null, totals: [] })
    }))
    const el = await mountAsync('gable-ar-aging-report')
    await flush()
    const before = urls.length
    expect(before).toBeGreaterThan(0)
    window.dispatchEvent(new CustomEvent('gable:branch-changed'))
    await flush()
    expect(urls.length).toBeGreaterThan(before)
    el.remove()
    const after = urls.length
    window.dispatchEvent(new CustomEvent('gable:branch-changed'))
    await flush()
    expect(urls.length).toBe(after)
  })

  it('the statement re-reads only when a customer is selected', async () => {
    const urls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      urls.push(String(input))
      return new Response('{}', { status: 404, headers: { 'Content-Type': 'application/json' } })
    }))
    const el = await mountAsync('gable-customer-statement')
    window.dispatchEvent(new CustomEvent('gable:branch-changed'))
    await flush()
    expect(urls.filter(u => u.includes('/statement'))).toHaveLength(0)
    ;(el as unknown as { selectedCustomer: unknown }).selectedCustomer = { id: CUSTOMER_ID, name: 'X', branch_ids: [] }
    ;(el as unknown as { customerId: string }).customerId = CUSTOMER_ID
    window.dispatchEvent(new CustomEvent('gable:branch-changed'))
    await flush()
    expect(urls.filter(u => u.includes('/statement'))).toHaveLength(1)
    await (el as unknown as { updateComplete: Promise<unknown> }).updateComplete
    expect(el.querySelector('[data-testid="statement-yard-note"]')).not.toBeNull()
  })
})
