// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The Pile's row formatting: target margin and commission are stored as zero when
 * not set, and the old code rendered "0.0%" and "0.0% Comm": a page full of
 * zeros that read like every product had been configured to default the demo
 * margin. The fix is two parts:
 *
 *  1. When the margin or commission is exactly zero, render "Not set" in the
 *     muted tone instead of a numeric percentage.
 *  2. Drop the "ID: xxxxxxxx" line under each description. The SKU is the
 *     product's stable identifier; a second line of UUID fragment is noise on
 *     a list view that already has the SKU cell on the same row.
 *
 * A set value still formats to one decimal.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './InventoryTable'
import type { GableInventoryTable } from './InventoryTable'
import { mount, text as textOf } from '../../test/dom'

function product(overrides: Record<string, unknown> = {}) {
  return {
    id: '00000000-0000-4000-8000-000000000001',
    sku: 'LUM-2X4-08',
    description: '2x4x8 SPF Premium',
    stock_uom: 'PCS',
    base_price_ten_thousandths: 79900,
    average_unit_cost_ten_thousandths: 50000,
    target_margin: 30,
    commission_rate: 5,
    vendor: 'Acme Lumber',
    vendor_id: null,
    upc: null,
    weight_lbs: 10,
    length_in: 96,
    width_in: 3.5,
    height_in: 1.5,
    stackable: true,
    geometry_source: 'parametric',
    lead_time_days: 7,
    reorder_point: '40',
    reorder_qty: '200',
    on_hand: '500',
    allocated: '0',
    available: '500',
    revision: 7,
    sale_uom: 'PCS',
    price_uom: 'PCS',
    purchase_uom: 'PCS',
    board_thickness_in: null,
    board_width_in: null,
    board_length_ft: null,
    random_length: false,
    units: [],
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('inventory pile target margin and commission', () => {
  it('renders the margin and commission at one decimal when set', async () => {
    const el = await mount<GableInventoryTable>('gable-inventory-table', {
      products: [product({ target_margin: 33, commission_rate: 4.5 })],
    })
    expect(textOf(el)).toContain('33.0%')
    expect(textOf(el)).toContain('4.5% Comm')
  })

  it('shows "Not set" in the muted tone for an unset target margin', async () => {
    const el = await mount<GableInventoryTable>('gable-inventory-table', {
      products: [product({ target_margin: 0, commission_rate: 5 })],
    })
    expect(textOf(el)).not.toContain('0.0%')
    const notSetNode = Array.from(el.querySelectorAll('span')).find((span) =>
      textOf(span) === 'Not set',
    )
    expect(notSetNode, 'expected a span containing "Not set"').toBeDefined()
    expect(notSetNode!.className).toContain('text-zinc-500')
  })

  it('shows "Not set" for an unset commission, alongside a set margin', async () => {
    const el = await mount<GableInventoryTable>('gable-inventory-table', {
      products: [product({ target_margin: 33, commission_rate: 0 })],
    })
    expect(textOf(el)).toContain('33.0%')
    expect(textOf(el)).not.toContain('0.0% Comm')
    expect(textOf(el)).toContain('Not set')
  })

  it('shows "Not set" for both an unset margin and an unset commission', async () => {
    const el = await mount<GableInventoryTable>('gable-inventory-table', {
      products: [product({ target_margin: 0, commission_rate: 0 })],
    })
    expect(textOf(el)).not.toContain('0.0%')
    expect(textOf(el)).not.toContain('% Comm')
    // The set-margin row above leaves one "Not set" per cell, this row two.
    const notAll = textOf(el).match(/Not set/g) ?? []
    expect(notAll.length).toBeGreaterThanOrEqual(2)
  })
})

describe('inventory pile row identity', () => {
  it('does not render the "ID: ..." line under each description', async () => {
    const el = await mount<GableInventoryTable>('gable-inventory-table', {
      products: [product()],
    })
    expect(textOf(el)).not.toMatch(/ID:\s+[0-9a-f]{8}/)
  })

  it('still renders the SKU as the row identifier', async () => {
    const el = await mount<GableInventoryTable>('gable-inventory-table', {
      products: [product({ sku: 'LUM-2X4-08' })],
    })
    expect(textOf(el)).toContain('LUM-2X4-08')
  })
})