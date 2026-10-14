// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The inventory list shows a picture column only while a product has a picture, and a thumb per picture. */
import { describe, it, expect } from 'vitest'
import './InventoryTable'
import type { GableInventoryTable } from './InventoryTable'
import { mount } from '../../test/dom'

function row(id: string, sku: string, imageUrl: string | null) {
  return {
    id, sku, description: sku, stock_uom: 'PCS', base_price_ten_thousandths: 1000, average_unit_cost_ten_thousandths: 500,
    target_margin: 30, commission_rate: 0, vendor: null, upc: null, available: '10', image_url: imageUrl,
  }
}

describe('inventory list pictures', () => {
  it('starts each row with a thumb for a product with a picture and none without', async () => {
    const products = [row('a', 'A-1', '/catalog-illustrations/a.svg'), row('b', 'B-1', null)]
    const el = await mount<GableInventoryTable>('gable-inventory-table', { products: products as never })
    const rows = Array.from(el.querySelectorAll('tbody tr'))
    expect(rows.length).toBe(2)
    expect(rows[0].querySelector('td:first-child gable-product-thumb img')?.getAttribute('src')).toBe('/catalog-illustrations/a.svg')
    expect(rows[1].querySelector('td:first-child gable-product-thumb img')).toBeNull()
    expect(el.querySelectorAll('thead th').length).toBe(rows[0].querySelectorAll('td').length)
  })

  it('adds no column when no product has a picture', async () => {
    const el = await mount<GableInventoryTable>('gable-inventory-table', { products: [row('b', 'B-1', null)] as never })
    expect(el.querySelector('gable-product-thumb')).toBeNull()
    expect(el.querySelectorAll('thead th').length).toBe(9)
  })
})
