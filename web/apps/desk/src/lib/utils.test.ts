// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Money formatting on the ERP boundary.
 *
 * CLAUDE.md ("Money convention is not uniform across modules") documents a live
 * bug class: ERP endpoints (`/api/v1/orders`, `/api/v1/invoices`) serialize money
 * as int64 **cents**, while the portal serializes float64 **dollars**. Calling
 * `.toFixed(2)` on an ERP amount renders $73.88 as $7,388.07 — a 100x error on a
 * customer-facing invoice. `formatCents()` is the helper that has to hold that
 * line, so it gets the deepest tests in this suite.
 */
import { describe, it, expect } from 'vitest'
import { cn, formatCents } from './utils'

describe('test environment', () => {
  // formatCents() calls toLocaleString(undefined, ...) — it inherits the runtime's
  // default locale. Every exact-string expectation below assumes an en-US-style
  // locale ("," group separator, "." decimal separator), which is what Node
  // resolves to in CI. Asserting it here turns a confusing group-separator
  // mismatch into an obvious "your locale is not en-*" failure.
  it('resolves an en-* default locale', () => {
    expect(new Intl.NumberFormat().resolvedOptions().locale).toMatch(/^en\b/)
  })
})

describe('formatCents', () => {
  describe('the $73.88 -> $7,388.07 bug class (CLAUDE.md)', () => {
    it('renders an int64-cents amount as dollars, not as raw cents', () => {
      // 7388 cents is $73.88. The documented failure mode is rendering "$7,388.00".
      expect(formatCents(7388)).toBe('$73.88')
      expect(formatCents(7388)).not.toBe('$7,388.00')
    })

    it('renders the exact pair from the docs', () => {
      // The docs' example: the value that *should* print $73.88 prints $7,388.07
      // when a caller forgets the /100. These two calls must not be confusable.
      expect(formatCents(7388)).toBe('$73.88')
      expect(formatCents(738807)).toBe('$7,388.07')
    })

    it('is exactly 100x apart for inputs 100x apart', () => {
      expect(formatCents(1234)).toBe('$12.34')
      expect(formatCents(123400)).toBe('$1,234.00')
    })
  })

  describe('cents -> dollars conversion', () => {
    it('handles sub-dollar amounts', () => {
      expect(formatCents(1)).toBe('$0.01')
      expect(formatCents(9)).toBe('$0.09')
      expect(formatCents(99)).toBe('$0.99')
    })

    it('handles the dollar boundary', () => {
      expect(formatCents(100)).toBe('$1.00')
      expect(formatCents(101)).toBe('$1.01')
    })

    it('always renders exactly two decimal places', () => {
      expect(formatCents(500)).toBe('$5.00')
      expect(formatCents(550)).toBe('$5.50')
      expect(formatCents(555)).toBe('$5.55')
    })

    it('groups thousands', () => {
      expect(formatCents(100000)).toBe('$1,000.00')
      expect(formatCents(123456789)).toBe('$1,234,567.89')
    })
  })

  describe('zero and nullish', () => {
    it('renders zero', () => {
      expect(formatCents(0)).toBe('$0.00')
    })

    it('coerces null and undefined to zero rather than rendering "$NaN"', () => {
      expect(formatCents(null)).toBe('$0.00')
      expect(formatCents(undefined)).toBe('$0.00')
    })

    it('coerces non-finite numbers to zero', () => {
      expect(formatCents(NaN)).toBe('$0.00')
      expect(formatCents(Infinity)).toBe('$0.00')
      expect(formatCents(-Infinity)).toBe('$0.00')
    })
  })

  describe('large values', () => {
    it('formats an amount just under $10B without scientific notation', () => {
      expect(formatCents(999_999_999_999)).toBe('$9,999,999,999.99')
    })

    it('loses precision at the top of the safe-integer range', () => {
      // Documented, not endorsed: 9007199254740991 cents is $90,071,992,547,409.91,
      // but float64 division drops the trailing cent. Real LBM amounts are many
      // orders of magnitude below this, so this is a boundary note, not a defect
      // worth a refactor — it is asserted so a future BigInt/decimal migration
      // has to consciously update it.
      expect(formatCents(Number.MAX_SAFE_INTEGER)).toBe('$90,071,992,547,409.90')
    })
  })

  describe('rounding', () => {
    it('rounds fractional cents to the nearest cent', () => {
      // Fractional cents should never reach the UI (ERP money is int64), but the
      // signature accepts `number`, so pin the behavior.
      expect(formatCents(0.5)).toBe('$0.01')
      expect(formatCents(1.5)).toBe('$0.02')
      expect(formatCents(149)).toBe('$1.49')
    })
  })

  describe('negative amounts (credits, refunds, overpayments)', () => {
    it('keeps the magnitude correct for negative cents', () => {
      expect(formatCents(-7388)).toContain('73.88')
      expect(formatCents(-123456)).toContain('1,234.56')
    })

    // Regression: formatCents() used to build the string as `$` +
    // Number#toLocaleString(), so a negative amount rendered "$-73.88" instead of
    // the en-US convention "-$73.88". That reached users anywhere the ERP shows a
    // credit: an overpaid invoice (InvoiceDetail computes
    // `total_amount - totalPaid`), credit memos, and the Trial Balance difference
    // row. It now formats through Intl's `style: 'currency'`, which places the
    // sign outside the symbol.
    it('renders negative money as -$73.88, not $-73.88', () => {
      expect(formatCents(-7388)).toBe('-$73.88')
    })

    it('never renders a negative zero', () => {
      // -0 cents is zero; "-$0.00" on a balance row reads as a real credit.
      expect(formatCents(-0)).toBe('$0.00')
    })
  })
})

describe('cn', () => {
  it('joins class names', () => {
    expect(cn('flex', 'items-center')).toBe('flex items-center')
  })

  it('drops falsy values', () => {
    expect(cn('flex', false, null, undefined, '')).toBe('flex')
  })

  it('flattens arrays and conditional objects', () => {
    expect(cn(['rounded-lg', 'gap-2'], { hidden: false, 'font-bold': true }))
      .toBe('rounded-lg gap-2 font-bold')
  })

  it('resolves display conflicts introduced across an array and an object', () => {
    expect(cn(['flex', 'gap-2'], { block: true })).toBe('gap-2 block')
  })

  it('lets a later tailwind class win over an earlier conflicting one', () => {
    // This is the whole reason cn() wraps twMerge rather than just clsx.
    expect(cn('px-2', 'px-4')).toBe('px-4')
    expect(cn('text-white', 'text-gable-green')).toBe('text-gable-green')
  })

  it('keeps non-conflicting utilities from the same group', () => {
    expect(cn('px-4', 'py-2')).toBe('px-4 py-2')
  })
})
