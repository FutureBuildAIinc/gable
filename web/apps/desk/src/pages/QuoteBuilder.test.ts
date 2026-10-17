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
import { PricingService } from '../services/pricing.service'
import { ToastService } from '../lib/toast-service'
import type { CalculatedPrice } from '../types/pricing'

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
    await flush()
    await update(el, {})
    const row = q(el, '[data-testid="quote-line"]')
    expect(text(row)).toContain('SPF-2x4x8')
    expect(q<HTMLInputElement>(row, 'input[data-testid="line-qty"]').value).toBe('50')
  })

  it('steps a quantity and removes a line', async () => {
    const el = await builder()
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [ITEM], bubbles: true }))
    await flush()
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
    vi.spyOn(PricingService, 'calculatePrice').mockResolvedValue(priced(MATCH.product_id, 42500))
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [ITEM], bubbles: true }))
    await flush()
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
    await flush()
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

const OSB = {
  product_id: '44444444-4444-4444-8444-444444444444', sku: 'OSB-716-4x8', description: '7/16 OSB 4x8', uom: 'PCS', base_price: 19.5,
}
const OSB_ITEM: ParsedItem = { raw_text: '10 7/16 OSB', matched_product: OSB, quantity: 10, uom: 'PCS', confidence: 0.95, is_special_order: false, alternatives: [] }
const SPF_ITEM: ParsedItem = { ...ITEM, quantity: 4 }

function priced(productId: string, tt: number, uom = 'PCS'): CalculatedPrice {
  return {
    customer_id: CUSTOMER.id, product_id: productId, quantity: '1', uom, unit_price_ten_thousandths: tt,
    price_uom: uom, uom_qty: '1', price_uom_qty: '1', line_total_cents: 0, price_basis: 'level', details: 'Contractor (Level)',
  } as CalculatedPrice
}

function linePrices(el: HTMLElement): string[] {
  return Array.from(el.querySelectorAll('[data-testid="quote-line"]')).map((row) => text(row))
}

