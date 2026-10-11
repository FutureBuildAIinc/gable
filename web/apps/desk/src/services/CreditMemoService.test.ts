// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The credit memo wire (ADR 0005 section 6.3): the cursor envelope, the draft
 * create and update (revision beside If-Match), and post and void as
 * transitions. The old per-invoice and per-customer routes are gone.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { CreditMemoService, buildCreditMemoQuery } from './CreditMemoService'
import { ApiError } from './apiError'

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

function lastBody(): Record<string, unknown> {
  return JSON.parse(lastInit().body as string) as Record<string, unknown>
}

beforeEach(() => {
  fetchMock = vi.fn(async () => jsonResponse({ id: 'cm-1', status: 'draft', revision: 1 }))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => vi.unstubAllGlobals())

describe('CreditMemoService', () => {
  it('writes the filters under their contract names', () => {
    const q = new URLSearchParams(buildCreditMemoQuery({ status: ['draft', 'open'], customerId: 'c', invoiceId: 'i', jobId: 'j', limit: 10, cursor: 'z', includeTotal: true }))
    expect(q.get('status')).toBe('draft,open')
    expect(q.get('customer_id')).toBe('c')
    expect(q.get('invoice_id')).toBe('i')
    expect(q.get('job_id')).toBe('j')
    expect(q.get('limit')).toBe('10')
    expect(q.get('cursor')).toBe('z')
    expect(q.get('include')).toBe('total')
  })

  it('lists the cursor envelope from /credit-memos', async () => {
    fetchMock.mockImplementation(async () => jsonResponse({ items: [{ id: 'cm-1', number: null, total_cents: -4400 }], next_cursor: null, limit: 50 }))
    const page = await CreditMemoService.listCreditMemos({ invoiceId: 'i-1' })
    expect(lastUrl().pathname).toBe('/api/v1/credit-memos')
    expect(lastUrl().searchParams.get('invoice_id')).toBe('i-1')
    expect(page.items[0].number).toBeNull()
    expect(page.items[0].total_cents).toBe(-4400)
  })

  it('creates a draft with the negative decimal strings it was given, unchanged', async () => {
    const request = {
      invoice_id: 'i-1',
      reason_code: 'return' as const,
      reason: 'wrong species',
      lines: [{ invoice_line_id: 'l-1', quantity: '-2', restock: true }],
    }
    await CreditMemoService.createCreditMemo(request)
    expect(lastUrl().pathname).toBe('/api/v1/credit-memos')
    expect(lastInit().method).toBe('POST')
    expect(lastBody()).toEqual(request)
  })

  it('updates a draft with the revision in the body and a quoted If-Match', async () => {
    await CreditMemoService.updateCreditMemo('cm-1', 2, { reason_code: 'damage', reason: 'chipped', lines: [] })
    expect(lastUrl().pathname).toBe('/api/v1/credit-memos/cm-1')
    expect(lastInit().method).toBe('PUT')
    expect(lastBody().revision).toBe(2)
    expect((lastInit().headers as Headers).get('If-Match')).toBe('"2"')
  })

  it('posts a draft as the open transition and voids with a reason', async () => {
    await CreditMemoService.post('cm-1', 1)
    expect(lastUrl().pathname).toBe('/api/v1/credit-memos/cm-1/transitions')
    expect(lastBody()).toEqual({ to: 'open', revision: 1 })
    await CreditMemoService.voidCreditMemo('cm-1', 2, 'entered twice')
    expect(lastBody()).toEqual({ to: 'void', revision: 2, reason: 'entered twice' })
    expect((lastInit().headers as Headers).get('If-Match')).toBe('"2"')
  })

  it('carries the exceeds_billed blocker of a create', async () => {
    fetchMock.mockImplementation(async () =>
      jsonResponse({ error: { code: 'conflict', message: 'cannot credit more than was billed', details: [{ code: 'exceeds_billed', message: 'line 1' }] }, meta: { request_id: 'r' } }, 409),
    )
    const err = await CreditMemoService.createCreditMemo({ reason_code: 'return', reason: 'x', lines: [] }).catch(e => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.hasBlocker('exceeds_billed')).toBe(true)
  })

  it('walks the cursor for allCreditMemos', async () => {
    fetchMock
      .mockImplementationOnce(async () => jsonResponse({ items: [{ id: 'a' }], next_cursor: 'n', limit: 1 }))
      .mockImplementationOnce(async () => jsonResponse({ items: [{ id: 'b' }], next_cursor: null, limit: 1 }))
    expect((await CreditMemoService.allCreditMemos({ limit: 1 })).map(m => m.id)).toEqual(['a', 'b'])
  })
})
