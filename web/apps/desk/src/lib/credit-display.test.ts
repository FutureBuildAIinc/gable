// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The credit memo display and input helpers: stored quantities and totals are
 * negative and are never flipped in storage; a user types a positive "how many
 * come back" and string arithmetic carries it to the wire.
 */
import { describe, it, expect } from 'vitest'
import {
  addQuantities,
  creditQuantityFromInput,
  creditQuantityToInput,
  creditTextClass,
  formatCreditCents,
  formatQuantity,
  quantityToScaled,
  remainingCreditable,
  scaledToQuantity,
} from './credit-display'
import { formatDay } from './utils'

describe('credit quantities', () => {
  it('negates what a user types into the wire decimal string', () => {
    expect(creditQuantityFromInput('2')).toBe('-2')
    expect(creditQuantityFromInput('1.5')).toBe('-1.5')
    expect(creditQuantityFromInput(' 0.0625 ')).toBe('-0.0625')
  })

  it('refuses zero, negatives, text and too many fraction digits', () => {
    expect(creditQuantityFromInput('0')).toBeNull()
    expect(creditQuantityFromInput('-2')).toBeNull()
    expect(creditQuantityFromInput('two')).toBeNull()
    expect(creditQuantityFromInput('')).toBeNull()
    expect(creditQuantityFromInput('1.00001')).toBeNull()
  })

  it('shows a stored negative quantity as the positive text a user would type', () => {
    expect(creditQuantityToInput('-2')).toBe('2')
    expect(creditQuantityToInput('-1.5')).toBe('1.5')
    expect(creditQuantityToInput(null)).toBe('')
  })

  it('round trips through the scaled integer without a float', () => {
    for (const q of ['0.1', '-0.1', '24', '-24', '1234.5678', '-0.0001']) {
      expect(scaledToQuantity(quantityToScaled(q) as number)).toBe(q)
    }
    expect(scaledToQuantity(0)).toBe('0')
  })

  it('adds decimal strings exactly: 0.1 + 0.2 is 0.3', () => {
    expect(addQuantities('0.1', '0.2')).toBe('0.3')
    expect(addQuantities('24', '-2')).toBe('22')
    expect(addQuantities('x', '1')).toBeNull()
  })
})

describe('remainingCreditable', () => {
  it('is billed less the credits already written, which are negative', () => {
    expect(remainingCreditable('24', [])).toBe('24')
    expect(remainingCreditable('24', ['-2'])).toBe('22')
    expect(remainingCreditable('24', ['-2', '-0.5'])).toBe('21.5')
  })

  it('never goes below zero and has no figure for a note line', () => {
    expect(remainingCreditable('2', ['-3'])).toBe('0')
    expect(remainingCreditable(null, ['-1'])).toBeNull()
  })
})

describe('credit money display', () => {
  it('keeps the minus sign on a credit and marks it with the safety red token', () => {
    expect(formatCreditCents(-4400)).toBe('-$44.00')
    expect(creditTextClass(-4400)).toBe('text-red-400')
  })

  it('leaves positive and zero amounts unmarked and never shows -$0.00', () => {
    expect(creditTextClass(0)).toBe('')
    expect(creditTextClass(100)).toBe('')
    expect(formatCreditCents(0)).toBe('$0.00')
    expect(formatCreditCents(-0)).toBe('$0.00')
  })

  it('shows a missing quantity as a dash', () => {
    expect(formatQuantity(null)).toBe('—')
    expect(formatQuantity('-2')).toBe('-2')
  })
})

describe('formatDay', () => {
  it('renders a business date without a time zone shift', () => {
    expect(formatDay('2026-01-01')).toBe(new Date(2026, 0, 1).toLocaleDateString())
    expect(formatDay(null)).toBe('—')
    expect(formatDay('not a date')).toBe('not a date')
  })
})
