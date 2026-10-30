// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The quote list's follow up column and the follow up queue tabs: a late
 * follow up is marked overdue, and the queue tab asks the server for the
 * queue instead of filtering on the screen.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './QuoteList'
import type { GableQuoteList } from './QuoteList'
import { mountAsync, update, flush, q, text, jsonResponse } from '../../test/dom'

const QUOTE_ID = '11111111-1111-4111-8111-111111111111'

/** A date long past: a follow up on it is overdue. */
const PAST = '2020-03-04T00:00:00.000000Z'
/** A date long ahead: a follow up on it is not overdue. */
const FUTURE = '2999-03-04T00:00:00.000000Z'

function quote(followUpAt: string | null) {
  return {
    id: QUOTE_ID,
    number: 'Q-0001',
    status: 'sent',
    revision: 1,
    customer_id: '33333333-3333-4333-8333-333333333333',
    customer_name: 'Test Builder Co',
    created_at: '2026-01-01T00:00:00.000000Z',
    total_cents: 125000,
    freight_cents: 0,
    delivery_type: 'pickup',
    source: 'manual',
    follow_up_at: followUpAt,
    follow_up_owner_id: null,
    follow_up_owner_name: null,
  }
}

let urls: string[]
let fetchMock: ReturnType<typeof vi.fn>

function serve(items: ReturnType<typeof quote>[]) {
  urls = []
  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    urls.push(url)
    if (new URL(url, 'http://localhost').pathname === '/api/v1/quotes') {
      return jsonResponse({ items, next_cursor: null, limit: 50, total: items.length })
    }
    return jsonResponse({ message: 'none' }, 404)
  })
  vi.stubGlobal('fetch', fetchMock)
}

async function list(items: ReturnType<typeof quote>[]): Promise<GableQuoteList> {
  serve(items)
  const el = await mountAsync<GableQuoteList>('gable-quote-list', {})
  await flush()
  await update(el, {})
  return el
}

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  window.history.replaceState(null, '', '/quotes')
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('gable-quote-list — the follow up column', () => {
  it('shows the follow up date of a quote that carries one', async () => {
    const el = await list([quote(FUTURE)])
    const cell = q(el, '[data-testid="follow-up-cell"]')
    expect(text(cell)).not.toBe('—')
    expect(el.querySelector('[data-testid="follow-up-overdue"]')).toBeNull()
  })

  it('marks a follow up whose day has passed overdue', async () => {
    const el = await list([quote(PAST)])
    expect(q(el, '[data-testid="follow-up-overdue"]')).not.toBeNull()
    expect(text(q(el, '[data-testid="follow-up-overdue"]'))).toContain('Overdue')
  })

  it('shows a dash for a quote with no follow up', async () => {
    const el = await list([quote(null)])
    expect(text(q(el, '[data-testid="follow-up-cell"]'))).toBe('—')
    expect(el.querySelector('[data-testid="follow-up-overdue"]')).toBeNull()
  })
})

describe('gable-quote-list — the follow up queue tabs', () => {
  it('asks the server for the whole list when no queue is picked', async () => {
    await list([quote(FUTURE)])
    expect(urls.some((u) => u.includes('follow_up='))).toBe(false)
  })

  it('asks the server for the due queue when the Follow ups due tab is picked', async () => {
    const el = await list([quote(PAST)])
    q<HTMLButtonElement>(el, '[data-testid="follow-up-tab-due"]').click()
    await flush()
    await flush()
    await update(el, {})

    expect(urls.some((u) => u.includes('follow_up=due'))).toBe(true)
    expect(window.location.search).toContain('follow_up=due')
  })

  it('asks the server for the overdue queue when the Overdue tab is picked', async () => {
    const el = await list([quote(PAST)])
    q<HTMLButtonElement>(el, '[data-testid="follow-up-tab-overdue"]').click()
    await flush()
    await flush()
    await update(el, {})

    expect(urls.some((u) => u.includes('follow_up=overdue'))).toBe(true)
  })

  it('marks the picked tab as the one in hand', async () => {
    const el = await list([quote(PAST)])
    const tab = q<HTMLButtonElement>(el, '[data-testid="follow-up-tab-due"]')
    tab.click()
    await flush()
    await update(el, {})

    expect(q<HTMLButtonElement>(el, '[data-testid="follow-up-tab-due"]').getAttribute('aria-selected')).toBe('true')
    expect(q<HTMLButtonElement>(el, '[data-testid="follow-up-tab-all"]').getAttribute('aria-selected')).toBe('false')
  })

  it('goes back to the whole list when the All quotes tab is picked', async () => {
    const el = await list([quote(PAST)])
    q<HTMLButtonElement>(el, '[data-testid="follow-up-tab-due"]').click()
    await flush()
    await update(el, {})
    q<HTMLButtonElement>(el, '[data-testid="follow-up-tab-all"]').click()
    await flush()
    await flush()
    await update(el, {})

    expect(window.location.search).not.toContain('follow_up=')
    expect(urls[urls.length - 1]).not.toContain('follow_up=')
  })
})
