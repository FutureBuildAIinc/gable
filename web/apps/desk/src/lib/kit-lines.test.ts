// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * A kit on a sales document is one kit line with a component line per part
 * (ADR 0005 sections 2.1 and 2.6). The grouping files each part under the kit
 * it belongs to, and the cost of a kit is the cost of its parts.
 */
import { describe, it, expect } from 'vitest'
import { groupKitLines, groupCostCents, isKitLine, isComponentLine, lineCostCents } from './kit-lines'
import type { QuoteLine } from '../types/quote'

function line(over: Partial<QuoteLine>): QuoteLine {
  return {
    id: 'l1',
    quote_id: 'q1',
    product_id: null,
    stock_uom: null,
    stock_quantity: null,
    tally: null,
    sku: 'SKU',
    description: 'A thing',
    customer_note: null,
    quantity: '1',
    uom: 'EA',
    price_uom: 'EA',
    uom_qty: '1',
    price_uom_qty: '1',
    unit_price_ten_thousandths: 0,
    line_total_cents: 0,
    unit_cost_ten_thousandths: 0,
    created_at: '2026-01-01T00:00:00Z',
    ...over,
  } as QuoteLine
}

describe('grouping the lines of a document', () => {
  it('files each part under the kit it belongs to', () => {
    const groups = groupKitLines([
      line({ id: 'kit', line_type: 'kit' }),
      line({ id: 'part-a', line_type: 'component', parent_line_id: 'kit' }),
      line({ id: 'part-b', line_type: 'component', parent_line_id: 'kit' }),
      line({ id: 'plain', line_type: 'product' }),
    ])

    expect(groups).toHaveLength(2)
    expect(groups[0].line.id).toBe('kit')
    expect(groups[0].components.map(c => c.id)).toEqual(['part-a', 'part-b'])
    expect(groups[1].line.id).toBe('plain')
    expect(groups[1].components).toEqual([])
  })

  it('keeps a part whose parent is missing, so no line is lost', () => {
    const groups = groupKitLines([
      line({ id: 'orphan', line_type: 'component', parent_line_id: 'gone' }),
    ])

    expect(groups).toHaveLength(1)
    expect(groups[0].line.id).toBe('orphan')
  })

  it('names the kit and the part kinds', () => {
    expect(isKitLine(line({ line_type: 'kit' }))).toBe(true)
    expect(isComponentLine(line({ line_type: 'component' }))).toBe(true)
    expect(isKitLine(line({ line_type: 'product' }))).toBe(false)
  })
})

describe('the cost of a kit', () => {
  it('is the cost of its parts, not the cost of the kit product', () => {
    const groups = groupKitLines([
      line({ id: 'kit', line_type: 'kit', unit_cost_ten_thousandths: 99000 }),
      line({
        id: 'part-a',
        line_type: 'component',
        parent_line_id: 'kit',
        quantity: '12',
        unit_cost_ten_thousandths: 51000,
      }),
      line({
        id: 'part-b',
        line_type: 'component',
        parent_line_id: 'kit',
        quantity: '2',
        unit_cost_ten_thousandths: 195000,
      }),
    ])

    // 12 x $5.10 = $61.20 and 2 x $19.50 = $39.00, so $100.20 of parts. The
    // kit row's own $99.00 cost is not counted: a kit is never stocked, so it
    // carries no cost of its own.
    expect(groupCostCents(groups[0])).toBe(10020)
    expect(lineCostCents(groups[0].components[0])).toBe(6120)
    expect(lineCostCents(groups[0].line)).toBe(0)
  })

  it('is the plain line cost for a line that is not a kit', () => {
    const groups = groupKitLines([
      line({ id: 'plain', line_type: 'product', quantity: '3', unit_cost_ten_thousandths: 25000 }),
    ])

    expect(groupCostCents(groups[0])).toBe(750)
  })
})
