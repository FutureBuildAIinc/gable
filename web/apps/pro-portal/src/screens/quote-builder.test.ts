// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { GableProQuoteBuilder } from './quote-builder.ts'
import { catalogFixtures, customerQuoteFixture, quoteIds } from '../test/customer-quote-fixtures.ts'
import { meFixture } from '../test/fixtures.ts'
import { publicQuoteFixture, publicToken } from '../test/public-fixtures.ts'
import type { CustomerQuoteView } from '../types.ts'

const productC = 'a7a7a7a7-a7a7-4a7a-8a7a-a7a7a7a7a7a7'

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function sentQuote(quote: CustomerQuoteView): CustomerQuoteView {
  return { ...quote, status: 'sent', effective_status: 'sent', link: { expires_at: quote.valid_until } }
}

function mockPortal(quote: CustomerQuoteView) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof Request ? input.url : input.toString(), 'http://localhost')
    const path = url.pathname
    const method = init?.method ?? 'GET'
    if (path === '/api/portal/v1/customer-quotes') {
      return method === 'POST' ? json(quote, 201) : json({ items: [{ ...customerQuoteFixture, id: quote.id }], next_cursor: null, limit: 30 })
    }
    if (path === '/api/portal/v1/customer-quotes/export') {
      return url.searchParams.get('format') === 'csv' ? new Response('reference,title\nCQ-1001,Deck', { status: 200 }) : json({ customers: [], projects: [], quotes: [quote] })
    }
    if (path === '/api/portal/v1/catalog') return json(catalogFixtures)
    if (path === '/api/portal/v1/projects') return json({ items: [], next_cursor: null, limit: 50 })
    if (path === `/api/portal/v1/customer-quotes/${quote.id}/preview`) return json(publicQuoteFixture)
    if (path === `/api/portal/v1/customer-quotes/${quote.id}/send`) {
      const sent = sentQuote(quote)
      return json({ quote: sent, link: { path: `/q/${publicToken}`, expires_at: sent.valid_until, delivered: false } })
    }
    if (path === `/api/portal/v1/customer-quotes/${quote.id}/link/rotate`) {
      const sent = sentQuote(quote)
      return json({ quote: sent, link: { path: `/q/${publicToken}`, expires_at: sent.valid_until, delivered: false } })
    }
    if (path === `/api/portal/v1/customer-quotes/${quote.id}/link/revoke`) return new Response(null, { status: 204 })
    if (path === `/api/portal/v1/customer-quotes/${quote.id}/confirm`) {
      return json({ ...quote, status: 'confirmed', effective_status: 'confirmed', order: { id: quote.id, number: 'SO-1001', status: 'CONFIRMED', fulfilment: 'ordered', total_cents: 1000 } })
    }
    if (path === `/api/portal/v1/customer-quotes/${quote.id}`) return json(quote)
    return json({ error: 'unexpected' }, 404)
  })
}

async function mountBuilder(quote: CustomerQuoteView = customerQuoteFixture): Promise<{ element: GableProQuoteBuilder, fetchMock: ReturnType<typeof mockPortal> }> {
  const fetchMock = mockPortal(quote)
  vi.stubGlobal('fetch', fetchMock)
  const element = new GableProQuoteBuilder()
  element.me = meFixture
  element.quoteId = quote.id
  document.body.append(element)
  await vi.waitFor(() => expect(element.querySelector('[data-testid="quote-lines"]')).not.toBeNull())
  return { element, fetchMock }
}

