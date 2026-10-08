// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The quote wire (ADR 0001): cursor list, quoted If-Match on every write,
 * the transitions route, and the one error envelope. A renamed parameter or a
 * dropped If-Match is a 400 or 428 at runtime, not a compile error, so these
 * tests pin the exact URLs, headers and bodies.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  QuoteService,
  QuoteApiError,
  quoteErrorMessage,
  buildListQuery,
  ifMatch,
  orderRequestFromQuotePayload,
} from './QuoteService'
import type { QuoteRequest } from '../types/quote'

let fetchMock: ReturnType<typeof vi.fn>

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function lastUrl(): URL {
  const raw = fetchMock.mock.calls[fetchMock.mock.calls.length - 1][0] as string
  return new URL(raw, 'http://localhost')
}

function lastInit(): RequestInit {
  return (fetchMock.mock.calls[fetchMock.mock.calls.length - 1][1] ?? {}) as RequestInit
}

function lastHeader(name: string): string | undefined {
  // fetchWithAuth hands fetch a Headers object.
  return (lastInit().headers as Headers).get(name) ?? undefined
}

function lastBody(): Record<string, unknown> {
  return JSON.parse(lastInit().body as string) as Record<string, unknown>
}

beforeEach(() => {
  fetchMock = vi.fn(async () => jsonResponse({}))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('buildListQuery', () => {
  it('is empty with no params', () => {
    expect(buildListQuery()).toBe('')
  })

  it('joins statuses with commas and sends no offset', () => {
    const q = new URLSearchParams(buildListQuery({ status: ['draft', 'sent'], limit: 50, cursor: 'abc', includeTotal: true, customerId: 'c-1' }))
    expect(q.get('status')).toBe('draft,sent')
    expect(q.get('limit')).toBe('50')
    expect(q.get('cursor')).toBe('abc')
    expect(q.get('include')).toBe('total')
    expect(q.get('customer_id')).toBe('c-1')
    expect(q.has('offset')).toBe(false)
  })

  it('accepts a single status', () => {
    expect(new URLSearchParams(buildListQuery({ status: 'accepted' })).get('status')).toBe('accepted')
  })
})

describe('ifMatch', () => {
  it('quotes the revision', () => {
    expect(ifMatch(3)).toBe('"3"')
  })
})

describe('QuoteService reads', () => {
  it('lists with the cursor envelope', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [], next_cursor: null, limit: 50, total: 0 }))
    const page = await QuoteService.list({ status: 'sent', includeTotal: true })
    expect(lastUrl().pathname).toBe('/api/v1/quotes')
    expect(lastUrl().searchParams.get('status')).toBe('sent')
    expect(page.next_cursor).toBeNull()
    expect(page.total).toBe(0)
  })

  it('gets one quote and analytics', async () => {
    await QuoteService.get('q-1')
    expect(lastUrl().pathname).toBe('/api/v1/quotes/q-1')
    await QuoteService.analytics()
    expect(lastUrl().pathname).toBe('/api/v1/quotes/analytics')
  })
})

describe('QuoteService writes', () => {
  const request: QuoteRequest = {
    customer_id: 'c-1',
    delivery_type: 'pickup',
    freight_cents: 0,
    lines: [{ product_id: 'p-1', sku: 'S', description: 'D', quantity: '12.5', uom: 'PCS', unit_price_ten_thousandths: 55000 }],
  }

  it('creates with POST and the body untouched, money as integers', async () => {
    await QuoteService.create(request)
    expect(lastInit().method).toBe('POST')
    expect(lastUrl().pathname).toBe('/api/v1/quotes')
    const body = lastBody() as { lines: { quantity: unknown; unit_price_ten_thousandths: unknown }[] }
    expect(body.lines[0].quantity).toBe('12.5')
    expect(Number.isInteger(body.lines[0].unit_price_ten_thousandths)).toBe(true)
    expect(lastHeader('If-Match')).toBeUndefined()
  })

  it('updates with PUT and a quoted If-Match', async () => {
    await QuoteService.update('q-1', request, 4)
    expect(lastInit().method).toBe('PUT')
    expect(lastUrl().pathname).toBe('/api/v1/quotes/q-1')
    expect(lastHeader('If-Match')).toBe('"4"')
  })

  it('transitions on the transitions route, never /state', async () => {
    await QuoteService.transition('q-1', 'sent', 2)
    expect(lastInit().method).toBe('POST')
    expect(lastUrl().pathname).toBe('/api/v1/quotes/q-1/transitions')
    expect(lastBody()).toEqual({ to: 'sent', revision: 2 })
    expect(lastHeader('If-Match')).toBe('"2"')
  })

  it('converts with If-Match', async () => {
    await QuoteService.convert('q-1', 5)
    expect(lastInit().method).toBe('POST')
    expect(lastUrl().pathname).toBe('/api/v1/quotes/q-1/convert')
    expect(lastHeader('If-Match')).toBe('"5"')
  })
})

describe('error envelope', () => {
  it('parses code, message, details and request id', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({
      error: {
        code: 'validation_failed',
        message: 'The request is invalid',
        details: [{ field: 'lines[0].uom', message: 'is required', code: 'required' }],
      },
      meta: { request_id: 'req-9' },
    }, 400))
    const err = await QuoteService.create({ customer_id: 'c', lines: [] }).catch(e => e)
    expect(err).toBeInstanceOf(QuoteApiError)
    expect(err.status).toBe(400)
    expect(err.code).toBe('validation_failed')
    expect(err.requestId).toBe('req-9')
    expect(err.details[0].field).toBe('lines[0].uom')
    expect(err.displayMessage).toContain('lines[0].uom: is required')
    expect(quoteErrorMessage(err)).toContain('The request is invalid')
  })

  it('flags a stale revision', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { code: 'stale_revision', message: 'changed' }, meta: { request_id: 'r' } }, 409))
    const err = await QuoteService.transition('q-1', 'sent', 1).catch(e => e)
    expect(err.isStaleRevision).toBe(true)
    expect(err.status).toBe(409)
  })

  it('falls back when the body is not JSON', async () => {
    fetchMock.mockResolvedValueOnce(new Response('upstream down', { status: 502 }))
    const err = await QuoteService.get('q-1').catch(e => e)
    expect(err).toBeInstanceOf(QuoteApiError)
    expect(err.message).toBe('Failed to fetch quote')
    expect(err.code).toBe('unknown_error')
  })
})

describe('orderRequestFromQuotePayload', () => {
  it('maps price_each_cents and a decimal quantity onto the unconverted orders route', () => {
    const req = orderRequestFromQuotePayload({
      customer_id: 'c', quote_id: 'q', revision: 2,
      lines: [{ product_id: 'p', quantity: '12.5', uom: 'PCS', price_each_cents: 550 }],
    })
    expect(req.lines[0]).toEqual({ product_id: 'p', quantity: 12.5, price_each: 550 })
  })

  it('refuses special order lines rather than dropping them', () => {
    expect(() => orderRequestFromQuotePayload({
      customer_id: 'c', quote_id: 'q', revision: 2,
      lines: [{ product_id: null, quantity: '1', uom: 'EA', price_each_cents: 100 }],
    })).toThrow()
  })
})
