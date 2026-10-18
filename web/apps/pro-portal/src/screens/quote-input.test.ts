// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest'
import { bpsToPercent, percentToBps, quoteToInput } from './quote-input.ts'
import { customerQuoteFixture, quoteIds } from '../test/customer-quote-fixtures.ts'

describe('customer quote input mapping', () => {
  it('preserves all three markup tiers, labour, overhead, and terms without cost input', () => {
    const input = quoteToInput(customerQuoteFixture)
    expect(input.markup).toEqual({ kind: 'markup', bps: 1800 })
    expect(input.category_rules?.[0]?.markup).toEqual({ kind: 'markup', bps: 1500 })
    expect(input.lines?.[1]?.markup).toEqual({ kind: 'margin', bps: 2000 })
    expect(input.lines?.[1]?.group_key).toBe(quoteIds.group)
    expect(input.labour?.[0]?.rate_cents).toBe(300000)
    expect(input.overhead?.[0]?.value).toBe(500)
    expect(input.tax).toEqual({ mode: 'site_rate', rate_bps: 1000 })
    const wire = JSON.stringify(input)
    expect(wire).not.toContain('cost_cents')
    expect(wire).not.toContain('sell_cents')
    expect(wire).not.toContain('unit_cost_ten_thousandths')
  })

  it('converts percentages to basis points at the input boundary', () => {
    expect(percentToBps('18.25')).toBe(1825)
    expect(bpsToPercent(1825)).toBe('18.25')
    expect(percentToBps('')).toBe(0)
  })
})
