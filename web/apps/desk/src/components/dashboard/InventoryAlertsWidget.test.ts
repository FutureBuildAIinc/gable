// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dashboard's inventory alerts: each row names its yard, and opens that
 * product's stock at the yard as a real link named for a screen reader.
 */
import { describe, it, expect, afterEach } from 'vitest'
import './InventoryAlertsWidget'
import type { GableInventoryAlertsWidget } from './InventoryAlertsWidget'
import { mount, q, text } from '../../test/dom'

afterEach(() => { document.body.innerHTML = '' })

describe('Inventory alerts widget', () => {
  it('names the yard on each row, so two rows for one SKU can be told apart', async () => {
    const alerts = [
      { product_id: 'p', sku: 'HANGER-28', name: 'Joist Hanger 2x8', current_qty: 25, reorder_qty: 150, alert_type: 'LOW_STOCK', location_id: 'a', yard_name: 'Lake Country Outpost' },
      { product_id: 'p', sku: 'HANGER-28', name: 'Joist Hanger 2x8', current_qty: 130, reorder_qty: 150, alert_type: 'LOW_STOCK', location_id: 'b', yard_name: 'West Kelowna Satellite' },
    ] as unknown as GableInventoryAlertsWidget['alerts']
    const el = await mount<GableInventoryAlertsWidget>('gable-inventory-alerts-widget', { alerts })
    const rows = Array.from(el.querySelectorAll('[data-testid="alert-yard"]')).map((r) => text(r))
    expect(rows).toEqual(['Lake Country Outpost', 'West Kelowna Satellite'])
  })
})

describe('inventory alerts widget', () => {
  it("links each row to the product's stock, keeping the figures in the name and adding the action", async () => {
    const el = await mount<GableInventoryAlertsWidget>('gable-inventory-alerts-widget', {
      alerts: [
        { product_id: 'prod-1', sku: 'HANGER-26', name: 'Joist Hanger 2x6', current_qty: 4, reorder_qty: 10, alert_type: 'LOW_STOCK' },
      ],
    })

    const link = q<HTMLAnchorElement>(el, 'a[href="/inventory/prod-1"]')
    expect(link.getAttribute('aria-label')).toBeNull()
    expect(text(link)).toContain('Open the stock for Joist Hanger 2x6')
    expect(text(link)).toContain('HANGER-26')
    expect(text(link)).toContain('Low Stock')
  })
})
