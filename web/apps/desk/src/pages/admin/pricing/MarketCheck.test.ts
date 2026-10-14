// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The Market check screen: not configured, running, done, partial and all failed, the summary line, the
 * table's words, the link attributes and the chips, against a stubbed fetch.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './MarketCheck'
import { summarize } from './MarketCheck'
import type { GableMarketCheck } from './MarketCheck'
import { mount, update, flush, q, text, jsonResponse } from '../../../test/dom'

const SETTINGS = {
  connect_url: 'https://connect.example.test',
  webhook_id_saved: true,
  flow_name: 'Pricing flow',
  store_label: 'Home Depot, Spokane WA: nearest store with live prices',
  store_id: '',
  store_zip: '',
  band_pct: '5',
  margin_floor_pct: '15',
  configured: true,
}

function product(n: number, sku = `DL_SPF_${n}`) {
  return { id: `p-${n}`, sku, description: `Stud ${n}`, stock_uom: 'PCS' }
}

function row(n: number, over: Record<string, unknown> = {}) {
  return {
    product_id: `p-${n}`, sku: `DL_SPF_${n}`, description: `Stud ${n}`, uom: 'PCS', our_price_cents: 542, cost_known: true,
    status: 'matched',
    competitor: { title: `HD stud ${n}`, price_cents: 400, listed_price_cents: 4000, link: 'https://www.homedepot.com/p/1', confidence: 88, unit_note: 'Sold as a pack of 10; price divided by 10' },
    diff_pct: 35.5, position: 'above', suggested_price_cents: 420, reason: 'lower_to_band', note: '', ...over,
  }
}

function result(rows: unknown[]) {
  return { flow_name: 'Pricing flow', store_label: SETTINGS.store_label, store_seen: 'Spokane', band_pct: '5', margin_floor_pct: '15', rows }
}

let calls: { method: string; path: string; body: unknown }[]
let settings: Record<string, unknown>
let runAnswer: () => Response
/** The counter's tiles answer; null stands for a server without the route (404). */
let tiles: unknown[] | null

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  calls = []
  settings = SETTINGS
  tiles = null
  runAnswer = () => jsonResponse(result([row(1)]))
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const method = init?.method ?? 'GET'
    calls.push({ method, path: url.pathname, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    if (url.pathname === '/api/v1/admin/market-check/settings') return jsonResponse(settings)
    if (url.pathname === '/api/v1/pos/tiles') {
      calls.push({ method: 'TILES', path: url.search, body: undefined })
      return tiles === null
        ? new Response('404 page not found', { status: 404 })
        : jsonResponse({ categories: [], items: tiles, images: false })
    }
    if (url.pathname === '/api/v1/products') {
      return jsonResponse({ items: [1, 2, 3, 4, 5, 6, 7].map((n) => product(n)).concat([product(99, 'HW_NAILS')]), next_cursor: null, limit: 200 })
    }
    if (url.pathname === '/api/v1/pricing/market-check') return runAnswer()
    return jsonResponse({})
  }))
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

async function open(): Promise<GableMarketCheck> {
  const el = await mount<GableMarketCheck>('gable-market-check')
  await flush()
  await update(el, {})
  return el
}

async function run(el: GableMarketCheck) {
  q<HTMLButtonElement>(el, '[data-testid="run"]').click()
  await flush()
  await update(el, {})
  await flush()
  await update(el, {})
}

