// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest'
import * as money from './money'

// One helper prints dollars for the card and the Daily Till alike: the sign
// goes before the dollar mark, "-$5.00", never "$-5.00".
describe('formatDollars and formatCents', () => {
  it('puts the sign before the dollar mark', () => {
    const { formatDollars } = money as unknown as { formatDollars: (n: number) => string }
    expect(formatDollars(-5)).toBe('-$5.00')
    expect(formatDollars(1234.5)).toBe('$1,234.50')
    expect(formatDollars(0)).toBe('$0.00')
  })

  it('reads cents the same way', () => {
    const { formatCents } = money as unknown as { formatCents: (n: number) => string }
    expect(formatCents(-500)).toBe('-$5.00')
    expect(formatCents(12345)).toBe('$123.45')
  })
})
