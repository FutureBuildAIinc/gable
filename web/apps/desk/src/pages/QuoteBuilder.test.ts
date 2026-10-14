// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The quote builder is a split pane: Products (with the material list intake
 * inside it) on one side, Customer and terms on the other, one primary Send quote.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './QuoteBuilder'
import type { GableQuoteBuilder } from './QuoteBuilder'
import type { ParsedItem } from '../types/parsing'
import { mount, update, flush, q, text, jsonResponse } from '../test/dom'

const MATCH = {
  product_id: '33333333-3333-4333-8333-333333333333',
  sku: 'SPF-2x4x8',
  description: '2x4x8 SPF Stud',
  uom: 'PCS',
  base_price: 4.25,
}
const ITEM: ParsedItem = { raw_text: '50 pcs - 2x4x8 stud', matched_product: MATCH, quantity: 50, uom: 'PCS', confidence: 0.95, is_special_order: false, alternatives: [] }

const CUSTOMER = {
  id: 'c0000000-0000-4000-8000-000000000001', account_number: 'A-100', name: 'Test Builder Co', price_level: null,
  credit_limit_cents: 500000, balance_cents: 100000, payment_terms: { id: 't', code: 'N30', name: 'Net 30' },
  salesperson_name: null, po_required: false,
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
})

async function builder() {
  const el = await mount<GableQuoteBuilder>('gable-quote-builder')
  await flush()
  await update(el, {})
  return el
}

