// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dashboard's money card is cash collected (FROM-PRINCIPAL item 144): the
 * word "revenue" is the sales on account's, which read as AR. The Daily Till
 * the card opens says "collected" too and prints a negative net as "-$4.00".
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './Dashboard'
import './DailyTill'
import { mountAsync, text } from '../test/dom'

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('the dashboard card', () => {
  beforeEach(() => {
    localStorage.setItem('token', 'test-token')
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.includes('/dashboard/summary')) {
        return json({ today_revenue: 123400, today_revenue_change: 10, active_orders: 1, pending_dispatch: 0, outstanding_ar: 0, outstanding_ar_count: 0 })
      }
      if (url.includes('/revenue-trend')) return json([])
      if (url.includes('/order-activity')) return json({ recent_orders: [], status_breakdown: {} })
      return json([])
    }))
  })

  it('reads Collected today, not Today\'s Revenue', async () => {
    const el = await mountAsync<HTMLElement>('gable-dashboard')
    const body = text(el)
    expect(body).toContain('Collected today')
    expect(body).not.toContain("Today's Revenue")
  })

  it('names the trend as collected', async () => {
    const el = await mountAsync<HTMLElement>('gable-dashboard')
    expect(text(el)).toContain('Collected, last 7 days')
    expect(text(el)).not.toContain('Revenue Trend')
  })
})

describe('the Daily Till', () => {
  function stubTill(till: Record<string, unknown>) {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.includes('/daily-till')) return json(till)
      if (url.includes('/sales-summary')) {
        return json({ start_date: '2026-09-15', end_date: '2026-10-14', total_invoiced: 0, total_collected: 0, outstanding_ar: 0, invoice_count: 0 })
      }
      return json([])
    }))
  }

  it('says collected and shows received, refunds and the net', async () => {
    stubTill({ date: '2026-10-14', total_collected: 36, received: 40, refunds: 4, net_collected: 36, by_method: { CASH: 40 }, transaction_count: 1 })
    const el = await mountAsync<HTMLElement>('gable-daily-till')
    const parts = text(el.querySelector('[data-testid="till-parts"]'))
    expect(parts).toContain('Received $40.00')
    expect(parts).toContain('refunds $4.00')
    expect(parts).toContain('net $36.00')
    expect(text(el)).toContain('Collected (2026-10-14)')
  })

  it('prints a refund only day as -$4.00, never $-4.00', async () => {
    stubTill({ date: '2026-10-14', total_collected: -4, received: 0, refunds: 4, net_collected: -4, by_method: {}, transaction_count: 0 })
    const el = await mountAsync<HTMLElement>('gable-daily-till')
    const body = text(el)
    expect(body).toContain('-$4.00')
    expect(body).not.toContain('$-4.00')
  })
})
