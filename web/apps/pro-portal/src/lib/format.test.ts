// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest'
import { formatBps, formatCents, formatDollars, NO_FIGURE, quantityWithUnit, statusLabel, timeOfDay } from './format.ts'

describe('money formatting', () => {
  it('formats integer cents without treating them as dollars', () => {
    expect(formatCents(7388)).toBe('$73.88')
    expect(formatCents(-125050)).toBe('-$1,250.50')
    expect(formatCents(0)).toBe('$0.00')
  })

  it('keeps the legacy dollar routes on their own formatter', () => {
    expect(formatDollars(73.88)).toBe('$73.88')
    expect(formatDollars(null)).toBe(NO_FIGURE)
  })

  it('formats basis points and quantities for display only', () => {
    expect(formatBps(1850)).toBe('18.5%')
    expect(quantityWithUnit('12.5000', 'EA')).toBe('12.5000 EA')
    expect(statusLabel('OUT_FOR_DELIVERY')).toBe('Out for delivery')
  })

  it('uses twenty four hour times', () => {
    const value = new Date().toISOString()
    expect(timeOfDay(value, 'UTC')).toMatch(/^\d{2}:\d{2}$/)
  })
})