describe('QuoteBuilder split pane', () => {
  it('has a Products pane and a Customer and terms pane, products first', async () => {
    const el = await builder()
    const panes = Array.from(el.querySelectorAll('[data-pane]')).map((p) => p.getAttribute('data-pane'))
    expect(panes).toEqual(['products', 'customer'])
  })

  it('has one primary action, Send quote', async () => {
    const el = await builder()
    const primaries = el.querySelectorAll('[data-primary="true"]')
    expect(primaries.length).toBe(1)
    expect(text(primaries[0])).toContain('Send quote')
    expect(el.querySelector('[data-testid="save-draft"]')).not.toBeNull()
  })

  it('holds the material list intake inside the Products pane', async () => {
    const el = await builder()
    const pane = q(el, '[data-pane="products"]')
    expect(pane.querySelector('gable-material-intake')).not.toBeNull()
    expect(el.querySelector('gable-parsed-results-panel')).toBeNull()
  })

  it('adds an accepted parsed line to the quote with its product and quantity', async () => {
    const el = await builder()
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [ITEM], bubbles: true }))
    await update(el, {})
    const row = q(el, '[data-testid="quote-line"]')
    expect(text(row)).toContain('SPF-2x4x8')
    expect(q<HTMLInputElement>(row, 'input[data-testid="line-qty"]').value).toBe('50')
  })

  it('steps a quantity and removes a line', async () => {
    const el = await builder()
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [ITEM], bubbles: true }))
    await update(el, {})
    q<HTMLButtonElement>(el, '[data-testid="line-qty-up"]').click()
    await update(el, {})
    expect(q<HTMLInputElement>(el, 'input[data-testid="line-qty"]').value).toBe('51')
    q<HTMLButtonElement>(el, '[data-testid="line-remove"]').click()
    await update(el, {})
    expect(el.querySelector('[data-testid="quote-line"]')).toBeNull()
  })

  it('Send quote saves the draft and then marks it sent at the new revision', async () => {
    const quote = { id: 'q1', number: 'Q-0001', status: 'draft', revision: 1 }
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      const path = new URL(url, 'http://localhost').pathname
      if (path === '/api/v1/quotes' && init?.method === 'POST') return jsonResponse(quote, 201)
      if (path === '/api/v1/quotes/q1/transitions') return jsonResponse({ ...quote, status: 'sent', revision: 2 })
      return jsonResponse({ items: [], next_cursor: null })
    })
    const el = await builder()
    q(el, 'gable-customer-select').dispatchEvent(new CustomEvent('customer-select', { detail: CUSTOMER, bubbles: true }))
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [ITEM], bubbles: true }))
    await update(el, {})
    q<HTMLButtonElement>(el, '[data-primary="true"]').click()
    await flush(); await flush()
    const calls = fetchMock.mock.calls.map(([u, i]) => `${(i as RequestInit | undefined)?.method ?? 'GET'} ${new URL(u as string, 'http://localhost').pathname}`)
    expect(calls).toContain('POST /api/v1/quotes')
    const t = fetchMock.mock.calls.find(([u]) => (u as string).endsWith('/quotes/q1/transitions'))
    expect(t).toBeTruthy()
    expect(JSON.parse((t![1] as RequestInit).body as string)).toEqual({ to: 'sent', revision: 1 })
  })

  it('never lets a sample result join the quote, by any path', async () => {
    fetchMock.mockImplementation(async (url: string) => {
      const path = new URL(url, 'http://localhost').pathname
      if (path === '/api/v1/parsing/upload') {
        return jsonResponse({ items: [ITEM], source_image: '', parse_time_ms: 1, item_count: 1, synthetic: true })
      }
      return jsonResponse({ items: [], next_cursor: null })
    })
    const el = await builder()
    const intake = q(el, 'gable-material-intake')
    const area = q<HTMLTextAreaElement>(intake, 'textarea')
    area.value = 'x'
    area.dispatchEvent(new Event('input', { bubbles: true }))
    await update(el, {})
    q<HTMLButtonElement>(intake, '[data-testid="intake-parse-pasted"]').click()
    await flush(); await flush()
    await update(el, {})
    expect(el.querySelector('[data-testid="intake-sample-banner"]')).not.toBeNull()
    // No Accept anywhere; and the builder drops an accept that arrives anyway.
    expect(el.querySelector('[data-testid="intake-accept"]')).toBeNull()
    intake.dispatchEvent(new CustomEvent('accept-lines', { detail: [ITEM], bubbles: true }))
    await update(el, {})
    expect(el.querySelectorAll('[data-testid="quote-line"]').length).toBe(0)
  })

  it('puts the lines table right under the add line form and folds the material list intake away until used', async () => {
    const el = await builder()
    const pane = q(el, '[data-pane="products"]')
    const order = Array.from(pane.querySelectorAll('gable-line-item-editor, table, details[data-testid="intake-section"]'))
      .map((n) => n.tagName.toLowerCase())
    expect(order).toEqual(['gable-line-item-editor', 'table', 'details'])
    const section = q<HTMLDetailsElement>(el, 'details[data-testid="intake-section"]')
    expect(section.open).toBe(false)
    expect(text(section.querySelector('summary')!)).toContain('From a material list')
  })

  it('shows a stored Valid until as the same local day, west of UTC', async () => {
    const prior = process.env.TZ
    process.env.TZ = 'America/Chicago'
    try {
      const quote = {
        id: 'q9', number: 'Q-0009', branch_id: 'b0000000-0000-4000-8000-000000000001', customer_id: CUSTOMER.id, job_id: null,
        status: 'draft', revision: 2, expires_at: '2031-03-11T04:59:59Z', delivery_type: 'pickup', freight_cents: 0,
        vehicle_id: null, source: 'manual', lines: [],
      }
      fetchMock.mockImplementation(async (url: string) => {
        const path = new URL(url, 'http://localhost').pathname
        if (path === '/api/v1/quotes/q9') return jsonResponse(quote)
        if (path === `/api/v1/customers/${CUSTOMER.id}`) return jsonResponse(CUSTOMER)
        if (path === `/api/v1/branches/${quote.branch_id}`) return jsonResponse({ id: quote.branch_id, name: 'North Yard' })
        return jsonResponse({ items: [], next_cursor: null })
      })
      const el = await mount<GableQuoteBuilder>('gable-quote-builder', { routeId: 'q9' })
      await flush(); await flush()
      await update(el, {})
      expect(q<HTMLInputElement>(el, '#quote-valid-until').value).toBe('2031-03-10')
      expect(text(q(el, '[data-testid="quote-yard"]'))).toContain('North Yard')
    } finally {
      if (prior === undefined) delete process.env.TZ; else process.env.TZ = prior
    }
  })
})
