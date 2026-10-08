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
  formatQuantity,
  isPositiveQuantity,
  isNegativeQuantity,
  scaleTenThousandths,
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

  it('applies the conversion pair: quantity x price x price_uom_qty / uom_qty', () => {
    // 187.5 pieces priced at 500.00 per MBF, 187.5 pieces to 1 MBF
    expect(extensionCents('187.5', 5000000, '187.5', '1')).toBe(50000)
    // one piece of the same lumber: 266.67 cents rounds to 267
    expect(extensionCents('1', 5000000, '187.5', '1')).toBe(267)
    // 1500 pieces at 3.75 per thousand
    expect(extensionCents('1500', 37500, '1000', '1')).toBe(563)
    // a 1 to 1 pair changes nothing
    expect(extensionCents('10', 55000, '1', '1')).toBe(5500)
  })

  it('treats a missing or unparseable pair as 1 to 1', () => {
    expect(extensionCents('10', 55000, undefined, undefined)).toBe(5500)
    expect(extensionCents('10', 55000, '0', '0')).toBe(5500)
    expect(extensionCents('10', 55000, 'x', '1')).toBe(5500)
  })
})

describe('centsToInput', () => {
  it('renders two fraction digits', () => {
    expect(centsToInput(12500)).toBe('125.00')
    expect(centsToInput(5)).toBe('0.05')
  })
})

describe('formatQuantity', () => {
  it('groups the whole part and drops trailing fraction zeros', () => {
    expect(formatQuantity('1234.5000')).toBe('1,234.5')
    expect(formatQuantity('10.0000')).toBe('10')
    expect(formatQuantity('0.2500')).toBe('0.25')
    expect(formatQuantity('1234567')).toBe('1,234,567')
    expect(formatQuantity('007.10')).toBe('7.1')
  })

  it('keeps a negative sign only on a non-zero value', () => {
    expect(formatQuantity('-3.0000')).toBe('-3')
    expect(formatQuantity('-0.0000')).toBe('0')
  })

  it('answers 0 for nothing and the text itself for non-decimals', () => {
    expect(formatQuantity(null)).toBe('0')
    expect(formatQuantity(undefined)).toBe('0')
    expect(formatQuantity('')).toBe('0')
    expect(formatQuantity('abc')).toBe('abc')
  })
})

describe('isPositiveQuantity and isNegativeQuantity', () => {
  it('tell positive, zero and negative decimal strings apart', () => {
    expect(isPositiveQuantity('0.0001')).toBe(true)
    expect(isPositiveQuantity('12')).toBe(true)
    expect(isPositiveQuantity('0')).toBe(false)
    expect(isPositiveQuantity('0.0000')).toBe(false)
    expect(isPositiveQuantity('-1')).toBe(false)
    expect(isPositiveQuantity('')).toBe(false)
    expect(isNegativeQuantity('-0.5')).toBe(true)
    expect(isNegativeQuantity('-0.0000')).toBe(false)
    expect(isNegativeQuantity('5')).toBe(false)
  })
})

describe('scaleTenThousandths', () => {
  it('takes a ratio of a price with one rounding, half away from zero', () => {
    expect(scaleTenThousandths(42500, 6, 10)).toBe(25500)
    expect(scaleTenThousandths(1, 1, 2)).toBe(1)
    expect(scaleTenThousandths(-1, 1, 2)).toBe(-1)
    expect(scaleTenThousandths(10, 1, 3)).toBe(3)
  })

  it('is zero for a zero denominator or a non-finite input', () => {
    expect(scaleTenThousandths(100, 1, 0)).toBe(0)
    expect(scaleTenThousandths(Number.NaN, 1, 2)).toBe(0)
  })
})
