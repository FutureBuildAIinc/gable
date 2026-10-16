// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * `formatDollars`: money rendering for endpoints that already answer in float
 * dollars (the purchase order wire). The shared `formatCents` reads int64 cents;
 * the PO wire has not been migrated to that, so this helper sits beside it
 * rather than replacing it. Both helpers share the same rule: thousands
 * separators, exactly two decimal places, a leading "$", never bare digits.
 */
import { describe, it, expect } from 'vitest'
import { formatDollars } from './utils'

describe('formatDollars', () => {
  it('renders a small amount with two decimal places', () => {
    expect(formatDollars(12.5)).toBe('$12.50')
  })

  it('groups thousands on a six-figure amount', () => {
    expect(formatDollars(123456.78)).toBe('$123,456.78')
  })

  it('renders zero as $0.00, not as a dash or empty string', () => {
    expect(formatDollars(0)).toBe('$0.00')
  })

  it('renders a negative amount with a leading minus sign outside the symbol', () => {
    expect(formatDollars(-73.88)).toBe('-$73.88')
  })

  it('carries a fraction that rounds up into the next dollar', () => {
    expect(formatDollars(9.996)).toBe('$10.00')
    expect(formatDollars(12.9999)).toBe('$13.00')
    expect(formatDollars(1.0009 * 9.99)).toBe('$10.00')
  })

  it('never signs an amount that rounds to zero', () => {
    expect(formatDollars(-0.001)).toBe('$0.00')
  })

  it('treats null and undefined as zero', () => {
    expect(formatDollars(null)).toBe('$0.00')
    expect(formatDollars(undefined)).toBe('$0.00')
  })

  it('treats NaN as zero (the page never sends a NaN, but neither does it crash)', () => {
    expect(formatDollars(NaN)).toBe('$0.00')
  })

  it('does not pass through raw cents that look like dollars', () => {
    // The cents helper would render 738807 -> "$7,388.07". The dollars helper
    // renders the same number as $738,807.00. The two never share a call site.
    expect(formatDollars(738807)).toBe('$738,807.00')
  })
})
