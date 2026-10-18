// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { GableProToday } from './today.ts'
import { meFixture, todayFixture } from '../test/fixtures.ts'

function todayResponse(): Response {
  return new Response(JSON.stringify(todayFixture), {
    status: 200,
    headers: { 'Content-Type': 'application/json', ETag: 'today-v1', 'X-Portal-Latest-Change': new Date().toISOString() },
  })
}

async function mountToday(me = meFixture): Promise<GableProToday> {
  const element = new GableProToday()
  element.me = me
  document.body.append(element)
  await vi.waitFor(() => expect(element.querySelector('[data-testid="today-actions"]')).not.toBeNull())
  return element
}

describe('Today screen', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    localStorage.clear()
    sessionStorage.clear()
    vi.restoreAllMocks()
    vi.stubGlobal('fetch', vi.fn(async () => todayResponse()))
  })

  it('shows the read counts and opens the quote list with cents intact', async () => {
    const element = await mountToday()
    expect(element.querySelector('[data-testid="today-count-quotes"]')?.textContent).toBe('1')
    const tile = element.querySelector<HTMLButtonElement>('[data-testid="today-tile-quotes"]')
    tile?.click()
    await element.updateComplete
    expect(element.querySelector('[data-testid="today-list-quotes"]')?.textContent).toContain('$73.88')
  })

  it('hides money and approval actions when their capabilities are absent', async () => {
    const me = structuredClone(meFixture)
    me.user.role = 'site_super'
    me.capabilities.see_money = false
    me.capabilities.approve_quotes = false
    const element = await mountToday(me)
    expect(element.querySelector('[data-testid="today-tile-invoices"]')).toBeNull()
    expect(element.querySelector('[data-testid="today-tile-quotes"]')).toBeNull()
    const ordersTile = element.querySelector<HTMLButtonElement>('[data-testid="today-tile-orders"]')
    expect(ordersTile).not.toBeNull()
    ordersTile?.click()
    await element.updateComplete
    const ordersList = element.querySelector('[data-testid="today-list-orders"]')
    expect(ordersList).not.toBeNull()
    expect(ordersList?.textContent).not.toContain('$')
  })

  it('uses the cached read and reports a network fallback', async () => {
    const online = await mountToday()
    const listener = vi.fn()
    window.addEventListener('gable:pro-offline-read', listener, { once: true })
    online.remove()
    Object.defineProperty(navigator, 'onLine', { value: false, configurable: true })
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('offline') }))
    const cached = await mountToday()
    expect(cached.querySelector('[data-testid="today-count-orders"]')?.textContent).toBe('1')
    expect(listener).toHaveBeenCalledOnce()
    Object.defineProperty(navigator, 'onLine', { value: true, configurable: true })
  })
})
