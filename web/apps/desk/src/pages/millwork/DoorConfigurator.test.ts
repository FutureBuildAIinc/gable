// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The door configurator on the desk (QE-06): the price is the server's, the
 * dealer adds the item to a quote or an order, and both buttons are live.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, q, text, flush, jsonResponse } from '../../test/dom'
import type { DoorConfigurator } from './DoorConfigurator'
import './DoorConfigurator'
import '../../components/configured/ConfiguredTargetPicker'

const DOOR = {
  id: 'd1',
  category: 'door_type',
  name: 'Panel Door',
  price_adjustment_cents: 0,
  base_price_cents: 25000,
  base_cost_cents: 15000,
  cost_adjustment_cents: 0,
  attributes: null,
  revision: 1,
  created_at: '2026-01-01T00:00:00.000000Z',
  updated_at: '2026-01-01T00:00:00.000000Z',
}

const ESTIMATE = {
  sku: 'NS-DR-0123456789AB',
  description: 'Door Panel Door 36 x 80',
  product_id: '44444444-4444-4444-8444-444444444444',
  unit_price_ten_thousandths: 2500000,
  unit_cost_ten_thousandths: 1500000,
  configuration_json: { product_type: 'Door', selections: { door_type: 'Panel Door' }, width: 36, height: 80 },
  breakdown: [{ category: 'door_type', name: 'Panel Door', price_cents: 25000, cost_cents: 15000 }],
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes('/api/v1/millwork/options')) {
      const category = new URL(url, 'http://localhost').searchParams.get('category')
      return jsonResponse({ items: category === 'door_type' ? [DOOR] : [], next_cursor: null, limit: 100 })
    }
    if (url.includes('/api/v1/millwork/estimate')) return jsonResponse(ESTIMATE)
    return jsonResponse({ items: [], next_cursor: null })
  })
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('the door configurator', () => {
  it('shows the server\'s price and keeps both add buttons live', async () => {
    const el = await mount<DoorConfigurator>('gable-door-configurator')
    await flush()

    // The dealer picks a door type: the server prices it.
    const door = Array.from(el.querySelectorAll('button')).find((b) => text(b).includes(DOOR.name)) as HTMLElement
    expect(door).toBeTruthy()
    door.click()
    await flush()
    await flush()

    expect(text(q<HTMLElement>(el, '[data-testid="estimate-price"]'))).toBe('$250.00')

    const toQuote = q<HTMLButtonElement>(el, '[data-testid="add-to-quote"]')
    const toOrder = q<HTMLButtonElement>(el, '[data-testid="add-to-order"]')
    expect(toQuote.disabled).toBe(false)
    expect(toOrder.disabled).toBe(false)
  })

  it('opens the picker when the dealer adds the item to a quote', async () => {
    const el = await mount<DoorConfigurator>('gable-door-configurator')
    await flush()
    const door = Array.from(el.querySelectorAll('button')).find((b) => text(b).includes(DOOR.name)) as HTMLElement
    door.click()
    await flush()
    await flush()

    q<HTMLButtonElement>(el, '[data-testid="add-to-quote"]').click()
    await flush()

    const picker = q(el, 'gable-configured-target-picker')
    expect(picker).toBeTruthy()
    expect(picker.getAttribute('target')).toBe('quote')
  })

  it('opens the picker for an order when the dealer adds the item to one', async () => {
    const el = await mount<DoorConfigurator>('gable-door-configurator')
    await flush()
    const door = Array.from(el.querySelectorAll('button')).find((b) => text(b).includes(DOOR.name)) as HTMLElement
    door.click()
    await flush()
    await flush()

    q<HTMLButtonElement>(el, '[data-testid="add-to-order"]').click()
    await flush()

    const picker = q(el, 'gable-configured-target-picker')
    expect(picker).toBeTruthy()
    expect(picker.getAttribute('target')).toBe('order')
  })
})
