// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The market indices screen (QE-07-b1): the CSV import previews what a file
 * would do before it is applied, and every row's fate is named.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './MarketIndices'
import type { MarketIndicesPage } from './MarketIndices'
import { mount, update, flush, q, text, jsonResponse } from '../../test/dom'

const INDICES = [
  { id: 'idx-1', name: 'Random Lengths SYP', index_code: 'RLS', commodity_kind: 'LUMBER',
    current_value: 100, unit: 'MBF', is_active: true },
]

const PREVIEW = {
  rows: [
    { line: 2, index_code: 'RLS', index_id: 'idx-1', index_name: 'Random Lengths SYP', value: '120',
      as_of_date: '2026-01-06', source: 'MANUAL', status: 'accepted' },
    { line: 3, index_code: 'NOPE', value: '5', as_of_date: '2026-01-06', source: 'MANUAL',
      status: 'refused', reason: 'unknown index code' },
  ],
  accepted_count: 1,
  unchanged_count: 0,
  refused_count: 1,
  indices_affected: 1,
}

let calls: string[]
let previewAnswer: unknown

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  calls = []
  previewAnswer = PREVIEW
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push(`${(init?.method ?? 'GET')} ${url}`)
    if (url.includes('/import/preview')) return jsonResponse(previewAnswer)
    if (url.includes('/import/apply')) {
      return jsonResponse({ rows: PREVIEW.rows, applied_count: 1, changed_indices: 1, scans: 1,
        unchanged_count: 0, refused_count: 1 })
    }
    if (url.includes('/staleness')) {
      return jsonResponse({ threshold_days: 7, stale: false, stale_indices: [], indices: [] })
    }
    if (url.includes('/market-indices')) return jsonResponse(INDICES)
    return jsonResponse({})
  }))
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

async function open(): Promise<MarketIndicesPage> {
  const el = await mount<MarketIndicesPage>('gable-market-indices')
  await flush()
  await update(el, {})
  return el
}

/** Choose the CSV file the way the file input does. */
function chooseFile(el: MarketIndicesPage, name = 'indices.csv', body = 'index_code,value,as_of_date\n') {
  const input = q<HTMLInputElement>(el, '[data-testid="import-file"]')
  const file = new File([body], name, { type: 'text/csv' })
  Object.defineProperty(input, 'files', { value: [file], configurable: true })
  input.dispatchEvent(new Event('change'))
}

async function click(el: MarketIndicesPage, selector: string) {
  q<HTMLButtonElement>(el, selector).click()
  await flush()
  await update(el, {})
  await flush()
  await update(el, {})
}

describe('market indices import', () => {
  it('previews the file and names every row fate', async () => {
    const el = await open()
    chooseFile(el)
    await update(el, {})
    await click(el, '[data-testid="import-preview"]')
    expect(calls.some((c) => c.startsWith('POST') && c.includes('/import/preview'))).toBe(true)
    expect(text(q(el, '[data-testid="import-summary"]'))).toContain('1 row(s) will be written')
    const rows = el.querySelectorAll('[data-testid="import-row"]')
    expect(rows.length).toBe(2)
    expect(text(el)).toContain('unknown index code')
  })

  it('applies the file and reports what it did', async () => {
    const el = await open()
    chooseFile(el)
    await update(el, {})
    await click(el, '[data-testid="import-preview"]')
    await click(el, '[data-testid="import-apply"]')
    expect(calls.some((c) => c.startsWith('POST') && c.includes('/import/apply'))).toBe(true)
    expect(text(q(el, '[data-testid="import-result"]'))).toContain('1 row(s) written')
    expect(text(q(el, '[data-testid="import-result"]'))).toContain('1 exposure scan(s) ran')
  })

  it('keeps Apply disabled until a preview has something to write', async () => {
    const el = await open()
    expect(q<HTMLButtonElement>(el, '[data-testid="import-apply"]').disabled).toBe(true)
    chooseFile(el)
    await update(el, {})
    // no preview yet: still nothing to apply
    expect(q<HTMLButtonElement>(el, '[data-testid="import-apply"]').disabled).toBe(true)
    await click(el, '[data-testid="import-preview"]')
    expect(q<HTMLButtonElement>(el, '[data-testid="import-apply"]').disabled).toBe(false)
  })

  it('shows the staleness banner on the screen', async () => {
    const el = await open()
    expect(el.querySelector('gable-index-staleness-banner')).not.toBeNull()
  })
})
