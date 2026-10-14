// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dashboard's recent orders: each row names the customer and links to the
 * order; it never prints a fragment of the order's id.
 */
import { describe, it, expect } from 'vitest'
import './RecentOrdersFeed'
import type { GableRecentOrdersFeed } from './RecentOrdersFeed'
import { mount, q, text } from '../../test/dom'

const ORDER_ID = 'f9d3cc06-6456-4a7a-b6ba-63d0a04f7a63'

describe('recent orders feed', () => {
  it('links each order and prints no id fragment', async () => {
    const el = await mount<GableRecentOrdersFeed>('gable-recent-orders-feed', {
      orders: [{ order_id: ORDER_ID, customer_name: 'Kelbrook Construction', total_amount: 186600, status: 'CONFIRMED', created_at: '2026-10-12T00:00:00Z' }],
    })
    expect(text(el)).toContain('Kelbrook Construction')
    expect(el.querySelector(`a[href="/orders/${ORDER_ID}"]`)).not.toBeNull()
    expect(text(el)).not.toContain('f9d3cc06')
  })

  it('names each row for a screen reader', async () => {
    const el = await mount<GableRecentOrdersFeed>('gable-recent-orders-feed', {
      orders: [{ order_id: ORDER_ID, customer_name: 'Kelbrook Construction', total_amount: 186600, status: 'CONFIRMED', created_at: '2026-10-12T00:00:00Z' }],
    })
    const link = q<HTMLAnchorElement>(el, `a[href="/orders/${ORDER_ID}"]`)
    expect(link.getAttribute('aria-label')).toBe('Open the order for Kelbrook Construction')
  })
})
