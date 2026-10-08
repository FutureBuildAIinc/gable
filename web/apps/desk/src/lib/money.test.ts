// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest'
import {
  parseScaled,
  dollarsToCents,
  dollarsToTenThousandths,
  floatDollarsToTenThousandths,
  tenThousandthsToDecimal,
  tenThousandthsToInput,
  centsToInput,
  normalizeQuantity,
  numberToQuantity,
  extensionCents,
} from './money'

describe('dollarsToCents', () => {
  it('parses whole and fractional dollars with integer arithmetic', () => {
    expect(dollarsToCents('12')).toBe(1200)
    expect(dollarsToCents('12.5')).toBe(1250)
    expect(dollarsToCents('12.05')).toBe(1205)
    expect(dollarsToCents('.5')).toBe(50)
    expect(dollarsToCents('1.1')).toBe(110)
    expect(dollarsToCents('19.99')).toBe(1999)
  })

  it('rejects empty, signed, malformed and over-precise text', () => {
    expect(dollarsToCents('')).toBeNull()
    expect(dollarsToCents('.')).toBeNull()
    expect(dollarsToCents('-1')).toBeNull()
    expect(dollarsToCents('1e3')).toBeNull()
    expect(dollarsToCents('1.234')).toBeNull()
    expect(dollarsToCents('abc')).toBeNull()
  })
})

describe('dollarsToTenThousandths', () => {
  it('scales to four places', () => {
    expect(dollarsToTenThousandths('5.5')).toBe(55000)
    expect(dollarsToTenThousandths('1.3725')).toBe(13725)
    expect(dollarsToTenThousandths('0.0001')).toBe(1)
    expect(dollarsToTenThousandths('1.37251')).toBeNull()
  })
})

describe('parseScaled', () => {
  it('trims whitespace', () => {
    expect(parseScaled(' 3 ', 2)).toBe(300)
  })
})

describe('floatDollarsToTenThousandths', () => {
  it('converts a parsing API float at the boundary without float multiplication drift', () => {
    expect(floatDollarsToTenThousandths(1.1)).toBe(11000)
    expect(floatDollarsToTenThousandths(0.29)).toBe(2900)
    expect(floatDollarsToTenThousandths(5.5)).toBe(55000)
    expect(floatDollarsToTenThousandths(4.35)).toBe(43500)
  })

  it('maps junk to zero', () => {
    expect(floatDollarsToTenThousandths(NaN)).toBe(0)
    expect(floatDollarsToTenThousandths(-2)).toBe(0)
  })
})

describe('tenThousandthsToDecimal', () => {
  it('renders four fraction digits', () => {
    expect(tenThousandthsToDecimal(55000)).toBe('5.5000')
    expect(tenThousandthsToDecimal(1)).toBe('0.0001')
    expect(tenThousandthsToDecimal(0)).toBe('0.0000')
  })
})

describe('tenThousandthsToInput', () => {
  it('keeps at least two fraction digits', () => {
    expect(tenThousandthsToInput(55000)).toBe('5.50')
    expect(tenThousandthsToInput(13725)).toBe('1.3725')
    expect(tenThousandthsToInput(13720)).toBe('1.372')
    expect(tenThousandthsToInput(0)).toBe('0.00')
  })
})

describe('quantity strings', () => {
  it('normalises a typed quantity', () => {
    expect(normalizeQuantity('10')).toBe('10')
    expect(normalizeQuantity('12.50')).toBe('12.5')
    expect(normalizeQuantity('007')).toBe('7')
    expect(normalizeQuantity('0.0001')).toBe('0.0001')
  })

  it('requires a positive value of at most four fraction digits', () => {
    expect(normalizeQuantity('0')).toBeNull()
    expect(normalizeQuantity('')).toBeNull()
    expect(normalizeQuantity('1.23456')).toBeNull()
    expect(normalizeQuantity('-3')).toBeNull()
  })

  it('turns a parsing float into a decimal string', () => {
    expect(numberToQuantity(10)).toBe('10')
    expect(numberToQuantity(12.5)).toBe('12.5')
    expect(numberToQuantity(0)).toBeNull()
    expect(numberToQuantity(NaN)).toBeNull()
  })
})

describe('extensionCents', () => {
  it('multiplies quantity by unit price and rounds once to cents', () => {
    expect(extensionCents('10', 55000)).toBe(5500)
    expect(extensionCents('12.5', 55000)).toBe(6875)
    expect(extensionCents('3', 13725)).toBe(412) // 4.1175 dollars
    expect(extensionCents('1', 5)).toBe(0)
  })

  it('rounds half away from zero', () => {
    expect(extensionCents('1', 50)).toBe(1) // 0.005 dollars
    expect(extensionCents('1', -50)).toBe(-1)
    expect(extensionCents('1', 49)).toBe(0)
  })

  it('is zero for an unparseable quantity', () => {
    expect(extensionCents('x', 100)).toBe(0)
  })
})

describe('centsToInput', () => {
  it('renders two fraction digits', () => {
    expect(centsToInput(12500)).toBe('125.00')
    expect(centsToInput(5)).toBe('0.05')
  })
})