describe('customer quote builder', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    localStorage.clear()
    sessionStorage.clear()
    window.history.replaceState({}, '', `/quotes/${quoteIds.quote}`)
    Object.defineProperty(navigator, 'onLine', { value: true, configurable: true })
    vi.restoreAllMocks()
  })

  it('saves line, category, and quote markup with labour, overhead, and terms', async () => {
    const { element, fetchMock } = await mountBuilder()
    expect(element.querySelector('[data-testid="quote-summary"]')?.textContent).toContain('$526.96')
    const percent = element.querySelector<HTMLInputElement>('[data-testid="quote-line-1"] input[aria-label="Markup percent"]')
    if (percent !== null) {
      percent.value = '22'
      percent.dispatchEvent(new Event('input', { bubbles: true }))
    }
    await element.updateComplete
    element.querySelector<HTMLButtonElement>('[data-testid="save-quote"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('.pro-success')?.textContent).toContain('saved and priced'))
    const call = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith(quoteIds.quote) && (init?.method ?? 'GET') === 'PUT')
    const body = JSON.parse(String(call?.[1]?.body))
    expect(body.markup).toEqual({ kind: 'markup', bps: 1800 })
    expect(body.category_rules[0].markup).toEqual({ kind: 'markup', bps: 1500 })
    expect(body.lines[1].markup).toEqual({ kind: 'margin', bps: 2200 })
    expect(body.labour[0].rate_cents).toBe(300000)
    expect(body.overhead[0].value).toBe(500)
    expect(body.deposit_bps).toBe(3000)
    expect(body.tax).toEqual({ mode: 'site_rate', rate_bps: 1000 })
  })

  it('adds a catalog line without putting a price on the request', async () => {
    const { element, fetchMock } = await mountBuilder()
    element.querySelector<HTMLButtonElement>('[data-testid="add-product"]')?.click()
    await element.updateComplete
    element.querySelector<HTMLButtonElement>(`[data-testid="add-product-${productC}"]`)?.click()
    await element.updateComplete
    expect(element.querySelector('[data-testid="quote-line-2"]')?.textContent).toContain('Joist hanger')
    element.querySelector<HTMLButtonElement>('[data-testid="save-quote"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('.pro-success')).not.toBeNull())
    const call = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith(quoteIds.quote) && (init?.method ?? 'GET') === 'PUT')
    const body = JSON.parse(String(call?.[1]?.body))
    expect(body.lines).toHaveLength(3)
    expect(body.lines[2].product_id).toBe(productC)
    expect(body.lines[2].cost_cents).toBeUndefined()
    expect(body.lines[2].sell_cents).toBeUndefined()
  })

  it('previews, sends, rotates, and revokes the customer link', async () => {
    const { element, fetchMock } = await mountBuilder()
    element.querySelector<HTMLButtonElement>('[data-testid="preview-quote"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('[data-testid="preview-sheet"]')).not.toBeNull())
    element.querySelector<HTMLButtonElement>('[data-testid="preview-sheet"] button')?.click()
    await element.updateComplete
    element.querySelector<HTMLButtonElement>('[data-testid="send-quote"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('[data-testid="shared-link"]')).not.toBeNull())
    element.querySelector<HTMLButtonElement>('[data-testid="rotate-link"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('.pro-success')?.textContent).toContain('old link is dead'))
    element.querySelector<HTMLButtonElement>('[data-testid="revoke-link"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('.pro-success')?.textContent).toContain('revoked'))
    for (const action of ['preview', 'send', 'rotate', 'revoke']) {
      expect(fetchMock.mock.calls.some(([input]) => String(input).includes(action))).toBe(true)
    }
  })

  it('confirms an approved quote and blocks every write offline', async () => {
    const approved = { ...customerQuoteFixture, status: 'approved' as const, effective_status: 'approved' }
    const { element, fetchMock } = await mountBuilder(approved)
    element.querySelector<HTMLButtonElement>('[data-testid="confirm-quote"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('.pro-success')?.textContent).toContain('dealer order'))
    expect(fetchMock.mock.calls.some(([input, init]) => String(input).includes('/confirm') && (init?.method ?? 'GET') === 'POST')).toBe(true)
    element.offline = true
    await element.updateComplete
    expect(element.querySelector<HTMLButtonElement>('[data-testid="save-quote"]')?.disabled).toBe(true)
    expect(element.querySelector<HTMLButtonElement>('[data-testid="add-product"]')?.disabled).toBe(true)
    expect(element.querySelector('[data-testid="quote-offline"]')?.textContent).toContain('Nothing is queued')
  })

  it('exports the contractor records in both formats', async () => {
    const { element, fetchMock } = await mountBuilder()
    const create = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:fixture')
    const revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
    element.querySelector<HTMLButtonElement>('[data-testid="export-json"]')?.click()
    element.querySelector<HTMLButtonElement>('[data-testid="export-csv"]')?.click()
    await vi.waitFor(() => expect(create).toHaveBeenCalledTimes(2))
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/export')).length).toBe(2)
    expect(revoke).toHaveBeenCalledTimes(2)
    expect(click).toHaveBeenCalledTimes(2)
  })

  it('loads confirm-preview when dealer prices moved and confirms with accept_price_changes', async () => {
    const approved = { ...customerQuoteFixture, status: 'approved' as const, effective_status: 'approved' }
    let attempts = 0
    const baseMock = mockPortal(approved)
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input instanceof Request ? input.url : input.toString(), 'http://localhost')
      if (url.pathname === `/api/portal/v1/customer-quotes/${approved.id}/confirm`) {
        attempts += 1
        if (attempts === 1) return json({ error: 'price_changed' }, 409)
      }
      if (url.pathname === `/api/portal/v1/customer-quotes/${approved.id}/confirm-preview`) {
        return json({
          price_changed: true,
          captured_material_cost_cents: 217360,
          current_material_cost_cents: 221000,
          delta_cents: 3640,
          captured_margin_cents: 52696,
          projected_margin_cents: 49056,
          lines: [],
        })
      }
      return baseMock(input, init)
    })
    vi.stubGlobal('fetch', fetchMock)
    const element = new GableProQuoteBuilder()
    element.me = meFixture
    element.quoteId = approved.id
    document.body.append(element)
    await vi.waitFor(() => expect(element.querySelector('[data-testid="confirm-quote"]')).not.toBeNull())
    element.querySelector<HTMLButtonElement>('[data-testid="confirm-quote"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('[data-testid="confirm-preview"]')).not.toBeNull())
    expect(element.querySelector('[data-testid="confirm-preview"]')?.textContent).toContain('$2,210.00')
    element.querySelector<HTMLButtonElement>('[data-testid="confirm-accept-price-changes"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('.pro-success')?.textContent).toContain('dealer order'))
    const secondConfirm = fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/confirm'))[1]
    expect(JSON.parse(String(secondConfirm?.[1]?.body))).toEqual({ accept_price_changes: true })
  })
})
