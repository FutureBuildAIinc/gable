// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect, afterEach } from 'vitest'
import './InventoryAlertsWidget'
import type { GableInventoryAlertsWidget } from './InventoryAlertsWidget'
import { mount, text } from '../../test/dom'

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
