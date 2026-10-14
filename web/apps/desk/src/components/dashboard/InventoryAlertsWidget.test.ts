// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dashboard's inventory alerts: each row opens that product's stock at
 * the yard and is a real link named for a screen reader.
 */
import { describe, it, expect } from 'vitest'
import './InventoryAlertsWidget.ts'
import type { GableInventoryAlertsWidget } from './InventoryAlertsWidget.ts'
import { mount, q, text } from '../../test/dom'

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
