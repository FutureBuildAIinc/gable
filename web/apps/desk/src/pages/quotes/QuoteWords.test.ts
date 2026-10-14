// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * A quote read from a material list is called a Material list wherever it is shown:
 * the stored source `ai` cannot tell the rule based reader from the AI one.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './QuoteList'
import './QuoteAnalytics'
import { mount, flush, update, text, jsonResponse } from '../../test/dom'
import { QuoteService } from '../../services/QuoteService'

beforeEach(() => { localStorage.setItem('token', 'test-token') })
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('Material list words outside the quote', () => {
  it('the quote list Source column reads Material list for a list quote', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const path = new URL(url, 'http://localhost').pathname
      if (path === '/api/v1/quotes') {
        return jsonResponse({
          items: [{
            id: 'q1', number: 'Q-0001', status: 'draft', revision: 1, source: 'ai', customer_id: 'c0000000-0000-4000-8000-000000000001',
            customer_name: 'Test Builder Co', created_at: '2026-01-01T00:00:00Z', delivery_type: 'pickup', total_cents: 0, lines: [],
          }],
          next_cursor: null,
        })
      }
      return jsonResponse({ items: [], next_cursor: null })
    }))
    const el = await mount<HTMLElement>('gable-quote-list')
    await flush(); await flush()
    await update(el, {})
    expect(text(el)).toContain('Material list')
    expect(text(el)).not.toMatch(/\bAI\b/)
  })

  it('the analytics card reads Material list vs manual and Material list quotes', async () => {
    vi.spyOn(QuoteService, 'analytics').mockResolvedValue({
      total_quotes: 4, draft_count: 1, sent_count: 1, accepted_count: 1, rejected_count: 1, expired_count: 0,
      conversion_rate: 25, avg_days_to_close: 2, avg_margin_accepted_cents: 0, avg_margin_rejected_cents: 0,
      total_quote_value_cents: 0, total_accepted_value_cents: 0, ai_sourced_count: 2, ai_conversion_rate: 50,
      manual_conversion_rate: 0, trend_data: [],
    } as never)
    const el = await mount<HTMLElement>('gable-quote-analytics')
    await flush(); await flush()
    await update(el, {})
    expect(text(el)).toContain('Material list vs manual')
    expect(text(el)).toContain('Material list quotes')
    expect(text(el)).not.toMatch(/AI vs Manual|AI-Parsed/i)
  })
})
