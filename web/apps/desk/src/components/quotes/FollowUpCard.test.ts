// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The follow up on a quote: the card shows the date and the owner, marks a
 * late follow up overdue, and writes or clears the follow up on the quote's
 * revision.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './FollowUpCard'
import type { GableFollowUpCard } from './FollowUpCard'
import { mountAsync, update, flush, q, text, jsonResponse } from '../../test/dom'
import type { Quote } from '../../types/quote'

const QUOTE_ID = '11111111-1111-4111-8111-111111111111'
const OWNER_ID = '22222222-2222-4222-8222-222222222222'

/** A date long past: a follow up on it is overdue. */
const PAST = '2020-03-04T00:00:00.000000Z'
/** A date long ahead: a follow up on it is not overdue. */
const FUTURE = '2999-03-04T00:00:00.000000Z'

function quote(followUpAt: string | null, ownerName: string | null = null) {
  return {
    id: QUOTE_ID,
    number: 'Q-0001',
    status: 'sent',
    revision: 7,
    customer_id: '33333333-3333-4333-8333-333333333333',
    customer_name: 'Test Builder Co',
    created_at: '2026-01-01T00:00:00.000000Z',
    total_cents: 0,
    freight_cents: 0,
    delivery_type: 'pickup',
    source: 'manual',
    lines: [],
    follow_up_at: followUpAt,
    follow_up_owner_id: ownerName ? OWNER_ID : null,
    follow_up_owner_name: ownerName,
  } as unknown as Quote
}

interface Recorded {
  url: string
  method: string
  body: string | null
  headers: Headers
}

let calls: Recorded[]
let fetchMock: ReturnType<typeof vi.fn>

function serve() {
  calls = []
  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = (init?.method ?? 'GET').toUpperCase()
    calls.push({ url, method, body: typeof init?.body === 'string' ? init.body : null, headers: new Headers(init?.headers) })

    const path = new URL(url, 'http://localhost').pathname
    if (method === 'GET' && path === '/api/v1/sales-team') {
      return jsonResponse([
        { id: OWNER_ID, name: 'Dana Ramirez', email: 'dana@example.com', phone: '', role: 'Sales', is_active: true },
      ])
    }
    if (method === 'PUT' && path === `/api/v1/quotes/${QUOTE_ID}/follow-up`) {
      const sent = JSON.parse(init?.body as string)
      return jsonResponse({ ...quote(sent.follow_up_at, sent.follow_up_owner_id ? 'Dana Ramirez' : null), revision: 8 })
    }
    return jsonResponse({ message: 'none' }, 404)
  })
  vi.stubGlobal('fetch', fetchMock)
}

async function card(quoteFixture: Quote): Promise<GableFollowUpCard> {
  serve()
  const el = await mountAsync<GableFollowUpCard>('gable-follow-up-card', { quote: quoteFixture })
  await update(el, {})
  return el
}

function button(el: HTMLElement, label: string): HTMLButtonElement {
  const b = Array.from(el.querySelectorAll('button')).find((x) => text(x).includes(label))
  if (!b) throw new Error(`no button "${label}"`)
  return b as HTMLButtonElement
}

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('gable-follow-up-card: what it shows', () => {
  it('says there is no follow up when the quote carries none', async () => {
    const el = await card(quote(null))
    expect(text(q(el, '[data-testid="follow-up-date"]'))).toBe('No follow up set')
    expect(el.querySelector('[data-testid="follow-up-overdue"]')).toBeNull()
  })

  it('shows the date and the owner of a follow up', async () => {
    const el = await card(quote(FUTURE, 'Dana Ramirez'))
    expect(text(q(el, '[data-testid="follow-up-date"]'))).not.toBe('No follow up set')
    expect(text(q(el, '[data-testid="follow-up-owner"]'))).toContain('Dana Ramirez')
  })

  it('marks a follow up whose day has passed overdue', async () => {
    const el = await card(quote(PAST))
    expect(q(el, '[data-testid="follow-up-overdue"]')).not.toBeNull()
    expect(text(q(el, '[data-testid="follow-up-overdue"]'))).toContain('Overdue')
  })

  it('does not mark a follow up still ahead overdue', async () => {
    const el = await card(quote(FUTURE))
    expect(el.querySelector('[data-testid="follow-up-overdue"]')).toBeNull()
  })
})

describe('gable-follow-up-card: what it writes', () => {
  it('sets the follow up on the quote with its revision', async () => {
    const el = await card(quote(null))
    button(el, 'Set follow up').click()
    await flush()
    await update(el, {})

    button(el, 'Save follow up').click()
    await flush()
    await flush()
    await update(el, {})

    const put = calls.find((c) => c.method === 'PUT')
    expect(put, 'expected a follow up write').toBeDefined()
    expect(put!.url).toContain(`/api/v1/quotes/${QUOTE_ID}/follow-up`)
    expect(put!.headers.get('If-Match')).toBe('"7"')
    const sent = JSON.parse(put!.body ?? '{}')
    expect(sent.follow_up_at).toMatch(/^\d{4}-\d{2}-\d{2}T/)
  })

  it('clears the follow up with a null date', async () => {
    const el = await card(quote(FUTURE, 'Dana Ramirez'))
    button(el, 'Clear').click()
    await flush()
    await flush()
    await update(el, {})

    const put = calls.find((c) => c.method === 'PUT')
    expect(put, 'expected a follow up write').toBeDefined()
    expect(JSON.parse(put!.body ?? '{}')).toMatchObject({ follow_up_at: null, follow_up_owner_id: null })
  })

  it('hands the page the quote it wrote, so the page need not reload it', async () => {
    const el = await card(quote(FUTURE, 'Dana Ramirez'))
    const changed: unknown[] = []
    el.addEventListener('follow-up-changed', (e) => changed.push((e as CustomEvent).detail))

    button(el, 'Clear').click()
    await flush()
    await flush()
    await update(el, {})

    expect(changed).toHaveLength(1)
    expect((changed[0] as { quote: { revision: number } }).quote.revision).toBe(8)
  })
})
