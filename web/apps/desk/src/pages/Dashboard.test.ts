// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The manager's dashboard drills in: each KPI card is a link to its own
 * list at the yard the dashboard shows. Today's Revenue opens the day's
 * till, Active Orders the orders list, Pending Dispatch the dispatch
 * board, Outstanding AR the AR Aging page.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('chart.js/auto', () => ({
  default: class {
    destroy() {}
  },
}))

import './Dashboard.ts'
import type { GableDashboard } from './Dashboard.ts'
import { mountAsync, q, jsonResponse } from '../test/dom'

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.includes('/dashboard/summary')) {
      return jsonResponse({
        today_revenue: 313494,
        today_revenue_change: 0,
        active_orders: 3,
        pending_dispatch: 2,
        outstanding_ar: 6898394,
        outstanding_ar_count: 6,
      })
    }
    if (url.includes('/dashboard/inventory-alerts')) return jsonResponse([])
    if (url.includes('/dashboard/top-customers')) return jsonResponse([])
    if (url.includes('/dashboard/order-activity')) return jsonResponse({ recent_orders: [], status_breakdown: {} })
    if (url.includes('/dashboard/revenue-trend')) return jsonResponse([])
    return jsonResponse([])
  }))
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('the dashboard drills in', () => {
  // A card's accessible name keeps its visible content (the title and the
  // figure); the action rides along as visually hidden text, not an
  // aria-label that would hide the figure.
  it("Today's Revenue opens the day's till", async () => {
    const el = await mountAsync<GableDashboard>('gable-dashboard')
    const link = q<HTMLAnchorElement>(el, 'a[href="/reports/daily-till"]')
    expect(link.getAttribute('aria-label')).toBeNull()
    expect(link.textContent).toContain("Open today's sales")
    expect(link.textContent).toContain('$3,134.94')
  })

  it('Active Orders opens the orders list on its Confirmed tab', async () => {
    const el = await mountAsync<GableDashboard>('gable-dashboard')
    const link = q<HTMLAnchorElement>(el, 'a[href="/orders?status=confirmed"]')
    expect(link.getAttribute('aria-label')).toBeNull()
    expect(link.textContent).toContain('Active Orders')
    expect(link.textContent).toContain('Open the active orders')
  })

  it('Pending Dispatch opens the dispatch board', async () => {
    const el = await mountAsync<GableDashboard>('gable-dashboard')
    const link = q<HTMLAnchorElement>(el, 'a[href="/dispatch"]')
    expect(link.getAttribute('aria-label')).toBeNull()
    expect(link.textContent).toContain('Open the deliveries waiting for dispatch')
  })

  it('Outstanding AR opens the AR Aging page', async () => {
    const el = await mountAsync<GableDashboard>('gable-dashboard')
    const link = q<HTMLAnchorElement>(el, 'a[href="/reports/ar-aging"]')
    expect(link.getAttribute('aria-label')).toBeNull()
    expect(link.textContent).toContain('Open AR Aging')
  })
})
