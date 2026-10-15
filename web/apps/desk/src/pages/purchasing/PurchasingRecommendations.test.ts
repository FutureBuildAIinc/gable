// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The purchasing recommendations page: the route answers `items: null` when
 * nothing needs reordering, and the page must read that as an empty list.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './PurchasingRecommendations'
import type { LitElement } from 'lit'
import { mountAsync, text, jsonResponse } from '../../test/dom'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('purchasing recommendations', () => {
  it('reads items: null as no recommendations, never a crash', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({
      total_items: 0, critical_count: 0, high_count: 0, medium_count: 0, low_count: 0,
      total_estimated_cost: 0, items: null,
    })))
    const el = await mountAsync<LitElement>('gable-purchasing-recommendations')
    expect(text(el)).toContain('No recommendations found')
  })

  it('names the yard on each row: a recommendation is one product at one yard', async () => {
    const item = (branch_id: string, branch_name: string, stock: number) => ({
      branch_id, branch_name, product_id: 'p1', product_sku: 'HANGER-28', product_name: 'Joist Hanger 2x8',
      current_stock: stock, avg_daily_sales: 2, std_dev_sales: 0.6, lead_time_days: 7, reorder_point: 20,
      safety_stock: 4, suggested_qty: 40, estimated_cost: 100, urgency: 'HIGH', days_until_out: 5,
    })
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({
      total_items: 2, critical_count: 0, high_count: 2, medium_count: 0, low_count: 0,
      total_estimated_cost: 200, items: [item('y1', 'Lake Country Outpost', 10), item('y2', 'West Kelowna Satellite', 12)],
    })))
    const el = await mountAsync<LitElement>('gable-purchasing-recommendations')
    const yards = Array.from(el.querySelectorAll('[data-testid="rec-yard"]')).map((n) => (n.textContent ?? '').trim())
    expect(yards).toEqual(['Lake Country Outpost', 'West Kelowna Satellite'])
  })
})
