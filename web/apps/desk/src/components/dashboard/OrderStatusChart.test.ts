// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dashboard's order ring: its centre counts every order the ring draws and
 * says "Orders"; it never claims they are "Active" (the Active Orders card
 * counts only the orders in processing states).
 */
import { describe, it, expect, vi } from 'vitest'

vi.mock('chart.js/auto', () => ({
  default: class {
    destroy() {}
  },
}))

import './OrderStatusChart'
import type { GableOrderStatusChart } from './OrderStatusChart'
import { mount, text } from '../../test/dom'

describe('order status ring', () => {
  it('reads Orders and the figure is the sum of the slices', async () => {
    const el = await mount<GableOrderStatusChart>('gable-order-status-chart', {
      statusBreakdown: { confirmed: 1000, draft: 30, fulfilled: 12, cancelled: 0 },
    })
    await new Promise(resolve => requestAnimationFrame(() => resolve(null)))
    await el.updateComplete
    const centre = text(el)
    expect(centre).toContain('1042')
    expect(centre).toContain('Orders')
    expect(centre).not.toContain('Active')
  })
})