describe('QuoteBuilder accepted lines take the customer price', () => {
  async function withCustomer() {
    const el = await builder()
    q(el, 'gable-customer-select').dispatchEvent(new CustomEvent('customer-select', { detail: CUSTOMER, bubbles: true }))
    await update(el, {})
    return el
  }

  it('prices an accepted line at the customer price, not the list price', async () => {
    const spy = vi.spyOn(PricingService, 'calculatePrice').mockResolvedValue(priced(OSB.product_id, 165750))
    const el = await withCustomer()
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [OSB_ITEM], bubbles: true }))
    await flush(); await flush()
    await update(el, {})
    expect(spy).toHaveBeenCalledWith(CUSTOMER.id, OSB.product_id)
    const rows = linePrices(el)
    expect(rows.length).toBe(1)
    expect(rows[0]).toContain('OSB-716-4x8')
    expect(q<HTMLInputElement>(el, '[data-testid="quote-line"] input[aria-label="Unit price"]').value).toBe('16.575')
  })

  it('keeps the order of one batch and joins its lines once', async () => {
    let release: (v: CalculatedPrice) => void = () => {}
    vi.spyOn(PricingService, 'calculatePrice').mockImplementation((_c, productId) =>
      productId === OSB.product_id
        ? new Promise<CalculatedPrice>((res) => { release = res })
        : Promise.resolve(priced(productId, 36125)))
    const el = await withCustomer()
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [OSB_ITEM, SPF_ITEM], bubbles: true }))
    await flush()
    release(priced(OSB.product_id, 165750))
    await flush(); await flush()
    await update(el, {})
    const rows = linePrices(el)
    expect(rows.length).toBe(2)
    expect(rows[0]).toContain('OSB-716-4x8')
    expect(rows[1]).toContain('SPF-2x4x8')
  })

  it('joins a second Accept pressed while the first is still pricing, in the order accepted', async () => {
    let release: (v: CalculatedPrice) => void = () => {}
    vi.spyOn(PricingService, 'calculatePrice').mockImplementation((_c, productId) =>
      productId === OSB.product_id
        ? new Promise<CalculatedPrice>((res) => { release = res })
        : Promise.resolve(priced(productId, 36125)))
    const el = await withCustomer()
    const intake = q(el, 'gable-material-intake')
    intake.dispatchEvent(new CustomEvent('accept-lines', { detail: [OSB_ITEM], bubbles: true }))
    intake.dispatchEvent(new CustomEvent('accept-lines', { detail: [SPF_ITEM], bubbles: true }))
    await flush()
    release(priced(OSB.product_id, 165750))
    await flush(); await flush(); await flush()
    await update(el, {})
    const rows = linePrices(el)
    expect(rows.length).toBe(2)
    expect(rows[0]).toContain('OSB-716-4x8')
    expect(rows[1]).toContain('SPF-2x4x8')
    const prices = Array.from(el.querySelectorAll<HTMLInputElement>('[data-testid="quote-line"] input[aria-label="Unit price"]')).map((i) => i.value)
    expect(prices).toEqual(['16.575', '3.6125'])
  })

  it('saves a draft only after an accepted batch that is still pricing has joined, so no line is lost', async () => {
    const quote = { id: 'q1', number: 'Q-0001', status: 'draft', revision: 1 }
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      const path = new URL(url, 'http://localhost').pathname
      if (path === '/api/v1/quotes' && init?.method === 'POST') return jsonResponse(quote, 201)
      return jsonResponse({ items: [], next_cursor: null })
    })
    let release: (v: CalculatedPrice) => void = () => {}
    vi.spyOn(PricingService, 'calculatePrice').mockImplementation((_c, productId) =>
      productId === OSB.product_id
        ? new Promise<CalculatedPrice>((res) => { release = res })
        : Promise.resolve(priced(productId, 36125)))
    const el = await withCustomer()
    const intake = q(el, 'gable-material-intake')
    intake.dispatchEvent(new CustomEvent('accept-lines', { detail: [SPF_ITEM], bubbles: true }))
    await flush(); await flush()
    intake.dispatchEvent(new CustomEvent('accept-lines', { detail: [OSB_ITEM], bubbles: true }))
    await flush()
    await update(el, {})
    // The first line has joined and the second is still pricing: neither save button may act.
    const draft = q<HTMLButtonElement>(el, '[data-testid="save-draft"]')
    const send = q<HTMLButtonElement>(el, '[data-primary="true"]')
    expect(draft.disabled).toBe(true)
    expect(send.disabled).toBe(true)
    expect(text(draft)).toContain('Adding lines')
    draft.click()
    send.click()
    await flush()
    expect(fetchMock.mock.calls.some(([, i]) => (i as RequestInit | undefined)?.method === 'POST')).toBe(false)
    release(priced(OSB.product_id, 165750))
    await flush(); await flush(); await flush()
    await update(el, {})
    expect(q<HTMLButtonElement>(el, '[data-testid="save-draft"]').disabled).toBe(false)
    q<HTMLButtonElement>(el, '[data-testid="save-draft"]').click()
    await flush(); await flush()
    const post = fetchMock.mock.calls.find(([u, i]) => (i as RequestInit | undefined)?.method === 'POST' && new URL(u as string, 'http://localhost').pathname === '/api/v1/quotes')
    expect(post).toBeTruthy()
    const body = JSON.parse((post![1] as RequestInit).body as string)
    expect(body.lines.map((l: { sku: string }) => l.sku)).toEqual(['SPF-2x4x8', 'OSB-716-4x8'])
  })

  it('reprices the batch at the base price when the customer is cleared while pricing runs', async () => {
    let release: (v: CalculatedPrice) => void = () => {}
    vi.spyOn(PricingService, 'calculatePrice').mockImplementation(
      () => new Promise<CalculatedPrice>((res) => { release = res }))
    const el = await withCustomer()
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [OSB_ITEM], bubbles: true }))
    await flush()
    q<HTMLButtonElement>(el, '[data-testid="change-customer"]').click()
    await update(el, {})
    release(priced(OSB.product_id, 165750))
    await flush(); await flush(); await flush()
    await update(el, {})
    expect(q<HTMLInputElement>(el, '[data-testid="quote-line"] input[aria-label="Unit price"]').value).toBe('19.50')
  })

  it('reprices the batch for the new customer when the customer changes while pricing runs', async () => {
    const OTHER = { ...CUSTOMER, id: 'c0000000-0000-4000-8000-000000000002', name: 'Other Builder Co' }
    const releases: Array<(v: CalculatedPrice) => void> = []
    const spy = vi.spyOn(PricingService, 'calculatePrice').mockImplementation((customerId, productId) =>
      customerId === CUSTOMER.id
        ? new Promise<CalculatedPrice>((res) => { releases.push(res) })
        : Promise.resolve(priced(productId, 175000)))
    const el = await withCustomer()
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [OSB_ITEM], bubbles: true }))
    await flush()
    q<HTMLButtonElement>(el, '[data-testid="change-customer"]').click()
    await update(el, {})
    q(el, 'gable-customer-select').dispatchEvent(new CustomEvent('customer-select', { detail: OTHER, bubbles: true }))
    await update(el, {})
    releases.forEach((r) => r(priced(OSB.product_id, 165750)))
    await flush(); await flush(); await flush()
    await update(el, {})
    expect(spy).toHaveBeenCalledWith(OTHER.id, OSB.product_id)
    expect(q<HTMLInputElement>(el, '[data-testid="quote-line"] input[aria-label="Unit price"]').value).toBe('17.50')
  })

  it('falls back to the base price when pricing fails, with one message for the batch', async () => {
    vi.spyOn(PricingService, 'calculatePrice').mockRejectedValue(new Error('down'))
    const toast = vi.spyOn(ToastService, 'show').mockImplementation(() => {})
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const el = await withCustomer()
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [OSB_ITEM, SPF_ITEM], bubbles: true }))
    await flush(); await flush()
    await update(el, {})
    const inputs = Array.from(el.querySelectorAll<HTMLInputElement>('[data-testid="quote-line"] input[aria-label="Unit price"]')).map((i) => i.value)
    expect(inputs).toEqual(['19.50', '4.25'])
    const errors = toast.mock.calls.filter(([, type]) => type === 'error')
    expect(errors.length).toBe(1)
    expect(errors[0][0]).toBe('Customer prices could not be read for 2 lines; the base price is on them')
  })

  it('uses the base price when the customer is not named, with no pricing call', async () => {
    const spy = vi.spyOn(PricingService, 'calculatePrice')
    const el = await builder()
    q(el, 'gable-material-intake').dispatchEvent(new CustomEvent('accept-lines', { detail: [OSB_ITEM], bubbles: true }))
    await flush()
    await update(el, {})
    expect(spy).not.toHaveBeenCalled()
    expect(q<HTMLInputElement>(el, '[data-testid="quote-line"] input[aria-label="Unit price"]').value).toBe('19.50')
  })
})
