// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The pricing matrix page on the converted wire: a rule saved from the drawer is a PUT on the
 * revision the matrix loaded with only the value fields, an override of an inherited rule is a POST
 * that names the cell's own category and tier, a delete names the revision, bulk apply replaces a
 * direct rule by id, and a 409 stale_revision reloads the matrix with a toast.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './PricingMatrix'
import type { GablePricingMatrix } from './PricingMatrix'
import { ToastService } from '../../../lib/toast-service'
import { mount, update, flush, q, text, jsonResponse } from '../../../test/dom'

const RULE = {
  id: 'r-1',
  target_type: 'tier',
  customer_id: null,
  tier: 'GOLD',
  category_id: 'cat-1',
  rule_type: 'markdown',
  value_ten_thousandths: null,
  value_pct: '15.0000',
  margin_floor_pct: null,
  starts_at: null,
  expires_at: null,
  is_active: true,
  priority: 2,
  created_by: 'system',
  revision: 6,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  category_name: 'Framing',
  category_path: 'lumber.framing',
}

const CATEGORIES = [
  { id: 'cat-1', name: 'Framing', slug: 'framing', path: 'lumber.framing', parent_id: null, sort_order: 0, is_active: true, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' },
]

function matrix(cells: unknown[]) {
  return { categories: CATEGORIES, tiers: ['GOLD', 'SILVER'], cells }
}

const DIRECT = { category_id: 'cat-1', category_name: 'Framing', category_path: 'lumber.framing', tier: 'GOLD', rule: RULE, inherited: false }
const INHERITED = { category_id: 'cat-1', category_name: 'Framing', category_path: 'lumber.framing', tier: 'SILVER', rule: { ...RULE, id: 'r-parent', revision: 2, category_id: 'cat-0' }, inherited: true, source_path: 'lumber' }

let fetchMock: ReturnType<typeof vi.fn>
let calls: { method: string; url: URL; ifMatch: string | null; body: unknown }[]
let respond: (method: string, url: URL) => Response

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  calls = []
  respond = (method, url) => {
    if (url.pathname.endsWith('/audit')) return jsonResponse({ items: [], next_cursor: null, limit: 50 })
    return method === 'GET' ? jsonResponse(matrix([DIRECT, INHERITED])) : jsonResponse({})
  }
  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const method = init?.method ?? 'GET'
    const headers = init?.headers as Headers | undefined
    calls.push({ method, url, ifMatch: headers?.get('If-Match') ?? null, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    return respond(method, url)
  })
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

async function open(): Promise<GablePricingMatrix> {
  const el = await mount<GablePricingMatrix>('gable-pricing-matrix')
  await flush()
  await update(el, {})
  return el
}

/** The matrix cell for a tier column of the one category row (GOLD is the first tier cell). */
function cellAt(el: GablePricingMatrix, index: number): HTMLElement {
  const cells = el.querySelectorAll('gable-matrix-grid tbody tr td')
  return cells[index + 1] as HTMLElement
}

async function clickButton(el: Element, label: string, host: GablePricingMatrix) {
  const button = Array.from(el.querySelectorAll('button')).find((b) => (b.textContent ?? '').includes(label))
  if (!button) throw new Error(`no ${label} button`)
  button.click()
  await flush()
  await update(host, {})
}

async function setValue(host: GablePricingMatrix, value: string) {
  const input = q<HTMLInputElement>(host, 'gable-rule-drawer input[type="number"]')
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await flush()
}

describe('PricingMatrix', () => {
  it('renders the rule value from value_pct and reads the matrix with a plain GET', async () => {
    const el = await open()
    expect(calls[0].method).toBe('GET')
    expect(calls[0].url.pathname).toBe('/api/v1/pricing/matrix')
    expect(text(cellAt(el, 0))).toContain('-15%')
  })

  it('updates a direct rule with PUT, If-Match on the loaded revision, and only the value fields', async () => {
    const el = await open()
    cellAt(el, 0).click()
    await flush()
    await update(el, {})
    await setValue(el, '18.5')
    await clickButton(el, 'Update Rule', el)

    const put = calls.find((c) => c.method === 'PUT')
    if (!put) throw new Error('no PUT')
    expect(put.url.pathname).toBe('/api/v1/pricing/category-rules/r-1')
    expect(put.ifMatch).toBe('"6"')
    expect(put.body).toEqual({ rule_type: 'markdown', value_pct: '18.5', starts_at: null, expires_at: null, is_active: true, priority: 2 })
  })

  it('overriding an inherited rule creates a rule for the cell\'s own tier and category', async () => {
    const el = await open()
    cellAt(el, 1).click()
    await flush()
    await update(el, {})
    await setValue(el, '10')
    await clickButton(el, 'Create Rule', el)

    const post = calls.find((c) => c.method === 'POST')
    if (!post) throw new Error('no POST')
    expect(post.url.pathname).toBe('/api/v1/pricing/category-rules')
    expect(post.ifMatch).toBeNull()
    expect(post.body).toEqual({ rule_type: 'markdown', value_pct: '10', target_type: 'tier', tier: 'SILVER', category_id: 'cat-1', is_active: true })
  })

  it('bulk apply replaces a direct rule by id and creates the others', async () => {
    const el = await open()
    await clickButton(el, 'Bulk Edit', el)
    cellAt(el, 0).click()
    cellAt(el, 1).click()
    await update(el, {})
    const value = q<HTMLInputElement>(el, 'input[placeholder="Value"]')
    value.value = '5'
    value.dispatchEvent(new Event('input', { bubbles: true }))
    await update(el, {})
    await clickButton(el, 'Apply to Selected', el)

    const post = calls.find((c) => c.method === 'POST' && c.url.pathname.endsWith('/bulk'))
    if (!post) throw new Error('no bulk POST')
    const items = post.body as Record<string, unknown>[]
    expect(items).toHaveLength(2)
    expect(items.find((i) => i.tier === 'GOLD')?.id).toBe('r-1')
    expect(items.find((i) => i.tier === 'SILVER')).not.toHaveProperty('id')
    expect(items[0]).toMatchObject({ target_type: 'tier', rule_type: 'markdown', value_pct: '5' })
  })

  it('deletes with the loaded revision', async () => {
    const el = await open()
    cellAt(el, 0).click()
    await flush()
    await update(el, {})
    await clickButton(el, 'Delete this rule', el)
    await clickButton(el, 'Confirm Delete', el)

    const del = calls.find((c) => c.method === 'DELETE')
    if (!del) throw new Error('no DELETE')
    expect(del.url.pathname).toBe('/api/v1/pricing/category-rules/r-1')
    expect(del.ifMatch).toBe('"6"')
  })

  it('on a 409 stale_revision toasts the server message and reloads the matrix', async () => {
    const shown: string[] = []
    vi.spyOn(ToastService, 'show').mockImplementation((message: string) => { shown.push(message) })
    respond = (method, url) => {
      if (method === 'PUT') {
        return jsonResponse({ error: { code: 'stale_revision', message: 'the rule changed since it was loaded', details: [] }, meta: { request_id: 'r' } }, 409)
      }
      if (url.pathname.endsWith('/audit')) return jsonResponse({ items: [], next_cursor: null, limit: 50 })
      return jsonResponse(matrix([DIRECT, INHERITED]))
    }
    const el = await open()
    cellAt(el, 0).click()
    await flush()
    await update(el, {})
    await setValue(el, '12')
    const before = calls.filter((c) => c.method === 'GET').length
    await clickButton(el, 'Update Rule', el)
    await flush()

    expect(shown.some((m) => m.includes('the rule changed since it was loaded'))).toBe(true)
    expect(calls.filter((c) => c.method === 'GET').length).toBe(before + 1)
    expect(el.querySelector('gable-rule-drawer')).toBeNull()
  })
})
