// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The invoice wire (ADR 0001, ADR 0005 section 6): the cursor envelope, the
 * lowercase status filter, the void transition with its quoted If-Match, and
 * the 409 blockers carried in the one error envelope.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { InvoiceService, buildInvoiceQuery } from './InvoiceService'
import { ApiError, ifMatch } from './apiError'
import { voidBlockerHint } from '../pages/invoices/InvoiceDetail'

let fetchMock: ReturnType<typeof vi.fn>

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function lastUrl(): URL {
  return new URL(fetchMock.mock.calls[fetchMock.mock.calls.length - 1][0] as string, 'http://localhost')
}

function lastInit(): RequestInit {
  return (fetchMock.mock.calls[fetchMock.mock.calls.length - 1][1] ?? {}) as RequestInit
}

beforeEach(() => {
  fetchMock = vi.fn(async () => jsonResponse({ items: [], next_cursor: null, limit: 50 }))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => vi.unstubAllGlobals())

describe('buildInvoiceQuery', () => {
  it('writes every filter under its contract name', () => {
    const q = new URLSearchParams(
      buildInvoiceQuery({
        status: ['unpaid', 'partial'],
        customerId: 'c-1',
        jobId: 'j-1',
        shipToId: 's-1',
        orderId: 'o-1',
        overdue: true,
        limit: 25,
        cursor: 'abc',
        includeTotal: true,
      }),
    )
    expect(q.get('status')).toBe('unpaid,partial')
    expect(q.get('customer_id')).toBe('c-1')
    expect(q.get('job_id')).toBe('j-1')
    expect(q.get('ship_to_id')).toBe('s-1')
    expect(q.get('order_id')).toBe('o-1')
    expect(q.get('overdue')).toBe('true')
    expect(q.get('limit')).toBe('25')
    expect(q.get('cursor')).toBe('abc')
    expect(q.get('include')).toBe('total')
  })

  it('sends overdue=false when asked and nothing at all when no filter is set', () => {
    expect(buildInvoiceQuery({ overdue: false })).toBe('overdue=false')
    expect(buildInvoiceQuery()).toBe('')
  })
})

describe('InvoiceService', () => {
  it('lists through the cursor envelope and returns it whole', async () => {
    fetchMock.mockImplementation(async () => jsonResponse({ items: [{ id: 'i-1', number: 'IN-000001' }], next_cursor: 'next', limit: 50, total: 9 }))
    const page = await InvoiceService.listInvoices({ status: 'unpaid', includeTotal: true })
    expect(lastUrl().pathname).toBe('/api/v1/invoices')
    expect(lastUrl().searchParams.get('status')).toBe('unpaid')
    expect(page.items).toHaveLength(1)
    expect(page.next_cursor).toBe('next')
    expect(page.total).toBe(9)
  })

  it('walks the cursor to the last page for allInvoices', async () => {
    fetchMock
      .mockImplementationOnce(async () => jsonResponse({ items: [{ id: 'a' }], next_cursor: 'c2', limit: 1 }))
      .mockImplementationOnce(async () => jsonResponse({ items: [{ id: 'b' }], next_cursor: null, limit: 1 }))
    const all = await InvoiceService.allInvoices({ limit: 1 })
    expect(all.map(i => i.id)).toEqual(['a', 'b'])
    expect(lastUrl().searchParams.get('cursor')).toBe('c2')
  })

  it('voids through the transitions route with the revision in the body and a quoted If-Match', async () => {
    fetchMock.mockImplementation(async () => jsonResponse({ id: 'i-1', status: 'void', revision: 4 }))
    const inv = await InvoiceService.voidInvoice('i-1', 3, 'billed twice')
    expect(lastUrl().pathname).toBe('/api/v1/invoices/i-1/transitions')
    expect(lastInit().method).toBe('POST')
    expect(JSON.parse(lastInit().body as string)).toEqual({ to: 'void', revision: 3, reason: 'billed twice' })
    expect((lastInit().headers as Headers).get('If-Match')).toBe(ifMatch(3))
    expect(inv.status).toBe('void')
  })

  it('emails through the unchanged route', async () => {
    fetchMock.mockImplementation(async () => new Response(null, { status: 202 }))
    await InvoiceService.emailInvoice('i-1')
    expect(lastUrl().pathname).toBe('/api/v1/invoices/i-1/email')
    expect(lastInit().method).toBe('POST')
  })

  it('parses a void refusal into an ApiError carrying the server message and the blocker code', async () => {
    const body = {
      error: {
        code: 'conflict',
        message: 'the invoice has payments or applied credit memos: reverse them before voiding it',
        details: [{ code: 'has_applications', message: 'payments recorded' }],
      },
      meta: { request_id: 'r-1' },
    }
    fetchMock.mockImplementation(async () => jsonResponse(body, 409))
    const err = await InvoiceService.voidInvoice('i-1', 3, 'oops').catch(e => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(409)
    expect(err.message).toContain('reverse them before voiding')
    expect(err.hasBlocker('has_applications')).toBe(true)
    expect(err.hasBlocker('has_credit_memos')).toBe(false)
    expect(err.requestId).toBe('r-1')
  })

  it('says the blocker in plain words and keeps the server message', async () => {
    const applications = new ApiError(409, 'conflict', 'server words', [{ code: 'has_applications', message: 'x' }])
    expect(voidBlockerHint(applications)).toContain('server words')
    expect(voidBlockerHint(applications)).toContain('then void the invoice')
    const memos = new ApiError(409, 'conflict', 'server words', [{ code: 'has_credit_memos', message: 'x' }])
    expect(voidBlockerHint(memos)).toContain('then void the invoice')
    expect(voidBlockerHint(new ApiError(409, 'invalid_state_transition', 'cannot', []))).toBe('cannot')
  })

  it('throws the fallback for a non-JSON failure instead of returning a partial result', async () => {
    fetchMock.mockImplementation(async () => new Response('boom', { status: 500 }))
    await expect(InvoiceService.getInvoice('i-1')).rejects.toThrow('Failed to fetch invoice')
  })
})
