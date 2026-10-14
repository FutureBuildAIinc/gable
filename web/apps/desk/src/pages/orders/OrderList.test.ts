// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The orders list reads a known `status` from the page address on load
 * and opens on that tab, so a link like `/orders?status=confirmed` (the
 * dashboard's Active Orders card) lands on the list it counted.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './OrderList.ts'
import type { GableOrderList } from './OrderList.ts'
import { mountAsync, text, flush, jsonResponse } from '../../test/dom'

describe('the orders list reads the status from the address', () => {
  let requestedUrls: string[] = []

  beforeEach(() => {
    requestedUrls = []
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      requestedUrls.push(url)
      return jsonResponse({ items: [], next_cursor: null })
    }))
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    history.replaceState(null, '', '/orders')
  })

  it('?status=confirmed opens the list on the Confirmed tab and asks the server for confirmed orders', async () => {
    history.replaceState(null, '', '/orders?status=confirmed')
    const el = await mountAsync<GableOrderList>('gable-order-list')

    const ordersCall = requestedUrls.find((u) => u.includes('/api/v1/orders'))
    expect(ordersCall, 'expected a call to the orders list').toBeDefined()
    expect(ordersCall).toContain('status=confirmed')

    // The Confirmed tab is the chosen one.
    const activeTab = Array.from(el.querySelectorAll('button')).find((b) =>
      b.className.includes('text-gable-green'),
    )
    expect(text(activeTab ?? null)).toBe('Confirmed')
  })

  it('a status the list does not know opens All', async () => {
    history.replaceState(null, '', '/orders?status=nonsense')
    const el = await mountAsync<GableOrderList>('gable-order-list')

    const ordersCall = requestedUrls.find((u) => u.includes('/api/v1/orders'))
    expect(ordersCall).toBeDefined()
    expect(ordersCall).not.toContain('status=')

    const activeTab = Array.from(el.querySelectorAll('button')).find((b) =>
      b.className.includes('text-gable-green'),
    )
    expect(text(activeTab ?? null)).toBe('All')
  })

  it('the address follows the chosen tab, replacing rather than pushing', async () => {
    history.replaceState(null, '', '/orders?status=confirmed')
    const el = await mountAsync<GableOrderList>('gable-order-list')
    const pushSpy = vi.spyOn(history, 'pushState')
    const replaceSpy = vi.spyOn(history, 'replaceState')

    const allTab = Array.from(el.querySelectorAll('button')).find((b) => text(b) === 'All') as HTMLButtonElement
    allTab.click()
    await flush()
    await el.updateComplete

    // Choosing All drops the query, so a reload reopens All.
    expect(window.location.pathname + window.location.search).toBe('/orders')
    expect(pushSpy).not.toHaveBeenCalled()
    expect(replaceSpy).toHaveBeenCalled()

    const confirmedTab = Array.from(el.querySelectorAll('button')).find((b) => text(b) === 'Confirmed') as HTMLButtonElement
    confirmedTab.click()
    await flush()
    await el.updateComplete

    expect(window.location.pathname + window.location.search).toBe('/orders?status=confirmed')
  })
})
