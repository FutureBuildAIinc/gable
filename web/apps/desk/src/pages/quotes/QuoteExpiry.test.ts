// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** QE-01-a: the Valid until column and mark on the list, and the expired detail page. */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './QuoteDetail'
import './QuoteList'
import type { GableQuoteList } from './QuoteList'
import type { GableQuoteDetail } from './QuoteDetail'
import { mount, flush, update, q, text, jsonResponse } from '../../test/dom'
import { isQuoteExpired, quoteExpiredWords } from './quoteExpiry'

const PAST = '2020-03-04T12:00:00Z'
const FUTURE = '2999-03-04T12:00:00Z'

function quote(id: string, status: string, expires_at: string | null) {
  return {
    id, number: `Q-${id}`, status, revision: 3, source: 'manual', customer_id: 'c0000000-0000-4000-8000-000000000001',
    customer_name: 'Test Builder Co', created_at: '2026-01-01T00:00:00Z', expires_at, lines: [],
    subtotal_cents: 0, tax_cents: 0, total_cents: 0, freight_cents: 0, delivery_type: 'pickup',
  }
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    const path = new URL(url, 'http://localhost').pathname
    if (path === '/api/v1/quotes' ) {
      return jsonResponse({ items: [quote('e1', 'expired', PAST), quote('o1', 'sent', FUTURE), quote('n1', 'draft', null)], next_cursor: null, total: 3 })
    }
    if (path === '/api/v1/quotes/e1') return jsonResponse(quote('e1', 'expired', PAST))
    if (path === '/api/v1/quotes/o1') return jsonResponse(quote('o1', 'sent', FUTURE))
    if (path === '/api/v1/quotes/e1/transitions' && init?.method === 'POST') return jsonResponse(quote('e1', 'draft', PAST))
    return jsonResponse({ message: 'none' }, 404)
  })
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('quote expiry helpers', () => {
  it('is expired in the expired state or open past its date, never when accepted', () => {
    expect(isQuoteExpired({ status: 'expired', expires_at: null })).toBe(true)
    expect(isQuoteExpired({ status: 'sent', expires_at: PAST })).toBe(true)
    expect(isQuoteExpired({ status: 'sent', expires_at: FUTURE })).toBe(false)
    expect(isQuoteExpired({ status: 'draft', expires_at: null })).toBe(false)
    expect(isQuoteExpired({ status: 'accepted', expires_at: PAST })).toBe(false)
  })
  it('uses the ruling words', () => {
    expect(quoteExpiredWords({ status: 'expired', expires_at: PAST })).toMatch(
      /^This quote expired on .+: revise it with a new valid until date to send or convert it$/)
  })
})

describe('QuoteList Valid until', () => {
  it('shows a Valid until column and the Expired mark only on the past quote', async () => {
    const el = await mount<GableQuoteList>('gable-quote-list')
    await flush(); await flush()
    await update(el, {})
    expect(text(el)).toContain('Valid until')
    expect(el.querySelectorAll('[data-testid="quote-expired-mark"]').length).toBe(1)
    expect(text(el)).toContain('No date')
    const row = el.querySelector('[data-testid="quote-expired-mark"]')!.closest('tr')!
    expect(row.querySelector('[aria-label="Convert to order"]')).toBeNull()
    expect(row.querySelector('[aria-label="Accept quote"]')).toBeNull()
  })
})

describe('QuoteDetail expired quote', () => {
  async function detail(id: string) {
    const el = await mount<GableQuoteDetail>('gable-quote-detail', { routeId: id } as Partial<GableQuoteDetail>)
    await flush(); await flush()
    await update(el, {})
    return el
  }
  const button = (el: HTMLElement, label: string) =>
    Array.from(el.querySelectorAll('button')).find((b) => text(b).includes(label))

  it('says the ruling words, offers Reopen to edit, and no Send or Convert', async () => {
    const el = await detail('e1')
    expect(text(q(el, '[data-testid="quote-expired-notice"]'))).toContain('revise it with a new valid until date to send or convert it')
    expect(text(el)).toContain('Valid until')
    expect(button(el, 'Reopen to edit')).toBeTruthy()
    expect(button(el, 'Mark Sent')).toBeUndefined()
    expect(button(el, 'Convert to Order')).toBeUndefined()
  })

  it('offers Send and Convert, no notice, on a quote still in date', async () => {
    const el = await detail('o1')
    expect(el.querySelector('[data-testid="quote-expired-notice"]')).toBeNull()
    expect(button(el, 'Convert to Order')).toBeTruthy()
    expect(button(el, 'Reopen to edit')).toBeUndefined()
  })
})
