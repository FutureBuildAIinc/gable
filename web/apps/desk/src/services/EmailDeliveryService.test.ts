// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The email delivery read (CP-14): the document's sends through the list
// route, and the bounded follow of one queued delivery until its send
// resolves it.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { EmailDeliveryService, type EmailDelivery } from './EmailDeliveryService'

let fetchMock: ReturnType<typeof vi.fn>

function page(items: Partial<EmailDelivery>[]): unknown {
  return { items, next_cursor: null, limit: items.length }
}

function row(overrides: Partial<EmailDelivery>): EmailDelivery {
  return {
    id: 'd-1',
    created_at: '2026-10-01T12:00:00Z',
    branch_id: null,
    recipient: 'billing@example.test',
    subject: 'Invoice IN-000051',
    document_type: 'invoice',
    document_id: 'doc-1',
    document_number: 'IN-000051',
    status: 'queued',
    sender_answer: null,
    error: null,
    sent_by: null,
    ...overrides,
  } as EmailDelivery
}

beforeEach(() => {
  fetchMock = vi.fn(async () => new Response(JSON.stringify(page([])), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => vi.unstubAllGlobals())

describe('EmailDeliveryService', () => {
  it('lists the document sends through the filter the route declares', async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(page([row({ status: 'logged' })])), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    const rows = await EmailDeliveryService.listForDocument('doc-1')
    expect(rows).toHaveLength(1)
    expect(rows[0].status).toBe('logged')
    const called = new URL(fetchMock.mock.calls[0][0] as string, 'http://localhost')
    expect(called.pathname).toBe('/api/v1/email-deliveries')
    expect(called.searchParams.get('document_id')).toBe('doc-1')
    expect(called.searchParams.get('limit')).toBe('20')
  })

  it('follows a queued delivery until the send resolves it', async () => {
    fetchMock
      .mockResolvedValueOnce(new Response(JSON.stringify(page([row({ status: 'queued' })])), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify(page([row({ status: 'logged' })])), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    const settled = await EmailDeliveryService.follow('d-1', 'doc-1', 500, 5)
    expect(settled?.status).toBe('logged')
  })

  it('returns the still queued row when the bound runs out, never a made up outcome', async () => {
    fetchMock.mockImplementation(async () => new Response(JSON.stringify(page([row({ status: 'queued' })])), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    const unsettled = await EmailDeliveryService.follow('d-1', 'doc-1', 20, 5)
    expect(unsettled?.status).toBe('queued')
  })

  it('returns undefined when the row never appears', async () => {
    fetchMock.mockImplementation(async () => new Response(JSON.stringify(page([])), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    const missing = await EmailDeliveryService.follow('d-1', 'doc-1', 20, 5)
    expect(missing).toBeUndefined()
  })
})
