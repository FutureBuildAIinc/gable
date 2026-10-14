// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The one date module's legs (G1 walk note N15): one style per locale (the
 * dealer's, with the month as a short word), a business day that never shifts
 * a day west of UTC, typed parsing of both accepted forms, the week the
 * locale starts, and the once at start locale read (desk route for the desk,
 * the portal's public config for the portal, en-US on any failure, quietly).
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  initDates,
  setDealerLocale,
  getDealerLocale,
  formatDay,
  formatDate,
  formatDateTime,
  parseDay,
  todayDay,
  weekStart,
  weekdayNames,
  monthName,
} from './dates'
import { jsonResponse } from '../test/dom'

const DAY = '2026-10-14'
const MOMENT = new Date(2026, 9, 14, 15, 4)

beforeEach(() => setDealerLocale('en-US'))
afterEach(() => {
  vi.unstubAllGlobals()
  window.history.replaceState(null, '', '/')
})

describe('one style per locale', () => {
  it('en-US', () => {
    setDealerLocale('en-US')
    expect(formatDay(DAY)).toBe('Oct 14, 2026')
    expect(formatDate(MOMENT)).toBe('Oct 14, 2026')
    expect(formatDateTime(MOMENT)).toBe('Oct 14, 2026, 3:04 PM')
  })
  it('en-CA', () => {
    setDealerLocale('en-CA')
    expect(formatDay(DAY)).toBe('Oct 14, 2026')
    expect(formatDate(MOMENT)).toBe('Oct 14, 2026')
    expect(formatDateTime(MOMENT)).toBe('Oct 14, 2026, 3:04 p.m.')
  })
  it('en-GB', () => {
    setDealerLocale('en-GB')
    expect(formatDay(DAY)).toBe('14 Oct 2026')
    expect(formatDate(MOMENT)).toBe('14 Oct 2026')
    expect(formatDateTime(MOMENT)).toBe('14 Oct 2026, 15:04')
  })
  it('never a numeric month first or day first form', () => {
    for (const tag of ['en-US', 'en-CA', 'en-GB', 'fr-CA']) {
      setDealerLocale(tag)
      const text = formatDay(DAY)
      expect(text).not.toMatch(/\d{1,2}\/\d{1,2}/)
      expect(text).toMatch(/[A-Za-z]{3}/)
    }
  })
})

describe('formatDay is a business day', () => {
  it('never shifts a day west of UTC', () => {
    const tz = process.env.TZ
    process.env.TZ = 'America/Los_Angeles'
    try {
      // The UTC parse of the same text lands on the day before out west;
      // the business day is built from its parts instead.
      expect(new Date('2026-01-15').getDate()).toBe(14)
      expect(formatDay('2026-01-15')).toBe('Jan 15, 2026')
    } finally {
      process.env.TZ = tz
    }
  })
  it('empty and non date pass through as before', () => {
    expect(formatDay(null)).toBe('—')
    expect(formatDay(undefined)).toBe('—')
    expect(formatDay('not a date')).toBe('not a date')
    // A moment is not a business day; it passes through (formatDate reads it).
    expect(formatDay('2026-10-14T09:00:00Z')).toBe('2026-10-14T09:00:00Z')
  })
})

describe('formatDate is a moment', () => {
  it('reads ISO text and Dates; the unreadable come back as sent', () => {
    expect(formatDate('2026-10-14T15:04:00')).toBe('Oct 14, 2026')
    expect(formatDate(null)).toBe('—')
    expect(formatDate('')).toBe('—')
    expect(formatDate('junk')).toBe('junk')
    expect(formatDateTime('2026-10-14T15:04:00')).toBe('Oct 14, 2026, 3:04 PM')
  })
})

describe('parseDay', () => {
  it('takes YYYY-MM-DD', () => {
    expect(parseDay('2026-10-14')).toBe(DAY)
    expect(parseDay(' 2026-10-14 ')).toBe(DAY)
    expect(parseDay('2026-02-30')).toBeNull()
    expect(parseDay('2026-13-01')).toBeNull()
  })
  it('takes the module style in the dealer locale', () => {
    setDealerLocale('en-US')
    expect(parseDay('Oct 14, 2026')).toBe(DAY)
    expect(parseDay('October 14, 2026')).toBe(DAY)
    expect(parseDay('14 Oct 2026')).toBe(DAY)
    setDealerLocale('en-GB')
    expect(parseDay('14 Oct 2026')).toBe(DAY)
    expect(parseDay('Oct 14, 2026')).toBe(DAY)
  })
  it('refuses what is not a day', () => {
    expect(parseDay('')).toBeNull()
    expect(parseDay('14 2026')).toBeNull()
    expect(parseDay('Oct 14')).toBeNull()
    expect(parseDay('Oct 2026 14 extra 9')).toBeNull()
    expect(parseDay('Smarch 14, 2026')).toBeNull()
  })
})

describe('the week and the grid words', () => {
  it('the week starts as the locale starts it', () => {
    setDealerLocale('en-US')
    expect(weekStart()).toBe(0)
    setDealerLocale('en-GB')
    expect(weekStart()).toBe(1)
  })
  it('weekday names follow the week start', () => {
    setDealerLocale('en-US')
    expect(weekdayNames()).toEqual(['S', 'M', 'T', 'W', 'T', 'F', 'S'])
    setDealerLocale('en-GB')
    expect(weekdayNames()).toEqual(['M', 'T', 'W', 'T', 'F', 'S', 'S'])
  })
  it('the grid heading names the month and year', () => {
    expect(monthName(10, 2026)).toBe('October 2026')
  })
  it('todayDay is the business day of now', () => {
    expect(todayDay()).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })
})

describe('initDates', () => {
  function stubLocale(body: unknown, ok = true) {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(ok ? jsonResponse(body) : Promise.resolve(new Response('no', { status: 500 })))),
    )
  }

  it('reads the desk route for a desk page', async () => {
    window.history.replaceState(null, '', '/erp/reports/ar-aging')
    stubLocale({ locale: 'en-CA' })
    await initDates()
    expect(getDealerLocale()).toBe('en-CA')
    expect(vi.mocked(fetch).mock.calls[0][0]).toContain('/api/v1/dealer-profile/locale')
  })

  it('reads the portal config for a portal page', async () => {
    window.history.replaceState(null, '', '/portal/orders')
    stubLocale({ locale: 'en-GB' })
    await initDates()
    expect(getDealerLocale()).toBe('en-GB')
    expect(vi.mocked(fetch).mock.calls[0][0]).toContain('/api/portal/v1/config')
  })

  it('falls back to en-US on failure, quietly', async () => {
    setDealerLocale('en-GB')
    stubLocale(null, false)
    await initDates()
    expect(getDealerLocale()).toBe('en-GB')
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('down'))))
    setDealerLocale('en-US')
    await initDates()
    expect(getDealerLocale()).toBe('en-US')
  })

  it('a tag Intl refuses leaves the default', async () => {
    setDealerLocale('en-US')
    stubLocale({ locale: 'not a locale' })
    await initDates()
    expect(getDealerLocale()).toBe('en-US')
  })
})
