// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dashboard's recent orders: each row names the customer and links to the
 * order; it never prints a fragment of the order's id. Two rows for one
 * customer get distinct names (the status and the total ride along).
 */
import { describe, it, expect } from 'vitest'
import './RecentOrdersFeed'
import type { GableRecentOrdersFeed } from './RecentOrdersFeed'
import { mount, q, text } from '../../test/dom'

const ORDER_ID = 'f9d3cc06-6456-4a7a-b6ba-63d0a04f7a63'
const NEUTRAL_AT = '2000-01-01T00:00:00Z'

describe('recent orders feed', () => {
  it('links each order and prints no id fragment', async () => {
    const el = await mount<GableRecentOrdersFeed>('gable-recent-orders-feed', {
      orders: [{ order_id: ORDER_ID, customer_name: 'Tarnwick Construction', total_amount: 186600, status: 'CONFIRMED', created_at: NEUTRAL_AT }],
    })
    expect(text(el)).toContain('Tarnwick Construction')
    expect(el.querySelector(`a[href="/orders/${ORDER_ID}"]`)).not.toBeNull()
    expect(text(el)).not.toContain('f9d3cc06')
  })

  it('names each row for a screen reader without hiding its content', async () => {
    const el = await mount<GableRecentOrdersFeed>('gable-recent-orders-feed', {
      orders: [{ order_id: ORDER_ID, customer_name: 'Tarnwick Construction', total_amount: 186600, status: 'CONFIRMED', created_at: NEUTRAL_AT }],
    })
    const link = q<HTMLAnchorElement>(el, `a[href="/orders/${ORDER_ID}"]`)
    expect(link.getAttribute('aria-label')).toBeNull()
    expect(text(link)).toContain('Open the order for Tarnwick Construction')
    expect(text(link)).toContain('CONFIRMED')
  })

  it('gives two orders of one customer distinct names', async () => {
    const el = await mount<GableRecentOrdersFeed>('gable-recent-orders-feed', {
      orders: [
        { order_id: ORDER_ID, customer_name: 'Tarnwick Construction', total_amount: 186600, status: 'CONFIRMED', created_at: NEUTRAL_AT },
        { order_id: 'a1b2c3d4-6456-4a7a-b6ba-63d0a04f7a63', customer_name: 'Tarnwick Construction', total_amount: 4200, status: 'COMPLETED', created_at: NEUTRAL_AT },
      ],
    })
    const links = Array.from(el.querySelectorAll<HTMLAnchorElement>('a[href^="/orders/"]'))
    expect(links.length).toBe(2)
    expect(text(links[0])).not.toBe(text(links[1]))
    expect(text(links[0])).toContain('$1,866.00')
    expect(text(links[1])).toContain('$42.00')
  })
})
