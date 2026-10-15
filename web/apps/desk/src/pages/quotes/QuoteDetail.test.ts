// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors


/**
 * The saved quote of a material list: words that are true for either reader, and a pasted
 * list's own text on the Original Upload tab.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './QuoteDetail'
import type { GableQuoteDetail } from './QuoteDetail'
import { createElement, Sparkles } from 'lucide'
import { mount, flush, update, q, text, jsonResponse } from '../../test/dom'

const QUOTE = {
  id: 'q1', number: 'Q-0001', status: 'draft', revision: 1, source: 'ai', customer_id: 'c0000000-0000-4000-8000-000000000001',
  customer_name: 'Test Builder Co', created_at: '2026-01-01T00:00:00Z', lines: [],
  subtotal_cents: 0, tax_cents: 0, total_cents: 0, freight_cents: 0, delivery_type: 'pickup',
  original_filename: 'list.txt', original_content_type: 'text/plain',
  parse_map: [{
    raw_text: '10 osb', matched_sku: 'OSB', confidence: 0.95, is_special_order: false,
    matched_product: { product_id: 'p1', sku: 'OSB-716-4x8', description: '7/16 OSB 4x8', uom: 'PCS', base_price: 19.5 },
    quantity: 10, uom: 'PCS',
    alternatives: [{ product_id: 'p2', sku: 'OSB-12', description: '1/2 OSB', uom: 'PCS', base_price: 21.3725 }],
  }],
}

let fetchMock: ReturnType<typeof vi.fn>

function lines(n: number): string {
  return Array.from({ length: n }, (_, i) => `line ${i + 1} 2x4x8 stud`).join('\n')
}

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  fetchMock = vi.fn(async (url: string) => {
    const path = new URL(url, 'http://localhost').pathname
    if (path === '/api/v1/quotes/q1/file') {
      return new Response(lines(250), { status: 200, headers: { 'Content-Type': 'text/plain' } })
    }
    if (path === '/api/v1/quotes/q1') return jsonResponse(QUOTE)
    if (path === '/api/v1/quotes/q2/file') {
      return new Response('second quote text', { status: 200, headers: { 'Content-Type': 'text/plain' } })
    }
    if (path === '/api/v1/quotes/q2') return jsonResponse({ ...QUOTE, id: 'q2', number: 'Q-0002' })
    return jsonResponse({ message: 'none' }, 404)
  })
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

async function detail() {
  const el = await mount<GableQuoteDetail>('gable-quote-detail', { routeId: 'q1' } as Partial<GableQuoteDetail>)
  await flush(); await flush()
  await update(el, {})
  return el
}

function tab(el: HTMLElement, label: string): HTMLButtonElement {
  const b = Array.from(el.querySelectorAll('button')).find((x) => text(x).includes(label))
  if (!b) throw new Error(`no tab ${label}`)
  return b as HTMLButtonElement
}

describe('QuoteDetail material list words', () => {
  it('says Material list and List mapping, never AI', async () => {
    const el = await detail()
    expect(text(el)).toContain('Material list')
    expect(text(el)).toContain('List mapping')
    expect(text(el)).not.toMatch(/AI Parsed|AI Mapping|AI Extraction/i)
    tab(el, 'List mapping').click()
    await update(el, {})
    expect(text(el)).toContain('List mapping')
    expect(text(el)).toContain('1 items extracted')
  })

  it('shows a pasted list as text, cut at 200 lines, never naming the MIME type', async () => {
    const el = await detail()
    tab(el, 'Original Upload').click()
    await flush(); await flush()
    await update(el, {})
    const pre = q(el, '[data-testid="original-text"]')
    expect(pre.textContent).toContain('line 1 2x4x8 stud')
    expect(pre.textContent).toContain('line 200 2x4x8 stud')
    expect(pre.textContent).not.toContain('line 201 ')
    expect(text(el)).toContain('first 200 lines')
    expect(text(el)).not.toContain('text/plain')
    expect(text(el)).not.toContain('Preview not available')
    expect(el.querySelector('a[download]')).not.toBeNull()
  })

  it('labels the mapping price as the catalogue price with its unit, all four decimals kept', async () => {
    const el = await detail()
    tab(el, 'List mapping').click()
    await update(el, {})
    expect(text(el)).toContain('Catalogue $19.50 / PCS')
    expect(text(el)).toContain('Catalogue $21.3725 / PCS')
    expect(text(el)).not.toContain('/ea')
  })

  it('shows the new quote\'s own text when the route changes with the Original Upload tab open', async () => {
    const el = await detail()
    tab(el, 'Original Upload').click()
    await flush(); await flush()
    await update(el, {})
    expect(q(el, '[data-testid="original-text"]').textContent).toContain('line 1 2x4x8 stud')
    await update(el, { routeId: 'q2' } as Partial<GableQuoteDetail>)
    await flush(); await flush(); await flush()
    await update(el, {})
    expect(q(el, '[data-testid="original-text"]').textContent).toContain('second quote text')
  })

  it('keeps the new quote\'s text when the earlier quote\'s slow text answers last', async () => {
    let answerQ1: (r: Response) => void = () => {}
    const slowQ1 = new Promise<Response>((res) => { answerQ1 = res })
    fetchMock.mockImplementation(async (url: string) => {
      const path = new URL(url, 'http://localhost').pathname
      if (path === '/api/v1/quotes/q1/file') return slowQ1
      if (path === '/api/v1/quotes/q1') return jsonResponse(QUOTE)
      if (path === '/api/v1/quotes/q2/file') return new Response('second quote text', { status: 200, headers: { 'Content-Type': 'text/plain' } })
      if (path === '/api/v1/quotes/q2') return jsonResponse({ ...QUOTE, id: 'q2', number: 'Q-0002' })
      return jsonResponse({ message: 'none' }, 404)
    })
    const el = await detail()
    tab(el, 'Original Upload').click()
    await flush()
    await update(el, { routeId: 'q2' } as Partial<GableQuoteDetail>)
    await flush(); await flush(); await flush()
    await update(el, {})
    expect(q(el, '[data-testid="original-text"]').textContent).toContain('second quote text')
    answerQ1(new Response('first quote text', { status: 200, headers: { 'Content-Type': 'text/plain' } }))
    await flush(); await flush(); await flush()
    await update(el, {})
    expect(q(el, '[data-testid="original-text"]').textContent).toContain('second quote text')
    expect(text(el)).not.toContain('Loading the list')
  })

  it('wears no violet AI styling on the Material list badge', async () => {
    const el = await detail()
    const badge = Array.from(el.querySelectorAll('span')).find((s) => text(s).trim() === 'Material list')
    expect(badge).toBeTruthy()
    expect(badge!.className).not.toContain('violet')
  })

  it('does not show the sparkle (AI) icon on the Material list badge', async () => {
    const el = await detail()
    const badge = Array.from(el.querySelectorAll('span')).find((s) => text(s).trim() === 'Material list')
    expect(badge).toBeTruthy()
    expect(badge!.querySelector('svg')!.innerHTML).not.toBe(createElement(Sparkles).innerHTML)
  })
})

describe('QuoteDetail original file with the token', () => {
  const API = /\/api\/v1\/quotes\/q\d\/file/
  function authOf(call: unknown): string | null {
    return new Headers((call as [string, RequestInit])[1]?.headers).get('Authorization')
  }

  it('shows an image original from a blob fetched with the token, never an API address', async () => {
    URL.createObjectURL = vi.fn(() => 'blob:original-image')
    URL.revokeObjectURL = vi.fn()
    fetchMock.mockImplementation(async (url: string) => {
      const path = new URL(url, 'http://localhost').pathname
      if (path === '/api/v1/quotes/q1/file') return new Response('PNGBYTES', { status: 200, headers: { 'Content-Type': 'image/png' } })
      if (path === '/api/v1/quotes/q1') return jsonResponse({ ...QUOTE, original_filename: 'list.png', original_content_type: 'image/png' })
      return jsonResponse({ message: 'none' }, 404)
    })
    const el = await detail()
    tab(el, 'Original Upload').click()
    await flush(); await flush()
    await update(el, {})
    const img = q<HTMLImageElement>(el, 'img[alt="Original material list"]')
    expect(img.getAttribute('src')).toBe('blob:original-image')
    const call = fetchMock.mock.calls.find(([u]) => API.test(String(u)))
    expect(call, 'expected the original to be fetched').toBeDefined()
    expect(authOf(call)).toBe('Bearer test-token')
  })

  it('shows a pdf original from a blob, and Download Original fetches with the token', async () => {
    URL.createObjectURL = vi.fn(() => 'blob:original-pdf')
    URL.revokeObjectURL = vi.fn()
    fetchMock.mockImplementation(async (url: string) => {
      const path = new URL(url, 'http://localhost').pathname
      if (path === '/api/v1/quotes/q1/file') return new Response('%PDF', { status: 200, headers: { 'Content-Type': 'application/pdf' } })
      if (path === '/api/v1/quotes/q1') return jsonResponse({ ...QUOTE, original_filename: 'list.pdf', original_content_type: 'application/pdf' })
      return jsonResponse({ message: 'none' }, 404)
    })
    const saved: string[] = []
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      saved.push(`${this.getAttribute('href')}|${this.download}`)
    })
    try {
      const el = await detail()
      tab(el, 'Original Upload').click()
      await flush(); await flush()
      await update(el, {})
      expect(q<HTMLIFrameElement>(el, 'iframe[title="Original PDF"]').getAttribute('src')).toBe('blob:original-pdf')
      for (const a of Array.from(el.querySelectorAll('a'))) expect(a.getAttribute('href') ?? '').not.toMatch(/\/api\/v1\//)
      const link = Array.from(el.querySelectorAll('a')).find((a) => text(a).includes('Download Original')) as HTMLAnchorElement
      link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      await flush(); await flush()
      expect(saved).toContain('blob:original-pdf|list.pdf')
      for (const c of fetchMock.mock.calls.filter(([u]) => API.test(String(u)))) expect(authOf(c)).toBe('Bearer test-token')
    } finally {
      click.mockRestore()
    }
  })
})

describe('QuoteDetail original blob lifecycle', () => {
  const IMG = { ...QUOTE, original_filename: 'list.png', original_content_type: 'image/png' }
  let revoked: string[]
  let made: number

  function routes(fileGate?: Promise<void>) {
    fetchMock.mockImplementation(async (url: string) => {
      const path = new URL(url, 'http://localhost').pathname
      if (path === '/api/v1/quotes/q1/file' || path === '/api/v1/quotes/q2/file') {
        if (fileGate) await fileGate
        return new Response('PNGBYTES', { status: 200, headers: { 'Content-Type': 'image/png' } })
      }
      if (path === '/api/v1/quotes/q1') return jsonResponse(IMG)
      if (path === '/api/v1/quotes/q2') return jsonResponse({ ...IMG, id: 'q2', number: 'Q-0002' })
      return jsonResponse({ message: 'none' }, 404)
    })
  }
  beforeEach(() => {
    revoked = []
    made = 0
    URL.createObjectURL = vi.fn(() => `blob:original-${++made}`)
    URL.revokeObjectURL = vi.fn((u: string) => { revoked.push(u) })
  })

  it('revokes a url that answers after the page has gone', async () => {
    let open!: () => void
    routes(new Promise<void>((r) => { open = r }))
    const el = await detail()
    tab(el, 'Original Upload').click()
    await flush()
    el.remove()
    open()
    await flush(); await flush()
    expect(made).toBe(1)
    expect(revoked).toEqual(['blob:original-1'])
  })

  it('fetches the original once for overlapping asks', async () => {
    let open!: () => void
    routes(new Promise<void>((r) => { open = r }))
    const el = await detail()
    tab(el, 'Original Upload').click()
    await flush()
    const link = Array.from(el.querySelectorAll('a')).find((a) => text(a).includes('Download Original')) as HTMLAnchorElement
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    try {
      link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      open()
      await flush(); await flush()
    } finally {
      click.mockRestore()
    }
    expect(fetchMock.mock.calls.filter(([u]) => String(u).endsWith('/api/v1/quotes/q1/file'))).toHaveLength(1)
    expect(revoked).toEqual([])
  })

  it('releases the old quote\'s blob when the route moves to another quote', async () => {
    routes()
    const el = await detail()
    tab(el, 'Original Upload').click()
    await flush(); await flush()
    expect(made).toBe(1)
    await update(el, { routeId: 'q2' } as Partial<GableQuoteDetail>)
    await flush(); await flush()
    expect(revoked).toContain('blob:original-1')
  })
})

