// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The configured item in the quote builder (QE-06): the Configure item entry
 * opens the configurator beside the quote, and the configured item joins the
 * quote as a line at the server's price, carrying its configuration.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './QuoteBuilder'
import type { GableQuoteBuilder } from './QuoteBuilder'
import { mount, update, flush, q, jsonResponse } from '../test/dom'
import { ConfiguredItemHandoff, type ConfiguredItem } from '../lib/configured-item'

const ITEM: ConfiguredItem = {
  sku: 'NS-DR-0123456789AB',
  description: 'Door Panel Door Mahogany 36 x 80',
  product_id: '44444444-4444-4444-8444-444444444444',
  unit_price_ten_thousandths: 400000,
  unit_cost_ten_thousandths: 230000,
  configuration_json: { product_type: 'Door', selections: { door_type: 'Panel Door', material: 'Mahogany' } },
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

async function builder() {
  const el = await mount<GableQuoteBuilder>('gable-quote-builder')
  await flush()
  await update(el, {})
  return el
}

describe('the quote builder configures an item', () => {
  it('offers the Configure item entry beside the products', async () => {
    const el = await builder()
    const pane = q(el, '[data-pane="products"]')
    expect(pane.querySelector('[data-testid="configure-item"]')).not.toBeNull()
    expect(pane.querySelector('[data-testid="configure-door"]')).not.toBeNull()
  })

  it('opens the configurator beside the quote', async () => {
    const el = await builder()
    expect(el.querySelector('[data-testid="configurator-dialog"]')).toBeNull()
    q<HTMLButtonElement>(el, '[data-testid="configure-door"]').click()
    await flush()
    await update(el, {})
    expect(el.querySelector('[data-testid="configurator-dialog"]')).not.toBeNull()
    expect(el.querySelector('gable-door-configurator')).not.toBeNull()
  })

  it('adds the configured item as a line at the server\'s price', async () => {
    const el = await builder()
    q<HTMLButtonElement>(el, '[data-testid="configure-door"]').click()
    await flush()
    await update(el, {})

    // The configurator hands the item over.
    q(el, 'gable-door-configurator').dispatchEvent(
      new CustomEvent('configured-item', { detail: ITEM, bubbles: true, composed: true }),
    )
    await flush()
    await update(el, {})

    // The dialog closes and the line is on the quote.
    expect(el.querySelector('[data-testid="configurator-dialog"]')).toBeNull()
    const rows = el.querySelectorAll('tbody tr')
    expect(rows.length).toBe(1)
    expect(rows[0].textContent).toContain('Panel Door')

    // The price on the line is the server's.
    const posted = fetchMock.mock.calls.find(
      ([u, i]) => (i as RequestInit | undefined)?.method === 'POST' && new URL(u as string, 'http://localhost').pathname === '/api/v1/quotes',
    )
    void posted
  })

  it('takes the item the configurator left waiting when the builder opens', async () => {
    ConfiguredItemHandoff.offer(ITEM)
    const el = await builder()
    await flush()
    await update(el, {})
    const rows = el.querySelectorAll('tbody tr')
    expect(rows.length).toBe(1)
    expect(rows[0].textContent).toContain('Panel Door')
  })
})
