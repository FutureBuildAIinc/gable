// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The configured item (QE-06) on the desk: the handoff a configurator makes
 * to the document that carries the item, the server-priced estimate the door
 * configurator reads, and the line the quote builder adds.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { ConfiguredItemHandoff, CONFIGURED_ITEM_EVENT, type ConfiguredItem } from '../lib/configured-item'
import { MillworkService } from '../services/MillworkService'
import { jsonResponse } from '../test/dom'

const ITEM: ConfiguredItem = {
  sku: 'NS-DR-0123456789AB',
  description: 'Door Panel Door Mahogany 36 x 80',
  product_id: '44444444-4444-4444-8444-444444444444',
  unit_price_ten_thousandths: 400000,
  unit_cost_ten_thousandths: 230000,
  configuration_json: { product_type: 'Door', selections: { door_type: 'Panel Door' } },
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  fetchMock = vi.fn(async () => jsonResponse({ items: [], next_cursor: null }))
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
  ConfiguredItemHandoff.take()
})

describe('the configured item handoff', () => {
  it('offers the item once and takes it once', () => {
    expect(ConfiguredItemHandoff.peek()).toBeNull()
    ConfiguredItemHandoff.offer(ITEM)
    expect(ConfiguredItemHandoff.peek()).toEqual(ITEM)
    expect(ConfiguredItemHandoff.take()).toEqual(ITEM)
    expect(ConfiguredItemHandoff.take()).toBeNull()
  })

  it('tells the listeners when the item is offered', () => {
    const seen: ConfiguredItem[] = []
    const off = ConfiguredItemHandoff.subscribe((item) => seen.push(item))
    ConfiguredItemHandoff.offer(ITEM)
    off()
    ConfiguredItemHandoff.offer({ ...ITEM, sku: 'NS-DR-OTHER' })
    expect(seen).toHaveLength(1)
    expect(seen[0].sku).toBe(ITEM.sku)
  })

  it('names the event the configurators fire', () => {
    expect(CONFIGURED_ITEM_EVENT).toBe('configured-item')
  })
})

describe('the millwork service prices on the server', () => {
  it('sends the configuration and reads the server\'s price', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ ...ITEM, breakdown: [{ category: 'door_type', name: 'Panel Door', price_cents: 25000, cost_cents: 15000 }] }),
    )
    const estimated = await MillworkService.estimate({
      product_type: 'Door',
      selections: { door_type: 'Panel Door' },
      width: 36,
      height: 80,
    })
    expect(estimated.unit_price_ten_thousandths).toBe(400000)
    expect(estimated.breakdown[0].price_cents).toBe(25000)
    const call = fetchMock.mock.calls[0]
    expect(call[0]).toContain('/api/v1/millwork/estimate')
    expect(JSON.parse(call[1].body)).toEqual({
      product_type: 'Door',
      selections: { door_type: 'Panel Door' },
      width: 36,
      height: 80,
    })
  })

  it('sends the option\'s money on its revision when it updates', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ id: 'o1', category: 'door_type', name: 'Panel Door', revision: 4 }))
    await MillworkService.updateOption('o1', {
      category: 'door_type',
      name: 'Panel Door',
      price_adjustment_cents: 0,
      base_price_cents: 25000,
      base_cost_cents: 15000,
      cost_adjustment_cents: 0,
      revision: 3,
    })
    const call = fetchMock.mock.calls[0]
    expect(call[0]).toContain('/api/v1/millwork/options/o1')
    expect(call[1].method).toBe('PUT')
    expect(new Headers(call[1].headers).get('If-Match')).toBe('"3"')
  })
})