describe('Market check screen', () => {
  it('says it is off, with a link to Tech Admin, when not configured', async () => {
    settings = { ...SETTINGS, configured: false, webhook_id_saved: false }
    const el = await open()
    expect(text(q(el, '[data-testid="not-configured"]'))).toContain(
      "Market check is off. An admin enters the FB Connect address and the flow's webhook id in Tech Admin.",
    )
    expect(q<HTMLAnchorElement>(el, '[data-testid="not-configured"] a').getAttribute('href')).toBe('/admin')
    expect(el.querySelector('[data-testid="run"]')).toBeNull()
  })

  it('shows the header, the store label, the badge and the first pick', async () => {
    const el = await open()
    expect(text(el)).toContain('Market check')
    expect(text(q(el, '[data-testid="store-label"]'))).toBe(SETTINGS.store_label)
    expect(text(q(el, '[data-testid="powered-by"]'))).toBe('Powered by FB Connect: Pricing flow')
    const chips = text(q(el, '[data-testid="chips"]'))
    expect(chips).toContain('DL_SPF_1')
    expect(chips).toContain('DL_SPF_6')
    expect(chips).not.toContain('DL_SPF_7')
    expect(chips).not.toContain('HW_NAILS')
  })

  it('prefers the yard top sellers in Lumber from the counter tiles, and says so', async () => {
    tiles = [3, 1, 5].map((n) => ({ product_id: `p-${n}`, sku: `TOP_${n}`, description: `Top ${n}` }))
    const el = await open()
    expect(calls.find((c) => c.method === 'TILES')!.path).toBe('?category=Lumber&limit=6')
    const chips = text(q(el, '[data-testid="chips"]'))
    expect(chips).toContain('TOP_3')
    expect(chips).toContain('TOP_5')
    expect(chips).not.toContain('DL_SPF_1')
    expect(text(el)).toContain('the yard top sellers in Lumber')
  })

  it('falls back to the first catalogue products, never by a SKU prefix, when the tiles are absent', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      if (url.pathname === '/api/v1/admin/market-check/settings') return jsonResponse(SETTINGS)
      if (url.pathname === '/api/v1/products') {
        return jsonResponse({ items: [1, 2, 3, 4, 5, 6, 7].map((n) => product(n, `HW_${n}`)), next_cursor: null, limit: 200 })
      }
      return new Response('404 page not found', { status: 404 })
    }))
    const el = await open()
    const chips = text(q(el, '[data-testid="chips"]'))
    expect(chips).toContain('HW_1')
    expect(chips).toContain('HW_6')
    expect(chips).not.toContain('HW_7')
    expect(text(el)).toContain('the first products in the catalogue')
  })

  it('removes a chip, adds from the search, and runs with the picked ids', async () => {
    const el = await open()
    q<HTMLButtonElement>(el, '[aria-label="Remove DL_SPF_1"]').click()
    await update(el, {})
    const input = q<HTMLInputElement>(el, 'input[type="text"]')
    input.value = 'HW_NAILS'
    input.dispatchEvent(new Event('input'))
    await update(el, {})
    q<HTMLButtonElement>(el, '[data-testid="matches"] button').click()
    await update(el, {})
    await run(el)
    const post = calls.find((c) => c.method === 'POST')!
    expect(post.body).toEqual({ product_ids: ['p-2', 'p-3', 'p-4', 'p-5', 'p-6', 'p-99'] })
  })

  it('shows the placeholders and the reading words while it runs', async () => {
    let release: (r: Response) => void = () => {}
    runAnswer = () => { throw new Error('unused') }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      if (url.pathname === '/api/v1/admin/market-check/settings') return jsonResponse(SETTINGS)
      if (url.pathname === '/api/v1/products') return jsonResponse({ items: [product(1)], next_cursor: null, limit: 200 })
      if (url.pathname === '/api/v1/pos/tiles') return new Response('404 page not found', { status: 404 })
      return new Promise<Response>((resolve) => { release = resolve })
    }))
    const el = await open()
    q<HTMLButtonElement>(el, '[data-testid="run"]').click()
    await flush()
    await update(el, {})
    expect(text(q(el, '[data-testid="running"]'))).toContain('Reading live prices through FB Connect')
    release(jsonResponse(result([row(1)])))
    await flush()
    await update(el, {})
    expect(el.querySelector('[data-testid="running"]')).toBeNull()
  })

  it('renders the done table: words, link attributes, difference, position and the suggestion', async () => {
    runAnswer = () => jsonResponse(result([
      row(1),
      row(2, { position: 'below', diff_pct: -12.5, our_price_cents: 350, suggested_price_cents: 380, reason: 'raise_to_band' }),
      row(3, { position: 'in_line', diff_pct: 0, our_price_cents: 400, suggested_price_cents: 400, reason: 'keep' }),
      row(4, { suggested_price_cents: 589, reason: 'held_at_floor' }),
      row(5, { status: 'unmatched', competitor: null, diff_pct: null, position: '', suggested_price_cents: null, reason: '', note: 'No equivalent found' }),
    ]))
    const el = await open()
    await run(el)
    expect(text(q(el, '[data-testid="summary"]'))).toBe(
      '2 of 5 are above Home Depot by more than 5 percent, 1 is below, 1 is in line; 1 could not be matched',
    )
    const body = text(q(el, 'tbody'))
    expect(body).toContain('$5.42')
    expect(body).toContain('HD stud 1')
    expect(body).toContain('88%')
    expect(body).toContain('Sold as a pack of 10; price divided by 10')
    expect(body).toContain('+35.5%')
    expect(body).toContain('+$1.42')
    expect(body).toContain('Above')
    expect(body).toContain('Below')
    expect(body).toContain('In line')
    expect(body).toContain('Within 5 percent of Home Depot')
    expect(body).toContain('Raise to within 5 percent')
    expect(body).toContain('Keep')
    expect(body).toContain('Held at the 15 percent margin floor')
    expect(body).toContain('No match: No equivalent found')
    expect(text(q(el, '[data-testid="result"]'))).toContain('Home Depot prices are US list prices; no exchange is applied.')
    const a = q<HTMLAnchorElement>(el, 'tbody a')
    expect(a.getAttribute('target')).toBe('_blank')
    expect(a.getAttribute('rel')).toBe('noopener noreferrer')
    expect(a.getAttribute('href')).toBe('https://www.homedepot.com/p/1')
  })

  it('shows a title with no link as plain text', async () => {
    runAnswer = () => jsonResponse(result([row(1, { competitor: { ...row(1).competitor, link: '' } })]))
    const el = await open()
    await run(el)
    expect(el.querySelector('tbody a')).toBeNull()
    expect(text(q(el, 'tbody'))).toContain('HD stud 1')
  })

  it('says some rows could not be read and keeps the rest (partial)', async () => {
    runAnswer = () => jsonResponse(result([row(1), row(2, { status: 'error', competitor: null, diff_pct: null, position: '', suggested_price_cents: null, reason: '', note: 'FB Connect did not answer' })]))
    const el = await open()
    await run(el)
    expect(text(q(el, '[data-testid="partial"]'))).toContain('Some products could not be read')
    expect(text(q(el, 'tbody'))).toContain('FB Connect did not answer')
    expect(el.querySelector('[data-testid="all-failed"]')).toBeNull()
    expect(el.querySelector('[data-testid="summary"]')).not.toBeNull()
  })

  it('says none could be read when every row failed', async () => {
    const bad = { status: 'error', competitor: null, diff_pct: null, position: '', suggested_price_cents: null, reason: '', note: 'FB Connect did not answer' }
    runAnswer = () => jsonResponse(result([row(1, bad), row(2, bad)]))
    const el = await open()
    await run(el)
    expect(text(q(el, '[data-testid="all-failed"]'))).toContain('None of the picked products could be read')
    expect(el.querySelector('[data-testid="summary"]')).toBeNull()
  })

  it('shows the server words when the run itself is refused', async () => {
    runAnswer = () => jsonResponse({ error: { code: 'validation_failed', message: 'pick between 1 and 12 products' } }, 400)
    const el = await open()
    await run(el)
    expect(text(q(el, '[data-testid="run-error"]'))).toContain('pick between 1 and 12 products')
  })

  it('summarize counts singular and plural', () => {
    expect(summarize(result([row(1)]) as never)).toBe('1 of 1 is above Home Depot by more than 5 percent, 0 are below, 0 are in line; 0 could not be matched')
  })
})
