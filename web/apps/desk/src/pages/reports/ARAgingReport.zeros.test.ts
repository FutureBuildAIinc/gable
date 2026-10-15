// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * AR Aging draws a zero quietly (the muted label text) and colours only an
 * amount that is there: no yellow, orange or red $0.00 (G1 walk note N16).
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './ARAgingReport'
import { mountAsync, jsonResponse } from '../../test/dom'

const flush = () => new Promise(r => setTimeout(r, 0))

const ROW = {
  customer_id: '00000000-0000-4000-8000-0000000000c1', customer_name: 'Ridgeview Framing', job_id: null, job_name: null,
  ship_to_id: null, ship_to_code: null, currency: 'USD',
  current_cents: 12345, days_1_30_cents: 0, days_31_60_cents: 0, days_61_90_cents: 0, over_90_cents: 0,
  unapplied_cents: 0, total_cents: 12345,
}
const TOTAL = {
  currency: 'USD', current_cents: 12345, days_1_30_cents: 0, days_31_60_cents: 0, days_61_90_cents: 0, over_90_cents: 0,
  unapplied_cents: 0, total_cents: 12345,
}
const AMOUNT = /^-?\$[\d,]+\.\d{2}$/

describe('AR aging zeros are quiet', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('draws a zero amount in the muted class and an amount that is there in its colour', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/ar/aging/summary')) return jsonResponse({ as_of: '2031-01-01', basis: 'due_date', totals: [TOTAL] })
      return jsonResponse({ items: [ROW], next_cursor: null })
    }))
    const el = await mountAsync('gable-ar-aging-report')
    await flush()
    await flush()
    await (el as unknown as { updateComplete: Promise<unknown> }).updateComplete

    const cells = Array.from(el.querySelectorAll('td.font-mono, p.font-mono')).filter(c => AMOUNT.test((c.textContent || '').trim()))
    expect(cells.length).toBeGreaterThan(0)
    const colour = /text-(yellow|amber|orange|red|rose|emerald|teal)-/
    const zeros = cells.filter(c => (c.textContent || '').trim() === '$0.00')
    const there = cells.filter(c => (c.textContent || '').trim() === '$123.45')
    expect(zeros.length).toBeGreaterThan(0)
    expect(there.length).toBeGreaterThan(0)
    for (const z of zeros) {
      expect(z.className, 'a zero is drawn in the muted text').toContain('text-zinc-500')
      expect(z.className, 'a zero carries no warning colour').not.toMatch(colour)
    }
    for (const a of there) {
      expect(a.className, 'an amount that is there keeps its colour').not.toContain('text-zinc-500')
    }
  })
})
